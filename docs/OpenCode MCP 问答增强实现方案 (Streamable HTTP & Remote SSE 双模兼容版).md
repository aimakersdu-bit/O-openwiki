# OpenCode MCP 问答增强实现方案 (Streamable HTTP & Remote SSE 双模兼容版)

> **实施说明：** 本方案严格遵守“不修改 OpenWiki 源码，仅在 `deploy/` 运维与编排层增强”的原则。全面遵循 **MCP 2025-03-26 最新规范 (Streamable HTTP)** 并向下兼容 **旧版 Remote SSE**，通过 Tool 显式强制传入 `user_id` 实现严格审计，100% 兼容现有管理界面的审计与会话查询。

**目标 (Goal):** 
在 `deploy/unit1-orchestrator` 内内置高可用 MCP 协议服务端（原生支持 Streamable HTTP `/mcp` 与旧版 Remote SSE `/mcp/sse` + `/mcp/messages`），供 OpenCode 远程直连。`ask_repository` 工具**强制要求传入 `user_id`、`repo_name`、`question`**，底层直接无侵入复用现有 LangGraph QA 引擎，流式推送 Markdown 答案；所有问答调用原生写入现有 `qa_sessions` 审计表，现有管理界面及管理员账号可直接查看所有 MCP 问答流水。

**架构原则 (Principles):**
1. **现代传输协议 (Streamable HTTP 优先，双模兼容)**：
   - 主路径采用 **MCP 2025 Streamable HTTP** 单端点 (`/mcp`)，通过 HTTP POST/GET 与 `Mcp-Session-Id` header 通信，性能更好、链路更简洁；
   - 同时保留 **旧版 Remote SSE** (`GET /mcp/sse` + `POST /mcp/messages?sessionId=...`) 的兼容路由，确保新旧 OpenCode 客户端开箱即用。
2. **MCP Tool 级强制审计 (`user_id` 必填)**：
   - OpenCode 在执行 skill 时动态获取当前用户 ID，调用 `ask_repository` 时强制传入 `user_id`，若缺失则立即拦截报错；
   - 服务端配置固定 URL，无需在 `opencode.json` 中写死用户信息或端口。
3. **QA 核心代码零修改复用**：
   - 现有 `qa.Manager.StreamChat` 功能完善，MCP 层通过实现适配器 `mcpStreamWriter`（实现 `io.Writer`）无缝桥接，无需为 `qa.Manager` 增加新方法或改动已有业务逻辑。
4. **管理界面 100% 兼容**：
   - 原生写入现有 `qa_sessions` 数据表，不增加新表、不改动任何旧接口，管理员在原有 Web 管理界面中无缝查看 MCP 调用的问答记录与归属用户。
5. **健壮的并发与安全防护**：
   - 内置 Origin Header 校验（防 DNS Rebinding）、Session 数量上限与 TTL 闲置自动回收机制，保证系统稳定。

---

## 一、用户审计与交互时序设计

### 1. 为什么在 MCP Tool 参数中强制传递 `user_id`？
- **避免写死配置**：团队共享或多环境部署时，`opencode.json` 的 MCP URL 仅需配置为固定的 `http://<ip>/mcp`，不需要为每个工程师配置不同的 URL。
- **与 OpenCode Skill 完美契合**：OpenCode 中的 skill 可以动态获取当前环境的工号/开发者账号（如环境变量、Git 配置或登录态），在发起 MCP 调用时显式传入 `user_id`。
- **强制审计约束**：若 OpenCode 未传 `user_id`，MCP 工具直接返回错误：`"user_id is required for audit. Please provide your username or employee ID"`，杜绝无主调用。

### 2. 审计时序与管理界面交互流

```
+----------------------------------------------------------------------------------------------------+
|                                    OpenCode 智能体 (opencode.json)                                 |
|                                    url: http://openwiki-host/mcp                                   |
+----------------------------------------------------------------------------------------------------+
                                                  |
                                                  | 1. POST /mcp (Streamable HTTP)
                                                  |    Header: Mcp-Session-Id: <uuid>
                                                  |    Body: tools/call {
                                                  |      repo_name: "openwiki",
                                                  |      user_id:   "zhangsan",  <--- Skill 动态传入
                                                  |      question:  "架构是什么?"
                                                  |    }
                                                  v
+----------------------------------------------------------------------------------------------------+
|                                      Nginx 反向代理 (:80)                                           |
|                                      location /mcp { proxy_pass http://127.0.0.1:3000/mcp; ... }   |
+----------------------------------------------------------------------------------------------------+
                                                  |
                                                  v
+----------------------------------------------------------------------------------------------------+
|                      Unit 1 Orchestrator (Go :3000) - internal/mcp                                 |
|                                                                                                    |
|  [安全检查与参数校验]                                                                              |
|  - 校验 Origin Header (防 DNS Rebinding)                                                           |
|  - 校验 user_id 必填，缺失直接返回 Tool 错误拒绝执行                                               |
|  - 根据 repo_name 精确匹配/降级查找真实 RepoID                                                     |
|                                                                                                    |
|  [零侵入调度执行]                                                                                  |
|  - 构造 mcpStreamWriter 适配器作为 io.Writer 传入                                                  |
|  - 直接调用现有 qaMgr.StreamChat(ctx, repo, "zhangsan", sessionID, question, streamWriter)         |
|  - StreamChat 内部自动逐行提取 tokens 并落库: db.RecordQASession(sessionID, repoID, "zhangsan", ...) |
|  - streamWriter 将 delta token 即时封装并通过 MCP SSE 流返回给 OpenCode 智能体                     |
+----------------------------------------------------------------------------------------------------+
                                                  |
                                                  v
+----------------------------------------------------------------------------------------------------+
|                                     orchestrator.db (SQLite)                                       |
|  表: qa_sessions                                                                                   |
|  - id:          1024                                                                               |
|  - session_id:  "mcp_sess_1728350000"                                                              |
|  - repo_id:     "openwiki"                                                                         |
|  - user_id:     "zhangsan"  <--- 真实工程师工号/用户名                                             |
|  - question:    "架构是什么?"                                                                      |
|  - answer:      "OpenWiki 架构分为..."                                                             |
|  - created_at:  "2026-10-08 10:45:00"                                                              |
+----------------------------------------------------------------------------------------------------+
                                                  ^
                                                  |
                                                  | 2. 查询审计流水 (GET /api/qa/sessions / history)
                                                  |
+-------------------------------------------------+--------------------------------------------------+
|                                  现有 Web 管理界面 (Unit 2 Portal)                                 |
|  - 管理员界面：直接看到该仓库下 zhangsan 调用的全部问答记录，完全兼容无感                          |
|  - 普通用户界面：zhangsan 登录后，也能看到自己在 OpenCode 上的问答记录                              |
+----------------------------------------------------------------------------------------------------+
```

---

## 二、实施任务清单 (Task Breakdown)

### Task 1: 增强 SQLite 仓储层（防歧义仓库查询与索引状态联动）

**涉及文件：**
- 修改：`deploy/unit1-orchestrator/internal/db/sqlite.go`
- 测试：`deploy/unit1-orchestrator/internal/db/sqlite_test.go`

- [ ] **Step 1: 编写单元测试 (`sqlite_test.go`)**
  测试 `GetRepoByNameOrID`：
  - 验证当传入 ID 时精确返回对应仓库；
  - 验证当传入 Name 时准确返回对应仓库；
  - 特殊边界测试：当仓库 A 的 `name` 与仓库 B 的 `id` 相同且入参为该标识符时，优先精准匹配 ID 为 B 的仓库（消除 SQL 顺序歧义）。
  - 测试 `GetLatestBuild(repoID)`：验证能正确获取最新构建记录的状态。

- [ ] **Step 2: 运行测试验证**
  运行：`cd deploy/unit1-orchestrator && CGO_ENABLED=1 go test -v -run TestGetRepoByNameOrID ./internal/db`

- [ ] **Step 3: 实现 `GetRepoByNameOrID` 与辅助查询**
  在 `sqlite.go` 中避免使用单个 `WHERE id = ? OR name = ? LIMIT 1`，改为清晰的两级查找：
  ```go
  // GetRepoByNameOrID 优先根据 ID 精确匹配，未找到则按 Name 精确匹配
  func (db *DB) GetRepoByNameOrID(identifier string) (*Repo, error) {
      // 1. 优先按 ID 精确查
      r, err := db.GetRepo(identifier)
      if err != nil {
          return nil, err
      }
      if r != nil {
          return r, nil
      }

      // 2. 降级按 Name 查
      r = &Repo{}
      err = db.conn.QueryRow(`SELECT id, name, git_url, branch, local_path, 
          COALESCE(wiki_dir,''), COALESCE(static_dir,''), schedule, status, 
          created_at, updated_at FROM repos WHERE name = ? LIMIT 1`, 
          identifier).
          Scan(&r.ID, &r.Name, &r.GitURL, &r.Branch, &r.LocalPath,
              &r.WikiDir, &r.StaticDir, &r.Schedule, &r.Status,
              &r.CreatedAt, &r.UpdatedAt)
      if err == sql.ErrNoRows {
          return nil, nil
      }
      return r, err
  }
  ```

---

### Task 2: 实现 MCP 核心引擎与 Session 管理 (`internal/mcp`)

**涉及文件：**
- 新增：`deploy/unit1-orchestrator/internal/mcp/types.go`
- 新增：`deploy/unit1-orchestrator/internal/mcp/session.go`
- 新增：`deploy/unit1-orchestrator/internal/mcp/server.go`
- 新增：`deploy/unit1-orchestrator/internal/mcp/writer.go`
- 测试：`deploy/unit1-orchestrator/internal/mcp/server_test.go`

- [ ] **Step 1: 定义 JSON-RPC 2.0 与 MCP 数据模型 (`types.go`)**
  定义符合 MCP 2025-03-26 规范的请求/响应体：
  - JSON-RPC 标准格式：`JSONRPCMessage`, `Request`, `Response`, `ErrorResponse`
  - 协议握手：`InitializeRequestParams`, `InitializeResult` (ServerInfo, Capabilities)
  - 工具能力：`ListToolsResult`, `Tool`, `CallToolRequestParams`, `CallToolResult`, `ContentItem`
  - 协议通知：`LoggingMessageNotification` / `ProgressNotification`

- [ ] **Step 2: 实现 Session 管理器与安全防护 (`session.go`)**
  - **最大连接限制**：配置最大允许活跃 Session 数（默认 100），超出时拒绝并返回 HTTP 503；
  - **Origin 校验白名单**：检查请求 `Origin` Header，若非配置允许来源则阻断（防止 DNS Rebinding）；
  - **TTL 闲置清理机制**：每个 Session 记录 `lastActiveAt`，后台 goroutine 每分钟扫描一次，清理超过 30 分钟无通信的闲置 Session，释放 channel 与内存；
  - 提供 `GetOrCreateSession(id string)` 和消息通道发送接口。

- [ ] **Step 3: 实现 `mcpStreamWriter` 适配器 (`writer.go`)**
  零修改复用现有的 `qa.Manager.StreamChat`：
  ```go
  // mcpStreamWriter 包装流式数据，将底层 qa-daemon 的 SSE 输出转换成 MCP 格式发送
  type mcpStreamWriter struct {
      onToken func(token string)
      buf     strings.Builder
  }

  func (w *mcpStreamWriter) Write(p []byte) (int, error) {
      // 解析来自 qa.Manager 的流式数据（如 data: {"text":"..."} 行或文本块）
      // 提取出增量 Markdown token 并触发 onToken(token) 回调通知客户端
      return len(p), nil
  }
  ```

- [ ] **Step 4: 实现 MCP Tools 逻辑与 `user_id` 强校验 (`server.go`)**
  - **`ask_repository` 工具**：
    - InputSchema 必填项：`["repo_name", "user_id", "question"]`
    - 参数校验：检查 `user_id` 是否为空，若空直接返回 MCP 错误：`isError: true, content: [{"type":"text", "text":"user_id is required for audit"}]`；
    - 仓库定位：调用 `db.GetRepoByNameOrID(repoName)`，找不到返回错误；
    - 执行问答：调用现有的 `qaManager.StreamChat(ctx, repo, userID, sessionID, question, streamWriter)`，`qaManager` 会全自动处理 worker 通信并在问答完成时落库 `db.RecordQASession`，完全零侵入！
  - **`list_repositories` 工具**：
    - 查询所有活跃仓库，连同每个仓库的最新构建状态（`status`、`last_build_status`），并在结果中标记 `ready: true/false`，使智能体能感知知识库是否已建好。

- [ ] **Step 5: 编写与运行单元测试**
  运行：`cd deploy/unit1-orchestrator && CGO_ENABLED=1 go test -v ./internal/mcp`
  预期：PASS。

---

### Task 3: 挂载 Streamable HTTP & 旧版 SSE 路由并集成至 Orchestrator

**涉及文件：**
- 修改：`deploy/unit1-orchestrator/internal/api/server.go`
- 修改：`deploy/unit1-orchestrator/internal/api/router.go`
- 修改：`deploy/unit1-orchestrator/cmd/orchestrator/main.go`
- 测试：`deploy/unit1-orchestrator/internal/api/mcp_test.go`

- [ ] **Step 1: 将 `mcp.Server` 实例注入 `api.Server`**
  - 在 `internal/api/server.go` 中，为 `Server` 结构体增加 `mcpServer *mcp.Server` 字段；
  - 在 `cmd/orchestrator/main.go` 中，初始化 `mcpServer := mcp.NewServer(cfg, db, qaMgr)` 并传递给 `api.NewServer(...)`。

- [ ] **Step 2: 挂载多版本协议路由 (`router.go`)**
  - **Streamable HTTP 端点 (MCP 2025 规范)**：
    - `POST /mcp`：处理客户端 JSON-RPC 调用。支持普通 JSON 响应及 `text/event-stream` 流式传输长响应；
    - `GET /mcp`：长轮询或初始化 SSE 监听流（携带 `Mcp-Session-Id` header）；
  - **旧版 Remote SSE 兼容端点 (MCP 2024-11 规范 Fallback)**：
    - `GET /mcp/sse`：建立 SSE 长连接并下发 `endpoint` 事件；
    - `POST /mcp/messages`：接收旧版客户端投递的 JSON-RPC 请求；
  - **备用 HTTP 直连端点**：
    - `POST /api/mcp/chat`：标准 REST/SSE 接口，支持传入 `user_id`。

- [ ] **Step 3: 编写端到端集成测试 (`mcp_test.go`)**
  - 模拟 Streamable HTTP 协议握手、`tools/list` 与 `tools/call`；
  - 模拟旧版 Remote SSE 握手与消息发送；
  - 验证审计落库：调用 `GET /api/qa/sessions`，确认刚刚在 MCP 触发的问答记录已成功持久化并归属指定 `user_id`。
  - 运行：`cd deploy/unit1-orchestrator && CGO_ENABLED=1 go test -v -run TestMCP ./internal/api`

---

### Task 4: Nginx 反向代理配置与构建自动化

**涉及文件：**
- 修改：`deploy/nginx.conf`
- 修改：`deploy/MANUAL.md`

- [ ] **Step 1: 更新 `deploy/nginx.conf`**
  针对 Streamable HTTP 与 SSE 增加完善的反代参数配置，确保流式响应不被缓冲或截断：
  ```nginx
  # OpenCode MCP 协议端点反向代理 (/mcp 与 /mcp/)
  location /mcp {
      proxy_pass http://127.0.0.1:3000;
      proxy_http_version 1.1;
      proxy_set_header Connection '';
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
      
      # 保证流式输出实时刷新
      proxy_buffering off;
      proxy_cache off;
      chunked_transfer_encoding off;
      proxy_read_timeout 600s;
      proxy_send_timeout 600s;
  }
  ```

- [ ] **Step 2: 运行编译与全量测试验证**
  - 运行 `CGO_ENABLED=1 bash deploy/scripts/test-unit.sh`，确保所有单元测试通过，服务可正常编译出二进制包。

---

## 三、OpenCode 客户端配置与使用

### 1. 客户端配置 (`opencode.json`)
OpenCode 配置极其精简，直接配置统一的服务端地址，免去任何 URL 硬编码参数：

**推荐方式 (Streamable HTTP, MCP 2025 标准)：**
```json
{
  "mcp": {
    "openwiki": {
      "type": "remote",
      "url": "http://<OpenWiki服务器地址>/mcp"
    }
  }
}
```

*注：若使用仅支持旧版协议的客户端，亦可配置为 `http://<OpenWiki服务器地址>/mcp/sse`，服务端将自动兼容识别。*

### 2. 智能体 Tool 调用方式
OpenCode 的 Skill 在调用 `ask_repository` 工具时，传入环境动态识别的工号或用户名 `user_id`：
```json
{
  "name": "ask_repository",
  "arguments": {
    "repo_name": "openwiki",
    "user_id": "zhangsan",
    "question": "介绍一下整个项目的核心架构与登录权限流转"
  }
}
```

### 3. 管理界面审计呈现
1. **自动落库**：调用执行的同时，`orchestrator.db` 中的 `qa_sessions` 表实时记录该次对话（包含 `session_id="mcp_sess_..."`, `user_id="zhangsan"`）。
2. **管理员透明可见**：管理员在现有 Unit 2 Web 门户中进入“历史问答 / 审计日志”，直接查看该仓库下 `zhangsan` 的所有 MCP 提问记录与完整 Markdown 回答。
3. **用户查看**：`zhangsan` 在前端登录自己的账户时，也能在“我的问答”中查阅到由 OpenCode 代表自己执行的所有问答历史。
