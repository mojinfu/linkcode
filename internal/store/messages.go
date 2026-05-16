package store

import "time"

// MessageRole indicates who sent the message.
type MessageRole string

const (
	RoleUser  MessageRole = "user"
	RoleAgent MessageRole = "agent"
)

// MessageRecord is a row from the messages table.
type MessageRecord struct {
	ID          int64
	SessionID   int64
	Role        MessageRole
	Content     string
	ContentType string
	CreatedAt   time.Time
}

// InsertMessage persists a chat message for a session.
func (db *DB) InsertMessage(sessionID int64, role MessageRole, content, contentType string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO messages (session_id, role, content, content_type) VALUES (?, ?, ?, ?)`,
		sessionID, string(role), content, contentType,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListMessages returns all messages for a session ordered by time.
func (db *DB) ListMessages(sessionID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Query(
		`SELECT id, session_id, role, content, content_type, created_at FROM messages WHERE session_id = ? ORDER BY created_at ASC LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []MessageRecord
	for rows.Next() {
		var m MessageRecord
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.ContentType, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// DeleteMessagesBySession removes all messages for a session.
func (db *DB) DeleteMessagesBySession(sessionID int64) error {
	_, err := db.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID)
	return err
}
