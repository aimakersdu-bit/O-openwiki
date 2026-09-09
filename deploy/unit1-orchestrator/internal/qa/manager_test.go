package qa

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

func TestManagerWorkerLifecycle(t *testing.T) {
	// Create mock node script that acts as an echo QA daemon over UDS
	tmpDir, err := os.MkdirTemp("", "qa-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockDaemonScript := filepath.Join(tmpDir, "mock-daemon.js")
	mockScriptContent := `
const http = require('http');
const fs = require('fs');

const args = process.argv.slice(2);
let socketPath = '';
for (const arg of args) {
  if (arg.startsWith('--socket=')) socketPath = arg.substring(9);
}

try { if (fs.existsSync(socketPath)) fs.unlinkSync(socketPath); } catch {}

const server = http.createServer((req, res) => {
  if (req.url === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ status: 'ok' }));
    return;
  }
  if (req.method === 'POST') {
    let body = '';
    req.on('data', c => body += c);
    req.on('end', () => {
      const parsed = JSON.parse(body);
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      res.write('event: status\ndata: {"stage":"tool_start","name":"test_tool"}\n\n');
      res.write('event: delta\ndata: {"text":"Hello ' + parsed.question + '"}\n\n');
      res.write('event: done\ndata: {"fullAnswer":"Hello ' + parsed.question + '"}\n\n');
      res.end();
    });
  }
});

server.listen(socketPath, () => {
  console.log("Mock daemon listening on " + socketPath);
});
`
	if err := os.WriteFile(mockDaemonScript, []byte(mockScriptContent), 0o755); err != nil {
		t.Fatalf("failed to write mock daemon: %v", err)
	}

	// Initialize test DB for RecordQASession
	dbPath := filepath.Join(tmpDir, "test.db")
	if err := db.InitDB(dbPath); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	defer db.CloseDB()

	// Create manager with mock daemon
	mgr := NewManager(mockDaemonScript, tmpDir, 10, "")
	defer mgr.Close()

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
}
