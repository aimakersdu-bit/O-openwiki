package db

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*DB, func()) {
	baseDSN := os.Getenv("TEST_DB_DSN")
	if baseDSN == "" {
		baseDSN = "root:@tcp(127.0.0.1:3306)/"
	}
	dbName := fmt.Sprintf("test_db_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	adminDB, err := sql.Open("mysql", baseDSN)
	if err != nil {
		t.Skipf("skipping test, cannot connect to mysql: %v", err)
		return nil, func() {}
	}
	if err := adminDB.Ping(); err != nil {
		adminDB.Close()
		t.Skipf("skipping test, mysql not reachable: %v", err)
		return nil, func() {}
	}
	if _, err := adminDB.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
		adminDB.Close()
		t.Fatalf("failed to create test db %s: %v", dbName, err)
	}
	adminDB.Close()

	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:3306)/%s?parseTime=true", dbName)
	database, err := Open(dsn)
	if err != nil {
		admin, _ := sql.Open("mysql", baseDSN)
		if admin != nil {
			_, _ = admin.Exec("DROP DATABASE `" + dbName + "`")
			admin.Close()
		}
		t.Fatalf("failed to open test db: %v", err)
	}

	cleanup := func() {
		database.Close()
		admin, err := sql.Open("mysql", baseDSN)
		if err == nil {
			_, _ = admin.Exec("DROP DATABASE `" + dbName + "`")
			admin.Close()
		}
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

func TestProjectQASessionCRUD(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	proj := &Project{ID: "proj-qa", Name: "QA Project", LocalPath: "/tmp/proj-qa", Status: "active"}
	if err := database.CreateProject(proj); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	// 1. Create Project QA Session
	err := database.CreateProjectQASession("proj-sess-1", "proj-qa", "alice", "How does service A call service B?", "Via FeignClient /orders")
	if err != nil {
		t.Fatalf("CreateProjectQASession failed: %v", err)
	}

	// 2. List Project QA Sessions
	sessions, err := database.ListProjectQASessions("proj-qa", "alice", 10)
	if err != nil {
		t.Fatalf("ListProjectQASessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Question != "How does service A call service B?" {
		t.Errorf("expected matching question, got %s", sessions[0].Question)
	}

	// 3. List User Project QA Sessions (Summary)
	summaries, err := database.ListUserProjectQASessions("proj-qa", "alice", 10)
	if err != nil {
		t.Fatalf("ListUserProjectQASessions failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].SessionID != "proj-sess-1" {
		t.Errorf("expected session_id proj-sess-1, got %s", summaries[0].SessionID)
	}

	// 4. Get Session Messages (unified across projects & repos)
	msgs, err := database.GetQASessionMessages("proj-sess-1", "alice")
	if err != nil {
		t.Fatalf("GetQASessionMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// 5. Delete Session
	if err := database.DeleteQASession("proj-sess-1", "alice"); err != nil {
		t.Fatalf("DeleteQASession failed: %v", err)
	}
	remaining, _ := database.ListProjectQASessions("proj-qa", "alice", 10)
	if len(remaining) != 0 {
		t.Errorf("expected 0 sessions after deletion, got %d", len(remaining))
	}
}

func TestProjectTriCodeLookupAndStaleCleanup(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if err := database.CreateRepo(&Repo{
		ID:        "xone-auth",
		Name:      "数字员工鉴权",
		GitURL:    "https://git.bank.local/bxbank/DEO/xone-auth.git",
		Branch:    "master",
		LocalPath: "/tmp/xone-auth",
		Status:    "active",
	}); err != nil {
		t.Fatalf("CreateRepo failed: %v", err)
	}

	if err := database.CreateProject(&Project{
		ID:             "proj-deo",
		TriCode:        "DEO",
		Name:           "DEO-数字员工微服务集群",
		BusinessDomain: "数字员工",
		LocalPath:      "/tmp/proj-deo",
		Status:         "building",
	}); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}
	if err := database.SetProjectRepos("proj-deo", []ProjectRepo{
		{RepoID: "xone-auth", AliasName: "xone-auth", Role: "service"},
	}); err != nil {
		t.Fatalf("SetProjectRepos failed: %v", err)
	}

	// Verify lookup by tri_code (case-insensitive), ID, and display name
	for _, key := range []string{"DEO", "deo", "proj-deo", "DEO-数字员工微服务集群"} {
		found, err := database.GetProjectByNameOrID(key)
		if err != nil {
			t.Fatalf("GetProjectByNameOrID(%q) error: %v", key, err)
		}
		if found == nil || found.ID != "proj-deo" || found.TriCode != "DEO" {
			t.Fatalf("GetProjectByNameOrID(%q) got %+v", key, found)
		}
	}

	// Verify GetProjectIDsByRepoIDs
	projIDs, err := database.GetProjectIDsByRepoIDs([]string{"xone-auth"})
	if err != nil {
		t.Fatalf("GetProjectIDsByRepoIDs error: %v", err)
	}
	if len(projIDs) != 1 || projIDs[0] != "proj-deo" {
		t.Fatalf("expected [proj-deo], got %v", projIDs)
	}

	// Verify CleanStaleBuilds resets stale building project to failed
	if err := database.CleanStaleBuilds(); err != nil {
		t.Fatalf("CleanStaleBuilds failed: %v", err)
	}
	reloaded, err := database.GetProject("proj-deo")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if reloaded.Status != "failed" {
		t.Fatalf("expected stale building project reset to failed, got %q", reloaded.Status)
	}
}

func TestResetBatchFailedTasks(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	batchID := "batch_retry_db_1"
	if err := database.CreateBatchRun(&BatchRun{
		ID:             batchID,
		Name:           "失败任务重跑测试",
		TotalCount:     3,
		MaxConcurrency: 2,
		Status:         "completed",
	}); err != nil {
		t.Fatalf("CreateBatchRun failed: %v", err)
	}

	for _, id := range []string{"r1", "r2", "r3"} {
		_ = database.CreateBatchTask(&BatchTask{BatchID: batchID, RepoID: id, Status: "pending"})
	}
	_ = database.UpdateBatchTaskStatus(batchID, "r1", "completed", "ok", 5)
	_ = database.UpdateBatchTaskStatus(batchID, "r2", "failed", "git auth error", 2)
	_ = database.UpdateBatchTaskStatus(batchID, "r3", "failed", "timeout", 3)

	// 1. Selective reset of r2 only
	reset1, pending1, err := database.ResetBatchFailedTasks(batchID, []string{"r2"}, true)
	if err != nil {
		t.Fatalf("ResetBatchFailedTasks selective failed: %v", err)
	}
	if reset1 != 1 || pending1 != 1 {
		t.Fatalf("expected reset=1 pending=1, got reset=%d pending=%d", reset1, pending1)
	}
	run, _ := database.GetBatchRun(batchID)
	if run.Status != "pending" || run.SuccessCount != 1 || run.FailedCount != 1 || run.ProcessedCount != 2 {
		t.Fatalf("unexpected run counters after partial reset: %+v", run)
	}

	// 2. Reset remaining failed tasks (r3)
	reset2, pending2, err := database.ResetBatchFailedTasks(batchID, nil, true)
	if err != nil {
		t.Fatalf("ResetBatchFailedTasks all failed: %v", err)
	}
	if reset2 != 1 || pending2 != 2 {
		t.Fatalf("expected reset=1 pending=2, got reset=%d pending=%d", reset2, pending2)
	}
	run, _ = database.GetBatchRun(batchID)
	if run.FailedCount != 0 || run.ProcessedCount != 1 {
		t.Fatalf("expected failed=0 processed=1, got %+v", run)
	}

	// 3. No failed/pending tasks left -> error
	_ = database.UpdateBatchTaskStatus(batchID, "r2", "completed", "ok", 4)
	_ = database.UpdateBatchTaskStatus(batchID, "r3", "completed", "ok", 4)
	if _, _, err := database.ResetBatchFailedTasks(batchID, nil, false); err == nil {
		t.Fatalf("expected error when no failed or pending tasks remain")
	}
}

func TestGetLatestFinishedBuild(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repoID := "repo-finish-test"
	_ = database.CreateRepo(&Repo{ID: repoID, Name: "Test Repo", GitURL: "http://example.com/test.git", LocalPath: "/tmp/repo"})

	// 1. 无任何构建时返回 nil
	latest, err := database.GetLatestFinishedBuild(repoID)
	if err != nil {
		t.Fatalf("unexpected error on empty builds: %v", err)
	}
	if latest != nil {
		t.Fatalf("expected nil when no builds exist, got %+v", latest)
	}

	// 2. 只有 running 构建时仍返回 nil
	b1ID, _ := database.CreateBuild(repoID, "head-1", "running")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest != nil {
		t.Fatalf("expected nil when only running builds exist, got %+v (err=%v)", latest, err)
	}

	// 3. 第一次失败构建
	_ = database.FinishBuild(b1ID, "failed", "build error log", "exit 1")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "failed" || latest.ID != b1ID {
		t.Fatalf("expected latest finished build to be failed b1 (%d), got %+v", b1ID, latest)
	}

	// 4. 第二次构建正在 running，不应干扰查询上一次 finished 的构建
	b2ID, _ := database.CreateBuild(repoID, "head-2", "running")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "failed" || latest.ID != b1ID {
		t.Fatalf("expected latest finished build to remain failed b1 (%d), got %+v", b1ID, latest)
	}

	// 5. 第二次构建完成为 completed
	_ = database.FinishBuild(b2ID, "completed", "success log", "")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "completed" || latest.ID != b2ID {
		t.Fatalf("expected latest finished build to be completed, got %+v", latest)
	}
}
