package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

type Session struct {
	Token       string    `json:"token"`
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// InitDB opens SQLite connection and creates sessions table.
func InitDB(dbPath string) error {
	var err error
	DB, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		display_name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
	`
	if _, err := DB.Exec(schema); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	return nil
}

func CloseDB() {
	if DB != nil {
		DB.Close()
	}
}

// SaveSession creates or updates a user session.
func SaveSession(s *Session) error {
	query := `
	INSERT INTO sessions (token, user_id, display_name, created_at, expires_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(token) DO UPDATE SET
		expires_at = excluded.expires_at;
	`
	timeFormat := "2006-01-02 15:04:05"
	createdAtStr := time.Now().UTC().Format(timeFormat)
	expiresAtStr := s.ExpiresAt.UTC().Format(timeFormat)

	_, err := DB.Exec(query, s.Token, s.UserID, s.DisplayName, createdAtStr, expiresAtStr)
	return err
}

// GetSession retrieves a valid unexpired session.
func GetSession(token string) (*Session, error) {
	query := `
	SELECT token, user_id, display_name, created_at, expires_at
	FROM sessions WHERE token = ? AND expires_at > CURRENT_TIMESTAMP;
	`
	row := DB.QueryRow(query, token)
	var s Session
	err := row.Scan(&s.Token, &s.UserID, &s.DisplayName, &s.CreatedAt, &s.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// DeleteSession removes a session (logout).
func DeleteSession(token string) error {
	_, err := DB.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

// CleanExpiredSessions removes expired sessions from DB.
func CleanExpiredSessions() error {
	_, err := DB.Exec("DELETE FROM sessions WHERE expires_at <= CURRENT_TIMESTAMP")
	return err
}
