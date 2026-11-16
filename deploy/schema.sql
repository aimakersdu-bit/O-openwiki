-- OpenWiki MySQL 5.7+ / 8.0+ Baseline Schema (Unit 1 Orchestrator + Unit 2 Portal)
-- Usage:
--   1. In bank production where app DB users only have DML privileges, ask DBA to execute this file once,
--      then set DB_AUTO_MIGRATE=false for both orchestrator and portal.
--   2. Ensure DSN includes parseTime=true (e.g. user:pass@tcp(host:3306)/openwiki?parseTime=true&charset=utf8mb4).

-- ============================================================================
-- Unit 1: Orchestrator Tables
-- ============================================================================

CREATE TABLE IF NOT EXISTS repos (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS builds (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS qa_sessions (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    session_id VARCHAR(255),
    repo_id    VARCHAR(255) NOT NULL,
    user_id    VARCHAR(255) NOT NULL,
    question   TEXT NOT NULL,
    answer     MEDIUMTEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS projects (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS project_repos (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    project_id VARCHAR(255) NOT NULL,
    repo_id    VARCHAR(255) NOT NULL,
    alias_name VARCHAR(255) NOT NULL,
    role       VARCHAR(255) DEFAULT 'service',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_project_repo (project_id, repo_id),
    UNIQUE KEY uk_project_alias (project_id, alias_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS project_qa_sessions (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    session_id VARCHAR(255),
    project_id VARCHAR(255) NOT NULL,
    user_id    VARCHAR(255) NOT NULL,
    question   TEXT NOT NULL,
    answer     MEDIUMTEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS batch_runs (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS batch_tasks (
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
    UNIQUE KEY uk_batch_repo (batch_id, repo_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS daily_token_usage (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS system_settings (
    `key`      VARCHAR(255) PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS volumes (
    id          VARCHAR(255) PRIMARY KEY,
    mount_point VARCHAR(512) NOT NULL,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    used_bytes  BIGINT NOT NULL DEFAULT 0,
    status      VARCHAR(64) NOT NULL DEFAULT 'active',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS volume_migrations (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_builds_repo ON builds(repo_id);
CREATE INDEX idx_qa_repo_user ON qa_sessions(repo_id, user_id);
CREATE INDEX idx_qa_session_id ON qa_sessions(session_id);
CREATE INDEX idx_project_repos_proj ON project_repos(project_id);
CREATE INDEX idx_project_qa_proj ON project_qa_sessions(project_id);
CREATE INDEX idx_project_qa_sess ON project_qa_sessions(session_id);
CREATE INDEX idx_batch_tasks_batch ON batch_tasks(batch_id);
CREATE INDEX idx_batch_tasks_status ON batch_tasks(batch_id, status);

INSERT IGNORE INTO system_settings (`key`, value) VALUES
    ('max_tpm', '0'),
    ('max_rpm', '0'),
    ('day_max_concurrency', '2'),
    ('night_max_concurrency', '4'),
    ('llm_timeout_sec', '600'),
    ('llm_proxy_max_retries', '6');

-- ============================================================================
-- Unit 2: Portal Tables
-- ============================================================================

CREATE TABLE IF NOT EXISTS sessions (
    token        VARCHAR(255) PRIMARY KEY,
    user_id      VARCHAR(255) NOT NULL,
    display_name TEXT,
    role         VARCHAR(255) DEFAULT 'user',
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at   DATETIME NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS users (
    username     VARCHAR(255) PRIMARY KEY,
    display_name TEXT,
    role         VARCHAR(255) NOT NULL DEFAULT 'user',
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS user_domain_grants (
    username        VARCHAR(255) NOT NULL,
    business_domain VARCHAR(255) NOT NULL,
    granted_by      VARCHAR(255) NOT NULL,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (username, business_domain)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS audit_logs (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    operator    VARCHAR(255) NOT NULL,
    action      VARCHAR(255) NOT NULL,
    target_type VARCHAR(255) NOT NULL,
    target_id   VARCHAR(255) NOT NULL,
    details     TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS business_domains (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(255) NOT NULL UNIQUE,
    code        VARCHAR(255) DEFAULT '',
    description TEXT,
    sort_order  INT DEFAULT 0,
    status      VARCHAR(255) NOT NULL DEFAULT 'active',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_grants_user ON user_domain_grants(username);
CREATE INDEX idx_audit_created ON audit_logs(created_at);
CREATE INDEX idx_business_domains_name ON business_domains(name);

