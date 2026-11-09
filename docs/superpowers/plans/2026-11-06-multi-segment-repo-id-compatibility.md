# Multi-Segment Repository ID Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate Unix Domain Socket (`ENOENT` / `sun_path` length limit), build log persistence, and portal graph routing failures when repository or project IDs contain multi-segment slashes (e.g., `bxbank/DEO/xone-auth`).

**Architecture:** Introduce a deterministic, collision-resistant, and length-bounded socket path builder (`BuildSocketPath`) in the Orchestrator QA Manager paired with defensive parent-directory creation in `qa-daemon.js`. Ensure parent directories are created before writing per-repo build logs in `pipeline.go`, and support multi-segment `/wiki/<group>/<subgroup>/<repo>/` Referer paths in Portal's `/api/graph` handler.

**Tech Stack:** Go 1.22+ (`net`, `os`, `path/filepath`, `crypto/sha256`), Node.js ESM (`node:fs`, `node:path`, `node:http`), Go `testing` / `httptest`.

---

## File Structure

- Modify: `deploy/unit1-orchestrator/internal/qa/manager.go` — Add `BuildSocketPath(socketDir, prefix, id string) string` to sanitize path separators (`/` and `\` -> `__`) and cap total UDS path length at 100 bytes using a SHA-256 suffix; ensure parent directory exists before starting worker.
- Modify: `deploy/unit1-orchestrator/internal/qa/manager_test.go` — Add unit tests for `BuildSocketPath` (multi-segment IDs, separator collision resistance, >100-byte length bound, and `StreamChat` with `bxbank/DEO/xone-auth`).
- Modify: `deploy/unit1-orchestrator/scripts/qa-daemon.js` — Ensure `fs.mkdirSync(path.dirname(socketPath), { recursive: true })` before socket cleanup/listen and attach an explicit `server.on('error')` logger.
- Modify: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` — Ensure `os.MkdirAll(filepath.Dir(logFilePath), 0755)` is called before writing per-repo build log files when `repo.ID` contains `/`.
- Modify: `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go` — Add unit test verifying multi-segment `repo.ID` build log file creation.
- Modify: `deploy/unit2-portal/internal/api/router.go` — Update `handleApiGraph` Referer parser to extract full multi-segment `repoID` under `/wiki/...` instead of only the first path segment.
- Modify: `deploy/unit2-portal/internal/api/rbac_test.go` — Add unit test `TestHandleApiGraphMultiSegmentRepoID` verifying `/api/graph` with Referer `/wiki/bxbank/DEO/xone-auth/`.

---

### Task 1: Safe Unix Domain Socket Path Builder in QA Manager

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/qa/manager.go:1-25,130-175,450-495`
- Test: `deploy/unit1-orchestrator/internal/qa/manager_test.go`

- [x] **Step 1: Write the failing test in `deploy/unit1-orchestrator/internal/qa/manager_test.go`**

Append `TestBuildSocketPathAndMultiSegmentRepoID` to `deploy/unit1-orchestrator/internal/qa/manager_test.go`:

```go
func TestBuildSocketPathAndMultiSegmentRepoID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "qa-multiseg-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Multi-segment repo ID should not create nested subdirectories in socket filename
	sockPath := BuildSocketPath("/tmp", "openwiki-qa", "bxbank/DEO/xone-auth")
	expected := "/tmp/openwiki-qa-bxbank__DEO__xone-auth.sock"
	if sockPath != expected {
		t.Fatalf("expected socket path %q, got %q", expected, sockPath)
	}
	if filepath.Dir(sockPath) != "/tmp" {
		t.Fatalf("expected socket parent dir '/tmp', got %q", filepath.Dir(sockPath))
	}

	// 2. Collision resistance between "a/b" and "a_b"
	p1 := BuildSocketPath("/tmp", "openwiki-qa", "a/b")
	p2 := BuildSocketPath("/tmp", "openwiki-qa", "a_b")
	if p1 == p2 {
		t.Fatalf("expected distinct socket paths for 'a/b' and 'a_b', both got %q", p1)
	}

	// 3. Length cap for Unix Domain Socket limit (<= 100 bytes)
	longRepoID := "bxbank/department-alpha-beta-gamma/subteam-delta-epsilon/microservice-authentication-gateway-v2-production"
	longSockPath := BuildSocketPath("/tmp", "openwiki-qa", longRepoID)
	if len(longSockPath) > 100 {
		t.Fatalf("expected socket path length <= 100 bytes for UDS safety, got %d (%q)", len(longSockPath), longSockPath)
	}
	if !strings.HasSuffix(longSockPath, ".sock") {
		t.Fatalf("expected truncated socket path to end with .sock, got %q", longSockPath)
	}

	// 4. End-to-end StreamChat with multi-segment repo ID
	dbPath := filepath.Join(tmpDir, "test_multiseg.db")
	if err := db.InitDB(dbPath); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	defer db.CloseDB()

	mgr := NewManager("scripts/qa-daemon.js", tmpDir, 10, "")
	defer mgr.Close()

	mgr.SetTransport(&mockRoundTripper{
		fn: func(req *http.Request) (*http.Response, error) {
			respContent := "event: delta\ndata: {\"text\":\"OK\"}\n\nevent: done\ndata: {\"fullAnswer\":\"OK\"}\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(respContent)),
			}, nil
		},
	})

	repo := &db.Repo{
		ID:        "bxbank/DEO/xone-auth",
		LocalPath: filepath.Join(tmpDir, "repos", "bxbank", "DEO", "xone-auth"),
	}

	var buf bytes.Buffer
	if err := mgr.StreamChat(context.Background(), repo, "u1", "s1", "ping", &buf); err != nil {
		t.Fatalf("StreamChat with multi-segment repo ID failed: %v", err)
	}
	wp, ok := mgr.workers[repo.ID]
	if !ok {
		t.Fatalf("expected worker to be registered under key %q", repo.ID)
	}
	if filepath.Dir(wp.SocketPath) != filepath.Clean(tmpDir) {
		t.Fatalf("expected worker socket inside %q, got %q", tmpDir, wp.SocketPath)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run:
```bash
cd /Users/bigc/openwiki/deploy/unit1-orchestrator && go test -v ./internal/qa -run TestBuildSocketPathAndMultiSegmentRepoID
```
Expected: FAIL with `undefined: BuildSocketPath`.

- [x] **Step 3: Implement `BuildSocketPath` and update `getOrStartWorker` & `getOrStartProjectWorker` in `deploy/unit1-orchestrator/internal/qa/manager.go`**

In `deploy/unit1-orchestrator/internal/qa/manager.go`, add `"crypto/sha256"` and `"encoding/hex"` to imports, define `BuildSocketPath`, and update socket path construction in `getOrStartWorker` and `getOrStartProjectWorker`:

```go
// maxUDSSocketPathLen is kept safely below the 104-byte (macOS/BSD) and 108-byte (Linux) sun_path limit.
const maxUDSSocketPathLen = 100

// BuildSocketPath constructs a flat, UDS-safe socket path inside socketDir.
// Path separators ('/' and '\') in id are replaced with "__" so multi-segment repo IDs
// (e.g. "bxbank/DEO/xone-auth") do not reference non-existent subdirectories under socketDir.
// If the resulting path exceeds maxUDSSocketPathLen, the sanitized ID is truncated and
// suffixed with an 8-character SHA-256 digest of the original id.
func BuildSocketPath(socketDir, prefix, id string) string {
	if socketDir == "" {
		socketDir = os.TempDir()
	}
	safeID := strings.ReplaceAll(id, "/", "__")
	safeID = strings.ReplaceAll(safeID, "\\", "__")
	safeID = strings.ReplaceAll(safeID, ":", "_")

	fileName := fmt.Sprintf("%s-%s.sock", prefix, safeID)
	fullPath := filepath.Join(socketDir, fileName)
	if len(fullPath) <= maxUDSSocketPathLen {
		return fullPath
	}

	sum := sha256.Sum256([]byte(id))
	shortHash := hex.EncodeToString(sum[:4]) // 8 hex chars
	suffix := fmt.Sprintf("-%s.sock", shortHash)
	prefixPart := filepath.Join(socketDir, prefix+"-")

	avail := maxUDSSocketPathLen - len(prefixPart) - len(suffix)
	if avail < 8 {
		return filepath.Join(socketDir, fmt.Sprintf("%s-%s.sock", prefix, shortHash))
	}
	return prefixPart + safeID[:avail] + suffix
}
```

And in `getOrStartWorker` (`deploy/unit1-orchestrator/internal/qa/manager.go`):
```go
	if m.transport != nil {
		wp := &WorkerProcess{
			RepoID:     repo.ID,
			SocketPath: BuildSocketPath(m.socketDir, "mock", repo.ID),
			CreatedAt:  time.Now(),
		}
		m.workers[repo.ID] = wp
		return wp, nil
	}

	// Ensure repository directory exists
	if repo.LocalPath != "" {
		if _, err := os.Stat(repo.LocalPath); os.IsNotExist(err) {
			_ = os.MkdirAll(repo.LocalPath, 0o755)
		}
	}

	// Create and start new worker
	socketPath := BuildSocketPath(m.socketDir, "openwiki-qa", repo.ID)
	_ = os.MkdirAll(filepath.Dir(socketPath), 0o755)
	_ = os.Remove(socketPath) // Clean up stale socket if any
```

And in `getOrStartProjectWorker` (`deploy/unit1-orchestrator/internal/qa/manager.go`):
```go
	if m.transport != nil {
		wp := &WorkerProcess{
			RepoID:     workerKey,
			SocketPath: BuildSocketPath(m.socketDir, "mock-proj", proj.ID),
			CreatedAt:  time.Now(),
		}
		m.workers[workerKey] = wp
		return wp, nil
	}

	if proj.LocalPath != "" {
		if _, err := os.Stat(proj.LocalPath); os.IsNotExist(err) {
			_ = os.MkdirAll(proj.LocalPath, 0o755)
		}
	}

	socketPath := BuildSocketPath(m.socketDir, "openwiki-qa-proj", proj.ID)
	_ = os.MkdirAll(filepath.Dir(socketPath), 0o755)
	_ = os.Remove(socketPath)
```

- [x] **Step 4: Run test to verify it passes**

Run:
```bash
cd /Users/bigc/openwiki/deploy/unit1-orchestrator && go test -v ./internal/qa
```
Expected: PASS (`TestManagerWorkerLifecycle` and `TestBuildSocketPathAndMultiSegmentRepoID`).

- [x] **Step 5: Commit**

```bash
git add deploy/unit1-orchestrator/internal/qa/manager.go deploy/unit1-orchestrator/internal/qa/manager_test.go
git commit -m "fix(qa): sanitize multi-segment repo IDs and cap length in UDS socket path"
```

---

### Task 2: Defensive Parent Directory Creation & Error Logging in `qa-daemon.js`

**Files:**
- Modify: `deploy/unit1-orchestrator/scripts/qa-daemon.js:398-413`

- [x] **Step 1: Update `deploy/unit1-orchestrator/scripts/qa-daemon.js` to create parent directories and log server listen errors**

In `deploy/unit1-orchestrator/scripts/qa-daemon.js` (lines 398-413), replace the socket cleanup and `server.listen` block with:

```javascript
// Ensure socket parent directory exists and remove existing stale socket file if present
try {
  const socketDir = path.dirname(socketPath);
  if (socketDir && !fs.existsSync(socketDir)) {
    fs.mkdirSync(socketDir, { recursive: true });
  }
  if (fs.existsSync(socketPath)) {
    fs.unlinkSync(socketPath);
  }
} catch (err) {
  console.warn(`[QA-Daemon] Notice: could not prepare socket path ${socketPath}:`, err.message);
}

// Handle signals
process.on('SIGINT', () => cleanupAndExit(0));
process.on('SIGTERM', () => cleanupAndExit(0));

server.on('error', (err) => {
  console.error(`[QA-Daemon] Server error on UDS ${socketPath}:`, err);
  process.exit(1);
});

server.listen(socketPath, () => {
  console.log(`[QA-Daemon] Worker listening on UDS ${socketPath} (Repo: ${repoDir}, TTL: ${ttlSeconds}s)`);
});
```

- [x] **Step 2: Validate `qa-daemon.js` syntax**

Run:
```bash
node --check /Users/bigc/openwiki/deploy/unit1-orchestrator/scripts/qa-daemon.js
```
Expected: Exit code `0` with no syntax errors.

- [x] **Step 3: Commit**

```bash
git add deploy/unit1-orchestrator/scripts/qa-daemon.js
git commit -m "fix(qa-daemon): ensure UDS parent directory exists before server.listen"
```

---

### Task 3: Multi-Segment Repo ID Build Log Directory Creation

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go:294-301`
- Test: `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go`

- [x] **Step 1: Write the failing test in `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go`**

Add `WriteBuildLogFile` test `TestWriteBuildLogFileMultiSegmentRepoID` to `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go`:

```go
func TestWriteBuildLogFileMultiSegmentRepoID(t *testing.T) {
	tmpDir := t.TempDir()
	repoID := "bxbank/DEO/xone-auth"
	logContent := "=== BUILD SUCCESSFUL ===\n"

	logFilePath, err := scheduler.WriteBuildLogFile(tmpDir, repoID, logContent)
	if err != nil {
		t.Fatalf("WriteBuildLogFile failed for multi-segment repoID %q: %v", repoID, err)
	}

	data, err := os.ReadFile(logFilePath)
	if err != nil {
		t.Fatalf("failed to read written build log at %q: %v", logFilePath, err)
	}
	if string(data) != logContent {
		t.Fatalf("expected log content %q, got %q", logContent, string(data))
	}
}
```
(Also add `"os"` to the imports in `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go`.)

- [x] **Step 2: Run test to verify it fails**

Run:
```bash
cd /Users/bigc/openwiki/deploy/unit1-orchestrator && go test -v ./internal/scheduler -run TestWriteBuildLogFileMultiSegmentRepoID
```
Expected: FAIL with `undefined: scheduler.WriteBuildLogFile`.

- [x] **Step 3: Implement `WriteBuildLogFile` in `deploy/unit1-orchestrator/internal/scheduler/pipeline.go`**

In `deploy/unit1-orchestrator/internal/scheduler/pipeline.go`, add `WriteBuildLogFile` and call it at line 295:

```go
// WriteBuildLogFile persists a repository build log to <logBaseDir>/builds/general/<repoID>.log,
// creating any intermediate subdirectories required when repoID contains slashes (e.g. "bxbank/DEO/xone-auth").
func WriteBuildLogFile(logBaseDir, repoID, fullLog string) (string, error) {
	buildDir := filepath.Join(logBaseDir, "builds", "general")
	logFilePath := filepath.Join(buildDir, fmt.Sprintf("%s.log", repoID))
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(logFilePath, []byte(fullLog), 0644); err != nil {
		return "", err
	}
	return logFilePath, nil
}
```

And replace lines 295-300 in `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` with:

```go
	// Persist build log to disk_log file
	if p.logBaseDir != "" {
		_, _ = WriteBuildLogFile(p.logBaseDir, repo.ID, fullLog)
	}
```

- [x] **Step 4: Run test to verify it passes**

Run:
```bash
cd /Users/bigc/openwiki/deploy/unit1-orchestrator && go test -v ./internal/scheduler -run TestWriteBuildLogFileMultiSegmentRepoID
```
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/pipeline.go deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go
git commit -m "fix(scheduler): create parent directories when writing build logs for multi-segment repo IDs"
```

---

### Task 4: Multi-Segment Repo ID Referer Extraction in Portal `/api/graph`

**Files:**
- Modify: `deploy/unit2-portal/internal/api/router.go:128-165`
- Test: `deploy/unit2-portal/internal/api/rbac_test.go`

- [x] **Step 1: Write the failing test in `deploy/unit2-portal/internal/api/rbac_test.go`**

Append `TestHandleApiGraphMultiSegmentRepoID` to `deploy/unit2-portal/internal/api/rbac_test.go`:

```go
func TestHandleApiGraphMultiSegmentRepoID(t *testing.T) {
	server, cleanup := setupTestPortalServer(t)
	defer cleanup()

	staticDir, err := os.MkdirTemp("", "portal_static_graph_*")
	if err != nil {
		t.Fatalf("failed to create static temp dir: %v", err)
	}
	defer os.RemoveAll(staticDir)

	server.config.StaticOutputDir = staticDir

	repoID := "bxbank/DEO/xone-auth"
	graphDir := filepath.Join(staticDir, repoID, "api")
	if err := os.MkdirAll(graphDir, 0755); err != nil {
		t.Fatalf("failed to create graph dir: %v", err)
	}
	expectedGraph := `{"nodes":[{"id":"quickstart"}]}`
	if err := os.WriteFile(filepath.Join(graphDir, "graph"), []byte(expectedGraph), 0644); err != nil {
		t.Fatalf("failed to write graph file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/graph", nil)
	req.Header.Set("Referer", "http://127.0.0.1:8080/wiki/bxbank/DEO/xone-auth/index.html")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/graph with multi-segment Referer, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != expectedGraph {
		t.Fatalf("expected graph body %s, got %s", expectedGraph, rec.Body.String())
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run:
```bash
cd /Users/bigc/openwiki/deploy/unit2-portal && go test -v ./internal/api -run TestHandleApiGraphMultiSegmentRepoID
```
Expected: FAIL with `expected 200 OK from /api/graph with multi-segment Referer, got 500: Failed to read graph file` (because `parts[1]` extracted `"bxbank"` instead of `"bxbank/DEO/xone-auth"`).

- [x] **Step 3: Update Referer parsing in `deploy/unit2-portal/internal/api/router.go`**

In `deploy/unit2-portal/internal/api/router.go` (lines 134-144), update `handleApiGraph` Referer extraction to support multi-segment repo paths:

```go
	repoID := r.URL.Query().Get("repo")
	if repoID == "" {
		if ref := r.Referer(); ref != "" {
			if u, err := url.Parse(ref); err == nil {
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) >= 2 && parts[0] == "wiki" {
					subParts := parts[1:]
					if len(subParts) > 1 && strings.HasSuffix(subParts[len(subParts)-1], ".html") {
						subParts = subParts[:len(subParts)-1]
					}
					repoID = strings.Join(subParts, "/")
				}
			}
		}
	}
```

- [x] **Step 4: Run test to verify it passes**

Run:
```bash
cd /Users/bigc/openwiki/deploy/unit2-portal && go test -v ./internal/api -run TestHandleApiGraphMultiSegmentRepoID
```
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add deploy/unit2-portal/internal/api/router.go deploy/unit2-portal/internal/api/rbac_test.go
git commit -m "fix(portal): support multi-segment repo IDs in /api/graph Referer extraction"
```

