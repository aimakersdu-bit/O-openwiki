package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var (
	testMu     sync.Mutex
	testDBMap  = make(map[string]string)
	testDBRefs = make(map[string]int)
)

func resolveDSN(dsn string) (string, string, error) {
	if strings.Contains(dsn, "@") {
		return dsn, "", nil
	}

	testMu.Lock()
	defer testMu.Unlock()

	baseDSN := os.Getenv("TEST_DB_DSN")
	if baseDSN == "" {
		baseDSN = "root:@tcp(127.0.0.1:3306)/"
	}

	dbName, exists := testDBMap[dsn]
	if !exists {
		dbName = fmt.Sprintf("test_orch_%d_%d", time.Now().UnixNano(), rand.Intn(1000000))
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

	targetDSN := fmt.Sprintf("root:@tcp(127.0.0.1:3306)/%s?parseTime=true", dbName)
	return targetDSN, dbName, nil
}

type DB struct {
	conn       *sql.DB
	testDBName string
}

// Open opens the MySQL database and runs migrations by default.
func Open(dsn string) (*DB, error) {
	return OpenWithOptions(dsn, true)
}

// OpenWithOptions opens the MySQL database and optionally runs schema migrations.
func OpenWithOptions(dsn string, autoMigrate bool) (*DB, error) {
	targetDSN, testDBName, err := resolveDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("resolve mysql dsn: %w", err)
	}

	conn, err := sql.Open("mysql", targetDSN)
	if err != nil {
		return nil, fmt.Errorf("open mysql db %s: %w", targetDSN, err)
	}
	conn.SetMaxOpenConns(150)
	conn.SetMaxIdleConns(50)
	conn.SetConnMaxLifetime(5 * time.Minute)

	db := &DB{conn: conn, testDBName: testDBName}
	if autoMigrate {
		if err := db.migrate(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("migrate db: %w", err)
		}
	} else if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping mysql db: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	err := db.conn.Close()
	if db.testDBName != "" {
		testMu.Lock()
		testDBRefs[db.testDBName]--
		if testDBRefs[db.testDBName] <= 0 {
			for k, v := range testDBMap {
				if v == db.testDBName {
					delete(testDBMap, k)
				}
			}
			delete(testDBRefs, db.testDBName)

			baseDSN := os.Getenv("TEST_DB_DSN")
			if baseDSN == "" {
				baseDSN = "root:@tcp(127.0.0.1:3306)/"
			}
			adminDB, aErr := sql.Open("mysql", baseDSN)
			if aErr == nil {
				_, _ = adminDB.Exec("DROP DATABASE `" + db.testDBName + "`")
				adminDB.Close()
			}
		}
		testMu.Unlock()
	}
	return err
}

var globalDB *DB

// InitDB initializes the global database connection with auto-migration enabled.
func InitDB(dsn string) error {
	return InitDBWithOptions(dsn, true)
}

// InitDBWithOptions initializes the global database connection and controls startup DDL.
func InitDBWithOptions(dsn string, autoMigrate bool) error {
	var err error
	globalDB, err = OpenWithOptions(dsn, autoMigrate)
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
	// Only mark builds that started before this process launched as interrupted,
	// so freshly created builds (same-second race with Start()) are not clobbered.
	cutoff := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec("UPDATE builds SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP, error = 'Interrupted by daemon restart' WHERE status = 'running' AND started_at <= ?", cutoff)
	if err != nil {
		return err
	}
	_, _ = db.conn.Exec("UPDATE projects SET status = 'failed' WHERE status IN ('building', 'queued') AND updated_at <= ?", cutoff)
	return nil
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

func FinishBuildWithTokens(id int64, status, buildLog, buildErr string, promptTokens, completionTokens, totalTokens, savedTokens int64, retries int) error {
	return globalDB.FinishBuildWithTokens(id, status, buildLog, buildErr, promptTokens, completionTokens, totalTokens, savedTokens, retries)
}

func GetLatestBuilds(repoID string, limit int) ([]Build, error) {
	return globalDB.ListBuilds(repoID, limit)
}

func GetLatestBuild(repoID string) (*Build, error) {
	return globalDB.GetLatestBuild(repoID)
}

func GetLatestFinishedBuild(repoID string) (*Build, error) {
	return globalDB.GetLatestFinishedBuild(repoID)
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

func GetProjectByNameOrID(identifier string) (*Project, error) {
	return globalDB.GetProjectByNameOrID(identifier)
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
			id              VARCHAR(255) PRIMARY KEY,
			name            VARCHAR(255) NOT NULL,
			git_url         VARCHAR(512) NOT NULL,
			branch          VARCHAR(255) NOT NULL DEFAULT 'main',
			local_path      VARCHAR(512) NOT NULL,
			wiki_dir        VARCHAR(512),
			static_dir      VARCHAR(512),
			schedule        VARCHAR(255) NOT NULL DEFAULT '0 2 * * *',
			status          VARCHAR(255) NOT NULL DEFAULT 'active',
			tier            VARCHAR(64) NOT NULL DEFAULT 'B',
			business_domain VARCHAR(255) NOT NULL DEFAULT '',
			last_release_at DATETIME NULL,
			volume_id       VARCHAR(255) NOT NULL DEFAULT '',
			size_bytes      BIGINT NOT NULL DEFAULT 0,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS builds (
			id                   INT AUTO_INCREMENT PRIMARY KEY,
			repo_id              VARCHAR(255) NOT NULL,
			git_head             VARCHAR(255),
			status               VARCHAR(255) NOT NULL DEFAULT 'pending',
			started_at           DATETIME NULL,
			finished_at          DATETIME NULL,
			log                  LONGTEXT,
			error                TEXT,
			rate_limited_retries INT DEFAULT 0,
			prompt_tokens        BIGINT DEFAULT 0,
			completion_tokens    BIGINT DEFAULT 0,
			total_tokens         BIGINT DEFAULT 0,
			saved_tokens         BIGINT DEFAULT 0
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS qa_sessions (
			id         INT AUTO_INCREMENT PRIMARY KEY,
			session_id VARCHAR(255),
			repo_id    VARCHAR(255) NOT NULL,
			user_id    VARCHAR(255) NOT NULL,
			question   TEXT NOT NULL,
			answer     MEDIUMTEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS projects (
			id              VARCHAR(255) PRIMARY KEY,
			name            VARCHAR(255) NOT NULL,
			tri_code        VARCHAR(255) NOT NULL DEFAULT '',
			description     TEXT,
			business_domain VARCHAR(255) NOT NULL DEFAULT '',
			architecture_md MEDIUMTEXT,
			local_path      VARCHAR(512) NOT NULL,
			status          VARCHAR(255) NOT NULL DEFAULT 'active',
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS project_repos (
			id         INT AUTO_INCREMENT PRIMARY KEY,
			project_id VARCHAR(255) NOT NULL,
			repo_id    VARCHAR(255) NOT NULL,
			alias_name VARCHAR(255) NOT NULL,
			role       VARCHAR(255) DEFAULT 'service',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, repo_id),
			UNIQUE(project_id, alias_name)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS project_qa_sessions (
			id         INT AUTO_INCREMENT PRIMARY KEY,
			session_id VARCHAR(255),
			project_id VARCHAR(255) NOT NULL,
			user_id    VARCHAR(255) NOT NULL,
			question   TEXT NOT NULL,
			answer     MEDIUMTEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}

	for _, t := range tables {
		if _, err := db.conn.Exec(t); err != nil {
			return fmt.Errorf("exec table creation: %w", err)
		}
	}

	// Best-effort incremental upgrades for pre-existing tables created without newer columns
	alterStmts := []string{
		`ALTER TABLE qa_sessions ADD COLUMN session_id VARCHAR(255)`,
		`ALTER TABLE repos ADD COLUMN tier VARCHAR(64) NOT NULL DEFAULT 'B'`,
		`ALTER TABLE repos ADD COLUMN business_domain VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE repos ADD COLUMN last_release_at DATETIME NULL`,
		`ALTER TABLE repos ADD COLUMN volume_id VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE repos ADD COLUMN size_bytes BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE projects ADD COLUMN business_domain VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN architecture_md MEDIUMTEXT`,
		`ALTER TABLE projects ADD COLUMN tri_code VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE builds ADD COLUMN rate_limited_retries INT DEFAULT 0`,
		`ALTER TABLE builds ADD COLUMN prompt_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE builds ADD COLUMN completion_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE builds ADD COLUMN total_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE builds ADD COLUMN saved_tokens BIGINT DEFAULT 0`,
		// Upgrade earlier VARCHAR(255)/INT columns if created by an older MySQL migration run
		`ALTER TABLE projects MODIFY COLUMN architecture_md MEDIUMTEXT`,
		`ALTER TABLE builds MODIFY COLUMN prompt_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE builds MODIFY COLUMN completion_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE builds MODIFY COLUMN total_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE builds MODIFY COLUMN saved_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE repos MODIFY COLUMN size_bytes BIGINT NOT NULL DEFAULT 0`,
	}
	for _, stmt := range alterStmts {
		_, _ = db.conn.Exec(stmt)
	}

	batchTables := []string{
		`CREATE TABLE IF NOT EXISTS batch_runs (
			id              VARCHAR(255) PRIMARY KEY,
			name            VARCHAR(255) NOT NULL,
			total_count     INT DEFAULT 0,
			processed_count INT DEFAULT 0,
			success_count   INT DEFAULT 0,
			skipped_count   INT DEFAULT 0,
			failed_count    INT DEFAULT 0,
			status          VARCHAR(255) NOT NULL DEFAULT 'pending',
			max_concurrency INT DEFAULT 2,
			night_mode_only TINYINT(1) DEFAULT 0,
			started_at      DATETIME NULL,
			finished_at     DATETIME NULL,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS batch_tasks (
			id                INT AUTO_INCREMENT PRIMARY KEY,
			batch_id          VARCHAR(255) NOT NULL,
			repo_id           VARCHAR(255) NOT NULL,
			status            VARCHAR(255) NOT NULL DEFAULT 'pending',
			duration_sec      INT DEFAULT 0,
			prompt_tokens     BIGINT DEFAULT 0,
			completion_tokens BIGINT DEFAULT 0,
			total_tokens      BIGINT DEFAULT 0,
			summary           TEXT,
			error             TEXT,
			started_at        DATETIME NULL,
			finished_at       DATETIME NULL,
			UNIQUE(batch_id, repo_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS daily_token_usage (
			date               VARCHAR(64) NOT NULL,
			business_domain    VARCHAR(191) NOT NULL DEFAULT '',
			prompt_tokens      BIGINT DEFAULT 0,
			completion_tokens  BIGINT DEFAULT 0,
			total_tokens       BIGINT DEFAULT 0,
			saved_tokens       BIGINT DEFAULT 0,
			build_count        INT DEFAULT 0,
			skipped_count      INT DEFAULT 0,
			throttled_count    INT DEFAULT 0,
			total_duration_sec BIGINT DEFAULT 0,
			updated_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (date, business_domain)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		"CREATE TABLE IF NOT EXISTS system_settings (\n\t\t\t`key`      VARCHAR(255) PRIMARY KEY,\n\t\t\tvalue      TEXT NOT NULL,\n\t\t\tupdated_at DATETIME DEFAULT CURRENT_TIMESTAMP\n\t\t) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		`CREATE TABLE IF NOT EXISTS volumes (
			id          VARCHAR(255) PRIMARY KEY,
			mount_point VARCHAR(512) NOT NULL,
			total_bytes BIGINT NOT NULL DEFAULT 0,
			used_bytes  BIGINT NOT NULL DEFAULT 0,
			status      VARCHAR(64) NOT NULL DEFAULT 'active',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS volume_migrations (
			id             INT AUTO_INCREMENT PRIMARY KEY,
			repo_id        VARCHAR(255) NOT NULL,
			from_volume_id VARCHAR(255) NOT NULL,
			to_volume_id   VARCHAR(255) NOT NULL,
			status         VARCHAR(64) NOT NULL DEFAULT 'planned',
			error          TEXT,
			started_at     DATETIME NULL,
			finished_at    DATETIME NULL,
			created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}

	for _, bt := range batchTables {
		if _, err := db.conn.Exec(bt); err != nil {
			return fmt.Errorf("exec batch table creation: %w", err)
		}
	}

	// Incremental upgrades for batch/volume/usage tables from older schemas
	batchAlterStmts := []string{
		`ALTER TABLE daily_token_usage ADD COLUMN business_domain VARCHAR(191) NOT NULL DEFAULT ''`,
		`ALTER TABLE daily_token_usage ADD COLUMN throttled_count INT DEFAULT 0`,
		`ALTER TABLE daily_token_usage ADD COLUMN total_duration_sec BIGINT DEFAULT 0`,
		`ALTER TABLE daily_token_usage MODIFY COLUMN prompt_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE daily_token_usage MODIFY COLUMN completion_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE daily_token_usage MODIFY COLUMN total_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE daily_token_usage MODIFY COLUMN saved_tokens BIGINT DEFAULT 0`,
		`ALTER TABLE daily_token_usage MODIFY COLUMN total_duration_sec BIGINT DEFAULT 0`,
		`ALTER TABLE volumes MODIFY COLUMN total_bytes BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE volumes MODIFY COLUMN used_bytes BIGINT NOT NULL DEFAULT 0`,
	}
	for _, stmt := range batchAlterStmts {
		_, _ = db.conn.Exec(stmt)
	}

	defaultSettings := map[string]string{
		"max_tpm":               "0",
		"max_rpm":               "0",
		"day_max_concurrency":   "2",
		"night_max_concurrency": "4",
		"llm_timeout_sec":       "600",
		"llm_proxy_max_retries": "6",
	}
	for k, v := range defaultSettings {
		db.conn.Exec("INSERT IGNORE INTO system_settings (`key`, value) VALUES (?, ?)", k, v)
	}

	indices := []string{
		`CREATE INDEX idx_builds_repo ON builds(repo_id)`,
		`CREATE INDEX idx_qa_repo_user ON qa_sessions(repo_id, user_id)`,
		`CREATE INDEX idx_qa_session_id ON qa_sessions(session_id)`,
		`CREATE INDEX idx_project_repos_proj ON project_repos(project_id)`,
		`CREATE INDEX idx_project_qa_proj ON project_qa_sessions(project_id)`,
		`CREATE INDEX idx_project_qa_sess ON project_qa_sessions(session_id)`,
		`CREATE INDEX idx_batch_tasks_batch ON batch_tasks(batch_id)`,
		`CREATE INDEX idx_batch_tasks_status ON batch_tasks(batch_id, status)`,
	}

	for _, idx := range indices {
		// Ignore duplicate-index errors for idempotent migration
		if _, err := db.conn.Exec(idx); err != nil {
			// MySQL error 1061: Duplicate key name
			if !strings.Contains(err.Error(), "1061") {
				return fmt.Errorf("exec index creation: %w", err)
			}
		}
	}

	return nil
}

// --- Repo CRUD ---

// Repo represents a registered repository.
type Repo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	GitURL         string `json:"git_url"`
	Branch         string `json:"branch"`
	LocalPath      string `json:"local_path"`
	WikiDir        string `json:"wiki_dir"`
	StaticDir      string `json:"static_dir"`
	Schedule       string `json:"schedule"`
	Status         string `json:"status"`
	Tier           string `json:"tier"`            // A, B, C 等级
	BusinessDomain string `json:"business_domain"` // 所属微服务业务域
	LastReleaseAt  string `json:"last_release_at"` // 最近发版时间
	VolumeID       string `json:"volume_id"`       // 所在物理数据盘ID (如 vol1, vol2)
	SizeBytes      int64  `json:"size_bytes"`      // 仓库实际占用磁盘字节数
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func nullIfEmpty(s string) interface{} {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

// ListRepos returns all registered repositories.
func (db *DB) ListRepos() ([]Repo, error) {
	rows, err := db.conn.Query(`SELECT id, name, git_url, branch, local_path, 
		COALESCE(wiki_dir,''), COALESCE(static_dir,''), schedule, status, 
		COALESCE(tier,'B'), COALESCE(business_domain,''), COALESCE(DATE_FORMAT(last_release_at, '%Y-%m-%d %H:%i:%s'),''),
		COALESCE(volume_id,''), COALESCE(size_bytes, 0),
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
			&r.Tier, &r.BusinessDomain, &r.LastReleaseAt,
			&r.VolumeID, &r.SizeBytes,
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
		COALESCE(tier,'B'), COALESCE(business_domain,''), COALESCE(DATE_FORMAT(last_release_at, '%Y-%m-%d %H:%i:%s'),''),
		COALESCE(volume_id,''), COALESCE(size_bytes, 0),
		created_at, updated_at FROM repos WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.GitURL, &r.Branch, &r.LocalPath,
			&r.WikiDir, &r.StaticDir, &r.Schedule, &r.Status,
			&r.Tier, &r.BusinessDomain, &r.LastReleaseAt,
			&r.VolumeID, &r.SizeBytes,
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
		COALESCE(tier,'B'), COALESCE(business_domain,''), COALESCE(DATE_FORMAT(last_release_at, '%Y-%m-%d %H:%i:%s'),''),
		COALESCE(volume_id,''), COALESCE(size_bytes, 0),
		created_at, updated_at FROM repos WHERE name = ? LIMIT 1`, identifier).
		Scan(&r.ID, &r.Name, &r.GitURL, &r.Branch, &r.LocalPath,
			&r.WikiDir, &r.StaticDir, &r.Schedule, &r.Status,
			&r.Tier, &r.BusinessDomain, &r.LastReleaseAt,
			&r.VolumeID, &r.SizeBytes,
			&r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// CreateRepo inserts a new repository.
func (db *DB) CreateRepo(r *Repo) error {
	tier := r.Tier
	if tier == "" {
		tier = "B"
	}
	_, err := db.conn.Exec(`INSERT INTO repos (id, name, git_url, branch, local_path, wiki_dir, static_dir, schedule, status, tier, business_domain, last_release_at, volume_id, size_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.GitURL, r.Branch, r.LocalPath, r.WikiDir, r.StaticDir, r.Schedule, r.Status,
		tier, r.BusinessDomain, nullIfEmpty(r.LastReleaseAt), r.VolumeID, r.SizeBytes)
	return err
}

// UpdateRepo updates repository fields and updated_at timestamp.
func (db *DB) UpdateRepo(r *Repo) error {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	tier := r.Tier
	if tier == "" {
		tier = "B"
	}
	_, err := db.conn.Exec(`UPDATE repos SET name = ?, git_url = ?, branch = ?, local_path = ?, wiki_dir = ?, static_dir = ?, schedule = ?, status = ?, tier = ?, business_domain = ?, last_release_at = ?, volume_id = ?, size_bytes = ?, updated_at = ? WHERE id = ?`,
		r.Name, r.GitURL, r.Branch, r.LocalPath, r.WikiDir, r.StaticDir, r.Schedule, r.Status, tier, r.BusinessDomain, nullIfEmpty(r.LastReleaseAt), r.VolumeID, r.SizeBytes, timeStr, r.ID)
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
	ID                 int64  `json:"id"`
	RepoID             string `json:"repo_id"`
	GitHead            string `json:"git_head"`
	Status             string `json:"status"`
	StartedAt          string `json:"started_at"`
	FinishedAt         string `json:"finished_at"`
	Log                string `json:"log"`
	Error              string `json:"error"`
	RateLimitedRetries int    `json:"rate_limited_retries"`
	PromptTokens       int64  `json:"prompt_tokens"`
	CompletionTokens   int64  `json:"completion_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	SavedTokens        int64  `json:"saved_tokens"`
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

// FinishBuildWithTokens updates a build record with status, logs, real tokens, and retry counts.
func (db *DB) FinishBuildWithTokens(id int64, status, buildLog, buildErr string, promptTokens, completionTokens, totalTokens, savedTokens int64, retries int) error {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`UPDATE builds SET status = ?, finished_at = ?, log = ?, error = ?,
		prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, saved_tokens = ?, rate_limited_retries = ?
		WHERE id = ?`,
		status, timeStr, buildLog, buildErr, promptTokens, completionTokens, totalTokens, savedTokens, retries, id)
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
		COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(log,''), COALESCE(error,''),
		COALESCE(rate_limited_retries, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0), COALESCE(saved_tokens, 0)
		FROM builds WHERE repo_id = ? ORDER BY id DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var builds []Build
	for rows.Next() {
		var b Build
		if err := rows.Scan(&b.ID, &b.RepoID, &b.GitHead, &b.Status,
			&b.StartedAt, &b.FinishedAt, &b.Log, &b.Error,
			&b.RateLimitedRetries, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens, &b.SavedTokens); err != nil {
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

// GetLatestFinishedBuild returns the most recent build for a repo that has finished (status: completed, failed, or skipped).
func (db *DB) GetLatestFinishedBuild(repoID string) (*Build, error) {
	query := `SELECT id, repo_id, COALESCE(git_head,''), status,
		COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(log,''), COALESCE(error,''),
		COALESCE(rate_limited_retries, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0), COALESCE(saved_tokens, 0)
		FROM builds
		WHERE repo_id = ? AND status IN ('completed', 'failed', 'skipped')
		ORDER BY id DESC LIMIT 1`

	row := db.conn.QueryRow(query, repoID)
	var b Build
	if err := row.Scan(&b.ID, &b.RepoID, &b.GitHead, &b.Status,
		&b.StartedAt, &b.FinishedAt, &b.Log, &b.Error,
		&b.RateLimitedRetries, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens, &b.SavedTokens); err != nil {
		if errors.Is(err, sql.ErrNoRows) || err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
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
				COALESCE(session_id, CONCAT('legacy_', id)) as sess_id,
				MIN(repo_id) as repo_id,
				MIN(user_id) as user_id,
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
				COALESCE(session_id, CONCAT('legacy_', id)) as sess_id,
				MIN(repo_id) as repo_id,
				MIN(user_id) as user_id,
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
			FROM qa_sessions WHERE (session_id = ? OR (session_id IS NULL AND CONCAT('legacy_', id) = ?)) AND user_id = ?
			UNION ALL
			SELECT id, COALESCE(session_id,''), project_id, user_id, question, COALESCE(answer,''), created_at
			FROM project_qa_sessions WHERE (session_id = ? OR (session_id IS NULL AND CONCAT('legacy_', id) = ?)) AND user_id = ?
			ORDER BY id ASC`
		rows, err = db.conn.Query(query, sessionID, sessionID, userID, sessionID, sessionID, userID)
	} else {
		query := `
			SELECT id, COALESCE(session_id,''), repo_id, user_id, question, COALESCE(answer,''), created_at
			FROM qa_sessions WHERE session_id = ? OR (session_id IS NULL AND CONCAT('legacy_', id) = ?)
			UNION ALL
			SELECT id, COALESCE(session_id,''), project_id, user_id, question, COALESCE(answer,''), created_at
			FROM project_qa_sessions WHERE session_id = ? OR (session_id IS NULL AND CONCAT('legacy_', id) = ?)
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
	_, err1 := db.conn.Exec(`DELETE FROM qa_sessions WHERE (session_id = ? OR CONCAT('legacy_', id) = ?) AND (user_id = ? OR ? = '')`, sessionID, sessionID, userID, userID)
	_, err2 := db.conn.Exec(`DELETE FROM project_qa_sessions WHERE (session_id = ? OR CONCAT('legacy_', id) = ?) AND (user_id = ? OR ? = '')`, sessionID, sessionID, userID, userID)
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
				COALESCE(session_id, CONCAT('legacy_', id)) as sess_id,
				MIN(project_id) as project_id,
				MIN(user_id) as user_id,
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
				COALESCE(session_id, CONCAT('legacy_', id)) as sess_id,
				MIN(project_id) as project_id,
				MIN(user_id) as user_id,
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
	ID             string `json:"id"`
	Name           string `json:"name"`
	TriCode        string `json:"tri_code"`
	Description    string `json:"description"`
	BusinessDomain string `json:"business_domain"`
	ArchitectureMD string `json:"architecture_md"`
	LocalPath      string `json:"local_path"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
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
	rows, err := db.conn.Query(`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.TriCode, &p.Description, &p.BusinessDomain, &p.ArchitectureMD, &p.LocalPath, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProject returns a single project by ID.
func (db *DB) GetProject(id string) (*Project, error) {
	p := &Project{}
	err := db.conn.QueryRow(`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.TriCode, &p.Description, &p.BusinessDomain, &p.ArchitectureMD, &p.LocalPath, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// GetProjectByNameOrID resolves a microservice project by ID, tri_code (three-letter code),
// semantic proj-<tri_code>, exact name, or business_domain.
func (db *DB) GetProjectByNameOrID(identifier string) (*Project, error) {
	ident := strings.TrimSpace(identifier)
	if ident == "" {
		return nil, nil
	}

	queries := []struct {
		sql string
		arg string
	}{
		{`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE id = ? LIMIT 1`, ident},
		{`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE LOWER(id) = LOWER(?) LIMIT 1`, ident},
		{`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE UPPER(tri_code) = UPPER(?) AND tri_code != '' LIMIT 1`, ident},
		{`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE LOWER(id) = LOWER(?) LIMIT 1`, "proj-" + ident},
		{`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE LOWER(name) = LOWER(?) LIMIT 1`, ident},
		{`SELECT id, name, COALESCE(tri_code,''), COALESCE(description,''), COALESCE(business_domain,''), COALESCE(architecture_md,''), local_path, status, created_at, updated_at FROM projects WHERE business_domain = ? ORDER BY updated_at DESC LIMIT 1`, ident},
	}

	for _, q := range queries {
		p := &Project{}
		err := db.conn.QueryRow(q.sql, q.arg).
			Scan(&p.ID, &p.Name, &p.TriCode, &p.Description, &p.BusinessDomain, &p.ArchitectureMD, &p.LocalPath, &p.Status, &p.CreatedAt, &p.UpdatedAt)
		if err == nil {
			return p, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	return nil, nil
}

// GetProjectIDsByRepoIDs returns distinct project IDs containing any of the given repo IDs.
func (db *DB) GetProjectIDsByRepoIDs(repoIDs []string) ([]string, error) {
	if len(repoIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(repoIDs))
	args := make([]interface{}, len(repoIDs))
	for i, id := range repoIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT DISTINCT project_id FROM project_repos WHERE repo_id IN (%s) ORDER BY project_id`, strings.Join(placeholders, ","))
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		ids = append(ids, pid)
	}
	return ids, rows.Err()
}

// CreateProject inserts a new project.
func (db *DB) CreateProject(p *Project) error {
	_, err := db.conn.Exec(`INSERT INTO projects (id, name, tri_code, description, business_domain, architecture_md, local_path, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.TriCode, p.Description, p.BusinessDomain, p.ArchitectureMD, p.LocalPath, p.Status)
	return err
}

// UpdateProject updates project fields and updated_at timestamp.
func (db *DB) UpdateProject(p *Project) error {
	timeStr := time.Now().Local().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(`UPDATE projects SET name = ?, tri_code = ?, description = ?, business_domain = ?, architecture_md = ?, local_path = ?, status = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.TriCode, p.Description, p.BusinessDomain, p.ArchitectureMD, p.LocalPath, p.Status, timeStr, p.ID)
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
		       r.created_at, r.updated_at, pr.alias_name, COALESCE(pr.role, 'service'),
		       COALESCE(r.tier,'B'), COALESCE(r.business_domain,''), COALESCE(DATE_FORMAT(r.last_release_at, '%Y-%m-%d %H:%i:%s'),''),
		       COALESCE(r.volume_id,'disk0'), COALESCE(r.size_bytes,0)
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
			&item.Tier, &item.BusinessDomain, &item.LastReleaseAt,
			&item.VolumeID, &item.SizeBytes,
		); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// --- Batch & Daily Token Usage ---

type BatchRun struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	TotalCount     int    `json:"total_count"`
	ProcessedCount int    `json:"processed_count"`
	SuccessCount   int    `json:"success_count"`
	SkippedCount   int    `json:"skipped_count"`
	FailedCount    int    `json:"failed_count"`
	Status         string `json:"status"`
	MaxConcurrency int    `json:"max_concurrency"`
	NightModeOnly  bool   `json:"night_mode_only"`
	StartedAt      string `json:"started_at,omitempty"`
	FinishedAt     string `json:"finished_at,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type BatchTask struct {
	ID               int64  `json:"id"`
	BatchID          string `json:"batch_id"`
	RepoID           string `json:"repo_id"`
	Status           string `json:"status"`
	DurationSec      int    `json:"duration_sec"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Summary          string `json:"summary,omitempty"`
	Error            string `json:"error,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	FinishedAt       string `json:"finished_at,omitempty"`
}

type DailyTokenUsage struct {
	Date             string `json:"date"`
	BusinessDomain   string `json:"business_domain"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	SavedTokens      int64  `json:"saved_tokens"`
	BuildCount       int    `json:"build_count"`
	SkippedCount     int    `json:"skipped_count"`
	ThrottledCount   int    `json:"throttled_count"`
	TotalDurationSec int    `json:"total_duration_sec"`
	UpdatedAt        string `json:"updated_at"`
}

func (db *DB) CreateBatchRun(r *BatchRun) error {
	if r.MaxConcurrency <= 0 {
		r.MaxConcurrency = 2
	}
	_, err := db.conn.Exec(`INSERT INTO batch_runs (id, name, total_count, processed_count, success_count, skipped_count, failed_count, status, max_concurrency, night_mode_only, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, r.ID, r.Name, r.TotalCount, r.ProcessedCount, r.SuccessCount, r.SkippedCount, r.FailedCount, r.Status, r.MaxConcurrency, r.NightModeOnly)
	return err
}

func (db *DB) UpdateBatchConcurrency(batchID string, concurrency int) error {
	_, err := db.conn.Exec(`UPDATE batch_runs SET max_concurrency = ? WHERE id = ?`, concurrency, batchID)
	return err
}

func (db *DB) UpdateBatchRunStatus(batchID, status string) error {
	var err error
	if status == "completed" || status == "failed" {
		_, err = db.conn.Exec(`UPDATE batch_runs SET status = ?, finished_at = CURRENT_TIMESTAMP WHERE id = ?`, status, batchID)
	} else {
		_, err = db.conn.Exec(`UPDATE batch_runs SET status = ? WHERE id = ?`, status, batchID)
	}
	return err
}

func (db *DB) GetBatchRun(id string) (*BatchRun, error) {
	row := db.conn.QueryRow(`SELECT id, name, total_count, processed_count, success_count, skipped_count, failed_count, 
		status, max_concurrency, night_mode_only, COALESCE(started_at,''), COALESCE(finished_at,''), created_at 
		FROM batch_runs WHERE id = ?`, id)
	var r BatchRun
	err := row.Scan(&r.ID, &r.Name, &r.TotalCount, &r.ProcessedCount, &r.SuccessCount, &r.SkippedCount, &r.FailedCount,
		&r.Status, &r.MaxConcurrency, &r.NightModeOnly, &r.StartedAt, &r.FinishedAt, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetLatestActiveBatchRun retrieves the latest batch run, prioritizing active/running/pending ones.
func (db *DB) GetLatestActiveBatchRun() (*BatchRun, error) {
	row := db.conn.QueryRow(`SELECT id, name, total_count, processed_count, success_count, skipped_count, failed_count, 
		status, max_concurrency, night_mode_only, COALESCE(started_at,''), COALESCE(finished_at,''), created_at 
		FROM batch_runs ORDER BY 
		CASE WHEN status IN ('running', 'pending', 'paused') THEN 0 ELSE 1 END,
		created_at DESC LIMIT 1`)
	var r BatchRun
	err := row.Scan(&r.ID, &r.Name, &r.TotalCount, &r.ProcessedCount, &r.SuccessCount, &r.SkippedCount, &r.FailedCount,
		&r.Status, &r.MaxConcurrency, &r.NightModeOnly, &r.StartedAt, &r.FinishedAt, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// SetBatchNightMode updates the night_mode_only setting for a batch run.
func (db *DB) SetBatchNightMode(batchID string, nightMode bool) error {
	_, err := db.conn.Exec(`UPDATE batch_runs SET night_mode_only = ? WHERE id = ?`, nightMode, batchID)
	return err
}

func (db *DB) CreateBatchTask(t *BatchTask) error {
	_, err := db.conn.Exec(`INSERT INTO batch_tasks (batch_id, repo_id, status, duration_sec, prompt_tokens, completion_tokens, total_tokens) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.BatchID, t.RepoID, t.Status, t.DurationSec, t.PromptTokens, t.CompletionTokens, t.TotalTokens)
	return err
}

func (db *DB) UpdateBatchTaskStatus(batchID, repoID, status, summary string, durationSec int, tokens ...int64) error {
	var promptTokens, completionTokens, totalTokens int64
	if len(tokens) >= 3 {
		promptTokens = tokens[0]
		completionTokens = tokens[1]
		totalTokens = tokens[2]
	}

	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		err := func() error {
			tx, err := db.conn.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()

			if _, err := tx.Exec(`UPDATE batch_tasks SET status = ?, summary = ?, duration_sec = ?, 
				prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, finished_at = CURRENT_TIMESTAMP
				WHERE batch_id = ? AND repo_id = ?`, status, summary, durationSec, promptTokens, completionTokens, totalTokens, batchID, repoID); err != nil {
				return err
			}

			// 重新聚合更新 batch_runs 的计数与状态
			if _, err := tx.Exec(`UPDATE batch_runs SET 
				processed_count = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status IN ('completed','skipped','failed')),
				success_count   = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'completed'),
				skipped_count   = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'skipped'),
				failed_count    = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'failed')
				WHERE id = ?`, batchID, batchID, batchID, batchID, batchID); err != nil {
				return err
			}

			return tx.Commit()
		}()

		if err == nil {
			return nil
		}
		lastErr = err
		if strings.Contains(err.Error(), "1213") || strings.Contains(err.Error(), "Deadlock") {
			time.Sleep(time.Duration(10+rand.Intn(40)) * time.Millisecond)
			continue
		}
		return err
	}
	return lastErr
}

func (db *DB) ListBatchTasks(batchID string) ([]BatchTask, error) {
	rows, err := db.conn.Query(`SELECT id, batch_id, repo_id, status, duration_sec, prompt_tokens, completion_tokens, total_tokens, 
		COALESCE(summary,''), COALESCE(error,''), COALESCE(started_at,''), COALESCE(finished_at,'')
		FROM batch_tasks WHERE batch_id = ? ORDER BY id ASC`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []BatchTask
	for rows.Next() {
		var t BatchTask
		if err := rows.Scan(&t.ID, &t.BatchID, &t.RepoID, &t.Status, &t.DurationSec,
			&t.PromptTokens, &t.CompletionTokens, &t.TotalTokens,
			&t.Summary, &t.Error, &t.StartedAt, &t.FinishedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (db *DB) RecordDailyTokenUsage(u *DailyTokenUsage) error {
	domain := u.BusinessDomain
	_, err := db.conn.Exec(`INSERT INTO daily_token_usage (date, business_domain, prompt_tokens, completion_tokens, total_tokens, saved_tokens, build_count, skipped_count, throttled_count, total_duration_sec, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON DUPLICATE KEY UPDATE
			prompt_tokens = prompt_tokens + ?,
			completion_tokens = completion_tokens + ?,
			total_tokens = total_tokens + ?,
			saved_tokens = saved_tokens + ?,
			build_count = build_count + ?,
			skipped_count = skipped_count + ?,
			throttled_count = throttled_count + ?,
			total_duration_sec = total_duration_sec + ?,
			updated_at = CURRENT_TIMESTAMP`,
		u.Date, domain, u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.SavedTokens, u.BuildCount, u.SkippedCount, u.ThrottledCount, u.TotalDurationSec,
		u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.SavedTokens, u.BuildCount, u.SkippedCount, u.ThrottledCount, u.TotalDurationSec)
	return err
}

func (db *DB) ListDailyTokenUsage(limit int, domainFilter ...string) ([]DailyTokenUsage, error) {
	if limit <= 0 {
		limit = 30
	}
	var rows *sql.Rows
	var err error
	if len(domainFilter) > 0 && domainFilter[0] != "" {
		rows, err = db.conn.Query(`SELECT date, COALESCE(business_domain, ''), prompt_tokens, completion_tokens, total_tokens, saved_tokens, build_count, skipped_count, COALESCE(throttled_count, 0), COALESCE(total_duration_sec, 0), updated_at
			FROM daily_token_usage WHERE business_domain = ? ORDER BY date DESC LIMIT ?`, domainFilter[0], limit)
	} else {
		rows, err = db.conn.Query(`SELECT date, COALESCE(business_domain, ''), prompt_tokens, completion_tokens, total_tokens, saved_tokens, build_count, skipped_count, COALESCE(throttled_count, 0), COALESCE(total_duration_sec, 0), updated_at
			FROM daily_token_usage ORDER BY date DESC, business_domain ASC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DailyTokenUsage
	for rows.Next() {
		var u DailyTokenUsage
		if err := rows.Scan(&u.Date, &u.BusinessDomain, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.SavedTokens, &u.BuildCount, &u.SkippedCount, &u.ThrottledCount, &u.TotalDurationSec, &u.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, nil
}

// RecoverInterruptedBatchTasks resets any residual running batch tasks to pending upon startup.
func (db *DB) RecoverInterruptedBatchTasks() (int64, error) {
	res, err := db.conn.Exec(`UPDATE batch_tasks SET status = 'pending' WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[batch_scheduler] Reset %d interrupted running tasks to pending on startup", n)
	}
	return n, nil
}

// ResetBatchFailedTasks resets failed (and optionally stale running) tasks in a batch back to pending,
// refreshes batch_runs counters, and clears finished_at so the batch can be re-scheduled.
func (db *DB) ResetBatchFailedTasks(batchID string, repoIDs []string, includeStaleRunning bool) (int64, int64, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	statusFilter := "status = 'failed'"
	if includeStaleRunning {
		statusFilter = "status IN ('failed', 'running')"
	}

	var args []interface{}
	query := fmt.Sprintf(`UPDATE batch_tasks
		SET status = 'pending', summary = '', error = '', duration_sec = 0,
		    prompt_tokens = 0, completion_tokens = 0, total_tokens = 0,
		    started_at = NULL, finished_at = NULL
		WHERE batch_id = ? AND %s`, statusFilter)
	args = append(args, batchID)

	if len(repoIDs) > 0 {
		placeholders := make([]string, 0, len(repoIDs))
		for _, id := range repoIDs {
			trimmed := strings.TrimSpace(id)
			if trimmed == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, trimmed)
		}
		if len(placeholders) > 0 {
			query += " AND repo_id IN (" + strings.Join(placeholders, ",") + ")"
		}
	}

	res, err := tx.Exec(query, args...)
	if err != nil {
		return 0, 0, err
	}
	resetCount, _ := res.RowsAffected()

	var pendingTotal int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'pending'`, batchID).Scan(&pendingTotal); err != nil {
		return 0, 0, err
	}
	if pendingTotal == 0 {
		return 0, 0, fmt.Errorf("no failed or pending tasks to retry")
	}

	if _, err := tx.Exec(`UPDATE batch_runs SET
		status = 'pending',
		finished_at = NULL,
		processed_count = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status IN ('completed','skipped','failed')),
		success_count   = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'completed'),
		skipped_count   = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'skipped'),
		failed_count    = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'failed')
		WHERE id = ?`, batchID, batchID, batchID, batchID, batchID); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return resetCount, pendingTotal, nil
}

// --- System Settings DAO ---

// GetSetting retrieves a setting value by key, or returns defaultVal if not found.
func (db *DB) GetSetting(key string, defaultVal ...string) (string, error) {
	var val string
	err := db.conn.QueryRow("SELECT value FROM system_settings WHERE `key` = ?", key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) && len(defaultVal) > 0 {
			return defaultVal[0], nil
		}
		return "", err
	}
	return val, nil
}

// SetSetting upserts a setting key/value.
func (db *DB) SetSetting(key, val string) error {
	_, err := db.conn.Exec("INSERT INTO system_settings (`key`, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)\n\t\tON DUPLICATE KEY UPDATE value = ?, updated_at = CURRENT_TIMESTAMP", key, val, val)
	return err
}

// GetAllSettings returns all key/value pairs from system_settings.
func (db *DB) GetAllSettings() (map[string]string, error) {
	rows, err := db.conn.Query("SELECT `key`, value FROM system_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, rows.Err()
}

func GetSetting(key string, defaultVal ...string) (string, error) {
	return globalDB.GetSetting(key, defaultVal...)
}

func SetSetting(key, val string) error {
	return globalDB.SetSetting(key, val)
}

func GetAllSettings() (map[string]string, error) {
	return globalDB.GetAllSettings()
}

// --- Storage Volumes & Migrations ---

type Volume struct {
	ID         string `json:"id"`
	MountPoint string `json:"mount_point"`
	TotalBytes int64  `json:"total_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
	Status     string `json:"status"` // "active", "read_only", "disabled"
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type VolumeMigration struct {
	ID           int64  `json:"id"`
	RepoID       string `json:"repo_id"`
	FromVolumeID string `json:"from_volume_id"`
	ToVolumeID   string `json:"to_volume_id"`
	Status       string `json:"status"` // planned, syncing, frozen, delta, verifying, switched, retained, rollback
	Error        string `json:"error"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// CreateVolume inserts or updates a storage volume.
func (db *DB) CreateVolume(v *Volume) error {
	status := v.Status
	if status == "" {
		status = "active"
	}
	_, err := db.conn.Exec(`INSERT INTO volumes (id, mount_point, total_bytes, used_bytes, status)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			mount_point = ?,
			total_bytes = ?,
			used_bytes = ?,
			status = ?,
			updated_at = CURRENT_TIMESTAMP`,
		v.ID, v.MountPoint, v.TotalBytes, v.UsedBytes, status,
		v.MountPoint, v.TotalBytes, v.UsedBytes, status)
	return err
}

// DeleteVolume removes a storage volume and resets volume_id on mapped repositories.
func (db *DB) DeleteVolume(id string) error {
	_, _ = db.conn.Exec(`UPDATE repos SET volume_id = '', updated_at = CURRENT_TIMESTAMP WHERE volume_id = ?`, id)
	_, err := db.conn.Exec(`DELETE FROM volumes WHERE id = ?`, id)
	return err
}

// GetVolume retrieves a volume by ID.
func (db *DB) GetVolume(id string) (*Volume, error) {
	var v Volume
	err := db.conn.QueryRow(`SELECT id, mount_point, total_bytes, used_bytes, status, created_at, updated_at
		FROM volumes WHERE id = ?`, id).
		Scan(&v.ID, &v.MountPoint, &v.TotalBytes, &v.UsedBytes, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ListVolumes returns all configured storage volumes.
func (db *DB) ListVolumes() ([]Volume, error) {
	rows, err := db.conn.Query(`SELECT id, mount_point, total_bytes, used_bytes, status, created_at, updated_at
		FROM volumes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Volume
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.ID, &v.MountPoint, &v.TotalBytes, &v.UsedBytes, &v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// UpdateVolumeUsage updates the observed used and total capacity of a volume.
func (db *DB) UpdateVolumeUsage(id string, usedBytes, totalBytes int64) error {
	_, err := db.conn.Exec(`UPDATE volumes SET used_bytes = ?, total_bytes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		usedBytes, totalBytes, id)
	return err
}

// UpdateRepoVolumeAndSize updates the volume_id and/or size_bytes of a repository.
func (db *DB) UpdateRepoVolumeAndSize(repoID, volumeID string, sizeBytes int64) error {
	if volumeID != "" {
		_, err := db.conn.Exec(`UPDATE repos SET volume_id = ?, size_bytes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			volumeID, sizeBytes, repoID)
		return err
	}
	_, err := db.conn.Exec(`UPDATE repos SET size_bytes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		sizeBytes, repoID)
	return err
}

// GetRepoCountByVolume returns the number of repositories mapped to each volume.
func (db *DB) GetRepoCountByVolume() (map[string]int, error) {
	rows, err := db.conn.Query(`SELECT volume_id, COUNT(*) FROM repos WHERE volume_id != '' GROUP BY volume_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var volID string
		var cnt int
		if err := rows.Scan(&volID, &cnt); err != nil {
			return nil, err
		}
		counts[volID] = cnt
	}
	return counts, rows.Err()
}

// CreateVolumeMigration inserts a new volume migration record.
func (db *DB) CreateVolumeMigration(m *VolumeMigration) (int64, error) {
	res, err := db.conn.Exec(`INSERT INTO volume_migrations (repo_id, from_volume_id, to_volume_id, status, error)
		VALUES (?, ?, ?, ?, ?)`, m.RepoID, m.FromVolumeID, m.ToVolumeID, m.Status, m.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateVolumeMigration updates status, error, and timestamps of a migration.
func (db *DB) UpdateVolumeMigration(id int64, status, errMsg string) error {
	var finishedClause string
	if status == "switched" || status == "retained" || status == "rollback" {
		finishedClause = ", finished_at = CURRENT_TIMESTAMP"
	} else if status == "syncing" {
		finishedClause = ", started_at = CURRENT_TIMESTAMP"
	}
	query := fmt.Sprintf(`UPDATE volume_migrations SET status = ?, error = ?, updated_at = CURRENT_TIMESTAMP%s WHERE id = ?`, finishedClause)
	_, err := db.conn.Exec(query, status, errMsg, id)
	return err
}

// GetVolumeMigration retrieves a migration by ID.
func (db *DB) GetVolumeMigration(id int64) (*VolumeMigration, error) {
	var m VolumeMigration
	var errStr, startedAt, finishedAt sql.NullString
	err := db.conn.QueryRow(`SELECT id, repo_id, from_volume_id, to_volume_id, status, error, started_at, finished_at, created_at, updated_at
		FROM volume_migrations WHERE id = ?`, id).
		Scan(&m.ID, &m.RepoID, &m.FromVolumeID, &m.ToVolumeID, &m.Status, &errStr, &startedAt, &finishedAt, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Error = errStr.String
	m.StartedAt = startedAt.String
	m.FinishedAt = finishedAt.String
	return &m, nil
}

// ListVolumeMigrations returns all migration records.
func (db *DB) ListVolumeMigrations() ([]VolumeMigration, error) {
	rows, err := db.conn.Query(`SELECT id, repo_id, from_volume_id, to_volume_id, status, error, started_at, finished_at, created_at, updated_at
		FROM volume_migrations ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []VolumeMigration
	for rows.Next() {
		var m VolumeMigration
		var errStr, startedAt, finishedAt sql.NullString
		if err := rows.Scan(&m.ID, &m.RepoID, &m.FromVolumeID, &m.ToVolumeID, &m.Status, &errStr, &startedAt, &finishedAt, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Error = errStr.String
		m.StartedAt = startedAt.String
		m.FinishedAt = finishedAt.String
		list = append(list, m)
	}
	return list, rows.Err()
}
