# Recent Commits Audit Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all P0, P1, and P2 issues uncovered in the 11-commit audit: SQL build status filtering, production-safe DSN resolution, completeness-check caching & duplicate-call removal, `cron.go` parity, ARM version ldflags, and Git artifact hygiene.

**Architecture:** Each fix is isolated to its owning subsystem with TDD coverage first. Completeness caching lives in a dedicated `completeness_cache.go` file inside `internal/scheduler` using `sync.RWMutex` keyed by path + file `ModTime`/`Size` (for `api/graph`) and short TTL (for wiki directory checks), invalidated automatically when builds finish. Note: `deploy/nginx.conf` (`3000`/`8080`) was verified to match `deploy/Dockerfile` (`EXPOSE 80 8080 3000`), while `deploy/scripts/ecs-deploy.sh` uses `deploy/nginx-standalone.conf` (`3009`/`8099`), so `deploy/nginx.conf` requires no revert.

**Tech Stack:** Go (`database/sql`, `testing`, `sync`, `encoding/json`), Bash (`build-arm.sh`, `test-unit.sh`), Git.

---

## File Structure Map

| File | Responsibility |
| --- | --- |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | Fix `GetLatestFinishedBuild` status filter; guard `resolveDSN` to test-only mode and preserve `TEST_DB_DSN` credentials/host |
| `deploy/unit1-orchestrator/internal/db/sqlite_test.go` | Test `'success'`/`'interrupted'` in `GetLatestFinishedBuild` and `buildTargetTestDSN` |
| `deploy/unit2-portal/internal/db/sqlite.go` | Mirror `resolveDSN` test-only guard and `buildTargetTestDSN` fix |
| `deploy/unit1-orchestrator/internal/scheduler/completeness_cache.go` | New focused module: thread-safe cache for `IsStaticDirComplete` (mtime+size validated) and `IsWikiDirComplete` (TTL + explicit invalidation) |
| `deploy/unit1-orchestrator/internal/scheduler/builder.go` | Wire completeness cache into `IsStaticDirComplete` / `IsWikiDirComplete` and invalidate on build/export |
| `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` | Unit tests for cache hit, mtime/size change invalidation, and explicit invalidation |
| `deploy/unit1-orchestrator/internal/mcp/server.go` | Remove redundant second `IsStaticDirComplete` call in `isProjectWikiReady` |
| `deploy/unit1-orchestrator/internal/api/projects.go` | Remove redundant second `IsStaticDirComplete` call in `deriveProjectTaskStatus` |
| `deploy/unit1-orchestrator/internal/scheduler/cron.go` | Align legacy `!status.HasUpdates` skip guard with `pipeline.go` |
| `deploy/scripts/build-arm.sh` | Inject `Version`, `GitCommit`, `BuildTime` via `-ldflags` matching `build-x86.sh` |
| `.gitignore` | Ignore stray compiled binaries, release tarballs, and nested `deploy/deploy/` |

---

### Task 1: Fix `GetLatestFinishedBuild` SQL Status Filter

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go:771-780`
- Test: `deploy/unit1-orchestrator/internal/db/sqlite_test.go:558-601`

- [ ] **Step 1: Add failing test cases for `'success'` and `'interrupted'` in `TestGetLatestFinishedBuild`**

Edit `deploy/unit1-orchestrator/internal/db/sqlite_test.go` at the end of `TestGetLatestFinishedBuild`:

```go
	// 5. 第二次构建完成为 completed
	_ = database.FinishBuild(b2ID, "completed", "success log", "")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "completed" || latest.ID != b2ID {
		t.Fatalf("expected latest finished build to be completed, got %+v", latest)
	}

	// 6. 第三次构建完成为 legacy 'success'（cron.go 写入）
	b3ID, _ := database.CreateBuild(repoID, "head-3", "running")
	_ = database.FinishBuild(b3ID, "success", "cron success log", "")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "success" || latest.ID != b3ID {
		t.Fatalf("expected latest finished build to be success b3 (%d), got %+v", b3ID, latest)
	}

	// 7. 第四次构建被中断为 'interrupted'，不应回退到更早的 success
	b4ID, _ := database.CreateBuild(repoID, "head-4", "running")
	_ = database.FinishBuild(b4ID, "interrupted", "killed", "process restarted")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "interrupted" || latest.ID != b4ID {
		t.Fatalf("expected latest finished build to be interrupted b4 (%d), got %+v", b4ID, latest)
	}
```

- [ ] **Step 2: Run test to verify current behavior**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/db -run TestGetLatestFinishedBuild`
Expected: FAIL on step 6 (`success`) if MySQL is running, or SKIP if local MySQL is stopped.

- [ ] **Step 3: Update SQL `IN` clause in `GetLatestFinishedBuild`**

Edit `deploy/unit1-orchestrator/internal/db/sqlite.go:771-779`:

```go
// GetLatestFinishedBuild returns the most recent build for a repo that has reached a terminal state
// (completed, success, failed, skipped, or interrupted).
func (db *DB) GetLatestFinishedBuild(repoID string) (*Build, error) {
	query := `SELECT id, repo_id, COALESCE(git_head,''), status,
		COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(log,''), COALESCE(error,''),
		COALESCE(rate_limited_retries, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0), COALESCE(saved_tokens, 0)
		FROM builds
		WHERE repo_id = ? AND status IN ('completed', 'success', 'failed', 'skipped', 'interrupted')
		ORDER BY id DESC LIMIT 1`
```

- [ ] **Step 4: Run tests to verify compilation and pass**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/db -run TestGetLatestFinishedBuild`
Expected: PASS (or clean SKIP when MySQL is unreachable) with zero compile errors.

- [ ] **Step 5: Commit**

```bash
git add deploy/unit1-orchestrator/internal/db/sqlite.go deploy/unit1-orchestrator/internal/db/sqlite_test.go
git commit -m "fix(db): include success and interrupted statuses in GetLatestFinishedBuild"
```

---

### Task 2: Guard `resolveDSN` to Test-Only Mode and Fix Hardcoded `TEST_DB_DSN`

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go:1-65`
- Modify: `deploy/unit2-portal/internal/db/sqlite.go:1-65`
- Test: `deploy/unit1-orchestrator/internal/db/sqlite_test.go`

- [ ] **Step 1: Write unit test for `buildTargetTestDSN` in `sqlite_test.go`**

Append to `deploy/unit1-orchestrator/internal/db/sqlite_test.go` (does not require a live MySQL server):

```go
func TestBuildTargetTestDSN(t *testing.T) {
	cases := []struct {
		name    string
		baseDSN string
		dbName  string
		want    string
	}{
		{
			name:    "default root no password",
			baseDSN: "root:@tcp(127.0.0.1:3306)/",
			dbName:  "test_orch_1",
			want:    "root:@tcp(127.0.0.1:3306)/test_orch_1?parseTime=true",
		},
		{
			name:    "custom user password host port",
			baseDSN: "admin:s3cr3t@tcp(10.0.0.5:3307)/",
			dbName:  "test_orch_2",
			want:    "admin:s3cr3t@tcp(10.0.0.5:3307)/test_orch_2?parseTime=true",
		},
		{
			name:    "base DSN with existing query params",
			baseDSN: "user:pass@tcp(db.local:3306)/?charset=utf8mb4",
			dbName:  "test_orch_3",
			want:    "user:pass@tcp(db.local:3306)/test_orch_3?charset=utf8mb4&parseTime=true",
		},
		{
			name:    "base DSN already has parseTime",
			baseDSN: "user:pass@tcp(db.local:3306)/?parseTime=true&loc=Local",
			dbName:  "test_orch_4",
			want:    "user:pass@tcp(db.local:3306)/test_orch_4?parseTime=true&loc=Local",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildTargetTestDSN(tc.baseDSN, tc.dbName)
			if got != tc.want {
				t.Fatalf("buildTargetTestDSN(%q, %q) = %q, want %q", tc.baseDSN, tc.dbName, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails (`undefined: buildTargetTestDSN`)**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/db -run TestBuildTargetTestDSN`
Expected: FAIL with `undefined: buildTargetTestDSN`.

- [ ] **Step 3: Implement `buildTargetTestDSN` and test-only guard in Unit 1 `sqlite.go`**

In `deploy/unit1-orchestrator/internal/db/sqlite.go`, import `"flag"` and update `resolveDSN`:

```go
func isRunningUnderGoTest() bool {
	return flag.Lookup("test.v") != nil
}

func buildTargetTestDSN(baseDSN, dbName string) string {
	prefix := baseDSN
	query := ""
	if idx := strings.Index(baseDSN, "?"); idx >= 0 {
		prefix = baseDSN[:idx]
		query = baseDSN[idx+1:]
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if query == "" {
		return prefix + dbName + "?parseTime=true"
	}
	if strings.Contains(query, "parseTime=") {
		return prefix + dbName + "?" + query
	}
	return prefix + dbName + "?" + query + "&parseTime=true"
}

func resolveDSN(dsn string) (string, string, error) {
	if strings.Contains(dsn, "@") {
		return dsn, "", nil
	}
	if !isRunningUnderGoTest() {
		return "", "", fmt.Errorf("invalid mysql dsn (missing '@' host specification): %q", dsn)
	}

	testMu.Lock()
	defer testMu.Unlock()

	baseDSN := os.Getenv("TEST_DB_DSN")
	if baseDSN == "" {
		baseDSN = "root:@tcp(127.0.0.1:3306)/"
	}

	dbName, exists := testDBMap[dsn]
	if !exists {
		dbName = fmt.Sprintf("test_orch_%d_%d", time.Now().UnixNano(), rand.Intn(1000000))
		adminDB, err := sql.Open("mysql", baseDSN)
		if err != nil {
			return "", "", fmt.Errorf("connect mysql base: %w", err)
		}
		if err := adminDB.Ping(); err != nil {
			adminDB.Close()
			return "", "", fmt.Errorf("ping mysql base: %w", err)
		}
		if _, err := adminDB.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
			adminDB.Close()
			return "", "", fmt.Errorf("create test db %s: %w", dbName, err)
		}
		adminDB.Close()
		testDBMap[dsn] = dbName
	}
	testDBRefs[dbName]++

	targetDSN := buildTargetTestDSN(baseDSN, dbName)
	return targetDSN, dbName, nil
}
```

- [ ] **Step 4: Mirror the exact same `isRunningUnderGoTest` and `buildTargetTestDSN` fix in Unit 2 `deploy/unit2-portal/internal/db/sqlite.go`**

In `deploy/unit2-portal/internal/db/sqlite.go`, import `"flag"`, add `isRunningUnderGoTest()` and `buildTargetTestDSN(baseDSN, dbName)`, guard `resolveDSN` with `if !isRunningUnderGoTest()`, and replace the hardcoded `targetDSN := fmt.Sprintf("root:@tcp(127.0.0.1:3306)/%s?parseTime=true", dbName)` with `targetDSN := buildTargetTestDSN(baseDSN, dbName)`.

- [ ] **Step 5: Run tests in both Unit 1 and Unit 2**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/db -run TestBuildTargetTestDSN && cd ../unit2-portal && go test -v ./internal/db`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add deploy/unit1-orchestrator/internal/db/sqlite.go deploy/unit1-orchestrator/internal/db/sqlite_test.go deploy/unit2-portal/internal/db/sqlite.go
git commit -m "fix(db): guard test-only resolveDSN in production and honor TEST_DB_DSN"
```

---

### Task 3: Add Completeness Cache & Remove Redundant Double Calls

**Files:**
- Create: `deploy/unit1-orchestrator/internal/scheduler/completeness_cache.go`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go:880-971`
- Modify: `deploy/unit1-orchestrator/internal/mcp/server.go:390-415`
- Modify: `deploy/unit1-orchestrator/internal/api/projects.go:74-90`
- Test: `deploy/unit1-orchestrator/internal/scheduler/builder_test.go`

- [ ] **Step 1: Write unit tests for completeness caching & invalidation in `builder_test.go`**

Append to `deploy/unit1-orchestrator/internal/scheduler/builder_test.go`:

```go
func TestCompletenessCache_StaticAndWikiInvalidation(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "static-cache-test")
	_ = os.MkdirAll(filepath.Join(staticDir, "vendor"), 0755)
	_ = os.MkdirAll(filepath.Join(staticDir, "api"), 0755)

	for _, rel := range requiredStaticFiles {
		_ = os.WriteFile(filepath.Join(staticDir, rel), []byte("non-empty"), 0644)
	}
	graphPath := filepath.Join(staticDir, "api", "graph")
	_ = os.WriteFile(graphPath, []byte(`{"nodes":[{"id":"n1"}],"links":[]}`), 0644)

	InvalidateCompletenessCache(staticDir, "")
	if !IsStaticDirComplete(staticDir) {
		t.Fatal("expected IsStaticDirComplete=true on valid static dir")
	}

	// Corrupt api/graph with different size -> cache must detect size/mtime change and return false
	_ = os.WriteFile(graphPath, []byte(`{"bad`), 0644)
	if IsStaticDirComplete(staticDir) {
		t.Fatal("expected IsStaticDirComplete=false immediately after api/graph is corrupted")
	}

	// Restore valid graph -> returns true again
	_ = os.WriteFile(graphPath, []byte(`{"nodes":[{"id":"n1"},{"id":"n2"}],"links":[]}`), 0644)
	if !IsStaticDirComplete(staticDir) {
		t.Fatal("expected IsStaticDirComplete=true after restoring valid api/graph")
	}

	// Wiki cache test: create valid wiki dir, verify true, delete .last-update.json, invalidate, verify false
	wikiDir := filepath.Join(tmp, "wiki-cache-test")
	_ = os.MkdirAll(wikiDir, 0755)
	marker := filepath.Join(wikiDir, ".last-update.json")
	_ = os.WriteFile(marker, []byte(`{"updatedAt":"2026-11-19T00:00:00Z"}`), 0644)
	InvalidateCompletenessCache("", wikiDir)
	if !IsWikiDirComplete(wikiDir) {
		t.Fatal("expected IsWikiDirComplete=true when marker exists")
	}
	_ = os.Remove(marker)
	InvalidateCompletenessCache("", wikiDir)
	if IsWikiDirComplete(wikiDir) {
		t.Fatal("expected IsWikiDirComplete=false after marker removed and cache invalidated")
	}
}
```

- [ ] **Step 2: Run test to verify it fails (`undefined: InvalidateCompletenessCache`)**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run TestCompletenessCache_StaticAndWikiInvalidation`
Expected: FAIL with `undefined: InvalidateCompletenessCache`.

- [ ] **Step 3: Create `deploy/unit1-orchestrator/internal/scheduler/completeness_cache.go`**

```go
package scheduler

import (
	"sync"
	"time"
)

const wikiCompletenessCacheTTL = 30 * time.Second

type staticCacheEntry struct {
	graphModTime int64
	graphSize    int64
	complete     bool
}

type wikiCacheEntry struct {
	checkedAt time.Time
	complete  bool
}

var (
	completenessMu   sync.RWMutex
	staticCheckCache = make(map[string]staticCacheEntry)
	wikiCheckCache   = make(map[string]wikiCacheEntry)
)

func getCachedStaticComplete(staticDir string, graphModTime, graphSize int64) (bool, bool) {
	completenessMu.RLock()
	entry, ok := staticCheckCache[staticDir]
	completenessMu.RUnlock()
	if ok && entry.graphModTime == graphModTime && entry.graphSize == graphSize {
		return entry.complete, true
	}
	return false, false
}

func setCachedStaticComplete(staticDir string, graphModTime, graphSize int64, complete bool) {
	completenessMu.Lock()
	staticCheckCache[staticDir] = staticCacheEntry{
		graphModTime: graphModTime,
		graphSize:    graphSize,
		complete:     complete,
	}
	completenessMu.Unlock()
}

func getCachedWikiComplete(wikiDir string) (bool, bool) {
	completenessMu.RLock()
	entry, ok := wikiCheckCache[wikiDir]
	completenessMu.RUnlock()
	if ok && time.Since(entry.checkedAt) < wikiCompletenessCacheTTL {
		return entry.complete, true
	}
	return false, false
}

func setCachedWikiComplete(wikiDir string, complete bool) {
	completenessMu.Lock()
	wikiCheckCache[wikiDir] = wikiCacheEntry{
		checkedAt: time.Now(),
		complete:  complete,
	}
	completenessMu.Unlock()
}

// InvalidateCompletenessCache clears cached completeness results for the given static and/or wiki directories.
// Pass empty string to skip invalidating that dimension.
func InvalidateCompletenessCache(staticDir, wikiDir string) {
	completenessMu.Lock()
	if staticDir != "" {
		delete(staticCheckCache, staticDir)
	}
	if wikiDir != "" {
		delete(wikiCheckCache, wikiDir)
	}
	completenessMu.Unlock()
}
```

- [ ] **Step 4: Wire cache into `IsStaticDirComplete`, `IsWikiDirComplete`, `BuildRepoWithModeCtx`, and `ExportActiveGraph` in `builder.go`**

In `deploy/unit1-orchestrator/internal/scheduler/builder.go`:
1. In `IsStaticDirComplete(staticDir string) bool`:
   - Stat all `requiredStaticFiles` (recording `graphInfo` when `rel == "api/graph"`). If any file is missing/dir/empty, call `InvalidateCompletenessCache(staticDir, "")` and return `false`.
   - Check `if cached, ok := getCachedStaticComplete(staticDir, graphInfo.ModTime().UnixNano(), graphInfo.Size()); ok { return cached }`.
   - On cache miss, read and unmarshal `api/graph`, call `setCachedStaticComplete(staticDir, graphInfo.ModTime().UnixNano(), graphInfo.Size(), valid)`, and return `valid`.
2. In `IsWikiDirComplete(wikiDir string) bool`:
   - Check `if cached, ok := getCachedWikiComplete(wikiDir); ok { return cached }`.
   - Compute completeness via `.last-update.json` stat / `filepath.Walk`, store with `setCachedWikiComplete(wikiDir, result)`, and return `result`.
3. In `BuildRepoWithModeCtx` (via `defer InvalidateCompletenessCache(filepath.Join(b.StaticOutputDir, repoID), wikiDir)`) and `ExportActiveGraph` (via `defer InvalidateCompletenessCache(staticDir, wikiDir)`): invalidate cache whenever build or graph export completes.

- [ ] **Step 5: Fix redundant double `IsStaticDirComplete` calls in `mcp/server.go` and `api/projects.go`**

In `deploy/unit1-orchestrator/internal/mcp/server.go:395-404`:

```go
			staticComplete := true
			if len(staticBaseDir) > 0 && staticBaseDir[0] != "" {
				projStaticDir := filepath.Join(staticBaseDir[0], "projects", p.ID)
				rootStaticDir := filepath.Join(staticBaseDir[0], p.ID)
				staticComplete = scheduler.IsStaticDirComplete(projStaticDir) || scheduler.IsStaticDirComplete(rootStaticDir)
			}
```

In `deploy/unit1-orchestrator/internal/api/projects.go:77-85`:

```go
			staticComplete := true
			if len(staticBaseDir) > 0 && staticBaseDir[0] != "" {
				projStaticDir := filepath.Join(staticBaseDir[0], "projects", proj.ID)
				rootStaticDir := filepath.Join(staticBaseDir[0], proj.ID)
				staticComplete = scheduler.IsStaticDirComplete(projStaticDir) || scheduler.IsStaticDirComplete(rootStaticDir)
			}
```

- [ ] **Step 6: Run scheduler, mcp, and api tests**

Run: `cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run "TestCompletenessCache|TestIsStatic|TestIsWiki" && go test -v ./internal/mcp`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/completeness_cache.go deploy/unit1-orchestrator/internal/scheduler/builder.go deploy/unit1-orchestrator/internal/scheduler/builder_test.go deploy/unit1-orchestrator/internal/mcp/server.go deploy/unit1-orchestrator/internal/api/projects.go
git commit -m "perf(scheduler): cache static/wiki completeness checks and remove redundant calls"
```

---

### Task 4: Align `cron.go` Skip Decision with `pipeline.go`

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/cron.go:272-286`

- [ ] **Step 1: Update `!status.HasUpdates` branch in `deploy/unit1-orchestrator/internal/scheduler/cron.go`**

Replace lines 272-285 of `deploy/unit1-orchestrator/internal/scheduler/cron.go`:

```go
		if !status.HasUpdates {
			var prevBuildSuccess bool
			if s.db != nil {
				if latest, err := s.db.GetLatestFinishedBuild(repo.ID); err == nil && latest != nil {
					prevBuildSuccess = (latest.Status == "completed" || latest.Status == "success" || latest.Status == "skipped")
				}
			}
			wikiComplete := s.builder.IsWikiComplete(repo.WikiDir)
			staticComplete := s.builder.IsStaticOutputComplete(repo.ID)
			if prevBuildSuccess && wikiComplete && staticComplete {
				logBuf.WriteString(fmt.Sprintf("No updates detected (HEAD=%s) and build output is complete.\n", status.LocalHead))
				log.Printf("[build][%s] no updates (HEAD=%s), skipping", repo.ID, status.LocalHead)
				s.finishBuild(buildID, "skipped", logBuf.String(), "")
				return
			}
			regenMsg := fmt.Sprintf("No git updates (HEAD=%s), but prior build/output is incomplete (prev_ok=%v, wiki_ok=%v, static_ok=%v); skipping git pull and regenerating.\n",
				status.LocalHead, prevBuildSuccess, wikiComplete, staticComplete)
			logBuf.WriteString(regenMsg)
			log.Printf("[build][%s] %s", repo.ID, strings.TrimSpace(regenMsg))
			_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
		} else {
			if err := GitPull(repo.LocalPath, repo.Branch); err != nil {
				errStr := fmt.Sprintf("git pull failed: %v", err)
				log.Printf("[build][%s] %s", repo.ID, errStr)
				s.finishBuild(buildID, "failed", logBuf.String(), errStr)
				return
			}
			logBuf.WriteString("Git pull completed successfully.\n")
			_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
		}
```

- [ ] **Step 2: Run scheduler package tests & compile check**

Run: `cd deploy/unit1-orchestrator && go test ./internal/scheduler`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/cron.go
git commit -m "fix(scheduler): align cron fallback skip guard with pipeline completeness checks"
```

---

### Task 5: Add Version LDFLAGS Injection to `build-arm.sh`

**Files:**
- Modify: `deploy/scripts/build-arm.sh:34-49`

- [ ] **Step 1: Update `deploy/scripts/build-arm.sh` to inject version metadata**

Replace lines 34-48 in `deploy/scripts/build-arm.sh`:

```bash
echo "构建目标架构: OS=$GOOS, ARCH=$GOARCH, CGO_ENABLED=$CGO_ENABLED"

# 提取版本信息 (git describe + commit + 构建时间)
GIT_VERSION=$(cd "$ROOT_DIR" && git describe --tags --always --long 2>/dev/null | sed 's/-[0-9]*-g/-/g' || echo "unknown")
GIT_COMMIT=$(cd "$ROOT_DIR" && git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-s -w -X github.com/openwiki/orchestrator/internal/version.Version=${GIT_VERSION} -X github.com/openwiki/orchestrator/internal/version.GitCommit=${GIT_COMMIT} -X github.com/openwiki/orchestrator/internal/version.BuildTime=${BUILD_TIME} -X github.com/openwiki/portal/internal/version.Version=${GIT_VERSION} -X github.com/openwiki/portal/internal/version.GitCommit=${GIT_COMMIT} -X github.com/openwiki/portal/internal/version.BuildTime=${BUILD_TIME}"
echo "--> 版本: ${GIT_VERSION} (commit: ${GIT_COMMIT}, built: ${BUILD_TIME})"

# 3. 编译 Go 后端二进制
echo "--> 编译 Orchestrator ($GOOS/$GOARCH)..."
cd "$ROOT_DIR/deploy/unit1-orchestrator"
go build -v -ldflags="${LDFLAGS}" -o "$OUTPUT_DIR/orchestrator" .

echo "--> 编译 Portal ($GOOS/$GOARCH)..."
cd "$ROOT_DIR/deploy/unit2-portal"
go build -v -ldflags="${LDFLAGS}" -o "$OUTPUT_DIR/portal" .

echo "--> 编译 openwiki-migrate CLI ($GOOS/$GOARCH)..."
cd "$ROOT_DIR/deploy/unit1-orchestrator/cmd/openwiki-migrate"
go build -v -ldflags="${LDFLAGS}" -o "$OUTPUT_DIR/openwiki-migrate" .
```

- [ ] **Step 2: Verify `build-arm.sh` syntax and dry run**

Run: `bash -n deploy/scripts/build-arm.sh`
Expected: Exit code 0.

- [ ] **Step 3: Commit**

```bash
git add deploy/scripts/build-arm.sh
git commit -m "build: inject version ldflags in build-arm.sh for /api/version parity"
```

---

### Task 6: Repository Hygiene — Untrack Stray Binaries, Tarball, and Nested `deploy/deploy/`

**Files:**
- Modify: `.gitignore`
- Remove from Git tracking: `deploy/bin_orchestrator`, `deploy/bin_portal`, `deploy/openwiki-deploy-package.tar.gz`, `deploy/deploy/`

- [ ] **Step 1: Add build artifact patterns to `.gitignore`**

Append to `.gitignore`:

```gitignore
deploy/bin_*
deploy/openwiki-deploy-package.tar.gz
deploy/deploy/
```

- [ ] **Step 2: Remove stray binaries, tarball, and duplicate `deploy/deploy/` from Git index**

Run:
```bash
git rm -r --cached deploy/bin_orchestrator deploy/bin_portal deploy/openwiki-deploy-package.tar.gz deploy/deploy/
```
Expected: Files removed from Git index while local disk copies remain intact if still needed.

- [ ] **Step 3: Commit**

```bash
git add .gitignore
git commit -m "chore: untrack compiled binaries, tarball, and nested deploy/deploy artifacts"
```

---

### Task 7: Full Verification & Release Package Rebuild

**Files:**
- Verification only (outputs to gitignored `deploy/bin/x86` and `deploy/bin/arm`)

- [ ] **Step 1: Run full unit test suite**

Run: `bash deploy/scripts/test-unit.sh`
Expected: `ALL UNIT TESTS & BUILDS PASSED SUCCESSFULLY!`

- [ ] **Step 2: Rebuild x86_64 and ARM64 release bundles**

Run: `bash deploy/scripts/build-x86.sh && bash deploy/scripts/build-arm.sh`
Expected: Both scripts print `--> 版本: v0.3.1-<commit>` and finish with exit code 0.

- [ ] **Step 3: Verify clean working tree**

Run: `git status`
Expected: `nothing to commit, working tree clean`

