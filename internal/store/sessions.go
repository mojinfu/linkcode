package store

import (
	"database/sql"
	"time"
)

// ProcessStatus represents the state of an agent process.
type ProcessStatus string

const (
	ProcessWaked   ProcessStatus = "waked"
	ProcessSleeped ProcessStatus = "sleeped"
)

// SessionRecord is a row from the sessions table.
type SessionRecord struct {
	ID              int64
	Name            string
	AgentType       string
	ProcessStatus   ProcessStatus
	ClaudeSessionID sql.NullString
	BoundBotID      sql.NullInt64
	TotalCost       float64
	CreatedAt       time.Time
	LastActiveAt    time.Time
}

const sessionsColumns = `id, name, agent_type, process_status, claude_session_id, bound_bot_id, total_cost, created_at, last_active_at`

func scanSession(r *SessionRecord, sc scanner) error {
	return sc.Scan(&r.ID, &r.Name, &r.AgentType, &r.ProcessStatus, &r.ClaudeSessionID, &r.BoundBotID, &r.TotalCost, &r.CreatedAt, &r.LastActiveAt)
}

type scanner interface {
	Scan(dest ...any) error
}

// InsertSession creates a new session record.
func (db *DB) InsertSession(name, agentType, claudeSessionID string, boundBotID int64) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO sessions (name, agent_type, process_status, claude_session_id, bound_bot_id) VALUES (?, ?, ?, ?, NULLIF(?, 0))`,
		name, agentType, string(ProcessWaked), claudeSessionID, boundBotID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetSessionByID looks up a session by its internal id.
func (db *DB) GetSessionByID(id int64) (*SessionRecord, error) {
	r := &SessionRecord{}
	err := db.QueryRow(`SELECT `+sessionsColumns+` FROM sessions WHERE id = ?`, id).Scan(
		&r.ID, &r.Name, &r.AgentType, &r.ProcessStatus, &r.ClaudeSessionID, &r.BoundBotID, &r.TotalCost, &r.CreatedAt, &r.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GetSessionByBotID looks up the session bound to a given bot (internal id).
func (db *DB) GetSessionByBotID(botInternalID int64) (*SessionRecord, error) {
	r := &SessionRecord{}
	err := db.QueryRow(`SELECT `+sessionsColumns+` FROM sessions WHERE bound_bot_id = ?`, botInternalID).Scan(
		&r.ID, &r.Name, &r.AgentType, &r.ProcessStatus, &r.ClaudeSessionID, &r.BoundBotID, &r.TotalCost, &r.CreatedAt, &r.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GetSessionByPlatformBotID looks up the session bound to a given platform bot_id.
func (db *DB) GetSessionByPlatformBotID(platformBotID string) (*SessionRecord, error) {
	r := &SessionRecord{}
	err := db.QueryRow(
		`SELECT s.id, s.name, s.agent_type, s.process_status, s.claude_session_id, s.bound_bot_id, s.total_cost, s.created_at, s.last_active_at
		 FROM sessions s JOIN bots b ON s.bound_bot_id = b.id
		 WHERE b.bot_id = ?`,
		platformBotID,
	).Scan(&r.ID, &r.Name, &r.AgentType, &r.ProcessStatus, &r.ClaudeSessionID, &r.BoundBotID, &r.TotalCost, &r.CreatedAt, &r.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GetSessionByClaudeID looks up the session by the Claude session identifier.
func (db *DB) GetSessionByClaudeID(claudeSessionID string) (*SessionRecord, error) {
	r := &SessionRecord{}
	err := db.QueryRow(`SELECT `+sessionsColumns+` FROM sessions WHERE claude_session_id = ?`, claudeSessionID).Scan(
		&r.ID, &r.Name, &r.AgentType, &r.ProcessStatus, &r.ClaudeSessionID, &r.BoundBotID, &r.TotalCost, &r.CreatedAt, &r.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListActiveSessions returns sessions with waked or sleeped processes.
func (db *DB) ListActiveSessions() ([]SessionRecord, error) {
	rows, err := db.Query(
		`SELECT `+sessionsColumns+` FROM sessions WHERE process_status IN (?, ?) ORDER BY last_active_at DESC`,
		string(ProcessWaked), string(ProcessSleeped),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionRecord
	for rows.Next() {
		var r SessionRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.AgentType, &r.ProcessStatus, &r.ClaudeSessionID, &r.BoundBotID, &r.TotalCost, &r.CreatedAt, &r.LastActiveAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, r)
	}
	return sessions, rows.Err()
}

// UpdateSessionStatus changes the process status and bumps last_active_at.
func (db *DB) UpdateSessionStatus(sessionID int64, status ProcessStatus) error {
	_, err := db.Exec(
		`UPDATE sessions SET process_status = ?, last_active_at = NOW() WHERE id = ?`,
		string(status), sessionID,
	)
	return err
}

// UpdateClaudeSessionID saves the Claude-side session UUID.
func (db *DB) UpdateClaudeSessionID(sessionID int64, claudeSessionID string) error {
	_, err := db.Exec(`UPDATE sessions SET claude_session_id = ? WHERE id = ?`, claudeSessionID, sessionID)
	return err
}

// TouchSession updates last_active_at to now.
func (db *DB) TouchSession(sessionID int64) error {
	_, err := db.Exec(`UPDATE sessions SET last_active_at = NOW() WHERE id = ?`, sessionID)
	return err
}

// UpdateSessionCost persists the accumulated cost for a session.
func (db *DB) UpdateSessionCost(sessionID int64, cost float64) error {
	_, err := db.Exec(`UPDATE sessions SET total_cost = ? WHERE id = ?`, cost, sessionID)
	return err
}

// DeleteSession removes a session record.
func (db *DB) DeleteSession(sessionID int64) error {
	_, err := db.Exec(`DELETE FROM sessions WHERE id = ?`, sessionID)
	return err
}
