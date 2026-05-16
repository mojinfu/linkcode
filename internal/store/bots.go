package store

import (
	"database/sql"
	"time"
)

// BotStatus represents the bind state of a bot in the pool.
type BotStatus string

const (
	BotIdle        BotStatus = "idle"
	BotBound       BotStatus = "bound"
	BotUnavailable BotStatus = "unavailable"
)

// BotRecord is a row from the bots table.
type BotRecord struct {
	ID               int64
	BotID            string
	BotName          string
	BotSecretEncrypted []byte
	Status           BotStatus
	BoundSessionID   sql.NullInt64
	CreatedAt        time.Time
	LastUsedAt       sql.NullTime
}

// InsertBot adds a new bot to the pool.
func (db *DB) InsertBot(botID, name string, encryptedSecret []byte) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO bots (bot_id, bot_name, bot_secret_encrypted, status) VALUES (?, ?, ?, ?)`,
		botID, name, encryptedSecret, string(BotIdle),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetBotByBotID looks up a bot by its platform bot_id.
func (db *DB) GetBotByBotID(botID string) (*BotRecord, error) {
	r := &BotRecord{}
	err := db.QueryRow(
		`SELECT id, bot_id, bot_name, bot_secret_encrypted, status, bound_session_id, created_at, last_used_at FROM bots WHERE bot_id = ?`,
		botID,
	).Scan(&r.ID, &r.BotID, &r.BotName, &r.BotSecretEncrypted, &r.Status, &r.BoundSessionID, &r.CreatedAt, &r.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GetBotByID looks up a bot by internal id.
func (db *DB) GetBotByID(id int64) (*BotRecord, error) {
	r := &BotRecord{}
	err := db.QueryRow(
		`SELECT id, bot_id, bot_name, bot_secret_encrypted, status, bound_session_id, created_at, last_used_at FROM bots WHERE id = ?`,
		id,
	).Scan(&r.ID, &r.BotID, &r.BotName, &r.BotSecretEncrypted, &r.Status, &r.BoundSessionID, &r.CreatedAt, &r.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListIdleBots returns all bots that are available for binding.
func (db *DB) ListIdleBots() ([]BotRecord, error) {
	rows, err := db.Query(
		`SELECT id, bot_id, bot_name, bot_secret_encrypted, status, bound_session_id, created_at, last_used_at FROM bots WHERE status = ? ORDER BY last_used_at ASC`,
		string(BotIdle),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bots []BotRecord
	for rows.Next() {
		var r BotRecord
		if err := rows.Scan(&r.ID, &r.BotID, &r.BotName, &r.BotSecretEncrypted, &r.Status, &r.BoundSessionID, &r.CreatedAt, &r.LastUsedAt); err != nil {
			return nil, err
		}
		bots = append(bots, r)
	}
	return bots, rows.Err()
}

// ListBots returns all bots regardless of status.
func (db *DB) ListBots() ([]BotRecord, error) {
	rows, err := db.Query(
		`SELECT id, bot_id, bot_name, bot_secret_encrypted, status, bound_session_id, created_at, last_used_at FROM bots ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bots []BotRecord
	for rows.Next() {
		var r BotRecord
		if err := rows.Scan(&r.ID, &r.BotID, &r.BotName, &r.BotSecretEncrypted, &r.Status, &r.BoundSessionID, &r.CreatedAt, &r.LastUsedAt); err != nil {
			return nil, err
		}
		bots = append(bots, r)
	}
	return bots, rows.Err()
}

// BindBot marks a bot as bound to a session.
func (db *DB) BindBot(botID int64, sessionID int64) error {
	// Update bot record.
	_, err := db.Exec(
		`UPDATE bots SET status = ?, bound_session_id = ?, last_used_at = NOW() WHERE id = ? AND status = ?`,
		string(BotBound), sessionID, botID, string(BotIdle),
	)
	if err != nil {
		return err
	}
	// Update session's bound_bot_id.
	_, err = db.Exec(
		`UPDATE sessions SET bound_bot_id = ? WHERE id = ?`,
		botID, sessionID,
	)
	return err
}

// ReleaseBot returns a bot to the idle pool and clears the session binding.
func (db *DB) ReleaseBot(botID int64) error {
	// Clear session's bound_bot_id first.
	_, _ = db.Exec(
		`UPDATE sessions SET bound_bot_id = NULL WHERE bound_bot_id = ?`,
		botID,
	)
	_, err := db.Exec(
		`UPDATE bots SET status = ?, bound_session_id = NULL WHERE id = ?`,
		string(BotIdle), botID,
	)
	return err
}

// MarkBotUnavailable sets a bot as unavailable (e.g. credential expired).
func (db *DB) MarkBotUnavailable(botID int64) error {
	_, err := db.Exec(
		`UPDATE bots SET status = ? WHERE id = ?`,
		string(BotUnavailable), botID,
	)
	return err
}

// DeleteBot removes a bot from the pool.
func (db *DB) DeleteBot(botID int64) error {
	_, err := db.Exec(`DELETE FROM bots WHERE id = ? AND status != ?`, botID, string(BotBound))
	return err
}
