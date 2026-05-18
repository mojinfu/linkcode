// LinkCode - Remote Agent wakeup and binding system via IM bots.
//
// Usage:
//
//	linkcode -config configs/linkcode.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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
	"linkcode/internal/router"
	"linkcode/internal/session"
	"linkcode/internal/store"
)

func main() {
	configPath := flag.String("config", "configs/linkcode.yaml", "Path to YAML configuration file")
	flag.Parse()

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
		log.Fatalf("read migration: %v", err)
	}
	if err := db.RunMigrations(string(migrationSQL)); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	log.Println("migrations applied")

	// Initialize layers.
	botPool := botpool.New(db, cfg.EncryptKey)
	sessionMgr := session.New(db)
	agentRunner := claude.NewRunner(cfg.Agent.ClaudeCodePath, cfg.Agent.ClaudeWorkDir)

	// Create control bot channel.
	ctrlChan := wecom.New(cfg.ControlBot.BotID, cfg.ControlBot.Secret)

	// Create gateway (manages all bot channels).
	gw := gateway.New(ctrlChan)

	// Initialize controller and router.
	ctrl := controller.New(sessionMgr, botPool, agentRunner, gw)
	rtr := router.New(sessionMgr, botPool, agentRunner, gw)

	// Wire worker bot handlers globally via gateway.
	gw.SetWorkerMessageHandler(func(msg channel.Message) {
		rtr.HandleWorkerMessage(msg)
	})
	gw.SetWorkerEventHandler(func(msg channel.Message) {
		rtr.HandleWorkerEvent(msg)
	})

	// Wire control bot message handling.
	ctrlChan.OnMessage(func(msg channel.Message) {
		ctx := context.Background()
		reply := ctrl.HandleMessage(ctx, msg)
		if reply != "" {
			if err := ctrlChan.SendMessage(ctx, msg.UserID, channel.MessageContent{
				Text:          reply,
				ReplyToID:     msg.ID,
				OriginalReqID: msg.ReqID,
				ChatID:        msg.ChatID,
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

	// Reconnect worker bots for active (waked) sessions on startup.
	activeSessions, _ := sessionMgr.ListActive()
	for _, s := range activeSessions {
		if s.ProcessStatus == "waked" && s.BoundBotID > 0 {
			bot, err := botPool.GetBotBySession(s.BoundBotID)
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
		adminSrv := admin.New(cfg.Admin.BindAddr, sessionMgr, botPool)
		go func() {
			log.Printf("admin panel: http://%s", cfg.Admin.BindAddr)
			if err := adminSrv.Start(); err != nil {
				log.Printf("admin server: %v", err)
			}
		}()
	}

	log.Println("LinkCode is running. Press Ctrl+C to stop.")

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down...")
	gw.CloseAll()
	fmt.Println("LinkCode stopped.")
}
