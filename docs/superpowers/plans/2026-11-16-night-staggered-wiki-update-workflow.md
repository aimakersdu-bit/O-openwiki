# 夜间分散错峰代码更新与知识流水线重构实施计划 (Night Staggered Wiki Update Workflow)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 重构 OpenWiki 调度与构建工作流：新仓库首次构建执行 `init` 后跳过无意义的重复 `update`；日常即时触发优先进行轻量级知识摄取与秒级图谱发布；日常定时更新改为夜间（01:00~05:00）基于仓库哈希分散错峰拉取代码并调用 `openwiki code --update` 进行全量知识更新，彻底消除大模型并发限流与超时卡死。

**Architecture:** 
1. **构建动作解耦**：在 `builder.go` 中区分首次全量构建（`init`）、日常增量更新（`update`）以及纯知识快速发布（`knowledge-sync`），`init` 成功后自动短路跳过 `update`。
2. **夜间错峰调度器 (Night Staggered Cron)**：在 `scheduler/cron.go` 中根据仓库 ID 进行一致性散列，将每日定时任务离散分布在夜间窗口（默认凌晨 01:00 ~ 05:00），避免所有仓库在 `0 2 * * *` 准点并发冲击大模型 upstream API。
3. **两阶段工作流机制**：日间（白天）提交自定义知识或代码通过轻量流水线快速同步 Web 图谱与 MCP；夜间调度由后台自动错峰拉取最新代码并执行深度的代码理解与知识沉淀。

**Tech Stack:** Go 1.22+, `github.com/robfig/cron/v3`, SQLite/MySQL (`internal/db`), OpenWiki CLI (Node.js 22)

---

### File Structure Map

| 文件路径 | 职责范围 |
| :--- | :--- |
| `deploy/unit1-orchestrator/internal/scheduler/builder.go` | 流水线引擎：新增 `BuildMode`、跳过 `init` 后的 `update`、支持纯知识快速发布模式 |
| `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` | 测试 `init` 跳过 `update` 及不同构建模式分支逻辑 |
| `deploy/unit1-orchestrator/internal/scheduler/cron.go` | 错峰调度核心：实现 `ComputeStaggeredSchedule` 散列算法，替换写死的默认 schedule |
| `deploy/unit1-orchestrator/internal/scheduler/cron_test.go` | 测试错峰生成算法在多仓库场景下的离散度、边界以及时间窗口约束 |
| `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` | 调度通道执行层：对接轻量构建模式与夜间全量更新模式 |
| `deploy/unit1-orchestrator/internal/api/router.go` | API 触发接口更新：支持按模式指定触发（如 `mode=knowledge` 或 `mode=full`） |

---

### Task 1: 消除 `init` 后的重复 `update` 调用并增加构建模式控制

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder_test.go`

- [ ] **Step 1: 编写测试用例验证 `init` 成功后跳过 `update`**
  - 在 `builder_test.go` 中新增测试 `TestBuilder_SkipsUpdateWhenInitRan`，模拟 `wikiDir` 不存在时执行 `init`，断言后续不会再尝试运行 `code --update`。

- [ ] **Step 2: 运行测试并确认失败**
  - 执行 `cd deploy/unit1-orchestrator && go test -v -run TestBuilder_SkipsUpdateWhenInitRan ./internal/scheduler/...`。

- [ ] **Step 3: 修改 `builder.go` 实现短路跳过逻辑**
  - 引入 `ranInit := false` 标识。当 `Step 0: openwiki code --init` 成功执行后，置 `ranInit = true`。
  - 在 `Step 1: openwiki code --update` 前增加判断：
    ```go
    if ranInit {
        b.logStep(repoID, "openwiki init already generated fresh wiki, skipping redundant openwiki update", &logBuf, onProgress...)
    } else {
        // 执行 openwiki code --update
    }
    ```

- [ ] **Step 4: 运行测试并验证通过**
  - 执行 `cd deploy/unit1-orchestrator && go test -v -run TestBuilder_SkipsUpdateWhenInitRan ./internal/scheduler/...`。

- [ ] **Step 5: 提交代码**
  - `git commit -m "fix(builder): skip redundant openwiki update after init"`

---

### Task 2: 实现夜间错峰哈希计算算法 (Night Staggered Schedule)

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/stagger.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/stagger_test.go`

- [ ] **Step 1: 编写错峰调度算法测试用例**
  - 在 `stagger_test.go` 中测试：
    1. 给定多个不同仓库 ID，计算出的 Cron 表达式必须严格落在夜间窗口（如 01:00 ~ 05:00）。
    2. 同一个仓库 ID 重复计算必须具有确定性（幂等）。
    3. 多个仓库的分钟（0-59）与小时（1-4）具有良好的离散分布，避免聚簇。
    4. 支持自定义时间窗口（默认起始 1 点，截止 5 点）。

- [ ] **Step 2: 运行测试并确认失败**
  - 执行 `cd deploy/unit1-orchestrator && go test -v -run TestComputeStaggeredSchedule ./internal/scheduler/...`。

- [ ] **Step 3: 实现 `ComputeStaggeredSchedule` 算法**
  - 使用 FNV-1a 或 SHA256 对 `repoID` 做哈希运算：
    ```go
    func ComputeStaggeredSchedule(repoID string, startHour, endHour int) string {
        // 计算 hash
        // hour = startHour + (hash / 60) % (endHour - startHour)
        // minute = hash % 60
        // return fmt.Sprintf("%d %d * * *", minute, hour)
    }
    ```

- [ ] **Step 4: 运行测试并验证通过**
  - 执行 `cd deploy/unit1-orchestrator && go test -v -run TestComputeStaggeredSchedule ./internal/scheduler/...`。

- [ ] **Step 5: 提交代码**
  - `git commit -m "feat(scheduler): implement deterministic staggered night cron schedule"`

---

### Task 3: 重构 `cron.go` 集成夜间错峰调度与自动迁移

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/cron.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/cron_test.go`

- [ ] **Step 1: 编写自动错峰注册的测试**
  - 测试如果仓库配置为 `staggered` 或使用旧的硬编码 `0 2 * * *`，调度器能够自动将其打散到夜间不同时间片，并在控制台日志中输出打散后的时间。

- [ ] **Step 2: 运行测试确认行为**
  - 执行测试并记录断言差异。

- [ ] **Step 3: 修改 `cron.go` 中的 `addJob` 逻辑**
  - 遇到空的、`staggered` 或是硬编码的 `0 2 * * *` 调度表达式时，自动调用 `ComputeStaggeredSchedule(repo.ID, 1, 5)` 生成夜间专属错峰时间（如 `18 3 * * *`）。
  - 更新内存中的注册记录与日志输出。

- [ ] **Step 4: 运行测试确保回归无误**
  - 执行 `cd deploy/unit1-orchestrator && go test -v ./internal/scheduler/...`。

- [ ] **Step 5: 提交代码**
  - `git commit -m "feat(scheduler): integrate staggered night cron schedule for repos"`

---

### Task 4: 支持白天“自定义知识快速同步模式”与夜间“全量代码增量分析模式”

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [ ] **Step 1: 编写快速知识同步测试**
  - 测试当传入模式为 `mode=knowledge-only` 时：跳过 Git Checkout 和 `openwiki code --update`，仅执行 Step 0.5 知识摄取与 Step 2~5 离线图谱提取和静态发布，整个过程耗时 < 2 秒。

- [ ] **Step 2: 实现快速模式接口与处理逻辑**
  - 在 `BuildRepoWithBuildID` 中支持 `skipUpdate bool` 参数。
  - 在 API `/api/build/trigger` 中解析 query 参数 `?mode=knowledge` 或 `?mode=full`（默认如果是夜间定时器触发则为 full，如果是日常文档推送则为 knowledge）。

- [ ] **Step 3: 运行完整流水线测试**
  - 执行 `cd deploy/unit1-orchestrator && go test -v ./internal/...`。

- [ ] **Step 4: 提交代码**
  - `git commit -m "feat(pipeline): support lightweight knowledge sync and full night update modes"`

---

### Task 5: 部署到阿里云 ECS 实机验证与验收

**Files:**
- Target Server: 阿里云 ECS `8.160.163.69`
- Target Repo: `zk1MTest`

- [ ] **Step 1: 交叉编译生产二进制**
  - 执行 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o orchestrator_linux_x86 ./main.go`。

- [ ] **Step 2: 上传部署并热重启服务**
  - 通过 `workbench upload` 上传二进制至 ECS 并替换 `/opt/openwiki/bin/orchestrator`，重启 `openwiki-orchestrator` 服务。

- [ ] **Step 3: 验证服务启动日志中的错峰调度时间**
  - 检查 `journalctl -u openwiki-orchestrator -n 30`：
    - 确认 `aimakersdu-bit/health` 与 `zk1MTest` 分别被分配在不同的夜间分散时间点（例如 `24 2 * * *` 和 `41 3 * * *`），不再拥挤在 `0 2 * * *`。

- [ ] **Step 4: 测试快速知识同步与夜间全量更新**
  - 调用快速构建接口，确认 1 秒内完成知识摄取与图谱刷新；
  - 触发全量更新，确认 `init` 不再重复调用 `update`。

- [ ] **Step 5: 最终验收与清理临时文件**
  - 确认浏览器前端和 MCP 接口均稳定响应，清理服务器与本地构建残留。

