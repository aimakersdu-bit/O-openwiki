package scheduler

import (
	"context"
	"fmt"
	"log"
	"strconv"
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
	PendingCount          int              `json:"pending_count"`
	FailedTasks           []db.BatchTask   `json:"failed_tasks,omitempty"`
	AvgDurationSec        float64          `json:"avg_duration_sec"`
	EstimatedRemainingSec int              `json:"estimated_remaining_sec"`
	FormattedETA          string           `json:"formatted_eta"`
	IsWindowOpen          bool             `json:"is_window_open"`
}

// BatchScheduler manages the non-disruptive, rate-limited batch execution of wiki builds across repositories.
type BatchScheduler struct {
	db               *db.DB
	pipeline         *BuildPipeline
	limiter          *DynamicRateLimiter
	mu               sync.RWMutex
	activeSlots      map[int]*WorkerSlotInfo
	runningBatchID   string
	cancelFunc       context.CancelFunc
	pauseCond        *sync.Cond
	isPaused         bool
	windowStartHour  int
	windowEndHour    int
	throttleCap      int
	throttleUntil    time.Time
	throttleCooldown time.Duration
	nowFunc          func() time.Time
	onBatchCompleted func(batchID string, tasks []db.BatchTask)
}

// SetOnBatchCompleted registers a callback invoked once all tasks in a batch finish.
func (bs *BatchScheduler) SetOnBatchCompleted(fn func(batchID string, tasks []db.BatchTask)) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.onBatchCompleted = fn
}

// NewBatchScheduler constructs a new BatchScheduler.
func NewBatchScheduler(database *db.DB, pipeline *BuildPipeline, limiter *DynamicRateLimiter) *BatchScheduler {
	bs := &BatchScheduler{
		db:               database,
		pipeline:         pipeline,
		limiter:          limiter,
		activeSlots:      make(map[int]*WorkerSlotInfo),
		windowStartHour:  22,
		windowEndHour:    7,
		throttleCooldown: 10 * time.Minute,
		nowFunc:          time.Now,
	}
	bs.pauseCond = sync.NewCond(&bs.mu)
	_, _ = bs.RecoverInterruptedTasks()
	return bs
}

func (bs *BatchScheduler) now() time.Time {
	bs.mu.RLock()
	fn := bs.nowFunc
	bs.mu.RUnlock()
	if fn != nil {
		return fn()
	}
	return time.Now()
}

// SetThrottleCooldown overrides the auto-throttle recovery cooldown duration (useful for tests).
func (bs *BatchScheduler) SetThrottleCooldown(d time.Duration) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.throttleCooldown = d
}

// SetNowFunc overrides the time source (useful for tests).
func (bs *BatchScheduler) SetNowFunc(fn func() time.Time) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.nowFunc = fn
}

// ClearAutoThrottle lifts any active 429 concurrency cap so configured limits apply immediately.
func (bs *BatchScheduler) ClearAutoThrottle() {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.throttleCap = 0
	bs.throttleUntil = time.Time{}
}

// RecordAutoThrottle steps down concurrency by 1 (minimum 1) and locks a recovery cooldown
// so ApplyDayNightConcurrency will not immediately reset it back to the configured ceiling.
func (bs *BatchScheduler) RecordAutoThrottle() int {
	bs.mu.Lock()
	cur := bs.limiter.GetConcurrency()
	nextConc := cur - 1
	if nextConc < 1 {
		nextConc = 1
	}
	if bs.throttleCap <= 0 || nextConc < bs.throttleCap {
		bs.throttleCap = nextConc
	} else {
		nextConc = bs.throttleCap
	}
	cooldown := bs.throttleCooldown
	if cooldown <= 0 {
		cooldown = 10 * time.Minute
	}
	nowFn := bs.nowFunc
	now := time.Now()
	if nowFn != nil {
		now = nowFn()
	}
	bs.throttleUntil = now.Add(cooldown)
	runningID := bs.runningBatchID
	bs.mu.Unlock()

	bs.limiter.SetConcurrency(nextConc)
	if runningID != "" && bs.db != nil {
		_ = bs.db.UpdateBatchConcurrency(runningID, nextConc)
	}
	log.Printf("[batch_scheduler] Auto-throttled concurrency from %d to %d (cooldown %v until %s)", cur, nextConc, cooldown, bs.throttleUntil.Format(time.RFC3339))
	return nextConc
}

// RecoverInterruptedTasks resets any residual running batch tasks to pending upon startup.
func (bs *BatchScheduler) RecoverInterruptedTasks() (int64, error) {
	if bs.db == nil {
		return 0, nil
	}
	return bs.db.RecoverInterruptedBatchTasks()
}

// SetConcurrency dynamically updates the concurrency limit in real time.
func (bs *BatchScheduler) SetConcurrency(batchID string, concurrency int) error {
	if concurrency < 1 {
		concurrency = 1
	}
	bs.ClearAutoThrottle()
	bs.limiter.SetConcurrency(concurrency)
	if batchID != "" {
		_ = bs.db.UpdateBatchConcurrency(batchID, concurrency)
	}
	log.Printf("[batch_scheduler] Concurrency adjusted to %d for batch %s", concurrency, batchID)
	return nil
}

// ApplyDayNightConcurrency checks the current hour and sets the concurrency according to system_settings.
// When an auto-throttle cooldown is active, it respects the throttled ceiling and steps back up +1 per cooldown after expiry.
// Day: 07:00 - 22:00 (default day_max_concurrency: 2)
// Night: 22:00 - 07:00 (default night_max_concurrency: 4)
func (bs *BatchScheduler) ApplyDayNightConcurrency(now time.Time) int {
	isNight := IsNightWindow(now, bs.windowStartHour, bs.windowEndHour)
	settingKey := "day_max_concurrency"
	defaultVal := 2
	if isNight {
		settingKey = "night_max_concurrency"
		defaultVal = 4
	}

	targetConc := defaultVal
	if bs.db != nil {
		valStr, err := bs.db.GetSetting(settingKey, strconv.Itoa(defaultVal))
		if err == nil {
			if n, err := strconv.Atoi(valStr); err == nil && n > 0 {
				targetConc = n
			}
		}
	}

	bs.mu.Lock()
	if bs.throttleCap > 0 {
		if now.Before(bs.throttleUntil) {
			if targetConc > bs.throttleCap {
				targetConc = bs.throttleCap
			}
		} else {
			// Cooldown elapsed: step up gradually by +1 instead of jumping straight back to ceiling.
			nextCap := bs.throttleCap + 1
			cooldown := bs.throttleCooldown
			if cooldown <= 0 {
				cooldown = 10 * time.Minute
			}
			if nextCap >= targetConc {
				bs.throttleCap = 0
				bs.throttleUntil = time.Time{}
				log.Printf("[batch_scheduler] Auto-throttle recovered to configured concurrency %d", targetConc)
			} else {
				bs.throttleCap = nextCap
				bs.throttleUntil = now.Add(cooldown)
				targetConc = nextCap
				log.Printf("[batch_scheduler] Auto-throttle stepping up concurrency to %d", targetConc)
			}
		}
	}
	bs.mu.Unlock()

	cur := bs.limiter.GetConcurrency()
	if cur != targetConc {
		bs.limiter.SetConcurrency(targetConc)
		log.Printf("[batch_scheduler] Auto-switched concurrency to %d (night=%v)", targetConc, isNight)
	}
	return targetConc
}

// PauseBatch temporarily suspends new tasks from acquiring execution slots.
func (bs *BatchScheduler) PauseBatch(batchID string) error {
	bs.mu.Lock()
	bs.isPaused = true
	bs.mu.Unlock()
	return bs.db.UpdateBatchRunStatus(batchID, "paused")
}

// ResumeBatch resumes execution of paused tasks, or initiates execution if not already running.
func (bs *BatchScheduler) ResumeBatch(batchID string) error {
	bs.mu.Lock()
	if bs.runningBatchID == "" {
		bs.mu.Unlock()
		return bs.StartBatch(context.Background(), batchID)
	}
	bs.isPaused = false
	bs.pauseCond.Broadcast()
	bs.mu.Unlock()
	return bs.db.UpdateBatchRunStatus(batchID, "running")
}

// AutoResumeLatestBatch checks on startup if there is an active running or pending batch, and starts it.
func (bs *BatchScheduler) AutoResumeLatestBatch() {
	if bs.db == nil {
		return
	}
	run, err := bs.db.GetLatestActiveBatchRun()
	if err == nil && run != nil && (run.Status == "running" || run.Status == "pending") {
		log.Printf("[batch_scheduler] Auto-resuming active batch %s (%s)", run.ID, run.Name)
		_ = bs.StartBatch(context.Background(), run.ID)
	}
}

// RetryFailedTasks resets failed tasks (or specified failed repoIDs) back to pending
// and re-enters the batch scheduler loop so they are dispatched again.
func (bs *BatchScheduler) RetryFailedTasks(ctx context.Context, batchID string, repoIDs []string) (int64, error) {
	run, err := bs.db.GetBatchRun(batchID)
	if err != nil {
		return 0, fmt.Errorf("get batch run %s: %w", batchID, err)
	}
	if run == nil {
		return 0, fmt.Errorf("batch run %s not found", batchID)
	}

	bs.mu.Lock()
	if bs.runningBatchID != "" && bs.runningBatchID != batchID {
		running := bs.runningBatchID
		bs.mu.Unlock()
		return 0, fmt.Errorf("another batch (%s) is already running", running)
	}

	alreadyRunning := bs.runningBatchID == batchID
	resetCount, pendingTotal, err := bs.db.ResetBatchFailedTasks(batchID, repoIDs, !alreadyRunning)
	if err != nil {
		bs.mu.Unlock()
		return 0, err
	}

	if alreadyRunning {
		bs.isPaused = false
		bs.pauseCond.Broadcast()
		bs.mu.Unlock()
		_ = bs.db.UpdateBatchRunStatus(batchID, "running")
	} else {
		bs.mu.Unlock()
		if err := bs.StartBatch(ctx, batchID); err != nil {
			return 0, err
		}
	}

	if resetCount == 0 && pendingTotal > 0 {
		return pendingTotal, nil
	}
	return resetCount, nil
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
	var pendingCount int
	var failedTasks []db.BatchTask
	for _, t := range tasks {
		if t.Status == "pending" || t.Status == "running" {
			pendingCount++
		}
		if t.Status == "failed" {
			failedTasks = append(failedTasks, t)
		}
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
		PendingCount:          pendingCount,
		FailedTasks:           failedTasks,
		AvgDurationSec:        avgDuration,
		EstimatedRemainingSec: estimatedRemainingSec,
		FormattedETA:          FormatETA(estimatedRemainingSec),
		IsWindowOpen:          isWindowOpen,
	}, nil
}

// StartBatch begins asynchronous execution of all pending tasks in the given batch.
func (bs *BatchScheduler) StartBatch(ctx context.Context, batchID string) error {
	bs.mu.Lock()
	if bs.runningBatchID == batchID {
		// Already assigned to this batch, ensure unpaused
		bs.isPaused = false
		bs.pauseCond.Broadcast()
		bs.mu.Unlock()
		_ = bs.db.UpdateBatchRunStatus(batchID, "running")
		return nil
	}
	if bs.runningBatchID != "" {
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

		// Auto-switch concurrency based on day/night window, cooldown lock, and system_settings
		bs.ApplyDayNightConcurrency(bs.now())

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
			bs.mu.Lock()
			latestTasks, listErr := bs.db.ListBatchTasks(batchID)
			hasMorePending := false
			if listErr == nil {
				for _, t := range latestTasks {
					if t.Status == "pending" {
						hasMorePending = true
						break
					}
				}
				if hasMorePending {
					tasks = latestTasks
				}
			}
			if !hasMorePending {
				bs.runningBatchID = ""
				bs.cancelFunc = nil
			}
			bs.mu.Unlock()
			if hasMorePending {
				continue
			}

			log.Printf("[batch_scheduler][%s] All tasks finished, marking batch completed", batchID)
			_ = bs.db.UpdateBatchRunStatus(batchID, "completed")
			bs.mu.RLock()
			cb := bs.onBatchCompleted
			bs.mu.RUnlock()
			if cb != nil {
				cb(batchID, tasks)
			}
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

			// Re-evaluate day/night window & auto-throttle step-up mid-batch before acquiring next slot
			bs.ApplyDayNightConcurrency(bs.now())

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
