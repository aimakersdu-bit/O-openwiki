# 全行代码库基线批量导入、算力防打穿增量调度与微服务业务聚合实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建支持全行 500+ 代码库筛选与批量导入的基线管理系统，实现带有 Token 漏桶限流、时段控制（夜间窗口）与 Git 无变更跳过的平稳增量批跑引擎（BatchScheduler），并自动聚合微服务到业务项目域，实现全行级安全可控的代码知识构建。

**Architecture:** 
1. **数据模型层**：在 SQLite 扩展 `repos` 元数据（`tier` 等级、`business_domain` 业务域、`last_release_at` 发版时间），新增 `batch_runs` 和 `batch_tasks` 持久化表支持断点续跑与状态追踪。
2. **算力保护调度层**：在 Orchestrator 引入基于令牌桶（Token Bucket）的速率限制器与时间窗口策略，结合 `git rev-parse HEAD` 哈希快照比对，实现无变更仓库 0 Token 瞬时跳过、并发度动态管控（默认 2 并发）与夜间批处理模式。
3. **微服务聚合与接口层**：提供 `/api/repos/batch-import` 批量导入与去重，自动创建业务项目域（Project）并关联；提供 `/api/batch/*` 控制批跑启动、暂停、恢复与进度查询。
4. **前端看板层**：在 Portal 管理后台新增“全行基线与批量调度”面板，支持批量导入配置、分级筛选、运行中批次监控与实时日志流。

**Tech Stack:** Go (Unit 1 Orchestrator), SQLite3 (WAL 模式), Node.js (Worker/Linker), Vanilla ES6 JS, HTML5/CSS3.

---

### File Structure Map

| 文件路径 | 模块职责 | 变更类型 |
| :--- | :--- | :--- |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | 数据表迁移与 DAO：扩展 `repos` 字段、新增批跑任务 CRUD | Modify |
| `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` | 批跑任务持久化与基线字段单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go` | 算力保护：令牌桶限流器与时间窗口判定逻辑 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` | 令牌桶限流与时间窗口单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go` | 批跑调度引擎：队列排队、断点续跑、并发控制与无变更跳过 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` | 批跑引擎生命周期（启动/暂停/恢复/完成）单元测试 | Create |
| `deploy/unit1-orchestrator/internal/api/batch.go` | 批量导入与批跑控制 REST API 端点处理函数 | Create |
| `deploy/unit1-orchestrator/internal/api/batch_test.go` | 批量导入与批跑 API HTTP 单元测试 | Create |
| `deploy/unit1-orchestrator/internal/api/router.go` | 注册 `/api/repos/batch-import` 与 `/api/batch/*` 路由 | Modify |
| `deploy/unit2-portal/internal/api/router.go` | Portal 反向代理增加批跑控制 API 透传 | Modify |
| `deploy/unit2-portal/public/admin.html` | 后台管理界面增加“全行基线导入与算力调度”面板 | Modify |
| `deploy/unit2-portal/public/js/admin.js` | 批量导入弹窗、批跑任务状态轮询与进度条渲染 | Modify |
| `deploy/unit2-portal/public/js/api.js` | 封装批量导入与批跑 API 调用客户端方法 | Modify |
| `deploy/scripts/build-arm.sh` | 同步编译产物与静态资源脚本 | Modify |
| `deploy/scripts/build-x86.sh` | 同步编译产物与静态资源脚本 | Modify |

---

### Task 1: 数据库与模型扩展（基线元数据、分级 Tier、业务域与批跑任务持久化）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go:170-290`
- Create: `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go`

- [ ] **Step 1: 编写数据库迁移与批跑 CRUD 单元测试**

在 `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` 编写测试用例：
```go
package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

func TestBatchRunAndRepoTierCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_batch.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

	// 1. 测试创建带有 Tier、业务域和发版时间的 Repo
	repo := &db.Repo{
		ID:             "trade-gateway",
		Name:           "交易边缘网关",
		GitURL:         "git@github.com:bank/trade-gateway.git",
		Branch:         "master",
		LocalPath:      "/data/repos/trade-gateway",
		Tier:           "A",
		BusinessDomain: "消费金融",
		LastReleaseAt:  "2025-11-20 10:00:00",
		Status:         "active",
	}

	if err := database.CreateRepo(repo); err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	fetched, err := database.GetRepo("trade-gateway")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	if fetched.Tier != "A" || fetched.BusinessDomain != "消费金融" {
		t.Errorf("Unexpected repo metadata: tier=%s, domain=%s", fetched.Tier, fetched.BusinessDomain)
	}

	// 2. 测试创建批处理任务 batch_runs
	run := &db.BatchRun{
		ID:             "batch-20261029-001",
		Name:           "全行近3年发版仓库首次知识构建",
		TotalCount:     1,
		Status:         "pending",
		MaxConcurrency: 2,
		NightModeOnly:  true,
	}
	if err := database.CreateBatchRun(run); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	// 3. 测试创建批处理明细任务 batch_tasks
	task := &db.BatchTask{
		BatchID: run.ID,
		RepoID:  repo.ID,
		Status:  "pending",
	}
	if err := database.CreateBatchTask(task); err != nil {
		t.Fatalf("CreateBatchTask failed: %v", err)
	}

	// 4. 更新批次状态与进度
	if err := database.UpdateBatchTaskStatus(run.ID, repo.ID, "completed", "Git HEAD一致, 跳过构建", 0); err != nil {
		t.Fatalf("UpdateBatchTaskStatus failed: %v", err)
	}

	tasks, err := database.ListBatchTasks(run.ID)
	if err != nil {
		t.Fatalf("ListBatchTasks failed: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != "completed" {
		t.Errorf("Unexpected tasks state: %+v", tasks)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/db -run TestBatchRunAndRepoTierCRUD
```
预期：编译错误（未定义 `Tier`、`BatchRun`、`BatchTask` 等结构体和方法）。

- [ ] **Step 3: 在 `sqlite.go` 中实现表迁移与 DAO**

编辑 `deploy/unit1-orchestrator/internal/db/sqlite.go`：
1. 扩展 `Repo` 结构体：
```go
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
	BusinessDomain string `json:"business_domain"` // 所属业务域（如消金、信贷、支付）
	LastReleaseAt  string `json:"last_release_at"` // 最近发版时间
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}
```

2. 在 `migrate()` 中新增对 `repos` 字段的平滑 ALTER 与新表创建：
```go
// 迁移增加列 (容错忽略已存在)
db.conn.Exec(`ALTER TABLE repos ADD COLUMN tier TEXT DEFAULT 'B'`)
db.conn.Exec(`ALTER TABLE repos ADD COLUMN business_domain TEXT DEFAULT ''`)
db.conn.Exec(`ALTER TABLE repos ADD COLUMN last_release_at DATETIME`)

// 创建 batch_runs 表
db.conn.Exec(`CREATE TABLE IF NOT EXISTS batch_runs (
	id              TEXT PRIMARY KEY,
	name            TEXT NOT NULL,
	total_count     INTEGER DEFAULT 0,
	processed_count INTEGER DEFAULT 0,
	success_count   INTEGER DEFAULT 0,
	skipped_count   INTEGER DEFAULT 0,
	failed_count    INTEGER DEFAULT 0,
	status          TEXT NOT NULL DEFAULT 'pending', -- pending, running, paused, completed, stopped
	max_concurrency INTEGER DEFAULT 2,
	night_mode_only BOOLEAN DEFAULT 0,
	started_at      DATETIME,
	finished_at     DATETIME,
	created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
)`)

// 创建 batch_tasks 表
db.conn.Exec(`CREATE TABLE IF NOT EXISTS batch_tasks (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	batch_id     TEXT NOT NULL REFERENCES batch_runs(id) ON DELETE CASCADE,
	repo_id      TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	status       TEXT NOT NULL DEFAULT 'pending', -- pending, running, completed, skipped, failed
	duration_sec INTEGER DEFAULT 0,
	summary      TEXT,
	error        TEXT,
	started_at   DATETIME,
	finished_at  DATETIME,
	UNIQUE(batch_id, repo_id)
)`)
```

3. 实现 `BatchRun`、`BatchTask` 结构体及 DAO 方法：
```go
type BatchRun struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	TotalCount     int       `json:"total_count"`
	ProcessedCount int       `json:"processed_count"`
	SuccessCount   int       `json:"success_count"`
	SkippedCount   int       `json:"skipped_count"`
	FailedCount    int       `json:"failed_count"`
	Status         string    `json:"status"`
	MaxConcurrency int       `json:"max_concurrency"`
	NightModeOnly  bool      `json:"night_mode_only"`
	StartedAt      string    `json:"started_at,omitempty"`
	FinishedAt     string    `json:"finished_at,omitempty"`
	CreatedAt      string    `json:"created_at"`
}

type BatchTask struct {
	ID          int64  `json:"id"`
	BatchID     string `json:"batch_id"`
	RepoID      string `json:"repo_id"`
	Status      string `json:"status"`
	DurationSec int    `json:"duration_sec"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

func (db *DB) CreateBatchRun(r *BatchRun) error {
	if r.MaxConcurrency <= 0 {
		r.MaxConcurrency = 2
	}
	_, err := db.conn.Exec(`INSERT INTO batch_runs (id, name, total_count, status, max_concurrency, night_mode_only)
		VALUES (?, ?, ?, ?, ?, ?)`, r.ID, r.Name, r.TotalCount, r.Status, r.MaxConcurrency, r.NightModeOnly)
	return err
}

func (db *DB) GetBatchRun(id string) (*BatchRun, error) {
	row := db.conn.QueryRow(`SELECT id, name, total_count, processed_count, success_count, skipped_count, failed_count, 
		status, max_concurrency, night_mode_only, COALESCE(started_at,''), COALESCE(finished_at,''), created_at 
		FROM batch_runs WHERE id = ?`, id)
	var r BatchRun
	err := row.Scan(&r.ID, &r.Name, &r.TotalCount, &r.ProcessedCount, &r.SuccessCount, &r.SkippedCount, &r.FailedCount,
		&r.Status, &r.MaxConcurrency, &r.NightModeOnly, &r.StartedAt, &r.FinishedAt, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) CreateBatchTask(t *BatchTask) error {
	_, err := db.conn.Exec(`INSERT INTO batch_tasks (batch_id, repo_id, status) VALUES (?, ?, ?)`,
		t.BatchID, t.RepoID, t.Status)
	return err
}

func (db *DB) UpdateBatchTaskStatus(batchID, repoID, status, summary string, durationSec int, taskErr ...string) error {
	errMsg := ""
	if len(taskErr) > 0 {
		errMsg = taskErr[0]
	}
	_, err := db.conn.Exec(`UPDATE batch_tasks SET status = ?, summary = ?, error = ?, duration_sec = ?, finished_at = CURRENT_TIMESTAMP
		WHERE batch_id = ? AND repo_id = ?`, status, summary, errMsg, durationSec, batchID, repoID)
	if err != nil {
		return err
	}

	// 重新聚合更新 batch_runs 的计数与状态
	_, err = db.conn.Exec(`UPDATE batch_runs SET 
		processed_count = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status IN ('completed','skipped','failed')),
		success_count   = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'completed'),
		skipped_count   = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'skipped'),
		failed_count    = (SELECT COUNT(*) FROM batch_tasks WHERE batch_id = ? AND status = 'failed')
		WHERE id = ?`, batchID, batchID, batchID, batchID, batchID)
	return err
}

func (db *DB) ListBatchTasks(batchID string) ([]BatchTask, error) {
	rows, err := db.conn.Query(`SELECT id, batch_id, repo_id, status, duration_sec, COALESCE(summary,''), COALESCE(error,''), COALESCE(started_at,''), COALESCE(finished_at,'')
		FROM batch_tasks WHERE batch_id = ? ORDER BY id ASC`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []BatchTask
	for rows.Next() {
		var t BatchTask
		if err := rows.Scan(&t.ID, &t.BatchID, &t.RepoID, &t.Status, &t.DurationSec, &t.Summary, &t.Error, &t.StartedAt, &t.FinishedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/db -run TestBatchRunAndRepoTierCRUD
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/db/sqlite.go deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go
git commit -m "feat(db): add repo baseline tier metadata and batch run persistence schema"
```

---

### Task 2: 算力保护限流器与 Git 增量变更检测器（RateLimiter & GitDiffGuard）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go`

- [ ] **Step 1: 编写令牌桶限流与时间窗口测试**

在 `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` 编写测试：
```go
package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestTokenBucketLimiter(t *testing.T) {
	// 创建一个最大突发 2 个并发令牌、每秒释放 2 个令牌的限流器
	limiter := scheduler.NewTokenBucketLimiter(2, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 连续获取 2 个令牌应当立刻成功
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatalf("First acquire failed: %v", err)
	}
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatalf("Second acquire failed: %v", err)
	}
}

func TestNightWindowCheck(t *testing.T) {
	// 验证时间窗口判定函数：夜间 22:00 ~ 07:00 为夜间低峰期
	t1 := time.Date(2026, 10, 29, 23, 30, 0, 0, time.Local)
	if !scheduler.IsNightWindow(t1, 22, 7) {
		t.Errorf("Expected 23:30 to be within night window [22, 7]")
	}

	t2 := time.Date(2026, 10, 29, 14, 0, 0, 0, time.Local)
	if scheduler.IsNightWindow(t2, 22, 7) {
		t.Errorf("Expected 14:00 NOT to be within night window [22, 7]")
	}

	t3 := time.Date(2026, 10, 29, 6, 15, 0, 0, time.Local)
	if !scheduler.IsNightWindow(t3, 22, 7) {
		t.Errorf("Expected 06:15 to be within night window [22, 7]")
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestTokenBucketLimiter
```
预期：FAIL（未定义相关方法与类型）。

- [ ] **Step 3: 实现 `rate_limiter.go`**

创建 `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`：
```go
package scheduler

import (
	"context"
	"sync"
	"time"
)

// TokenBucketLimiter 实现平滑并发与请求控制，保护私有大模型不被打穿
type TokenBucketLimiter struct {
	capacity     int
	tokens       chan struct{}
	fillInterval time.Duration
	stopCh       chan struct{}
	mu           sync.Mutex
}

// NewTokenBucketLimiter 初始化令牌桶
func NewTokenBucketLimiter(capacity int, tokensPerSecond int) *TokenBucketLimiter {
	if capacity <= 0 {
		capacity = 2
	}
	if tokensPerSecond <= 0 {
		tokensPerSecond = 1
	}

	limiter := &TokenBucketLimiter{
		capacity:     capacity,
		tokens:       make(chan struct{}, capacity),
		fillInterval: time.Second / time.Duration(tokensPerSecond),
		stopCh:       make(chan struct{}),
	}

	// 预先注满令牌
	for i := 0; i < capacity; i++ {
		limiter.tokens <- struct{}{}
	}

	// 后台按速率定时补充令牌
	go limiter.refillLoop()

	return limiter
}

func (l *TokenBucketLimiter) refillLoop() {
	ticker := time.NewTicker(l.fillInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			select {
			case l.tokens <- struct{}{}:
			default:
				// 桶满则丢弃多余补充
			}
		}
	}
}

// Acquire 尝试获取一个执行令牌，若当前无可用令牌则阻塞等待直到获取成功或 context 超时
func (l *TokenBucketLimiter) Acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.tokens:
		return nil
	}
}

// Release 归还令牌
func (l *TokenBucketLimiter) Release() {
	select {
	case l.tokens <- struct{}{}:
	default:
	}
}

// Close 关闭补充循环
func (l *TokenBucketLimiter) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}

// IsNightWindow 检查指定时间是否处于跨午夜的夜间时间窗口内 (如 22:00 至 次日 07:00)
func IsNightWindow(t time.Time, startHour, endHour int) bool {
	hour := t.Hour()
	if startHour > endHour {
		// 跨午夜: 22点以后 或 7点以前
		return hour >= startHour || hour < endHour
	}
	return hour >= startHour && hour < endHour
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run "TestTokenBucketLimiter|TestNightWindowCheck"
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go
git commit -m "feat(scheduler): implement token bucket rate limiter and night window policy"
```

---

### Task 3: 平稳增量批量调度引擎（BatchScheduler）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go`

- [ ] **Step 1: 编写批跑调度引擎状态机单元测试**

在 `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` 编写测试：
```go
package scheduler_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestBatchSchedulerLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "batch_engine.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

	// 注册 2 个模拟 Repo
	r1 := &db.Repo{ID: "repo-1", Name: "服务1", GitURL: "local", LocalPath: tmpDir, Status: "active"}
	r2 := &db.Repo{ID: "repo-2", Name: "服务2", GitURL: "local", LocalPath: tmpDir, Status: "active"}
	_ = database.CreateRepo(r1)
	_ = database.CreateRepo(r2)

	builder := scheduler.NewBuilder("echo", "", "", tmpDir)
	engine := scheduler.NewBatchScheduler(database, builder, 2)

	// 启动批跑任务
	runID := "batch-test-01"
	err = engine.StartBatch(context.Background(), runID, "测试批处理", []string{"repo-1", "repo-2"}, false)
	if err != nil {
		t.Fatalf("StartBatch failed: %v", err)
	}

	// 验证任务状态可查询
	status, err := engine.GetStatus(runID)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status.TotalCount != 2 {
		t.Errorf("Expected 2 total count, got %d", status.TotalCount)
	}

	// 暂停批跑
	err = engine.PauseBatch(runID)
	if err != nil {
		t.Fatalf("PauseBatch failed: %v", err)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestBatchSchedulerLifecycle
```
预期：FAIL。

- [ ] **Step 3: 实现 `batch_scheduler.go` 核心引擎**

创建 `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`：
```go
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

// BatchScheduler 负责对全量/存量代码库进行限流、平稳排队与断点续跑构建
type BatchScheduler struct {
	db             *db.DB
	builder        *Builder
	maxConcurrency int
	mu             sync.RWMutex
	activeCancels  map[string]context.CancelFunc
	limiter        *TokenBucketLimiter
}

// NewBatchScheduler 创建批跑调度引擎
func NewBatchScheduler(database *db.DB, builder *Builder, defaultConcurrency int) *BatchScheduler {
	if defaultConcurrency <= 0 {
		defaultConcurrency = 2
	}
	return &BatchScheduler{
		db:             database,
		builder:        builder,
		maxConcurrency: defaultConcurrency,
		activeCancels:  make(map[string]context.CancelFunc),
		limiter:        NewTokenBucketLimiter(defaultConcurrency, 1),
	}
}

// StartBatch 创建并启动一个批处理作业
func (s *BatchScheduler) StartBatch(ctx context.Context, batchID, name string, repoIDs []string, nightModeOnly bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.activeCancels[batchID]; exists {
		return fmt.Errorf("batch %s is already running", batchID)
	}

	// 1. 持久化 batch_runs 与明细 tasks
	run := &db.BatchRun{
		ID:             batchID,
		Name:           name,
		TotalCount:     len(repoIDs),
		Status:         "running",
		MaxConcurrency: s.maxConcurrency,
		NightModeOnly:  nightModeOnly,
	}
	if err := s.db.CreateBatchRun(run); err != nil {
		return fmt.Errorf("create batch run: %w", err)
	}

	for _, repoID := range repoIDs {
		_ = s.db.CreateBatchTask(&db.BatchTask{
			BatchID: batchID,
			RepoID:  repoID,
			Status:  "pending",
		})
	}

	// 2. 创建取消上下文并异步派发执行
	batchCtx, cancel := context.WithCancel(ctx)
	s.activeCancels[batchID] = cancel

	go s.runBatchWorker(batchCtx, batchID, repoIDs, nightModeOnly)

	return nil
}

func (s *BatchScheduler) runBatchWorker(ctx context.Context, batchID string, repoIDs []string, nightModeOnly bool) {
	defer func() {
		s.mu.Lock()
		delete(s.activeCancels, batchID)
		s.mu.Unlock()
	}()

	sem := make(chan struct{}, s.maxConcurrency)
	var wg sync.WaitGroup

	for _, repoID := range repoIDs {
		select {
		case <-ctx.Done():
			log.Printf("[BatchScheduler][%s] Stopped by context", batchID)
			return
		default:
		}

		// 若启用夜间模式，检测当前时间是否处于 22:00 ~ 07:00，否则睡眠轮询等待
		for nightModeOnly && !IsNightWindow(time.Now(), 22, 7) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Minute):
				// 继续等待夜间窗口
			}
		}

		// 获取令牌限流保护
		if err := s.limiter.Acquire(ctx); err != nil {
			return
		}

		sem <- struct{}{}
		wg.Add(1)

		go func(rID string) {
			defer func() {
				<-sem
				s.limiter.Release()
				wg.Done()
			}()

			s.executeSingleRepo(ctx, batchID, rID)
		}(repoID)
	}

	wg.Wait()
	log.Printf("[BatchScheduler][%s] All tasks finished", batchID)
}

func (s *BatchScheduler) executeSingleRepo(ctx context.Context, batchID, repoID string) {
	startTime := time.Now()
	repo, err := s.db.GetRepo(repoID)
	if err != nil {
		s.db.UpdateBatchTaskStatus(batchID, repoID, "failed", "仓库不存在", 0, err.Error())
		return
	}

	// 核心算力防打穿机制：Git Commit 比对。若无变更直接 0 消耗跳过
	latestBuild, _ := s.db.GetLatestBuild(repoID)
	currentHead := s.getGitHead(repo.LocalPath)

	if latestBuild != nil && latestBuild.Status == "success" && latestBuild.GitHead != "" && latestBuild.GitHead == currentHead {
		duration := int(time.Since(startTime).Seconds())
		s.db.UpdateBatchTaskStatus(batchID, repoID, "skipped", "Git Commit无变更, 零Token跳过", duration)
		return
	}

	// 触发实际构建
	result := s.builder.BuildRepo(repo.ID, repo.LocalPath, repo.Branch)
	duration := int(time.Since(startTime).Seconds())

	if result.Success {
		s.db.UpdateBatchTaskStatus(batchID, repoID, "completed", "构建成功", duration)
	} else {
		s.db.UpdateBatchTaskStatus(batchID, repoID, "failed", "构建失败", duration, result.Error)
	}
}

func (s *BatchScheduler) getGitHead(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PauseBatch 暂停批处理
func (s *BatchScheduler) PauseBatch(batchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cancel, ok := s.activeCancels[batchID]
	if !ok {
		return fmt.Errorf("batch %s is not active", batchID)
	}
	cancel()
	delete(s.activeCancels, batchID)

	return nil
}

// GetStatus 查询批处理当前进度
func (s *BatchScheduler) GetStatus(batchID string) (*db.BatchRun, error) {
	return s.db.GetBatchRun(batchID)
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestBatchSchedulerLifecycle
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go
git commit -m "feat(scheduler): implement BatchScheduler engine with git snapshot diff guard"
```

---

### Task 4: 批量基线导入与微服务业务域自动关联 API

**Files:**
- Create: `deploy/unit1-orchestrator/internal/api/batch.go`
- Create: `deploy/unit1-orchestrator/internal/api/batch_test.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [ ] **Step 1: 编写批量导入与批跑控制 API 测试**

在 `deploy/unit1-orchestrator/internal/api/batch_test.go` 中编写测试：
```go
package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openwiki/orchestrator/internal/api"
	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestBatchImportAndStartAPI(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "api_batch.db")
	database, _ := db.Open(dbPath)
	defer database.Close()

	cfg := &config.Config{
		ReposBaseDir: tmpDir,
	}
	builder := scheduler.NewBuilder("echo", "", "", tmpDir)
	sched := scheduler.NewScheduler(builder)
	server := api.NewServer(cfg, sched, nil)

	// 测试批量导入发版白名单仓库
	importPayload := map[string]interface{}{
		"repos": []map[string]interface{}{
			{
				"id":              "loan-service",
				"name":            "信贷服务",
				"git_url":         "git@github.com:bank/loan-service.git",
				"branch":          "master",
				"tier":            "A",
				"business_domain": "消费金融",
				"last_release_at": "2025-06-15 12:00:00",
			},
		},
		"auto_create_project": true,
	}
	body, _ := json.Marshal(importPayload)
	req := httptest.NewRequest("POST", "/api/repos/batch-import", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/api -run TestBatchImportAndStartAPI
```
预期：FAIL（404 Not Found 或未路由）。

- [ ] **Step 3: 实现 `batch.go` 并在 `router.go` 注册路由**

在 `deploy/unit1-orchestrator/internal/api/batch.go` 实现处理逻辑：
```go
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

type BatchImportRepoItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	GitURL         string `json:"git_url"`
	Branch         string `json:"branch"`
	LocalPath      string `json:"local_path"`
	Tier           string `json:"tier"`
	BusinessDomain string `json:"business_domain"`
	LastReleaseAt  string `json:"last_release_at"`
}

type BatchImportRequest struct {
	Repos             []BatchImportRepoItem `json:"repos"`
	AutoCreateProject bool                  `json:"auto_create_project"`
}

func (s *Server) handleBatchImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req BatchImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	importedCount := 0
	domainRepoMap := make(map[string][]string)

	for _, item := range req.Repos {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" || item.GitURL == "" {
			continue
		}
		if item.Branch == "" {
			item.Branch = "master"
		}
		if item.Tier == "" {
			item.Tier = "B"
		}
		if item.LocalPath == "" {
			item.LocalPath = filepath.Join(s.config.ReposBaseDir, item.ID)
		}

		repo := &db.Repo{
			ID:             item.ID,
			Name:           item.Name,
			GitURL:         item.GitURL,
			Branch:         item.Branch,
			LocalPath:      item.LocalPath,
			Tier:           item.Tier,
			BusinessDomain: item.BusinessDomain,
			LastReleaseAt:  item.LastReleaseAt,
			Schedule:       "0 2 * * *",
			Status:         "active",
		}
		_ = db.SaveRepo(repo)
		importedCount++

		if item.BusinessDomain != "" {
			domainRepoMap[item.BusinessDomain] = append(domainRepoMap[item.BusinessDomain], item.ID)
		}
	}

	// 自动将微服务关联到所属业务项目域 (Virtual Project Monorepo)
	if req.AutoCreateProject {
		for domain, repoIDs := range domainRepoMap {
			projID := "proj-" + strings.ToLower(domain)
			existing, _ := db.GetProject(projID)
			if existing == nil {
				_ = db.CreateProject(&db.Project{
					ID:          projID,
					Name:        domain + "业务系统域",
					Description: fmt.Sprintf("由发版白名单自动聚合的 %s 微服务集群", domain),
					LocalPath:   filepath.Join(s.config.ProjectsBaseDir, projID),
					Status:      "active",
				})
			}
			var pRepos []db.ProjectRepo
			for _, rID := range repoIDs {
				pRepos = append(pRepos, db.ProjectRepo{
					ProjectID: projID,
					RepoID:    rID,
					AliasName: rID,
					Role:      "service",
				})
			}
			_ = db.SetProjectRepos(projID, pRepos)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"imported_count": importedCount,
		"message":        fmt.Sprintf("已成功导入 %d 个发版基线仓库并完成业务域聚合", importedCount),
	})
}
```

并在 `deploy/unit1-orchestrator/internal/api/router.go` 注册 `/api/repos/batch-import` 路由。

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/api -run TestBatchImportAndStartAPI
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/api/batch.go deploy/unit1-orchestrator/internal/api/batch_test.go deploy/unit1-orchestrator/internal/api/router.go
git commit -m "feat(api): implement batch repository import and domain auto-aggregation API"
```

---

### Task 5: Unit 2 Portal 网关代理与 API 转发

**Files:**
- Modify: `deploy/unit2-portal/internal/api/router.go:50-90`

- [ ] **Step 1: 在 Unit 2 Portal 路由中透传批处理 API 请求**

编辑 `deploy/unit2-portal/internal/api/router.go`，在反向代理匹配逻辑中增加对 `/api/repos/batch-import` 与 `/api/batch/` 前缀的转发，使其直接透传至 Unit 1 Orchestrator。

- [ ] **Step 2: 编译测试 Unit 2 Portal**

运行：
```bash
go build -o /dev/null ./deploy/unit2-portal/internal/api/...
```
预期：编译成功无报错。

- [ ] **Step 3: 提交更改**

```bash
git add deploy/unit2-portal/internal/api/router.go
git commit -m "feat(portal): add proxy pass for batch import and scheduler endpoints"
```

---

### Task 6: 管理后台批量导入与平稳调度控制看板（Admin UI）

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html`
- Modify: `deploy/unit2-portal/public/js/admin.js`
- Modify: `deploy/unit2-portal/public/js/api.js`

- [ ] **Step 1: 在 `admin.html` 新增“全行基线与算力批跑”面板**

在标签页导航区新增：
```html
<button class="tab-btn" data-tab="tab-batch">⚡ 全行基线与批跑调度</button>
```
并在内容区新增批跑控制面板：
- 批量导入文本域/JSON 导入框（预置陈立芝 3年发版过滤规则模板）；
- 分级（Tier 1/2/3）与业务域筛选统计指示牌；
- 算力保护开关（夜间窗口 22:00-07:00 强制、并发限制滑块 1~4）；
- 批跑实时进度条（已完成、零Token跳过、失败数、总用时）；
- 操作按钮：“导入基线”、“启动批量构建”、“暂停构建”。

- [ ] **Step 2: 在 `api.js` 中封装批量导入与批跑请求方法**

```javascript
API.batchImportRepos = function(payload) {
  return API.fetchJSON('/api/repos/batch-import', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
};

API.getBatchStatus = function(batchId) {
  return API.fetchJSON('/api/batch/status?id=' + encodeURIComponent(batchId));
};
```

- [ ] **Step 3: 在 `admin.js` 中实现批跑状态定时轮询与交互**

实现导入数据解析、提交 API 以及启动批跑后每 3 秒刷新任务进度条与状态徽章。

- [ ] **Step 4: 提交更改**

```bash
git add deploy/unit2-portal/public/admin.html deploy/unit2-portal/public/js/admin.js deploy/unit2-portal/public/js/api.js
git commit -m "feat(ui): add batch baseline import and rate-limited scheduler admin dashboard"
```

---

### Task 7: 部署产物同步与端到端自动化测试验证

**Files:**
- Modify: `deploy/scripts/build-arm.sh`
- Modify: `deploy/scripts/build-x86.sh`

- [ ] **Step 1: 更新部署编译脚本**

确保 `build-arm.sh` 与 `build-x86.sh` 编译最新包含批跑引擎的 Orchestrator 与 Portal。

- [ ] **Step 2: 运行全量单元测试与构建验证**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/...
go test -v ./deploy/unit2-portal/...
```
预期：所有模块单元测试 100% PASS。

- [ ] **Step 3: 提交更改**

```bash
git add deploy/scripts/build-arm.sh deploy/scripts/build-x86.sh
git commit -m "chore(deploy): update build scripts with batch scheduler components"
```
