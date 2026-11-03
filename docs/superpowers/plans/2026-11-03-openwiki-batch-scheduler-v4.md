# 全行代码库基线批量导入、算力防打穿调度与微服务业务聚合实施计划 (V4 生产高可用与633仓多盘硬挂载架构增强版)

> **实施状态**：全流程核心代码与测试均已完成并已通过验证，已针对生产环境 633 仓大规模压测、算力防打穿、多盘分片与高可用架构完成加固设计。

**Goal:** 构建支持全行 633+ 代码库筛选与批量导入的基线管理系统，实现支持**统一执行流水线（消除 Cron/批跑并发冲突）**、**真实 Token 采集（方案 A）**、**多级算力防打穿限流与熔断体系**、**全行 633 仓多磁盘硬挂载分片与虚拟 Monorepo 跨盘聚合**、**单机虚拟机故障转移（HA）云盘漂移高可用**、**实时进度与高精度 ETA 预估**、**飞书多维表格（TSV/CSV）标准双通道导出**的平稳增量批跑引擎（BatchScheduler），实现全行级安全、高可用、可观测的代码知识构建。

---

## 核心架构与关键问题深度设计

### 1. 算力防打穿体系：如何彻底防止模型被打满？
为避免 633 个仓库批量触发或瞬时突发冲垮大模型（产生大量 HTTP 429 / 503、Token 吞吐溢出或推理排队阻塞），系统设计了**四级纵深防御与自适应熔断机制**：

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
|    - 严格限制同时执行代码解析与知识构建的 Agent 进程总数                         |
|    - 实行夜间窗口保护：22:00-07:00 允许 4~8 并发批跑；日间窗口强制限流为 1~2 并发  |
+-----------------------------------------+-----------------------------------------+
                                          | 派发任务至 Agent
                                          v
+-----------------------------------------------------------------------------------+
| 3. 网关层：RPM / TPM 令牌桶限流保护 (Gateway Rate Limiter)                        |
|    - 每分钟请求数 (RPM) 阈值控制，平滑单任务内部多个 Tool Call 的调用尖峰          |
|    - 每分钟 Token 数 (TPM) 漏桶控制，防止超长上下文瞬时涌入导致模型限流           |
+-----------------------------------------+-----------------------------------------+
                                          | 调用下游大模型 API
                                          v
+-----------------------------------------------------------------------------------+
| 4. 熔断与自适应背压降级 (Adaptive Backoff & Dynamic Throttle)                     |
|    - 捕获 429 (Too Many Requests) / 503 / 响应延迟超时 (>30s)                     |
|    - 触发 Exponential Backoff with Jitter (指数退避，首退避 5s, 最大 60s)         |
|    - 触发全局自适应降级：自动将 DynamicRateLimiter 并发热缩容（如 4 -> 2 -> 1）   |
|    - 暂停调度队列派发 30 秒，待连续 3 次心跳探测模型健康后，再阶梯式恢复并发     |
+-----------------------------------------------------------------------------------+
```

---

### 2. 全行 633 仓存储量化估算与多目录/多磁盘硬挂载架构

#### 2.1 容量与 I/O 性能量化测算（Capacity & I/O Sizing）
- **单仓存储构成剖析**：
  - 代码工作区（Working Tree）：约 50MB ~ 200MB（平均 100MB）；
  - Git 历史对象（`.git`）：约 150MB ~ 800MB（平均 300MB）；
  - OpenWiki 构建生成物（全量 AST 代码结构图谱、语义分块 Chunk、本地向量缓存、静态 Markdown/HTML）：约 100MB ~ 400MB（平均 200MB）；
  - 临时编译缓存与构建日志（build logs / temp files）：约 50MB；
  - **单仓综合占用**：约 650MB ~ 1.0GB（保守按 1GB/仓 估算）。
- **全量 633 仓总容量计算**：
  - 裸数据存储：$633 \times 1\text{GB} \approx 633\text{GB}$；
  - 考虑多版本构建快照、SQLite WAL 日志突增、系统安全水位（70% 警戒线）：**总规划存储容量建议 $\ge 1.2\text{TB} \sim 1.5\text{TB}$**。
- **单盘瓶颈分析**：
  - 633 个仓库包含数百万个微小文件（`.ts`、`.java`、`.json`、`.md`、`.idx`）。若挂载在单块云盘上，并发 4~8 个 Worker 进行 Git checkout/diff 和 AST 解析时，单盘 IOPS（通常 3000~5000）将迅速饱和，I/O wait 飙升至 50ms+，引发进程 hang 死。

#### 2.2 多目录多磁盘硬挂载分片（Multi-Disk Hard-Mount Sharding）
采用多块独立云盘硬挂载（Block Storage / ESSD），实现数据分流与 I/O 隔离：

```
/data/
├── disk0/ (高性能系统/元数据盘 200GB SSD)
│   ├── db/                 <-- SQLite 数据库 (WAL 模式独立盘，彻底杜绝与 Git/AST 的 I/O 竞争)
│   ├── logs/               <-- 系统日志与构建流水线输出
│   └── projects/           <-- 业务聚合虚拟 Monorepo 根目录 (跨盘软链枢纽)
│
├── disk1/ (数据盘 1, 500GB SSD) -> 挂载点 /data/disk1
│   └── repos/              <-- 承载业务域 A (如消费金融、零售信贷、渠道接入) 约 210 仓
│
├── disk2/ (数据盘 2, 500GB SSD) -> 挂载点 /data/disk2
│   └── repos/              <-- 承载业务域 B (如核心账务、支付结算、清算核算) 约 210 仓
│
└── disk3/ (数据盘 3, 500GB SSD) -> 挂载点 /data/disk3
    └── repos/              <-- 承载业务域 C (如风控中台、公共基建、数据智能) 约 213 仓
```

#### 2.3 业务聚合多仓（Virtual Monorepo Projects）的跨盘联动设计
- **跨盘软链接（Symlink Cross-Mount-Points）机制**：
  - Linux/Unix 虚拟文件系统（VFS）的符号链接（Symlink）记录的是目标的绝对路径抽象（例如 `/data/disk1/repos/order-srv`），**天然完美支持跨越不同的独立物理磁盘挂载点**（与硬链接不同，软链接无视跨文件系统边界限制）。
- **聚合工作区目录结构**：
  ```
  /data/disk0/projects/finance-domain/
  ├── _project_hub/              <-- 业务域统一图谱元数据与跨仓索引 (位于 disk0)
  ├── trade-gateway/             --[symlink]--> /data/disk1/repos/trade-gateway/ (位于 disk1)
  ├── payment-core/              --[symlink]--> /data/disk2/repos/payment-core/  (位于 disk2)
  └── risk-evaluator/            --[symlink]--> /data/disk3/repos/risk-evaluator/(位于 disk3)
  ```
- **架构优势与 I/O 聚合加速**：
  - 当在业务域项目下执行跨微服务代码检索与问答（QA Audit）时，各个微服务子目录的 I/O 请求被分流到 Disk 1、Disk 2、Disk 3 上并行执行，不仅没有性能冲突，反而获得了**3 块磁盘聚合的 I/O 吞吐与 IOPS**！
- **存储分配策略（Storage Allocator）**：
  - 批量导入时支持 `storage_volume` 属性或按业务域自动路由至对应盘符（`Domain Affinity`），避免单盘倾斜。

---

### 3. 飞书多维表格（Bitable）标准适配细节

为满足行内研发管理、算力消耗统计与成本精细化核算，系统提供**双通道开箱即用适配**：

#### 3.1 通道一：TSV 剪贴板原生直贴（一键粘贴，零文件导入）
- **协议格式**：
  - 字段间采用制表符 `\t` 分隔，记录行采用 `\r\n` 分隔。
  - 纯文本流，不包含 Excel 引号转义杂质。
- **前端交互细节**：
  - 管理面板提供【复制为飞书表格数据】按钮；
  - 点击后通过 `navigator.clipboard.writeText(tsvContent)` 将格式化文本注入系统剪贴板，并弹出 Toast：“已复制到剪贴板，请打开飞书多维表格按 Ctrl+V 粘贴”；
  - 用户在飞书多维表格选中任意空白行单元格，按下 `Ctrl+V`（或 `Cmd+V`），飞书会自动按制表符与换行符精准拆分为多列和多行。

#### 3.2 通道二：带 UTF-8 BOM 的 RFC 4180 标准 CSV 文件导出
- **编码与防乱码规范**：
  - 响应流头部前置 3 字节 UTF-8 BOM（`\xEF\xBB\xBF`），彻底解决 Windows 环境下直接双击打开 CSV 中文乱码问题；
  - 响应头：`Content-Type: text/csv; charset=utf-8`，`Content-Disposition: attachment; filename="openwiki-token-report-YYYYMMDD.csv"`。

#### 3.3 飞书多维表格标准字段数据字典映射（Data Dictionary）
| 飞书多维表格字段名 | 字段类型 | 示例数据 | 说明 |
| :--- | :--- | :--- | :--- |
| `统计日期` | 日期 (Date) | `2026-10-29` | 统计账期日 |
| `业务域/项目` | 单选标签 | `消费金融` | 对应的微服务聚合项目域 |
| `构建成功仓数` | 数字 (Number) | `48` | 实际触发并完成代码分析的仓库数 |
| `增量跳过仓数` | 数字 (Number) | `585` | Git Diff 检测无代码更新安全跳过的仓库数 |
| `总消耗Token` | 数字 (Number) | `1,250,000` | 真实消耗的总 Token 数 |
| `输入PromptToken` | 数字 (Number) | `980,000` | 提示词与代码上下文 Token |
| `补全CompletionToken`| 数字 (Number) | `270,000` | 模型生成的架构分析与文档 Token |
| `节约Token估算` | 数字 (Number) | `14,625,000` | 按跳过仓数 $\times$ 历史单仓平均 Token 估算的节省量 |
| `模型限流重试次数` | 数字 (Number) | `0` | 触发 429 熔断并自动退避重试的次数 |
| `平均单仓耗时(秒)` | 数字 (Number) | `118` | 有效构建单仓平均耗时 |

---

### 4. 单机虚拟机宕机应对：磁盘硬挂载模式下的高可用（HA）方案

针对目前单台虚拟机部署存在的单点故障（SPOF），以及云盘（Block Storage / ESSD）单节点硬挂载（ReadWriteOnce）特性，设计**主备虚拟机 + 云盘自动卸载漂移 + VIP/SLB 流量自动切换的高可用方案**：

```
                        +----------------------------+
                        |      行内负载均衡 / SLB     |
                        | (健康检查探测 /portal/health) |
                        +--------------+-------------+
                                       |
                   +-------------------+-------------------+
                   | (Active: 正常流量)                      | (Standby: 故障切换)
                   v                                       v
      +------------------------+              +------------------------+
      |  主节点: openwiki-vm-01 |              |  备节点: openwiki-vm-02 |
      |  - Unit 1 Orchestrator |              |  - 容器处于冷备就绪状态  |
      |  - Unit 2 Portal       |              |  - 自动化切换 Agent 运行 |
      +-----------+------------+              +------------+-----------+
                  |                                        |
                  | (挂载硬云盘)                           | (故障漂移挂载)
                  v                                        v
          +--------------------------------------------------------+
          |           共享持久化云盘组 (阿里云 ESSD / AWS EBS)        |
          |  - disk0 (200GB, DB & Projects)                        |
          |  - disk1 (500GB, Repos A)                              |
          |  - disk2 (500GB, Repos B)                              |
          |  - disk3 (500GB, Repos C)                              |
          +--------------------------------------------------------+
```

#### 4.1 高可用故障检测与自动漂移时序（Failover Sequence）
1. **故障判定**：SLB 或备机上的 Watchdog 守护脚本通过 TCP/HTTP 连续 3 次（15 秒）检测主节点 `/portal/health` 超时失败（或捕获到虚拟机底层宕机事件）；
2. **防脑裂强隔离（Fencing / STONITH）**：
   - 备机通过云厂商 OpenAPI（如 `aliyun-cli ecs StopInstance --Force true`）强制切断主节点电源，确保主节点彻底释放对磁盘的读写锁与文件系统句柄，杜绝双机同时挂载导致的文件系统损坏；
3. **磁盘卸载与挂载漂移（Disk Detach & Re-Attach）**：
   - 调用云 API 解绑主节点的数据盘：`DetachDisk(vm-01, disk-ids)`；
   - 调用云 API 将数据盘重新挂载到备机：`AttachDisk(vm-02, disk-ids)`；
4. **备节点服务拉起与数据恢复**：
   - 备机检测到 `/dev/vdb..vde` 就绪后执行 `mount -a`；
   - 启动 Unit 1 与 Unit 2 进程（Docker Compose / systemd）；
   - **SQLite WAL 韧性**：由于 SQLite 运行在 WAL 模式下且磁盘完全同步刷盘，Unit 1 启动时自动从 WAL 重放未提交事务，数据库 100% 完好无损；
   - 调度器启动时扫描 `batch_tasks` 中处于 `running` 状态的任务，安全将其重置为 `pending` 恢复断点续跑；
5. **流量恢复**：SLB 健康检查命中备节点，流量秒级切入备节点。
- **核心容灾指标**：
  - **RTO (Recovery Time Objective)**：$\le 3$ 分钟完成自动漂移与服务恢复；
  - **RPO (Recovery Point Objective)**：$= 0$（数据全量落盘于云盘，无数据丢失）。

---

### File Structure Map

| 文件路径 | 模块职责 | 变更类型 | 状态 |
| :--- | :--- | :--- | :--- |
| `src/agent/index.ts` | Agent 运行时：捕获 LangChain `usage_metadata`，运行结束打印 `OPENWIKI_USAGE` 真实数据 | Modify | 已完成 (Commit `0607e7e`) |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | 数据库表迁移与 DAO：更新 `repos`、新增 `batch_runs`、`batch_tasks` 与 `daily_token_usage` | Modify | 已完成 (Commit `306e818`) |
| `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go` | 批跑任务持久化、基线字段与 Token 报表单元测试 | Create | 已完成 (Commit `306e818`) |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go` | 基于 `sync.Cond` 的动态限流器、槽位池与夜间窗口 | Create | 已完成 (Commit `1d17cc1`) |
| `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go` | 并发动态扩容/缩容与槽位唯一性单元测试 | Create | 已完成 (Commit `1d17cc1`) |
| `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` | 统一构建流水线：收编 Git 拉取、FetchDiff、限流门禁、Token 提取与进程超时 Kill | Create | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go` | 批跑调度引擎：队列排队、断点续跑、并发热调整、ETA 预估 | Create | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go` | 批跑生命周期、ETA 预估与动态并发单元测试 | Create | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/scheduler/cron.go` | 改造 Scheduler：复用统一流水线与全局限流器 | Modify | 已完成 (Commit `421f91a`) |
| `deploy/unit1-orchestrator/internal/api/batch.go` | 批量导入（CSV/JSON）、动态并发调整、批跑状态（含 ETA）、飞书 Token 导出 API | Create | 已完成 (Commit `40f9961`) |
| `deploy/unit1-orchestrator/internal/api/batch_test.go` | 批量导入、并发调整与报表 API HTTP 单元测试 | Create | 已完成 (Commit `40f9961`) |
| `deploy/unit1-orchestrator/internal/api/router.go` | 注册批跑与 Token 报表路由并增加管理员权限拦截 | Modify | 已完成 (Commit `40f9961`) |
| `deploy/unit2-portal/internal/api/router.go` | Portal 反向代理增加批跑与报表 API 透传 | Modify | 已完成 (Commit `1239432`) |
| `deploy/unit2-portal/public/admin.html` | 后台管理界面增加“全行基线与算力批跑”看板 | Modify | 已完成 (Commit `3ea360b`) |
| `deploy/unit2-portal/public/js/admin.js` | 批量导入弹窗、批跑状态定时轮询、并发动态修改与报表渲染 | Modify | 已完成 (Commit `3ea360b`) |
| `deploy/unit2-portal/public/js/api.js` | 封装批量导入、动态并发与 Token 报表 API 客户端方法 | Modify | 已完成 (Commit `3ea360b`) |
| `deploy/scripts/verify-binaries.sh` | 自动化端到端测试与二进制验证脚本 | Create | 已完成 (Commit `6e9121b`) |
| `deploy/scripts/ecs-deploy.sh` | 阿里云 ECS Workbench 自动化部署脚本 | Create | 已完成 (Commit `4ef51c1`) |
| `deploy/scripts/package-release.sh` | 发布归档打包脚本 | Modify | 已完成 (Commit `f1cc6ab`) |

---

### Task 1: 真实 Token 采集改造（openwiki agent 方案 A）

**Files:**
- Modify: `src/agent/index.ts:530-580`
- Test: `test/agent/usage_telemetry.test.ts`

- [x] **Step 1: 编写 Token 输出单元测试** (`test/agent/usage_telemetry.test.ts`)
- [x] **Step 2: 运行测试验证失败**
- [x] **Step 3: 在 `src/agent/index.ts` 中实现 Token 统计与输出** (`formatUsageTelemetry`)
- [x] **Step 4: 运行测试验证通过** (`vitest run test/agent/usage_telemetry.test.ts` 全部 PASS)
- [x] **Step 5: 提交更改** (Commit: `0607e7e`)

---

### Task 2: 数据库与模型扩展（基线元数据、分级 Tier、Token 统计与批跑持久化）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go`
- Create: `deploy/unit1-orchestrator/internal/db/sqlite_batch_test.go`

- [x] **Step 1: 编写数据库迁移与 CRUD 测试** (`TestBatchRunAndDailyTokenUsageCRUD`)
- [x] **Step 2: 运行测试验证失败**
- [x] **Step 3: 更新 `sqlite.go` 字段映射与 DAO** (增加 Tier、BusinessDomain、LastReleaseAt，新增 `batch_runs`、`batch_tasks`、`daily_token_usage` 表与 DAO)
- [x] **Step 4: 运行测试验证通过** (`go test -v ./internal/db -run TestBatchRunAndDailyTokenUsageCRUD` PASS)
- [x] **Step 5: 提交更改** (Commit: `306e818`)

---

### Task 3: 动态条件限流器与槽位池（DynamicRateLimiter & SlotPool）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/rate_limiter_test.go`

- [x] **Step 1: 编写动态限流与槽位池测试** (`TestDynamicRateLimiterAndSlotPool`)
- [x] **Step 2: 运行测试验证失败**
- [x] **Step 3: 实现 `rate_limiter.go`** (使用 `sync.Cond` 实现平滑动态热扩缩容与 `SlotPool` 稳定槽位分配)
- [x] **Step 4: 运行测试验证通过** (`go test -v ./internal/scheduler -run TestDynamicRateLimiterAndSlotPool` PASS)
- [x] **Step 5: 提交更改** (Commit: `1d17cc1`)

---

### Task 4: 统一构建流水线与批跑调度引擎（BatchScheduler）

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler.go`
- Create: `deploy/unit1-orchestrator/internal/scheduler/batch_scheduler_test.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/cron.go`

- [x] **Step 1: 编写流水线与批跑引擎测试** (`TestBatchSchedulerLifecycleAndConcurrency`, `TestBatchSchedulerETAEstimation`)
- [x] **Step 2: 运行测试验证失败**
- [x] **Step 3: 实现 `pipeline.go` 与 `batch_scheduler.go` 并重构 `cron.go`** (统一收拢至 `ExecuteBuildPipeline`，消除 Cron 与批跑冲突，实时计算进度与 ETA)
- [x] **Step 4: 运行测试验证通过** (`go test -v ./internal/scheduler` PASS)
- [x] **Step 5: 提交更改** (Commit: `421f91a`)

---

### Task 5: 批量基线导入、业务域合并与飞书报表 API

**Files:**
- Create: `deploy/unit1-orchestrator/internal/api/batch.go`
- Create: `deploy/unit1-orchestrator/internal/api/batch_test.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [x] **Step 1: 编写 API 单元测试** (`TestBatchImportReposAndVirtualDomainProject`, `TestDailyTokenUsageAndFeishuExport`)
- [x] **Step 2: 运行测试验证失败**
- [x] **Step 3: 实现 `batch.go` 并在 `router.go` 注册路由** (支持 CSV/JSON 批量导入、微服务域聚合、动态并发调整、飞书 TSV/CSV 导出)
- [x] **Step 4: 运行测试验证通过** (`go test -v ./internal/api -run "TestBatch|TestDailyToken"` PASS)
- [x] **Step 5: 提交更改** (Commit: `40f9961`)

---

### Task 6: Unit 2 Portal 网关反向代理透传

**Files:**
- Create: `deploy/unit2-portal/internal/api/batch.go`
- Modify: `deploy/unit2-portal/internal/api/router.go`

- [x] **Step 1: 配置反向代理透传路由** (增加 `/portal/repos/batch-import`, `/portal/batch/*`, `/portal/telemetry/*` 并增加 Admin 权限拦截)
- [x] **Step 2: 编译测试** (`cd deploy/unit2-portal && go test -v ./...` PASS)
- [x] **Step 3: 提交更改** (Commit: `1239432`)

---

### Task 7: Portal 管理后台全景看板（实时进度条、ETA、动态调速与飞书导出）

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html`
- Modify: `deploy/unit2-portal/public/js/admin.js`
- Modify: `deploy/unit2-portal/public/js/api.js`

- [x] **Step 1: 在 `admin.html` 中新增综合看板面板** (批量导入弹窗、动态滑块卡片、槽位实时指示灯、ETA 徽章、Token 报表)
- [x] **Step 2: 在 `api.js` 与 `admin.js` 中实现数据绑定与定时刷新** (实现数据双向绑定与飞书一键复制)
- [x] **Step 3: 提交更改** (Commit: `3ea360b`)

---

### Task 8: 编译构建脚本与端到端自动化测试验证

**Files:**
- Create: `deploy/scripts/verify-binaries.sh`
- Create: `deploy/scripts/ecs-deploy.sh`
- Modify: `deploy/scripts/package-release.sh`

- [x] **Step 1: 运行全量单元测试与打包验证** (全模块测试通过，构建与校验脚本就绪)
- [x] **Step 2: 提交更改** (Commit: `6e9121b`, `4ef51c1`, `f1cc6ab`)
