package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/llmproxy"
)

var usageRegex = regexp.MustCompile(`OPENWIKI_USAGE\s*(\{.*?\})`)

type usageData struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ParseTokenUsage extracts prompt, completion, and total tokens from OpenWiki process log.
// If multiple markers exist, the last emitted marker is used as it represents the cumulative total.
func ParseTokenUsage(logContent string) (prompt, completion, total int64) {
	matches := usageRegex.FindAllStringSubmatch(logContent, -1)
	if len(matches) == 0 {
		return 0, 0, 0
	}
	lastMatch := matches[len(matches)-1]
	if len(lastMatch) >= 2 {
		var u usageData
		if err := json.Unmarshal([]byte(lastMatch[1]), &u); err == nil {
			return u.PromptTokens, u.CompletionTokens, u.TotalTokens
		}
	}
	return 0, 0, 0
}

// FormatETA converts duration in seconds into a friendly localized string like "1小时15分钟20秒" or "45秒".
func FormatETA(seconds int) string {
	if seconds <= 0 {
		return "0秒"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60

	if h > 0 {
		return fmt.Sprintf("%d小时%d分钟%d秒", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%d分钟%d秒", m, s)
	}
	return fmt.Sprintf("%d秒", s)
}

// PipelineResult represents the outcome of executing a build pipeline.
type PipelineResult struct {
	Success            bool   `json:"success"`
	Status             string `json:"status"` // "completed", "skipped", "failed"
	Log                string `json:"log"`
	Error              string `json:"error"`
	PromptTokens       int64  `json:"prompt_tokens"`
	CompletionTokens   int64  `json:"completion_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	SavedTokens        int64  `json:"saved_tokens"`
	RateLimitedRetries int    `json:"rate_limited_retries"`
	DurationSec        int    `json:"duration_sec"`
	GitHead            string `json:"git_head"`
}

// PipelineProgressCallback is notified of pipeline status progression.
type PipelineProgressCallback func(stage string, progress float64, currentLog string)

// BuildPipeline encapsulates the entire build lifecycle for a single repository,
// unifying Git checkout/diff, CLI execution, token metric extraction, and DB persistence.
type BuildPipeline struct {
	db            *db.DB
	builder       *Builder
	rateLimiter   *DynamicRateLimiter
	llmProxy      *llmproxy.LLMProxy
	logBaseDir    string
	mu            sync.Mutex
	runningBuilds map[string]int64 // repo_id -> build_id
}

// NewBuildPipeline creates a new shared BuildPipeline instance.
func NewBuildPipeline(database *db.DB, builder *Builder, rateLimiter *DynamicRateLimiter) *BuildPipeline {
	return &BuildPipeline{
		db:            database,
		builder:       builder,
		rateLimiter:   rateLimiter,
		runningBuilds: make(map[string]int64),
	}
}

// SetLLMProxy sets the proxy instance for intercepting build token usage.
func (p *BuildPipeline) SetLLMProxy(proxy *llmproxy.LLMProxy) {
	p.llmProxy = proxy
}

// SetLogBaseDir sets the base directory for disk_log persistence.
func (p *BuildPipeline) SetLogBaseDir(dir string) {
	p.logBaseDir = dir
}

// Execute runs the build pipeline for a repository using the default BuildModeFull.
func (p *BuildPipeline) Execute(ctx context.Context, repo *db.Repo, force bool, slot int, onProgress PipelineProgressCallback) (*PipelineResult, error) {
	return p.ExecuteWithMode(ctx, repo, force, slot, BuildModeFull, onProgress)
}

// ExecuteWithMode runs the build pipeline for a repository according to the specified BuildMode.
func (p *BuildPipeline) ExecuteWithMode(ctx context.Context, repo *db.Repo, force bool, slot int, mode BuildMode, onProgress PipelineProgressCallback) (*PipelineResult, error) {
	if repo == nil {
		return nil, fmt.Errorf("repo is nil")
	}

	p.mu.Lock()
	if activeID, running := p.runningBuilds[repo.ID]; running {
		p.mu.Unlock()
		return &PipelineResult{
			Success: false,
			Status:  "failed",
			Error:   fmt.Sprintf("build already in progress (build_id=%d)", activeID),
		}, fmt.Errorf("build already in progress for repo %s (build_id=%d)", repo.ID, activeID)
	}

	buildID, err := p.db.CreateBuild(repo.ID, "", "running")
	if err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("create build record: %w", err)
	}
	p.runningBuilds[repo.ID] = buildID
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.runningBuilds, repo.ID)
		p.mu.Unlock()
	}()

	startTime := time.Now()
	log.Printf("[pipeline][%s] Starting build %d on slot %d (branch: %s, mode: %s)", repo.ID, buildID, slot, repo.Branch, mode)
	var logBuf strings.Builder
	logBuf.WriteString(fmt.Sprintf("=== Step 1: Git Checkout (%s) [Slot %d] ===\n", repo.Branch, slot))
	_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")

	if onProgress != nil {
		onProgress("git_checkout", 0.1, logBuf.String())
	}

	gitDir := filepath.Join(repo.LocalPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		msg := fmt.Sprintf("Local path %s does not exist. Cloning from %s...", repo.LocalPath, repo.GitURL)
		log.Printf("[pipeline][%s] %s", repo.ID, msg)
		logBuf.WriteString(msg + "\n")
		_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")

		if err := os.MkdirAll(filepath.Dir(repo.LocalPath), 0755); err != nil {
			errStr := fmt.Sprintf("failed to create directory %s: %v", repo.LocalPath, err)
			logBuf.WriteString(errStr + "\n")
			p.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return &PipelineResult{
				Success:     false,
				Status:      "failed",
				Log:         logBuf.String(),
				Error:       errStr,
				DurationSec: int(time.Since(startTime).Seconds()),
			}, nil
		}

		if err := GitClone(repo.GitURL, repo.LocalPath, repo.Branch); err != nil {
			errStr := fmt.Sprintf("git clone failed: %v", err)
			logBuf.WriteString(errStr + "\n")
			p.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return &PipelineResult{
				Success:     false,
				Status:      "failed",
				Log:         logBuf.String(),
				Error:       errStr,
				DurationSec: int(time.Since(startTime).Seconds()),
			}, nil
		}
		logBuf.WriteString("Git clone completed successfully.\n")
		_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
	} else if !force {
		status, err := GitFetchAndDiff(repo.LocalPath, repo.Branch)
		if err != nil {
			errStr := fmt.Sprintf("git fetch failed: %v", err)
			logBuf.WriteString(errStr + "\n")
			p.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return &PipelineResult{
				Success:     false,
				Status:      "failed",
				Log:         logBuf.String(),
				Error:       errStr,
				DurationSec: int(time.Since(startTime).Seconds()),
			}, nil
		}

		if !status.HasUpdates {
			// No upstream changes. Before skipping the entire build, verify that a
			// prior successful run actually produced complete wiki content and static
			// assets. If the previous build failed midway (e.g. code cloned but wiki
			// generation or static export never finished), the output is missing or
			// incomplete and must be regenerated. We keep the git pull skipped (the
			// working tree is already at HEAD, so clone/pull is idempotent here) but
			// fall through to the build steps below.
			wikiComplete := p.builder.IsWikiComplete(repo.WikiDir)
			staticComplete := p.builder.IsStaticOutputComplete(repo.ID)
			if wikiComplete && staticComplete {
				msg := fmt.Sprintf("No updates detected (HEAD=%s) and build output is complete. Skipping wiki generation (0 tokens).\n", status.LocalHead)
				logBuf.WriteString(msg)
				log.Printf("[pipeline][%s] %s", repo.ID, strings.TrimSpace(msg))
				p.finishBuild(buildID, "skipped", logBuf.String(), "")

				today := time.Now().Format("2006-01-02")
				_ = p.db.RecordDailyTokenUsage(&db.DailyTokenUsage{
					Date:         today,
					SavedTokens:  150000,
					SkippedCount: 1,
				})

				if onProgress != nil {
					onProgress("skipped", 1.0, logBuf.String())
				}

				return &PipelineResult{
					Success:     true,
					Status:      "skipped",
					Log:         logBuf.String(),
					GitHead:     status.LocalHead,
					SavedTokens: 150000,
					DurationSec: int(time.Since(startTime).Seconds()),
				}, nil
			}

			// Output is missing or incomplete: skip the redundant git pull (code is
			// already at HEAD) but continue into the build phase to regenerate wiki
			// and static assets.
			regenMsg := fmt.Sprintf("No git updates (HEAD=%s) but prior build output is incomplete (wiki_complete=%v, static_complete=%v); regenerating build output without re-pulling.\n", status.LocalHead, wikiComplete, staticComplete)
			logBuf.WriteString(regenMsg)
			log.Printf("[pipeline][%s] %s", repo.ID, strings.TrimSpace(regenMsg))
			_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		} else if err := GitPull(repo.LocalPath, repo.Branch); err != nil {
			errStr := fmt.Sprintf("git pull failed: %v", err)
			logBuf.WriteString(errStr + "\n")
			p.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return &PipelineResult{
				Success:     false,
				Status:      "failed",
				Log:         logBuf.String(),
				Error:       errStr,
				DurationSec: int(time.Since(startTime).Seconds()),
			}, nil
		} else {
			logBuf.WriteString("Git pull completed successfully.\n")
			_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		}
	} else {
		logBuf.WriteString("Forced build triggered. Pulling latest code...\n")
		_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		if err := GitPull(repo.LocalPath, repo.Branch); err != nil {
			msg := fmt.Sprintf("Git pull warning: %v (continuing with local code)\n", err)
			logBuf.WriteString(msg)
			_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		} else {
			logBuf.WriteString("Git pull completed successfully.\n")
			_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		}
	}

	if onProgress != nil {
		onProgress("building", 0.5, logBuf.String())
	}

	var logFlushMu sync.Mutex
	lastLogFlush := time.Now()
	buildRes := p.builder.BuildRepoWithModeCtx(ctx, buildID, repo.ID, repo.LocalPath, repo.WikiDir, mode, func(currentLog string) {
		fullLog := logBuf.String() + "\n" + currentLog
		logFlushMu.Lock()
		shouldFlush := time.Since(lastLogFlush) >= 5*time.Second
		if shouldFlush {
			lastLogFlush = time.Now()
		}
		logFlushMu.Unlock()
		if shouldFlush {
			_ = p.db.UpdateBuildLog(buildID, fullLog, "")
		}
		if onProgress != nil {
			onProgress("building", 0.7, fullLog)
		}
	})

	durationSec := int(time.Since(startTime).Seconds())
	if durationSec <= 0 {
		durationSec = 1
	}

	finalStatus := "completed"
	if !buildRes.Success {
		finalStatus = "failed"
	}

	fullLog := logBuf.String() + "\n" + buildRes.Log

	var promptTokens, completionTokens, totalTokens int64
	var retries int
	if p.llmProxy != nil {
		if usage, ok := p.llmProxy.PopBuildUsage(fmt.Sprintf("%d", buildID)); ok && usage.TotalTokens > 0 {
			promptTokens = int64(usage.PromptTokens)
			completionTokens = int64(usage.CompletionTokens)
			totalTokens = int64(usage.TotalTokens)
			retries = usage.RateLimitedRetries
		}
	}
	if totalTokens == 0 {
		promptTokens, completionTokens, totalTokens = ParseTokenUsage(fullLog)
	}

	_ = p.db.FinishBuildWithTokens(buildID, finalStatus, fullLog, buildRes.Error, promptTokens, completionTokens, totalTokens, 0, retries)

	// Persist build log to disk_log file
	if p.logBaseDir != "" {
		_, _ = WriteBuildLogFile(p.logBaseDir, repo.ID, fullLog)
	}

	today := time.Now().Format("2006-01-02")
	_ = p.db.RecordDailyTokenUsage(&db.DailyTokenUsage{
		Date:             today,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		BuildCount:       1,
	})

	if buildRes.Success {
		_ = p.db.UpdateRepoStatus(repo.ID, "active")
	}

	if onProgress != nil {
		onProgress(finalStatus, 1.0, fullLog)
	}

	log.Printf("[pipeline][%s] Build %d finished: status=%s, duration=%ds, prompt_tokens=%d, completion_tokens=%d, total_tokens=%d, retries=%d",
		repo.ID, buildID, finalStatus, durationSec, promptTokens, completionTokens, totalTokens, retries)

	return &PipelineResult{
		Success:            buildRes.Success,
		Status:             finalStatus,
		Log:                fullLog,
		Error:              buildRes.Error,
		PromptTokens:       promptTokens,
		CompletionTokens:   completionTokens,
		TotalTokens:        totalTokens,
		RateLimitedRetries: retries,
		DurationSec:        durationSec,
		GitHead:            buildRes.GitHead,
	}, nil
}

func (p *BuildPipeline) finishBuild(buildID int64, status, buildLog, buildErr string) {
	if buildID > 0 {
		_ = p.db.FinishBuild(buildID, status, buildLog, buildErr)
	}
}

// WriteBuildLogFile persists a repository build log to <logBaseDir>/builds/general/<repoID>.log,
// creating any intermediate subdirectories required when repoID contains slashes (e.g. "bxbank/DEO/xone-auth").
func WriteBuildLogFile(logBaseDir, repoID, fullLog string) (string, error) {
	buildDir := filepath.Join(logBaseDir, "builds", "general")
	logFilePath := filepath.Join(buildDir, fmt.Sprintf("%s.log", repoID))
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(logFilePath, []byte(fullLog), 0644); err != nil {
		return "", err
	}
	return logFilePath, nil
}
