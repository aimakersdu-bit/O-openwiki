package qa

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

type dummyRunner struct{}

func (d *dummyRunner) StreamChat(ctx context.Context, repo *db.Repo, userID string, question string, outWriter io.Writer) error {
	time.Sleep(50 * time.Millisecond)
	_, err := outWriter.Write([]byte("data: hello\n\n"))
	return err
}

func TestPoolConcurrencyLimit(t *testing.T) {
	// Create pool with maxConcurrent = 2
	pool := &Pool{
		runner:        nil, // We will test semaphore behavior directly
		maxConcurrent: 2,
		sem:           make(chan struct{}, 2),
	}

	active, max := pool.Stats()
	if max != 2 {
		t.Errorf("expected max 2, got %d", max)
	}
	if active != 0 {
		t.Errorf("expected active 0, got %d", active)
	}

	// Fill slots manually
	pool.sem <- struct{}{}
	pool.incActive()
	pool.sem <- struct{}{}
	pool.incActive()

	active, _ = pool.Stats()
	if active != 2 {
		t.Errorf("expected active 2, got %d", active)
	}

	// Try to acquire with zero timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	var out strings.Builder
	repo := &db.Repo{ID: "r1"}

	err := pool.StreamChat(ctx, repo, "u1", "q1", &out)
	if err == nil {
		t.Errorf("expected error when pool is full, got nil")
	}

	// Release 1 slot
	<-pool.sem
	pool.decActive()

	active, _ = pool.Stats()
	if active != 1 {
		t.Errorf("expected active 1, got %d", active)
	}
}
