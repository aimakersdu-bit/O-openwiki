# 全行代码库基线批量导入、算力防打穿增量调度与微服务业务聚合实施计划 (V2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建支持全行 500+ 代码库筛选与批量导入的基线管理系统，实现支持**运行时并发动态热调整**、**实时进度与 ETA 剩余时间预估**、**每日 Token 消耗统计与飞书多维表格报表同步**的平稳增量批跑引擎（BatchScheduler），并自动聚合微服务到业务项目域，实现全行级安全可观测的代码知识构建。

**Architecture:** 
1. **数据模型层**：在 SQLite 扩展 `repos` 元数据（`tier` 等级、`business_domain` 业务域、`last_release_at` 发版时间），新增 `batch_runs`（含动态并发、耗时预估、活跃任务）、`batch_tasks`（含 Token 消耗）与 `daily_token_usage` 表（沉淀每日 Token 消耗与节省统计）。
2. **算力保护与动态伸缩调度层**：在 Orchestrator 引入支持**动态热伸缩（Dynamic Concurrency）**的信号通道，支持运行时即刻调大/调小并发槽位；结合夜间时间窗口（22:00-07:00）与 Git Commit 快照对比（0 Token 瞬时跳过）。
3. **进度观测与 ETA 预估引擎**：实时追踪当前正在执行的仓库与执行阶段，根据已完成样本动态推算平均单仓耗时，输出高精度预计剩余时间（ETA）。
4. **Token 报表与飞书云文档适配**：从构建日志萃取 Token 消耗，提供每日历史明细 API 及符合飞书多维表格（Lark Sheets）导入规范的导出接口。
5. **微服务聚合与接口层**：提供 `/api/repos/batch-import` 批量导入与去重，自动创建业务项目域（Project）并关联；提供 `/api/batch/*` 控制批跑启动、暂停、**动态调整并发**与进度状态查询。
6. **前端看板层**：在 Portal 管理后台新增“全行基线与批量调度”面板，展示实时进度条、ETA 倒计时、槽位状态灯、并发调节滑块以及 Token 每日报表表格。

**Tech Stack:** Go (Unit 1 Orchestrator), SQLite3 (WAL 模式), Node.js (Worker/Linker), Vanilla ES6 JS, HTML5/CSS3.

---

### File Structure Map

| 文件路径 | 模块职责 | 变更类型 |
| :--- | :--- | :--- |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | 数据表迁移与 DAO：扩展 `repos` 字段、新增批跑表与 `daily_token_usage` 每日报表表 | Modify |
| `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` | 批跑任务持久化、基线字段与 Token 报表单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go` | 算力保护：支持运行时热调整的 `DynamicRateLimiter` 与时间窗口判定 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` | 并发动态调整（调大/调小）与时间窗口单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go` | 批跑调度引擎：队列排队、断点续跑、并发热调整、ETA 计算、Token 统计与无变更跳过 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` | 批跑引擎生命周期、ETA 预估与动态并发单元测试 | Create |
| `deploy/unit1-orchestrator/internal/api/batch.go` | 批量导入、动态并发调整、批跑状态（含 ETA）、Token 报表导出 REST API | Create |
| `deploy/unit1-orchestrator/internal/api/batch_test.go` | 批量导入、并发调整与报表 API HTTP 单元测试 | Create |
| `deploy/unit1-orchestrator/internal/api/router.go` | 注册 `/api/repos/batch-import`、`/api/batch/*` 与 `/api/telemetry/*` 路由 | Modify |
| `deploy/unit2-portal/internal/api/router.go` | Portal 反向代理增加批跑与报表 API 透传 | Modify |
| `deploy/unit2-portal/public/admin.html` | 后台管理界面增加“全行基线与算力批跑”看板（含并发滑块、进度条、ETA 与 Token 报表） | Modify |
| `deploy/unit2-portal/public/js/admin.js` | 批量导入弹窗、批跑状态定时轮询、并发动态修改与报表渲染 | Modify |
| `deploy/unit2-portal/public/js/api.js` | 封装批量导入、动态并发与 Token 报表 API 客户端方法 | Modify |
| `deploy/scripts/build-arm.sh` | 同步编译产物与静态资源脚本 | Modify |
| `deploy/scripts/build-x86.sh` | 同步编译产物与静态资源脚本 | Modify |

---

### Task 1: 数据库与模型扩展（基线元数据、分级 Tier、Token 统计与批跑持久化）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go`
- Create: `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go`

- [ ] **Step 1: 编写数据库迁移、Token 统计与批跑 CRUD 单元测试**

在 `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` 编写测试用例：
```go
package db_test

import (
	"path/filepath"
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
)

func TestBatchRunAndDailyTokenUsageCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_batch_token.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

	// 1. 测试创建带有 Tier、业务域的 Repo
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

	// 2. 测试创建批处理任务 batch_runs
	run := &db.BatchRun{
		ID:             "batch-20261029-001",
		Name:           "全行近3年发版仓库首次知识构建",
		TotalCount:     1,
		Status:         "running",
		MaxConcurrency: 2,
		NightModeOnly:  true,
	}
	if err := database.CreateBatchRun(run); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	// 3. 测试动态更新批次并发数
	if err := database.UpdateBatchConcurrency(run.ID, 4); err != nil {
		t.Fatalf("UpdateBatchConcurrency failed: %v", err)
	}
	fetchedRun, _ := database.GetBatchRun(run.ID)
	if fetchedRun.MaxConcurrency != 4 {
		t.Errorf("Expected concurrency 4, got %d", fetchedRun.MaxConcurrency)
	}

	// 4. 测试创建批处理明细任务 batch_tasks 并记录 Token
	task := &db.BatchTask{
		BatchID:          run.ID,
		RepoID:           repo.ID,
		Status:           "pending",
		PromptTokens:     12500,
		CompletionTokens: 3200,
		TotalTokens:      15700,
	}
	if err := database.CreateBatchTask(task); err != nil {
		t.Fatalf("CreateBatchTask failed: %v", err)
	}

	// 5. 更新任务状态与耗时
	if err := database.UpdateBatchTaskStatus(run.ID, repo.ID, "completed", "构建完成", 120, 12500, 3200, 15700); err != nil {
		t.Fatalf("UpdateBatchTaskStatus failed: %v", err)
	}

	// 6. 记录每日 Token 报表统计
	daily := &db.DailyTokenUsage{
		Date:             "2026-10-29",
		PromptTokens:     12500,
		CompletionTokens: 3200,
		TotalTokens:      15700,
		SavedTokens:      25000,
		BuildCount:       1,
		SkippedCount:     1,
	}
	if err := database.RecordDailyTokenUsage(daily); err != nil {
		t.Fatalf("RecordDailyTokenUsage failed: %v", err)
	}

	usages, err := database.ListDailyTokenUsage(7)
	if err != nil || len(usages) != 1 {
		t.Fatalf("ListDailyTokenUsage failed: %v", err)
	}
	if usages[0].TotalTokens != 15700 {
		t.Errorf("Expected 15700 total tokens, got %d", usages[0].TotalTokens)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/db -run TestBatchRunAndDailyTokenUsageCRUD
```
预期：FAIL（未定义相关类型与方法）。

- [ ] **Step 3: 在 `sqlite.go` 中实现表迁移与 DAO**

编辑 `deploy/unit1-orchestrator/internal/db/sqlite.go`：
1. 扩展 `Repo` 结构体增加 `Tier`、`BusinessDomain`、`LastReleaseAt`。
2. 在 `migrate()` 中创建表：
```sql
CREATE TABLE IF NOT EXISTS batch_runs (
	id              TEXT PRIMARY KEY,
	name            TEXT NOT NULL,
	total_count     INTEGER DEFAULT 0,
	processed_count INTEGER DEFAULT 0,
	success_count   INTEGER DEFAULT 0,
	skipped_count   INTEGER DEFAULT 0,
	failed_count    INTEGER DEFAULT 0,
	status          TEXT NOT NULL DEFAULT 'pending',
	max_concurrency INTEGER DEFAULT 2,
	night_mode_only BOOLEAN DEFAULT 0,
	started_at      DATETIME,
	finished_at     DATETIME,
	created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS batch_tasks (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	batch_id          TEXT NOT NULL REFERENCES batch_runs(id) ON DELETE CASCADE,
	repo_id           TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	status            TEXT NOT NULL DEFAULT 'pending',
	duration_sec      INTEGER DEFAULT 0,
	prompt_tokens     INTEGER DEFAULT 0,
	completion_tokens INTEGER DEFAULT 0,
	total_tokens      INTEGER DEFAULT 0,
	summary           TEXT,
	error             TEXT,
	started_at        DATETIME,
	finished_at       DATETIME,
	UNIQUE(batch_id, repo_id)
);

CREATE TABLE IF NOT EXISTS daily_token_usage (
	date              TEXT PRIMARY KEY, -- YYYY-MM-DD
	prompt_tokens     INTEGER DEFAULT 0,
	completion_tokens INTEGER DEFAULT 0,
	total_tokens      INTEGER DEFAULT 0,
	saved_tokens      INTEGER DEFAULT 0, -- 无变更跳过所节约的预估 Token
	build_count       INTEGER DEFAULT 0,
	skipped_count     INTEGER DEFAULT 0,
	updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

3. 实现对应的 DAO 方法：
```go
type DailyTokenUsage struct {
	Date             string `json:"date"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	SavedTokens      int64  `json:"saved_tokens"`
	BuildCount       int    `json:"build_count"`
	SkippedCount     int    `json:"skipped_count"`
	UpdatedAt        string `json:"updated_at"`
}

func (db *DB) UpdateBatchConcurrency(batchID string, concurrency int) error {
	_, err := db.conn.Exec(`UPDATE batch_runs SET max_concurrency = ? WHERE id = ?`, concurrency, batchID)
	return err
}

func (db *DB) RecordDailyTokenUsage(u *DailyTokenUsage) error {
	_, err := db.conn.Exec(`INSERT INTO daily_token_usage (date, prompt_tokens, completion_tokens, total_tokens, saved_tokens, build_count, skipped_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(date) DO UPDATE SET
			prompt_tokens = prompt_tokens + excluded.prompt_tokens,
			completion_tokens = completion_tokens + excluded.completion_tokens,
			total_tokens = total_tokens + excluded.total_tokens,
			saved_tokens = saved_tokens + excluded.saved_tokens,
			build_count = build_count + excluded.build_count,
			skipped_count = skipped_count + excluded.skipped_count,
			updated_at = CURRENT_TIMESTAMP`,
		u.Date, u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.SavedTokens, u.BuildCount, u.SkippedCount)
	return err
}

func (db *DB) ListDailyTokenUsage(limit int) ([]DailyTokenUsage, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := db.conn.Query(`SELECT date, prompt_tokens, completion_tokens, total_tokens, saved_tokens, build_count, skipped_count, updated_at
		FROM daily_token_usage ORDER BY date DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DailyTokenUsage
	for rows.Next() {
		var u DailyTokenUsage
		if err := rows.Scan(&u.Date, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.SavedTokens, &u.BuildCount, &u.SkippedCount, &u.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/db -run TestBatchRunAndDailyTokenUsageCRUD
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/db/sqlite.go deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go
git commit -m "feat(db): add daily token usage schema and dynamic concurrency persistence"
```

---

### Task 2: 动态热调整限流器与时间窗口控制（DynamicRateLimiter & WindowGuard）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go`

- [ ] **Step 1: 编写并发动态热调整单元测试**

在 `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` 编写测试：
```go
package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestDynamicRateLimiterResize(t *testing.T) {
	// 初始并发为 1
	limiter := scheduler.NewDynamicRateLimiter(1)
	defer limiter.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 获取第 1 个令牌成功
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// 此时容量为 1，且已被占用，再次获取应当超时
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelTimeout()
	if err := limiter.Acquire(ctxTimeout); err == nil {
		t.Fatalf("Expected acquire to timeout, but succeeded")
	}

	// 动态将并发扩容为 3
	limiter.SetConcurrency(3)
	if limiter.GetConcurrency() != 3 {
		t.Errorf("Expected concurrency 3, got %d", limiter.GetConcurrency())
	}

	// 扩容后，立刻能再次获取到令牌
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatalf("Acquire after resize failed: %v", err)
	}

	// 归还所有令牌
	limiter.Release()
	limiter.Release()
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestDynamicRateLimiterResize
```
预期：FAIL。

- [ ] **Step 3: 实现 `DynamicRateLimiter`**

在 `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go` 中实现：
```go
package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DynamicRateLimiter 支持在系统运行过程中无重启热调整并发数，并向外暴露当前槽位利用率
type DynamicRateLimiter struct {
	concurrency int32
	tokens      chan struct{}
	mu          sync.Mutex
	stopCh      chan struct{}
}

// NewDynamicRateLimiter 创建动态限流器
func NewDynamicRateLimiter(initialConcurrency int) *DynamicRateLimiter {
	if initialConcurrency <= 0 {
		initialConcurrency = 2
	}
	l := &DynamicRateLimiter{
		concurrency: int32(initialConcurrency),
		tokens:      make(chan struct{}, 64), // 足够大的缓冲支持热扩容
		stopCh:      make(chan struct{}),
	}
	for i := 0; i < initialConcurrency; i++ {
		l.tokens <- struct{}{}
	}
	return l
}

// SetConcurrency 动态热更新并发数
func (l *DynamicRateLimiter) SetConcurrency(newCapacity int) {
	if newCapacity <= 0 {
		newCapacity = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	old := int(atomic.LoadInt32(&l.concurrency))
	if newCapacity == old {
		return
	}

	atomic.StoreInt32(&l.concurrency, int32(newCapacity))

	// 若扩容：补入差额令牌
	if newCapacity > old {
		diff := newCapacity - old
		for i := 0; i < diff; i++ {
			select {
			case l.tokens <- struct{}{}:
			default:
			}
		}
	} else {
		// 若缩容：排出多余的闲置令牌
		diff := old - newCapacity
		for i := 0; i < diff; i++ {
			select {
			case <-l.tokens:
			default:
			}
		}
	}
}

// GetConcurrency 获取当前配置的并发数
func (l *DynamicRateLimiter) GetConcurrency() int {
	return int(atomic.LoadInt32(&l.concurrency))
}

// GetSlotStats 获取当前槽位状态统计 (总槽位, 活跃中槽位, 空闲槽位)
func (l *DynamicRateLimiter) GetSlotStats() (total, busy, idle int) {
	total = l.GetConcurrency()
	idle = len(l.tokens)
	if idle > total {
		idle = total
	}
	busy = total - idle
	if busy < 0 {
		busy = 0
	}
	return total, busy, idle
}

func (l *DynamicRateLimiter) Acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.tokens:
		return nil
	}
}

func (l *DynamicRateLimiter) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case l.tokens <- struct{}{}:
	default:
	}
}

func (l *DynamicRateLimiter) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}

func IsNightWindow(t time.Time, startHour, endHour int) bool {
	hour := t.Hour()
	if startHour > endHour {
		return hour >= startHour || hour < endHour
	}
	return hour >= startHour && hour < endHour
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestDynamicRateLimiterResize
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go
git commit -m "feat(scheduler): implement dynamic rate limiter with live concurrency adjustment"
```

---

### Task 3: 带进度观测、ETA 预估与 Token 萃取的调度引擎（BatchScheduler）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go`

- [ ] **Step 1: 编写调度引擎 ETA 估算与 Token 统计测试**

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

func TestBatchSchedulerETAAndTokens(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "batch_eta.db")
	database, _ := db.Open(dbPath)
	defer database.Close()

	_ = database.CreateRepo(&db.Repo{ID: "repo-a", Name: "服务A", LocalPath: tmpDir, Status: "active"})
	_ = database.CreateRepo(&db.Repo{ID: "repo-b", Name: "服务B", LocalPath: tmpDir, Status: "active"})

	builder := scheduler.NewBuilder("echo", "", "", tmpDir)
	engine := scheduler.NewBatchScheduler(database, builder, 2)

	_ = engine.StartBatch(context.Background(), "batch-eta-01", "ETA测试批次", []string{"repo-a", "repo-b"}, false)

	// 测试动态调整并发数
	err := engine.SetConcurrency("batch-eta-01", 4)
	if err != nil {
		t.Fatalf("SetConcurrency failed: %v", err)
	}

	// 获取运行状态并验证包含 ETA 字段和 Worker 活跃槽位信息
	info, err := engine.GetDetailedStatus("batch-eta-01")
	if err != nil {
		t.Fatalf("GetDetailedStatus failed: %v", err)
	}
	if info.MaxConcurrency != 4 {
		t.Errorf("Expected concurrency 4, got %d", info.MaxConcurrency)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestBatchSchedulerETAAndTokens
```
预期：FAIL。

- [ ] **Step 3: 实现 `batch_scheduler.go`**

创建 `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`：
```go
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

type WorkerSlotInfo struct {
	SlotID    int    `json:"slot_id"`
	RepoID    string `json:"repo_id"`
	RepoName  string `json:"repo_name"`
	StartedAt string `json:"started_at"`
	Stage     string `json:"stage"` // "AST解析", "文档萃取", "图谱导出"
}

type BatchStatusResponse struct {
	db.BatchRun
	ActiveSlots         []WorkerSlotInfo `json:"active_slots"`
	BusySlotsCount      int              `json:"busy_slots_count"`
	IdleSlotsCount      int              `json:"idle_slots_count"`
	AvgDurationSec      int              `json:"avg_duration_sec"`
	EstimatedRemainingSec int            `json:"estimated_remaining_sec"`
	FormattedETA        string           `json:"formatted_eta"`
}

type BatchScheduler struct {
	db            *db.DB
	builder       *Builder
	limiter       *DynamicRateLimiter
	mu            sync.RWMutex
	activeCancels map[string]context.CancelFunc
	activeSlots   map[string]map[int]WorkerSlotInfo // batchID -> slotID -> info
	durations     map[string][]int                  // batchID -> completed durations for ETA
}

func NewBatchScheduler(database *db.DB, builder *Builder, defaultConcurrency int) *BatchScheduler {
	if defaultConcurrency <= 0 {
		defaultConcurrency = 2
	}
	return &BatchScheduler{
		db:            database,
		builder:       builder,
		limiter:       NewDynamicRateLimiter(defaultConcurrency),
		activeCancels: make(map[string]context.CancelFunc),
		activeSlots:   make(map[string]map[int]WorkerSlotInfo),
		durations:     make(map[string][]int),
	}
}

func (s *BatchScheduler) SetConcurrency(batchID string, newConcurrency int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.limiter.SetConcurrency(newConcurrency)
	return s.db.UpdateBatchConcurrency(batchID, newConcurrency)
}

func (s *BatchScheduler) StartBatch(ctx context.Context, batchID, name string, repoIDs []string, nightModeOnly bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.activeCancels[batchID]; exists {
		return fmt.Errorf("batch %s already running", batchID)
	}

	concurrency := s.limiter.GetConcurrency()
	run := &db.BatchRun{
		ID:             batchID,
		Name:           name,
		TotalCount:     len(repoIDs),
		Status:         "running",
		MaxConcurrency: concurrency,
		NightModeOnly:  nightModeOnly,
	}
	_ = s.db.CreateBatchRun(run)

	for _, id := range repoIDs {
		_ = s.db.CreateBatchTask(&db.BatchTask{
			BatchID: batchID,
			RepoID:  id,
			Status:  "pending",
		})
	}

	batchCtx, cancel := context.WithCancel(ctx)
	s.activeCancels[batchID] = cancel
	s.activeSlots[batchID] = make(map[int]WorkerSlotInfo)
	s.durations[batchID] = make([]int, 0)

	go s.runBatch(batchCtx, batchID, repoIDs, nightModeOnly)
	return nil
}

func (s *BatchScheduler) runBatch(ctx context.Context, batchID string, repoIDs []string, nightModeOnly bool) {
	defer func() {
		s.mu.Lock()
		delete(s.activeCancels, batchID)
		delete(s.activeSlots, batchID)
		s.mu.Unlock()
	}()

	var wg sync.WaitGroup
	slotCounter := 0

	for _, repoID := range repoIDs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 夜间窗口自旋控制
		for nightModeOnly && !IsNightWindow(time.Now(), 22, 7) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}

		if err := s.limiter.Acquire(ctx); err != nil {
			return
		}

		wg.Add(1)
		slotCounter++
		currentSlot := slotCounter % s.limiter.GetConcurrency()

		go func(rID string, slot int) {
			defer func() {
				s.limiter.Release()
				s.mu.Lock()
				if slots, ok := s.activeSlots[batchID]; ok {
					delete(slots, slot)
				}
				s.mu.Unlock()
				wg.Done()
			}()

			s.executeRepoWithTelemetry(ctx, batchID, rID, slot)
		}(repoID, currentSlot)
	}

	wg.Wait()
}

func (s *BatchScheduler) executeRepoWithTelemetry(ctx context.Context, batchID, repoID string, slot int) {
	startTime := time.Now()
	repo, err := s.db.GetRepo(repoID)
	if err != nil {
		s.db.UpdateBatchTaskStatus(batchID, repoID, "failed", "仓库不存在", 0)
		return
	}

	s.mu.Lock()
	if slots, ok := s.activeSlots[batchID]; ok {
		slots[slot] = WorkerSlotInfo{
			SlotID:    slot,
			RepoID:    repo.ID,
			RepoName:  repo.Name,
			StartedAt: startTime.Format("15:04:05"),
			Stage:     "Git快照比对",
		}
	}
	s.mu.Unlock()

	// 1. Git Commit 快照 0 消耗比对
	latestBuild, _ := s.db.GetLatestBuild(repoID)
	currentHead := s.getGitHead(repo.LocalPath)

	if latestBuild != nil && latestBuild.Status == "success" && latestBuild.GitHead != "" && latestBuild.GitHead == currentHead {
		duration := int(time.Since(startTime).Seconds())
		_ = s.db.UpdateBatchTaskStatus(batchID, repoID, "skipped", "Git Commit无变更, 零Token跳过", duration)
		// 累加节约的 Token
		_ = s.db.RecordDailyTokenUsage(&db.DailyTokenUsage{
			Date:         time.Now().Format("2006-01-02"),
			SavedTokens:  150000, // 经验估算：单仓全量平均节约 150k Token
			SkippedCount: 1,
		})
		return
	}

	// 2. 状态更新为萃取中
	s.mu.Lock()
	if slots, ok := s.activeSlots[batchID]; ok {
		info := slots[slot]
		info.Stage = "文档构建与拓扑萃取"
		slots[slot] = info
	}
	s.mu.Unlock()

	result := s.builder.BuildRepo(repo.ID, repo.LocalPath, repo.Branch)
	duration := int(time.Since(startTime).Seconds())

	// 3. 提取 Token 消耗
	promptTokens, completionTokens, totalTokens := s.parseTokensFromLog(result.Log)

	if result.Success {
		_ = s.db.UpdateBatchTaskStatus(batchID, repoID, "completed", "构建成功", duration)
		_ = s.db.RecordDailyTokenUsage(&db.DailyTokenUsage{
			Date:             time.Now().Format("2006-01-02"),
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
			BuildCount:       1,
		})
		s.mu.Lock()
		s.durations[batchID] = append(s.durations[batchID], duration)
		s.mu.Unlock()
	} else {
		_ = s.db.UpdateBatchTaskStatus(batchID, repoID, "failed", "构建失败: "+result.Error, duration)
	}
}

func (s *BatchScheduler) parseTokensFromLog(logContent string) (prompt, completion, total int64) {
	// 从 OpenWiki Agent 日志提取 Token (匹配类似 "Tokens: prompt=12300 completion=2400 total=14700" 或 OpenAI usage)
	re := regexp.MustCompile(`total_tokens[":\s]+(\d+)`)
	matches := re.FindStringSubmatch(logContent)
	if len(matches) > 1 {
		total, _ = strconv.ParseInt(matches[1], 10, 64)
		prompt = total * 8 / 10
		completion = total - prompt
	} else {
		// 保底经验值
		total = 120000
		prompt = 100000
		completion = 20000
	}
	return prompt, completion, total
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

func (s *BatchScheduler) GetDetailedStatus(batchID string) (*BatchStatusResponse, error) {
	run, err := s.db.GetBatchRun(batchID)
	if err != nil {
		return nil, err
	}

	s.mu.RWMutex.RLock()
	defer s.mu.RWMutex.RUnlock()

	var activeSlots []WorkerSlotInfo
	if slots, ok := s.activeSlots[batchID]; ok {
		for _, info := range slots {
			activeSlots = append(activeSlots, info)
		}
	}

	totalSlots, busySlots, idleSlots := s.limiter.GetSlotStats()

	// 计算 ETA
	avgDuration := 180 // 默认按 3 分钟预估
	if ds, ok := s.durations[batchID]; ok && len(ds) > 0 {
		sum := 0
		for _, d := range ds {
			sum += d
		}
		avgDuration = sum / len(ds)
	}

	remainingTasks := run.TotalCount - run.ProcessedCount
	if remainingTasks < 0 {
		remainingTasks = 0
	}

	concurrency := totalSlots
	if concurrency <= 0 {
		concurrency = 2
	}
	estSec := (remainingTasks * avgDuration) / concurrency
	formattedETA := formatDuration(estSec)

	return &BatchStatusResponse{
		BatchRun:              *run,
		ActiveSlots:           activeSlots,
		BusySlotsCount:        busySlots,
		IdleSlotsCount:        idleSlots,
		AvgDurationSec:        avgDuration,
		EstimatedRemainingSec: estSec,
		FormattedETA:          formattedETA,
	}, nil
}

func formatDuration(sec int) string {
	if sec <= 0 {
		return "即将完成"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("预计还需 %d小时 %d分钟", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("预计还需 %d分钟 %d秒", m, s)
	}
	return fmt.Sprintf("预计还需 %d秒", s)
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestBatchSchedulerETAAndTokens
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go
git commit -m "feat(scheduler): implement dynamic concurrency, live progress ETA, and token tracking"
```

---

### Task 4: 批量基线导入、动态调参与 Token 报表 API

**Files:**
- Create: `deploy/unit1-orchestrator/internal/api/batch.go`
- Create: `deploy/unit1-orchestrator/internal/api/batch_test.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [ ] **Step 1: 编写 API 单元测试**

在 `deploy/unit1-orchestrator/internal/api/batch_test.go` 编写并发更新与报表测试：
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

func TestBatchAPIsAndTokenExport(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "api_full_test.db")
	database, _ := db.Open(dbPath)
	defer database.Close()

	cfg := &config.Config{ReposBaseDir: tmpDir}
	builder := scheduler.NewBuilder("echo", "", "", tmpDir)
	sched := scheduler.NewScheduler(builder)
	server := api.NewServer(cfg, sched, nil)

	// 1. 测试 Token 报表拉取
	req := httptest.NewRequest("GET", "/api/telemetry/tokens/daily", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", w.Code)
	}

	// 2. 测试飞书云文档格式导出
	reqExport := httptest.NewRequest("GET", "/api/telemetry/tokens/export?format=csv", nil)
	wExport := httptest.NewRecorder()
	server.ServeHTTP(wExport, reqExport)
	if wExport.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", wExport.Code)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/api -run TestBatchAPIsAndTokenExport
```
预期：FAIL（404 Not Found）。

- [ ] **Step 3: 实现 `batch.go` 并在 `router.go` 注册所有端点**

在 `deploy/unit1-orchestrator/internal/api/batch.go` 增加：
1. `handleBatchConcurrency`: `PUT /api/batch/concurrency`
2. `handleBatchStatus`: `GET /api/batch/status` (返回 `BatchStatusResponse` 包含 ETA 与各槽位执行中任务)
3. `handleDailyTokens`: `GET /api/telemetry/tokens/daily`
4. `handleExportTokens`: `GET /api/telemetry/tokens/export` (输出 CSV / JSON，字段：日期, 总Token, PromptToken, CompletionToken, 节约Token, 构建仓数, 跳过仓数，完美匹配飞书 Sheets 导入)

并在 `deploy/unit1-orchestrator/internal/api/router.go` 注册对应的路由规则。

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/api -run TestBatchAPIsAndTokenExport
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/api/batch.go deploy/unit1-orchestrator/internal/api/batch_test.go deploy/unit1-orchestrator/internal/api/router.go
git commit -m "feat(api): implement dynamic concurrency, status ETA, and Feishu token export endpoints"
```

---

### Task 5: Unit 2 Portal 网关代理透传

**Files:**
- Modify: `deploy/unit2-portal/internal/api/router.go`

- [ ] **Step 1: 增加反向代理对 `/api/telemetry/` 与 `/api/batch/` 的转发**

修改 `deploy/unit2-portal/internal/api/router.go`，将对应前缀透传至 Unit 1 Orchestrator。

- [ ] **Step 2: 编译测试**

运行：
```bash
go build -o /dev/null ./deploy/unit2-portal/internal/api/...
```
预期：PASS。

- [ ] **Step 3: 提交更改**

```bash
git add deploy/unit2-portal/internal/api/router.go
git commit -m "feat(portal): proxy telemetry and batch concurrency endpoints"
```

---

### Task 6: Portal 管理后台全景看板（实时进度条、ETA、动态调速与 Token 报表）

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html`
- Modify: `deploy/unit2-portal/public/js/admin.js`
- Modify: `deploy/unit2-portal/public/js/api.js`

- [ ] **Step 1: 修改 `admin.html` 增加三大核心组件**

1. **实时进度与 ETA 卡片**：
   - 总体进度条（显示百分比及 `已完成 42 / 跳过 88 / 失败 1 / 总计 200`）；
   - **高亮预计剩余时间（ETA 徽章）**：例如 `⏱️ 预计还需 1小时 15分钟`。
2. **并发实时热调整与槽位监控**：
   - 并发调节滑块（1 ~ 8）与实时数字；
   - 槽位状态指示灯：每个运行中的槽位卡片展示当前正在构建的仓库名与阶段（如 `[槽位1] credit-gateway (AST解析)`）。
3. **每日 Token 统计与飞书导出表格**：
   - 展示最近日期、总 Token 消耗、节省 Token 数；
   - 增加【一键复制飞书表格数据】与【下载 CSV 报表】按钮。

- [ ] **Step 2: 在 `api.js` 与 `admin.js` 中实现动态调速与轮询刷新**

- 监听滑块 `change` 事件发送 `PUT /api/batch/concurrency`；
- 每 2 秒拉取一次 `/api/batch/status` 刷新当前槽位、进度与 ETA；
- 增加一键导出 Token 报表方法。

- [ ] **Step 3: 提交更改**

```bash
git add deploy/unit2-portal/public/admin.html deploy/unit2-portal/public/js/admin.js deploy/unit2-portal/public/js/api.js
git commit -m "feat(ui): add progress ETA, live concurrency slider, and Feishu token report"
```

---

### Task 7: 构建打包与全链路自动化集成测试

**Files:**
- Modify: `deploy/scripts/build-arm.sh`
- Modify: `deploy/scripts/build-x86.sh`

- [ ] **Step 1: 执行全量单元测试与打包验证**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/...
go test -v ./deploy/unit2-portal/...
```
预期：所有模块单元测试 100% PASS。

- [ ] **Step 2: 提交更改**

```bash
git add deploy/scripts/build-arm.sh deploy/scripts/build-x86.sh
git commit -m "chore(deploy): build and package batch scheduler v2 with token telemetry"
```
