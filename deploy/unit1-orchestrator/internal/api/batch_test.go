package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
		t.Skipf("skipping test, mysql not reachable: %v", err)
		return nil, nil, ""
	}

	cfg := &config.Config{
		DBPath:          dbPath,
		ProjectsBaseDir: filepath.Join(tmpDir, "projects"),
		StaticOutputDir: filepath.Join(tmpDir, "static"),
		ReposBaseDir:    filepath.Join(tmpDir, "repos"),
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
		BusinessDomain:   "消费金融",
		PromptTokens:     100000,
		CompletionTokens: 25000,
		TotalTokens:      125000,
		SavedTokens:      300000,
		BuildCount:       10,
		SkippedCount:     2,
		ThrottledCount:   3,
		TotalDurationSec: 150,
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

	// 2. 测试 TSV (飞书多维表格直接粘贴) 导出包含 10 个业务字段
	reqTSV := httptest.NewRequest(http.MethodGet, "/api/telemetry/tokens/export?format=tsv", nil)
	wTSV := httptest.NewRecorder()
	server.ServeHTTP(wTSV, reqTSV)

	if wTSV.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for TSV export, got %d", wTSV.Code)
	}
	tsvBody := wTSV.Body.String()
	if !strings.Contains(tsvBody, "日期\t业务域\t构建成功仓数\t增量跳过仓数\t总消耗Token\t输入PromptToken\t补全CompletionToken\t节约Token估算\t模型限流重试次数\t平均单仓耗时") {
		t.Errorf("TSV header mismatch: %s", tsvBody)
	}
	if !strings.Contains(tsvBody, "2026-10-29\t消费金融\t10\t2\t125000\t100000\t25000\t300000\t3\t15.0s") {
		t.Errorf("TSV data row mismatch: %s", tsvBody)
	}

	// 3. 测试 CSV 导出 (带 UTF-8 BOM)
	reqCSV := httptest.NewRequest(http.MethodGet, "/api/telemetry/tokens/export?format=csv", nil)
	wCSV := httptest.NewRecorder()
	server.ServeHTTP(wCSV, reqCSV)

	if wCSV.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for CSV export, got %d", wCSV.Code)
	}
	csvBody := wCSV.Body.String()
	if !strings.HasPrefix(csvBody, "\xEF\xBB\xBF") {
		t.Errorf("CSV missing UTF-8 BOM prefix")
	}
	if !strings.Contains(csvBody, "2026-10-29,消费金融,10,2,125000,100000,25000,300000,3,15.0s") {
		t.Errorf("CSV data row mismatch: %s", csvBody)
	}
}

func TestRecoverInterruptedTasksOnStartup(t *testing.T) {
	_, database, _ := setupTestServer(t)
	defer database.Close()

	batchID := "batch-interrupted"
	_ = database.CreateBatchRun(&db.BatchRun{
		ID:     batchID,
		Name:   "Interrupted Batch",
		Status: "running",
	})
	_ = database.CreateRepo(&db.Repo{
		ID:        "interrupted-repo-1",
		Name:      "Interrupted 1",
		GitURL:    "https://example.com/1.git",
		LocalPath: "/tmp/1",
	})

	_ = database.CreateBatchTask(&db.BatchTask{
		BatchID: batchID,
		RepoID:  "interrupted-repo-1",
		Status:  "running",
	})

	// Verify it is 'running'
	tasks, _ := database.ListBatchTasks(batchID)
	if len(tasks) != 1 || tasks[0].Status != "running" {
		t.Fatalf("expected 1 running task, got %+v", tasks)
	}

	// Trigger recovery
	recovered, err := database.RecoverInterruptedBatchTasks()
	if err != nil {
		t.Fatalf("RecoverInterruptedBatchTasks failed: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 task recovered, got %d", recovered)
	}

	// Verify it became 'pending'
	tasksAfter, _ := database.ListBatchTasks(batchID)
	if len(tasksAfter) != 1 || tasksAfter[0].Status != "pending" {
		t.Fatalf("expected task status 'pending' after recovery, got %s", tasksAfter[0].Status)
	}
}

func TestStorageVolumesAndBatchPlacement(t *testing.T) {
	server, database, tmpDir := setupTestServer(t)
	defer database.Close()

	volDir := filepath.Join(tmpDir, "data_vol1")
	_ = os.MkdirAll(volDir, 0755)

	server.GetVolumeManager().SetStatfsFunc(func(path string) (int64, int64, error) {
		return 1000 * 1024 * 1024 * 1024, 100 * 1024 * 1024 * 1024, nil
	})

	// 1. Register volume via POST /api/storage/volumes
	payload := fmt.Sprintf(`{"id":"vol1","mount_point":%q}`, volDir)
	reqPost := httptest.NewRequest(http.MethodPost, "/api/storage/volumes", strings.NewReader(payload))
	reqPost.Header.Set("Content-Type", "application/json")
	wPost := httptest.NewRecorder()
	server.ServeHTTP(wPost, reqPost)

	if wPost.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wPost.Code, wPost.Body.String())
	}

	// 2. Query volume via GET /api/storage/volumes
	reqGet := httptest.NewRequest(http.MethodGet, "/api/storage/volumes", nil)
	wGet := httptest.NewRecorder()
	server.ServeHTTP(wGet, reqGet)

	var getResp struct {
		Volumes []db.Volume `json:"volumes"`
	}
	_ = json.NewDecoder(wGet.Body).Decode(&getResp)
	if len(getResp.Volumes) != 1 || getResp.Volumes[0].ID != "vol1" {
		t.Fatalf("expected 1 volume 'vol1', got %+v", getResp.Volumes)
	}

	// 3. Import repo and verify placed on vol1
	importPayload := `{
		"batch_name": "多盘放置测试",
		"concurrency": 2,
		"repos": [
			{
				"id": "trade-gateway",
				"name": "交易网关",
				"git_url": "git@github.com:bank/trade-gateway.git",
				"branch": "master",
				"tier": "A",
				"business_domain": "零售业务"
			}
		]
	}`
	reqImport := httptest.NewRequest(http.MethodPost, "/api/repos/batch-import", strings.NewReader(importPayload))
	reqImport.Header.Set("Content-Type", "application/json")
	wImport := httptest.NewRecorder()
	server.ServeHTTP(wImport, reqImport)

	if wImport.Code != http.StatusOK && wImport.Code != http.StatusCreated {
		t.Fatalf("expected 200/201 on batch import, got %d", wImport.Code)
	}

	repo, err := database.GetRepo("trade-gateway")
	if err != nil || repo == nil {
		t.Fatalf("expected repo trade-gateway created, got %v", err)
	}
	if repo.VolumeID != "vol1" {
		t.Fatalf("expected repo placed on vol1, got %s", repo.VolumeID)
	}

	// 4. Update volume mount point via POST
	newVolDir := filepath.Join(tmpDir, "data_vol1_updated")
	_ = os.MkdirAll(newVolDir, 0755)
	updatePayload := fmt.Sprintf(`{"id":"vol1","mount_point":%q}`, newVolDir)
	reqUpdate := httptest.NewRequest(http.MethodPost, "/api/storage/volumes", strings.NewReader(updatePayload))
	reqUpdate.Header.Set("Content-Type", "application/json")
	wUpdate := httptest.NewRecorder()
	server.ServeHTTP(wUpdate, reqUpdate)
	if wUpdate.Code != http.StatusOK {
		t.Fatalf("expected 200 on update, got %d: %s", wUpdate.Code, wUpdate.Body.String())
	}
	vUpdated, err := database.GetVolume("vol1")
	if err != nil || vUpdated == nil || vUpdated.MountPoint != newVolDir {
		t.Fatalf("expected mount_point updated to %s, got %+v (err: %v)", newVolDir, vUpdated, err)
	}

	// 5. Delete volume via DELETE /api/storage/volumes?id=vol1
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/storage/volumes?id=vol1", nil)
	wDel := httptest.NewRecorder()
	server.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", wDel.Code, wDel.Body.String())
	}

	vDeleted, _ := database.GetVolume("vol1")
	if vDeleted != nil {
		t.Fatalf("expected volume vol1 to be deleted, got %+v", vDeleted)
	}

	repoAfterDel, err := database.GetRepo("trade-gateway")
	if err != nil || repoAfterDel == nil {
		t.Fatalf("failed to query repo after volume delete: %v", err)
	}
	if repoAfterDel.VolumeID != "" {
		t.Fatalf("expected repo volume_id reset to empty, got %q", repoAfterDel.VolumeID)
	}
}

func TestMigrationAndRebalanceAPI(t *testing.T) {
	server, database, tmpDir := setupTestServer(t)
	defer database.Close()

	vol1Path := filepath.Join(tmpDir, "vol1")
	vol2Path := filepath.Join(tmpDir, "vol2")
	_ = os.MkdirAll(vol1Path, 0755)
	_ = os.MkdirAll(vol2Path, 0755)

	_, _ = server.GetVolumeManager().RegisterVolume("vol1", vol1Path)
	_, _ = server.GetVolumeManager().RegisterVolume("vol2", vol2Path)

	repoID := "core-banking"
	physicalVol1, symlinkPath, _ := server.GetVolumeManager().CreateRepoPlacement(repoID, "vol1")
	_ = os.WriteFile(filepath.Join(physicalVol1, "main.go"), []byte("package main"), 0644)

	repo := &db.Repo{
		ID:        repoID,
		Name:      "Core Banking",
		VolumeID:  "vol1",
		Status:    "active",
		LocalPath: symlinkPath,
	}
	_ = database.CreateRepo(repo)

	// 1. GET /api/storage/rebalance/plan
	reqPlan := httptest.NewRequest(http.MethodGet, "/api/storage/rebalance/plan", nil)
	wPlan := httptest.NewRecorder()
	server.ServeHTTP(wPlan, reqPlan)
	if wPlan.Code != http.StatusOK {
		t.Fatalf("expected 200 on rebalance plan, got %d", wPlan.Code)
	}

	// 2. POST /api/storage/migrations (migrate core-banking from vol1 to vol2)
	migPayload := fmt.Sprintf(`{"repo_id":%q,"to_volume_id":"vol2"}`, repoID)
	reqMig := httptest.NewRequest(http.MethodPost, "/api/storage/migrations", strings.NewReader(migPayload))
	reqMig.Header.Set("Content-Type", "application/json")
	wMig := httptest.NewRecorder()
	server.ServeHTTP(wMig, reqMig)

	if wMig.Code != http.StatusOK {
		t.Fatalf("expected 200 on migration trigger, got %d: %s", wMig.Code, wMig.Body.String())
	}

	var mig db.VolumeMigration
	_ = json.NewDecoder(wMig.Body).Decode(&mig)
	if mig.Status != "retained" {
		t.Fatalf("expected migration status retained, got %s", mig.Status)
	}

	// 3. POST /api/storage/migrations/{id}/rollback
	reqRollback := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/storage/migrations/%d/rollback", mig.ID), nil)
	wRollback := httptest.NewRecorder()
	server.ServeHTTP(wRollback, reqRollback)

	if wRollback.Code != http.StatusOK {
		t.Fatalf("expected 200 on rollback, got %d: %s", wRollback.Code, wRollback.Body.String())
	}

	r, _ := database.GetRepo(repoID)
	if r.VolumeID != "vol1" {
		t.Fatalf("expected repo volume restored to vol1, got %s", r.VolumeID)
	}
}

func TestBatchImportSemanticTriCodeExtraction(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	payload := `{
		"batch_name": "三字码聚合语义验证批跑",
		"concurrency": 2,
		"night_mode_only": false,
		"auto_start": false,
		"repos": [
			{
				"id": "xone-auth",
				"name": "数字员工鉴权服务",
				"git_url": "https://git.bank.local/bxbank/DEO/xone-auth.git",
				"branch": "master",
				"tier": "A",
				"business_domain": "数字员工",
				"last_release_at": "2025-11-20"
			},
			{
				"id": "xone-task",
				"name": "数字员工调度服务",
				"git_url": "https://git.bank.local/bxbank/DEO/xone-task.git",
				"branch": "master",
				"tier": "A",
				"business_domain": "数字员工",
				"last_release_at": "2025-11-18"
			},
			{
				"id": "credit-core",
				"name": "信贷风控核心",
				"git_url": "https://git.bank.local/org/credit-core.git",
				"branch": "master",
				"tier": "B",
				"business_domain": "CFN-信贷中台",
				"last_release_at": "2025-10-01"
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

	var resp struct {
		TotalImported       int `json:"total_imported"`
		AggregatedProjects []struct {
			ID             string `json:"id"`
			TriCode        string `json:"tri_code"`
			Name           string `json:"name"`
			BusinessDomain string `json:"business_domain"`
		} `json:"aggregated_projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if resp.TotalImported != 3 {
		t.Fatalf("expected 3 imported repos, got %d", resp.TotalImported)
	}
	if len(resp.AggregatedProjects) != 2 {
		t.Fatalf("expected 2 aggregated projects, got %d", len(resp.AggregatedProjects))
	}

	deoProj, err := database.GetProject("proj-deo")
	if err != nil || deoProj == nil {
		t.Fatalf("expected semantic project proj-deo to be created, err=%v", err)
	}
	if deoProj.TriCode != "DEO" || deoProj.Name != "DEO-数字员工微服务集群" || deoProj.BusinessDomain != "数字员工" {
		t.Fatalf("unexpected proj-deo metadata: %+v", deoProj)
	}

	cfnProj, err := database.GetProject("proj-cfn")
	if err != nil || cfnProj == nil {
		t.Fatalf("expected semantic project proj-cfn to be created, err=%v", err)
	}
	if cfnProj.TriCode != "CFN" || cfnProj.Name != "CFN-信贷中台微服务集群" {
		t.Fatalf("unexpected proj-cfn metadata: %+v", cfnProj)
	}
}

func TestBatchRetryFailedAPI(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	batchID := "batch_api_retry"
	if err := database.CreateBatchRun(&db.BatchRun{
		ID:             batchID,
		Name:           "API失败重跑测试",
		TotalCount:     2,
		MaxConcurrency: 1,
		NightModeOnly:  true, // Keep loop parked after re-queue if needed, or cancel at end
		Status:         "completed",
	}); err != nil {
		t.Fatal(err)
	}
	_ = database.CreateBatchTask(&db.BatchTask{BatchID: batchID, RepoID: "repo-ok", Status: "pending"})
	_ = database.CreateBatchTask(&db.BatchTask{BatchID: batchID, RepoID: "repo-bad", Status: "pending"})
	_ = database.UpdateBatchTaskStatus(batchID, "repo-ok", "completed", "ok", 2)
	_ = database.UpdateBatchTaskStatus(batchID, "repo-bad", "failed", "network error", 1)

	// 1. Batch status includes failed_tasks
	wStatus := httptest.NewRecorder()
	server.ServeHTTP(wStatus, httptest.NewRequest(http.MethodGet, "/api/batch/status?batch_id="+batchID, nil))
	if wStatus.Code != http.StatusOK || !strings.Contains(wStatus.Body.String(), `"repo-bad"`) {
		t.Fatalf("expected failed_tasks in batch status, code=%d body=%s", wStatus.Code, wStatus.Body.String())
	}

	// 2. POST /api/batch/retry-failed re-queues repo-bad
	reqRetry := httptest.NewRequest(http.MethodPost, "/api/batch/retry-failed?batch_id="+batchID, strings.NewReader(`{"repo_ids":["repo-bad"]}`))
	reqRetry.Header.Set("Content-Type", "application/json")
	wRetry := httptest.NewRecorder()
	server.ServeHTTP(wRetry, reqRetry)
	if wRetry.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from retry-failed, got %d: %s", wRetry.Code, wRetry.Body.String())
	}
	var retryResp struct {
		Success      bool   `json:"success"`
		RetriedCount int64  `json:"retried_count"`
		BatchID      string `json:"batch_id"`
	}
	if err := json.Unmarshal(wRetry.Body.Bytes(), &retryResp); err != nil {
		t.Fatal(err)
	}
	if !retryResp.Success || retryResp.RetriedCount != 1 || retryResp.BatchID != batchID {
		t.Fatalf("unexpected retry-failed response: %+v", retryResp)
	}

	// Cleanly cancel the parked night-mode batch loop before closing DB
	wCancel := httptest.NewRecorder()
	server.ServeHTTP(wCancel, httptest.NewRequest(http.MethodPost, "/api/batch/cancel?batch_id="+batchID, nil))
	if wCancel.Code != http.StatusOK {
		t.Fatalf("cancel failed: %d: %s", wCancel.Code, wCancel.Body.String())
	}

	// 3. When no failed/pending tasks exist -> 400 Bad Request
	_ = database.UpdateBatchTaskStatus(batchID, "repo-bad", "completed", "ok", 2)
	wEmpty := httptest.NewRecorder()
	server.ServeHTTP(wEmpty, httptest.NewRequest(http.MethodPost, "/api/batch/retry-failed?batch_id="+batchID, nil))
	if wEmpty.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when nothing to retry, got %d: %s", wEmpty.Code, wEmpty.Body.String())
	}
}
