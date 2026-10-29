package scheduler_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestBatchSchedulerLifecycleAndConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_batch_sched.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

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
	dbPath := filepath.Join(tmpDir, "test_batch_eta.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}
	defer database.Close()

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
