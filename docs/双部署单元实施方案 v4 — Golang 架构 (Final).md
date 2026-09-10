# 双部署单元实施方案 v4 — Golang 架构 (Final)

---

## 一、设计原则

1. **零改动 OpenWiki 源码**：OpenWiki 仅作为外部 CLI 工具被调用。
2. **技术栈 Golang**：两个独立 Go 模块，分别对应两个部署单元。
3. **SQLite 统一存储**：仓库配置、构建状态、用户会话、问答历史全部存储在 SQLite 中。
4. **LDAP 认证**：Go 后端直接通过 LDAP/LDAPS 协议对接 AD 域。
5. **同机部署**：单元 1 构建的静态产物直接写入单元 2 的本地目录。

---

## 二、技术架构图

```
                  ┌──────────────────────────────────────────────────────────────┐
                  │              部署单元 2: openwiki-portal (Go + Nginx)         │
                  ├──────────────────────────────────────────────────────────────┤
                  │                                                              │
                  │  Nginx (端口 80/443):                                        │
                  │    /wiki/:id/*       → 静态文件 /var/www/openwiki-static/:id/ │
                  │    /portal/*         → proxy_pass http://127.0.0.1:8080      │
                  │    /api/*            → proxy_pass http://127.0.0.1:3000      │
                  │                                                              │
                  │  Go Portal Server (端口 8080):                               │
                  │    POST /portal/login         → LDAP 认证 → 签发 Session     │
                  │    GET  /portal/repos          → 返回仓库列表 + wiki_url      │
                  │         响应示例: { id: "order-svc", wiki_url: "/wiki/order-svc/" } │
                  │    GET  /portal/sessions       → 查询用户问答历史 (读 SQLite)   │
                  │    GET  /portal/               → 返回 Web Portal 前端页面      │
                  │                                                              │
                  │  前端流程 (public/):                                           │
                  │    1. 调 /portal/repos 获取仓库列表 (含 wiki_url 字段)          │
                  │    2. 用户点击仓库 → 用返回的 wiki_url 直接访问 Nginx 静态路由  │
                  │    3. 选择仓库问答 → SSE 调用 /api/chat → Nginx 代理至单元 1   │
                  │    4. 用户 Session 管理 → 显示问答历史                         │
                  │                                                              │
                  └──────────────────────────────────────────────────────────────┘
                                                │
                                                ▼
                  ┌──────────────────────────────────────────────────────────────┐
                  │              部署单元 1: openwiki-orchestrator (Go)           │
                  ├──────────────────────────────────────────────────────────────┤
                  │                                                              │
                  │  Go Orchestrator Server (端口 3000):                         │
                  │                                                              │
                  │  ┌─ HTTP API ───────────────────────────────────────────┐     │
                  │  │ GET  /api/repos          → 仓库列表 (读 SQLite)      │     │
                  │  │ POST /api/repos          → 注册新仓库               │     │
                  │  │ POST /api/chat           → 问答调度 (spawn openwiki) │     │
                  │  │ GET  /api/build/status   → 构建状态查询              │     │
                  │  │ POST /api/build/trigger  → 手动触发构建              │     │
                  │  └──────────────────────────────────────────────────────┘     │
                  │                                                              │
                  │  ┌─ Cron 调度器 ────────────────────────────────────────┐     │
                  │  │ 每晚指定时间 → 遍历 SQLite 仓库表                    │     │
                  │  │   → git fetch + diff 检测                           │     │
                  │  │   → 有更新则 git pull                               │     │
                  │  │   → exec: openwiki code --update --print            │     │
                  │  │   → 调用 buildGraph() 导出 graph.json               │     │
                  │  │   → 复制内置可视化前端资源                           │     │
                  │  │   → 直接写入本地 /var/www/openwiki-static/:id/       │     │
                  │  │   → 更新 SQLite 构建状态                            │     │
                  │  └──────────────────────────────────────────────────────┘     │
                  │                                                              │
                  │  ┌─ QA Runner (问答进程管理) ───────────────────────────┐     │
                  │  │ spawn: openwiki code chat --print "用户问题"         │     │
                  │  │   cwd = 仓库本地路径                                │     │
                  │  │ 捕获 stdout → SSE 流式推回                          │     │
                  │  │ 并发池 (maxWorkers) + 超时 kill                     │     │
                  │  │ 写问答记录至 SQLite                                 │     │
                  │  └──────────────────────────────────────────────────────┘     │
                  │                                                              │
                  │  SQLite 数据库 (openwiki-orchestrator.db):                   │
                  │    repos       → 仓库配置与状态                              │
                  │    builds      → 构建历史与日志                              │
                  │    qa_sessions → 问答会话记录                                │
                  │                                                              │
                  └──────────────────────────────────────────────────────────────┘
```

---

## 三、SQLite 数据模型

### 3.1 单元 1 数据库 (`orchestrator.db`)

```sql
-- 仓库配置表
CREATE TABLE repos (
    id          TEXT PRIMARY KEY,        -- 仓库唯一标识 (如 "order-service")
    name        TEXT NOT NULL,           -- 显示名称
    git_url     TEXT NOT NULL,           -- Git 仓库地址
    branch      TEXT DEFAULT 'master',   -- 跟踪分支
    local_path  TEXT NOT NULL,           -- 本地克隆路径
    wiki_dir    TEXT,                    -- openwiki 输出目录 (默认 {local_path}/openwiki)
    static_dir  TEXT,                    -- 静态产物输出路径
    schedule    TEXT DEFAULT '0 2 * * *', -- Cron 表达式
    status      TEXT DEFAULT 'active',   -- active / paused / error
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 构建记录表
CREATE TABLE builds (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     TEXT NOT NULL REFERENCES repos(id),
    git_head    TEXT,                    -- 构建时的 commit hash
    status      TEXT NOT NULL,           -- pending / running / success / failed / skipped
    started_at  DATETIME,
    finished_at DATETIME,
    log         TEXT,                    -- 构建日志
    error       TEXT                     -- 错误信息
);

-- 问答会话记录表
CREATE TABLE qa_sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     TEXT NOT NULL REFERENCES repos(id),
    user_id     TEXT NOT NULL,           -- AD 域用户标识
    question    TEXT NOT NULL,
    answer      TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 3.2 单元 2 数据库 (`portal.db`)

```sql
-- 用户会话表
CREATE TABLE sessions (
    token       TEXT PRIMARY KEY,        -- Session Token
    user_id     TEXT NOT NULL,           -- AD 域 sAMAccountName
    display_name TEXT,                   -- AD 域 displayName
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at  DATETIME NOT NULL
);
```

---

## 四、关键技术实现

### 4.1 LDAP 认证 (Go 后端)

```go
// 使用 go-ldap/ldap/v3 库
// POST /portal/login { username, password }
//   → ldap.DialURL("ldaps://ad.company.com:636")
//   → conn.Bind(fmt.Sprintf("%s@company.com", username), password)
//   → 查询用户信息 (displayName, memberOf)
//   → 生成 Session Token 写入 portal.db
//   → 返回 Set-Cookie
```

### 4.2 内网离线资源打包 (Vendor Bundling)

> [!IMPORTANT]
> **内网部署关键**：OpenWiki 可视化前端引用了 4 个 `/vendor/*` 路径的 JS 库。源码中的 [`vendor.ts`](file:///Users/bigc/openwiki/src/visualize/vendor.ts) 仅包含**最小化 fallback stub**（功能极其精简），在内网环境下图谱渲染和 Markdown 解析会严重退化。必须预先下载完整版本并打包到本地。

**需要预下载的 4 个完整版 vendor 库**：

| 库名 | 用途 | 下载来源 | 打包路径 |
| :--- | :--- | :--- | :--- |
| `force-graph` | 力导向图谱渲染 (Canvas 2D) | `npm:force-graph` | `vendor/force-graph.min.js` |
| `marked` | Markdown → HTML 解析 | `npm:marked` | `vendor/marked.min.js` |
| `DOMPurify` | HTML 安全净化 | `npm:dompurify` | `vendor/dompurify.min.js` |
| `mermaid` | 流程图/架构图渲染 | `npm:mermaid` | `vendor/mermaid.min.js` |

**打包策略**：
1. 部署单元 1 项目中维护一个 `assets/vendor/` 目录，存放预下载的完整版 JS 文件。
2. 首次部署时，在有外网的环境中执行一次性下载脚本（`scripts/download-vendor.sh`），将 4 个库的 UMD/IIFE 产物下载到 `assets/vendor/`。
3. 每次构建仓库静态产物时，将 `assets/vendor/` 整体复制到各仓库的静态输出目录中。
4. 后续内网环境中，这些文件随部署包一起分发，**无需任何外网访问**。

```bash
# scripts/download-vendor.sh (一次性执行，下载完整版 vendor 库)
mkdir -p assets/vendor
curl -o assets/vendor/force-graph.min.js  "https://unpkg.com/force-graph/dist/force-graph.min.js"
curl -o assets/vendor/marked.min.js       "https://unpkg.com/marked/marked.min.js"
curl -o assets/vendor/dompurify.min.js    "https://unpkg.com/dompurify/dist/purify.min.js"
curl -o assets/vendor/mermaid.min.js      "https://unpkg.com/mermaid/dist/mermaid.min.js"
```

---

### 4.3 静态可视化导出 (Go 调用)

单元 1 在构建完成后，导出 OpenWiki 内置可视化的静态产物：

```go
// 1. 导出图谱 JSON（调用 OpenWiki 的 buildGraph 纯函数）
cmd := exec.Command("node", "-e", `
  import("./dist/visualize/graph.js")
    .then(m => m.buildGraph("` + wikiDir + `"))
    .then(g => process.stdout.write(JSON.stringify(g)))
`)
cmd.Dir = openwikiInstallPath
graphJSON, _ := cmd.Output()

// 2. 写入静态目录
os.MkdirAll(filepath.Join(staticDir, "api"), 0755)
os.WriteFile(filepath.Join(staticDir, "api", "graph"), graphJSON, 0644)

// 3. 复制内置前端资源
copyFile(filepath.Join(openwikiDist, "visualize", "client.js"), filepath.Join(staticDir, "client.js"))
copyFile(filepath.Join(openwikiDist, "visualize", "client-lib.js"), filepath.Join(staticDir, "client-lib.js"))
// index.html 从 PAGE 常量预生成

// 4. 复制预下载的完整版 vendor 库（内网离线可用）
copyDir("assets/vendor/", filepath.Join(staticDir, "vendor/"))
```

### 4.3 问答调度 (Go 进程管理)

```go
// POST /api/chat { repo_id, user_id, message }

// 并发池控制
sem := make(chan struct{}, maxConcurrent) // 如 maxConcurrent = 5
sem <- struct{}{}
defer func() { <-sem }()

cmd := exec.CommandContext(ctx, "openwiki", "code", "chat", "--print", message)
cmd.Dir = repo.LocalPath
cmd.Env = append(os.Environ(), "OPENWIKI_PROVIDER=...", "OPENWIKI_MODEL=...")

stdout, _ := cmd.StdoutPipe()
cmd.Start()

// SSE 流式输出
w.Header().Set("Content-Type", "text/event-stream")
scanner := bufio.NewScanner(stdout)
for scanner.Scan() {
    fmt.Fprintf(w, "data: %s\n\n", scanner.Text())
    w.(http.Flusher).Flush()
}
cmd.Wait()

// 记录问答至 SQLite
db.Exec("INSERT INTO qa_sessions ...")
```

---

## 五、项目目录结构

```
deploy/
├── unit1-orchestrator/              ← Go Module 1
│   ├── go.mod
│   ├── go.sum
│   ├── main.go                      ← 入口：启动 HTTP Server + Cron
│   ├── internal/
│   │   ├── config/
│   │   │   └── config.go            ← 配置加载 (YAML/ENV)
│   │   ├── db/
│   │   │   ├── sqlite.go            ← SQLite 连接与迁移
│   │   │   └── models.go            ← 数据模型 (repos, builds, qa_sessions)
│   │   ├── scheduler/
│   │   │   ├── cron.go              ← Cron 调度器
│   │   │   ├── git.go               ← Git fetch/diff/pull 操作
│   │   │   └── builder.go           ← openwiki --update + 静态导出
│   │   ├── qa/
│   │   │   ├── runner.go            ← 问答进程 spawn + stdout 捕获
│   │   │   └── pool.go              ← 并发池管理
│   │   └── api/
│   │       ├── router.go            ← HTTP 路由
│   │       ├── repos.go             ← /api/repos 处理
│   │       ├── chat.go              ← /api/chat 处理 (SSE)
│   │       └── build.go             ← /api/build/* 处理
│   └── Dockerfile
│
├── unit2-portal/                    ← Go Module 2
│   ├── go.mod
│   ├── go.sum
│   ├── main.go                      ← 入口：启动 Portal HTTP Server
│   ├── internal/
│   │   ├── auth/
│   │   │   ├── ldap.go              ← LDAP Bind + 用户查询
│   │   │   └── session.go           ← Session Token 管理 (SQLite)
│   │   ├── db/
│   │   │   └── sqlite.go            ← Portal SQLite
│   │   └── api/
│   │       ├── router.go            ← HTTP 路由
│   │       ├── login.go             ← POST /portal/login
│   │       ├── repos.go             ← GET /portal/repos (代理至单元 1)
│   │       └── sessions.go          ← GET /portal/sessions
│   ├── public/                      ← 静态前端资源
│   │   ├── index.html               ← Web Portal 主页面
│   │   ├── app.js                   ← 前端交互逻辑
│   │   └── style.css                ← 样式
│   ├── nginx.conf                   ← Nginx 配置模板
│   └── Dockerfile
│
└── docker-compose.yml               ← (可选) 一键启动双单元
```

---

## 六、Go 依赖选择

| 功能 | 推荐库 | 说明 |
| :--- | :--- | :--- |
| HTTP Server | `net/http` (标准库) 或 `gin` / `chi` | 轻量路由 |
| Cron 调度 | `github.com/robfig/cron/v3` | Go 标准 Cron 库 |
| SQLite | `github.com/mattn/go-sqlite3` 或 `modernc.org/sqlite` | 后者纯 Go 无 CGO |
| LDAP 认证 | `github.com/go-ldap/ldap/v3` | AD/LDAP 标准库 |
| Git 操作 | `os/exec` 调用 `git` CLI | 最简可靠 |
| SSE 输出 | `net/http` + `Flusher` | 标准库即可 |

---

## 七、Verification Plan

### Automated Tests
```bash
# 单元 1
cd deploy/unit1-orchestrator && go test ./...

# 单元 2
cd deploy/unit2-portal && go test ./...
```

### Manual Verification
1. 注册测试仓库 → 等待 Cron 触发 → 验证静态 Wiki 在 Nginx 可访问。
2. AD 域账号登录 Portal → 验证 Session 生成与 Cookie 返回。
3. 选择仓库问答 → 验证 SSE 流式输出 → 验证问答历史写入 SQLite。
4. 不同用户登录 → 验证会话隔离。
5. 手动触发构建 → 验证增量更新。
