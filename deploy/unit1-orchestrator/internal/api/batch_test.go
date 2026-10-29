package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openwiki/orchestrator/internal/api"
	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/qa"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func setupTestServer(t *testing.T) (*api.Server, *db.DB, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_api.db")
	_ = db.InitDB(dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}

	cfg := &config.Config{
		DBPath:          dbPath,
		ProjectsBaseDir: filepath.Join(tmpDir, "projects"),
		StaticOutputDir: filepath.Join(tmpDir, "static"),
		OpenwikiCLI:     "openwiki",
	}

	builder := scheduler.NewBuilder(cfg.OpenwikiCLI, tmpDir, tmpDir, cfg.StaticOutputDir)
	sched := scheduler.New(database, builder)
	qaRunner := qa.NewRunner("openwiki", 0)
	qaPool := qa.NewPool(qaRunner, 2)
	qaManager := qa.NewManager("", tmpDir, 0, "")

	server := api.NewServer(cfg, sched, qaPool, qaManager, nil)
	return server, database, tmpDir
}

func TestBatchImportReposAndVirtualDomainProject(t *testing.T) {
	server, database, tmpDir := setupTestServer(t)
	defer database.Close()

	payload := `{
		"batch_name": "2026生产发版基线导入",
		"concurrency": 3,
		"night_mode_only": false,
		"auto_start": false,
		"repos": [
			{
				"id": "trade-gateway",
				"name": "交易边缘网关",
				"git_url": "git@github.com:bank/trade-gateway.git",
				"branch": "master",
				"tier": "A",
				"business_domain": "消费金融",
				"last_release_at": "2025-10-01 12:00:00"
			},
			{
				"id": "order-service",
				"name": "信贷订单微服务",
				"git_url": "git@github.com:bank/order-service.git",
				"branch": "master",
				"tier": "A",
				"business_domain": "消费金融",
				"last_release_at": "2025-11-01 12:00:00"
			}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/repos/batch-import", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("Expected 200/201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if int(resp["total_imported"].(float64)) != 2 {
		t.Errorf("Expected 2 repos imported, got %v", resp["total_imported"])
	}

	batchID, ok := resp["batch_id"].(string)
	if !ok || batchID == "" {
		t.Fatalf("Expected valid batch_id in response, got %v", resp["batch_id"])
	}

	// 验证 repo schedule 为 manual
	r1, err := database.GetRepo("trade-gateway")
	if err != nil || r1 == nil {
		t.Fatalf("GetRepo trade-gateway failed: %v", err)
	}
	if r1.Schedule != "manual" {
		t.Errorf("Expected schedule manual, got %s", r1.Schedule)
	}

	// 验证自动聚合到消费金融微服务虚拟项目
	projects, err := database.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	foundDomainProject := false
	for _, p := range projects {
		if strings.Contains(p.Name, "消费金融") {
			foundDomainProject = true
			repos, _ := database.GetProjectRepos(p.ID)
			if len(repos) != 2 {
				t.Errorf("Expected 2 repos in domain project, got %d", len(repos))
			}
			break
		}
	}
	if !foundDomainProject {
		t.Errorf("Expected auto-aggregated project for domain 消费金融, found none")
	}

	_ = tmpDir
}

func TestDynamicConcurrencyAndStatusAPI(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	// 先创建一个批跑
	batchID := "batch_concurrency_test"
	batchRun := &db.BatchRun{
		ID:             batchID,
		Name:           "并发动态调整测试",
		TotalCount:     5,
		MaxConcurrency: 2,
		Status:         "pending",
	}
	if err := database.CreateBatchRun(batchRun); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	// 调整并发为 5
	payload := `{"batch_id":"batch_concurrency_test","concurrency":5}`
	req := httptest.NewRequest(http.MethodPut, "/api/batch/concurrency", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// 查看详情状态
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/batch/status?batch_id="+batchID, nil)
	wStatus := httptest.NewRecorder()
	server.ServeHTTP(wStatus, reqStatus)

	if wStatus.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for status, got %d: %s", wStatus.Code, wStatus.Body.String())
	}

	var statusResp map[string]interface{}
	if err := json.Unmarshal(wStatus.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("Unmarshal status response failed: %v", err)
	}

	if int(statusResp["idle_slots_count"].(float64)) != 5 {
		t.Errorf("Expected 5 idle slots after concurrency update, got %v", statusResp["idle_slots_count"])
	}
}

func TestDailyTokenUsageAndFeishuExport(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	// 记录两条每日 Token 统计
	err := database.RecordDailyTokenUsage(&db.DailyTokenUsage{
		Date:             "2026-10-29",
		PromptTokens:     100000,
		CompletionTokens: 25000,
		TotalTokens:      125000,
		SavedTokens:      300000,
		BuildCount:       10,
		SkippedCount:     2,
	})
	if err != nil {
		t.Fatalf("RecordDailyTokenUsage failed: %v", err)
	}

	// 1. 测试 JSON 格式返回
	reqJSON := httptest.NewRequest(http.MethodGet, "/api/telemetry/tokens/daily", nil)
	wJSON := httptest.NewRecorder()
	server.ServeHTTP(wJSON, reqJSON)

	if wJSON.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for daily tokens, got %d: %s", wJSON.Code, wJSON.Body.String())
	}

	var dailyList []db.DailyTokenUsage
	if err := json.Unmarshal(wJSON.Body.Bytes(), &dailyList); err != nil {
		t.Fatalf("Unmarshal daily tokens failed: %v", err)
	}
	if len(dailyList) == 0 || dailyList[0].TotalTokens != 125000 {
		t.Errorf("Expected total tokens 125000, got %v", dailyList)
	}

	// 2. 测试 TSV (飞书多维表格直接粘贴) 导出
	reqTSV := httptest.NewRequest(http.MethodGet, "/api/telemetry/tokens/export?format=tsv", nil)
	wTSV := httptest.NewRecorder()
	server.ServeHTTP(wTSV, reqTSV)

	if wTSV.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for TSV export, got %d", wTSV.Code)
	}
	tsvBody := wTSV.Body.String()
	if !strings.Contains(tsvBody, "Prompt消耗Tokens\tCompletion消耗Tokens\t总消耗Tokens") {
		t.Errorf("TSV header mismatch: %s", tsvBody)
	}
	if !strings.Contains(tsvBody, "100000\t25000\t125000\t300000\t10\t2") {
		t.Errorf("TSV data row mismatch: %s", tsvBody)
	}

	// 3. 测试 CSV 导出
	reqCSV := httptest.NewRequest(http.MethodGet, "/api/telemetry/tokens/export?format=csv", nil)
	wCSV := httptest.NewRecorder()
	server.ServeHTTP(wCSV, reqCSV)

	if wCSV.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for CSV export, got %d", wCSV.Code)
	}
	csvBody := wCSV.Body.String()
	if !strings.Contains(csvBody, "100000,25000,125000,300000,10,2") {
		t.Errorf("CSV data row mismatch: %s", csvBody)
	}
}
