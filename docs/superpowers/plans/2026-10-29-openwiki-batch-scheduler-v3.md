# 全行代码库基线批量导入、算力防打穿调度与微服务业务聚合实施计划 (V3 生产级加固版)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建支持全行 500+ 代码库筛选与批量导入的基线管理系统，实现支持**统一执行流水线（消除 Cron/批跑并发冲突）**、**真实 Token 采集（方案 A）**、**基于条件变量的并发动态热缩放**、**实时进度与高精度 ETA 预估**、**飞书多维表格（TSV/CSV）标准导出**的平稳增量批跑引擎（BatchScheduler），并自动聚合微服务到业务项目域，实现全行级安全可观测的代码知识构建。

**Architecture:** 
1. **统一构建流水线与并发安全**：将单仓构建核心流程收敛为统一调度管道 `ExecuteBuildPipeline`，Cron 定时任务、手动触发构建与批处理作业**共用全局唯一 `runningBuilds` 锁与全局限流器**，彻底杜绝瞬间并发冲垮模型。
2. **真实 Token 采集（方案 A）**：在 openwiki agent 流式运行中捕获真实 `usage_metadata`，执行结束打印结构化标记 `OPENWIKI_USAGE {"prompt_tokens":P,"completion_tokens":C,"total_tokens":T}`，由 Orchestrator 精准解析入库，剔除写死假数据。
3. **可靠增量与零消耗跳过**：严谨复用 `GitClone` 与 `GitFetchAndDiff`，若远端代码无更新，安全记录为 `skipped` 并写入 `builds` 表，耗时 < 1 秒，Token 消耗为 0。
4. **动态条件限流器与槽位池**：基于 `sync.Cond` 实现 `DynamicRateLimiter`，彻底修复通道令牌回流导致的缩容失效缺陷；通过 `SlotPool` 分配稳定槽位 ID，杜绝面板槽位号重叠。
5. **微服务业务域增量合并**：导入时按业务域（`business_domain`）自动创建或增量追加微服务到 `projects`，并调用 `WorkspaceManager.PrepareWorkspace` 刷新虚拟 Monorepo 软链接。
6. **飞书多维表格标准适配**：提供 TSV（制表符分隔）复制与 CSV 导出端点，完美适配飞书多维表格直接粘贴入库。

**Tech Stack:** Go (Unit 1 Orchestrator), Node.js (OpenWiki Agent / CLI), SQLite3 (WAL 模式), Vanilla ES6 JS, HTML5/CSS3.

---

### File Structure Map

| 文件路径 | 模块职责 | 变更类型 |
| :--- | :--- | :--- |
| `src/agent/index.ts` | Agent 运行时：捕获 LangChain `usage_metadata`，运行结束打印 `OPENWIKI_USAGE` 真实数据 | Modify |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | 数据库表迁移与 DAO：更新 `repos`、新增 `batch_runs`、`batch_tasks` 与 `daily_token_usage` | Modify |
| `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` | 批跑任务持久化、基线字段与 Token 报表单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go` | 基于 `sync.Cond` 的动态限流器、槽位池与夜间窗口 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` | 并发动态扩容/缩容与槽位唯一性单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` | 统一构建流水线：收编 Git 拉取、FetchDiff、限流门禁、Token 提取与进程超时 Kill | Create |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go` | 批跑调度引擎：队列排队、断点续跑、并发热调整、ETA 预估 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` | 批跑生命周期、ETA 预估与动态并发单元测试 | Create |
| `deploy/unit1-orchestrator/internal/scheduler/cron.go` | 改造 Scheduler：复用统一流水线与全局限流器 | Modify |
| `deploy/unit1-orchestrator/internal/api/batch.go` | 批量导入（CSV/JSON）、动态并发调整、批跑状态（含 ETA）、飞书 Token 导出 API | Create |
| `deploy/unit1-orchestrator/internal/api/batch_test.go` | 批量导入、并发调整与报表 API HTTP 单元测试 | Create |
| `deploy/unit1-orchestrator/internal/api/router.go` | 注册批跑与 Token 报表路由并增加管理员权限拦截 | Modify |
| `deploy/unit2-portal/internal/api/router.go` | Portal 反向代理增加批跑与报表 API 透传 | Modify |
| `deploy/unit2-portal/public/admin.html` | 后台管理界面增加“全行基线与算力批跑”看板 | Modify |
| `deploy/unit2-portal/public/js/admin.js` | 批量导入弹窗、批跑状态定时轮询、并发动态修改与报表渲染 | Modify |
| `deploy/unit2-portal/public/js/api.js` | 封装批量导入、动态并发与 Token 报表 API 客户端方法 | Modify |
| `deploy/scripts/build-arm.sh` | 同步编译产物与静态资源脚本 | Modify |
| `deploy/scripts/build-x86.sh` | 同步编译产物与静态资源脚本 | Modify |

---

### Task 1: 真实 Token 采集改造（openwiki agent 方案 A）

**Files:**
- Modify: `src/agent/index.ts:530-580`
- Test: `test/agent/usage_telemetry.test.ts`

- [ ] **Step 1: 编写 Token 输出单元测试**

在 `test/agent/usage_telemetry.test.ts` 中编写测试：
```typescript
import { describe, it, expect } from "vitest";
import { formatUsageTelemetry } from "../../src/agent/index.js";

describe("formatUsageTelemetry", () => {
  it("should format prompt, completion and total tokens in standard json", () => {
    const output = formatUsageTelemetry({ promptTokens: 1200, completionTokens: 350, totalTokens: 1550 });
    expect(output).toContain("OPENWIKI_USAGE");
    const parsed = JSON.parse(output.replace("OPENWIKI_USAGE ", ""));
    expect(parsed.prompt_tokens).toBe(1200);
    expect(parsed.completion_tokens).toBe(350);
    expect(parsed.total_tokens).toBe(1550);
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
pnpm vitest run test/agent/usage_telemetry.test.ts
```
预期：FAIL。

- [ ] **Step 3: 在 `src/agent/index.ts` 中实现 Token 统计与输出**

在 `src/agent/index.ts` 中导出并实现 `formatUsageTelemetry`，并在流式执行或 agent 执行结束时输出：
```typescript
export interface TokenUsageStats {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
}

export function formatUsageTelemetry(stats: TokenUsageStats): string {
  return `OPENWIKI_USAGE ${JSON.stringify({
    prompt_tokens: stats.promptTokens,
    completion_tokens: stats.completionTokens,
    total_tokens: stats.totalTokens,
  })}`;
}
```
在 `runOpenWikiAgent` 执行完成退出前打印该标识：
```typescript
if (accumulatedUsage.totalTokens > 0) {
  console.log(formatUsageTelemetry(accumulatedUsage));
}
```

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
pnpm vitest run test/agent/usage_telemetry.test.ts
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add src/agent/index.ts test/agent/usage_telemetry.test.ts
git commit -m "feat(agent): collect real LLM token usage and emit OPENWIKI_USAGE marker"
```

---

### Task 2: 数据库与模型扩展（基线元数据、分级 Tier、Token 统计与批跑持久化）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go`
- Create: `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go`

- [ ] **Step 1: 编写数据库迁移与 CRUD 测试**

在 `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` 中编写完整测试：
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

	// 1. 测试创建带有 Tier、业务域和发版时间的 Repo 并检索
	repo := &db.Repo{
		ID:             "trade-gateway",
		Name:           "交易边缘网关",
		GitURL:         "git@github.com:bank/trade-gateway.git",
		Branch:         "master",
		LocalPath:      filepath.Join(tmpDir, "trade-gateway"),
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
	if fetched.Tier != "A" || fetched.BusinessDomain != "消费金融" || fetched.LastReleaseAt != "2025-11-20 10:00:00" {
		t.Errorf("Unexpected repo metadata: %+v", fetched)
	}

	// 2. 测试创建与更新 batch_runs
	run := &db.BatchRun{
		ID:             "batch-20261029-001",
		Name:           "全行发版仓库首次知识构建",
		TotalCount:     1,
		Status:         "running",
		MaxConcurrency: 2,
		NightModeOnly:  true,
	}
	if err := database.CreateBatchRun(run); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	if err := database.UpdateBatchConcurrency(run.ID, 4); err != nil {
		t.Fatalf("UpdateBatchConcurrency failed: %v", err)
	}

	// 3. 测试创建与更新 batch_tasks
	task := &db.BatchTask{
		BatchID: run.ID,
		RepoID:  repo.ID,
		Status:  "pending",
	}
	if err := database.CreateBatchTask(task); err != nil {
		t.Fatalf("CreateBatchTask failed: %v", err)
	}

	if err := database.UpdateBatchTaskStatus(run.ID, repo.ID, "completed", "构建成功", 120, 12500, 3200, 15700); err != nil {
		t.Fatalf("UpdateBatchTaskStatus failed: %v", err)
	}

	// 4. 测试 daily_token_usage 报表累加
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
	if err != nil || len(usages) != 1 || usages[0].TotalTokens != 15700 {
		t.Fatalf("Unexpected daily token usage: %+v", usages)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/db -run TestBatchRunAndDailyTokenUsageCRUD
```
预期：FAIL。

- [ ] **Step 3: 更新 `sqlite.go` 字段映射与 DAO**

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
	Tier           string `json:"tier"`            // A, B, C
	BusinessDomain string `json:"business_domain"` // 所属微服务业务域
	LastReleaseAt  string `json:"last_release_at"` // 最近发版时间
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}
```
2. 在 `migrate()` 中对 `repos` 增加列并创建 `batch_runs`、`batch_tasks`、`daily_token_usage`。
3. 补全 `CreateRepo`、`GetRepo`、`GetRepoByNameOrID`、`ListRepos` 中对 `tier`、`business_domain`、`last_release_at` 的 SELECT 与 INSERT。
4. 实现 `CreateBatchRun`, `GetBatchRun`, `UpdateBatchConcurrency`, `CreateBatchTask`, `UpdateBatchTaskStatus`, `RecordDailyTokenUsage`, `ListDailyTokenUsage`。

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/db -run TestBatchRunAndDailyTokenUsageCRUD
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/db/sqlite.go deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go
git commit -m "feat(db): extend repo baseline metadata and add batch persistence with daily token tables"
```

---

### Task 3: 动态条件限流器与槽位池（DynamicRateLimiter & SlotPool）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go`

- [ ] **Step 1: 编写动态限流与槽位池测试**

在 `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` 中编写：
```go
package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestDynamicRateLimiterAndSlotPool(t *testing.T) {
	limiter := scheduler.NewDynamicRateLimiter(2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	slot1, err := limiter.AcquireSlot(ctx)
	if err != nil {
		t.Fatalf("AcquireSlot 1 failed: %v", err)
	}
	slot2, err := limiter.AcquireSlot(ctx)
	if err != nil {
		t.Fatalf("AcquireSlot 2 failed: %v", err)
	}

	if slot1 == slot2 {
		t.Errorf("Slot IDs must be unique, got %d and %d", slot1, slot2)
	}

	// 此时容量为 2 且已满，再次获取应当超时
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelTimeout()
	if _, err := limiter.AcquireSlot(ctxTimeout); err == nil {
		t.Fatalf("Expected acquire to timeout when slots are full")
	}

	// 热扩容到 3
	limiter.SetConcurrency(3)
	slot3, err := limiter.AcquireSlot(ctx)
	if err != nil {
		t.Fatalf("AcquireSlot 3 after resize failed: %v", err)
	}

	limiter.ReleaseSlot(slot1)
	limiter.ReleaseSlot(slot2)
	limiter.ReleaseSlot(slot3)

	total, busy, idle := limiter.GetSlotStats()
	if total != 3 || busy != 0 || idle != 3 {
		t.Errorf("Expected 3 idle slots, got total=%d, busy=%d, idle=%d", total, busy, idle)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestDynamicRateLimiterAndSlotPool
```
预期：FAIL。

- [ ] **Step 3: 实现 `rate_limiter.go`**

创建 `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`：
```go
package scheduler

import (
	"context"
	"sync"
	"time"
)

type DynamicRateLimiter struct {
	mu          sync.Mutex
	cond        *sync.Cond
	concurrency int
	busyCount   int
	available   map[int]bool
}

func NewDynamicRateLimiter(initialConcurrency int) *DynamicRateLimiter {
	if initialConcurrency <= 0 {
		initialConcurrency = 2
	}
	l := &DynamicRateLimiter{
		concurrency: initialConcurrency,
		available:   make(map[int]bool),
	}
	l.cond = sync.NewCond(&l.mu)
	for i := 0; i < initialConcurrency; i++ {
		l.available[i] = true
	}
	return l
}

func (l *DynamicRateLimiter) SetConcurrency(n int) {
	if n <= 0 {
		n = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	old := l.concurrency
	l.concurrency = n

	if n > old {
		// 增加可用槽位
		for i := old; i < n; i++ {
			l.available[i] = true
		}
		l.cond.Broadcast()
	} else if n < old {
		// 缩容时清除多余的空闲槽位
		for i := n; i < old; i++ {
			delete(l.available, i)
		}
	}
}

func (l *DynamicRateLimiter) GetConcurrency() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.concurrency
}

func (l *DynamicRateLimiter) AcquireSlot(ctx context.Context) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}

		if l.busyCount < l.concurrency && len(l.available) > 0 {
			// 摘取一个最小的可用槽位 ID
			for slot := 0; slot < l.concurrency; slot++ {
				if l.available[slot] {
					delete(l.available, slot)
					l.busyCount++
					return slot, nil
				}
			}
		}

		// 等待槽位释放或扩容通知
		waitDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				l.mu.Lock()
				l.cond.Broadcast()
				l.mu.Unlock()
			case <-waitDone:
			}
		}()

		l.cond.Wait()
		close(waitDone)
	}
}

func (l *DynamicRateLimiter) ReleaseSlot(slot int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.busyCount--
	if l.busyCount < 0 {
		l.busyCount = 0
	}

	if slot < l.concurrency {
		l.available[slot] = true
	}
	l.cond.Broadcast()
}

func (l *DynamicRateLimiter) GetSlotStats() (total, busy, idle int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total = l.concurrency
	busy = l.busyCount
	idle = total - busy
	if idle < 0 {
		idle = 0
	}
	return total, busy, idle
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
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestDynamicRateLimiterAndSlotPool
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go
git commit -m "feat(scheduler): implement dynamic rate limiter with slot pool using sync.Cond"
```

---

### Task 4: 统一构建流水线与批跑调度引擎（BatchScheduler）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/cron.go`

- [ ] **Step 1: 编写流水线与批跑引擎测试**

在 `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` 中编写：
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

func TestBatchSchedulerExecutionAndETA(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "batch_test.db")
	database, _ := db.Open(dbPath)
	defer database.Close()

	_ = database.CreateRepo(&db.Repo{ID: "r1", Name: "服务1", LocalPath: tmpDir, Status: "active"})
	_ = database.CreateRepo(&db.Repo{ID: "r2", Name: "服务2", LocalPath: tmpDir, Status: "active"})

	builder := scheduler.NewBuilder("echo", "", "", tmpDir)
	engine := scheduler.NewBatchScheduler(database, builder, 2)

	err := engine.StartBatch(context.Background(), "batch-test", "测试批次", []string{"r1", "r2"}, false)
	if err != nil {
		t.Fatalf("StartBatch failed: %v", err)
	}

	status, err := engine.GetDetailedStatus("batch-test")
	if err != nil {
		t.Fatalf("GetDetailedStatus failed: %v", err)
	}
	if status.TotalCount != 2 {
		t.Errorf("Expected 2 total count, got %d", status.TotalCount)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestBatchSchedulerExecutionAndETA
```
预期：FAIL。

- [ ] **Step 3: 实现 `pipeline.go` 与 `batch_scheduler.go` 并重构 `cron.go`**

1. 创建 `pipeline.go`：封装单仓构建的标准流程：
   - 检查 `runningBuilds` 防重；
   - 获取 `DynamicRateLimiter` 槽位许可；
   - 执行 `GitClone` 或 `GitFetchAndDiff`（无更新直接写 `skipped`，耗时 $\le 1$ 秒，Token 计 0）；
   - 执行 `BuildRepo`（单仓超时 30 分钟），从 stdout 提取 `OPENWIKI_USAGE` 真实数据；
   - 累加写入 `daily_token_usage`。
2. 实现 `batch_scheduler.go`：维护批处理队列，启动多协程消费队列并派发执行流水线，实时更新活跃 Worker 槽位状态与 ETA 预估。
3. 改造 `cron.go`：让 `runBuild` 也调用同一套 `ExecuteBuildPipeline`，消除 Cron 与批跑的并发冲突。

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/scheduler -run TestBatchSchedulerExecutionAndETA
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/pipeline.go deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go deploy/unit1-orchestrator/internal/scheduler/cron.go deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go
git commit -m "feat(scheduler): unify build pipeline and implement BatchScheduler with real token collection"
```

---

### Task 5: 批量基线导入、业务域合并与飞书报表 API

**Files:**
- Create: `deploy/unit1-orchestrator/internal/api/batch.go`
- Create: `deploy/unit1-orchestrator/internal/api/batch_test.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [ ] **Step 1: 编写 API 单元测试**

在 `deploy/unit1-orchestrator/internal/api/batch_test.go` 编写：
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

func TestBatchAPIsAndFeishuExport(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "api_test.db")
	_ = db.InitDB(dbPath)
	defer db.CloseDB()

	cfg := &config.Config{ReposBaseDir: tmpDir, ProjectsBaseDir: tmpDir}
	builder := scheduler.NewBuilder("echo", "", "", tmpDir)
	sched := scheduler.NewScheduler(builder)
	server := api.NewServer(cfg, sched, nil, nil, nil)

	// 1. 测试批量导入
	importPayload := map[string]interface{}{
		"repos": []map[string]interface{}{
			{
				"id":              "order-srv",
				"name":            "订单微服务",
				"git_url":         "git@github.com:bank/order-srv.git",
				"tier":            "A",
				"business_domain": "消费金融",
			},
		},
		"auto_create_project": true,
	}
	body, _ := json.Marshal(importPayload)
	req := httptest.NewRequest("POST", "/api/repos/batch-import", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Batch import failed with status %d: %s", w.Code, w.Body.String())
	}

	// 2. 测试 TSV 格式导出 (直接兼容飞书多维表格粘贴)
	reqTSV := httptest.NewRequest("GET", "/api/telemetry/tokens/export?format=tsv", nil)
	wTSV := httptest.NewRecorder()
	server.ServeHTTP(wTSV, reqTSV)
	if wTSV.Code != http.StatusOK {
		t.Fatalf("TSV export failed with status %d", wTSV.Code)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/api -run TestBatchAPIsAndFeishuExport
```
预期：FAIL。

- [ ] **Step 3: 实现 `batch.go` 并在 `router.go` 注册路由**

在 `batch.go` 实现：
- `handleBatchImport`: 解析 CSV 或 JSON 列表，批量写入 `repos`（schedule 默认不设为单独 cron），将相同业务域的微服务**增量合并**到 `projects` 并调用 `PrepareWorkspace`；
- `handleBatchConcurrency`: 校验 admin 身份，调用 `SetConcurrency` 即刻热调整并发；
- `handleBatchStatus`: 返回进度、槽位详情及 ETA；
- `handleTokenExport`: 支持 `?format=tsv`（制表符分隔）与 `?format=csv`，表头严格按飞书多维表格（日期, 业务域, 总Token, PromptToken, CompletionToken, 节约Token）排布。
在 `router.go` 增加路由规则并注入管理员权限检查。

- [ ] **Step 4: 运行测试验证通过**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/internal/api -run TestBatchAPIsAndFeishuExport
```
预期：PASS。

- [ ] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/api/batch.go deploy/unit1-orchestrator/internal/api/batch_test.go deploy/unit1-orchestrator/internal/api/router.go
git commit -m "feat(api): implement batch import with domain merging and Feishu TSV/CSV token export"
```

---

### Task 6: Unit 2 Portal 网关反向代理透传

**Files:**
- Modify: `deploy/unit2-portal/internal/api/router.go`

- [ ] **Step 1: 配置反向代理透传路由**

修改 `deploy/unit2-portal/internal/api/router.go`，在代理匹配规则中加入 `/api/repos/batch-import`、`/api/batch/` 与 `/api/telemetry/`。

- [ ] **Step 2: 编译测试**

运行：
```bash
go build -o /dev/null ./deploy/unit2-portal/internal/api/...
```
预期：PASS。

- [ ] **Step 3: 提交更改**

```bash
git add deploy/unit2-portal/internal/api/router.go
git commit -m "feat(portal): add proxy rules for batch control and token telemetry"
```

---

### Task 7: Portal 管理后台全景看板（实时进度条、ETA、动态调速与飞书导出）

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html`
- Modify: `deploy/unit2-portal/public/js/admin.js`
- Modify: `deploy/unit2-portal/public/js/api.js`

- [ ] **Step 1: 在 `admin.html` 中新增综合看板面板**

1. 批量导入面板：支持粘贴 CSV / JSON，一键预演解析与提交；
2. 动态并发控制卡片：滑块调节 1~8 并发，显示活跃槽位实时指示灯；
3. 进度与 ETA 徽章：展示总体百分比与精准倒计时；
4. Token 报表卡片：展示每日用量历史，提供【复制为飞书表格数据】与【下载 CSV】。

- [ ] **Step 2: 在 `api.js` 与 `admin.js` 中实现数据绑定与定时刷新**

编写前端异步调用与定时轮询逻辑。

- [ ] **Step 3: 提交更改**

```bash
git add deploy/unit2-portal/public/admin.html deploy/unit2-portal/public/js/admin.js deploy/unit2-portal/public/js/api.js
git commit -m "feat(ui): add full-featured batch admin dashboard with live slider and Feishu export"
```

---

### Task 8: 编译构建脚本与端到端自动化测试验证

**Files:**
- Modify: `deploy/scripts/build-arm.sh`
- Modify: `deploy/scripts/build-x86.sh`

- [ ] **Step 1: 运行全量单元测试与打包验证**

运行：
```bash
go test -v ./deploy/unit1-orchestrator/...
go test -v ./deploy/unit2-portal/...
```
预期：所有模块单元测试 100% PASS。

- [ ] **Step 2: 提交更改**

```bash
git add deploy/scripts/build-arm.sh deploy/scripts/build-x86.sh
git commit -m "chore(deploy): build and package batch scheduler v3 production release"
```
