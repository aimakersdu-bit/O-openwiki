# LLMProxy 真正流式透传与防降级优化 Implementation Plan (v2 修订版)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 彻底解决 OpenWiki 请求经过 Orchestrator 内置 `llmproxy` 时被降级为非流式、全量缓冲导致任务卡慢的问题，在**严格保证 OpenWiki 核心源码 (`src/`, `test/`) 零修改**的前提下，实现端到端真正实时流式透传与零延迟首 Token 响应。

**Architecture:** 
1. **网络层剥离与防缓冲**：在 `llmproxy` 请求转发层剥离客户端传来的 `Accept-Encoding: gzip`，防止上游反向代理因无法对动态流实时压缩而进行全量内存缓冲；注入 `X-Accel-Buffering: no` 等防中间代理缓冲头；
2. **流式协商与协议保真**：规范化上游 `Accept: text/event-stream` 请求头；对 `200 OK` 且为 `text/event-stream` 的响应实时逐 chunk `Flush`；对非 200 或非 SSE 响应保真透传其 HTTP 状态码与 `application/json` 头，严禁篡改 Content-Type；
3. **Token 记账优化**：在 SSE 转发中优化 JSON 解析（仅对包含 `"usage"` 的数据行解析），自动为流式请求补齐 `"stream_options": {"include_usage": true}` 保证 Token 统计不丢失；
4. **子进程环境与配置对齐**：在 `builder.go` 中规范注入 `OPENWIKI_PROVIDER=openai-compatible`，同时注入 `OPENAI_API_KEY` 与 `OPENAI_COMPATIBLE_API_KEY=ow-build-<buildID>`，防止 OpenWiki CLI 凭据缺失崩溃；在 `config.go` 中支持 `openwiki_provider` 与 `~/.openwiki/.env` 兜底读取。

**Tech Stack:** Go 1.22+, `net/http`, Server-Sent Events (SSE), OpenWiki Node.js CLI (Zero-Modification on `src/`)

---

### File Structure Map

- Modify: `deploy/unit1-orchestrator/internal/llmproxy/proxy.go`
  - 职责：请求头过滤（剔除 `Accept-Encoding`）、流式协商标准化、响应防缓冲头注入、真实实时 chunk 透传、错误响应保真、usage 高效记账与 `stream_options` 补齐。
- Modify: `deploy/unit1-orchestrator/internal/llmproxy/proxy_test.go`
  - 职责：验证 Header 过滤、`stream: true` 头标准化、基于 `io.Pipe` 的真正实时无缓冲推流验证、错误响应保真验证。
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go`
  - 职责：规范 OpenWiki 运行环境变量，同时注入 `OPENWIKI_PROVIDER=openai-compatible`、`OPENAI_API_KEY`、`OPENAI_COMPATIBLE_API_KEY` 及 `OPENWIKI_MODEL_ID`。
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder_test.go`
  - 职责：验证子进程环境变量生成，覆盖凭据、Provider 及 Model ID 注入。
- Modify: `deploy/unit1-orchestrator/internal/config/config.go`
  - 职责：在 `Config` 中增加 `OpenwikiProvider` 字段；当未配置上游 URL/Key 时自动从 `~/.openwiki/.env` 读取兜底。
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`
  - 职责：健壮解析 `ListenAddr` 端口（避免 `0.0.0.0:3000` 截取错误），正确传递 Provider 与 Model 给 Builder。

---

### Task 1: 编写 LLMProxy 请求头过滤与协议标准化单元测试

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/llmproxy/proxy_test.go`

- [x] **Step 1: 在 `proxy_test.go` 编写针对 `Accept-Encoding` 剥离与 `stream: true` 规范化的测试**

```go
func TestStreamingHeaderSanitization(t *testing.T) {
	var capturedAcceptEncoding string
	var capturedAccept string
	var capturedBody []byte

	mockRT := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		capturedAcceptEncoding = r.Header.Get("Accept-Encoding")
		capturedAccept = r.Header.Get("Accept")
		capturedBody, _ = io.ReadAll(r.Body)
		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}, nil
	})

	proxy := NewLLMProxy("http://mock-upstream/v1", "key", nil, nil)
	proxy.SetTransport(mockRT)

	req := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[]}`))
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept", "application/json") // Node fetch default

	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if capturedAcceptEncoding != "" {
		t.Fatalf("expected Accept-Encoding to be stripped, got %q", capturedAcceptEncoding)
	}
	if capturedAccept != "text/event-stream" {
		t.Fatalf("expected Accept to be normalized to text/event-stream for stream:true, got %q", capturedAccept)
	}
	if w.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering: no, got %q", w.Header().Get("X-Accel-Buffering"))
	}
	// 验证自动补齐 stream_options.include_usage
	if !strings.Contains(string(capturedBody), `"include_usage":true`) && !strings.Contains(string(capturedBody), `"include_usage": true`) {
		t.Fatalf("expected stream_options.include_usage injected into stream request, got %s", string(capturedBody))
	}
}
```

- [x] **Step 2: 运行测试验证失败**

Run: `cd deploy/unit1-orchestrator && go test -v -run TestStreamingHeaderSanitization ./internal/llmproxy`
Expected: FAIL（因当前代码未剥离 `Accept-Encoding` 且未自动补齐 `include_usage`）。

---

### Task 2: 修复 `proxy.go` 中的请求头规范化与防缓冲

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/llmproxy/proxy.go`

- [x] **Step 1: 实现请求体流式检测与 `stream_options` 注入**

在 `proxy.go` 中添加：
```go
func isStreamRequest(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Stream
	}
	return bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
}

func ensureStreamUsageOption(body []byte) []byte {
	if len(body) == 0 || !isStreamRequest(body) {
		return body
	}
	if bytes.Contains(body, []byte(`"include_usage"`)) {
		return body
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	opts, ok := raw["stream_options"].(map[string]interface{})
	if !ok {
		opts = make(map[string]interface{})
	}
	opts["include_usage"] = true
	raw["stream_options"] = opts
	if modified, err := json.Marshal(raw); err == nil {
		return modified
	}
	return body
}
```

- [x] **Step 2: 在 `ServeHTTP` 转发请求头时剔除 `Accept-Encoding` 与 `Content-Length`，对流式请求规范化 `Accept`**

```go
bodyBytes = ensureStreamUsageOption(bodyBytes)
isStream := isStreamRequest(bodyBytes)

for k, vv := range r.Header {
    if strings.EqualFold(k, "Accept-Encoding") || strings.EqualFold(k, "Content-Length") {
        continue
    }
    for _, v := range vv {
        req.Header.Add(k, v)
    }
}
if isStream {
    req.Header.Set("Accept", "text/event-stream")
}
```

- [x] **Step 3: 运行测试验证 Task 1 通过**

Run: `cd deploy/unit1-orchestrator && go test -v -run TestStreamingHeaderSanitization ./internal/llmproxy`
Expected: PASS

---

### Task 3: 编写并实现真正实时流式推流（基于 `io.Pipe` 验证）与非 200 错误响应保真

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/llmproxy/proxy_test.go`
- Modify: `deploy/unit1-orchestrator/internal/llmproxy/proxy.go`

- [x] **Step 1: 在 `proxy_test.go` 中编写基于 `io.Pipe` 的真正实时无缓冲推流测试**

```go
type flushingRecorder struct {
	*httptest.ResponseRecorder
	flushedCh chan struct{}
	flushCount atomic.Int32
}

func newFlushingRecorder() *flushingRecorder {
	return &flushingRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushedCh:        make(chan struct{}, 10),
	}
}

func (f *flushingRecorder) Flush() {
	f.flushCount.Add(1)
	select {
	case f.flushedCh <- struct{}{}:
	default:
	}
}

func TestRealTimeStreamingFlush(t *testing.T) {
	pr, pw := io.Pipe()

	mockRT := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       pr,
		}, nil
	})

	proxy := NewLLMProxy("http://mock-upstream/v1", "key", nil, nil)
	proxy.SetTransport(mockRT)

	req := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	w := newFlushingRecorder()

	doneCh := make(chan struct{})
	go func() {
		proxy.ServeHTTP(w, req)
		close(doneCh)
	}()

	// 1. 发送首个 chunk，保持 pipe 不关闭
	_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))

	// 2. 验证客户端在 upstream 结束前就已经收到数据并触发了 Flush (证明无 io.ReadAll 缓冲)
	select {
	case <-w.flushedCh:
		// 成功收到实时 Flush
	case <-time.After(2 * time.Second):
		pw.Close()
		t.Fatal("timed out waiting for first chunk to be flushed; proxy buffered the stream!")
	}

	// 3. 发送带 usage 的结束 chunk 并关闭
	_, _ = pw.Write([]byte("data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n\n"))
	pw.Close()

	<-doneCh

	// 验证 token 记账正确
	u, ok := proxy.GetBuildUsage("")
	if !ok || u.TotalTokens != 15 {
		t.Fatalf("expected 15 total tokens, got %v (ok=%v)", u, ok)
	}
}
```

- [x] **Step 2: 在 `proxy_test.go` 中增加测试：验证 upstream 返回非 200 JSON 错误时保持 `application/json` 头，绝不篡改为 `text/event-stream`**

```go
func TestErrorResponsePreservesJSONContentType(t *testing.T) {
	mockRT := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid model","type":"invalid_request_error"}}`)),
		}, nil
	})

	proxy := NewLLMProxy("http://mock-upstream/v1", "key", nil, nil)
	proxy.SetTransport(mockRT)

	req := httptest.NewRequest("POST", "/internal/llm/v1/chat/completions", strings.NewReader(`{"stream":true,"model":"unknown"}`))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected application/json preserved for error response, got %s", w.Header().Get("Content-Type"))
	}
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("error response should NEVER have text/event-stream content-type")
	}
}
```

- [x] **Step 3: 修改 `forwardResponse` 与 `forwardSSE`**

在 `forwardResponse` 中严格判定：
```go
func (p *LLMProxy) forwardResponse(w http.ResponseWriter, resp *http.Response, buildID string, estTokens int) {
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	// 仅当 200 OK 且明确为 text/event-stream 时走流式通道
	if resp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		p.forwardSSE(w, resp, buildID, estTokens)
		return
	}

	// 其余普通响应（JSON、错误码、HTML等）保真透传
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read upstream response: %v", err), http.StatusInternalServerError)
		return
	}

	// 提取 Usage (仅在 200 OK 的 JSON 响应中)
	if strings.Contains(contentType, "application/json") && resp.StatusCode == http.StatusOK {
		var tokenData struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &tokenData); err == nil && tokenData.Usage.TotalTokens > 0 {
			p.AddTokens(buildID, tokenData.Usage.PromptTokens, tokenData.Usage.CompletionTokens, tokenData.Usage.TotalTokens)
			p.limiter.RecordRealUsage(estTokens, tokenData.Usage.TotalTokens)
		}
	}

	// 复制并过滤 Header，保持原始状态码与 Content-Type
	for k, vv := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
```

在 `forwardSSE` 中：
1. 注入防反代缓冲头：`X-Accel-Buffering: no`、`Cache-Control: no-cache, no-transform`、`Connection: keep-alive`；
2. 剔除 `Content-Encoding`、`Transfer-Encoding`，防止破坏客户端解析；
3. 解析 chunk usage 前先进行 `strings.Contains(payload, "usage")` 快速检查，避免对数千行 delta chunks 造成无意义的 JSON 反序列化开销；
4. 记录 `scanner.Err()` 避免静默吞掉流传输错误。

- [x] **Step 4: 运行测试验证**

Run: `cd deploy/unit1-orchestrator && go test -v -run "TestRealTimeStreamingFlush|TestErrorResponsePreservesJSONContentType" ./internal/llmproxy`
Expected: PASS

---

### Task 4: 修复 `builder.go` 环境变量注入与 OpenWiki 凭据对齐

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder_test.go`

- [x] **Step 1: 在 `builder_test.go` 中编写针对 Provider、双 Key 注入及 Model ID 的验证测试**

```go
func TestBuilderEnvInjectionForOpenAICompatible(t *testing.T) {
	b := &Builder{
		LLMBaseURL:    "http://127.0.0.1:3000/internal/llm/v1",
		LLMTimeoutSec: 300,
	}

	cmd := b.createOpenWikiCmdWithBuildID(123, "/tmp/mock-repo", "code", "--update")

	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// 1. 验证 Provider 设为 openai-compatible
	if envMap["OPENWIKI_PROVIDER"] != "openai-compatible" {
		t.Fatalf("expected OPENWIKI_PROVIDER=openai-compatible, got %q", envMap["OPENWIKI_PROVIDER"])
	}
	// 2. 验证 OPENAI_API_KEY 与 OPENAI_COMPATIBLE_API_KEY 均被注入为 ow-build-123
	if envMap["OPENAI_API_KEY"] != "ow-build-123" {
		t.Fatalf("expected OPENAI_API_KEY=ow-build-123, got %q", envMap["OPENAI_API_KEY"])
	}
	if envMap["OPENAI_COMPATIBLE_API_KEY"] != "ow-build-123" {
		t.Fatalf("expected OPENAI_COMPATIBLE_API_KEY=ow-build-123, got %q", envMap["OPENAI_COMPATIBLE_API_KEY"])
	}
	// 3. 验证 Base URL
	if envMap["OPENAI_BASE_URL"] != "http://127.0.0.1:3000/internal/llm/v1/build/123" {
		t.Fatalf("unexpected OPENAI_BASE_URL: %s", envMap["OPENAI_BASE_URL"])
	}
	if envMap["OPENAI_COMPATIBLE_BASE_URL"] != "http://127.0.0.1:3000/internal/llm/v1/build/123" {
		t.Fatalf("unexpected OPENAI_COMPATIBLE_BASE_URL: %s", envMap["OPENAI_COMPATIBLE_BASE_URL"])
	}
}
```

- [x] **Step 2: 修改 `builder.go` 中的环境变量注入逻辑**

在 `createOpenWikiCmdWithBuildID` 中：
```go
if llmBaseURL != "" {
    baseURL := strings.TrimSuffix(llmBaseURL, "/")
    if buildID > 0 {
        baseURL = fmt.Sprintf("%s/build/%d", baseURL, buildID)
    }
    env = upsertEnv(env, "OPENAI_BASE_URL", baseURL)
    env = upsertEnv(env, "OPENAI_COMPATIBLE_BASE_URL", baseURL)

    // 默认对齐通用 OpenAI 兼容网关 (/v1/chat/completions)
    env = setEnvIfUnset(env, "OPENWIKI_PROVIDER", "openai-compatible")

    cliTimeoutSec := llmTimeoutSec*2 + 120
    env = upsertEnv(env, "OPENWIKI_LLM_TIMEOUT_SEC", fmt.Sprintf("%d", cliTimeoutSec))
    env = setEnvIfUnset(env, "OPENWIKI_PROVIDER_RETRY_ATTEMPTS", "1")
}

// 关键修正：同时注入 OPENAI_API_KEY 与 OPENAI_COMPATIBLE_API_KEY
// 防止 OpenWiki 在 openai-compatible 模式下因缺少 OPENAI_COMPATIBLE_API_KEY 报错崩溃
if buildID > 0 {
    keyVal := fmt.Sprintf("ow-build-%d", buildID)
    env = upsertEnv(env, "OPENAI_API_KEY", keyVal)
    env = upsertEnv(env, "OPENAI_COMPATIBLE_API_KEY", keyVal)
} else if llmBaseURL != "" {
    // buildID == 0 占位符，保证 CLI 校验通过
    env = setEnvIfUnset(env, "OPENAI_API_KEY", "ow-build-0")
    env = setEnvIfUnset(env, "OPENAI_COMPATIBLE_API_KEY", "ow-build-0")
}
```

- [x] **Step 3: 运行 builder 测试验证**

Run: `cd deploy/unit1-orchestrator && go test -v -run TestBuilderEnv ./internal/scheduler`
Expected: PASS

---

### Task 5: 优化 `config.go` 与 `router.go`（支持 `~/.openwiki/.env` 兜底与端口健壮解析）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/config/config.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go`

- [x] **Step 1: 在 `Config` 中增加 `OpenwikiProvider` 字段并在 `overrideEnv` 中支持 `~/.openwiki/.env` 读取**

在 `config.go`：
```go
type Config struct {
    ...
    OpenwikiProvider   string `json:"openwiki_provider"`
    ...
}
```
当系统环境变量和 config.json 中未配置 `openai_upstream_url` 时，尝试检查 `~/.openwiki/.env`（或 `$OPENWIKI_HOME/.env`）中的 `OPENAI_COMPATIBLE_BASE_URL` 和 `OPENAI_COMPATIBLE_API_KEY` 作为兜底，避免私有化部署时 upstream 默认退回到官方 openai。

- [x] **Step 2: 在 `router.go` 健壮提取监听端口**

```go
port := "3000"
if cfg != nil && cfg.ListenAddr != "" {
    if _, p, err := net.SplitHostPort(cfg.ListenAddr); err == nil && p != "" {
        port = p
    } else {
        port = strings.TrimPrefix(cfg.ListenAddr, ":")
    }
}
```

- [x] **Step 3: 运行配置与路由测试验证**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/config/... ./internal/api/...`
Expected: PASS

---

### Task 6: 整体编译与完整回归验证

**Files:**
- Run commands across `deploy/unit1-orchestrator`

- [x] **Step 1: 运行 llmproxy 全部单元测试**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/llmproxy/...`
Expected: PASS (所有并发、限流、退避、Header 剥离、双向流式推流、错误保真测试均通过)

- [x] **Step 2: 运行 scheduler 与 api 全部单元测试**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/scheduler/... ./internal/api/...`
Expected: PASS

- [x] **Step 3: 验证 orchestrator 完整编译**

Run: `cd deploy/unit1-orchestrator && go build -o /tmp/test-orchestrator main.go`
Expected: 编译成功，退出码 0。
