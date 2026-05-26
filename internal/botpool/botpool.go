// Package botpool manages pre-created IM Bot credentials.
// In the simplified model, each bot is always bound to a session
// (one bot = one agent). There is no idle pool or allocate/release cycle.
package botpool

import (
	"database/sql"
	"errors"
	"fmt"
	"linkcode/internal/crypto"
	"linkcode/internal/store"
)

// Bot represents a bot with its decrypted secret.
type Bot struct {
	ID     int64
	BotID  string
	Name   string
	Secret string // decrypted
	Status store.BotStatus
}

// Pool manages bot lifecycle.
type Pool struct {
	db     *store.DB
	encKey string
}

// New creates a new Pool.
func New(db *store.DB, encKey string) *Pool {
	return &Pool{db: db, encKey: encKey}
}

// Add validates and stores a new bot credential. The secret is encrypted before storage.
func (p *Pool) Add(botID, name, secret string) (*Bot, error) {
	encrypted, err := crypto.Encrypt(secret, p.encKey)
	if err != nil {
		return nil, fmt.Errorf("botpool: encrypt secret: %w", err)
	}

	id, err := p.db.InsertBot(botID, name, encrypted)
	if err != nil {
		return nil, fmt.Errorf("botpool: insert bot: %w", err)
	}

	return &Bot{ID: id, BotID: botID, Name: name, Secret: secret, Status: store.BotIdle}, nil
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
			ID:     r.ID,
			BotID:  r.BotID,
			Name:   r.BotName,
			Secret: secret,
			Status: r.Status,
		})
		_ = err
	}
	return bots, nil
}

// Remove deletes a bot from the pool.
func (p *Pool) Remove(botInternalID int64) error {
	return p.db.DeleteBot(botInternalID)
}

func (p *Pool) recordToBot(record *store.BotRecord) (*Bot, error) {
	secret, err := crypto.Decrypt(record.BotSecretEncrypted, p.encKey)
	if err != nil {
		return nil, fmt.Errorf("botpool: decrypt secret: %w", err)
	}
	return &Bot{
		ID:     record.ID,
		BotID:  record.BotID,
		Name:   record.BotName,
		Secret: secret,
		Status: record.Status,
	}, nil
}
