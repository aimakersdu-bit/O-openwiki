# 全行仓库纳管与多仓聚合架构设计文档

> **文档状态**: 设计阶段（Design Phase）
> **版本**: v1.0
> **日期**: 2026-11-04
> **审查基线**: deploy/unit1-orchestrator@HEAD, deploy/unit2-portal@HEAD

---

## 目录

1. [系统概述与边界](#1-系统概述与边界)
2. [架构总览](#2-架构总览)
3. [P1：633 仓库批量导入](#3-p1仓库批量导入)
4. [P2：多仓聚合与域管理](#4-p2多仓聚合与域管理)
5. [P3：Repo 知识纳管](#5-p3-repo知识纳管)
6. [数据模型](#6-数据模型)
7. [API 契约](#7-api契约)
8. [状态机与序列图](#8-状态机与序列图)
9. [模块接口规约](#9-模块接口规约)
10. [部署拓扑](#10-部署拓扑)

---

## 1. 系统概述与边界

### 1.1 系统目标

| 编号 | 目标 | 约束 |
|------|------|------|
| P1 | 将 633 个微服务仓库导入系统并完成知识库生成 | 算力（LLM TPM/RPM 限流）、存储（多盘均衡） |
| P2 | 建立业务域维度的多仓聚合视图，支持域级 RBAC 管理 | 底层 git 数据零冗余、域管理员自治 |
| P3 | 将 repo 内 `doc/knowledge` 目录的 MD 知识纳入 wiki 并静态化、图谱化 | 兼容无该目录的 repo |

### 1.2 系统边界

```
┌─────────────────────────────────────────────────────────────────┐
│                        浏览器 (HTTP)                              │
│   admin.html / index.html (Portal 前端)                          │
└────────────┬────────────────────────────────────┬───────────────┘
             │ :8099                               │
┌────────────▼──────────┐    HMAC    ┌────────────▼──────────────┐
│  Unit2 Portal (Go)    │───────────▶│  Unit1 Orchestrator (Go)  │
│  认证/RBAC/签名代理    │  X-OW-*    │  仓库/项目/构建/调度/存储   │
│  portal.db            │            │  orchestrator.db          │
│  :8099                │            │  :3009                    │
└───────────────────────┘            └──────────┬────────────────┘
                                                │ exec / fork
                                     ┌──────────▼──────────┐
                                     │  OpenWiki CLI (TS)  │
                                     │  openwiki code      │
                                     │  --update --print   │
                                     │  → wiki + graph.json│
                                     └──────────┬──────────┘
                                                │ HTTP
                                     ┌──────────▼──────────┐
                                     │  LLM Proxy (Go)     │
                                     │  TPM/RPM 限流        │
                                     │  429 重试            │
                                     │  → 上游 LLM (glm-5.2)│
                                     └─────────────────────┘
```

### 1.3 已就绪能力清单（代码审查结论）

| 模块 | 源文件 | 已实现能力 |
|------|--------|-----------|
| VolumeManager | `storage/volume.go:97` | 加权最少负载选盘（0.50容量+0.30IO+0.20数量）、80%硬约束、软链放置 |
| BatchScheduler | `scheduler/batch_scheduler.go:53` | 日夜窗口并发、动态热扩缩容、暂停/恢复/取消、中断恢复、ETA |
| DynamicRateLimiter | `scheduler/rate_limiter.go:14` | sync.Cond 槽位池、运行时无重启调参 |
| BuildPipeline | `scheduler/pipeline.go:61` | Git checkout/diff → openwiki init/update → token 提取 → DB 持久化 |
| Builder | `scheduler/builder.go:44` | openwiki init/update → graph.json → vendor → 原子目录交换 |
| LLMProxy | `llmproxy/proxy.go:44` | TPM/RPM 滑动窗口限流、429 指数退避重试、自动降并发、按 build 记账 |
| WorkspaceManager | `project/*.go:42` | 软链聚合、孤立链接清理、`.project-meta.json`、健康检查/修复 |
| batch-import | `api/batch.go:57` | CSV/JSON 批量导入 → VolumeManager 放置 → 按域自动聚合项目 |
| RBAC 域授权 | `portal/rbac_middleware.go` | user_domain_grants 表 → HMAC 签名 → AuthContext 域校验 |
| Project CRUD | `api/projects.go:42` | 项目创建/编辑/删除、group_leader 域权限隔离、links health/repair |

### 1.4 关键差距清单

| 差距编号 | 描述 | 影响 |
|---------|------|------|
| G1 | ECS 上 0 个存储卷注册，repos_base_dir 指向单盘 | 633 仓库无法分散存储 |
| G2 | 仓库 business_domain 普遍为空 | 域下拉为空、无法聚合 |
| G3 | BuildProjectWorkspace 只生成 quickstart.md，不调用 openwiki 全量构建 | 多仓 wiki 缺失 |
| G4 | 项目无 architecture_md 属性 | 域管理员无法维护架构描述 |
| G5 | 构建管线不检测 doc/knowledge 目录 | repo 内知识无法纳管 |

---

## 2. 架构总览

### 2.1 分层架构

```
┌──────────────────────────────────────────────────────────────┐
│  表示层 (Presentation)                                        │
│  Portal 前端: admin.html + admin.js + api.js                 │
├──────────────────────────────────────────────────────────────┤
│  接入层 (Access)                                              │
│  Unit2 Portal: 认证 → RBAC → HMAC 签名 → 代理转发            │
├──────────────────────────────────────────────────────────────┤
│  业务层 (Business)                                            │
│  Unit1 Orchestrator API:                                     │
│  ├─ Repo CRUD + Batch Import                                 │
│  ├─ Project CRUD + Workspace Build                           │
│  ├─ Build Trigger + Status                                   │
│  ├─ Storage Volume + Migration                               │
│  ├─ Telemetry + LLM Quota                                    │
│  └─ MCP / QA Chat                                            │
├──────────────────────────────────────────────────────────────┤
│  调度层 (Scheduling)                                          │
│  ├─ BatchScheduler: 日夜窗口 + 并发槽位 + 任务分发            │
│  ├─ DynamicRateLimiter: sync.Cond 槽位池                     │
│  └─ CronScheduler: 单仓定时构建                              │
├──────────────────────────────────────────────────────────────┤
│  管线层 (Pipeline)                                            │
│  BuildPipeline: Git → openwiki CLI → Token 提取 → DB          │
│  Builder: init → update → graph → vendor → 原子交换          │
├──────────────────────────────────────────────────────────────┤
│  存储层 (Storage)                                             │
│  ├─ VolumeManager: 加权选盘 + 软链放置                       │
│  ├─ Migrator: 零停机卷间迁移                                  │
│  ├─ SQLite: orchestrator.db + portal.db                      │
│  └─ 文件系统: /data/disk{N}/repos/ + /data/.../links/repos/  │
├──────────────────────────────────────────────────────────────┤
│  知识层 (Knowledge)                                           │
│  OpenWiki CLI (TS): agent → wiki + graph.json + 静态站点      │
│  LLMProxy: TPM/RPM 限流 → 上游 LLM                           │
└──────────────────────────────────────────────────────────────┘
```

### 2.2 存储拓扑（目录模拟多盘）

测试环境用目录模拟多块物理盘；生产环境替换为真实挂载点。

```
/data/
├── disk0/                    # 模拟物理盘 0
│   ├── repos/                # 物理仓库存储 (git clone 目标)
│   │   ├── svc-payment/
│   │   └── svc-risk-engine/
│   └── links/
│       └── repos/            # 统一逻辑访问层 (软链)
│           ├── svc-payment → /data/disk0/repos/svc-payment
│           └── svc-risk-engine → /data/disk1/repos/svc-risk-engine
├── disk1/                    # 模拟物理盘 1
│   └── repos/
│       └── svc-channel-gateway/
├── disk2/                    # 模拟物理盘 2
│   └── repos/
├── disk3/                    # 模拟物理盘 3
│   └── repos/
├── openwiki/
│   ├── db/
│   │   ├── orchestrator.db
│   │   └── portal.db
│   ├── repos/                # (兼容旧配置, 逐步迁移到 disk{N})
│   ├── projects/             # 多仓项目 workspace (软链聚合)
│   │   └── proj-xxxx/
│   │       ├── svc-payment → /data/disk0/links/repos/svc-payment
│   │       ├── _project_hub/
│   │       │   ├── openwiki/      # 多仓聚合 wiki
│   │       │   │   ├── quickstart.md
│   │       │   │   ├── architecture.md  (新增: P2.3)
│   │       │   │   └── knowledge/       (新增: P3)
│   │       │   └── contracts/
│   │       └── .project-meta.json
│   ├── static/               # 静态化输出
│   │   ├── {repoID}/         # 单仓静态站点
│   │   └── projects/
│   │       └── {projectID}/  # 多仓聚合静态站点
│   └── logs/
└── disk_log/                 # 构建日志持久化
    └── builds/
```

**软链层级关系**（三层间接，零数据冗余）：

```
物理层:   /data/disk{N}/repos/{repoID}/          ← git clone 物理数据
逻辑层:   /data/disk0/links/repos/{repoID}       → 软链 → 物理层
聚合层:   /data/openwiki/projects/{projID}/{alias} → 软链 → 逻辑层
```

> **设计决策**: 三层软链而非两层。逻辑层（links/repos）解耦了物理盘位置变更对聚合层的影响——当仓库在盘间迁移时，只需更新逻辑层软链目标，聚合层软链无需变动。

---

## 3. P1：633 仓库批量导入

### 3.1 存储卷注册（目录模拟）

#### 3.1.1 设计

测试环境在单块 40GB 磁盘上用目录模拟 4 块 100GB 盘：

| 卷 ID | 模拟挂载点 | 模拟容量 | 用途 |
|-------|-----------|---------|------|
| vol0 | `/data/disk0` | 100GB | 物理仓库存储 + 逻辑访问层 |
| vol1 | `/data/disk1` | 100GB | 物理仓库存储 |
| vol2 | `/data/disk2` | 100GB | 物理仓库存储 |
| vol3 | `/data/disk3` | 100GB | 物理仓库存储 |

#### 3.1.2 注册流程

```
1. 在 ECS 上创建目录结构:
   mkdir -p /data/disk{0,1,2,3}/repos
   mkdir -p /data/disk0/links/repos

2. 调用 VolumeManager.RegisterVolume() 注册每个卷:
   POST /api/storage/volumes  {"id":"vol0","mount_point":"/data/disk0"}
   POST /api/storage/volumes  {"id":"vol1","mount_point":"/data/disk1"}
   POST /api/storage/volumes  {"id":"vol2","mount_point":"/data/disk2"}
   POST /api/storage/volumes  {"id":"vol3","mount_point":"/data/disk3"}

3. RegisterVolume 内部:
   a. statfs(mount_point) 获取磁盘容量 → 更新 volumes 表
   b. CreateVolume 写入 DB
   (目录模拟时 statfs 返回的是 /data 所在磁盘的真实容量,
    生产环境返回各自挂载点的真实容量)
```

#### 3.1.3 目录模拟的容量限制问题

**问题**: `syscall.Statfs("/data/disk0")` 在目录模拟下返回的是 `/data` 所在磁盘（40GB）的容量，所有卷共享同一物理磁盘。VolumeManager 的 80% 硬约束会误判。

**方案**: 在 `VolumeManager` 中增加 `SetStatfsFunc` 覆盖（已存在该接口，`volume.go:43`），为测试环境注入模拟容量：

```go
// 测试环境: 注入模拟 100GB 容量
vm.SetStatfsFunc(func(path string) (int64, int64, error) {
    // 模拟每卷 100GB, 按 DB 中已注册仓库的 size_bytes 估算 used
    total := int64(100 * 1024 * 1024 * 1024) // 100GB
    used := queryUsedBytesForVolume(path)     // 从 DB 汇总
    return total, used, nil
})
```

**生产环境**: 删除 `SetStatfsFunc` 覆盖，恢复 `DefaultStatfs`（真实 `syscall.Statfs`）。

### 3.2 633 仓库导入清单

#### 3.2.1 数据格式

支持 CSV（表头驱动）和 JSON 数组两种格式，由 Portal 前端 `batchDataInput` 文本框接收。

**CSV 格式**:
```csv
id,name,git_url,branch,tier,business_domain,last_release_at
svc-payment,payment-service,http://gitlab.xxx/svc/payment.git,master,A,消金,2026-10-15
svc-risk-engine,risk-engine,http://gitlab.xxx/svc/risk.git,master,B,风控,2026-09-20
svc-channel-gateway,channel-gateway,http://gitlab.xxx/svc/channel.git,main,B,渠道,2026-10-30
```

**字段规约**:

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | string | 是 | 仓库唯一标识（建议用 gitlab group/project 格式） |
| name | string | 否 | 显示名称，缺省取 id |
| git_url | string | 是 | Git clone URL |
| branch | string | 否 | 分支，缺省 `master` |
| tier | string | 否 | 优先级 A/B/C，缺省 `B`（A 高优先级优先构建） |
| business_domain | string | **是** | 所属业务域（域管理与聚合的必要条件） |
| last_release_at | string | 否 | 最近发版时间（ISO 日期） |

> **G2 修复**: `business_domain` 在批量导入时标记为必填。`batch.go:69` 的 `BatchImportRepoItem` 已有该字段，导入逻辑 `batch.go:106` 在 `item.BusinessDomain != ""` 时自动聚合域项目。需在导入入口增加校验：缺 `business_domain` 的行拒绝导入或归入默认域 `未分配`。

#### 3.2.2 域划分建议

域应对应行内信息技术部的组织形式。建议域列表（需与实际组织确认）：

| 域 | 说明 | 预估仓库数 |
|----|------|-----------|
| 消金 | 消费金融 | ~80 |
| 零售 | 零售业务 | ~120 |
| 渠道 | 渠道与开放平台 | ~60 |
| 风控 | 风险控制 | ~50 |
| 数据 | 数据平台与大数据 | ~70 |
| 技术 | 技术中台与基础设施 | ~90 |
| 支付 | 支付与清算 | ~40 |
| 其他 | 未归类 | ~123 |

### 3.3 算力调度设计

#### 3.3.1 调度架构

```
                    ┌─────────────────────┐
                    │  BatchScheduler     │
                    │  runBatchLoop()     │
                    └──────────┬──────────┘
                               │ 遍历 pending tasks
                    ┌──────────▼──────────┐
                    │  DynamicRateLimiter │
                    │  AcquireSlot(ctx)   │
                    │  sync.Cond 槽位池    │
                    └──────────┬──────────┘
                               │ slot acquired
                    ┌──────────▼──────────┐
                    │  BuildPipeline      │
                    │  Execute(ctx, repo) │
                    └──────────┬──────────┘
                               │ fork openwiki CLI
                    ┌──────────▼──────────┐
                    │  LLMProxy           │
                    │  TPM/RPM 限流        │
                    │  429 → 退避重试      │
                    │  → 自动降并发        │
                    └──────────┬──────────┘
                               │ HTTP
                    ┌──────────▼──────────┐
                    │  上游 LLM (glm-5.2) │
                    └─────────────────────┘
```

#### 3.3.2 限流机制（三层）

**第一层: 并发槽位 (DynamicRateLimiter)**

```
源文件: scheduler/rate_limiter.go:14

机制:
- sync.Mutex + sync.Cond 管理固定大小槽位池
- AcquireSlot(ctx) 阻塞等待空闲槽位, context 取消时唤醒
- ReleaseSlot(slot) 归还槽位
- SetConcurrency(n) 运行时热扩缩容:
  - 扩容: 补充新槽位, Broadcast 唤醒等待者
  - 缩容: 删除空闲槽位, 运行中任务归还时自然收敛

配置 (system_settings 表):
- day_max_concurrency:   2  (07:00-22:00)
- night_max_concurrency: 4  (22:00-07:00)

切换: BatchScheduler.ApplyDayNightConcurrency() 每轮循环检查
```

**第二层: TPM/RPM 滑动窗口 (TPMRateLimiter)**

```
源文件: llmproxy/limiter.go:11

机制:
- 1 分钟滑动窗口, records[] 存储每条请求的 timestamp + tokens
- prune() 清理过期记录
- Acquire(ctx, estTokens): 阻塞直到 RPM < maxRPM 且 TPM+est < maxTPM
- estTokens = bodyBytes / 4 (请求体字节数粗估)
- RecordRealUsage(est, actual): 用真实 token 数校正

配置 (system_settings 表):
- max_tpm: 0 (0 = 不限, 需按 LLM 供应商配额设置)
- max_rpm: 0 (0 = 不限)

LLMProxy.ServeHTTP 流程:
1. 解析 buildID (from Authorization: Bearer ow-build-{buildID})
2. 估算 estTokens
3. limiter.Acquire(ctx, estTokens)
4. 转发到上游, 复制 headers, 注入真实 API key
5. 429/503 → record429 → 指数退避重试 (base=5s, max=4 次, cap=60s)
6. 连续 3 次 429 → throttleCallback(-1) 自动降并发
7. 成功 → 提取 usage → AddTokens(buildID) → 记账
```

**第三层: 上游 429 自适应退避 (LLMProxy)**

```
源文件: llmproxy/proxy.go:148

退避算法:
- base = 5s (可配)
- backoff = base * (2^attempt)  // 指数退避
- cap = 60s
- jitter = rand(0-50ms)
- 优先读取 Retry-After / x-ratelimit-reset-requests 响应头
- maxRetries = 4

自适应降并发:
- 连续 429 >= 3 次 → throttleCallback(-1)
- throttleCallback 在 Server 构造时绑定:
  cur := lim.GetConcurrency()
  if cur > 1 { lim.SetConcurrency(cur - 1) }
```

#### 3.3.3 日夜窗口调度

```
源文件: scheduler/batch_scheduler.go:113

窗口定义:
- 夜间窗口: 22:00 - 07:00 (windowStartHour=22, windowEndHour=7)
- 白天窗口: 07:00 - 22:00

行为:
- runBatchLoop() 每轮循环调用 ApplyDayNightConcurrency(now)
- 夜间: concurrency = night_max_concurrency (默认 4)
- 白天: concurrency = day_max_concurrency (默认 2)
- NightModeOnly=true 时, 白天不执行任务, sleep 30s 等待夜间窗口
```

#### 3.3.4 重试机制

```
源文件: scheduler/pipeline.go:131 (Git 层)
         llmproxy/proxy.go:148 (LLM 层)

Git 层:
- git clone 失败 → 标记 build failed, 不自动重试
- git fetch 失败 → 标记 build failed
- git pull 失败 → 标记 build failed

LLM 层 (LLMProxy):
- 429/503 → 自动指数退避重试, 最多 4 次
- 重试次数记录到 BuildUsage.RateLimitedRetries
- 连续 3 次 429 → 自动降并发
- 重试耗尽 → 转发最后一个 429 响应 → openwiki CLI 报错 → build failed

Batch 层:
- build failed 的 task 不自动重试
- 可通过前端「重试失败任务」手动触发 (需新增 API, 见 G-建议)
```

#### 3.3.5 633 仓库的分批策略

**建议策略**: 按域分批导入 + 按优先级排序构建

```
批次 1: tier=A 的所有仓库 (高优先级, ~100 个)
批次 2: tier=B 消金 + 零售 (~200 个)
批次 3: tier=B 渠道 + 风控 + 数据 (~180 个)
批次 4: tier=B 技术 + 支付 (~130 个)
批次 5: tier=C + 其他 (~23 个)

每批次:
- concurrency = 2 (白天) / 4 (夜间)
- auto_start = true
- night_mode_only = false (测试环境可全天跑)
```

### 3.4 构建管线完整流程

```
源文件: scheduler/pipeline.go:131 Execute()

┌─────────────────────────────────────────────────────┐
│  BuildPipeline.Execute(ctx, repo, force, slot)      │
├─────────────────────────────────────────────────────┤
│ 1. 防重入检查 (runningBuilds map)                    │
│ 2. CreateBuild(repoID, "", "running") → buildID      │
│                                                      │
│ 3. Git Checkout:                                     │
│    a. .git 不存在 → GitClone(url, path, branch)     │
│    b. .git 存在 + !force:                           │
│       GitFetchAndDiff → 无更新 → skip (省 150k token)│
│       有更新 → GitPull                              │
│    c. .git 存在 + force: GitPull                    │
│                                                      │
│ 4. Builder.BuildRepoWithBuildID(buildID, repo):     │
│    a. Step 0: openwiki init (如 .openwiki 不存在)   │
│    b. Step 1: openwiki code --update --print         │
│    c. Step 2: exportGraph (graph.json)               │
│    d. Step 3: copyVisualizerAssets                   │
│    e. Step 4: copyVendorAssets                       │
│    f. Step 5: 原子目录交换 (staging → final)        │
│                                                      │
│ 5. Token 提取:                                       │
│    a. LLMProxy.PopBuildUsage(buildID)                │
│    b. fallback: ParseTokenUsage(log)                 │
│                                                      │
│ 6. FinishBuildWithTokens(...) → DB                   │
│ 7. RecordDailyTokenUsage → DB                        │
│ 8. 日志持久化 → disk_log/builds/general/{repoID}.log │
│ 9. onProgress(finalStatus) → SSE 推送                │
└─────────────────────────────────────────────────────┘
```

---

## 4. P2：多仓聚合与域管理

### 4.1 软链聚合无数据冗余（P2.1）

#### 4.1.1 设计原理

底层仓库 git clone 到物理盘，多仓项目通过软链访问，零数据冗余：

```
物理层 (git clone, 真实数据):
  /data/disk1/repos/svc-payment/.git/        ← 唯一的 git 仓库数据
  /data/disk1/repos/svc-payment/src/
  /data/disk1/repos/svc-payment/openwiki/    ← 单仓 wiki

逻辑层 (统一访问入口):
  /data/disk0/links/repos/svc-payment → /data/disk1/repos/svc-payment

聚合层 (多仓项目 workspace):
  /data/openwiki/projects/proj-consumption-finance/
    ├── svc-payment        → /data/disk0/links/repos/svc-payment
    ├── svc-loan-core      → /data/disk0/links/repos/svc-loan-core
    ├── svc-collection     → /data/disk0/links/repos/svc-collection
    ├── _project_hub/
    │   └── openwiki/      ← 多仓聚合 wiki (独立生成)
    └── .project-meta.json
```

#### 4.1.2 WorkspaceManager.PrepareWorkspace 算法规约

```
源文件: project/*.go:42

输入: Project (含 LocalPath), []RepoWithAlias
输出: wsDir (workspace 目录路径), error

算法:
1. 确定 wsDir = project.LocalPath || filepath.Join(BaseProjectsDir, project.ID)
2. MkdirAll(wsDir)
3. MkdirAll(wsDir/_project_hub/openwiki)
4. MkdirAll(wsDir/_project_hub/contracts)
5. 对每个 repo:
   a. alias = r.AliasName || r.ID
   b. activeAliases[alias] = true
   c. linkPath = filepath.Join(wsDir, alias)
   d. sourcePath = r.LocalPath
   e. 若 linkPath 已存在:
      - 是软链且目标正确 → continue (幂等)
      - 否则 → Remove(linkPath)
   f. os.Symlink(sourcePath, linkPath)
6. 清理孤立软链: 遍历 wsDir, 不在 activeAliases 中的软链 → Remove
7. 写 .project-meta.json
8. return wsDir
```

### 4.2 域 RBAC 管理（P2.2）

#### 4.2.1 域管理模型

```
┌──────────────────────────────────────────────────┐
│  Portal DB (portal.db)                            │
│                                                   │
│  users                     user_domain_grants     │
│  ┌──────────┐              ┌──────────────────┐  │
│  │ username  │──1:N──▶      │ username         │  │
│  │ role      │              │ business_domain  │  │
│  │ ...       │              │ granted_by       │  │
│  └──────────┘              │ created_at       │  │
│                             └──────────────────┘  │
│  roles: super_admin / admin / group_leader        │
└──────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────┐
│  Orchestrator DB (orchestrator.db)                │
│                                                   │
│  repos                      projects              │
│  ┌──────────────────┐       ┌──────────────────┐ │
│  │ id               │       │ id               │ │
│  │ business_domain  │       │ business_domain  │ │
│  │ volume_id        │       │ local_path       │ │
│  │ ...              │       │ ...              │ │
│  └──────────────────┘       └──────────────────┘ │
│         │                          │              │
│         └────── project_repos ─────┘              │
│                ┌──────────────────┐               │
│                │ project_id       │               │
│                │ repo_id          │               │
│                │ alias_name       │               │
│                │ role             │               │
│                └──────────────────┘               │
└──────────────────────────────────────────────────┘
```

#### 4.2.2 RBAC 权限矩阵

| 操作 | super_admin | admin | group_leader | 其他 |
|------|:-----------:|:-----:|:------------:|:----:|
| 注册/导入仓库 | ✅ 全部 | ✅ 全部 | ❌ | ❌ |
| 编辑仓库 | ✅ 全部 | ✅ 全部 | ❌ | ❌ |
| 删除仓库 | ✅ 全部 | ✅ 全部 | ❌ | ❌ |
| 创建项目 | ✅ 全部域 | ✅ 全部域 | ✅ 仅授权域 | ❌ |
| 编辑项目 | ✅ 全部 | ✅ 全部 | ✅ 仅授权域内项目 | ❌ |
| 触发项目构建 | ✅ 全部 | ✅ 全部 | ✅ 仅授权域内项目 | ❌ |
| 编辑架构 MD | ✅ 全部 | ✅ 全部 | ✅ 仅授权域内项目 | ❌ |
| 查看项目列表 | ✅ 全部 | ✅ 全部 | ✅ 仅授权域 | ❌ |
| 用户域授权 | ✅ | ❌ | ❌ | ❌ |
| 存储卷管理 | ✅ | ❌ | ❌ | ❌ |
| LLM 配额配置 | ✅ | ❌ | ❌ | ❌ |

#### 4.2.3 认证链路

```
浏览器
  │ POST /portal/api/projects (创建项目)
  ▼
Portal Server (rbac_middleware.go:42 getSessionUser)
  │ 1. auth.GetSessionFromRequest(r) → sess
  │ 2. db.GetUserDomains(sess.UserID) → domains
  │ 3. 组装 UserInfo{UserID, Role, AuthorizedDomains}
  │
  │ 4. RBAC 校验:
  │    if group_leader:
  │      req.business_domain 必须在 AuthorizedDomains 中
  │      每个 repo.business_domain 必须在 AuthorizedDomains 中
  │    elif !super_admin && !admin:
  │      403 Forbidden
  │
  │ 5. SignRequest(req, secret, user, role, domains):
  │    X-OpenWiki-User: {userID}
  │    X-OpenWiki-Role: {role}
  │    X-OpenWiki-Domains: {domain1,domain2}
  │    X-OpenWiki-Sig: HMAC-SHA256(secret, "user:role:domains")
  │
  ▼
Orchestrator Server (auth.go GetAuthContext)
  │ 1. 验证 HMAC 签名
  │ 2. 解析 headers → AuthContext{User, Role, AuthorizedDomains}
  │ 3. AuthContext.IsDomainAuthorized(domain):
  │    super_admin/admin → true
  │    group_leader → domain in AuthorizedDomains
  │
  │ 4. 业务逻辑中使用 AuthContext 做二次域校验
  ▼
DB / 文件系统操作
```

#### 4.2.4 G2 修复：仓库强制归属域

**现状**: `repos.business_domain` 可为空，导致域下拉为空。

**修复方案**:

| 入口 | 改动 |
|------|------|
| `batch-import` (`batch.go:69`) | 校验 `BusinessDomain` 非空，空则拒绝该行并返回错误列表 |
| 单仓注册 (`repos.go:36 createRepo`) | `CreateRepoRequest` 增加 `business_domain` 字段，校验非空 |
| 前端 `admin.js populateDomainSelect()` | 无需改动（逻辑已正确：管理员从仓库域去重，组长从 authorized_domains） |

**改动细节**:

```go
// batch.go handleBatchImport - 在 for 循环内增加:
if item.BusinessDomain == "" {
    importErrors = append(importErrors, fmt.Sprintf("仓库 %s 缺少 business_domain", item.ID))
    continue
}

// repos.go CreateRepoRequest 增加字段:
type CreateRepoRequest struct {
    ID             string `json:"id"`
    Name           string `json:"name"`
    GitURL         string `json:"git_url"`
    Branch         string `json:"branch"`
    BusinessDomain string `json:"business_domain"`  // 新增
    Tier           string `json:"tier"`              // 新增
    LocalPath      string `json:"local_path"`
    Schedule       string `json:"schedule"`
}
```

### 4.3 项目架构描述 MD（P2.3）

#### 4.3.1 设计

新增 `architecture_md` 属性，由域管理员维护，描述多仓系统中各底层仓库的作用与关系。

#### 4.3.2 数据模型变更

```sql
-- orchestrator.db migration
ALTER TABLE projects ADD COLUMN architecture_md TEXT DEFAULT '';
```

```go
// db/sqlite.go Project struct 增加字段:
type Project struct {
    ID             string `json:"id"`
    Name           string `json:"name"`
    Description    string `json:"description"`
    BusinessDomain string `json:"business_domain"`
    ArchitectureMD string `json:"architecture_md"`   // 新增
    LocalPath      string `json:"local_path"`
    Status         string `json:"status"`
    CreatedAt      string `json:"created_at"`
    UpdatedAt      string `json:"updated_at"`
}
```

**SQL 改动清单**:

| 函数 | 改动 |
|------|------|
| `migrate()` | 增加 `ALTER TABLE projects ADD COLUMN architecture_md TEXT DEFAULT ''` |
| `ListProjects()` | SELECT 增加 `COALESCE(architecture_md,'')` |
| `GetProject()` | SELECT 增加 `COALESCE(architecture_md,'')` |
| `CreateProject()` | INSERT 增加 `architecture_md` 列 |
| `UpdateProject()` | UPDATE SET 增加 `architecture_md = ?` |

#### 4.3.3 API 契约变更

```json
// CreateProjectRequest 增加字段:
{
  "id": "proj-consumption",
  "name": "消金微服务集群",
  "description": "...",
  "business_domain": "消金",
  "architecture_md": "# 消金系统架构\n\n## 服务职责\n...",
  "repos": [...]
}
```

#### 4.3.4 架构 MD 持久化到文件

在 `BuildProjectWorkspace()` 中，将 `architecture_md` 写入 `_project_hub/openwiki/architecture.md`：

```
源文件: api/projects.go:319 BuildProjectWorkspace

改动位置: 在 quickstart.md 生成之后增加:

archPath := filepath.Join(hubOpenwikiDir, "architecture.md")
if proj.ArchitectureMD != "" {
    os.WriteFile(archPath, []byte(proj.ArchitectureMD), 0644)
} else {
    // 生成默认模板, 引导域管理员填写
    tmpl := fmt.Sprintf("# %s 架构描述\n\n> 请域管理员维护此文档...\n", proj.Name)
    os.WriteFile(archPath, []byte(tmpl), 0644)
}
```

#### 4.3.5 前端 UI

在项目编辑弹窗中增加 MD 编辑器：

```html
<!-- admin.html 项目弹窗内增加 -->
<div class="form-group">
  <label for="projArchitectureMD">架构描述 (Markdown)</label>
  <textarea id="projArchitectureMD" rows="12"
    placeholder="# 系统架构\n\n## 各服务职责与关系\n..."></textarea>
  <small>由域管理员维护，描述各底层仓库的作用与调用关系</small>
</div>
```

```javascript
// admin.js 项目编辑逻辑:
// - 编辑项目时填充: projArchitectureMD.value = project.architecture_md || ''
// - 提交时携带: architecture_md: projArchitectureMD.value
// - 权限: group_leader 仅能编辑授权域内项目
```

### 4.4 多仓全量 openwiki 构建（P2.4）

#### 4.4.1 现状与差距

`BuildProjectWorkspace()`（`projects.go:319`）当前只做：
1. `PrepareWorkspace()` — 软链聚合
2. `linker.js` — 调用链接扫描脚本
3. 生成 `quickstart.md` — 静态模板

**缺失**: 没有调用 openwiki CLI 对多仓 workspace 做全量 wiki 生成、静态化、图谱化。

#### 4.4.2 设计方案

在 `BuildProjectWorkspace()` 的 quickstart.md 生成之后，增加 openwiki 全量构建步骤：

```
BuildProjectWorkspace(projectID) 完整流程:

1. proj.Status = "building"
2. PrepareWorkspace(proj, repos) → wsDir              [已实现]
3. linker.js wsDir                                    [已实现]
4. 生成 quickstart.md                                 [已实现]
5. 写入 architecture.md                               [新增 P2.3]
6. ★ 调用 Builder.BuildRepoWithBuildID:
     buildID := CreateBuild(projectID, "", "running")
     buildRes := builder.BuildRepoWithBuildID(buildID, projectID, wsDir,
                 filepath.Join(wsDir, "_project_hub", "openwiki"))
     [新增 P2.4]
7. 静态化输出:
     finalStaticDir = StaticOutputDir/projects/{projectID}
     [Builder 内部原子交换已处理]
8. proj.Status = "active" / "failed"
```

#### 4.4.3 多仓构建的关键问题

**问题 1: openwiki CLI 对多仓 workspace 的兼容性**

openwiki CLI 以 `repoPath` 为根目录运行。多仓 workspace 是一个包含多个软链子目录的目录：

```
wsDir/
├── svc-payment/     → 软链 → 物理仓库
├── svc-loan-core/   → 软链 → 物理仓库
├── _project_hub/
│   └── openwiki/    ← wiki 输出目录
└── .project-meta.json
```

openwiki agent 运行时会遍历 workspace 根目录，发现各子服务（软链），自动生成聚合 wiki。

**配置**: `Builder.BuildRepoWithBuildID(buildID, repoID, repoPath, wikiDir)`
- `repoID` = projectID（如 `proj-consumption`）
- `repoPath` = wsDir（多仓 workspace 目录）
- `wikiDir` = `filepath.Join(wsDir, "_project_hub", "openwiki")`

**问题 2: 构建并发与限流**

多仓项目构建复用单仓的 `BuildPipeline`，但当前 `BuildPipeline.Execute` 绑定了 Git 操作（clone/pull/diff）。多仓项目不需要 Git 操作（子仓库各自有独立构建）。

**方案**: 增加一个 `BuildProjectPipeline` 方法或在 `Execute` 中增加 `skipGit` 参数：

```go
// 方案: 在 BuildProjectWorkspace 中直接调用 Builder, 跳过 BuildPipeline 的 Git 步骤
func (s *Server) BuildProjectWorkspace(projectID string) error {
    // ... 现有步骤 1-5 ...

    // Step 6: openwiki 全量构建 (跳过 Git)
    buildID, _ := db.CreateBuild(projectID, "", "running")
    wikiDir := filepath.Join(wsDir, "_project_hub", "openwiki")
    buildRes := s.scheduler.GetBuilder().BuildRepoWithBuildID(
        buildID, projectID, wsDir, wikiDir,
        func(currentLog string) {
            db.UpdateBuildLog(buildID, currentLog, "")
        },
    )

    // Token 记账
    if s.llmProxy != nil {
        if usage, ok := s.llmProxy.PopBuildUsage(fmt.Sprintf("%d", buildID)); ok {
            db.FinishBuildWithTokens(buildID, status, log, "",
                int64(usage.PromptTokens), int64(usage.CompletionTokens),
                int64(usage.TotalTokens), 0, usage.RateLimitedRetries)
        }
    }

    // Step 7: 静态化已由 Builder 原子交换处理
    // StaticOutputDir/projects/{projectID}/ ← 自动生成
}
```

**问题 3: 多仓构建不应占用 BatchScheduler 槽位**

多仓项目构建是按需触发的（域管理员手动触发），不应占用批量导入的并发槽位。方案：直接在 goroutine 中调用 Builder，通过 LLMProxy 的 TPM/RPM 限流自然约束。

#### 4.4.4 多仓构建与单仓构建的关系

```
单仓构建 (BuildPipeline.Execute):
  Git checkout → openwiki update → graph → static
  → 输出: /data/openwiki/static/{repoID}/

多仓构建 (BuildProjectWorkspace):
  PrepareWorkspace → linker → quickstart.md → architecture.md
  → openwiki update → graph → static
  → 输出: /data/openwiki/static/projects/{projectID}/

关系:
  - 多仓 wiki 引用各子仓的 wiki (通过软链访问)
  - 多仓 graph.json 聚合各子仓的概念节点
  - 多仓静态站点是独立门户, 子仓静态站点独立保留
```

---

## 5. P3：Repo 知识纳管

### 5.1 设计目标

将 repo 代码库中 `doc/knowledge` 目录的 MD 文档纳入 OpenWiki 知识库，使其能够：
- 被 openwiki agent 发现并引用
- 随 wiki 一起静态化
- 出现在知识图谱中

### 5.2 方案 A：构建管线预处理（Go 侧）

#### 5.2.1 设计

在 `Builder.BuildRepoWithBuildID` 的 Step 0（init）之后、Step 1（update）之前，增加 "Knowledge Ingestion" 步骤。

#### 5.2.2 算法规约

```
源文件: scheduler/builder.go BuildRepoWithBuildID

新增 Step 0.5: Knowledge Ingestion

输入: repoPath (仓库本地路径)
输出: bool (是否纳管了知识文档)

算法:
1. knowledgeSrcDir = filepath.Join(repoPath, "doc", "knowledge")
2. stat(knowledgeSrcDir):
   - 不存在 → return false (兼容无知识目录的 repo)
   - 存在 → 继续
3. knowledgeDstDir = filepath.Join(repoPath, "openwiki", "knowledge")
4. MkdirAll(knowledgeDstDir)
5. 遍历 knowledgeSrcDir 下所有 *.md 文件:
   for each mdFile:
     dstFile = filepath.Join(knowledgeDstDir, relativePath)
     创建软链: os.Symlink(srcMdFile, dstFile)
     (软链而非复制, 保持非侵入式, 源文件由 repo 维护)
6. logStep("Knowledge ingested: N files from doc/knowledge → openwiki/knowledge/")
7. return true

错误处理:
- 软链创建失败 → 记录 warning, 继续下一个文件
- doc/knowledge 存在但为空 → return false
```

#### 5.2.3 构建管线改动位置

```go
// builder.go BuildRepoWithBuildID - 在 Step 0 之后, Step 1 之前:

// Step 0.5: Knowledge Ingestion (doc/knowledge → openwiki/knowledge)
b.logStep(repoID, "=== Step 0.5: Knowledge Ingestion ===", &logBuf, onProgress...)
if ingested := b.ingestKnowledge(repoID, repoPath, &logBuf, onProgress...); ingested {
    b.logStep(repoID, "Knowledge documents linked to openwiki/knowledge/", &logBuf, onProgress...)
} else {
    b.logStep(repoID, "No doc/knowledge directory found, skipping knowledge ingestion", &logBuf, onProgress...)
}

// Step 1: openwiki code --update --print (现有代码)
```

#### 5.2.4 新增方法

```go
// ingestKnowledge detects doc/knowledge directory and symlinks MD files
// into openwiki/knowledge/ for the openwiki agent to discover.
func (b *Builder) ingestKnowledge(repoID, repoPath string, logBuf *strings.Builder,
    onProgress ...func(string)) bool {

    knowledgeSrcDir := filepath.Join(repoPath, "doc", "knowledge")
    info, err := os.Stat(knowledgeSrcDir)
    if err != nil || !info.IsDir() {
        return false
    }

    knowledgeDstDir := filepath.Join(repoPath, "openwiki", "knowledge")
    if err := os.MkdirAll(knowledgeDstDir, 0755); err != nil {
        b.logStep(repoID, fmt.Sprintf("create knowledge dir failed: %v", err), logBuf, onProgress...)
        return false
    }

    count := 0
    filepath.Walk(knowledgeSrcDir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }
        if !strings.HasSuffix(info.Name(), ".md") {
            return nil
        }
        relPath, _ := filepath.Rel(knowledgeSrcDir, path)
        dstFile := filepath.Join(knowledgeDstDir, relPath)
        os.MkdirAll(filepath.Dir(dstFile), 0755)
        os.Remove(dstFile) // 幂等: 先删再建
        if err := os.Symlink(path, dstFile); err != nil {
            b.logStep(repoID, fmt.Sprintf("link knowledge %s failed: %v", relPath, err),
                logBuf, onProgress...)
            return nil
        }
        count++
        return nil
    })

    b.logStep(repoID, fmt.Sprintf("Ingested %d knowledge MD files", count), logBuf, onProgress...)
    return count > 0
}
```

#### 5.2.5 静态化与图谱化（已就绪）

知识文档软链到 `openwiki/knowledge/` 后：

1. **openwiki agent 发现**: `openwiki code --update` 运行时，agent 遍历 `openwiki/` 目录，发现 `knowledge/` 子目录的 MD 文档，自动引用为知识源
2. **静态化**: `Builder` 的 Step 2-5 对整个 `openwiki/` 目录生成 `graph.json` 和静态站点，`knowledge/` 下的文档自动包含
3. **图谱化**: `graph.ts`（`src/visualize/graph.ts`）解析 wiki 中所有 MD 文件为概念节点，`knowledge/` 下的文档作为独立节点出现在图谱中，通过 Markdown 链接与其他页面建立边

#### 5.2.6 无知识目录兼容

`doc/knowledge` 不存在时，`ingestKnowledge` 返回 `false`，日志记录 "No doc/knowledge directory found, skipping"，构建流程正常继续。零侵入、零副作用。

#### 5.2.7 .openwikiignore 交互

如果 repo 的 `.openwikiignore` 排除了 `doc/knowledge`，agent 不会读取源目录。但 P3 方案 A 的软链将文件放入 `openwiki/knowledge/`，而 `openwiki/` 目录不在 `.openwikiignore` 排除范围内（它是 wiki 输出目录）。因此 `.openwikiignore` 不影响 P3 方案 A 的知识纳管。

> **注意**: 如果 `openwiki/knowledge/` 需要被 agent 视为"只读引用"而非"待生成页面"，可在 prompt 中增加提示（可选增强，非必须）。

---

## 6. 数据模型

### 6.1 orchestrator.db 完整 Schema（含变更）

```sql
-- 已有表 (无变更)
CREATE TABLE repos (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    git_url         TEXT NOT NULL,
    branch          TEXT NOT NULL DEFAULT 'main',
    local_path      TEXT NOT NULL,
    wiki_dir        TEXT,
    static_dir      TEXT,
    schedule        TEXT NOT NULL DEFAULT '0 2 * * *',
    status          TEXT NOT NULL DEFAULT 'active',
    tier            TEXT DEFAULT 'B',
    business_domain TEXT DEFAULT '',          -- G2: 导入时强制非空
    last_release_at DATETIME,
    volume_id       TEXT DEFAULT '',
    size_bytes      INTEGER DEFAULT 0,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 已有表 (无变更)
CREATE TABLE builds (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id               TEXT NOT NULL REFERENCES repos(id),
    git_head              TEXT,
    status                TEXT NOT NULL DEFAULT 'pending',
    started_at            DATETIME,
    finished_at           DATETIME,
    log                   TEXT,
    error                 TEXT,
    rate_limited_retries  INTEGER DEFAULT 0,
    prompt_tokens         INTEGER DEFAULT 0,
    completion_tokens     INTEGER DEFAULT 0,
    total_tokens          INTEGER DEFAULT 0,
    saved_tokens          INTEGER DEFAULT 0
);

-- ★ 变更: 新增 architecture_md 字段
CREATE TABLE projects (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT,
    business_domain TEXT NOT NULL DEFAULT '',
    architecture_md TEXT DEFAULT '',           -- ★ 新增 P2.3
    local_path      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 已有表 (无变更)
CREATE TABLE project_repos (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repo_id     TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    alias_name  TEXT NOT NULL,
    role        TEXT DEFAULT 'service',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, repo_id),
    UNIQUE(project_id, alias_name)
);

-- 已有表 (无变更)
CREATE TABLE batch_runs (
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

-- 已有表 (无变更)
CREATE TABLE batch_tasks (
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

-- 已有表 (无变更)
CREATE TABLE daily_token_usage (
    date               TEXT NOT NULL,
    business_domain    TEXT NOT NULL DEFAULT '',
    prompt_tokens      INTEGER DEFAULT 0,
    completion_tokens  INTEGER DEFAULT 0,
    total_tokens      INTEGER DEFAULT 0,
    saved_tokens      INTEGER DEFAULT 0,
    build_count        INTEGER DEFAULT 0,
    skipped_count      INTEGER DEFAULT 0,
    throttled_count    INTEGER DEFAULT 0,
    total_duration_sec INTEGER DEFAULT 0,
    updated_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (date, business_domain)
);

-- 已有表 (无变更)
CREATE TABLE volumes (
    id          TEXT PRIMARY KEY,
    mount_point TEXT NOT NULL,
    total_bytes INTEGER NOT NULL DEFAULT 0,
    used_bytes  INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 已有表 (无变更)
CREATE TABLE system_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 6.2 portal.db 完整 Schema（无变更）

```sql
CREATE TABLE users (
    username     TEXT PRIMARY KEY,
    display_name TEXT,
    role         TEXT NOT NULL DEFAULT 'viewer',
    password_hash TEXT,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_domain_grants (
    username        TEXT NOT NULL,
    business_domain TEXT NOT NULL,
    granted_by      TEXT,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (username, business_domain)
);

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME
);

CREATE TABLE audit_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    operator    TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    details     TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## 7. API 契约

### 7.1 存储卷管理

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| GET | `/api/storage/volumes` | 列出所有卷 | super_admin |
| POST | `/api/storage/volumes` | 注册卷 | super_admin |

```json
// POST /api/storage/volumes
// Request:
{"id": "vol0", "mount_point": "/data/disk0"}
// Response:
{"id":"vol0","mount_point":"/data/disk0","total_bytes":107374182400,"used_bytes":0,"status":"active"}

// GET /api/storage/volumes
// Response:
{"volumes":[{"id":"vol0","mount_point":"/data/disk0",...}]}
```

### 7.2 批量导入

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| POST | `/api/repos/batch-import` | 批量导入仓库 | super_admin/admin |

```json
// Request:
{
  "batch_name": "全行生产发版基线-2026Q4",
  "concurrency": 2,
  "night_mode_only": false,
  "auto_start": true,
  "repos": [
    {"id":"svc-payment","name":"payment-service","git_url":"http://...","branch":"master","tier":"A","business_domain":"消金","last_release_at":"2026-10-15"},
    ...
  ]
}
// Response (201):
{"success":true,"batch_id":"batch_xxx","total_imported":633,"status":"pending"}

// ★ G2 修复: 缺 business_domain 的行在 Response 中报告:
{"success":true,"batch_id":"batch_xxx","total_imported":630,"skipped":3,"errors":["仓库 svc-xxx 缺少 business_domain"]}
```

### 7.3 项目管理（含新增字段）

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| GET | `/api/projects` | 列出项目（group_leader 仅授权域） | admin+/group_leader |
| POST | `/api/projects` | 创建项目 | admin+/group_leader |
| PUT | `/api/projects` | 更新项目 | admin+/group_leader |
| DELETE | `/api/projects` | 删除项目 | admin+ |
| POST | `/api/projects/build` | 触发项目构建 | admin+/group_leader |
| GET | `/api/projects/{id}/links/health` | 软链健康检查 | admin+/group_leader |
| POST | `/api/projects/{id}/links/repair` | 软链修复 | admin+/group_leader |

```json
// POST /api/projects (★ 增加 architecture_md)
// Request:
{
  "id": "proj-consumption",
  "name": "消金微服务集群",
  "description": "消费金融业务域微服务聚合",
  "business_domain": "消金",
  "architecture_md": "# 消金架构\n\n## 服务职责\n...",
  "repos": [
    {"repo_id":"svc-payment","alias_name":"payment","role":"core"},
    {"repo_id":"svc-loan-core","alias_name":"loan","role":"service"}
  ]
}
```

### 7.4 批量状态与控制

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/batch/status?batch_id=xxx` | 批量进度、槽位、ETA |
| PUT | `/api/batch/concurrency` | 动态调并发 |
| POST | `/api/batch/pause` | 暂停 |
| POST | `/api/batch/resume` | 恢复 |
| POST | `/api/batch/cancel` | 取消 |

### 7.5 LLM 配额

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| GET | `/api/settings/llm-quota` | 获取配额 | super_admin |
| PUT | `/api/settings/llm-quota` | 更新配额 | super_admin |

```json
// GET /api/settings/llm-quota
{
  "max_tpm": 0,
  "max_rpm": 0,
  "day_max_concurrency": 2,
  "night_max_concurrency": 4
}
```

---

## 8. 状态机与序列图

### 8.1 BatchRun 状态机

```
                ┌─────────┐
                │ pending │ ← 刚创建, 未启动
                └────┬────┘
                     │ StartBatch()
                     ▼
                ┌─────────┐
        ┌──────▶│ running │◀──────────────┐
        │       └────┬────┘               │
        │            │ PauseBatch()       │ ResumeBatch()
        │            ▼                    │
        │       ┌─────────┐               │
        │       │ paused  │───────────────┘
        │       └─────────┘
        │
        │ CancelBatch()
        │            │ 所有 task 完成
        │            ▼
        │       ┌──────────┐
        └──────▶│completed │
                └──────────┘
        CancelBatch()
        │            │
        ▼            ▼
   ┌─────────┐  ┌─────────┐
   │ failed  │  │ (final) │
   └─────────┘  └─────────┘
```

### 8.2 BatchTask 状态机

```
   ┌─────────┐     slot acquired     ┌───────────┐
   │ pending │──────────────────────▶│ executing │
   └─────────┘                       └─────┬─────┘
                                           │
                              ┌────────────┼────────────┐
                              │            │            │
                              ▼            ▼            ▼
                         ┌──────────┐ ┌──────────┐ ┌─────────┐
                         │completed │ │ skipped  │ │ failed  │
                         └──────────┘ └──────────┘ └─────────┘
                         (wiki 生成成功) (无 git 更新) (构建失败)
```

### 8.3 Project 状态机

```
   ┌─────────┐  BuildProjectWorkspace()  ┌──────────┐
   │ active  │─────────────────────────▶│ building │
   └─────────┘                           └────┬─────┘
        ▲                                     │
        │                                     │ 成功
        │─────────────────────────────────────┘
        │                                     │
        │ 失败                                ▼
        │                                ┌─────────┐
        └────────────────────────────────│ failed  │
                                         └─────────┘
```

### 8.4 批量导入序列图

```
浏览器    Portal    Orchestrator    VolumeManager    BatchScheduler    BuildPipeline    LLMProxy    LLM
  │          │           │               │                │                │             │          │
  │ POST     │           │               │                │                │             │          │
  │ batch-   │           │               │                │                │             │          │
  │ import   │           │               │                │                │             │          │
  │─────────▶│           │               │                │                │             │          │
  │          │ HMAC签名  │               │                │                │             │          │
  │          │──────────▶│               │                │                │             │          │
  │          │           │ 遍历 repos    │                │                │             │          │
  │          │           │──────────────▶│                │                │             │          │
  │          │           │               │ SelectVolume   │                │             │          │
  │          │           │               │ CreatePlacement│                │             │          │
  │          │           │◀──────────────│                │                │             │          │
  │          │           │ CreateRepo(DB)│                │                │             │          │
  │          │           │ aggregateDomain│               │                │             │          │
  │          │           │ CreateBatchRun│                │                │             │          │
  │          │           │ CreateTasks   │                │                │             │          │
  │          │           │──────────────▶│                │ StartBatch     │             │          │
  │          │           │               │                │───────────────▶│             │          │
  │          │ 201       │◀──────────────│                │                │             │          │
  │◀─────────│           │               │                │                │             │          │
  │          │           │               │                │                │             │          │
  │          │           │               │          (异步)│ for each task: │             │          │
  │          │           │               │                │  AcquireSlot   │             │          │
  │          │           │               │                │  Execute(repo) │             │          │
  │          │           │               │                │───────────────▶│             │          │
  │          │           │               │                │                │ Git checkout│          │
  │          │           │               │                │                │ openwiki    │          │
  │          │           │               │                │                │──update─────▶│          │
  │          │           │               │                │                │             │ TPM check│
  │          │           │               │                │                │             │─────────▶│
  │          │           │               │                │                │             │◀─────────│
  │          │           │               │                │                │◀────────────│          │
  │          │           │               │                │                │ graph+static│          │
  │          │           │               │                │                │ Token extract│         │
  │          │           │               │                │◀───────────────│             │          │
  │          │           │               │                │ UpdateTask(DB) │             │          │
```

### 8.5 多仓项目构建序列图

```
域管理员  Portal    Orchestrator              WorkspaceManager    Builder      OpenWikiCLI
   │        │           │                         │                 │             │
   │ POST   │           │                         │                 │             │
   │project │           │                         │                 │             │
   │build   │           │                         │                 │             │
   │───────▶│           │                         │                 │             │
   │        │ HMAC      │                         │                 │             │
   │        │──────────▶│                         │                 │             │
   │        │           │ RBAC校验(域权限)         │                 │             │
   │        │           │ BuildProjectWorkspace()  │                 │             │
   │        │           │────────────────────────▶│                 │             │
   │        │           │                         │ PrepareWorkspace│             │
   │        │           │                         │ (软链聚合)      │             │
   │        │           │◀────────────────────────│                 │             │
   │        │           │ linker.js                │                 │             │
   │        │           │ quickstart.md            │                 │             │
   │        │           │ architecture.md (★新增) │                 │             │
   │        │           │─────────────────────────────────────────▶│             │
   │        │           │                         │                 │ BuildRepo   │
   │        │           │                         │                 │ WithBuildID │
   │        │           │                         │                 │────────────▶│
   │        │           │                         │                 │             │ openwiki
   │        │           │                         │                 │             │ code --update
   │        │           │                         │                 │             │ (遍历软链子仓)
   │        │           │                         │                 │             │◀──────────
   │        │           │                         │                 │ graph.json  │
   │        │           │                         │                 │ vendor      │
   │        │           │                         │                 │ 原子交换     │
   │        │           │◀─────────────────────────────────────────│             │
   │        │           │ proj.Status="active"    │                 │             │
   │        │ 202       │                         │                 │             │
   │◀───────│           │                         │                 │             │
```

---

## 9. 模块接口规约

### 9.1 VolumeManager

```go
// 源文件: storage/volume.go
// 无需改动, 已就绪

type VolumeManager struct { ... }

func NewVolumeManager(db *DB, linksBaseDir string) *VolumeManager
func (vm *VolumeManager) RegisterVolume(id, mountPoint string) (*Volume, error)
func (vm *VolumeManager) SelectVolumeForRepo(repoID string, estSizeBytes int64) (*Volume, error)
func (vm *VolumeManager) CreateRepoPlacement(repoID, volumeID string) (physicalPath, symlinkPath string, err error)
func (vm *VolumeManager) UpdateRepoSize(repoID, physicalPath string) (int64, error)
func (vm *VolumeManager) SetStatfsFunc(fn StatfsFunc)  // 测试环境注入模拟容量
func (vm *VolumeManager) SetTaskBusy(volumeID string, delta int)
```

### 9.2 BatchScheduler

```go
// 源文件: scheduler/batch_scheduler.go
// 无需改动, 已就绪

type BatchScheduler struct { ... }

func NewBatchScheduler(db, pipeline, limiter) *BatchScheduler
func (bs *BatchScheduler) StartBatch(ctx, batchID) error
func (bs *BatchScheduler) PauseBatch(batchID) error
func (bs *BatchScheduler) ResumeBatch(batchID) error
func (bs *BatchScheduler) CancelBatch(batchID) error
func (bs *BatchScheduler) SetConcurrency(batchID, n) error
func (bs *BatchScheduler) ApplyDayNightConcurrency(now) int
func (bs *BatchScheduler) GetDetailedStatus(batchID) (*BatchStatusResponse, error)
func (bs *BatchScheduler) RecoverInterruptedTasks() (int64, error)
```

### 9.3 BuildPipeline

```go
// 源文件: scheduler/pipeline.go
// 无需改动, 已就绪

type BuildPipeline struct { ... }

func NewBuildPipeline(db, builder, rateLimiter) *BuildPipeline
func (p *BuildPipeline) Execute(ctx, repo, force, slot, onProgress) (*PipelineResult, error)
func (p *BuildPipeline) SetLLMProxy(proxy *LLMProxy)
func (p *BuildPipeline) SetLogBaseDir(dir string)
```

### 9.4 Builder（★ 新增 ingestKnowledge）

```go
// 源文件: scheduler/builder.go
// 新增方法: ingestKnowledge

type Builder struct { ... }

func NewBuilder(cli, vendorDir, distDir, outputDir, defaultLang) *Builder
func (b *Builder) BuildRepo(repoID, repoPath, wikiDir, onProgress) *BuildResult
func (b *Builder) BuildRepoWithBuildID(buildID, repoID, repoPath, wikiDir, onProgress) *BuildResult
func (b *Builder) SetLLMBaseURL(url string)

// ★ 新增 P3
func (b *Builder) ingestKnowledge(repoID, repoPath string, logBuf *strings.Builder,
    onProgress ...func(string)) bool
// 行为: 检测 doc/knowledge, 软链 MD 文件到 openwiki/knowledge/
// 返回: true=已纳管, false=无知识目录(兼容)
```

### 9.5 WorkspaceManager

```go
// 源文件: project/*.go
// 无需改动, 已就绪

type WorkspaceManager struct { ... }

func NewWorkspaceManager(baseProjectsDir string) *WorkspaceManager
func (m *WorkspaceManager) PrepareWorkspace(project *Project, repos []RepoWithAlias) (string, error)
func (m *WorkspaceManager) CheckWorkspaceHealth(project, repos) (*WorkspaceLinkPreview, error)
func (m *WorkspaceManager) RepairWorkspaceLinks(project, repos) (*WorkspaceLinkPreview, error)
func (m *WorkspaceManager) PreviewWorkspaceLinks(project, repos) (*WorkspaceLinkPreview, error)
```

### 9.6 Project API（★ 新增 architecture_md）

```go
// 源文件: api/projects.go
// 改动: CreateProjectRequest 增加 ArchitectureMD, BuildProjectWorkspace 增加全量构建

type CreateProjectRequest struct {
    ID             string             `json:"id"`
    Name           string             `json:"name"`
    Description    string             `json:"description"`
    BusinessDomain string             `json:"business_domain"`
    ArchitectureMD string             `json:"architecture_md"`  // ★ 新增 P2.3
    LocalPath      string             `json:"local_path,omitempty"`
    Repos          []ProjectRepoInput `json:"repos,omitempty"`
}

func (s *Server) BuildProjectWorkspace(projectID string) error
// 改动: 增加 architecture.md 写入 + openwiki 全量构建调用
```

### 9.7 LLMProxy

```go
// 源文件: llmproxy/proxy.go
// 无需改动, 已就绪

type LLMProxy struct { ... }

func NewLLMProxy(upstreamURL, realAPIKey, limiter, throttleCallback) *LLMProxy
func (p *LLMProxy) ServeHTTP(w, r)
func (p *LLMProxy) PopBuildUsage(buildID) (BuildUsage, bool)
func (p *LLMProxy) AddTokens(buildID, prompt, completion, total)
func (p *LLMProxy) Get429Count() int
```

---

## 10. 部署拓扑

### 10.1 测试环境（ECS, 目录模拟多盘）

```yaml
# /opt/openwiki/config.json (Orchestrator)
{
  "listen_addr": ":3009",
  "openwiki_cli": "/opt/openwiki/node22/bin/openwiki",
  "openwiki_dist_dir": "/opt/openwiki/dist",
  "static_output_dir": "/data/openwiki/static",
  "vendor_assets_dir": "/opt/openwiki/assets/vendor",
  "repos_base_dir": "/data/disk0/links/repos",     # 改: 指向逻辑访问层
  "projects_base_dir": "/data/openwiki/projects",
  "db_path": "/data/openwiki/db/orchestrator.db",
  "default_language": "zh-CN",
  "openai_upstream_url": "http://127.0.0.1:8889/v1",
  "openwiki_api_key": "sk-xxx",
  "openwiki_provider": "openai-compatible",
  "openwiki_model": "glm-5.2"
}

# VolumeManager linksBaseDir 自动推导:
# filepath.Join(filepath.Dir(ReposBaseDir), "disk0", "links", "repos")
# = filepath.Join("/data/disk0", "disk0", "links", "repos")
# 需确认推导路径正确, 或在 NewServer 中显式设置
```

```yaml
# /opt/openwiki/config.yaml (Portal)
port: 8099
db_path: "/data/openwiki/db/portal.db"
orchestrator_url: "http://127.0.0.1:3009"
static_output_dir: "/data/openwiki/static"
session_ttl_hours: 24
auth:
  admin_users: ["admin", "bigc", "lisi"]
```

### 10.2 生产环境（内网私有化部署）

```
物理机 / 虚拟机:
  - 多块云盘/本地磁盘挂载: /data/disk0 ~ /data/diskN
  - 每块盘独立文件系统, statfs 返回真实容量
  - VolumeManager 使用 DefaultStatfs (不注入模拟)
  - repos_base_dir 指向 /data/disk0/links/repos
  - 各盘 repos/ 目录由 VolumeManager.CreateRepoPlacement 自动创建

部署组件:
  - openwiki-orchestrator.service (Go binary, :3009)
  - openwiki-portal.service (Go binary, :8099)
  - openwiki CLI (Node.js, /opt/openwiki/node22/bin/openwiki)
  - LLM 上游 (内网部署的 glm-5.2 或兼容 API)
```

### 10.3 配置变更清单

| 配置项 | 测试环境 | 生产环境 | 说明 |
|--------|---------|---------|------|
| `repos_base_dir` | `/data/disk0/links/repos` | `/data/disk0/links/repos` | 逻辑访问层 |
| VolumeManager `linksBaseDir` | `/data/disk0/links/repos` | `/data/disk0/links/repos` | 软链根目录 |
| `SetStatfsFunc` | 注入模拟 100GB | 不注入（真实 statfs） | 容量探测 |
| 存储卷注册 | vol0-vol3 → 目录 | vol0-volN → 真实挂载点 | `POST /api/storage/volumes` |
| LLM 上游 | `http://127.0.0.1:8889/v1` | 内网 LLM 地址 | glm-5.2 |
| `max_tpm` / `max_rpm` | 0 (不限) | 按 LLM 配额设置 | `PUT /api/settings/llm-quota` |
| 日夜并发 | day=2 / night=4 | day=2 / night=4 | `system_settings` 表 |

---

## 附录 A: 改动影响矩阵

| 文件 | P1 | P2.2 | P2.3 | P2.4 | P3 | 变更类型 |
|------|:--:|:----:|:----:|:----:|:--:|---------|
| `orchestrator/db/sqlite.go` | | | ★ | | | ALTER TABLE + struct + SQL |
| `orchestrator/api/projects.go` | | | ★ | ★ | | Request struct + BuildProjectWorkspace |
| `orchestrator/api/repos.go` | | ★ | | | | CreateRepoRequest 加 business_domain |
| `orchestrator/api/batch.go` | | ★ | | | | 校验 business_domain 非空 |
| `orchestrator/scheduler/builder.go` | | | | | ★ | 新增 ingestKnowledge |
| `orchestrator/config/*.go` | | | | | | (确认 linksBaseDir 推导) |
| `portal/public/js/admin.js` | | | ★ | | | 项目弹窗 architecture_md 编辑 |
| `portal/public/admin.html` | | | ★ | | | textarea UI |
| `portal/public/js/api.js` | | | ★ | | | API 调用增加 architecture_md |
| `config.json` (ECS) | ★ | | | | | repos_base_dir 改为 links 目录 |

## 附录 B: 不需要改动的已就绪模块

| 模块 | 说明 |
|------|------|
| `storage/volume.go` | 选盘算法、放置、容量监控已就绪 |
| `storage/migrator.go` | 卷间迁移已就绪 |
| `scheduler/batch_scheduler.go` | 日夜窗口、并发控制、ETA 已就绪 |
| `scheduler/rate_limiter.go` | 槽位池、热扩缩容已就绪 |
| `scheduler/pipeline.go` | 构建管线全流程已就绪 |
| `llmproxy/proxy.go` | TPM/RPM 限流、429 重试已就绪 |
| `llmproxy/limiter.go` | 滑动窗口限流已就绪 |
| `project/*.go` | 软链聚合、健康检查已就绪 |
| `portal/rbac_middleware.go` | RBAC 域授权链路已就绪 |
| `portal/db/sqlite.go` | GrantDomain/RevokeDomain 已就绪 |
