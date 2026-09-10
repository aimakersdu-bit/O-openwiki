package qa

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

// WorkerProcess represents an active repository QA daemon process.
type WorkerProcess struct {
	RepoID     string
	SocketPath string
	Cmd        *exec.Cmd
	CreatedAt  time.Time
}

// Manager manages lifecycle, Unix Domain Sockets, and request routing for repository Worker daemons.
type Manager struct {
	mu           sync.Mutex
	workers      map[string]*WorkerProcess
	daemonScript string
	socketDir    string
	idleTimeout  int
	distDir      string
}

// NewManager creates a new repository worker daemon manager.
func NewManager(daemonScript, socketDir string, idleTimeoutSec int, distDir string) *Manager {
	if socketDir == "" {
		socketDir = os.TempDir()
	}
	if idleTimeoutSec <= 0 {
		idleTimeoutSec = 7200 // default 2 hours
	}
	if daemonScript == "" {
		daemonScript = "scripts/qa-daemon.js"
	}
	return &Manager{
		workers:      make(map[string]*WorkerProcess),
		daemonScript: daemonScript,
		socketDir:    socketDir,
		idleTimeout:  idleTimeoutSec,
		distDir:      distDir,
	}
}

func findDaemonScript(customPath string) string {
	if customPath != "" && filepath.IsAbs(customPath) {
		if _, err := os.Stat(customPath); err == nil {
			return customPath
		}
	}
	if env := os.Getenv("OPENWIKI_QA_DAEMON_SCRIPT"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}

	cwd, _ := os.Getwd()
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)

	candidates := []string{
		customPath,
		filepath.Join(cwd, customPath),
		filepath.Join(cwd, "scripts", "qa-daemon.js"),
		filepath.Join(cwd, "deploy", "unit1-orchestrator", "scripts", "qa-daemon.js"),
		filepath.Join(cwd, "..", "unit1-orchestrator", "scripts", "qa-daemon.js"),
		filepath.Join(cwd, "..", "deploy", "unit1-orchestrator", "scripts", "qa-daemon.js"),
		filepath.Join(cwd, "..", "..", "deploy", "unit1-orchestrator", "scripts", "qa-daemon.js"),
		filepath.Join(exeDir, "scripts", "qa-daemon.js"),
		filepath.Join(exeDir, "..", "scripts", "qa-daemon.js"),
		"/app/scripts/qa-daemon.js",
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if abs, err := filepath.Abs(c); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	return customPath
}

func findNodeBinary() string {
	if path, err := exec.LookPath("node"); err == nil {
		return path
	}
	candidates := []string{
		"/usr/local/bin/node",
		"/opt/homebrew/bin/node",
		"/usr/bin/node",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "node"
}

// getOrStartWorker returns an active, healthy worker for repo, or launches a new one.
func (m *Manager) getOrStartWorker(ctx context.Context, repo *db.Repo) (*WorkerProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if existing worker is still running and healthy
	if wp, ok := m.workers[repo.ID]; ok {
		if m.isWorkerAlive(wp) {
			return wp, nil
		}
		// Worker is dead or unhealthy, clean up
		m.cleanupWorker(wp)
		delete(m.workers, repo.ID)
	}

	// Ensure repository directory exists
	if repo.LocalPath != "" {
		if _, err := os.Stat(repo.LocalPath); os.IsNotExist(err) {
			_ = os.MkdirAll(repo.LocalPath, 0o755)
		}
	}

	// Create and start new worker
	socketPath := filepath.Join(m.socketDir, fmt.Sprintf("openwiki-qa-%s.sock", repo.ID))
	_ = os.Remove(socketPath) // Clean up stale socket if any

	// Locate daemon script and node binary
	scriptPath := findDaemonScript(m.daemonScript)
	nodeBin := findNodeBinary()

	args := []string{
		scriptPath,
		fmt.Sprintf("--socket=%s", socketPath),
		fmt.Sprintf("--ttl=%d", m.idleTimeout),
		fmt.Sprintf("--repo-dir=%s", repo.LocalPath),
	}

	cmd := exec.Command(nodeBin, args...)
	if repo.LocalPath != "" {
		cmd.Dir = repo.LocalPath
	}

	// Environment variables - ensure PATH includes common node installation directories
	env := os.Environ()
	hasPath := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = e + ":/usr/local/bin:/opt/homebrew/bin"
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin")
	}
	if m.distDir != "" {
		env = append(env, fmt.Sprintf("OPENWIKI_DIST_DIR=%s", m.distDir))
	}
	cmd.Env = env

	// Capture stderr for debugging
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start qa daemon for repo %s: %w (stderr: %s)", repo.ID, err, stderrBuf.String())
	}

	wp := &WorkerProcess{
		RepoID:     repo.ID,
		SocketPath: socketPath,
		Cmd:        cmd,
		CreatedAt:  time.Now(),
	}

	// Wait for UDS to become available (cold boot of large ESM modules can take 6-8s)
	readyTimeout := time.After(20 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var conn net.Conn
	var dialErr error
	ready := false

	for !ready {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = os.Remove(socketPath)
			return nil, ctx.Err()
		case <-readyTimeout:
			_ = cmd.Process.Kill()
			_ = os.Remove(socketPath)
			return nil, fmt.Errorf("timeout waiting for qa daemon UDS %s to become ready (stderr: %s)", socketPath, stderrBuf.String())
		case <-ticker.C:
			conn, dialErr = net.DialTimeout("unix", socketPath, 100*time.Millisecond)
			if dialErr == nil {
				_ = conn.Close()
				ready = true
			}
		}
	}

	m.workers[repo.ID] = wp
	log.Printf("[QA-Manager] Spawned QA Daemon for repo %s (PID: %d, Socket: %s)", repo.ID, cmd.Process.Pid, socketPath)
	return wp, nil
}

func (m *Manager) isWorkerAlive(wp *WorkerProcess) bool {
	if wp.Cmd == nil || wp.Cmd.Process == nil {
		return false
	}
	// Check if process has exited
	if err := wp.Cmd.Process.Signal(os.Signal(nil)); err != nil {
		return false
	}
	// Verify socket can be dialed
	conn, err := net.DialTimeout("unix", wp.SocketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (m *Manager) cleanupWorker(wp *WorkerProcess) {
	if wp.Cmd != nil && wp.Cmd.Process != nil {
		_ = wp.Cmd.Process.Kill()
		_ = wp.Cmd.Wait()
	}
	_ = os.Remove(wp.SocketPath)
}

// ChatPayload represents the request JSON payload sent to the Worker.
type ChatPayload struct {
	Question  string `json:"question"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	Language  string `json:"language,omitempty"`
}

// StreamChat forwards chat question to the repository's dedicated worker daemon.
func (m *Manager) StreamChat(ctx context.Context, repo *db.Repo, userID string, sessionID string, question string, outWriter io.Writer) error {
	var wp *WorkerProcess
	var err error

	// Attempt up to 2 times (in case worker died right as request arrived)
	for attempt := 0; attempt < 2; attempt++ {
		wp, err = m.getOrStartWorker(ctx, repo)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		errMsg := fmt.Sprintf("failed to acquire worker daemon: %v", err)
		if outWriter != nil {
			fmt.Fprintf(outWriter, "event: error\ndata: [Error: %s]\n\n", errMsg)
		}
		_ = db.RecordQASession(repo.ID, userID, question, "Error: "+errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Create HTTP client over Unix Domain Socket
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", wp.SocketPath)
			},
		},
	}

	threadID := sessionID
	if threadID != "" {
		threadID = fmt.Sprintf("openwiki-%s-%s", repo.ID, sessionID)
	}

	payloadBytes, _ := json.Marshal(ChatPayload{
		Question:  question,
		UserID:    userID,
		SessionID: sessionID,
		ThreadID:  threadID,
		Language:  "zh-CN",
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/chat", bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("create chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		// Connection failed - possibly worker exited; clean up worker and record error
		m.mu.Lock()
		m.cleanupWorker(wp)
		delete(m.workers, repo.ID)
		m.mu.Unlock()

		errMsg := fmt.Sprintf("worker communication error: %v", err)
		if outWriter != nil {
			fmt.Fprintf(outWriter, "event: error\ndata: [Error: %s]\n\n", errMsg)
		}
		_ = db.RecordQASession(repo.ID, userID, question, "Error: "+errMsg)
		return fmt.Errorf("%s", errMsg)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("worker returned HTTP %d: %s", resp.StatusCode, string(body))
		if outWriter != nil {
			fmt.Fprintf(outWriter, "event: error\ndata: [Error: %s]\n\n", errMsg)
		}
		_ = db.RecordQASession(repo.ID, userID, question, "Error: "+errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Stream response line by line to outWriter while aggregating the full answer
	scanner := bufio.NewScanner(resp.Body)
	var fullAnswer strings.Builder
	flusher, hasFlusher := outWriter.(interface{ Flush() })

	for scanner.Scan() {
		line := scanner.Text()

		// Parse SSE data to aggregate answer for session record
		if strings.HasPrefix(line, "data: ") {
			dataContent := strings.TrimPrefix(line, "data: ")
			var parsed struct {
				Text       string `json:"text"`
				FullAnswer string `json:"fullAnswer"`
				Error      string `json:"error"`
			}
			if err := json.Unmarshal([]byte(dataContent), &parsed); err == nil {
				if parsed.FullAnswer != "" {
					fullAnswer.Reset()
					fullAnswer.WriteString(parsed.FullAnswer)
				} else if parsed.Text != "" {
					fullAnswer.WriteString(parsed.Text)
				} else if parsed.Error != "" && fullAnswer.Len() == 0 {
					fullAnswer.WriteString("Error: " + parsed.Error)
				}
			} else {
				// Plaintext fallback
				fullAnswer.WriteString(dataContent)
			}
		}

		if outWriter != nil {
			fmt.Fprintf(outWriter, "%s\n", line)
			if hasFlusher {
				flusher.Flush()
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("error reading stream from worker: %w", err)
	}

	// Ensure trailing newline for standard SSE block termination
	if outWriter != nil {
		fmt.Fprintf(outWriter, "\n")
		if hasFlusher {
			flusher.Flush()
		}
	}

	answerText := fullAnswer.String()
	if answerText == "" {
		answerText = "[Completed]"
	}

	// Record QA session in SQLite database
	if err := db.RecordQASession(repo.ID, userID, question, answerText); err != nil {
		log.Printf("[QA-Manager] Warning: failed to record QA session: %v", err)
	}

	return nil
}

// Close gracefully terminates all running workers.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, wp := range m.workers {
		m.cleanupWorker(wp)
		delete(m.workers, id)
	}
}
