// LinkCode - Remote Agent wakeup and binding system via IM bots.
//
// Usage:
//
//	linkcode -config configs/linkcode.yaml
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"linkcode/configs"
	"linkcode/internal/admin"
	"linkcode/internal/agent/claude"
	"linkcode/internal/botpool"
	"linkcode/internal/channel"
	"linkcode/internal/channel/wecom"
	"linkcode/internal/controller"
	"linkcode/internal/crypto"
	"linkcode/internal/gateway"
	"linkcode/internal/pricing"
	"linkcode/internal/procman"
	"linkcode/internal/router"
	"linkcode/internal/session"
	"linkcode/internal/store"
)

const pidFile = "bin/.linkcode.pid"

// acquirePidFile checks for an existing linkcode process via a PID file.
// If a running process is found, it returns an error to prevent duplicate startup.
// If the PID file is stale (process no longer exists), it is cleaned up.
func acquirePidFile() error {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // no existing PID file, safe to start
		}
		return fmt.Errorf("read pid file: %w", err)
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		// Corrupted PID file, remove and continue.
		_ = os.Remove(pidFile)
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		// PID not valid on this platform, remove stale file.
		_ = os.Remove(pidFile)
		return nil
	}

	// Signal 0 checks if we can send a signal to the process (i.e., it exists).
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		return fmt.Errorf("linkcode is already running (pid %d).\n  Stop it first with: kill %d\n  Or remove the pid file: rm %s", pid, pid, pidFile)
	}

	// Process not running, clean up stale PID file.
	_ = os.Remove(pidFile)
	return nil
}

func writePidFile() error {
	return os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
}

func releasePidFile() {
	_ = os.Remove(pidFile)
}

func main() {
	configPath := flag.String("config", "configs/linkcode.yaml", "Path to YAML configuration file")
	flag.Parse()

	// Prevent duplicate instances.
	if err := acquirePidFile(); err != nil {
		log.Fatalf("startup: %v", err)
	}

	// Load configuration.
	cfg, err := configs.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Ensure encryption key exists and persist it.
	if cfg.EncryptKey == "" {
		key, err := crypto.GenerateKey()
		if err != nil {
			log.Fatalf("generate encryption key: %v", err)
		}
		cfg.EncryptKey = key
		// Save key to a local file so it survives restarts.
		if err := os.WriteFile("configs/.encrypt_key", []byte(key), 0600); err != nil {
			log.Printf("WARNING: could not save encrypt key: %v", err)
		} else {
			log.Printf("encryption key saved to configs/.encrypt_key")
		}
	}

	// Connect to MySQL.
	db, err := store.Open(cfg.DB.DSN, cfg.DB.MaxOpenConns, cfg.DB.MaxIdleConns)
	if err != nil {
		log.Fatalf("connect to MySQL: %v", err)
	}
	defer db.Close()
	log.Println("MySQL connected")

	// Run migrations.
	migrationSQL, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		log.Fatalf("read migration 001: %v", err)
	}
	if err := db.RunMigrations(string(migrationSQL)); err != nil {
		log.Fatalf("run migration 001: %v", err)
	}
	migrationV2, err := os.ReadFile("migrations/002_work_dir.sql")
	if err != nil {
		log.Fatalf("read migration 002: %v", err)
	}
	if err := db.RunMigrations(string(migrationV2)); err != nil {
		log.Fatalf("run migration 002: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE bots ADD COLUMN work_dir VARCHAR(1024) DEFAULT '' AFTER bot_name`); err != nil {
		if strings.Contains(err.Error(), "Duplicate column") {
			log.Printf("migration note: work_dir column already exists, skipped")
		} else {
			log.Fatalf("add work_dir column: %v", err)
		}
	}
	migrationV3, err := os.ReadFile("migrations/003_total_cost.sql")
	if err != nil {
		log.Fatalf("read migration 003: %v", err)
	}
	if err := db.RunMigrations(string(migrationV3)); err != nil {
		if strings.Contains(err.Error(), "Duplicate column") {
			log.Printf("migration note: total_cost column already exists, skipped")
		} else {
			log.Fatalf("run migration 003: %v", err)
		}
	}
	log.Println("migrations applied")

	// Report claude subprocess auth/endpoint env sources (file config > system env > missing).
	procman.LogClaudeEnv(cfg.Agent.Env)

	// Initialize layers.
	botPool := botpool.New(db, cfg.EncryptKey, cfg.Agent.DefaultWorkDir)
	sessionMgr := session.New(db)
	agentRunner := claude.NewRunner(cfg.Agent.ClaudeCodePath, cfg.Agent.Env)
	imStyler := wecom.WeComStyler{}

	// Create control bot channel.
	ctrlChan := wecom.New(cfg.ControlBot.BotID, cfg.ControlBot.Secret)

	// Create gateway (manages all bot channels).
	gw := gateway.New(ctrlChan)

	// Initialize controller and router.
	priceCalc := pricing.New(cfg.Agent.Pricing)
	statusMgr := router.NewStatusManager(gw, sessionMgr)
	rtr := router.New(sessionMgr, botPool, agentRunner, gw, statusMgr, imStyler, priceCalc)
	ctrl := controller.New(sessionMgr, botPool, gw, imStyler, cfg.Agent.ClaudeCodePath, rtr, rtr)

	// Wire worker bot handlers globally via gateway.
	gw.SetWorkerMessageHandler(func(msg channel.Message) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[main] worker message handler panic: %v", r)
			}
		}()
		rtr.HandleWorkerMessage(msg)
	})
	gw.SetWorkerEventHandler(func(msg channel.Message) {
		rtr.HandleWorkerEvent(msg)
	})
	gw.SetConnectionChangeHandler(statusMgr.HandleConnectionChange)

	// Wire control bot message handling.
	ctrlChan.OnMessage(func(msg channel.Message) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[main] control message handler panic: %v", r)
			}
		}()
		ctx := context.Background()
		reply := ctrl.HandleMessage(ctx, msg)
		if reply != "" {
			if err := ctrlChan.SendMessage(ctx, msg.UserID, channel.MessageContent{
				Text:      reply,
				ReplyToID: msg.ID,
				ChatID:    msg.ChatID,
			}); err != nil {
				log.Printf("[main] send control reply: %v", err)
			}
		}
	})

	// Connect control bot to WeCom.
	ctx := context.Background()
	if err := ctrlChan.Connect(ctx); err != nil {
		log.Fatalf("connect control bot: %v", err)
	}
	defer ctrlChan.Close()
	log.Printf("control bot %s connected to WeCom", cfg.ControlBot.BotID)

	// Reconnect worker bots for all bound sessions on startup.
	activeSessions, _ := sessionMgr.ListActive()
	for _, s := range activeSessions {
		if s.BoundBotID > 0 {
			bot, err := botPool.GetByID(s.BoundBotID)
			if err != nil || bot == nil {
				log.Printf("[main] skip session %d: bot %d not found", s.ID, s.BoundBotID)
				continue
			}
			if err := gw.OpenWorkerChannel(ctx, bot.ID, bot.BotID, bot.Secret); err != nil {
				log.Printf("[main] reconnect worker bot %d: %v", bot.ID, err)
				continue
			}
			log.Printf("[main] reconnected worker bot %s for session %d (%s)", bot.BotID, s.ID, s.Name)
		}
	}

	// Start admin panel.
	if cfg.Admin.Enabled {
		adminSrv := admin.New(cfg.Admin.BindAddr, sessionMgr, botPool, rtr)
		go func() {
			log.Printf("admin panel: http://%s", cfg.Admin.BindAddr)
			if err := adminSrv.Start(); err != nil {
				log.Printf("admin server: %v", err)
			}
		}()
	}

	if err := writePidFile(); err != nil {
		log.Printf("WARNING: could not write pid file: %v", err)
	} else {
		log.Printf("pid %d written to %s", os.Getpid(), pidFile)
	}

	log.Println("LinkCode is running. Press Ctrl+C to stop.")

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down...")
	gw.CloseAll()
	releasePidFile()
	fmt.Println("LinkCode stopped.")
}
