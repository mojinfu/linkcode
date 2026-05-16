// Package store provides MySQL data access for LinkCode.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// DB wraps the sql.DB connection pool.
type DB struct {
	*sql.DB
}

// Open creates a new MySQL connection pool.
func Open(dsn string, maxOpen, maxIdle int) (*DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open mysql: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping mysql: %w", err)
	}
	return &DB{DB: db}, nil
}

// RunMigrations executes the schema DDL.
func (db *DB) RunMigrations(migrationSQL string) error {
	_, err := db.Exec(migrationSQL)
	if err != nil {
		return fmt.Errorf("store: run migrations: %w", err)
	}
	return nil
}
