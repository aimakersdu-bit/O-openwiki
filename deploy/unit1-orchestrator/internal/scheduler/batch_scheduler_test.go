package scheduler_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/llmproxy"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestBatchSchedulerLifecycleAndConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// 准备两个测试 repo
	for i := 1; i <= 2; i++ {
		r := &db.Repo{
			ID:             filepath.Join("repo", string(rune('0'+i))),
			Name:           "Test Repo",
			GitURL:         "git@github.com:bank/test.git",
			Branch:         "master",
			LocalPath:      filepath.Join(tmpDir, "repos", string(rune('0'+i))),
			Tier:           "A",
			BusinessDomain: "消费金融",
			Status:         "active",
		}
		if err := database.CreateRepo(r); err != nil {
			t.Fatalf("CreateRepo failed: %v", err)
		}
	}

	builder := scheduler.NewBuilder("openwiki", tmpDir, tmpDir, tmpDir)
	limiter := scheduler.NewDynamicRateLimiter(2)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)
	sched := scheduler.NewBatchScheduler(database, pipeline, limiter)

	// 创建批跑记录
	batchID := "batch_test_001"
	batchRun := &db.BatchRun{
		ID:             batchID,
		Name:           "测试全行调度批跑",
		TotalCount:     2,
		MaxConcurrency: 2,
		Status:         "pending",
		NightModeOnly:  false,
	}
	if err := database.CreateBatchRun(batchRun); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	// 动态调整并发
	if err := sched.SetConcurrency(batchID, 4); err != nil {
		t.Fatalf("SetConcurrency failed: %v", err)
	}
	if limiter.GetConcurrency() != 4 {
		t.Fatalf("Expected limiter concurrency to be 4, got %d", limiter.GetConcurrency())
	}

	// 查看详情状态
	status, err := sched.GetDetailedStatus(batchID)
	if err != nil {
		t.Fatalf("GetDetailedStatus failed: %v", err)
	}
	if status.BatchRun.ID != batchID {
		t.Fatalf("Expected batch ID %s, got %s", batchID, status.BatchRun.ID)
	}
	if status.IdleSlotsCount != 4 {
		t.Fatalf("Expected 4 idle slots, got %d", status.IdleSlotsCount)
	}

	// 测试状态暂停与恢复
	if err := sched.PauseBatch(batchID); err != nil {
		t.Fatalf("PauseBatch failed: %v", err)
	}
	run, _ := database.GetBatchRun(batchID)
	if run.Status != "paused" {
		t.Fatalf("Expected status paused, got %s", run.Status)
	}

	if err := sched.ResumeBatch(batchID); err != nil {
		t.Fatalf("ResumeBatch failed: %v", err)
	}
	run, _ = database.GetBatchRun(batchID)
	if run.Status != "running" {
		t.Fatalf("Expected status running, got %s", run.Status)
	}
}

func TestBatchSchedulerETAEstimation(t *testing.T) {
	tmpDir := t.TempDir()
	database, cleanup := setupTestDB(t)
	defer cleanup()

	builder := scheduler.NewBuilder("openwiki", tmpDir, tmpDir, tmpDir)
	limiter := scheduler.NewDynamicRateLimiter(2)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)
	sched := scheduler.NewBatchScheduler(database, pipeline, limiter)

	batchID := "batch_eta_001"
	batchRun := &db.BatchRun{
		ID:             batchID,
		Name:           "ETA测试批跑",
		TotalCount:     10,
		ProcessedCount: 4,
		MaxConcurrency: 2,
		Status:         "running",
		NightModeOnly:  false,
	}
	if err := database.CreateBatchRun(batchRun); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	// 创建已完成的 4 个 task，每个耗时 30 秒（平均 30 秒）
	for i := 1; i <= 4; i++ {
		task := &db.BatchTask{
			ID:          int64(i),
			BatchID:     batchID,
			RepoID:      fmt.Sprintf("test-repo-%d", i),
			Status:      "completed",
			DurationSec: 30,
			TotalTokens: 2000,
		}
		if err := database.CreateBatchTask(task); err != nil {
			t.Fatalf("CreateBatchTask failed: %v", err)
		}
	}

	// 剩余任务 6 个，并发 2，预计还需要 (6 * 30) / 2 = 90 秒 = "1分钟30秒"
	status, err := sched.GetDetailedStatus(batchID)
	if err != nil {
		t.Fatalf("GetDetailedStatus failed: %v", err)
	}

	if status.AvgDurationSec != 30.0 {
		t.Fatalf("Expected AvgDurationSec 30.0, got %f", status.AvgDurationSec)
	}
	if status.EstimatedRemainingSec != 90 {
		t.Fatalf("Expected EstimatedRemainingSec 90, got %d", status.EstimatedRemainingSec)
	}
	if status.FormattedETA != "1分钟30秒" {
		t.Fatalf("Expected FormattedETA '1分钟30秒', got %q", status.FormattedETA)
	}
}

func TestDayNightConcurrencyAutoSwitch(t *testing.T) {
	tmpDir := t.TempDir()
	database, cleanup := setupTestDB(t)
	defer cleanup()

	builder := scheduler.NewBuilder("openwiki", tmpDir, tmpDir, tmpDir)
	limiter := scheduler.NewDynamicRateLimiter(1)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)
	sched := scheduler.NewBatchScheduler(database, pipeline, limiter)

	// 1. Initial settings in DB: day_max_concurrency=2, night_max_concurrency=4
	// Test at daytime: 14:00
	dayTime := time.Date(2026, 11, 3, 14, 0, 0, 0, time.Local)
	c1 := sched.ApplyDayNightConcurrency(dayTime)
	if c1 != 2 || limiter.GetConcurrency() != 2 {
		t.Fatalf("expected day concurrency 2, got %d (limiter %d)", c1, limiter.GetConcurrency())
	}

	// 2. Test at nighttime: 23:30
	nightTime := time.Date(2026, 11, 3, 23, 30, 0, 0, time.Local)
	c2 := sched.ApplyDayNightConcurrency(nightTime)
	if c2 != 4 || limiter.GetConcurrency() != 4 {
		t.Fatalf("expected night concurrency 4, got %d (limiter %d)", c2, limiter.GetConcurrency())
	}

	// 3. Dynamically update DB settings: day_max_concurrency=5, night_max_concurrency=8
	_ = database.SetSetting("day_max_concurrency", "5")
	_ = database.SetSetting("night_max_concurrency", "8")

	// Apply night time again -> should switch to 8
	c3 := sched.ApplyDayNightConcurrency(nightTime)
	if c3 != 8 || limiter.GetConcurrency() != 8 {
		t.Fatalf("expected night concurrency 8, got %d (limiter %d)", c3, limiter.GetConcurrency())
	}

	// Apply day time -> should switch to 5
	c4 := sched.ApplyDayNightConcurrency(dayTime)
	if c4 != 5 || limiter.GetConcurrency() != 5 {
		t.Fatalf("expected day concurrency 5, got %d (limiter %d)", c4, limiter.GetConcurrency())
	}
}

func TestPipelineWithLLMProxyUsage(t *testing.T) {
	tmpDir := t.TempDir()
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := &db.Repo{
		ID:             "test-proxy-repo",
		Name:           "Proxy Test",
		GitURL:         "local",
		Branch:         "master",
		LocalPath:      tmpDir,
		Status:         "active",
		BusinessDomain: "零售金融",
	}
	_ = database.CreateRepo(repo)

	builder := scheduler.NewBuilder("echo", tmpDir, tmpDir, tmpDir)
	limiter := scheduler.NewDynamicRateLimiter(2)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)

	// Create and attach mock proxy
	proxy := llmproxy.NewLLMProxy("http://mock-upstream", "key", nil, nil)
	pipeline.SetLLMProxy(proxy)

	// Inject usage for build 1
	proxy.AddTokens("1", 500, 200, 700)

	// Verify PopBuildUsage
	u, ok := proxy.PopBuildUsage("1")
	if !ok || u.TotalTokens != 700 || u.PromptTokens != 500 || u.CompletionTokens != 200 {
		t.Fatalf("expected tokens 500/200/700, got %+v", u)
	}
}

func TestBatchSchedulerRetryFailedTasks(t *testing.T) {
	tmpDir := t.TempDir()
	database, cleanup := setupTestDB(t)
	defer cleanup()

	builder := scheduler.NewBuilder("openwiki", tmpDir, tmpDir, tmpDir)
	limiter := scheduler.NewDynamicRateLimiter(2)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)
	sched := scheduler.NewBatchScheduler(database, pipeline, limiter)

	batchID := "batch_retry_sched"
	_ = database.CreateBatchRun(&db.BatchRun{
		ID:             batchID,
		Name:           "Retry Failed Batch",
		TotalCount:     2,
		MaxConcurrency: 2,
		Status:         "completed",
	})
	_ = database.CreateBatchTask(&db.BatchTask{BatchID: batchID, RepoID: "ok-repo", Status: "pending"})
	_ = database.CreateBatchTask(&db.BatchTask{BatchID: batchID, RepoID: "fail-repo", Status: "pending"})
	_ = database.UpdateBatchTaskStatus(batchID, "ok-repo", "completed", "done", 3)
	_ = database.UpdateBatchTaskStatus(batchID, "fail-repo", "failed", "clone failed", 1)

	status, err := sched.GetDetailedStatus(batchID)
	if err != nil {
		t.Fatalf("GetDetailedStatus failed: %v", err)
	}
	if len(status.FailedTasks) != 1 || status.FailedTasks[0].RepoID != "fail-repo" {
		t.Fatalf("expected 1 failed task fail-repo, got %+v", status.FailedTasks)
	}

	// Retry failed task: scheduler marks batch running and dispatches fail-repo again
	// (fail-repo doesn't exist in repos table so runBatchLoop quickly marks it failed again & finishes cleanly)
	retried, err := sched.RetryFailedTasks(context.Background(), batchID, nil)
	if err != nil {
		t.Fatalf("RetryFailedTasks failed: %v", err)
	}
	if retried != 1 {
		t.Fatalf("expected retried=1, got %d", retried)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := database.GetBatchRun(batchID)
		tasks, _ := database.ListBatchTasks(batchID)
		if run != nil && run.Status == "completed" && len(tasks) == 2 {
			if tasks[1].RepoID == "fail-repo" && tasks[1].Status == "failed" && tasks[1].Summary == "repo not found" {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for retried task fail-repo to be re-processed by scheduler loop")
}

func TestBatchSchedulerAutoThrottleCooldownAndRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	database, cleanup := setupTestDB(t)
	defer cleanup()

	_ = database.SetSetting("day_max_concurrency", "4")
	_ = database.SetSetting("night_max_concurrency", "6")

	builder := scheduler.NewBuilder("openwiki", tmpDir, tmpDir, tmpDir)
	limiter := scheduler.NewDynamicRateLimiter(4)
	pipeline := scheduler.NewBuildPipeline(database, builder, limiter)
	sched := scheduler.NewBatchScheduler(database, pipeline, limiter)
	sched.SetThrottleCooldown(10 * time.Minute)

	baseTime := time.Date(2026, 11, 11, 14, 0, 0, 0, time.Local)
	curNow := baseTime
	sched.SetNowFunc(func() time.Time { return curNow })

	if c := sched.ApplyDayNightConcurrency(curNow); c != 4 || limiter.GetConcurrency() != 4 {
		t.Fatalf("expected initial day concurrency 4, got %d (limiter=%d)", c, limiter.GetConcurrency())
	}

	// Trigger auto-throttle -> drops 4 -> 3 and locks cooldown
	if next := sched.RecordAutoThrottle(); next != 3 || limiter.GetConcurrency() != 3 {
		t.Fatalf("expected throttled concurrency 3, got %d (limiter=%d)", next, limiter.GetConcurrency())
	}

	// Within cooldown, ApplyDayNightConcurrency MUST NOT reset back to 4
	curNow = baseTime.Add(3 * time.Minute)
	if c := sched.ApplyDayNightConcurrency(curNow); c != 3 || limiter.GetConcurrency() != 3 {
		t.Fatalf("expected concurrency held at 3 during cooldown, got %d (limiter=%d)", c, limiter.GetConcurrency())
	}

	// Trigger second throttle -> drops 3 -> 2
	if next := sched.RecordAutoThrottle(); next != 2 || limiter.GetConcurrency() != 2 {
		t.Fatalf("expected throttled concurrency 2, got %d (limiter=%d)", next, limiter.GetConcurrency())
	}

	// After 10m cooldown expires -> steps up gradually +1 (2 -> 3)
	curNow = curNow.Add(11 * time.Minute)
	if c := sched.ApplyDayNightConcurrency(curNow); c != 3 || limiter.GetConcurrency() != 3 {
		t.Fatalf("expected step-up to 3 after cooldown, got %d (limiter=%d)", c, limiter.GetConcurrency())
	}

	// Manual SetConcurrency clears throttle lock immediately
	if err := sched.SetConcurrency("", 4); err != nil {
		t.Fatalf("SetConcurrency failed: %v", err)
	}
	if limiter.GetConcurrency() != 4 {
		t.Fatalf("expected manual concurrency 4, got %d", limiter.GetConcurrency())
	}
	if c := sched.ApplyDayNightConcurrency(curNow); c != 4 {
		t.Fatalf("expected concurrency 4 after manual reset, got %d", c)
	}
}
