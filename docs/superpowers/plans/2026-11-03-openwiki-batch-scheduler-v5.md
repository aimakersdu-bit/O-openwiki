# 全行代码库基线批量导入、算力防打穿调度与微服务业务聚合实施计划 (V5 纯deploy零侵入与生产级高可用终极版)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建支持全行 633+ 代码库筛选与批量导入的基线管理系统，实现支持**统一执行流水线（消除 Cron/批跑并发冲突）**、**【核心原则：100% 仅修改 deploy/，OpenWiki 源码零侵入 (Zero-Touch)】**、**真实 Token 采集（通过 deploy 脚本 Hook/透明网关，彻底还原 src/）**、**独立 TPM (Tokens Per Minute) 令牌桶与四级防打穿熔断体系**、**全行 633 仓多磁盘硬挂载分片与实时静态页面 (disk0/static) 托管**、**虚拟 Monorepo 跨盘业务聚合**、**单机虚拟机故障转移（HA）云盘漂移高可用**、**实时进度与高精度 ETA 预估**、**飞书多维表格（TSV/CSV）标准双通道导出**的平稳增量批跑引擎（BatchScheduler），实现全行级安全、高可用、可观测的代码知识构建。

---

## 核心架构与关键问题深度设计

### 1. 核心准则：仅修改 deploy/，OpenWiki 核心源码零侵入 (Zero-Touch)

#### 1.1 现状审核与合规结论
- **既有实施审核**：前期 Task 1 在 `src/agent/index.ts` 中直接修改了代码，增加了 `formatUsageTelemetry` 并修改了流式 chunk 提取，这违反了“**仅修改 deploy，不改动 openwiki 核心源码**”的架构边界要求。
- **重构原则与落地路径**：
  - **彻底还原 `src/` 与 `test/`**：将 `src/agent/index.ts` 回滚至干净的原生版本；
  - **无侵入运行时注入（Runtime Injection via `deploy/`）**：
    - 利用 Node.js 原生的 `NODE_OPTIONS="--import=/path/to/deploy/unit1-orchestrator/scripts/token-telemetry-hook.mjs"` 机制（该机制已经在 `builder.go` 中用于加载 `json-import-shim.mjs`）；
    - 在 `deploy/unit1-orchestrator/scripts/token-telemetry-hook.mjs` 中全局无感劫持 LangChain / `globalThis.fetch`，在进程退出时输出 `OPENWIKI_USAGE {"prompt_tokens":P,"completion_tokens":C,"total_tokens":T}`；
    - 调度流水线 `pipeline.go` 原生解析该输出，实现**OpenWiki 源码 0 修改，所有增强 100% 封装在 `deploy/` 体系内**。

---

### 2. 独立 TPM (Tokens Per Minute) 速率控制器与四级防打穿熔断体系

#### 2.1 为什么必须对 TPM (每分钟最大 Token 数) 设立独立设计？
- **传统并发槽位（Slot）的致命盲区**：
  - 调度器即使将并发严格压制在 2 个任务，如果这 2 个仓库包含大模块，一次 Prompt 上下文可能高达 60k ~ 100k Token。
  - 2 个任务在 10 秒内同时发送请求，将瞬时产生 120k ~ 200k Token 冲击。
  - 主流商业模型（Azure OpenAI、Anthropic、Qwen 百炼、企业内网 API 网关）对账号均设置了硬性的 **TPM 限制**（如 100k TPM 或 150k TPM）。
  - 一旦瞬时 Token 吞吐越界，网关将**立即返回 HTTP 429 ("Rate limit reached for TPM")**，导致任务批量失败。
- **因此，系统必须建立独立的 TPM 速率控制器与双重门禁体系**：

```
+-----------------------------------------------------------------------------------+
|                        四级算力防打穿与熔断保护流水线                             |
+-----------------------------------------------------------------------------------+
| 1. 增量零消耗过滤 (Git Diff Skip)                                                 |
|    - 每日检测远端 commit hash，无变更直接标记 skipped (耗时 < 1s, 消耗 0 Token)   |
|    - 过滤全行 90%+ 无更新仓库，实际每日仅调度发生发版变更的增量仓库               |
+-----------------------------------------+-----------------------------------------+
                                          | 有变更 (约 10%~20% 仓库)
                                          v
+-----------------------------------------------------------------------------------+
| 2. 调度层：槽位并发控制与夜间削峰 (DynamicRateLimiter & SlotPool)                 |
|    - 基于 sync.Cond 动态条件变量管理并发槽位（1~8 可调）                          |
|    - 实行夜间窗口保护：22:00-07:00 允许 4~8 并发批跑；日间窗口强制限流为 1~2 并发  |
+-----------------------------------------+-----------------------------------------+
                                          | 派发任务至 Agent 前置门禁
                                          v
+-----------------------------------------------------------------------------------+
| 3. 【独立设计】Token-Aware TPM 令牌桶控制器 (TPMRateLimiter)                      |
|    - 设定 MAX_TPM 阈值（如 120,000 Tokens/Min），每秒恒定注入 MAX_TPM / 60 许可   |
|    - 滑动窗口（Sliding Window）追踪最近 60s 内消耗的 Token 总量                   |
|    - 预检机制：根据待分析代码行数与 AST 节点数预估 Prompt Token，若当前剩余配额   |
|      不足以容纳该任务，该任务在通道中平滑休眠等待（Pacing），杜绝冲顶 429         |
+-----------------------------------------+-----------------------------------------+
                                          | 调用大模型 API 出现异常时
                                          v
+-----------------------------------------------------------------------------------+
| 4. 自适应熔断与 Retry-After 退避 (Adaptive Circuit Breaker)                       |
|    - 精确解析 429 响应头中的 `Retry-After: N` 或 `x-ratelimit-reset-tokens: Ns`   |
|    - 触发指数退避与全局 Jitter (首退避 5s, 最大 60s)                              |
|    - 全局动态降级：自动将 DynamicRateLimiter 并发热缩容（如 4 -> 2 -> 1）         |
|    - 连续 3 次探测成功后，阶梯式回升并发与 TPM 流速                              |
+-----------------------------------------------------------------------------------+
```

---

### 3. 全行 633 仓存储量化、实时静态页面 (static/) 布局与多盘硬挂载

#### 3.1 实时静态生成页面 (Visualizer Static Pages) 的存储选址与设计
- **静态页面的产生与规模**：
  - OpenWiki 在每个仓库构建完成时，除生成 `openwiki/` 文档外，还会由 `builder.go` 导出全量可视化静态站点（含 `index.html`、`client.js`、`graph.json` 代码图谱、Mermaid/Vendor 离线包），位于 `StaticOutputDir`。
  - 单仓静态产物：约 20MB ~ 50MB。
  - 全行 633 仓静态站点总规模：$633 \times 35\text{MB} \approx 22\text{GB} \sim 35\text{GB}$。
- **存储选址决策：独立部署在 `disk0/static/`**
  - **原因**：静态页面是 Unit 2 Portal / Nginx 直接面向全行研发人员高频访问的只读 Web 资产。
  - 将 `static/` 放置于高性能元数据盘 `disk0`，与承载海量 Git clone/checkout、AST 编译等重度随机写 I/O 的数据盘 `disk1 ~ disk3` 进行**物理级别读写隔离**！
  - 保证即使后台在执行 8 并发批跑压测，前端研发人员访问架构图谱和文档页面依然享有**毫秒级响应**，绝不出现页面卡顿。

#### 3.2 完整多盘挂载架构拓扑与容量规划

```
/data/ (总规划容量 >= 1.5TB)
├── disk0/ (高性能系统/元数据与 Web 资产盘 200GB SSD) -> 挂载点 /data/disk0
│   ├── db/                 <-- SQLite 数据库 (WAL 模式独占盘，彻底杜绝锁与 I/O 竞争)
│   ├── static/             <-- 【重点】633 仓实时静态可视化 Web 页面 (总计约 30GB, 面向 Portal 秒级直读)
│   ├── projects/           <-- 业务聚合虚拟 Monorepo 枢纽根目录 (跨盘软链中心)
│   └── logs/               <-- 系统运行日志与构建流水线输出
│
├── disk1/ (数据盘 1, 500GB SSD) -> 挂载点 /data/disk1
│   └── repos/              <-- 承载业务域 A (消费金融、零售信贷、渠道接入等) 约 210 仓源码与 Git 对象
│
├── disk2/ (数据盘 2, 500GB SSD) -> 挂载点 /data/disk2
│   └── repos/              <-- 承载业务域 B (核心账务、支付结算、清算核算等) 约 210 仓源码与 Git 对象
│
└── disk3/ (数据盘 3, 500GB SSD) -> 挂载点 /data/disk3
    └── repos/              <-- 承载业务域 C (风控中台、公共基建、数据智能等) 约 213 仓源码与 Git 对象
```

#### 3.3 业务聚合多仓（Virtual Monorepo Projects）的跨盘软链联动
- **跨盘兼容性**：符号链接（Symlink）记录目标绝对路径（如 `/data/disk1/repos/order-srv`），在 Linux VFS 中天然支持跨文件系统与跨独立挂载点（Cross-Mount-Points）；
- **I/O 聚合加速**：业务域工作区（如 `/data/disk0/projects/finance-domain/`）对子微服务建立软链，多仓跨仓检索问答时，I/O 负载被自动分摊至 `disk1`、`disk2`、`disk3`，聚合 IOPS 翻倍，彻底消除单盘饱和瓶颈。

---

### 4. 飞书多维表格（Bitable）标准双通道导出细节

- **通道一：TSV 剪贴板原生直贴（一键粘贴）**：
  - 采用 `\t` 分隔列，`\r\n` 分隔行；前端通过 `navigator.clipboard.writeText` 写入剪贴板；飞书多维表格单元格内 `Ctrl+V` 原生自动拆列填充。
- **通道二：带 UTF-8 BOM 的 RFC 4180 标准 CSV 下载**：
  - 响应头部注入 `\xEF\xBB\xBF` UTF-8 BOM，彻底杜绝 Windows Excel 打开乱码；
- **标准字段字典（Data Dictionary）**：
  - 包含 `统计日期`、`业务域/项目`、`构建成功仓数`、`增量跳过仓数`、`总消耗Token`、`输入PromptToken`、`补全CompletionToken`、`节约Token估算`、`模型限流重试次数`、`平均单仓耗时`。

---

### 5. 单机虚拟机宕机应对：磁盘硬挂载模式下的高可用（HA）方案

- **主备云盘漂移架构**：主节点 ECS-01（挂载硬云盘 disk0~disk3 提供服务）+ 备节点 ECS-02（冷备状态待命）；
- **故障转移时序（Failover Workflow）**：
  1. 探测告警：SLB/Watchdog 连续 3 次探测主节点失败；
  2. 强隔离（Fencing）：云 API 强制关机 ECS-01，防止双机挂盘脑裂破坏文件系统；
  3. 云盘漂移：云 API 解挂 ECS-01 磁盘并重新挂载至 ECS-02；
  4. 备机拉起：ECS-02 执行 `mount -a` 并启动 Unit 1/Unit 2，SQLite WAL 自动无损重放；
  5. 流量切换：SLB 流量秒级切入 ECS-02。
- **关键 SLA**：**RTO <= 3 分钟，RPO = 0（无数据丢失）**。

---

### File Structure Map (100% 封闭在 deploy/ 目录下)

| 文件路径 | 模块职责 | 变更类型 | 状态 |
| :--- | :--- | :--- | :--- |
| `deploy/unit1-orchestrator/scripts/token-telemetry-hook.mjs` | 【重点】Node.js 运行时 Hook：拦截 LangChain 真实 usage 并输出 OPENWIKI_USAGE，实现 src/ 零修改 | Create | 待接入替代 src 修改 |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | 数据库表迁移与 DAO：更新 `repos`、新增 `batch_runs`、`batch_tasks` 与 `daily_token_usage` | Modify | 已完成 (Commit `306e818`) |
| `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` | 批跑任务持久化、基线字段与 Token 报表单元测试 | Create | 已完成 (Commit `306e818`) |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go` | 基于 `sync.Cond` 的动态限流器、槽位池与 TPM 速率控制器 | Modify | 已完成基础限流，增加 TPM 控制 |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` | 并发动态扩容/缩容、TPM 速率与槽位唯一性单元测试 | Modify | 已完成 (Commit `1d17cc1`) |
| `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` | 统一构建流水线：收编 Git 拉取、FetchDiff、TPM 限流门禁、Token 提取与超时 Kill | Create | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go` | 批跑调度引擎：队列排队、断点续跑、并发热调整、ETA 预估 | Create | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` | 批跑生命周期、ETA 预估与动态并发单元测试 | Create | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/scheduler/cron.go` | 改造 Scheduler：复用统一流水线与全局限流器 | Modify | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/api/batch.go` | 批量导入（CSV/JSON）、动态并发调整、批跑状态（含 ETA）、飞书 Token 导出 API | Create | 已完成 (Commit `40f9961`) |
| `deploy/unit1-orchestrator/internal/api/batch_test.go` | 批量导入、并发调整与报表 API HTTP 单元测试 | Create | 已完成 (Commit `40f9961`) |
| `deploy/unit1-orchestrator/internal/api/router.go` | 注册批跑与 Token 报表路由并增加管理员权限拦截 | Modify | 已完成 (Commit `40f9961`) |
| `deploy/unit2-portal/internal/api/batch.go` | Portal 反向代理增加批跑与报表 API 透传 | Create | 已完成 (Commit `1239432`) |
| `deploy/unit2-portal/internal/api/router.go` | Portal 反向代理路由注册 | Modify | 已完成 (Commit `1239432`) |
| `deploy/unit2-portal/public/admin.html` | 后台管理界面增加“全行基线与算力批跑”看板 | Modify | 已完成 (Commit `3ea360b`) |
| `deploy/unit2-portal/public/js/admin.js` | 批量导入弹窗、批跑状态定时轮询、并发动态修改与报表渲染 | Modify | 已完成 (Commit `3ea360b`) |
| `deploy/unit2-portal/public/js/api.js` | 封装批量导入、动态并发与 Token 报表 API 客户端方法 | Modify | 已完成 (Commit `3ea360b`) |
| `deploy/scripts/verify-binaries.sh` | 自动化端到端测试与二进制验证脚本 | Create | 已完成 (Commit `6e9121b`) |
| `deploy/scripts/ecs-deploy.sh` | 阿里云 ECS Workbench 自动化部署脚本 | Create | 已完成 (Commit `4ef51c1`) |
| `deploy/scripts/package-release.sh` | 发布归档打包脚本 | Modify | 已完成 (Commit `f1cc6ab`) |

---

### Task 1: 纯 deploy 运行时 Hook 方案（还原 src/，OpenWiki 源码零侵入）

**Files:**
- Create: `deploy/unit1-orchestrator/scripts/token-telemetry-hook.mjs`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go` (NODE_OPTIONS 注入 hook)
- Revert: `src/agent/index.ts`, `src/cli/cli.tsx`, `src/cli/vfs-polyfill.ts`
- Test: `deploy/unit1-orchestrator/scripts/token_telemetry_test.mjs`

- [ ] **Step 1: 编写 Hook 捕获测试与脚本**
- [ ] **Step 2: 在 `deploy/unit1-orchestrator/scripts/token-telemetry-hook.mjs` 实现无侵入 usage 拦截**
- [ ] **Step 3: 回滚 `src/` 与 `test/` 源码变更，保持 OpenWiki 官方原版一尘不染**
- [ ] **Step 4: 运行 Node CLI 验证 `OPENWIKI_USAGE` 正常通过 Hook 打印**
- [ ] **Step 5: 提交更改**

---

### Task 2: 数据库与模型扩展（基线元数据、分级 Tier、Token 统计与批跑持久化）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go`
- Create: `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go`

- [x] **Step 1: 编写数据库迁移与 CRUD 测试** (`TestBatchRunAndDailyTokenUsageCRUD`)
- [x] **Step 2: 运行测试验证失败**
- [x] **Step 3: 更新 `sqlite.go` 字段映射与 DAO**
- [x] **Step 4: 运行测试验证通过**
- [x] **Step 5: 提交更改** (Commit: `306e818`)

---

### Task 3: 动态条件限流器、槽位池与 TPM 独立速率控制器

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go`

- [x] **Step 1: 编写动态限流与槽位池测试**
- [x] **Step 2: 实现 `rate_limiter.go`（含 sync.Cond 动态扩缩容、SlotPool 槽位池与 TPM 速率控制器）**
- [x] **Step 3: 运行测试验证通过**
- [x] **Step 4: 提交更改** (Commit: `1d17cc1`)

---

### Task 4: 统一构建流水线与批跑调度引擎（BatchScheduler）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/cron.go`

- [x] **Step 1: 编写流水线与批跑引擎测试**
- [x] **Step 2: 实现 `pipeline.go` 与 `batch_scheduler.go` 并重构 `cron.go`**
- [x] **Step 3: 运行测试验证通过**
- [x] **Step 4: 提交更改** (Commit: `421f91a`)

---

### Task 5: 批量基线导入、业务域合并与飞书报表 API

**Files:**
- Create: `deploy/unit1-orchestrator/internal/api/batch.go`
- Create: `deploy/unit1-orchestrator/internal/api/batch_test.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [x] **Step 1: 编写 API 单元测试**
- [x] **Step 2: 实现 `batch.go` 并在 `router.go` 注册路由**
- [x] **Step 3: 运行测试验证通过**
- [x] **Step 4: 提交更改** (Commit: `40f9961`)

---

### Task 6: Unit 2 Portal 网关反向代理透传

**Files:**
- Create: `deploy/unit2-portal/internal/api/batch.go`
- Modify: `deploy/unit2-portal/internal/api/router.go`

- [x] **Step 1: 配置反向代理透传路由**
- [x] **Step 2: 编译测试**
- [x] **Step 3: 提交更改** (Commit: `1239432`)

---

### Task 7: Portal 管理后台全景看板（实时进度条、ETA、动态调速与飞书导出）

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html`
- Modify: `deploy/unit2-portal/public/js/admin.js`
- Modify: `deploy/unit2-portal/public/js/api.js`

- [x] **Step 1: 在 `admin.html` 中新增综合看板面板**
- [x] **Step 2: 在 `api.js` 与 `admin.js` 中实现数据绑定与定时刷新**
- [x] **Step 3: 提交更改** (Commit: `3ea360b`)

---

### Task 8: 编译构建脚本与端到端自动化测试验证

**Files:**
- Create: `deploy/scripts/verify-binaries.sh`
- Create: `deploy/scripts/ecs-deploy.sh`
- Modify: `deploy/scripts/package-release.sh`

- [x] **Step 1: 运行全量单元测试与打包验证**
- [x] **Step 2: 提交更改** (Commit: `6e9121b`, `4ef51c1`, `f1cc6ab`)
