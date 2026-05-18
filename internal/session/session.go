// Package session manages Claude Session records and their bindings to bots and processes.
package session

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"linkcode/internal/store"
)

// ErrNotFound is returned when a session lookup fails.
var ErrNotFound = errors.New("session: not found")

// Session is the application-level representation of a session.
type Session struct {
	ID              int64
	Name            string
	AgentType       string
	ProcessStatus   store.ProcessStatus
	ClaudeSessionID string
	BoundBotID      int64
	CreatedAt       time.Time
	LastActiveAt    time.Time
}

// Manager handles session lifecycle.
type Manager struct {
	db *store.DB
}

// New creates a new Manager.
func New(db *store.DB) *Manager {
	return &Manager{db: db}
}

// Create inserts a new session and binds a bot to it.
func (m *Manager) Create(name, agentType, claudeSessionID string, botInternalID int64) (*Session, error) {
	id, err := m.db.InsertSession(name, agentType, claudeSessionID, botInternalID)
	if err != nil {
		return nil, fmt.Errorf("session: create: %w", err)
	}
	return m.Get(id)
}

// Get retrieves a session by its internal ID.
func (m *Manager) Get(id int64) (*Session, error) {
	r, err := m.db.GetSessionByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return fromRecord(r), nil
}

// GetByBot retrieves the session bound to a given bot internal ID.
func (m *Manager) GetByBot(botInternalID int64) (*Session, error) {
	r, err := m.db.GetSessionByBotID(botInternalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return fromRecord(r), nil
}

// GetByPlatformBotID retrieves the session bound to a platform bot_id string.
func (m *Manager) GetByPlatformBotID(platformBotID string) (*Session, error) {
	r, err := m.db.GetSessionByPlatformBotID(platformBotID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return fromRecord(r), nil
}

// GetByClaudeID retrieves a session by its Claude session identifier.
func (m *Manager) GetByClaudeID(claudeID string) (*Session, error) {
	r, err := m.db.GetSessionByClaudeID(claudeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return fromRecord(r), nil
}

// ListActive returns all sessions that have an active (waked or sleeped) process.
func (m *Manager) ListActive() ([]Session, error) {
	records, err := m.db.ListActiveSessions()
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(records))
	for _, r := range records {
		sessions = append(sessions, *fromRecord(&r))
	}
	return sessions, nil
}

// SetClaudeSessionID updates the claude session identifier for a session.
func (m *Manager) SetClaudeSessionID(sessionID int64, claudeSessionID string) error {
	return m.db.UpdateClaudeSessionID(sessionID, claudeSessionID)
}

// MarkWaked sets the process status to waked.
func (m *Manager) MarkWaked(sessionID int64) error {
	return m.db.UpdateSessionStatus(sessionID, store.ProcessWaked)
}

// MarkSleeped sets the process status to sleeped.
func (m *Manager) MarkSleeped(sessionID int64) error {
	return m.db.UpdateSessionStatus(sessionID, store.ProcessSleeped)
}

// Touch updates the last_active_at timestamp.
func (m *Manager) Touch(sessionID int64) error {
	return m.db.TouchSession(sessionID)
}

// Delete removes a session and its messages.
func (m *Manager) Delete(sessionID int64) error {
	if err := m.db.DeleteMessagesBySession(sessionID); err != nil {
		return err
	}
	return m.db.DeleteSession(sessionID)
}

// AddMessage persists a chat message for a session.
func (m *Manager) AddMessage(sessionID int64, role store.MessageRole, content, contentType string) error {
	_, err := m.db.InsertMessage(sessionID, role, content, contentType)
	return err
}

// GetMessages retrieves the message history for a session.
func (m *Manager) GetMessages(sessionID int64, limit int) ([]store.MessageRecord, error) {
	return m.db.ListMessages(sessionID, limit)
}

func fromRecord(r *store.SessionRecord) *Session {
	botID := int64(0)
	if r.BoundBotID.Valid {
		botID = r.BoundBotID.Int64
	}
	claudeSID := ""
	if r.ClaudeSessionID.Valid {
		claudeSID = r.ClaudeSessionID.String
	}
	return &Session{
		ID:              r.ID,
		Name:            r.Name,
		AgentType:       r.AgentType,
		ProcessStatus:   r.ProcessStatus,
		ClaudeSessionID: claudeSID,
		BoundBotID:      botID,
		CreatedAt:       r.CreatedAt,
		LastActiveAt:    r.LastActiveAt,
	}
}
