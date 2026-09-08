package qa

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

// Pool manages concurrent QA chat executions.
type Pool struct {
	runner        *Runner
	maxConcurrent int
	sem           chan struct{}
	mu            sync.Mutex
	activeCount   int
}

// NewPool initializes a QA concurrency pool.
func NewPool(runner *Runner, maxConcurrent int) *Pool {
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}
	return &Pool{
		runner:        runner,
		maxConcurrent: maxConcurrent,
		sem:           make(chan struct{}, maxConcurrent),
	}
}

// StreamChat acquires a slot from the concurrency pool and executes the chat stream.
func (p *Pool) StreamChat(ctx context.Context, repo *db.Repo, userID string, question string, outWriter io.Writer) error {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("server busy: too many active QA processes, please try again shortly")
	}

	p.incActive()
	defer p.decActive()

	return p.runner.StreamChat(ctx, repo, userID, question, outWriter)
}

func (p *Pool) incActive() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeCount++
}

func (p *Pool) decActive() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeCount--
}

// Stats returns current pool metrics.
func (p *Pool) Stats() (active int, max int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeCount, p.maxConcurrent
}
