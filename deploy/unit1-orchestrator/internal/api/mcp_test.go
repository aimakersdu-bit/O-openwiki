package api

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
	"github.com/openwiki/orchestrator/internal/mcp"
)

func setupTestAPIServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "api_mcp_test_*")
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

	mcpServer := mcp.NewServer(cfg, database, nil)
	server := NewServer(cfg, nil, nil, nil, mcpServer)

	cleanup := func() {
		database.Close()
		os.RemoveAll(tmpDir)
	}

	return server, cleanup
}

func TestMCPStreamableHTTPEndpoints(t *testing.T) {
	server, cleanup := setupTestAPIServer(t)
	defer cleanup()

	// 1. Test POST /mcp for initialize
	initBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /mcp, got %d", resp.StatusCode)
	}

	var jsonResp mcp.JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&jsonResp); err != nil {
		t.Fatalf("failed to decode initialize response: %v", err)
	}
	if jsonResp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", jsonResp.Error)
	}

	// 2. Test POST /mcp for tools/list
	listBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(listBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()

	server.ServeHTTP(w, req)
	resp = w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for tools/list, got %d", resp.StatusCode)
	}
}

func TestMCPLegacySSEEndpoints(t *testing.T) {
	server, cleanup := setupTestAPIServer(t)
	defer cleanup()

	// 1. Test GET /mcp/sse
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	go func() {
		server.ServeHTTP(w, req)
	}()

	var body string
	for i := 0; i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		body = w.Body.String()
		if strings.Contains(body, "event: endpoint") {
			break
		}
	}

	if !strings.Contains(body, "event: endpoint") || !strings.Contains(body, "/mcp/messages?sessionId=") {
		t.Fatalf("expected legacy endpoint event in body, got: %s", body)
	}

	// Extract session ID
	idx := strings.Index(body, "sessionId=")
	if idx == -1 {
		t.Fatalf("sessionId not found in endpoint event: %s", body)
	}
	sessionID := strings.TrimSpace(body[idx+len("sessionId="):])
	if end := strings.IndexAny(sessionID, "\r\n"); end != -1 {
		sessionID = sessionID[:end]
	}

	// 2. Test POST /mcp/messages?sessionId=...
	msgBody := []byte(`{"jsonrpc":"2.0","id":99,"method":"ping"}`)
	msgReq := httptest.NewRequest(http.MethodPost, "/mcp/messages?sessionId="+sessionID, bytes.NewReader(msgBody))
	msgReq.Header.Set("Content-Type", "application/json")
	msgW := httptest.NewRecorder()

	server.ServeHTTP(msgW, msgReq)
	if msgW.Result().StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted from /mcp/messages, got %d", msgW.Result().StatusCode)
	}
	cancel()
}
