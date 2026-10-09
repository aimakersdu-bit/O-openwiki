package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
)

func setupTestServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "mcp_test_db_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DBPath = dbPath

	// We pass nil qaMgr for non-execution tests
	server := NewServer(cfg, database, nil)

	cleanup := func() {
		server.sessions.Stop()
		database.Close()
		os.RemoveAll(tmpDir)
	}

	return server, cleanup
}

func TestMCPInitialize(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	initReq := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "opencode", "version": "1.0.0"}
		}
	}`)

	resp, err := server.ProcessJSONRPC(context.Background(), nil, initReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || resp.Error != nil {
		t.Fatalf("expected successful response, got %v", resp)
	}

	res, ok := resp.Result.(InitializeResult)
	if !ok {
		t.Fatalf("result is not InitializeResult: %T", resp.Result)
	}
	if res.ServerInfo.Name != "openwiki-mcp-server" {
		t.Errorf("expected server name openwiki-mcp-server, got %s", res.ServerInfo.Name)
	}
}

func TestMCPToolsList(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	listReq := []byte(`{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "tools/list"
	}`)

	resp, err := server.ProcessJSONRPC(context.Background(), nil, listReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("got json-rpc error: %v", resp.Error)
	}

	toolsRes, ok := resp.Result.(ListToolsResult)
	if !ok {
		t.Fatalf("result is not ListToolsResult: %T", resp.Result)
	}

	if len(toolsRes.Tools) < 2 {
		t.Fatalf("expected at least 2 tools, got %d", len(toolsRes.Tools))
	}

	var foundAsk bool
	for _, tool := range toolsRes.Tools {
		if tool.Name == "ask_repository" {
			foundAsk = true
			var hasUserID bool
			for _, reqField := range tool.InputSchema.Required {
				if reqField == "user_id" {
					hasUserID = true
					break
				}
			}
			if !hasUserID {
				t.Errorf("ask_repository MUST require user_id for audit")
			}
		}
	}

	if !foundAsk {
		t.Errorf("ask_repository tool not found in list")
	}
}

func TestMCPAuditValidation(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Missing user_id
	callReqMissingUser := []byte(`{
		"jsonrpc": "2.0",
		"id": 3,
		"method": "tools/call",
		"params": {
			"name": "ask_repository",
			"arguments": {
				"repo_name": "openwiki",
				"question": "What is the architecture?"
			}
		}
	}`)

	resp, err := server.ProcessJSONRPC(context.Background(), nil, callReqMissingUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, ok := resp.Result.(*CallToolResult)
	if !ok {
		t.Fatalf("result is not *CallToolResult: %T", resp.Result)
	}
	if !res.IsError {
		t.Fatalf("expected error when user_id is missing")
	}
	if len(res.Content) == 0 || res.Content[0].Text == "" {
		t.Fatalf("expected error message describing user_id is required")
	}

	// 2. Missing repo_name
	callReqMissingRepo := []byte(`{
		"jsonrpc": "2.0",
		"id": 4,
		"method": "tools/call",
		"params": {
			"name": "ask_repository",
			"arguments": {
				"user_id": "alice",
				"question": "What is the architecture?"
			}
		}
	}`)

	resp, err = server.ProcessJSONRPC(context.Background(), nil, callReqMissingRepo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, ok = resp.Result.(*CallToolResult)
	if !ok {
		t.Fatalf("result is not *CallToolResult: %T", resp.Result)
	}
	if !res.IsError {
		t.Fatalf("expected error when repo_name is missing")
	}
}

func TestMCPStreamWriter(t *testing.T) {
	var tokens []string
	writer := NewMCPStreamWriter(func(token string) {
		tokens = append(tokens, token)
	})

	// Simulate streaming data chunks from QA worker
	chunk1 := []byte("data: {\"text\":\"Hello \"}\n")
	chunk2 := []byte("data: {\"text\":\"World!\"}\n")
	chunk3 := []byte("data: [DONE]\n\n")

	_, _ = writer.Write(chunk1)
	_, _ = writer.Write(chunk2)
	_, _ = writer.Write(chunk3)
	writer.Flush()

	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0] != "Hello " || tokens[1] != "World!" {
		t.Errorf("unexpected tokens: %v", tokens)
	}

	if writer.FullAnswer() != "Hello World!" {
		t.Errorf("expected full answer 'Hello World!', got '%s'", writer.FullAnswer())
	}
}

func TestMCPSessionManager(t *testing.T) {
	sm := NewSessionManager(2, 50*time.Millisecond, []string{"http://localhost:3000"})
	defer sm.Stop()

	// 1. Origin check
	if !sm.CheckOrigin("http://localhost:3000", "localhost:3000") {
		t.Errorf("expected allowed origin to pass")
	}
	if sm.CheckOrigin("http://malicious-site.com", "localhost:3000") {
		t.Errorf("expected malicious origin to fail")
	}

	// 2. Max sessions check
	s1, err := sm.GetOrCreate("s1")
	if err != nil || s1 == nil {
		t.Fatalf("failed to create s1: %v", err)
	}
	s2, err := sm.GetOrCreate("s2")
	if err != nil || s2 == nil {
		t.Fatalf("failed to create s2: %v", err)
	}
	_, err = sm.GetOrCreate("s3")
	if err != ErrMaxSessionsExceeded {
		t.Fatalf("expected ErrMaxSessionsExceeded, got %v", err)
	}

	// 3. TTL Expiry
	time.Sleep(100 * time.Millisecond)
	sm.cleanExpired()
	if sm.Count() != 0 {
		t.Errorf("expected sessions to be expired, got %d", sm.Count())
	}
}

func TestStreamableHTTPProtocol(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Test POST /mcp with tools/list
	body := []byte(`{"jsonrpc":"2.0","id":10,"method":"tools/list"}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.HandleStreamableHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var jsonResp JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&jsonResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if jsonResp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", jsonResp.Error)
	}
}

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

func TestLegacySSEImmediateAcceptedAndStream(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	session, err := server.sessions.GetOrCreate("test-legacy-sess")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	body := []byte(`{
		"jsonrpc": "2.0",
		"id": 200,
		"method": "tools/call",
		"params": {
			"name": "ask_repository",
			"arguments": {
				"repo_name": "non-existent",
				"user_id": "u1",
				"question": "q1"
			},
			"_meta": {
				"progressToken": "token-xyz"
			}
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/mcp/messages?sessionId=test-legacy-sess", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.HandleLegacyMessages(w, req)

	// 1. Must respond 202 Accepted immediately
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", w.Code)
	}

	// 2. Channel must receive notifications or response asynchronously
	select {
	case msg := <-session.MsgChan:
		if msg == nil {
			t.Fatalf("received nil message on MsgChan")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for asynchronous message on MsgChan")
	}
}

func TestStreamableHTTPStreamingResponse(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	body := []byte(`{
		"jsonrpc": "2.0",
		"id": 300,
		"method": "tools/call",
		"params": {
			"name": "ask_repository",
			"arguments": {
				"repo_name": "non-existent",
				"user_id": "u1",
				"question": "q1"
			},
			"_meta": {
				"progressToken": "token-sse-stream"
			}
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()

	server.HandleStreamableHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %s", contentType)
	}

	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "event: message") {
		t.Fatalf("expected SSE chunks containing 'event: message', got: %s", bodyStr)
	}
}
