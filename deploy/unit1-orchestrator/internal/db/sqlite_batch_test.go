package db_test

import (
	"path/filepath"
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
)

func TestBatchRunAndDailyTokenUsageCRUD(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_batch_token.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

	// 1. 测试创建带有 Tier、业务域和发版时间的 Repo 并检索
	repo := &db.Repo{
		ID:             "trade-gateway",
		Name:           "交易边缘网关",
		GitURL:         "git@github.com:bank/trade-gateway.git",
		Branch:         "master",
		LocalPath:      filepath.Join(tmpDir, "trade-gateway"),
		Tier:           "A",
		BusinessDomain: "消费金融",
		LastReleaseAt:  "2025-11-20 10:00:00",
		Status:         "active",
	}
	if err := database.CreateRepo(repo); err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	fetched, err := database.GetRepo("trade-gateway")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	if fetched.Tier != "A" || fetched.BusinessDomain != "消费金融" || fetched.LastReleaseAt != "2025-11-20 10:00:00" {
		t.Errorf("Unexpected repo metadata: %+v", fetched)
	}

	repos, err := database.ListRepos()
	if err != nil || len(repos) != 1 {
		t.Fatalf("ListRepos failed: %v, len=%d", err, len(repos))
	}
	if repos[0].Tier != "A" || repos[0].BusinessDomain != "消费金融" {
		t.Errorf("Unexpected repo metadata in ListRepos: %+v", repos[0])
	}

	// 2. 测试创建与更新 batch_runs
	run := &db.BatchRun{
		ID:             "batch-20261029-001",
		Name:           "全行发版仓库首次知识构建",
		TotalCount:     1,
		Status:         "running",
		MaxConcurrency: 2,
		NightModeOnly:  true,
	}
	if err := database.CreateBatchRun(run); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	if err := database.UpdateBatchConcurrency(run.ID, 4); err != nil {
		t.Fatalf("UpdateBatchConcurrency failed: %v", err)
	}

	fetchedRun, err := database.GetBatchRun(run.ID)
	if err != nil {
		t.Fatalf("GetBatchRun failed: %v", err)
	}
	if fetchedRun.MaxConcurrency != 4 {
		t.Errorf("Expected concurrency 4, got %d", fetchedRun.MaxConcurrency)
	}

	// 3. 测试创建与更新 batch_tasks
	task := &db.BatchTask{
		BatchID: run.ID,
		RepoID:  repo.ID,
		Status:  "pending",
	}
	if err := database.CreateBatchTask(task); err != nil {
		t.Fatalf("CreateBatchTask failed: %v", err)
	}

	if err := database.UpdateBatchTaskStatus(run.ID, repo.ID, "completed", "构建成功", 120, 12500, 3200, 15700); err != nil {
		t.Fatalf("UpdateBatchTaskStatus failed: %v", err)
	}

	tasks, err := database.ListBatchTasks(run.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListBatchTasks failed: %v, len=%d", err, len(tasks))
	}
	if tasks[0].Status != "completed" || tasks[0].TotalTokens != 15700 {
		t.Errorf("Unexpected task: %+v", tasks[0])
	}

	// 4. 测试 daily_token_usage 报表累加
	daily := &db.DailyTokenUsage{
		Date:             "2026-10-29",
		PromptTokens:     12500,
		CompletionTokens: 3200,
		TotalTokens:      15700,
		SavedTokens:      25000,
		BuildCount:       1,
		SkippedCount:     1,
	}
	if err := database.RecordDailyTokenUsage(daily); err != nil {
		t.Fatalf("RecordDailyTokenUsage failed: %v", err)
	}

	usages, err := database.ListDailyTokenUsage(7)
	if err != nil || len(usages) != 1 || usages[0].TotalTokens != 15700 {
		t.Fatalf("Unexpected daily token usage: %+v", usages)
	}
}
