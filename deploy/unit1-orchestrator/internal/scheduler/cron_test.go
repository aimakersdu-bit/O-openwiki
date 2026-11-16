package scheduler

import (
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/robfig/cron/v3"
)

func TestScheduler_AddJob_AutoStaggers(t *testing.T) {
	s := &Scheduler{
		cron:    cron.New(),
		entries: make(map[string]cron.EntryID),
	}

	repo := db.Repo{
		ID:       "zk1MTest",
		Schedule: "0 2 * * *",
	}

	err := s.addJob(repo)
	if err != nil {
		t.Fatalf("unexpected error adding job: %v", err)
	}

	if len(s.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(s.entries))
	}

	entryID := s.entries[repo.ID]
	entry := s.cron.Entry(entryID)
	if entry.ID == 0 {
		t.Fatalf("entry not found in cron")
	}
}

func TestScheduler_AddJob_StaggeredKeyword(t *testing.T) {
	s := &Scheduler{
		cron:    cron.New(),
		entries: make(map[string]cron.EntryID),
	}

	repo := db.Repo{
		ID:       "aimakersdu-bit/health",
		Schedule: "staggered",
	}

	err := s.addJob(repo)
	if err != nil {
		t.Fatalf("unexpected error adding job: %v", err)
	}

	if len(s.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(s.entries))
	}
}
