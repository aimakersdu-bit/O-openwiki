package db

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestDB(t *testing.T) (*DB, func()) {
	tmpDir, err := os.MkdirTemp("", "unit1_test_db_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.RemoveAll(tmpDir)
	}

	return database, cleanup
}

func TestRepoCRUD(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := &Repo{
		ID:        "repo-test-1",
		Name:      "Test Repository",
		GitURL:    "https://github.com/test/repo.git",
		Branch:    "main",
		LocalPath: "/tmp/repo-1",
		WikiDir:   "/tmp/repo-1/wiki",
		StaticDir: "/tmp/repo-1/static",
		Schedule:  "0 2 * * *",
		Status:    "active",
	}

	// 1. Create Repo
	err := database.CreateRepo(repo)
	if err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	// 2. Get Repo
	fetched, err := database.GetRepo("repo-test-1")
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}
	if fetched == nil {
		t.Fatalf("expected repo to be found, got nil")
	}
	if fetched.Name != repo.Name {
		t.Errorf("expected name %s, got %s", repo.Name, fetched.Name)
	}

	// 3. List Repos
	repos, err := database.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos failed: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("expected 1 repo in list, got %d", len(repos))
	}

	// 4. Update Status
	err = database.UpdateRepoStatus("repo-test-1", "building")
	if err != nil {
		t.Fatalf("UpdateRepoStatus failed: %v", err)
	}

	updated, err := database.GetRepo("repo-test-1")
	if err != nil {
		t.Fatalf("GetRepo after update failed: %v", err)
	}
	if updated.Status != "building" {
		t.Errorf("expected status 'building', got %s", updated.Status)
	}

	// 5. Update Repo Details
	updated.Branch = "develop"
	updated.Name = "Updated Repository Name"
	if err := database.UpdateRepo(updated); err != nil {
		t.Fatalf("UpdateRepo failed: %v", err)
	}

	updatedRepo, err := database.GetRepo("repo-test-1")
	if err != nil || updatedRepo.Branch != "develop" || updatedRepo.Name != "Updated Repository Name" {
		t.Fatalf("expected updated branch 'develop' and name 'Updated Repository Name', got %v", updatedRepo)
	}

	// 6. Delete Repo
	if err := database.DeleteRepo("repo-test-1"); err != nil {
		t.Fatalf("DeleteRepo failed: %v", err)
	}
	deleted, err := database.GetRepo("repo-test-1")
	if err != nil || deleted != nil {
		t.Fatalf("expected repo to be deleted, got %v", deleted)
	}
}

func TestBuildCRUD(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed repo first
	_ = database.CreateRepo(&Repo{
		ID: "repo-1", Name: "R1", GitURL: "u", Branch: "b", LocalPath: "p", Schedule: "s", Status: "active",
	})

	buildID, err := database.CreateBuild("repo-1", "commit-abc1234", "pending")
	if err != nil {
		t.Fatalf("CreateBuild failed: %v", err)
	}
	if buildID <= 0 {
		t.Fatalf("expected positive build ID, got %d", buildID)
	}

	err = database.FinishBuild(buildID, "success", "Build completed successfully", "")
	if err != nil {
		t.Fatalf("FinishBuild failed: %v", err)
	}

	builds, err := database.ListBuilds("repo-1", 10)
	if err != nil {
		t.Fatalf("ListBuilds failed: %v", err)
	}
	if len(builds) != 1 {
		t.Fatalf("expected 1 build, got %d", len(builds))
	}
	if builds[0].Status != "success" {
		t.Errorf("expected status 'success', got %s", builds[0].Status)
	}
	if builds[0].GitHead != "commit-abc1234" {
		t.Errorf("expected git_head 'commit-abc1234', got %s", builds[0].GitHead)
	}
}

func TestQASessionCRUD(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	_ = database.CreateRepo(&Repo{
		ID: "repo-1", Name: "R1", GitURL: "u", Branch: "b", LocalPath: "p", Schedule: "s", Status: "active",
	})

	err := database.CreateQASession("repo-1", "user-alice", "What is OpenWiki?", "OpenWiki is a wiki tool.")
	if err != nil {
		t.Fatalf("CreateQASession failed: %v", err)
	}

	sessions, err := database.ListQASessions("repo-1", "user-alice", 10)
	if err != nil {
		t.Fatalf("ListQASessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Question != "What is OpenWiki?" {
		t.Errorf("expected question 'What is OpenWiki?', got %s", sessions[0].Question)
	}
}
