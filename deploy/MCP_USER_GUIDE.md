# OpenWiki 远程 MCP 服务使用手册 (MCP User Guide)

OpenWiki Orchestrator 内置了企业级 **Model Context Protocol (MCP)** 服务，支持主流 AI 编程助手与智能体（如 **OpenCode、Cursor、Claude Desktop、Windsurf、Cline / Roo Code**）直接接入位于 **`10.88.160.135`** 的企业代码知识库，进行仓库级架构检索与流式 AI 问答。

---

## 一、服务器地址与协议说明

服务器 IP：**`10.88.160.135`**

OpenWiki MCP 服务支持**双传输协议**，客户端可按需选择：

| 协议类型 | 推荐场景 | 服务端点 (Endpoint) | 协议优势 |
| :--- | :--- | :--- | :--- |
| **Streamable HTTP** *(推荐)* | OpenCode、新版 IDE、现代化智能体 | `POST http://10.88.160.135/mcp` | 遵循 MCP 2025 标准单端点，支持 `Mcp-Session-Id` 会话管理，网络开销低 |
| **Legacy Remote SSE** | Cursor、Claude Desktop、传统客户端 | `GET http://10.88.160.135/mcp/sse`<br>`POST http://10.88.160.135/mcp/messages` | 传统 Server-Sent Events 事件长连接 + HTTP POST 消息通道 |

### 端口说明
* **标准统一接入（推荐，经过 Nginx 80 端口）**：
  * Streamable HTTP: `http://10.88.160.135/mcp`
  * Remote SSE: `http://10.88.160.135/mcp/sse`
* **后台直连模式（直连 Orchestrator 服务端口）**：
  * 若直接访问 Orchestrator 二进制进程，请使用端口 `3009`（开发环境）或 `3000`（容器内部）：
  * Streamable HTTP: `http://10.88.160.135:3009/mcp`
  * Remote SSE: `http://10.88.160.135:3009/mcp/sse`

---

## 二、公开的 MCP Tools (工具定义)

服务端暴露了 2 个核心工具，均内置完备的 JSON-Schema：

### 1. `list_repositories`
* **功能说明**：获取当前平台已纳管、已索引的所有代码仓库列表及其构建就绪状态。
* **参数要求**：无需任何入参。
* **返回值**：返回仓库列表数组，每个仓库包含 `id`、`name`、`branch`、`latest_build_status` 及是否支持问答。

### 2. `ask_repository`
* **功能说明**：基于 LangGraph 与本地 RAG 索引，对目标代码仓库进行深度架构设计、业务流程或代码实现细节问答，返回标准 Markdown。
* **参数定义**：
  | 参数名 | 类型 | 必填 | 说明 |
  | :--- | :--- | :---: | :--- |
  | `repo_name` | string | **是** | 目标代码仓库标识（如 `test`、`order-service` 等） |
  | `user_id` | string | **是** | **企业调用审计标识**（调用者的用户名或工号，如 `zhangsan`、`dev_01`） |
  | `question` | string | **是** | 具体技术或架构问题 |
  | `session_id` | string | 否 | 多轮会话标识；若传入相同 ID 可保留历史上下文 |

> ⚠️ **关于 `user_id` 强制审计说明**：
> 为满足金融/企业级代码安全审计合规要求，`ask_repository` 工具强制要求调用方传入 `user_id`。所有的问答日志与回答内容将全量记入 SQLite 审计表，管理员可在 Web 门户系统的「💬 问答审计」页面随时检索调阅。

---

## 三、各主流客户端接入配置

### 1. OpenCode 智能体配置

在项目的 `.opencode/opencode.json` 或用户的全局配置文件中添加：

```json
{
  "mcp": {
    "openwiki": {
      "type": "remote",
      "url": "http://10.88.160.135/mcp",
      "timeout": 600000
    }
  }
}
```
> **提示**：
> 1. 设置 `"timeout": 600000`（10分钟，毫秒级）可彻底解除客户端在执行深层架构分析时的超时限制；
> 2. 服务端支持 MCP 2025 Streamable HTTP 真流式（`Accept: text/event-stream`），并内置了 `notifications/progress` 进度心跳，OpenCode 的 `resetTimeoutOnProgress` 会自动清零重置超时计时器。
> 3. 若直连非 80 端口环境，将 URL 调整为 `http://10.88.160.135:3009/mcp`。

---

### 2. Cursor IDE 配置 (`mcp.json`)

Cursor 支持 Remote SSE 类型的 MCP 服务：

1. 打开 Cursor 设置：`Settings` -> `Features` -> `MCP Servers`，或者编辑工作区根目录的 `.cursor/mcp.json`：
```json
{
  "mcpServers": {
    "repowiki": {
      "url": "http://10.88.160.135/mcp/sse",
      "timeout": 600,
      "requestTimeout": 600
    }
  }
}
```
2. **真流式保障机制**：
   * 当 Cursor 发起 `POST /mcp/messages` 工具调用时，服务端在 **1ms 内立即返回 `202 Accepted`**；
   * 问答任务异步执行，增量 Token（打字机）与进度心跳通过已建立的 `/mcp/sse` 长连接持续推送；
   * 彻底避免了 Cursor 默认 60 秒 POST 请求硬超时（`-32001: Request timed out`）。

---

### 3. Claude Desktop 配置

在 Claude Desktop 配置文件中配置（macOS 位于 `~/Library/Application Support/Claude/claude_desktop_config.json`，Windows 位于 `%APPDATA%\Claude\claude_desktop_config.json`）：

```json
{
  "mcpServers": {
    "openwiki": {
      "url": "http://10.88.160.135/mcp/sse"
    }
  }
}
```
*保存后重启 Claude Desktop 客户端即可。*

---

### 4. VS Code (Cline / Roo Code 插件) 配置

打开 Cline / Roo Code 扩展设置，编辑 MCP 服务配置 `cline_mcp_settings.json`：

```json
{
  "mcpServers": {
    "openwiki": {
      "type": "sse",
      "url": "http://10.88.160.135/mcp/sse",
      "timeout": 600
    }
  }
}
```

---

> ℹ️ **兼容性说明（Web 门户原有接口 100% 不受影响）**：
> MCP 的所有流式改造严格隔离在 `internal/mcp` 模块内。Web 管理门户原有的 AI 问答接口（`/api/chat`、`/qa/chat`）及历史记录审计功能保持 100% 独立与完全向后兼容，网页端体验没有任何变动与影响。

---

## 四、命令行快捷测试与调试 (cURL 示例)

可以使用 `curl` 工具快速验证 `10.88.160.135` 上的 MCP 服务健康状况与接口能力：

### 1. 协议握手初始化 (`initialize`)
```bash
curl -X POST http://10.88.160.135/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
      "protocolVersion": "2025-03-26",
      "capabilities": {},
      "clientInfo": { "name": "curl-test", "version": "1.0.0" }
    }
  }'
```
**预期响应**：返回协议版本 `2025-03-26` 及服务能力声明。

---

### 2. 获取可用工具列表 (`tools/list`)
```bash
curl -X POST http://10.88.160.135/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/list",
    "params": {}
  }'
```
**预期响应**：返回包含 `list_repositories` 与 `ask_repository` 的工具定义列表。

---

### 3. 调用工具列出仓库 (`list_repositories`)
```bash
curl -X POST http://10.88.160.135/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {
      "name": "list_repositories",
      "arguments": {}
    }
  }'
```

---

### 4. 调用工具向代码知识库提问 (`ask_repository`)
```bash
curl -X POST http://10.88.160.135/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 4,
    "method": "tools/call",
    "params": {
      "name": "ask_repository",
      "arguments": {
        "repo_name": "test",
        "user_id": "admin",
        "question": "这个仓库的核心技术栈和主要功能是什么？"
      }
    }
  }'
```
**预期响应示例**：
```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "### 1. 技术栈概览\n\n该项目是一个基于 Java 8 + Maven 的演示工程，核心解决 ZooKeeper 1MB 数据上限复现与配置修复...\n"
      }
    ],
    "isError": false
  }
}
```

---

## 五、智能体提示词最佳实践 (System Prompt / Skill)

在智能体（如 OpenCode 或 Cursor）的 System Prompt 或 Skill 说明中，推荐加入以下规则引导大模型正确调用：

```markdown
当你需要了解某个代码仓库的全局设计、架构分层或特定业务模块实现时：
1. 首先调用 `list_repositories` 查看可用代码仓库列表；
2. 调用 `ask_repository` 时，必须传入你当前的用户标识（如 user_id: "your_name"）、目标仓库名以及具体问题；
3. 工具返回的答案基于 OpenWiki 离线构建的最新代码知识图谱与技术文档，请优先参考其结论。
```
