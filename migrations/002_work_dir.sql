-- LinkCode database schema v2: work_dir support

-- Global settings table for control-level configuration.
CREATE TABLE IF NOT EXISTS settings (
    key_name    VARCHAR(128) PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT IGNORE INTO settings (key_name, value) VALUES ('default_work_dir', '');

-- Agent-level default work directory (column added in Go code for MySQL compatibility).
