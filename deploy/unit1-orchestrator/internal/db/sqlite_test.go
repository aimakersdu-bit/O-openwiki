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

	err := database.CreateQASession("sess-123", "repo-1", "user-alice", "What is OpenWiki?", "OpenWiki is a wiki tool.")
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

func TestGetRepoByNameOrID(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repoA := &Repo{
		ID:        "id-alpha",
		Name:      "shared-name",
		GitURL:    "https://github.com/test/alpha.git",
		Branch:    "main",
		LocalPath: "/tmp/alpha",
		Status:    "active",
	}
	repoB := &Repo{
		ID:        "shared-name", // ID 恰好等于 repoA 的 Name
		Name:      "name-beta",
		GitURL:    "https://github.com/test/beta.git",
		Branch:    "main",
		LocalPath: "/tmp/beta",
		Status:    "active",
	}

	if err := database.CreateRepo(repoA); err != nil {
		t.Fatalf("CreateRepo repoA failed: %v", err)
	}
	if err := database.CreateRepo(repoB); err != nil {
		t.Fatalf("CreateRepo repoB failed: %v", err)
	}

	// 1. By ID for Repo A
	found, err := database.GetRepoByNameOrID("id-alpha")
	if err != nil || found == nil {
		t.Fatalf("expected to find repoA by ID, got %v, err=%v", found, err)
	}
	if found.ID != "id-alpha" {
		t.Errorf("expected id-alpha, got %s", found.ID)
	}

	// 2. Disambiguation: "shared-name" matches repoB's ID and repoA's Name.
	// Must prioritize ID, so it must return repoB!
	found, err = database.GetRepoByNameOrID("shared-name")
	if err != nil || found == nil {
		t.Fatalf("expected to find repoB by ID priority, got %v, err=%v", found, err)
	}
	if found.ID != "shared-name" || found.Name != "name-beta" {
		t.Errorf("expected repoB (ID=shared-name), got ID=%s Name=%s", found.ID, found.Name)
	}

	// 3. Fallback to Name for repoB
	found, err = database.GetRepoByNameOrID("name-beta")
	if err != nil || found == nil {
		t.Fatalf("expected to find repoB by Name, got %v, err=%v", found, err)
	}
	if found.ID != "shared-name" {
		t.Errorf("expected ID=shared-name, got %s", found.ID)
	}

	// 4. Non-existent identifier
	found, err = database.GetRepoByNameOrID("non-existent")
	if err != nil {
		t.Fatalf("expected no error for non-existent repo, got %v", err)
	}
	if found != nil {
		t.Errorf("expected nil for non-existent repo, got %v", found)
	}
}

func TestProjectCRUD(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	proj := &Project{
		ID:          "proj-test-1",
		Name:        "Retail Finance Platform",
		Description: "Microservices for retail credit and payment",
		LocalPath:   "/tmp/projects/proj-test-1",
		Status:      "active",
	}

	// 1. Create Project
	if err := database.CreateProject(proj); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	// 2. Get Project
	fetched, err := database.GetProject("proj-test-1")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if fetched == nil {
		t.Fatalf("expected project to be found, got nil")
	}
	if fetched.Name != proj.Name {
		t.Errorf("expected name %s, got %s", proj.Name, fetched.Name)
	}

	// 3. List Projects
	projects, err := database.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("expected 1 project in list, got %d", len(projects))
	}

	// 4. Update Project
	proj.Name = "Updated Project Name"
	proj.Status = "building"
	if err := database.UpdateProject(proj); err != nil {
		t.Fatalf("UpdateProject failed: %v", err)
	}
	updated, err := database.GetProject("proj-test-1")
	if err != nil || updated.Name != "Updated Project Name" || updated.Status != "building" {
		t.Fatalf("expected updated project name and status, got %v", updated)
	}

	// 5. Delete Project
	if err := database.DeleteProject("proj-test-1"); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}
	deleted, err := database.GetProject("proj-test-1")
	if err != nil || deleted != nil {
		t.Fatalf("expected project to be deleted, got %v", deleted)
	}
}

func TestProjectReposMapping(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed project and 2 repos
	proj := &Project{
		ID:        "proj-m-1",
		Name:      "Proj M",
		LocalPath: "/tmp/proj-m",
		Status:    "active",
	}
	if err := database.CreateProject(proj); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	repoA := &Repo{ID: "repo-gw", Name: "Gateway", GitURL: "u1", Branch: "main", LocalPath: "/tmp/gw", Status: "active"}
	repoB := &Repo{ID: "repo-order", Name: "OrderSrv", GitURL: "u2", Branch: "main", LocalPath: "/tmp/order", Status: "active"}
	_ = database.CreateRepo(repoA)
	_ = database.CreateRepo(repoB)

	// 1. Set Project Repos
	mappings := []ProjectRepo{
		{ProjectID: "proj-m-1", RepoID: "repo-gw", AliasName: "gateway", Role: "gateway"},
		{ProjectID: "proj-m-1", RepoID: "repo-order", AliasName: "order-service", Role: "service"},
	}
	if err := database.SetProjectRepos("proj-m-1", mappings); err != nil {
		t.Fatalf("SetProjectRepos failed: %v", err)
	}

	// 2. Get Project Repos with Details
	repos, err := database.GetProjectRepos("proj-m-1")
	if err != nil {
		t.Fatalf("GetProjectRepos failed: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos mapped, got %d", len(repos))
	}
	if repos[0].AliasName != "gateway" && repos[1].AliasName != "gateway" {
		t.Errorf("expected alias gateway in result")
	}

	// 3. Update Project Repos (e.g. remove one repo, add new alias)
	newMappings := []ProjectRepo{
		{ProjectID: "proj-m-1", RepoID: "repo-order", AliasName: "orders", Role: "service"},
	}
	if err := database.SetProjectRepos("proj-m-1", newMappings); err != nil {
		t.Fatalf("SetProjectRepos update failed: %v", err)
	}
	reposUpdated, err := database.GetProjectRepos("proj-m-1")
	if err != nil {
		t.Fatalf("GetProjectRepos after update failed: %v", err)
	}
	if len(reposUpdated) != 1 || reposUpdated[0].AliasName != "orders" {
		t.Fatalf("expected 1 repo with alias orders, got %v", reposUpdated)
	}
}

