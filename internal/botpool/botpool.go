// Package botpool manages pre-created IM Bot credentials.
// In the simplified model, each bot is always bound to a session
// (one bot = one agent). There is no idle pool or allocate/release cycle.
package botpool

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"linkcode/internal/crypto"
	"linkcode/internal/store"
)

// Bot represents a bot with its decrypted secret.
type Bot struct {
	ID      int64
	BotID   string
	Name    string
	WorkDir string
	Secret  string // decrypted
	Status  store.BotStatus
}

// Pool manages bot lifecycle.
type Pool struct {
	db              *store.DB
	encKey          string
	configWorkDir   string // fallback from config file
}

// New creates a new Pool.
func New(db *store.DB, encKey, configWorkDir string) *Pool {
	return &Pool{db: db, encKey: encKey, configWorkDir: configWorkDir}
}

// Add validates and stores a new bot credential. The secret is encrypted before storage.
func (p *Pool) Add(botID, name, workDir, secret string) (*Bot, error) {
	encrypted, err := crypto.Encrypt(secret, p.encKey)
	if err != nil {
		return nil, fmt.Errorf("botpool: encrypt secret: %w", err)
	}

	id, err := p.db.InsertBot(botID, name, workDir, encrypted)
	if err != nil {
		return nil, fmt.Errorf("botpool: insert bot: %w", err)
	}

	return &Bot{ID: id, BotID: botID, Name: name, WorkDir: workDir, Secret: secret, Status: store.BotIdle}, nil
}

// GetByPlatformBotID looks up a bot by its platform bot_id and returns it with decrypted secret.
// Returns (nil, nil) when no bot is found.
func (p *Pool) GetByPlatformBotID(platformBotID string) (*Bot, error) {
	record, err := p.db.GetBotByBotID(platformBotID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p.recordToBot(record)
}

// GetByID looks up a bot by internal ID.
func (p *Pool) GetByID(id int64) (*Bot, error) {
	record, err := p.db.GetBotByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p.recordToBot(record)
}

// BindToSession binds a specific bot to a session. The bot must be idle.
func (p *Pool) BindToSession(botInternalID, sessionID int64) error {
	if err := p.db.BindBot(botInternalID, sessionID); err != nil {
		return fmt.Errorf("botpool: bind to session: %w", err)
	}
	return nil
}

// Release returns a bot to the idle pool without closing any WebSocket.
func (p *Pool) Release(botInternalID int64) error {
	return p.db.ReleaseBot(botInternalID)
}

// List returns all bots in the pool.
func (p *Pool) List() ([]Bot, error) {
	records, err := p.db.ListBots()
	if err != nil {
		return nil, err
	}
	bots := make([]Bot, 0, len(records))
	for _, r := range records {
		secret, err := crypto.Decrypt(r.BotSecretEncrypted, p.encKey)
		if err != nil {
			secret = "<decrypt error>"
		}
		bots = append(bots, Bot{
			ID:      r.ID,
			BotID:   r.BotID,
			Name:    r.BotName,
			WorkDir: r.WorkDir,
			Secret:  secret,
			Status:  r.Status,
		})
		_ = err
	}
	return bots, nil
}

// UpdateWorkDir sets the working directory for a bot.
func (p *Pool) UpdateWorkDir(botID int64, workDir string) error {
	return p.db.UpdateBotWorkDir(botID, workDir)
}

// DirExists checks whether a directory exists and is accessible.
func DirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// DB exposes the underlying store for settings access.
func (p *Pool) DB() *store.DB { return p.db }

// ResolveWorkDir resolves the working directory for starting an agent process.
// Resolution order: botWorkDir → DB setting(default_work_dir) → config fallback → os.Getwd().
// Returns (resolvedPath, sourceDescription).
func (p *Pool) ResolveWorkDir(botWorkDir string) (string, string) {
	if botWorkDir != "" && DirExists(botWorkDir) {
		return botWorkDir, fmt.Sprintf("Agent 设定 (%s)", botWorkDir)
	}

	dbSetting, _ := p.db.GetSetting("default_work_dir")
	if dbSetting != "" && DirExists(dbSetting) {
		return dbSetting, fmt.Sprintf("全局默认 (%s)", dbSetting)
	}

	if p.configWorkDir != "" && DirExists(p.configWorkDir) {
		return p.configWorkDir, fmt.Sprintf("配置文件 (%s)", p.configWorkDir)
	}

	wd, _ := os.Getwd()
	return wd, fmt.Sprintf("当前进程目录 (%s)", wd)
}

func (p *Pool) recordToBot(record *store.BotRecord) (*Bot, error) {
	secret, err := crypto.Decrypt(record.BotSecretEncrypted, p.encKey)
	if err != nil {
		return nil, fmt.Errorf("botpool: decrypt secret: %w", err)
	}
	return &Bot{
		ID:      record.ID,
		BotID:   record.BotID,
		Name:    record.BotName,
		WorkDir: record.WorkDir,
		Secret:  secret,
		Status:  record.Status,
	}, nil
}
