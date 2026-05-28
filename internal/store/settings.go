package store

// GetSetting reads a global setting value by key.
func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key_name = ?`, key).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetSetting upserts a global setting value.
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key_name, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)`,
		key, value,
	)
	return err
}
