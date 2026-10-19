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

func GetRepoByNameOrID(identifier string) (*Repo, error) {
	return globalDB.GetRepoByNameOrID(identifier)
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

func GetLatestBuild(repoID string) (*Build, error) {
	return globalDB.GetLatestBuild(repoID)
}

func RecordQASession(sessionID, repoID, userID, question, answer string) error {
	return globalDB.CreateQASession(sessionID, repoID, userID, question, answer)
}

func ListQASessions(repoID, userID string, limit int) ([]QASession, error) {
	return globalDB.ListQASessions(repoID, userID, limit)
}

func ListUserQASessions(repoID, userID string, limit int) ([]QASessionSummary, error) {
	return globalDB.ListUserQASessions(repoID, userID, limit)
}

func GetQASessionMessages(sessionID, userID string) ([]QASession, error) {
	return globalDB.GetQASessionMessages(sessionID, userID)
}

func DeleteQASession(sessionID, userID string) error {
	return globalDB.DeleteQASession(sessionID, userID)
}

func ListProjects() ([]Project, error) {
	return globalDB.ListProjects()
}

func GetProject(id string) (*Project, error) {
	return globalDB.GetProject(id)
}

func CreateProject(p *Project) error {
	return globalDB.CreateProject(p)
}

func UpdateProject(p *Project) error {
	return globalDB.UpdateProject(p)
}

func DeleteProject(id string) error {
	return globalDB.DeleteProject(id)
}

func SetProjectRepos(projectID string, repos []ProjectRepo) error {
	return globalDB.SetProjectRepos(projectID, repos)
}

func GetProjectRepos(projectID string) ([]RepoWithAlias, error) {
	return globalDB.GetProjectRepos(projectID)
}

// migrate creates all required tables if they do not exist.
func (db *DB) migrate() error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			git_url     TEXT NOT NULL,
			branch      TEXT NOT NULL DEFAULT 'main',
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
			session_id  TEXT,
			repo_id     TEXT NOT NULL REFERENCES repos(id),
			user_id     TEXT NOT NULL,
			question    TEXT NOT NULL,
			answer      TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			description TEXT,
			local_path  TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'active',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS project_repos (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			repo_id     TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
			alias_name  TEXT NOT NULL,
			role        TEXT DEFAULT 'service',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, repo_id),
			UNIQUE(project_id, alias_name)
		)`,
		`CREATE TABLE IF NOT EXISTS project_qa_sessions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id  TEXT,
			project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			user_id     TEXT NOT NULL,
			question    TEXT NOT NULL,
			answer      TEXT,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, t := range tables {
		if _, err := db.conn.Exec(t); err != nil {
			return fmt.Errorf("exec table creation: %w", err)
		}
	}

	// Try adding session_id column if table existed without it
	db.conn.Exec(`ALTER TABLE qa_sessions ADD COLUMN session_id TEXT`)

	indices := []string{
		`CREATE INDEX IF NOT EXISTS idx_builds_repo ON builds(repo_id)`,
		`CREATE INDEX IF NOT EXISTS idx_qa_repo_user ON qa_sessions(repo_id, user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_qa_session_id ON qa_sessions(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_project_repos_proj ON project_repos(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_project_qa_proj ON project_qa_sessions(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_project_qa_sess ON project_qa_sessions(session_id)`,
	}

	for _, idx := range indices {
		if _, err := db.conn.Exec(idx); err != nil {
			return fmt.Errorf("exec index creation: %w", err)
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

// GetRepoByNameOrID returns a single repository by ID first, and falls back to Name.
func (db *DB) GetRepoByNameOrID(identifier string) (*Repo, error) {
	// 1. Prioritize exact ID match
	r, err := db.GetRepo(identifier)
	if err != nil {
		return nil, err
	}
	if r != nil {
		return r, nil
	}

	// 2. Fallback to Name match
	r = &Repo{}
	err = db.conn.QueryRow(`SELECT id, name, git_url, branch, local_path, 
		COALESCE(wiki_dir,''), COALESCE(static_dir,''), schedule, status, 
		created_at, updated_at FROM repos WHERE name = ? LIMIT 1`, identifier).
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

// GetLatestBuild returns the most recent build for a repo.
func (db *DB) GetLatestBuild(repoID string) (*Build, error) {
	builds, err := db.ListBuilds(repoID, 1)
	if err != nil {
		return nil, err
	}
	if len(builds) == 0 {
		return nil, nil
	}
	return &builds[0], nil
}

// --- QA Session CRUD ---

// QASession represents a single Q&A exchange.
type QASession struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	RepoID    string `json:"repo_id"`
	UserID    string `json:"user_id"`
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	CreatedAt string `json:"created_at"`
}

type QASessionSummary struct {
	SessionID    string `json:"session_id"`
	RepoID       string `json:"repo_id"`
	UserID       string `json:"user_id"`
	Title        string `json:"title"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MessageCount int    `json:"message_count"`
}

// CreateQASession records a Q&A exchange.
func (db *DB) CreateQASession(sessionID, repoID, userID, question, answer string) error {
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`INSERT INTO qa_sessions (session_id, repo_id, user_id, question, answer, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, repoID, userID, question, answer, timeStr)
	return err
}

// ListQASessions returns Q&A history for a user in a repo, or all users if userID is empty.
func (db *DB) ListQASessions(repoID, userID string, limit int) ([]QASession, error) {
	var rows *sql.Rows
	var err error
	if userID != "" {
		rows, err = db.conn.Query(`SELECT id, COALESCE(session_id,''), repo_id, user_id, question, COALESCE(answer,''), created_at
			FROM qa_sessions WHERE repo_id = ? AND user_id = ? ORDER BY id DESC LIMIT ?`,
			repoID, userID, limit)
	} else {
		rows, err = db.conn.Query(`SELECT id, COALESCE(session_id,''), repo_id, user_id, question, COALESCE(answer,''), created_at
			FROM qa_sessions WHERE repo_id = ? ORDER BY id DESC LIMIT ?`,
			repoID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []QASession
	for rows.Next() {
		var s QASession
		if err := rows.Scan(&s.ID, &s.SessionID, &s.RepoID, &s.UserID, &s.Question, &s.Answer, &s.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// ListUserQASessions returns session summaries for a user in a repo, or all users if userID is empty.
func (db *DB) ListUserQASessions(repoID, userID string, limit int) ([]QASessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if userID != "" {
		query := `
			SELECT 
				COALESCE(session_id, printf('legacy_%d', id)) as sess_id,
				repo_id,
				user_id,
				MIN(question) as first_question,
				MIN(created_at) as first_created,
				MAX(created_at) as last_created,
				COUNT(id) as msg_count
			FROM qa_sessions 
			WHERE repo_id = ? AND user_id = ? 
			GROUP BY sess_id
			ORDER BY last_created DESC 
			LIMIT ?
		`
		rows, err = db.conn.Query(query, repoID, userID, limit)
	} else {
		query := `
			SELECT 
				COALESCE(session_id, printf('legacy_%d', id)) as sess_id,
				repo_id,
				user_id,
				MIN(question) as first_question,
				MIN(created_at) as first_created,
				MAX(created_at) as last_created,
				COUNT(id) as msg_count
			FROM qa_sessions 
			WHERE repo_id = ? 
			GROUP BY sess_id
			ORDER BY last_created DESC 
			LIMIT ?
		`
		rows, err = db.conn.Query(query, repoID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []QASessionSummary
	for rows.Next() {
		var s QASessionSummary
		if err := rows.Scan(&s.SessionID, &s.RepoID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt, &s.MessageCount); err != nil {
			return nil, err
		}
		if len([]rune(s.Title)) > 30 {
			s.Title = string([]rune(s.Title)[:30]) + "..."
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// GetQASessionMessages returns all Q&A messages for a specific session_id in chronological order across repos and projects.
func (db *DB) GetQASessionMessages(sessionID, userID string) ([]QASession, error) {
	var rows *sql.Rows
	var err error
	if userID != "" {
		query := `
			SELECT id, COALESCE(session_id,''), repo_id, user_id, question, COALESCE(answer,''), created_at
			FROM qa_sessions WHERE (session_id = ? OR (session_id IS NULL AND printf('legacy_%d', id) = ?)) AND user_id = ?
			UNION ALL
			SELECT id, COALESCE(session_id,''), project_id, user_id, question, COALESCE(answer,''), created_at
			FROM project_qa_sessions WHERE (session_id = ? OR (session_id IS NULL AND printf('legacy_%d', id) = ?)) AND user_id = ?
			ORDER BY id ASC`
		rows, err = db.conn.Query(query, sessionID, sessionID, userID, sessionID, sessionID, userID)
	} else {
		query := `
			SELECT id, COALESCE(session_id,''), repo_id, user_id, question, COALESCE(answer,''), created_at
			FROM qa_sessions WHERE session_id = ? OR (session_id IS NULL AND printf('legacy_%d', id) = ?)
			UNION ALL
			SELECT id, COALESCE(session_id,''), project_id, user_id, question, COALESCE(answer,''), created_at
			FROM project_qa_sessions WHERE session_id = ? OR (session_id IS NULL AND printf('legacy_%d', id) = ?)
			ORDER BY id ASC`
		rows, err = db.conn.Query(query, sessionID, sessionID, sessionID, sessionID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []QASession
	for rows.Next() {
		var s QASession
		if err := rows.Scan(&s.ID, &s.SessionID, &s.RepoID, &s.UserID, &s.Question, &s.Answer, &s.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, s)
	}
	return messages, rows.Err()
}

// DeleteQASession deletes a session and its messages.
func (db *DB) DeleteQASession(sessionID, userID string) error {
	_, err1 := db.conn.Exec(`DELETE FROM qa_sessions WHERE (session_id = ? OR printf('legacy_%d', id) = ?) AND (user_id = ? OR ? = '')`, sessionID, sessionID, userID, userID)
	_, err2 := db.conn.Exec(`DELETE FROM project_qa_sessions WHERE (session_id = ? OR printf('legacy_%d', id) = ?) AND (user_id = ? OR ? = '')`, sessionID, sessionID, userID, userID)
	if err1 != nil {
		return err1
	}
	return err2
}

func (db *DB) CreateProjectQASession(sessionID, projectID, userID, question, answer string) error {
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`INSERT INTO project_qa_sessions (session_id, project_id, user_id, question, answer, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, projectID, userID, question, answer, timeStr)
	return err
}

func RecordProjectQASession(sessionID, projectID, userID, question, answer string) error {
	if globalDB == nil {
		return nil
	}
	return globalDB.CreateProjectQASession(sessionID, projectID, userID, question, answer)
}

// ListProjectQASessions returns Q&A history for a user in a project, or all users if userID is empty.
func (db *DB) ListProjectQASessions(projectID, userID string, limit int) ([]QASession, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if userID != "" {
		rows, err = db.conn.Query(`SELECT id, COALESCE(session_id,''), project_id, user_id, question, COALESCE(answer,''), created_at
			FROM project_qa_sessions WHERE project_id = ? AND user_id = ? ORDER BY id DESC LIMIT ?`,
			projectID, userID, limit)
	} else {
		rows, err = db.conn.Query(`SELECT id, COALESCE(session_id,''), project_id, user_id, question, COALESCE(answer,''), created_at
			FROM project_qa_sessions WHERE project_id = ? ORDER BY id DESC LIMIT ?`,
			projectID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []QASession
	for rows.Next() {
		var s QASession
		if err := rows.Scan(&s.ID, &s.SessionID, &s.RepoID, &s.UserID, &s.Question, &s.Answer, &s.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func ListProjectQASessions(projectID, userID string, limit int) ([]QASession, error) {
	if globalDB == nil {
		return nil, nil
	}
	return globalDB.ListProjectQASessions(projectID, userID, limit)
}

// ListUserProjectQASessions returns session summaries for a user in a project, or all users if userID is empty.
func (db *DB) ListUserProjectQASessions(projectID, userID string, limit int) ([]QASessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if userID != "" {
		query := `
			SELECT 
				COALESCE(session_id, printf('legacy_%d', id)) as sess_id,
				project_id,
				user_id,
				MIN(question) as first_question,
				MIN(created_at) as first_created,
				MAX(created_at) as last_created,
				COUNT(id) as msg_count
			FROM project_qa_sessions 
			WHERE project_id = ? AND user_id = ? 
			GROUP BY sess_id
			ORDER BY last_created DESC 
			LIMIT ?
		`
		rows, err = db.conn.Query(query, projectID, userID, limit)
	} else {
		query := `
			SELECT 
				COALESCE(session_id, printf('legacy_%d', id)) as sess_id,
				project_id,
				user_id,
				MIN(question) as first_question,
				MIN(created_at) as first_created,
				MAX(created_at) as last_created,
				COUNT(id) as msg_count
			FROM project_qa_sessions 
			WHERE project_id = ? 
			GROUP BY sess_id
			ORDER BY last_created DESC 
			LIMIT ?
		`
		rows, err = db.conn.Query(query, projectID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []QASessionSummary
	for rows.Next() {
		var s QASessionSummary
		if err := rows.Scan(&s.SessionID, &s.RepoID, &s.UserID, &s.Title, &s.CreatedAt, &s.UpdatedAt, &s.MessageCount); err != nil {
			return nil, err
		}
		if len([]rune(s.Title)) > 30 {
			s.Title = string([]rune(s.Title)[:30]) + "..."
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func ListUserProjectQASessions(projectID, userID string, limit int) ([]QASessionSummary, error) {
	if globalDB == nil {
		return nil, nil
	}
	return globalDB.ListUserProjectQASessions(projectID, userID, limit)
}

// --- Project CRUD ---

// Project represents a multi-repo project workspace.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	LocalPath   string `json:"local_path"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ProjectRepo represents a repo mapping inside a project.
type ProjectRepo struct {
	ID        int64  `json:"id,omitempty"`
	ProjectID string `json:"project_id"`
	RepoID    string `json:"repo_id"`
	AliasName string `json:"alias_name"`
	Role      string `json:"role"`
}

// RepoWithAlias joins Repo with project-specific alias and role.
type RepoWithAlias struct {
	Repo
	AliasName string `json:"alias_name"`
	Role      string `json:"role"`
}

// ListProjects returns all registered projects.
func (db *DB) ListProjects() ([]Project, error) {
	rows, err := db.conn.Query(`SELECT id, name, COALESCE(description,''), local_path, status, created_at, updated_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.LocalPath, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProject returns a single project by ID.
func (db *DB) GetProject(id string) (*Project, error) {
	p := &Project{}
	err := db.conn.QueryRow(`SELECT id, name, COALESCE(description,''), local_path, status, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.LocalPath, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// CreateProject inserts a new project.
func (db *DB) CreateProject(p *Project) error {
	_, err := db.conn.Exec(`INSERT INTO projects (id, name, description, local_path, status) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.LocalPath, p.Status)
	return err
}

// UpdateProject updates project fields and updated_at timestamp.
func (db *DB) UpdateProject(p *Project) error {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`UPDATE projects SET name = ?, description = ?, local_path = ?, status = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, p.LocalPath, p.Status, timeStr, p.ID)
	return err
}

// DeleteProject removes a project and cascades repo mappings.
func (db *DB) DeleteProject(id string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM project_repos WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM project_qa_sessions WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetProjectRepos replaces all repo mappings for a project.
func (db *DB) SetProjectRepos(projectID string, repos []ProjectRepo) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM project_repos WHERE project_id = ?`, projectID); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO project_repos (project_id, repo_id, alias_name, role) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range repos {
		role := r.Role
		if role == "" {
			role = "service"
		}
		alias := r.AliasName
		if alias == "" {
			alias = r.RepoID
		}
		if _, err := stmt.Exec(projectID, r.RepoID, alias, role); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetProjectRepos returns all repos mapped to a project with their aliases.
func (db *DB) GetProjectRepos(projectID string) ([]RepoWithAlias, error) {
	rows, err := db.conn.Query(`
		SELECT r.id, r.name, r.git_url, r.branch, r.local_path, 
		       COALESCE(r.wiki_dir,''), COALESCE(r.static_dir,''), r.schedule, r.status, 
		       r.created_at, r.updated_at, pr.alias_name, COALESCE(pr.role, 'service')
		FROM project_repos pr
		JOIN repos r ON pr.repo_id = r.id
		WHERE pr.project_id = ?
		ORDER BY pr.alias_name
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RepoWithAlias
	for rows.Next() {
		var item RepoWithAlias
		if err := rows.Scan(
			&item.ID, &item.Name, &item.GitURL, &item.Branch, &item.LocalPath,
			&item.WikiDir, &item.StaticDir, &item.Schedule, &item.Status,
			&item.CreatedAt, &item.UpdatedAt, &item.AliasName, &item.Role,
		); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}
