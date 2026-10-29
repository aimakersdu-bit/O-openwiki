package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

// WorkerSlotInfo holds live tracking data for a worker executing in a specific slot.
type WorkerSlotInfo struct {
	SlotID     int       `json:"slot_id"`
	RepoID     string    `json:"repo_id"`
	RepoName   string    `json:"repo_name"`
	Branch     string    `json:"branch"`
	StartedAt  time.Time `json:"started_at"`
	Stage      string    `json:"stage"` // "git_checkout", "building", "completed", "failed", "skipped"
	ElapsedSec int       `json:"elapsed_sec"`
}

// BatchStatusResponse combines batch metadata, active slot telemetry, and dynamic ETA estimation.
type BatchStatusResponse struct {
	BatchRun              *db.BatchRun     `json:"batch_run"`
	ActiveSlots           []WorkerSlotInfo `json:"active_slots"`
	BusySlotsCount        int              `json:"busy_slots_count"`
	IdleSlotsCount        int              `json:"idle_slots_count"`
	AvgDurationSec        float64          `json:"avg_duration_sec"`
	EstimatedRemainingSec int              `json:"estimated_remaining_sec"`
	FormattedETA          string           `json:"formatted_eta"`
	IsWindowOpen          bool             `json:"is_window_open"`
}

// BatchScheduler manages the non-disruptive, rate-limited batch execution of wiki builds across repositories.
type BatchScheduler struct {
	db              *db.DB
	pipeline        *BuildPipeline
	limiter         *DynamicRateLimiter
	mu              sync.RWMutex
	activeSlots     map[int]*WorkerSlotInfo
	runningBatchID  string
	cancelFunc      context.CancelFunc
	pauseCond       *sync.Cond
	isPaused        bool
	windowStartHour int
	windowEndHour   int
}

// NewBatchScheduler constructs a new BatchScheduler.
func NewBatchScheduler(database *db.DB, pipeline *BuildPipeline, limiter *DynamicRateLimiter) *BatchScheduler {
	bs := &BatchScheduler{
		db:              database,
		pipeline:        pipeline,
		limiter:         limiter,
		activeSlots:     make(map[int]*WorkerSlotInfo),
		windowStartHour: 22,
		windowEndHour:   7,
	}
	bs.pauseCond = sync.NewCond(&bs.mu)
	return bs
}

// SetConcurrency dynamically updates the concurrency limit in real time.
func (bs *BatchScheduler) SetConcurrency(batchID string, concurrency int) error {
	if concurrency < 1 {
		concurrency = 1
	}
	bs.limiter.SetConcurrency(concurrency)
	if batchID != "" {
		_ = bs.db.UpdateBatchConcurrency(batchID, concurrency)
	}
	log.Printf("[batch_scheduler] Concurrency adjusted to %d for batch %s", concurrency, batchID)
	return nil
}

// PauseBatch temporarily suspends new tasks from acquiring execution slots.
func (bs *BatchScheduler) PauseBatch(batchID string) error {
	bs.mu.Lock()
	bs.isPaused = true
	bs.mu.Unlock()
	return bs.db.UpdateBatchRunStatus(batchID, "paused")
}

// ResumeBatch resumes execution of paused tasks.
func (bs *BatchScheduler) ResumeBatch(batchID string) error {
	bs.mu.Lock()
	bs.isPaused = false
	bs.pauseCond.Broadcast()
	bs.mu.Unlock()
	return bs.db.UpdateBatchRunStatus(batchID, "running")
}

// CancelBatch terminates the currently running batch.
func (bs *BatchScheduler) CancelBatch(batchID string) error {
	bs.mu.Lock()
	if bs.cancelFunc != nil {
		bs.cancelFunc()
	}
	bs.isPaused = false
	bs.pauseCond.Broadcast()
	bs.mu.Unlock()
	return bs.db.UpdateBatchRunStatus(batchID, "failed")
}

// GetDetailedStatus returns rich observational telemetry and dynamic ETA.
func (bs *BatchScheduler) GetDetailedStatus(batchID string) (*BatchStatusResponse, error) {
	run, err := bs.db.GetBatchRun(batchID)
	if err != nil {
		return nil, fmt.Errorf("get batch run %s: %w", batchID, err)
	}
	if run == nil {
		return nil, fmt.Errorf("batch run %s not found", batchID)
	}

	bs.mu.RLock()
	activeList := make([]WorkerSlotInfo, 0, len(bs.activeSlots))
	now := time.Now()
	for _, slotInfo := range bs.activeSlots {
		cp := *slotInfo
		cp.ElapsedSec = int(now.Sub(cp.StartedAt).Seconds())
		activeList = append(activeList, cp)
	}
	bs.mu.RUnlock()

	_, busySlots, idleSlots := bs.limiter.GetSlotStats()

	tasks, err := bs.db.ListBatchTasks(batchID)
	if err != nil {
		return nil, fmt.Errorf("list tasks for batch %s: %w", batchID, err)
	}

	var totalSec int
	var doneCount int
	for _, t := range tasks {
		if t.Status == "completed" || t.Status == "skipped" || t.Status == "failed" {
			totalSec += t.DurationSec
			doneCount++
		}
	}

	var avgDuration float64
	if doneCount > 0 {
		avgDuration = float64(totalSec) / float64(doneCount)
	}

	remainingTasks := run.TotalCount - run.ProcessedCount
	if remainingTasks < 0 {
		remainingTasks = 0
	}

	currConcurrency := bs.limiter.GetConcurrency()
	if currConcurrency <= 0 {
		currConcurrency = 1
	}

	estimatedRemainingSec := 0
	if avgDuration > 0 && remainingTasks > 0 {
		estimatedRemainingSec = int((float64(remainingTasks) * avgDuration) / float64(currConcurrency))
	}

	isWindowOpen := true
	if run.NightModeOnly {
		isWindowOpen = IsNightWindow(time.Now(), bs.windowStartHour, bs.windowEndHour)
	}

	return &BatchStatusResponse{
		BatchRun:              run,
		ActiveSlots:           activeList,
		BusySlotsCount:        busySlots,
		IdleSlotsCount:        idleSlots,
		AvgDurationSec:        avgDuration,
		EstimatedRemainingSec: estimatedRemainingSec,
		FormattedETA:          FormatETA(estimatedRemainingSec),
		IsWindowOpen:          isWindowOpen,
	}, nil
}

// StartBatch begins asynchronous execution of all pending tasks in the given batch.
func (bs *BatchScheduler) StartBatch(ctx context.Context, batchID string) error {
	bs.mu.Lock()
	if bs.runningBatchID != "" && bs.runningBatchID != batchID {
		bs.mu.Unlock()
		return fmt.Errorf("another batch (%s) is already running", bs.runningBatchID)
	}
	batchCtx, cancel := context.WithCancel(ctx)
	bs.cancelFunc = cancel
	bs.runningBatchID = batchID
	bs.isPaused = false
	bs.mu.Unlock()

	_ = bs.db.UpdateBatchRunStatus(batchID, "running")

	go bs.runBatchLoop(batchCtx, batchID)
	return nil
}

func (bs *BatchScheduler) runBatchLoop(ctx context.Context, batchID string) {
	defer func() {
		bs.mu.Lock()
		bs.runningBatchID = ""
		bs.cancelFunc = nil
		bs.mu.Unlock()
	}()

	log.Printf("[batch_scheduler][%s] Loop started", batchID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[batch_scheduler][%s] Context cancelled, exiting loop", batchID)
			return
		default:
		}

		bs.mu.Lock()
		for bs.isPaused {
			bs.pauseCond.Wait()
		}
		bs.mu.Unlock()

		run, err := bs.db.GetBatchRun(batchID)
		if err != nil || run == nil || run.Status == "failed" {
			log.Printf("[batch_scheduler][%s] Batch stopped or removed", batchID)
			return
		}

		// Night window throttling check
		if run.NightModeOnly && !IsNightWindow(time.Now(), bs.windowStartHour, bs.windowEndHour) {
			log.Printf("[batch_scheduler][%s] Outside night window (%d:00-%d:00), sleeping 30s...",
				batchID, bs.windowStartHour, bs.windowEndHour)
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				continue
			}
		}

		tasks, err := bs.db.ListBatchTasks(batchID)
		if err != nil {
			log.Printf("[batch_scheduler][%s] Error listing batch tasks: %v", batchID, err)
			time.Sleep(2 * time.Second)
			continue
		}

		var pendingTasks []db.BatchTask
		for _, t := range tasks {
			if t.Status == "pending" {
				pendingTasks = append(pendingTasks, t)
			}
		}

		if len(pendingTasks) == 0 {
			log.Printf("[batch_scheduler][%s] All tasks finished, marking batch completed", batchID)
			_ = bs.db.UpdateBatchRunStatus(batchID, "completed")
			return
		}

		var wg sync.WaitGroup
		for _, task := range pendingTasks {
			select {
			case <-ctx.Done():
				return
			default:
			}

			bs.mu.Lock()
			for bs.isPaused {
				bs.pauseCond.Wait()
			}
			bs.mu.Unlock()

			// Acquire slot with dynamic rate limiter
			slot, err := bs.limiter.AcquireSlot(ctx)
			if err != nil {
				// Context cancelled while waiting for slot
				return
			}

			repo, err := bs.db.GetRepo(task.RepoID)
			if err != nil || repo == nil {
				bs.limiter.ReleaseSlot(slot)
				_ = bs.db.UpdateBatchTaskStatus(batchID, task.RepoID, "failed", "repo not found", 0)
				continue
			}

			slotInfo := &WorkerSlotInfo{
				SlotID:    slot,
				RepoID:    repo.ID,
				RepoName:  repo.Name,
				Branch:    repo.Branch,
				StartedAt: time.Now(),
				Stage:     "git_checkout",
			}

			bs.mu.Lock()
			bs.activeSlots[slot] = slotInfo
			bs.mu.Unlock()

			wg.Add(1)
			go func(t db.BatchTask, r db.Repo, s int, si *WorkerSlotInfo) {
				defer wg.Done()
				defer bs.limiter.ReleaseSlot(s)
				defer func() {
					bs.mu.Lock()
					delete(bs.activeSlots, s)
					bs.mu.Unlock()
				}()

				log.Printf("[batch_scheduler][%s] Starting task for repo %s on slot %d", batchID, r.ID, s)

				res, pipeErr := bs.pipeline.Execute(ctx, &r, false, s, func(stage string, progress float64, currentLog string) {
					bs.mu.Lock()
					si.Stage = stage
					bs.mu.Unlock()
				})

				duration := 1
				status := "completed"
				var promptTokens, completionTokens, totalTokens int64
				summary := "Wiki generated successfully"
				if pipeErr != nil {
					status = "failed"
					summary = pipeErr.Error()
				} else if res != nil {
					status = res.Status
					duration = res.DurationSec
					promptTokens = res.PromptTokens
					completionTokens = res.CompletionTokens
					totalTokens = res.TotalTokens
					if res.Error != "" {
						summary = res.Error
					}
				}

				_ = bs.db.UpdateBatchTaskStatus(batchID, r.ID, status, summary, duration, promptTokens, completionTokens, totalTokens)
				log.Printf("[batch_scheduler][%s] Task for repo %s finished with status %s", batchID, r.ID, status)
			}(task, *repo, slot, slotInfo)
		}

		wg.Wait()
	}
}
