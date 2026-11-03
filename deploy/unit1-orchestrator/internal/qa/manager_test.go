package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

type mockRoundTripper struct {
	fn func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.fn(req)
}

func TestManagerWorkerLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "qa-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize test DB for RecordQASession
	dbPath := filepath.Join(tmpDir, "test.db")
	if err := db.InitDB(dbPath); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	defer db.CloseDB()

	// Create manager with mock transport
	mgr := NewManager("scripts/qa-daemon.js", tmpDir, 10, "")
	defer mgr.Close()

	mgr.SetTransport(&mockRoundTripper{
		fn: func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			var parsed struct {
				Question string `json:"question"`
			}
			_ = json.Unmarshal(body, &parsed)
			respContent := fmt.Sprintf("event: status\ndata: {\"stage\":\"tool_start\",\"name\":\"test_tool\"}\n\nevent: delta\ndata: {\"text\":\"Hello %s\"}\n\nevent: done\ndata: {\"fullAnswer\":\"Hello %s\"}\n\n", parsed.Question, parsed.Question)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(respContent)),
			}, nil
		},
	})

	repo := &db.Repo{
		ID:        "test-repo-1",
		LocalPath: tmpDir,
	}

	// Send chat request
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var buf bytes.Buffer
	err = mgr.StreamChat(ctx, repo, "user-1", "sess-1", "World", &buf)
	if err != nil {
		t.Fatalf("StreamChat failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "event: status") {
		t.Errorf("expected output to contain 'event: status', got: %s", output)
	}
	if !strings.Contains(output, "Hello World") {
		t.Errorf("expected output to contain 'Hello World', got: %s", output)
	}

	// Verify DB record
	sessions, err := db.ListQASessions(repo.ID, "user-1", 10)
	if err != nil {
		t.Fatalf("ListQASessions failed: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatalf("expected at least 1 QA session recorded, got 0")
	}
	if sessions[0].Question != "World" || !strings.Contains(sessions[0].Answer, "Hello World") {
		t.Errorf("unexpected session record: %+v", sessions[0])
	}

	// Test Evict
	mgr.Evict(repo.ID)
	if _, ok := mgr.workers[repo.ID]; ok {
		t.Errorf("expected repo worker to be evicted")
	}
}
