package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the SQLite database and runs migrations.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate db: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

var globalDB *DB

// InitDB initializes the global database connection.
func InitDB(path string) error {
	var err error
	globalDB, err = Open(path)
	return err
}

// CloseDB closes the global database connection.
func CloseDB() error {
	if globalDB != nil {
		return globalDB.Close()
	}
	return nil
}

// CleanStaleBuilds updates any builds stuck in 'running' status to 'interrupted'.
func (db *DB) CleanStaleBuilds() error {
	if db == nil || db.conn == nil {
		return nil
	}
	_, err := db.conn.Exec("UPDATE builds SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP, error = 'Interrupted by daemon restart' WHERE status = 'running'")
	return err
}

func CleanStaleBuilds() error {
	if globalDB != nil {
		return globalDB.CleanStaleBuilds()
	}
	return nil
}

// GetDB returns the global DB instance.
func GetDB() *DB {
	return globalDB
}

// Global helper wrappers:

func ListRepos() ([]Repo, error) {
	return globalDB.ListRepos()
}

func GetRepo(id string) (*Repo, error) {
	return globalDB.GetRepo(id)
}

func SaveRepo(r *Repo) error {
	return globalDB.CreateRepo(r)
}

func UpdateRepo(r *Repo) error {
	return globalDB.UpdateRepo(r)
}

func DeleteRepo(id string) error {
	return globalDB.DeleteRepo(id)
}

func UpdateRepoStatus(id, status string) error {
	return globalDB.UpdateRepoStatus(id, status)
}

func CreateBuild(repoID, gitHead, status string) (int64, error) {
	return globalDB.CreateBuild(repoID, gitHead, status)
}

func FinishBuild(id int64, status, buildLog, buildErr string) error {
	return globalDB.FinishBuild(id, status, buildLog, buildErr)
}

func GetLatestBuilds(repoID string, limit int) ([]Build, error) {
	return globalDB.ListBuilds(repoID, limit)
}

func RecordQASession(repoID, userID, question, answer string) error {
	return globalDB.CreateQASession(repoID, userID, question, answer)
}

func ListQASessions(repoID, userID string, limit int) ([]QASession, error) {
	return globalDB.ListQASessions(repoID, userID, limit)
}


// migrate creates all required tables if they do not exist.
func (db *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			git_url     TEXT NOT NULL,
			branch      TEXT NOT NULL DEFAULT 'master',
			local_path  TEXT NOT NULL,
			wiki_dir    TEXT,
			static_dir  TEXT,
			schedule    TEXT NOT NULL DEFAULT '0 2 * * *',
			status      TEXT NOT NULL DEFAULT 'active',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS builds (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_id     TEXT NOT NULL REFERENCES repos(id),
			git_head    TEXT,
			status      TEXT NOT NULL DEFAULT 'pending',
			started_at  DATETIME,
			finished_at DATETIME,
			log         TEXT,
			error       TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS qa_sessions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_id     TEXT NOT NULL REFERENCES repos(id),
			user_id     TEXT NOT NULL,
			question    TEXT NOT NULL,
			answer      TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_builds_repo ON builds(repo_id)`,
		`CREATE INDEX IF NOT EXISTS idx_qa_repo_user ON qa_sessions(repo_id, user_id)`,
	}

	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	return nil
}

// --- Repo CRUD ---

// Repo represents a registered repository.
type Repo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GitURL    string `json:"git_url"`
	Branch    string `json:"branch"`
	LocalPath string `json:"local_path"`
	WikiDir   string `json:"wiki_dir"`
	StaticDir string `json:"static_dir"`
	Schedule  string `json:"schedule"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListRepos returns all registered repositories.
func (db *DB) ListRepos() ([]Repo, error) {
	rows, err := db.conn.Query(`SELECT id, name, git_url, branch, local_path, 
		COALESCE(wiki_dir,''), COALESCE(static_dir,''), schedule, status, 
		created_at, updated_at FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Name, &r.GitURL, &r.Branch, &r.LocalPath,
			&r.WikiDir, &r.StaticDir, &r.Schedule, &r.Status,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// GetRepo returns a single repository by ID.
func (db *DB) GetRepo(id string) (*Repo, error) {
	r := &Repo{}
	err := db.conn.QueryRow(`SELECT id, name, git_url, branch, local_path, 
		COALESCE(wiki_dir,''), COALESCE(static_dir,''), schedule, status, 
		created_at, updated_at FROM repos WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.GitURL, &r.Branch, &r.LocalPath,
			&r.WikiDir, &r.StaticDir, &r.Schedule, &r.Status,
			&r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// CreateRepo inserts a new repository.
func (db *DB) CreateRepo(r *Repo) error {
	_, err := db.conn.Exec(`INSERT INTO repos (id, name, git_url, branch, local_path, wiki_dir, static_dir, schedule, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.GitURL, r.Branch, r.LocalPath, r.WikiDir, r.StaticDir, r.Schedule, r.Status)
	return err
}

// UpdateRepo updates repository fields and updated_at timestamp.
func (db *DB) UpdateRepo(r *Repo) error {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`UPDATE repos SET name = ?, git_url = ?, branch = ?, local_path = ?, wiki_dir = ?, static_dir = ?, schedule = ?, status = ?, updated_at = ? WHERE id = ?`,
		r.Name, r.GitURL, r.Branch, r.LocalPath, r.WikiDir, r.StaticDir, r.Schedule, r.Status, timeStr, r.ID)
	return err
}

// DeleteRepo removes a repository and its associated builds and qa sessions by ID.
func (db *DB) DeleteRepo(id string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM builds WHERE repo_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM qa_sessions WHERE repo_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM repos WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateRepoStatus updates the status and updated_at timestamp.
func (db *DB) UpdateRepoStatus(id, status string) error {
	_, err := db.conn.Exec(`UPDATE repos SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().Local().Format("2006-01-02 15:04:05"), id)
	return err
}

// --- Build CRUD ---

// Build represents a single build record.
type Build struct {
	ID         int64  `json:"id"`
	RepoID     string `json:"repo_id"`
	GitHead    string `json:"git_head"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Log        string `json:"log"`
	Error      string `json:"error"`
}

// CreateBuild inserts a new build record and returns its ID.
func (db *DB) CreateBuild(repoID, gitHead, status string) (int64, error) {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	res, err := db.conn.Exec(`INSERT INTO builds (repo_id, git_head, status, started_at)
		VALUES (?, ?, ?, ?)`, repoID, gitHead, status, timeStr)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishBuild updates a build record with its final status, log, and error.
func (db *DB) FinishBuild(id int64, status, buildLog, buildErr string) error {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`UPDATE builds SET status = ?, finished_at = ?, log = ?, error = ? WHERE id = ?`,
		status, timeStr, buildLog, buildErr, id)
	return err
}

// UpdateBuildLog updates intermediate build log and error without changing status.
func (db *DB) UpdateBuildLog(id int64, buildLog, buildErr string) error {
	_, err := db.conn.Exec(`UPDATE builds SET log = ?, error = ? WHERE id = ?`, buildLog, buildErr, id)
	return err
}

// ListBuilds returns recent builds for a repo.
func (db *DB) ListBuilds(repoID string, limit int) ([]Build, error) {
	rows, err := db.conn.Query(`SELECT id, repo_id, COALESCE(git_head,''), status, 
		COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(log,''), COALESCE(error,'')
		FROM builds WHERE repo_id = ? ORDER BY id DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var builds []Build
	for rows.Next() {
		var b Build
		if err := rows.Scan(&b.ID, &b.RepoID, &b.GitHead, &b.Status,
			&b.StartedAt, &b.FinishedAt, &b.Log, &b.Error); err != nil {
			return nil, err
		}
		builds = append(builds, b)
	}
	return builds, rows.Err()
}

// --- QA Session CRUD ---

// QASession represents a single Q&A exchange.
type QASession struct {
	ID        int64  `json:"id"`
	RepoID    string `json:"repo_id"`
	UserID    string `json:"user_id"`
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	CreatedAt string `json:"created_at"`
}

// CreateQASession records a Q&A exchange.
func (db *DB) CreateQASession(repoID, userID, question, answer string) error {
	_, err := db.conn.Exec(`INSERT INTO qa_sessions (repo_id, user_id, question, answer)
		VALUES (?, ?, ?, ?)`, repoID, userID, question, answer)
	return err
}

// ListQASessions returns Q&A history for a user in a repo.
func (db *DB) ListQASessions(repoID, userID string, limit int) ([]QASession, error) {
	rows, err := db.conn.Query(`SELECT id, repo_id, user_id, question, COALESCE(answer,''), created_at
		FROM qa_sessions WHERE repo_id = ? AND user_id = ? ORDER BY id DESC LIMIT ?`,
		repoID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []QASession
	for rows.Next() {
		var s QASession
		if err := rows.Scan(&s.ID, &s.RepoID, &s.UserID, &s.Question, &s.Answer, &s.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}
