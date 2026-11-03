package llmproxy

import (
	"context"
	"sync"
	"time"
)

// TokenRecord records a usage event for sliding window rate limiting.
type TokenRecord struct {
	Timestamp time.Time
	Tokens    int
}

// TPMRateLimiter implements a 1-minute sliding window rate limiter for TPM (tokens per minute)
// and RPM (requests per minute). 0 means unconstrained / unlimited.
type TPMRateLimiter struct {
	mu sync.Mutex

	maxTPM int // 0 = unlimited
	maxRPM int // 0 = unlimited

	records []TokenRecord

	peakTPM int
	peakRPM int

	nowFunc func() time.Time
}

// NewTPMRateLimiter creates a new rate limiter with the specified quotas.
func NewTPMRateLimiter(maxTPM, maxRPM int) *TPMRateLimiter {
	return &TPMRateLimiter{
		maxTPM:  maxTPM,
		maxRPM:  maxRPM,
		records: make([]TokenRecord, 0),
		nowFunc: time.Now,
	}
}

// SetNowFunc overrides the time source (useful for testing).
func (l *TPMRateLimiter) SetNowFunc(fn func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nowFunc = fn
}

func (l *TPMRateLimiter) now() time.Time {
	if l.nowFunc != nil {
		return l.nowFunc()
	}
	return time.Now()
}

// SetQuota dynamically updates the TPM and RPM quotas at runtime.
func (l *TPMRateLimiter) SetQuota(maxTPM, maxRPM int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxTPM = maxTPM
	l.maxRPM = maxRPM
}

// GetQuota returns the currently configured TPM and RPM quotas.
func (l *TPMRateLimiter) GetQuota() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxTPM, l.maxRPM
}

// prune removes records older than 1 minute and updates peaks.
// Must be called with l.mu held.
func (l *TPMRateLimiter) prune(now time.Time) {
	cutoff := now.Add(-60 * time.Second)
	idx := 0
	for idx < len(l.records) && l.records[idx].Timestamp.Before(cutoff) {
		idx++
	}
	if idx > 0 {
		l.records = l.records[idx:]
	}

	curTPM := 0
	for _, r := range l.records {
		curTPM += r.Tokens
	}
	curRPM := len(l.records)

	if curTPM > l.peakTPM {
		l.peakTPM = curTPM
	}
	if curRPM > l.peakRPM {
		l.peakRPM = curRPM
	}
}

// currentTokens returns the sum of tokens in the active window.
// Must be called with l.mu held.
func (l *TPMRateLimiter) currentTokens() int {
	sum := 0
	for _, r := range l.records {
		sum += r.Tokens
	}
	return sum
}

// Acquire waits until the estimatedTokens can be accepted within maxTPM and maxRPM.
func (l *TPMRateLimiter) Acquire(ctx context.Context, estimatedTokens int) error {
	if estimatedTokens < 0 {
		estimatedTokens = 0
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		l.mu.Lock()
		now := l.now()
		l.prune(now)

		// Check if unlimited
		if l.maxTPM <= 0 && l.maxRPM <= 0 {
			l.records = append(l.records, TokenRecord{
				Timestamp: now,
				Tokens:    estimatedTokens,
			})
			l.prune(now)
			l.mu.Unlock()
			return nil
		}

		curRPM := len(l.records)
		curTPM := l.currentTokens()

		rpmBlocked := l.maxRPM > 0 && curRPM >= l.maxRPM
		tpmBlocked := l.maxTPM > 0 && (curTPM+estimatedTokens) > l.maxTPM

		if !rpmBlocked && !tpmBlocked {
			// Acquired
			l.records = append(l.records, TokenRecord{
				Timestamp: now,
				Tokens:    estimatedTokens,
			})
			l.prune(now)
			l.mu.Unlock()
			return nil
		}

		// Calculate wait time
		var waitTime time.Duration = 10 * time.Millisecond
		if len(l.records) > 0 {
			if rpmBlocked {
				// Wait for the oldest record to expire
				expireAt := l.records[0].Timestamp.Add(60 * time.Second)
				if diff := expireAt.Sub(now); diff > waitTime {
					waitTime = diff
				}
			} else if tpmBlocked {
				// Find how many records must expire to accommodate estimatedTokens
				freed := 0
				targetFreed := (curTPM + estimatedTokens) - l.maxTPM
				for _, r := range l.records {
					freed += r.Tokens
					expireAt := r.Timestamp.Add(60 * time.Second)
					if diff := expireAt.Sub(now); diff > waitTime {
						waitTime = diff
					}
					if freed >= targetFreed {
						break
					}
				}
			}
		}

		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitTime):
		}
	}
}

// RecordRealUsage adjusts the recorded tokens once the actual LLM response usage is known.
func (l *TPMRateLimiter) RecordRealUsage(estimatedTokens int, actualTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	diff := actualTokens - estimatedTokens
	if diff == 0 {
		return
	}

	// Record the adjustment as a delta event at current time
	l.records = append(l.records, TokenRecord{
		Timestamp: now,
		Tokens:    diff,
	})
	l.prune(now)
}

// GetStats returns current usage and peak values within the 1-minute sliding window.
func (l *TPMRateLimiter) GetStats() (currentTPM, currentRPM, peakTPM, peakRPM int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.prune(l.now())
	currentTPM = l.currentTokens()
	currentRPM = len(l.records)
	peakTPM = l.peakTPM
	peakRPM = l.peakRPM
	return
}
