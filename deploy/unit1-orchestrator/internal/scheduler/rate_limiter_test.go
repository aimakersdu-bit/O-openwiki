package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestDynamicRateLimiterAndSlotPool(t *testing.T) {
	limiter := scheduler.NewDynamicRateLimiter(2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	slot1, err := limiter.AcquireSlot(ctx)
	if err != nil {
		t.Fatalf("AcquireSlot 1 failed: %v", err)
	}
	slot2, err := limiter.AcquireSlot(ctx)
	if err != nil {
		t.Fatalf("AcquireSlot 2 failed: %v", err)
	}

	if slot1 == slot2 {
		t.Errorf("Slot IDs must be unique, got %d and %d", slot1, slot2)
	}

	// 此时容量为 2 且已满，再次获取应当超时
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelTimeout()
	if _, err := limiter.AcquireSlot(ctxTimeout); err == nil {
		t.Fatalf("Expected acquire to timeout when slots are full")
	}

	// 热扩容到 3
	limiter.SetConcurrency(3)
	if limiter.GetConcurrency() != 3 {
		t.Errorf("Expected concurrency 3, got %d", limiter.GetConcurrency())
	}

	slot3, err := limiter.AcquireSlot(ctx)
	if err != nil {
		t.Fatalf("AcquireSlot 3 after resize failed: %v", err)
	}

	limiter.ReleaseSlot(slot1)
	limiter.ReleaseSlot(slot2)
	limiter.ReleaseSlot(slot3)

	total, busy, idle := limiter.GetSlotStats()
	if total != 3 || busy != 0 || idle != 3 {
		t.Errorf("Expected 3 idle slots, got total=%d, busy=%d, idle=%d", total, busy, idle)
	}
}

func TestNightWindowCheck(t *testing.T) {
	t1 := time.Date(2026, 10, 29, 23, 30, 0, 0, time.Local)
	if !scheduler.IsNightWindow(t1, 22, 7) {
		t.Errorf("Expected 23:30 to be within night window [22, 7]")
	}

	t2 := time.Date(2026, 10, 29, 14, 0, 0, 0, time.Local)
	if scheduler.IsNightWindow(t2, 22, 7) {
		t.Errorf("Expected 14:00 NOT to be within night window [22, 7]")
	}

	t3 := time.Date(2026, 10, 29, 6, 15, 0, 0, time.Local)
	if !scheduler.IsNightWindow(t3, 22, 7) {
		t.Errorf("Expected 06:15 to be within night window [22, 7]")
	}
}
