package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
)

func setupTestChatServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "api_chat_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	if err := db.InitDB(dbPath); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to init test db: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DBPath = dbPath

	server := NewServer(cfg, nil, nil, nil, nil)

	cleanup := func() {
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}

	return server, cleanup
}

func TestHTTPChatEndpointValidation(t *testing.T) {
	server, cleanup := setupTestChatServer(t)
	defer cleanup()

	// 1. Missing body
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty payload, got %d", w.Code)
	}

	// 2. Non-existent repo
	req2 := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader([]byte(`{
		"repo_id": "non_existent_repo",
		"question": "Hello?"
	}`)))
	w2 := httptest.NewRecorder()
	server.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent repo, got %d", w2.Code)
	}
}

func TestHTTPChatEndpointHeaders(t *testing.T) {
	server, cleanup := setupTestChatServer(t)
	defer cleanup()

	// Insert active repo
	repo := &db.Repo{
		ID:     "test-repo-qa",
		Name:   "Test Repo QA",
		Branch: "main",
		Status: "active",
	}
	if err := db.GetDB().CreateRepo(repo); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader([]byte(`{
		"repo_id": "test-repo-qa",
		"user_id": "test-user",
		"question": "What is the project architecture?"
	}`)))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	// Verify standard HTTP SSE headers are preserved
	contentType := w.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %s", contentType)
	}

	cacheControl := w.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "no-cache") {
		t.Fatalf("expected Cache-Control no-cache, got %s", cacheControl)
	}

	connection := w.Header().Get("Connection")
	if connection != "keep-alive" {
		t.Fatalf("expected Connection keep-alive, got %s", connection)
	}
}
