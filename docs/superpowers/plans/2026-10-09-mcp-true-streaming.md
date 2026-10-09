# MCP 真流式传输与抗超时架构改造实现计划 (MCP True Streaming Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 OpenWiki MCP 服务端（Unit 1 Orchestrator）全面升级为符合 MCP 2025 标准的**真流式架构 (True Streaming)**，支持 Remote SSE 即时 202 回复与异步 SSE 推流、Streamable HTTP `text/event-stream` 实时分块流式传输，以及 MCP `notifications/progress` 自动进度心跳；**严格确保 100% 向后兼容，绝不影响现有的 Web 门户 HTTP 问答接口（`/qa/chat` 及 `/api/chat`）**。

**Architecture:** 
1. **零破坏隔离原则 (Zero Impact on HTTP QA)**：现有的 HTTP SSE 问答接口（`/qa/chat`）的路由、入参结构和流式输出协议保持 100% 不变。`qa.Manager.StreamChat` 核心协议与入参接口保持纯粹复用，对 `qa-daemon.js` 的改动仅针对断开熔断与异常保护，保持原有 `event: status/delta/done/error` 数据面协议完全兼容；
2. **Remote SSE 异步流式 (Cursor/Claude Desktop)**：客户端调用 `POST /mcp/messages` 时，服务端完成参数校验后在 1ms 内立即返回 `202 Accepted`，后台 goroutine 驱动问答并将增量 Token、`notifications/progress` 和最终结果实时推入活跃的 `GET /mcp/sse` 长连接通道；
3. **Streamable HTTP 分块推流 (OpenCode/新版 IDE)**：`POST /mcp` 检测到 `Accept: text/event-stream` 时，立即发出 HTTP 200 Header 与 `Transfer-Encoding: chunked`，并在同一个 HTTP 连接中实时下发 SSE 事件，彻底消除首字节阻塞；若客户端要求 `application/json`，保持传统单包返回无缝降级；
4. **Worker 队列熔断与超时调优**：`qa-daemon.js` 在检测到调用方断开时快速释放队列，防止僵尸任务卡死 FIFO 队列；Orchestrator 内部默认 QA 超时从 120s 提升至 600s。

**Tech Stack:** Go 1.22+, Node.js (Worker Daemon), Model Context Protocol (MCP 2025-03-26 / Remote SSE), SQLite3, Nginx.

---

## 涉及文件清单 (File Map)

- `deploy/unit1-orchestrator/internal/mcp/types.go`：新增 `RequestMeta`、`ProgressParams` 等 MCP 进度与元数据结构体定义；
- `deploy/unit1-orchestrator/internal/mcp/server.go`：
  - 改造 `HandleLegacyMessages`：对 `tools/call` 异步执行并在 1ms 内返回 `202 Accepted`；
  - 改造 `HandleStreamableHTTP`：支持以 `text/event-stream` 格式实时推流；
  - 改造 `handleAskRepository`：支持 `_meta.progressToken` 周期性与事件级心跳；
- `deploy/unit1-orchestrator/internal/mcp/server_test.go`：新增真流式 SSE 与 HTTP 分块推流的单元测试；
- `deploy/unit1-orchestrator/internal/api/chat_test.go`：新增或扩充对既有 HTTP `/qa/chat` 接口的严格回归测试，确保不被任何 MCP 改动影响；
- `deploy/unit1-orchestrator/scripts/qa-daemon.js`：增加客户端断开时的排队清理与熔断逻辑（严格保留全部原有 SSE 数据格式）；
- `deploy/unit1-orchestrator/internal/config/config.go`：将 `QATimeoutSec` 默认值从 120 调升至 600；
- `deploy/entrypoint.sh` 与 `deploy/config-orch-local.json`：同步更新默认配置中的 `qa_timeout_sec` 为 600；
- `deploy/MCP_USER_GUIDE.md`：补充真流式架构说明及客户端超时配置指南。

---

## 任务拆解 (Tasks)

### Task 1: 扩展 MCP 类型定义 (`types.go`)

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/mcp/types.go`
- Test: `deploy/unit1-orchestrator/internal/mcp/server_test.go`

- [x] **Step 1: 编写针对 `_meta.progressToken` 解析的测试用例**

在 `deploy/unit1-orchestrator/internal/mcp/server_test.go` 中新增测试 `TestMCPProgressParamsParsing`：
```go
func TestMCPProgressParamsParsing(t *testing.T) {
	reqData := []byte(`{
		"jsonrpc": "2.0",
		"id": 100,
		"method": "tools/call",
		"params": {
			"name": "ask_repository",
			"arguments": {
				"repo_name": "test",
				"user_id": "test_user",
				"question": "test question"
			},
			"_meta": {
				"progressToken": "prog-token-123"
			}
		}
	}`)

	var req JSONRPCRequest
	if err := json.Unmarshal(reqData, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	var params CallToolRequestParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("unmarshal params error: %v", err)
	}

	if params.Meta == nil || params.Meta.ProgressToken != "prog-token-123" {
		t.Fatalf("expected progressToken prog-token-123, got %+v", params.Meta)
	}
}
```

- [x] **Step 2: 运行测试验证失败**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestMCPProgressParamsParsing ./internal/mcp
```
预期：编译失败，提示 `params.Meta` 未定义。

- [x] **Step 3: 在 `types.go` 中定义 `RequestMeta` 和 `ProgressParams`**

修改 `deploy/unit1-orchestrator/internal/mcp/types.go`：
```go
type RequestMeta struct {
	ProgressToken any `json:"progressToken,omitempty"`
}

type CallToolRequestParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Meta      *RequestMeta   `json:"_meta,omitempty"`
}

type ProgressParams struct {
	ProgressToken any     `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total,omitempty"`
	Message       string  `json:"message,omitempty"`
}
```

- [x] **Step 4: 重新运行测试验证通过**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestMCPProgressParamsParsing ./internal/mcp
```
预期：PASS。

- [x] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/mcp/types.go deploy/unit1-orchestrator/internal/mcp/server_test.go
git commit -m "feat(mcp): add RequestMeta and ProgressParams to support MCP progress notifications"
```

---

### Task 2: 实现 Remote SSE 模式的真流式响应 (`server.go`)

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/mcp/server.go`
- Test: `deploy/unit1-orchestrator/internal/mcp/server_test.go`

- [x] **Step 1: 编写 Remote SSE 异步 202 与流式推送测试**

在 `server_test.go` 中编写 `TestLegacySSEImmediateAcceptedAndStream`：
验证向 `/mcp/messages` 发送 `tools/call` 请求时：
1. HTTP POST 在 100ms 内即返回 `202 Accepted`；
2. 关联的 `session.MsgChan` 中能实时收到 `notifications/progress`、`notifications/message` 以及最终的 JSON-RPC 结果对象。

- [x] **Step 2: 运行测试验证失败**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestLegacySSEImmediateAcceptedAndStream ./internal/mcp
```
预期：FAIL（原代码同步阻塞直到问答全部结束）。

- [x] **Step 3: 改造 `HandleLegacyMessages` 与 `handleAskRepository`**

在 `server.go` 中：
1. 当 `req.Method == "tools/call"` 时：
   - 如果属于长耗时工具 `ask_repository`，在进行必要的基本校验后，**立即向客户端写入 HTTP 202 Accepted**；
   - 启动独立 goroutine 执行 `s.handleAskRepository(context.Background(), session, params.Arguments, params.Meta, req.ID)`；
2. 在 `handleAskRepository` 中：
   - 提取 `progressToken`；
   - 构造进度心跳：每 3~5 秒或在收到每个 Token 时向 `session` 派发 `notifications/progress`；
   - 问答结束时，将完整的 `JSONRPCResponse{ID: req.ID, Result: ...}` 发送到 `session.Send(resp)`。

- [x] **Step 4: 运行测试验证通过**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestLegacySSEImmediateAcceptedAndStream ./internal/mcp
```
预期：PASS。

- [x] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/mcp/server.go deploy/unit1-orchestrator/internal/mcp/server_test.go
git commit -m "feat(mcp): enable immediate 202 Accepted and async SSE streaming for legacy remote SSE"
```

---

### Task 3: 实现 Streamable HTTP 模式的分块推流响应 (`server.go`)

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/mcp/server.go`
- Test: `deploy/unit1-orchestrator/internal/mcp/server_test.go`

- [x] **Step 1: 编写 Streamable HTTP `text/event-stream` 分块传输测试**

在 `server_test.go` 中编写 `TestStreamableHTTPStreamingResponse`：
验证当请求携带 `Accept: text/event-stream` 并调用 `ask_repository` 时：
1. 响应头为 `Content-Type: text/event-stream`；
2. 响应体以分块 SSE 格式（`event: message\ndata: ...\n\n`）实时返回进度与答案。

- [x] **Step 2: 运行测试验证失败**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestStreamableHTTPStreamingResponse ./internal/mcp
```
预期：FAIL（原代码返回 `application/json`）。

- [x] **Step 3: 在 `HandleStreamableHTTP` 中实现流式分块下发**

在 `server.go` 中：
1. 检查 `r.Header.Get("Accept")`，若包含 `text/event-stream` 且请求为 `tools/call`：
   - 设置响应头：
     ```go
     w.Header().Set("Content-Type", "text/event-stream")
     w.Header().Set("Cache-Control", "no-cache")
     w.Header().Set("Connection", "keep-alive")
     w.Header().Set("X-Accel-Buffering", "no")
     w.WriteHeader(http.StatusOK)
     flusher.Flush()
     ```
   - 在流中持续推送 `notifications/message` 和 `notifications/progress`；
   - 执行完成后输出最终 JSON-RPC Result 响应事件并关闭连接。
2. 若客户端只请求 `application/json`，保持传统单包返回兼容。

- [x] **Step 4: 运行测试验证通过**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestStreamableHTTPStreamingResponse ./internal/mcp
```
预期：PASS。

- [x] **Step 5: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/mcp/server.go deploy/unit1-orchestrator/internal/mcp/server_test.go
git commit -m "feat(mcp): support text/event-stream chunked streaming for Streamable HTTP"
```

---

### Task 4: Worker 守护进程防队列堵塞与熔断优化（保障 HTTP 问答 100% 兼容）

**Files:**
- Modify: `deploy/unit1-orchestrator/scripts/qa-daemon.js`
- Sync to: `deploy/bin/x86/scripts/qa-daemon.js`, `deploy/bin/arm/scripts/qa-daemon.js`
- Test: `deploy/unit1-orchestrator/internal/api/chat_test.go`

- [x] **Step 1: 增加断开检测快速释放机制，严格保留原生 SSE 格式**

修改 `qa-daemon.js`：
在 `queue.enqueue` 执行队列中：
1. 保持原有的全部 SSE 输出格式（`status`、`delta`、`done`、`error`）100% 保持原状，确保 Web 门户 `/qa/chat` 和浏览器打字机无任何影响；
2. 在任务排队等待进入时，若 `clientDisconnected` 已为 true，直接跳过并丢弃，不启动底层 `runOpenWikiAgent`；
3. 增加队列超时机制：如果一个任务在队列中等待超过 30 秒尚未开始执行且调用方已断开，自动从队列中剔除；
4. 当执行异常中断时，确保当前仓库的 FIFO 锁立即释放，避免级联堵塞后续请求。

- [x] **Step 2: 验证现有 HTTP 问答接口 `/qa/chat` 兼容性**

编写并执行 `internal/api/chat_test.go` 测试，模拟 Web 门户发起正常的 `/qa/chat` 请求，验证：
1. HTTP 状态码为 200；
2. Header 保持 `Content-Type: text/event-stream`；
3. 输出流能够正常按行接收到 SSE 数据，未发生任何格式破坏或兼容性回归。
运行：
```bash
cd deploy/unit1-orchestrator && go test -v -run TestChat ./internal/api
```
预期：PASS。

- [x] **Step 3: 同步更新预构建目录中的 `qa-daemon.js`**

将修改后的 `deploy/unit1-orchestrator/scripts/qa-daemon.js` 复制到：
- `deploy/bin/x86/scripts/qa-daemon.js`
- `deploy/bin/arm/scripts/qa-daemon.js`

- [x] **Step 4: 提交更改**

```bash
git add deploy/unit1-orchestrator/scripts/qa-daemon.js deploy/bin/*/scripts/qa-daemon.js deploy/unit1-orchestrator/internal/api/chat_test.go
git commit -m "fix(qa-daemon): add client disconnect circuit breaker while preserving HTTP QA SSE compatibility"
```

---

### Task 5: 调高服务端 QA 超时配置与回归验证

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/config/config.go`
- Modify: `deploy/entrypoint.sh`
- Modify: `deploy/config-orch-local.json`

- [x] **Step 1: 修改默认超时为 600 秒**

1. `internal/config/config.go`：将 `DefaultConfig()` 中的 `QATimeoutSec: 120` 改为 `QATimeoutSec: 600`；
2. `deploy/entrypoint.sh`：将默认模板中的 `"qa_timeout_sec": 120` 改为 `600`；
3. `deploy/config-orch-local.json`：将 `"qa_timeout_sec": 120` 改为 `600`。

- [x] **Step 2: 重新运行 Orchestrator 全量单元测试（包含 MCP 与所有 HTTP API）**

运行：
```bash
cd deploy/unit1-orchestrator && go test -v ./...
```
预期：所有测试全部 PASS，包含 `/qa/chat`、`/api/repos`、`/mcp` 等所有路由。

- [x] **Step 3: 本地编译 Orchestrator 二进制**

运行：
```bash
cd deploy/unit1-orchestrator && go build -o ../bin_orchestrator main.go
```
预期：编译成功，生成 `deploy/bin_orchestrator`。

- [x] **Step 4: 提交更改**

```bash
git add deploy/unit1-orchestrator/internal/config/config.go deploy/entrypoint.sh deploy/config-orch-local.json
git commit -m "chore(config): bump default QA timeout from 120s to 600s"
```

---

### Task 6: 更新用户手册与在线说明 (`MCP_USER_GUIDE.md`)

**Files:**
- Modify: `deploy/MCP_USER_GUIDE.md`

- [x] **Step 1: 在使用手册中补充超时配置与流式说明**

在 `MCP_USER_GUIDE.md` 的配置章节中：
1. 明确指导在 `mcp.json` / `opencode.json` 中配置 `"timeout": 600`，彻底规避客户端 60s 硬限制；
2. 增加对 Streamable HTTP 与 Remote SSE 双真流式特性的说明；
3. 强调 Web 门户原有功能与接口完全一致无影响。

- [x] **Step 2: 提交文档更新**

```bash
git add deploy/MCP_USER_GUIDE.md
git commit -m "docs(mcp): update user guide with true streaming protocol and client timeout config"
```

---

## 自检核对 (Self-Review Checklist)

1. **HTTP QA 接口隔离度**：`/qa/chat` 与 `/api/chat` 的接口逻辑、入参、响应头和输出流未受任何修改，在 Task 4 中专设了回归测试用例；
2. **Spec Coverage**：涵盖客户端超时、FIFO 队列堵塞、Streamable HTTP 流式传输与 Remote SSE 异步返回；
3. **Placeholder Scan**：无任何 TBD 占位符；
4. **Type Consistency**：类型定义与 MCP 规范保持一致。
