package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/api"
	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/qa"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func setupTestLogServer(t *testing.T) (*api.Server, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_logs.db")
	_ = db.InitDB(dbPath)
	database, _ := db.Open(dbPath)

	logDir := filepath.Join(tmpDir, "disk_log")
	_ = os.MkdirAll(logDir, 0755)

	cfg := &config.Config{
		DBPath:          dbPath,
		ProjectsBaseDir: filepath.Join(tmpDir, "projects"),
		StaticOutputDir: filepath.Join(tmpDir, "static"),
		ReposBaseDir:    filepath.Join(tmpDir, "repos"),
		LogBaseDir:      logDir,
		OpenwikiCLI:     "openwiki",
	}

	builder := scheduler.NewBuilder(cfg.OpenwikiCLI, tmpDir, tmpDir, cfg.StaticOutputDir)
	sched := scheduler.New(database, builder)
	qaRunner := qa.NewRunner("openwiki", 0)
	qaPool := qa.NewPool(qaRunner, 2)
	qaManager := qa.NewManager("", tmpDir, 0, "")

	server := api.NewServer(cfg, sched, qaPool, qaManager, nil)
	return server, logDir
}

func TestLogStatsAndCleanup(t *testing.T) {
	server, logDir := setupTestLogServer(t)

	// Create test files
	batchDir := filepath.Join(logDir, "builds", "batch_101")
	_ = os.MkdirAll(batchDir, 0755)
	_ = os.WriteFile(filepath.Join(batchDir, "repo-1.log"), []byte("build log for repo 1"), 0644)
	_ = os.WriteFile(filepath.Join(batchDir, "repo-2.log"), []byte("build log for repo 2"), 0644)

	generalDir := filepath.Join(logDir, "builds", "general")
	_ = os.MkdirAll(generalDir, 0755)

	oldLog := filepath.Join(generalDir, "old.log")
	_ = os.WriteFile(oldLog, []byte("very old log content"), 0644)
	oldTime := time.Now().AddDate(0, 0, -10)
	_ = os.Chtimes(oldLog, oldTime, oldTime)

	newLog := filepath.Join(generalDir, "new.log")
	_ = os.WriteFile(newLog, []byte("fresh recent log content"), 0644)

	// 1. GET /api/logs/stats
	reqStats := httptest.NewRequest(http.MethodGet, "/api/logs/stats", nil)
	wStats := httptest.NewRecorder()
	server.ServeHTTP(wStats, reqStats)

	if wStats.Code != http.StatusOK {
		t.Fatalf("expected 200 on stats, got %d", wStats.Code)
	}

	var statsResp api.LogStatsResponse
	if err := json.NewDecoder(wStats.Body).Decode(&statsResp); err != nil {
		t.Fatalf("decode stats failed: %v", err)
	}
	if statsResp.LogFilesCount != 4 {
		t.Fatalf("expected 4 log files, got %d", statsResp.LogFilesCount)
	}

	// 2. POST /api/logs/cleanup (Purge by batch_id)
	reqCleanBatch := httptest.NewRequest(http.MethodPost, "/api/logs/cleanup", strings.NewReader(`{"batch_id":"batch_101"}`))
	reqCleanBatch.Header.Set("Content-Type", "application/json")
	wCleanBatch := httptest.NewRecorder()
	server.ServeHTTP(wCleanBatch, reqCleanBatch)

	if wCleanBatch.Code != http.StatusOK {
		t.Fatalf("expected 200 on batch cleanup, got %d", wCleanBatch.Code)
	}

	var batchResp api.LogCleanupResponse
	_ = json.NewDecoder(wCleanBatch.Body).Decode(&batchResp)
	if batchResp.DeletedFilesCount != 2 {
		t.Fatalf("expected 2 batch files deleted, got %d", batchResp.DeletedFilesCount)
	}
	if _, err := os.Stat(batchDir); !os.IsNotExist(err) {
		t.Fatalf("expected batch_101 directory removed")
	}

	// 3. POST /api/logs/cleanup (Purge older than 7 days)
	days := 7
	cleanDaysReq := api.LogCleanupRequest{OlderThanDays: &days}
	daysBody, _ := json.Marshal(cleanDaysReq)
	reqCleanDays := httptest.NewRequest(http.MethodPost, "/api/logs/cleanup", strings.NewReader(string(daysBody)))
	reqCleanDays.Header.Set("Content-Type", "application/json")
	wCleanDays := httptest.NewRecorder()
	server.ServeHTTP(wCleanDays, reqCleanDays)

	if wCleanDays.Code != http.StatusOK {
		t.Fatalf("expected 200 on days cleanup, got %d", wCleanDays.Code)
	}

	var daysResp api.LogCleanupResponse
	_ = json.NewDecoder(wCleanDays.Body).Decode(&daysResp)
	if daysResp.DeletedFilesCount != 1 {
		t.Fatalf("expected 1 old log deleted, got %d", daysResp.DeletedFilesCount)
	}

	// Verify old.log is deleted and new.log still exists
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatalf("expected old.log to be deleted")
	}
	if _, err := os.Stat(newLog); err != nil {
		t.Fatalf("expected new.log to still exist, got %v", err)
	}

	// 4. Security test: Path traversal attempt
	reqTraversal := httptest.NewRequest(http.MethodPost, "/api/logs/cleanup", strings.NewReader(`{"batch_id":"../../etc"}`))
	reqTraversal.Header.Set("Content-Type", "application/json")
	wTraversal := httptest.NewRecorder()
	server.ServeHTTP(wTraversal, reqTraversal)

	if wTraversal.Code != http.StatusBadRequest && wTraversal.Code != http.StatusForbidden {
		t.Fatalf("expected path traversal rejected with 400/403, got %d", wTraversal.Code)
	}
}
