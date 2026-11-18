package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
)

func setupTestProjectServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "api_project_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	if err := db.InitDB(dbPath); err != nil {
		os.RemoveAll(tmpDir)
		t.Skipf("skipping test, mysql not reachable: %v", err)
		return nil, func() {}
	}

	cfg := config.DefaultConfig()
	cfg.DBPath = dbPath
	cfg.ProjectsBaseDir = filepath.Join(tmpDir, "projects")

	server := NewServer(cfg, nil, nil, nil, nil)

	cleanup := func() {
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}

	return server, cleanup
}

func TestProjectsAPI(t *testing.T) {
	server, cleanup := setupTestProjectServer(t)
	defer cleanup()

	// Seed 2 repos
	_ = db.SaveRepo(&db.Repo{
		ID:        "repo-alpha",
		Name:      "Alpha Service",
		GitURL:    "https://example.com/alpha.git",
		Branch:    "main",
		LocalPath: "/tmp/repos/alpha",
		Status:    "active",
	})
	_ = db.SaveRepo(&db.Repo{
		ID:        "repo-beta",
		Name:      "Beta Service",
		GitURL:    "https://example.com/beta.git",
		Branch:    "main",
		LocalPath: "/tmp/repos/beta",
		Status:    "active",
	})

	// 1. Create Project
	createReq := CreateProjectRequest{
		ID:          "proj-fin",
		Name:        "Finance Services",
		Description: "Financial platform microservices",
		Repos: []ProjectRepoInput{
			{RepoID: "repo-alpha", AliasName: "alpha-svc", Role: "service"},
			{RepoID: "repo-beta", AliasName: "beta-svc", Role: "service"},
		},
	}
	body, _ := json.Marshal(createReq)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	// 2. List Projects
	listReq := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	wList := httptest.NewRecorder()
	server.ServeHTTP(wList, listReq)

	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", wList.Code, wList.Body.String())
	}

	var projects []ProjectResponse
	if err := json.Unmarshal(wList.Body.Bytes(), &projects); err != nil {
		t.Fatalf("failed to unmarshal projects response: %v", err)
	}
	if len(projects) != 1 || len(projects[0].Repos) != 2 {
		t.Fatalf("expected 1 project with 2 repos, got %+v", projects)
	}

	// 3. Update Project
	updateReq := CreateProjectRequest{
		ID:          "proj-fin",
		Name:        "Finance Platform Updated",
		Description: "Updated desc",
		Repos: []ProjectRepoInput{
			{RepoID: "repo-alpha", AliasName: "alpha-v2", Role: "gateway"},
		},
	}
	updBody, _ := json.Marshal(updateReq)
	reqUpd := httptest.NewRequest(http.MethodPut, "/api/projects", bytes.NewReader(updBody))
	wUpd := httptest.NewRecorder()
	server.ServeHTTP(wUpd, reqUpd)
	if wUpd.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for update, got %d: %s", wUpd.Code, wUpd.Body.String())
	}

	// 4. Delete Project
	delReq := httptest.NewRequest(http.MethodDelete, "/api/projects?id=proj-fin", nil)
	wDel := httptest.NewRecorder()
	server.ServeHTTP(wDel, delReq)
	if wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for delete, got %d: %s", wDel.Code, wDel.Body.String())
	}
}

func TestProjectLinksPreviewHealthAndRepair(t *testing.T) {
	server, cleanup := setupTestProjectServer(t)
	defer cleanup()

	tmpDir := filepath.Dir(server.config.ProjectsBaseDir)
	repoDir := filepath.Join(tmpDir, "repos", "repo-alpha")
	_ = os.MkdirAll(repoDir, 0755)

	_ = db.SaveRepo(&db.Repo{
		ID:        "repo-alpha",
		Name:      "Alpha Service",
		GitURL:    "https://example.com/alpha.git",
		Branch:    "main",
		LocalPath: repoDir,
		Status:    "active",
		VolumeID:  "vol1",
		SizeBytes: 1024 * 1024 * 15,
	})

	// Create project with repo-alpha
	projReq := CreateProjectRequest{
		ID:             "proj-links-test",
		Name:           "Link Test Platform",
		BusinessDomain: "零售",
		Repos: []ProjectRepoInput{
			{RepoID: "repo-alpha", AliasName: "alpha-svc", Role: "core"},
		},
	}
	body, _ := json.Marshal(projReq)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("failed to create project: %d: %s", w.Code, w.Body.String())
	}

	// 1. Check health
	reqHealth := httptest.NewRequest(http.MethodGet, "/api/projects/proj-links-test/links/health", nil)
	wHealth := httptest.NewRecorder()
	server.ServeHTTP(wHealth, reqHealth)
	if wHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for health, got %d: %s", wHealth.Code, wHealth.Body.String())
	}

	var healthRes struct {
		HealthyCount int            `json:"healthy_count"`
		BrokenCount  int            `json:"broken_count"`
		VolumeCounts map[string]int `json:"volume_counts"`
	}
	json.NewDecoder(wHealth.Body).Decode(&healthRes)
	if healthRes.HealthyCount != 1 || healthRes.BrokenCount != 0 {
		t.Fatalf("expected 1 healthy symlink, got %d healthy, %d broken", healthRes.HealthyCount, healthRes.BrokenCount)
	}
	if healthRes.VolumeCounts["vol1"] != 1 {
		t.Fatalf("expected volume vol1 count 1, got %v", healthRes.VolumeCounts)
	}

	// 2. Break link intentionally by removing symlink
	wsPath := filepath.Join(server.config.ProjectsBaseDir, "proj-links-test", "alpha-svc")
	_ = os.Remove(wsPath)

	reqBroken := httptest.NewRequest(http.MethodGet, "/api/projects/proj-links-test/links/health", nil)
	wBroken := httptest.NewRecorder()
	server.ServeHTTP(wBroken, reqBroken)
	var brokenRes struct {
		HealthyCount int `json:"healthy_count"`
		BrokenCount  int `json:"broken_count"`
	}
	json.NewDecoder(wBroken.Body).Decode(&brokenRes)
	if brokenRes.BrokenCount != 1 {
		t.Fatalf("expected 1 broken/unlinked repo, got %d", brokenRes.BrokenCount)
	}

	// 3. Repair links
	reqRepair := httptest.NewRequest(http.MethodPost, "/api/projects/proj-links-test/links/repair", nil)
	wRepair := httptest.NewRecorder()
	server.ServeHTTP(wRepair, reqRepair)
	if wRepair.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for repair, got %d: %s", wRepair.Code, wRepair.Body.String())
	}

	var repairRes struct {
		HealthyCount int `json:"healthy_count"`
		BrokenCount  int `json:"broken_count"`
	}
	json.NewDecoder(wRepair.Body).Decode(&repairRes)
	if repairRes.HealthyCount != 1 || repairRes.BrokenCount != 0 {
		t.Fatalf("expected 1 healthy symlink after repair, got %d healthy, %d broken", repairRes.HealthyCount, repairRes.BrokenCount)
	}
}

func TestProjectTaskStatusLifecycleAndBatchHook(t *testing.T) {
	server, cleanup := setupTestProjectServer(t)
	defer cleanup()

	tmpRepoDir := filepath.Join(filepath.Dir(server.config.ProjectsBaseDir), "repos", "xone-auth")
	if err := os.MkdirAll(tmpRepoDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(tmpRepoDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	if err := db.SaveRepo(&db.Repo{
		ID:             "xone-auth",
		Name:           "数字员工鉴权",
		GitURL:         "https://git.bank.local/bxbank/DEO/xone-auth.git",
		Branch:         "master",
		LocalPath:      tmpRepoDir,
		BusinessDomain: "数字员工",
		Status:         "active",
	}); err != nil {
		t.Fatalf("SaveRepo failed: %v", err)
	}

	projReq := CreateProjectRequest{
		ID:             "proj-deo",
		TriCode:        "DEO",
		Name:           "DEO-数字员工微服务集群",
		BusinessDomain: "数字员工",
		Repos:          []ProjectRepoInput{{RepoID: "xone-auth", AliasName: "xone-auth", Role: "service"}},
	}
	body, _ := json.Marshal(projReq)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", w.Code, w.Body.String())
	}

	// 1. Initial GET /api/projects reports never_built + query_key="DEO"
	wList1 := httptest.NewRecorder()
	server.ServeHTTP(wList1, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var list1 []ProjectResponse
	if err := json.Unmarshal(wList1.Body.Bytes(), &list1); err != nil {
		t.Fatal(err)
	}
	if len(list1) != 1 || list1[0].TaskStatus != "never_built" || list1[0].QueryKey != "DEO" {
		t.Fatalf("unexpected initial project status: %+v", list1)
	}

	// 2. BuildProjectWorkspace records a build and transitions task_status -> completed
	if err := server.BuildProjectWorkspace("proj-deo"); err != nil {
		t.Fatalf("BuildProjectWorkspace failed: %v", err)
	}

	wList2 := httptest.NewRecorder()
	server.ServeHTTP(wList2, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var list2 []ProjectResponse
	if err := json.Unmarshal(wList2.Body.Bytes(), &list2); err != nil {
		t.Fatal(err)
	}
	if len(list2) != 1 || list2[0].TaskStatus != "completed" || list2[0].LatestBuild == nil {
		t.Fatalf("expected task_status=completed with latest_build, got %+v", list2)
	}

	// Verify /api/build/status?repo_id=proj-deo returns project build logs
	fetchBuilds := func() []db.Build {
		wb := httptest.NewRecorder()
		server.ServeHTTP(wb, httptest.NewRequest(http.MethodGet, "/api/build/status?repo_id=proj-deo", nil))
		if wb.Code != http.StatusOK {
			t.Fatalf("GET /api/build/status status=%d body=%s", wb.Code, wb.Body.String())
		}
		var builds []db.Build
		_ = json.Unmarshal(wb.Body.Bytes(), &builds)
		return builds
	}

	buildsAfterWorkspace := fetchBuilds()
	if len(buildsAfterWorkspace) == 0 || !strings.Contains(buildsAfterWorkspace[0].Log, "=== 开始构建微服务聚合项目:") {
		t.Fatalf("expected project build logs in /api/build/status?repo_id=proj-deo, got %+v", buildsAfterWorkspace)
	}

	// 3. Batch completion callback triggers linked project build
	beforeCount := len(buildsAfterWorkspace)
	server.handleBatchCompletedProjects("batch-test-1", []db.BatchTask{
		{BatchID: "batch-test-1", RepoID: "xone-auth", Status: "success"},
	})

	deadline := time.Now().Add(3 * time.Second)
	triggered := false
	for time.Now().Before(deadline) {
		bList := fetchBuilds()
		if len(bList) > beforeCount && bList[0].Status == "completed" {
			triggered = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !triggered {
		t.Fatalf("expected handleBatchCompletedProjects to trigger and complete a new build for proj-deo")
	}
}
