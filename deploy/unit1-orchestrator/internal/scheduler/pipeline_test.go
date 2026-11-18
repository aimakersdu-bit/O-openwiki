package scheduler_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func setupTestDB(t *testing.T) (*db.DB, func()) {
	baseDSN := os.Getenv("TEST_DB_DSN")
	if baseDSN == "" {
		baseDSN = "root:@tcp(127.0.0.1:3306)/"
	}
	dbName := fmt.Sprintf("test_db_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	adminDB, err := sql.Open("mysql", baseDSN)
	if err != nil {
		t.Skipf("skipping test, cannot connect to mysql: %v", err)
		return nil, func() {}
	}
	if err := adminDB.Ping(); err != nil {
		adminDB.Close()
		t.Skipf("skipping test, mysql not reachable: %v", err)
		return nil, func() {}
	}
	if _, err := adminDB.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
		adminDB.Close()
		t.Fatalf("failed to create test db %s: %v", dbName, err)
	}
	adminDB.Close()

	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:3306)/%s?parseTime=true", dbName)
	database, err := db.Open(dsn)
	if err != nil {
		admin, _ := sql.Open("mysql", baseDSN)
		if admin != nil {
			_, _ = admin.Exec("DROP DATABASE `" + dbName + "`")
			admin.Close()
		}
		t.Fatalf("failed to open test db: %v", err)
	}

	cleanup := func() {
		database.Close()
		admin, err := sql.Open("mysql", baseDSN)
		if err == nil {
			_, _ = admin.Exec("DROP DATABASE `" + dbName + "`")
			admin.Close()
		}
	}
	return database, cleanup
}

func TestParseTokenUsage(t *testing.T) {
	sampleLog := `
=== Step 1: openwiki code --update --print --language zh-CN ===
Analyzing repository structure...
Chunk 1 processed.
[OpenWiki] OPENWIKI_USAGE {"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200}
Chunk 2 processed.
[OpenWiki] OPENWIKI_USAGE {"prompt_tokens":2500,"completion_tokens":550,"total_tokens":3050}
Wiki generated successfully.
`
	prompt, completion, total := scheduler.ParseTokenUsage(sampleLog)
	if prompt != 2500 || completion != 550 || total != 3050 {
		t.Fatalf("Expected prompt=2500, completion=550, total=3050, got %d, %d, %d", prompt, completion, total)
	}

	// Empty / no usage marker
	p0, c0, t0 := scheduler.ParseTokenUsage("No marker present")
	if p0 != 0 || c0 != 0 || t0 != 0 {
		t.Fatalf("Expected zeroes for log without usage marker, got %d, %d, %d", p0, c0, t0)
	}
}

func TestFormatETA(t *testing.T) {
	tests := []struct {
		seconds  int
		expected string
	}{
		{0, "0秒"},
		{45, "45秒"},
		{60, "1分钟0秒"},
		{95, "1分钟35秒"},
		{3600, "1小时0分钟0秒"},
		{3665, "1小时1分钟5秒"},
		{7320, "2小时2分钟0秒"},
	}

	for _, tc := range tests {
		got := scheduler.FormatETA(tc.seconds)
		if got != tc.expected {
			t.Errorf("FormatETA(%d) = %q, expected %q", tc.seconds, got, tc.expected)
		}
	}
}

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

func TestPipelineSkipDecision_RetryFailedDoesNotSkip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OPENWIKI_HOME", filepath.Join(tmpDir, ".openwiki"))

	database, cleanupDB := setupTestDB(t)
	defer cleanupDB()

	// 1. Mock OpenWiki script that succeeds and creates .last-update.json
	mockScript := filepath.Join(tmpDir, "mock-openwiki.sh")
	scriptContent := `#!/bin/sh
mkdir -p openwiki
echo '{"updatedAt":"2026-11-18T20:00:00Z"}' > openwiki/.last-update.json
echo '# Documentation Title\nThis is substantial markdown content that exceeds 100 bytes of valid documentation text to pass any validator checks in the pipeline test suite.' > openwiki/doc.md
exit 0
`
	if err := os.WriteFile(mockScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("write mockScript failed: %v", err)
	}

	staticDir := filepath.Join(tmpDir, "static")
	_ = os.MkdirAll(staticDir, 0755)
	builder := scheduler.NewBuilder(mockScript, "", "", staticDir)
	limiter := scheduler.NewDynamicRateLimiter(2)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)

	// 2. Setup bare git repo and clone
	runGitCmd := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@openwiki.local",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@openwiki.local",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s failed: %v\noutput: %s", args, dir, err, string(out))
		}
	}

	bareDir := filepath.Join(tmpDir, "remote.git")
	seedDir := filepath.Join(tmpDir, "seed")
	_ = os.MkdirAll(seedDir, 0755)
	runGitCmd(seedDir, "init", "-b", "main")
	_ = os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("# Hello\n"), 0644)
	runGitCmd(seedDir, "add", ".")
	runGitCmd(seedDir, "commit", "-m", "initial commit")
	runGitCmd(tmpDir, "clone", "--bare", seedDir, bareDir)

	repoDir := filepath.Join(tmpDir, "repo-local")
	runGitCmd(tmpDir, "clone", "--branch", "main", bareDir, repoDir)
	wikiDir := filepath.Join(repoDir, "openwiki")
	_ = os.MkdirAll(wikiDir, 0755)

	repoID := "test-skip-repo"
	repo := &db.Repo{
		ID:        repoID,
		Name:      "Test Skip Repo",
		GitURL:    bareDir,
		Branch:    "main",
		LocalPath: repoDir,
		WikiDir:   wikiDir,
		Status:    "active",
	}
	if err := database.CreateRepo(repo); err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	populateWiki := func() {
		_ = os.MkdirAll(wikiDir, 0755)
		_ = os.WriteFile(filepath.Join(wikiDir, ".last-update.json"), []byte(`{"updatedAt":"2026-11-18T20:00:00Z"}`), 0644)
		_ = os.WriteFile(filepath.Join(wikiDir, "content.md"), []byte("# Content with over 100 bytes of valid documentation markdown here for the test case validation."), 0644)
	}

	populateStatic := func() {
		sDir := filepath.Join(staticDir, repoID)
		_ = os.MkdirAll(filepath.Join(sDir, "vendor"), 0755)
		_ = os.MkdirAll(filepath.Join(sDir, "api"), 0755)
		for _, f := range []string{
			"index.html",
			"client.js",
			"client-lib.js",
			"vendor/force-graph.min.js",
			"vendor/marked.min.js",
			"vendor/dompurify.min.js",
			"vendor/mermaid.min.js",
		} {
			_ = os.WriteFile(filepath.Join(sDir, f), []byte("asset-content"), 0644)
		}
		_ = os.WriteFile(filepath.Join(sDir, "api", "graph"), []byte(`{"nodes":[{"id":"1"}],"links":[]}`), 0644)
	}

	// ------------------------------------------------------------------------
	// Case A: When the latest finished build in DB has status == "failed",
	// even if Git has no updates (!status.HasUpdates == true), the pipeline
	// must NOT return status == "skipped". It should skip git pull (idempotent),
	// but proceed to the build phase.
	// ------------------------------------------------------------------------
	populateWiki()
	populateStatic()

	b1ID, err := database.CreateBuild(repoID, "head-1", "running")
	if err != nil {
		t.Fatalf("CreateBuild failed: %v", err)
	}
	if err := database.FinishBuild(b1ID, "failed", "previous build failed log", "exit status 1"); err != nil {
		t.Fatalf("FinishBuild failed: %v", err)
	}

	resA, err := pipeline.Execute(context.Background(), repo, false, 0, nil)
	if err != nil {
		t.Fatalf("Case A: Execute failed: %v", err)
	}
	if resA.Status == "skipped" {
		t.Fatalf("Case A: expected pipeline NOT to return skipped when previous build failed, got %s", resA.Status)
	}
	if !resA.Success || resA.Status != "completed" {
		t.Fatalf("Case A: expected build to succeed with completed status, got success=%v status=%s error=%s", resA.Success, resA.Status, resA.Error)
	}
	if !strings.Contains(resA.Log, "skipping redundant git pull (idempotent) and proceeding to build phase") {
		t.Fatalf("Case A: expected log to record idempotent git pull skip, got:\n%s", resA.Log)
	}
	if !strings.Contains(resA.Log, `ended with status "failed"`) {
		t.Fatalf("Case A: expected log to mention prior build failed status, got:\n%s", resA.Log)
	}

	// ------------------------------------------------------------------------
	// Case B: When the latest finished build in DB has status == "completed",
	// AND IsWikiComplete(wikiDir) is true, AND IsStaticOutputComplete(repoID) is true,
	// it DOES return status == "skipped".
	// ------------------------------------------------------------------------
	// Note: Case A just finished as "completed". Ensure wiki and static are complete.
	populateWiki()
	populateStatic()

	resB, err := pipeline.Execute(context.Background(), repo, false, 0, nil)
	if err != nil {
		t.Fatalf("Case B: Execute failed: %v", err)
	}
	if resB.Status != "skipped" {
		t.Fatalf("Case B: expected status == 'skipped', got %s (log: %s)", resB.Status, resB.Log)
	}
	if !resB.Success {
		t.Fatalf("Case B: expected Success == true for skipped build")
	}
	if !strings.Contains(resB.Log, "Skipping wiki generation (0 tokens)") {
		t.Fatalf("Case B: expected skip log message, got:\n%s", resB.Log)
	}

	// ------------------------------------------------------------------------
	// Case C: When the latest finished build is completed, but static assets
	// are missing or incomplete (staticComplete == false), it must NOT return
	// status == "skipped"; it should proceed to build phase without re-pulling git.
	// ------------------------------------------------------------------------
	// Remove static output directory to simulate incomplete assets
	_ = os.RemoveAll(filepath.Join(staticDir, repoID))
	if builder.IsStaticOutputComplete(repoID) {
		t.Fatalf("Case C: expected IsStaticOutputComplete to be false after removing static dir")
	}

	resC, err := pipeline.Execute(context.Background(), repo, false, 0, nil)
	if err != nil {
		t.Fatalf("Case C: Execute failed: %v", err)
	}
	if resC.Status == "skipped" {
		t.Fatalf("Case C: expected pipeline NOT to skip when static assets are incomplete")
	}
	if !resC.Success || resC.Status != "completed" {
		t.Fatalf("Case C: expected build to succeed with completed status, got success=%v status=%s", resC.Success, resC.Status)
	}
	if !strings.Contains(resC.Log, "skipping redundant git pull (idempotent) and proceeding to build phase") {
		t.Fatalf("Case C: expected log to record idempotent git pull skip, got:\n%s", resC.Log)
	}
	if !strings.Contains(resC.Log, "static output assets are missing or incomplete") {
		t.Fatalf("Case C: expected log to mention static assets incomplete, got:\n%s", resC.Log)
	}
}
