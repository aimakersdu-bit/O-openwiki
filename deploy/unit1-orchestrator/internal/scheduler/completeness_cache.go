package scheduler

import (
	"sync"
	"time"
)

const wikiCompletenessCacheTTL = 30 * time.Second

type staticCacheEntry struct {
	graphModTime int64
	graphSize    int64
	complete     bool
}

type wikiCacheEntry struct {
	checkedAt time.Time
	complete  bool
}

var (
	completenessMu   sync.RWMutex
	staticCheckCache = make(map[string]staticCacheEntry)
	wikiCheckCache   = make(map[string]wikiCacheEntry)
)

func getCachedStaticComplete(staticDir string, graphModTime, graphSize int64) (bool, bool) {
	completenessMu.RLock()
	entry, ok := staticCheckCache[staticDir]
	completenessMu.RUnlock()
	if ok && entry.graphModTime == graphModTime && entry.graphSize == graphSize {
		return entry.complete, true
	}
	return false, false
}

func setCachedStaticComplete(staticDir string, graphModTime, graphSize int64, complete bool) {
	completenessMu.Lock()
	staticCheckCache[staticDir] = staticCacheEntry{
		graphModTime: graphModTime,
		graphSize:    graphSize,
		complete:     complete,
	}
	completenessMu.Unlock()
}

func getCachedWikiComplete(wikiDir string) (bool, bool) {
	completenessMu.RLock()
	entry, ok := wikiCheckCache[wikiDir]
	completenessMu.RUnlock()
	if ok && time.Since(entry.checkedAt) < wikiCompletenessCacheTTL {
		return entry.complete, true
	}
	return false, false
}

func setCachedWikiComplete(wikiDir string, complete bool) {
	completenessMu.Lock()
	wikiCheckCache[wikiDir] = wikiCacheEntry{
		checkedAt: time.Now(),
		complete:  complete,
	}
	completenessMu.Unlock()
}

// InvalidateCompletenessCache clears cached completeness results for the given static and/or wiki directories.
// Pass empty string to skip invalidating that dimension.
func InvalidateCompletenessCache(staticDir, wikiDir string) {
	completenessMu.Lock()
	if staticDir != "" {
		delete(staticCheckCache, staticDir)
	}
	if wikiDir != "" {
		delete(wikiCheckCache, wikiDir)
	}
	completenessMu.Unlock()
}
