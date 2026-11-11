package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

type Session struct {
	Token       string    `json:"token"`
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// InitDB opens SQLite connection and creates sessions table.
func InitDB(dbPath string) error {
	var err error
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	dsn := dbPath + sep + "_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=10000&_txlock=immediate"
	DB, err = sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	// Serialize SQLite operations via a single pooled connection in Go to prevent
	// intra-process write lock contention ("database is locked").
	DB.SetMaxOpenConns(1)
	DB.SetMaxIdleConns(1)
	DB.SetConnMaxLifetime(0)

	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		display_name TEXT,
		role TEXT DEFAULT 'user',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

	CREATE TABLE IF NOT EXISTS users (
		username TEXT PRIMARY KEY,
		display_name TEXT,
		role TEXT NOT NULL DEFAULT 'user',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS user_domain_grants (
		username TEXT NOT NULL,
		business_domain TEXT NOT NULL,
		granted_by TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (username, business_domain)
	);
	CREATE INDEX IF NOT EXISTS idx_grants_user ON user_domain_grants(username);

	CREATE TABLE IF NOT EXISTS audit_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		operator TEXT NOT NULL,
		action TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		details TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at);

	CREATE TABLE IF NOT EXISTS business_domains (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		code TEXT DEFAULT '',
		description TEXT DEFAULT '',
		sort_order INTEGER DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_business_domains_name ON business_domains(name);
	`
	if _, err := DB.Exec(schema); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	// Migration: add role column if missing in older schema
	_, _ = DB.Exec("ALTER TABLE sessions ADD COLUMN role TEXT DEFAULT 'user';")
	_, _ = DB.Exec("ALTER TABLE business_domains ADD COLUMN code TEXT DEFAULT '';")

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
	INSERT INTO sessions (token, user_id, display_name, role, created_at, expires_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(token) DO UPDATE SET
		role = excluded.role,
		expires_at = excluded.expires_at;
	`
	timeFormat := "2006-01-02 15:04:05"
	createdAtStr := time.Now().UTC().Format(timeFormat)
	expiresAtStr := s.ExpiresAt.UTC().Format(timeFormat)

	if s.Role == "" {
		s.Role = "user"
	}

	_, err := DB.Exec(query, s.Token, s.UserID, s.DisplayName, s.Role, createdAtStr, expiresAtStr)
	return err
}

// GetSession retrieves a valid unexpired session.
func GetSession(token string) (*Session, error) {
	query := `
	SELECT token, user_id, display_name, COALESCE(role, 'user'), created_at, expires_at
	FROM sessions WHERE token = ? AND expires_at > CURRENT_TIMESTAMP;
	`
	row := DB.QueryRow(query, token)
	var s Session
	err := row.Scan(&s.Token, &s.UserID, &s.DisplayName, &s.Role, &s.CreatedAt, &s.ExpiresAt)
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

// --- User, Domain Grants & Audit Log Models and DAOs ---

type User struct {
	Username          string    `json:"username"`
	DisplayName       string    `json:"display_name"`
	Role              string    `json:"role"` // 'super_admin', 'group_leader', 'user'
	AuthorizedDomains []string  `json:"authorized_domains,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type DomainGrant struct {
	Username       string    `json:"username"`
	BusinessDomain string    `json:"business_domain"`
	GrantedBy      string    `json:"granted_by"`
	CreatedAt      time.Time `json:"created_at"`
}

type AuditLog struct {
	ID         int64     `json:"id"`
	Operator   string    `json:"operator"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Details    string    `json:"details"`
	CreatedAt  time.Time `json:"created_at"`
}

// UpsertUser inserts or updates user profile while preserving existing role if not specified.
func UpsertUser(u *User) error {
	if u.Role == "" {
		u.Role = "user"
	}
	query := `
	INSERT INTO users (username, display_name, role, updated_at)
	VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(username) DO UPDATE SET
		display_name = excluded.display_name,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := DB.Exec(query, u.Username, u.DisplayName, u.Role)
	return err
}

// SetUserRole explicitly sets a user's role.
func SetUserRole(username, role string) error {
	query := `
	INSERT INTO users (username, role, updated_at)
	VALUES (?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(username) DO UPDATE SET
		role = excluded.role,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := DB.Exec(query, username, role)
	return err
}

// GetUser retrieves a user along with their authorized business domains.
func GetUser(username string) (*User, error) {
	row := DB.QueryRow(`SELECT username, COALESCE(display_name, ''), COALESCE(role, 'user'), created_at, updated_at FROM users WHERE username = ?`, username)
	var u User
	err := row.Scan(&u.Username, &u.DisplayName, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	domains, err := GetUserDomains(username)
	if err != nil {
		return nil, err
	}
	u.AuthorizedDomains = domains
	return &u, nil
}

// ListUsers returns all users in the system.
func ListUsers() ([]User, error) {
	rows, err := DB.Query(`SELECT username, COALESCE(display_name, ''), COALESCE(role, 'user'), created_at, updated_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Username, &u.DisplayName, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Close the cursor before issuing follow-up queries so single-connection pools cannot deadlock.
	rows.Close()

	for i := range users {
		domains, err := GetUserDomains(users[i].Username)
		if err != nil {
			return nil, err
		}
		users[i].AuthorizedDomains = domains
	}
	return users, nil
}

// GrantDomain authorizes a user (group leader) for a specific business domain.
func GrantDomain(username, domain, operator string) error {
	query := `
	INSERT OR IGNORE INTO user_domain_grants (username, business_domain, granted_by, created_at)
	VALUES (?, ?, ?, CURRENT_TIMESTAMP);
	`
	_, err := DB.Exec(query, username, domain, operator)
	return err
}

// RevokeDomain removes a user's authorization for a business domain.
func RevokeDomain(username, domain string) error {
	_, err := DB.Exec(`DELETE FROM user_domain_grants WHERE username = ? AND business_domain = ?`, username, domain)
	return err
}

// GetUserDomains returns all business domains granted to a user.
func GetUserDomains(username string) ([]string, error) {
	rows, err := DB.Query(`SELECT business_domain FROM user_domain_grants WHERE username = ? ORDER BY business_domain`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var domains []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

// BusinessDomain is a configurable business domain dictionary entry.
type BusinessDomain struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ListBusinessDomains returns all active business domains ordered by sort_order then name.
func ListBusinessDomains() ([]BusinessDomain, error) {
	rows, err := DB.Query(`SELECT id, name, COALESCE(code,''), COALESCE(description,''), sort_order, status, created_at, updated_at
		FROM business_domains WHERE status = 'active' ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []BusinessDomain
	for rows.Next() {
		var d BusinessDomain
		if err := rows.Scan(&d.ID, &d.Name, &d.Code, &d.Description, &d.SortOrder, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

// ListAllBusinessDomains returns all domains including inactive (for admin management).
func ListAllBusinessDomains() ([]BusinessDomain, error) {
	rows, err := DB.Query(`SELECT id, name, COALESCE(code,''), COALESCE(description,''), sort_order, status, created_at, updated_at
		FROM business_domains ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []BusinessDomain
	for rows.Next() {
		var d BusinessDomain
		if err := rows.Scan(&d.ID, &d.Name, &d.Code, &d.Description, &d.SortOrder, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

// CreateBusinessDomain inserts a new business domain.
func CreateBusinessDomain(d *BusinessDomain) error {
	_, err := DB.Exec(`INSERT INTO business_domains (name, code, description, sort_order, status) VALUES (?, ?, ?, ?, ?)`,
		d.Name, d.Code, d.Description, d.SortOrder, defaultStr(d.Status, "active"))
	return err
}

// UpdateBusinessDomain updates an existing business domain by id.
func UpdateBusinessDomain(d *BusinessDomain) error {
	_, err := DB.Exec(`UPDATE business_domains SET name = ?, code = ?, description = ?, sort_order = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		d.Name, d.Code, d.Description, d.SortOrder, defaultStr(d.Status, "active"), d.ID)
	return err
}

// DeleteBusinessDomain removes a business domain by id.
func DeleteBusinessDomain(id int64) error {
	_, err := DB.Exec(`DELETE FROM business_domains WHERE id = ?`, id)
	return err
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// IsUserAuthorizedForDomain checks whether the user has permission to manage the specified business domain.
func IsUserAuthorizedForDomain(username, role, domain string) (bool, error) {
	// Super admins have universal break-glass access
	if role == "super_admin" || role == "admin" {
		return true, nil
	}
	if role != "group_leader" {
		return false, nil
	}

	var count int
	err := DB.QueryRow(`SELECT COUNT(*) FROM user_domain_grants WHERE username = ? AND business_domain = ?`, username, domain).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RecordAuditLog writes an administrative action entry into the audit trail.
func RecordAuditLog(operator, action, targetType, targetID, details string) error {
	query := `
	INSERT INTO audit_logs (operator, action, target_type, target_id, details, created_at)
	VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP);
	`
	_, err := DB.Exec(query, operator, action, targetType, targetID, details)
	return err
}

// ListAuditLogs returns the most recent administrative audit records.
func ListAuditLogs(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := DB.Query(`SELECT id, operator, action, target_type, target_id, COALESCE(details, ''), created_at FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(&l.ID, &l.Operator, &l.Action, &l.TargetType, &l.TargetID, &l.Details, &l.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
