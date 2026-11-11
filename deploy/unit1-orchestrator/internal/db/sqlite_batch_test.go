package db_test

import (
	"fmt"
	"path/filepath"
	"sync"
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

	// 5. 测试 system_settings 与 FinishBuildWithTokens
	val, err := database.GetSetting("max_tpm")
	if err != nil || val != "0" {
		t.Fatalf("Expected default max_tpm 0, got %s, err: %v", val, err)
	}

	if err := database.SetSetting("max_tpm", "150000"); err != nil {
		t.Fatalf("SetSetting failed: %v", err)
	}
	val, _ = database.GetSetting("max_tpm")
	if val != "150000" {
		t.Fatalf("Expected updated max_tpm 150000, got %s", val)
	}

	buildID, err := database.CreateBuild("trade-gateway", "commit-1", "running")
	if err != nil {
		t.Fatalf("CreateBuild failed: %v", err)
	}
	if err := database.FinishBuildWithTokens(buildID, "completed", "success log", "", 100, 200, 300, 50, 2); err != nil {
		t.Fatalf("FinishBuildWithTokens failed: %v", err)
	}

	builds, err := database.ListBuilds("trade-gateway", 1)
	if err != nil || len(builds) != 1 || builds[0].TotalTokens != 300 || builds[0].RateLimitedRetries != 2 {
		t.Fatalf("Unexpected build after FinishBuildWithTokens: %+v", builds)
	}
}

func TestConcurrentBatchWritesAndReadsNoLockErrors(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_concurrent_batch.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

	const numWorkers = 10
	batchID := "batch-concurrent-001"

	if err := database.CreateProject(&db.Project{
		ID:             "proj-concurrent",
		Name:           "并发压测域",
		BusinessDomain: "核心交易",
		LocalPath:      filepath.Join(tmpDir, "proj-concurrent"),
		Status:         "active",
	}); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	if err := database.CreateBatchRun(&db.BatchRun{
		ID:             batchID,
		Name:           "高并发写库压测",
		TotalCount:     numWorkers,
		Status:         "running",
		MaxConcurrency: numWorkers,
	}); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	for i := 0; i < numWorkers; i++ {
		repoID := fmt.Sprintf("svc-%d", i)
		if err := database.CreateRepo(&db.Repo{
			ID:             repoID,
			Name:           repoID,
			GitURL:         fmt.Sprintf("git@gitlab.local:core/%s.git", repoID),
			Branch:         "master",
			LocalPath:      filepath.Join(tmpDir, repoID),
			BusinessDomain: "核心交易",
			Status:         "active",
		}); err != nil {
			t.Fatalf("CreateRepo %s failed: %v", repoID, err)
		}
		if err := database.CreateBatchTask(&db.BatchTask{
			BatchID: batchID,
			RepoID:  repoID,
			Status:  "pending",
		}); err != nil {
			t.Fatalf("CreateBatchTask %s failed: %v", repoID, err)
		}
	}

	errCh := make(chan error, 256)
	var wg sync.WaitGroup

	// Simulate 10 concurrent batch build workers hammering writes & transactions
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			repoID := fmt.Sprintf("svc-%d", idx)
			for round := 0; round < 5; round++ {
				buildID, err := database.CreateBuild(repoID, fmt.Sprintf("head-%d-%d", idx, round), "running")
				if err != nil {
					errCh <- fmt.Errorf("worker %d CreateBuild: %w", idx, err)
					return
				}
				for step := 0; step < 5; step++ {
					if err := database.UpdateBuildLog(buildID, fmt.Sprintf("worker %d round %d step %d", idx, round, step), ""); err != nil {
						errCh <- fmt.Errorf("worker %d UpdateBuildLog: %w", idx, err)
						return
					}
				}
				if err := database.FinishBuildWithTokens(buildID, "completed", "done", "", 100, 50, 150, 0, 0); err != nil {
					errCh <- fmt.Errorf("worker %d FinishBuildWithTokens: %w", idx, err)
					return
				}
				if err := database.RecordDailyTokenUsage(&db.DailyTokenUsage{
					Date:             "2026-11-11",
					BusinessDomain:   "核心交易",
					PromptTokens:     100,
					CompletionTokens: 50,
					TotalTokens:      150,
					BuildCount:       1,
				}); err != nil {
					errCh <- fmt.Errorf("worker %d RecordDailyTokenUsage: %w", idx, err)
					return
				}
				if err := database.UpdateRepoStatus(repoID, "active"); err != nil {
					errCh <- fmt.Errorf("worker %d UpdateRepoStatus: %w", idx, err)
					return
				}
				if err := database.UpdateBatchTaskStatus(batchID, repoID, "completed", "ok", 10, 100, 50, 150); err != nil {
					errCh <- fmt.Errorf("worker %d UpdateBatchTaskStatus: %w", idx, err)
					return
				}
			}
		}(i)
	}

	// Simulate deferred->write transactions (SetProjectRepos) running concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		var mappings []db.ProjectRepo
		for i := 0; i < numWorkers; i++ {
			mappings = append(mappings, db.ProjectRepo{
				ProjectID: "proj-concurrent",
				RepoID:    fmt.Sprintf("svc-%d", i),
				AliasName: fmt.Sprintf("svc-%d", i),
				Role:      "service",
			})
		}
		for round := 0; round < 10; round++ {
			if err := database.SetProjectRepos("proj-concurrent", mappings); err != nil {
				errCh <- fmt.Errorf("SetProjectRepos round %d: %w", round, err)
				return
			}
		}
	}()

	// Simulate 4 concurrent readers polling batch status & repos
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if _, err := database.GetBatchRun(batchID); err != nil {
					errCh <- fmt.Errorf("reader %d GetBatchRun: %w", readerID, err)
					return
				}
				if _, err := database.ListBatchTasks(batchID); err != nil {
					errCh <- fmt.Errorf("reader %d ListBatchTasks: %w", readerID, err)
					return
				}
				if _, err := database.ListRepos(); err != nil {
					errCh <- fmt.Errorf("reader %d ListRepos: %w", readerID, err)
					return
				}
			}
		}(r)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Concurrent DB error: %v", err)
	}

	run, err := database.GetBatchRun(batchID)
	if err != nil {
		t.Fatalf("GetBatchRun failed: %v", err)
	}
	if run.ProcessedCount != numWorkers || run.SuccessCount != numWorkers {
		t.Fatalf("Expected processed=%d success=%d, got processed=%d success=%d",
			numWorkers, numWorkers, run.ProcessedCount, run.SuccessCount)
	}
}
