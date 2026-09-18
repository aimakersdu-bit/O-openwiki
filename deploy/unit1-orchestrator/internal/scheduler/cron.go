package scheduler

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/robfig/cron/v3"
)

// Scheduler manages cron-based wiki build jobs for all registered repos.
type Scheduler struct {
	cron          *cron.Cron
	db            *db.DB
	builder       *Builder
	mu            sync.Mutex
	entries       map[string]cron.EntryID // repo_id -> cron entry ID
	runningBuilds map[string]int64        // repo_id -> active buildID
}

// NewScheduler creates a new Scheduler using the global database.
func NewScheduler(builder *Builder) *Scheduler {
	return &Scheduler{
		cron:          cron.New(),
		db:            db.GetDB(),
		builder:       builder,
		entries:       make(map[string]cron.EntryID),
		runningBuilds: make(map[string]int64),
	}
}

// GetBuilder returns the Scheduler's builder instance.
func (s *Scheduler) GetBuilder() *Builder {
	return s.builder
}

// New creates a new Scheduler with explicit database and builder.
func New(database *db.DB, builder *Builder) *Scheduler {
	return &Scheduler{
		cron:          cron.New(),
		db:            database,
		builder:       builder,
		entries:       make(map[string]cron.EntryID),
		runningBuilds: make(map[string]int64),
	}
}

// AddRepoJob registers a cron job for a newly registered repo.
func (s *Scheduler) AddRepoJob(repo *db.Repo) error {
	if repo == nil {
		return fmt.Errorf("repo is nil")
	}
	return s.addJob(*repo)
}

// Start loads all active repos from the database, registers their cron jobs, and starts the scheduler.
func (s *Scheduler) Start() error {
	// Clean up stale 'running' build records from previous daemon crashes/restarts
	_ = db.CleanStaleBuilds()

	repos, err := s.db.ListRepos()
	if err != nil {
		return fmt.Errorf("list repos: %w", err)
	}

	for _, repo := range repos {
		if repo.Status != "active" {
			continue
		}
		if err := s.addJob(repo); err != nil {
			log.Printf("[scheduler] skip repo %s: %v", repo.ID, err)
		}
	}

	s.cron.Start()
	log.Printf("[scheduler] started with %d active repo jobs", len(s.entries))
	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// TriggerBuild manually triggers a build for a specific repo (bypasses cron schedule, forced build).
func (s *Scheduler) TriggerBuild(repoID string) error {
	repo, err := s.db.GetRepo(repoID)
	if err != nil || repo == nil {
		return fmt.Errorf("repo not found: %s", repoID)
	}
	go s.runBuild(*repo, true)
	return nil
}

// addJob registers a cron entry for the given repo.
func (s *Scheduler) addJob(repo db.Repo) error {
	r := repo // capture for closure
	entryID, err := s.cron.AddFunc(repo.Schedule, func() {
		s.runBuild(r, false)
	})
	if err != nil {
		return fmt.Errorf("invalid cron %q: %w", repo.Schedule, err)
	}

	s.mu.Lock()
	s.entries[repo.ID] = entryID
	s.mu.Unlock()

	log.Printf("[scheduler] registered repo %s with schedule %q", repo.ID, repo.Schedule)
	return nil
}

// RemoveRepoJob removes a registered cron entry for a repo.
func (s *Scheduler) RemoveRepoJob(repoID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, exists := s.entries[repoID]; exists {
		s.cron.Remove(entryID)
		delete(s.entries, repoID)
		log.Printf("[scheduler] removed cron job for repo %s", repoID)
	}
}

// UpdateRepoJob updates the cron schedule for an existing repo.
func (s *Scheduler) UpdateRepoJob(repo *db.Repo) error {
	if repo == nil {
		return fmt.Errorf("repo is nil")
	}
	s.RemoveRepoJob(repo.ID)
	return s.addJob(*repo)
}

// runBuild executes the full build pipeline for a single repo:
// 1. git clone if local directory does not exist, otherwise git fetch/pull
// 2. openwiki init (if .openwiki not present)
// 3. openwiki code --update --print
// 4. Export static files & vendor assets to Nginx output directory
func (s *Scheduler) runBuild(repo db.Repo, force bool) {
	s.mu.Lock()
	if activeID, running := s.runningBuilds[repo.ID]; running {
		s.mu.Unlock()
		log.Printf("[build][%s] build already in progress (build_id=%d), skipping duplicate trigger", repo.ID, activeID)
		return
	}
	buildID, _ := s.db.CreateBuild(repo.ID, "", "running")
	s.runningBuilds[repo.ID] = buildID
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.runningBuilds, repo.ID)
		s.mu.Unlock()
	}()

	log.Printf("[build] starting build for repo %s (force=%v, build_id=%d)", repo.ID, force, buildID)

	// Step 1: Git Clone if local directory does not exist
	var logBuf strings.Builder
	logBuf.WriteString(fmt.Sprintf("=== Step 1: Git Checkout (%s) ===\n", repo.Branch))
	_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")

	gitDir := filepath.Join(repo.LocalPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		msg := fmt.Sprintf("Local path %s does not exist. Cloning from %s...", repo.LocalPath, repo.GitURL)
		log.Printf("[build][%s] %s", repo.ID, msg)
		logBuf.WriteString(msg + "\n")
		_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")

		if err := os.MkdirAll(filepath.Dir(repo.LocalPath), 0755); err != nil {
			errStr := fmt.Sprintf("failed to create directory %s: %v", repo.LocalPath, err)
			log.Printf("[build][%s] %s", repo.ID, errStr)
			s.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return
		}
		if err := GitClone(repo.GitURL, repo.LocalPath, repo.Branch); err != nil {
			errStr := fmt.Sprintf("git clone failed: %v", err)
			log.Printf("[build][%s] %s", repo.ID, errStr)
			s.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return
		}
		logBuf.WriteString("Git clone completed successfully.\n")
		_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
		log.Printf("[build][%s] git clone succeeded", repo.ID)
	} else if !force {
		// Check for updates if not forced by manual trigger
		status, err := GitFetchAndDiff(repo.LocalPath, repo.Branch)
		if err != nil {
			errStr := fmt.Sprintf("git fetch failed: %v", err)
			log.Printf("[build][%s] %s", repo.ID, errStr)
			s.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return
		}
		if !status.HasUpdates {
			logBuf.WriteString(fmt.Sprintf("No updates detected (HEAD=%s).\n", status.LocalHead))
			log.Printf("[build][%s] no updates (HEAD=%s)", repo.ID, status.LocalHead)
			s.finishBuild(buildID, "skipped", logBuf.String(), "")
			return
		}
		if err := GitPull(repo.LocalPath, repo.Branch); err != nil {
			errStr := fmt.Sprintf("git pull failed: %v", err)
			log.Printf("[build][%s] %s", repo.ID, errStr)
			s.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return
		}
		logBuf.WriteString("Git pull completed successfully.\n")
		_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
	} else {
		// Manual trigger: attempt pull if repo exists
		logBuf.WriteString("Manual trigger forced build. Pulling latest code...\n")
		_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
		if err := GitPull(repo.LocalPath, repo.Branch); err != nil {
			msg := fmt.Sprintf("Git pull warning: %v (continuing build with local code)\n", err)
			log.Printf("[build][%s] %s", repo.ID, msg)
			logBuf.WriteString(msg)
			_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
		} else {
			logBuf.WriteString("Git pull completed successfully.\n")
			_ = s.db.UpdateBuildLog(buildID, logBuf.String(), "")
		}
	}

	// Step 2, 3 & 4: OpenWiki Init + Update + Static Export
	result := s.builder.BuildRepo(repo.ID, repo.LocalPath, repo.WikiDir, func(currentLog string) {
		fullLog := logBuf.String() + "\n" + currentLog
		_ = s.db.UpdateBuildLog(buildID, fullLog, "")
	})

	finalStatus := "success"
	if !result.Success {
		finalStatus = "failed"
	}

	fullLog := logBuf.String() + "\n" + result.Log
	s.finishBuild(buildID, finalStatus, fullLog, result.Error)
	_ = s.db.UpdateRepoStatus(repo.ID, "active")
	log.Printf("[build][%s] build completed with status %s", repo.ID, finalStatus)
}

func (s *Scheduler) finishBuild(buildID int64, status, buildLog, buildErr string) {
	if buildID > 0 {
		_ = s.db.FinishBuild(buildID, status, buildLog, buildErr)
	}
}

// recordBuild is a helper for recording a build without the full pipeline.
func (s *Scheduler) recordBuild(repoID, gitHead, status, buildLog, buildErr string) {
	id, _ := s.db.CreateBuild(repoID, gitHead, status)
	if id > 0 && status != "pending" {
		_ = s.db.FinishBuild(id, status, buildLog, buildErr)
	}
}
