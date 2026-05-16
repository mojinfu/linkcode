// Package botpool manages the pool of pre-created IM Bot credentials.
// Bots are added via /addbot command, stored encrypted, and allocated to sessions on demand.
package botpool

import (
	"database/sql"
	"errors"
	"fmt"
	"linkcode/internal/crypto"
	"linkcode/internal/store"
)

// ErrNoIdleBots is returned when the pool has no available bots.
var ErrNoIdleBots = errors.New("botpool: no idle bots available")

// Bot represents a bot in the pool with its decrypted secret.
type Bot struct {
	ID      int64
	BotID   string
	Name    string
	Secret  string // decrypted
	Status  store.BotStatus
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

// Allocate picks an idle bot, binds it to a session, and returns it with decrypted secret.
func (p *Pool) Allocate(sessionID int64) (*Bot, error) {
	bots, err := p.db.ListIdleBots()
	if err != nil {
		return nil, fmt.Errorf("botpool: list idle: %w", err)
	}
	if len(bots) == 0 {
		return nil, ErrNoIdleBots
	}

	// Pick the least recently used idle bot.
	record := &bots[0]

	secret, err := crypto.Decrypt(record.BotSecretEncrypted, p.encKey)
	if err != nil {
		return nil, fmt.Errorf("botpool: decrypt secret: %w", err)
	}

	if err := p.db.BindBot(record.ID, sessionID); err != nil {
		return nil, fmt.Errorf("botpool: bind bot: %w", err)
	}

	return &Bot{
		ID:     record.ID,
		BotID:  record.BotID,
		Name:   record.BotName,
		Secret: secret,
		Status: store.BotBound,
	}, nil
}

// Release returns a bot to the idle pool.
func (p *Pool) Release(botInternalID int64) error {
	return p.db.ReleaseBot(botInternalID)
}

// GetBotBySession returns the bot bound to a given session, or nil if none.
func (p *Pool) GetBotBySession(botInternalID int64) (*Bot, error) {
	record, err := p.db.GetBotByID(botInternalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

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

// Remove deletes a bot from the pool. Cannot remove a bound bot.
func (p *Pool) Remove(botInternalID int64) error {
	return p.db.DeleteBot(botInternalID)
}
