package scheduler

import (
	"context"
	"sync"
	"time"
)

// DynamicRateLimiter 使用 sync.Cond 管理并发槽位，支持运行时无重启热扩容与平滑缩容
type DynamicRateLimiter struct {
	mu          sync.Mutex
	cond        *sync.Cond
	concurrency int
	busyCount   int
	available   map[int]bool
}

// NewDynamicRateLimiter 创建动态限流器，初始化槽位池
func NewDynamicRateLimiter(initialConcurrency int) *DynamicRateLimiter {
	if initialConcurrency <= 0 {
		initialConcurrency = 2
	}
	l := &DynamicRateLimiter{
		concurrency: initialConcurrency,
		available:   make(map[int]bool),
	}
	l.cond = sync.NewCond(&l.mu)
	for i := 0; i < initialConcurrency; i++ {
		l.available[i] = true
	}
	return l
}

// SetConcurrency 运行时动态热调整并发数
func (l *DynamicRateLimiter) SetConcurrency(n int) {
	if n <= 0 {
		n = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	old := l.concurrency
	l.concurrency = n

	if n > old {
		// 扩容：补充新开辟的空闲槽位
		for i := old; i < n; i++ {
			l.available[i] = true
		}
		// 广播唤醒等待中的任务
		l.cond.Broadcast()
	} else if n < old {
		// 缩容：收回未被占用的多余槽位（运行中的任务执行完毕归还时自然收敛）
		for i := n; i < old; i++ {
			delete(l.available, i)
		}
	}
}

// GetConcurrency 获取当前最大并发数
func (l *DynamicRateLimiter) GetConcurrency() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.concurrency
}

// AcquireSlot 获取一个执行槽位，若槽位全满则阻塞等待直到空闲或 context 超时/取消
func (l *DynamicRateLimiter) AcquireSlot(ctx context.Context) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 监听 context 取消信号，随时唤醒等待中的 cond
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			l.mu.Lock()
			l.cond.Broadcast()
			l.mu.Unlock()
		case <-done:
		}
	}()

	for {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}

		if l.busyCount < l.concurrency && len(l.available) > 0 {
			// 挑选一个最小序号的空闲槽位 ID，保证编号有序与前端展示稳定
			for slot := 0; slot < l.concurrency; slot++ {
				if l.available[slot] {
					delete(l.available, slot)
					l.busyCount++
					return slot, nil
				}
			}
		}

		l.cond.Wait()
	}
}

// ReleaseSlot 归还槽位，若当前并发上限允许则放回池中
func (l *DynamicRateLimiter) ReleaseSlot(slot int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.busyCount--
	if l.busyCount < 0 {
		l.busyCount = 0
	}

	// 仅当槽位编号依然在当前并发阈值内才回收（避免已缩容的槽位回流）
	if slot < l.concurrency {
		l.available[slot] = true
	}
	l.cond.Broadcast()
}

// GetSlotStats 获取当前槽位利用率指标
func (l *DynamicRateLimiter) GetSlotStats() (total, busy, idle int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total = l.concurrency
	busy = l.busyCount
	idle = total - busy
	if idle < 0 {
		idle = 0
	}
	return total, busy, idle
}

// IsNightWindow 判断指定时间是否处于跨午夜的夜间时间窗口内 (如 22:00 至 次日 07:00)
func IsNightWindow(t time.Time, startHour, endHour int) bool {
	hour := t.Hour()
	if startHour > endHour {
		return hour >= startHour || hour < endHour
	}
	return hour >= startHour && hour < endHour
}
