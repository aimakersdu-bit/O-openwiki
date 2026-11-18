package db

import (
	"database/sql"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var (
	DB           *sql.DB
	testMu       sync.Mutex
	testDBMap    = make(map[string]string)
	testDBRefs   = make(map[string]int)
	activeTestDB string
)

func isRunningUnderGoTest() bool {
	return flag.Lookup("test.v") != nil
}

func buildTargetTestDSN(baseDSN, dbName string) string {
	prefix := baseDSN
	query := ""
	if idx := strings.Index(baseDSN, "?"); idx >= 0 {
		prefix = baseDSN[:idx]
		query = baseDSN[idx+1:]
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if query == "" {
		return prefix + dbName + "?parseTime=true"
	}
	if strings.Contains(query, "parseTime=") {
		return prefix + dbName + "?" + query
	}
	return prefix + dbName + "?" + query + "&parseTime=true"
}

func resolveDSN(dsn string) (string, string, error) {
	if strings.Contains(dsn, "@") {
		return dsn, "", nil
	}
	if !isRunningUnderGoTest() {
		return "", "", fmt.Errorf("invalid mysql dsn (missing '@' host specification): %q", dsn)
	}

	testMu.Lock()
	defer testMu.Unlock()

	baseDSN := os.Getenv("TEST_DB_DSN")
	if baseDSN == "" {
		baseDSN = "root:@tcp(127.0.0.1:3306)/"
	}

	dbName, exists := testDBMap[dsn]
	if !exists {
		dbName = fmt.Sprintf("test_portal_%d_%d", time.Now().UnixNano(), rand.Intn(1000000))
		adminDB, err := sql.Open("mysql", baseDSN)
		if err != nil {
			return "", "", fmt.Errorf("connect mysql base: %w", err)
		}
		if err := adminDB.Ping(); err != nil {
			adminDB.Close()
			return "", "", fmt.Errorf("ping mysql base: %w", err)
		}
		if _, err := adminDB.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
			adminDB.Close()
			return "", "", fmt.Errorf("create test db %s: %w", dbName, err)
		}
		adminDB.Close()
		testDBMap[dsn] = dbName
	}
	testDBRefs[dbName]++

	targetDSN := buildTargetTestDSN(baseDSN, dbName)
	return targetDSN, dbName, nil
}

type Session struct {
	Token       string    `json:"token"`
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// InitDB opens MySQL connection and runs migrations by default.
func InitDB(dsn string) error {
	return InitDBWithOptions(dsn, true)
}

// InitDBWithOptions opens MySQL connection and optionally creates tables/indexes.
func InitDBWithOptions(dsn string, autoMigrate bool) error {
	targetDSN, testDB, err := resolveDSN(dsn)
	if err != nil {
		return fmt.Errorf("resolve mysql dsn: %w", err)
	}

	if DB != nil {
		CloseDB()
	}

	DB, err = sql.Open("mysql", targetDSN)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	DB.SetMaxOpenConns(150)
	DB.SetMaxIdleConns(50)
	DB.SetConnMaxLifetime(5 * time.Minute)
	activeTestDB = testDB

	if !autoMigrate {
		if err := DB.Ping(); err != nil {
			return fmt.Errorf("failed to ping database: %w", err)
		}
		return nil
	}

	tables := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			token VARCHAR(255) PRIMARY KEY,
			user_id VARCHAR(255) NOT NULL,
			display_name TEXT,
			role VARCHAR(255) DEFAULT 'user',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS users (
			username VARCHAR(255) PRIMARY KEY,
			display_name TEXT,
			role VARCHAR(255) NOT NULL DEFAULT 'user',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS user_domain_grants (
			username VARCHAR(255) NOT NULL,
			business_domain VARCHAR(255) NOT NULL,
			granted_by VARCHAR(255) NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (username, business_domain)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INT AUTO_INCREMENT PRIMARY KEY,
			operator VARCHAR(255) NOT NULL,
			action VARCHAR(255) NOT NULL,
			target_type VARCHAR(255) NOT NULL,
			target_id VARCHAR(255) NOT NULL,
			details TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS business_domains (
			id INT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL UNIQUE,
			code VARCHAR(255) DEFAULT '',
			description TEXT,
			sort_order INT DEFAULT 0,
			status VARCHAR(255) NOT NULL DEFAULT 'active',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, t := range tables {
		if _, err := DB.Exec(t); err != nil {
			return fmt.Errorf("failed to execute schema: %w", err)
		}
	}
	// Best-effort column upgrade if created by earlier migration
	_, _ = DB.Exec(`ALTER TABLE business_domains MODIFY COLUMN description TEXT`)
	indices := []string{
		`CREATE INDEX idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX idx_grants_user ON user_domain_grants(username)`,
		`CREATE INDEX idx_audit_created ON audit_logs(created_at)`,
		`CREATE INDEX idx_business_domains_name ON business_domains(name)`,
	}
	for _, idx := range indices {
		if _, err := DB.Exec(idx); err != nil {
			if !strings.Contains(err.Error(), "1061") {
				return fmt.Errorf("failed to create index: %w", err)
			}
		}
	}

	return nil
}

func CloseDB() {
	if DB != nil {
		DB.Close()
		DB = nil
	}
	if activeTestDB != "" {
		testMu.Lock()
		testDBRefs[activeTestDB]--
		if testDBRefs[activeTestDB] <= 0 {
			for k, v := range testDBMap {
				if v == activeTestDB {
					delete(testDBMap, k)
				}
			}
			delete(testDBRefs, activeTestDB)

			baseDSN := os.Getenv("TEST_DB_DSN")
			if baseDSN == "" {
				baseDSN = "root:@tcp(127.0.0.1:3306)/"
			}
			adminDB, aErr := sql.Open("mysql", baseDSN)
			if aErr == nil {
				_, _ = adminDB.Exec("DROP DATABASE `" + activeTestDB + "`")
				adminDB.Close()
			}
		}
		activeTestDB = ""
		testMu.Unlock()
	}
}

// SaveSession creates or updates a user session.
func SaveSession(s *Session) error {
	query := `
	INSERT INTO sessions (token, user_id, display_name, role, created_at, expires_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
		role = ?,
		expires_at = ?
	`
	timeFormat := "2006-01-02 15:04:05"
	createdAtStr := time.Now().UTC().Format(timeFormat)
	expiresAtStr := s.ExpiresAt.UTC().Format(timeFormat)

	if s.Role == "" {
		s.Role = "user"
	}

	_, err := DB.Exec(query, s.Token, s.UserID, s.DisplayName, s.Role, createdAtStr, expiresAtStr, s.Role, expiresAtStr)
	return err
}

// GetSession retrieves a valid unexpired session.
func GetSession(token string) (*Session, error) {
	query := `
	SELECT token, user_id, display_name, COALESCE(role, 'user'), created_at, expires_at
	FROM sessions WHERE token = ? AND expires_at > UTC_TIMESTAMP();
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
	_, err := DB.Exec("DELETE FROM sessions WHERE expires_at <= UTC_TIMESTAMP()")
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
	ON DUPLICATE KEY UPDATE
		display_name = ?,
		updated_at = CURRENT_TIMESTAMP
	`
	_, err := DB.Exec(query, u.Username, u.DisplayName, u.Role, u.DisplayName)
	return err
}

// SetUserRole explicitly sets a user's role.
func SetUserRole(username, role string) error {
	query := `
	INSERT INTO users (username, role, updated_at)
	VALUES (?, ?, CURRENT_TIMESTAMP)
	ON DUPLICATE KEY UPDATE
		role = ?,
		updated_at = CURRENT_TIMESTAMP
	`
	_, err := DB.Exec(query, username, role, role)
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
	INSERT IGNORE INTO user_domain_grants (username, business_domain, granted_by, created_at)
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
