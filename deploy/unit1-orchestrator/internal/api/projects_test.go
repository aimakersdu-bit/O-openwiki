package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
		t.Fatalf("failed to init test db: %v", err)
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
