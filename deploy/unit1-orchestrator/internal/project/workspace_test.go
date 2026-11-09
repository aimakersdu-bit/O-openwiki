package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
)

func TestWorkspaceManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openwiki_ws_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create fake repositories
	repo1Dir := filepath.Join(tmpDir, "repo_gw")
	repo2Dir := filepath.Join(tmpDir, "repo_order")
	_ = os.MkdirAll(repo1Dir, 0755)
	_ = os.MkdirAll(repo2Dir, 0755)
	_ = os.WriteFile(filepath.Join(repo1Dir, "gateway.go"), []byte("package main"), 0644)
	_ = os.WriteFile(filepath.Join(repo2Dir, "order.go"), []byte("package main"), 0644)

	baseProjectsDir := filepath.Join(tmpDir, "projects")
	manager := NewWorkspaceManager(baseProjectsDir)

	proj := &db.Project{
		ID:          "proj-fin-1",
		Name:        "Finance Platform",
		Description: "Financial microservices",
		Status:      "active",
	}

	repos := []db.RepoWithAlias{
		{
			Repo:      db.Repo{ID: "r1", Name: "Gateway", LocalPath: repo1Dir},
			AliasName: "gateway",
			Role:      "gateway",
		},
		{
			Repo:      db.Repo{ID: "r2", Name: "OrderSrv", LocalPath: repo2Dir},
			AliasName: "order-srv",
			Role:      "service",
		},
	}

	// 1. Prepare Workspace
	wsPath, err := manager.PrepareWorkspace(proj, repos)
	if err != nil {
		t.Fatalf("PrepareWorkspace failed: %v", err)
	}

	expectedWsPath := filepath.Join(baseProjectsDir, "proj-fin-1")
	if wsPath != expectedWsPath {
		t.Errorf("expected wsPath %s, got %s", expectedWsPath, wsPath)
	}

	// Verify symlinks exist and target the correct directories
	link1 := filepath.Join(wsPath, "gateway")
	dest1, err := os.Readlink(link1)
	if err != nil || dest1 != repo1Dir {
		t.Fatalf("expected symlink gateway to %s, got %s (err: %v)", repo1Dir, dest1, err)
	}

	link2 := filepath.Join(wsPath, "order-srv")
	dest2, err := os.Readlink(link2)
	if err != nil || dest2 != repo2Dir {
		t.Fatalf("expected symlink order-srv to %s, got %s (err: %v)", repo2Dir, dest2, err)
	}

	// Verify .project-meta.json is generated
	metaPath := filepath.Join(wsPath, ".project-meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read .project-meta.json: %v", err)
	}

	var meta ProjectMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("failed to unmarshal .project-meta.json: %v", err)
	}
	if meta.ProjectID != "proj-fin-1" || len(meta.Repos) != 2 {
		t.Errorf("invalid meta content: %+v", meta)
	}

	// Verify _project_hub structure
	hubDir := filepath.Join(wsPath, "_project_hub", "openwiki")
	if info, err := os.Stat(hubDir); err != nil || !info.IsDir() {
		t.Fatalf("expected _project_hub/openwiki directory to exist")
	}

	// 2. Test Idempotency and Orphan Cleanup
	// Remove order-srv, add new repo3
	repo3Dir := filepath.Join(tmpDir, "repo_pay")
	_ = os.MkdirAll(repo3Dir, 0755)

	newRepos := []db.RepoWithAlias{
		{
			Repo:      db.Repo{ID: "r1", Name: "Gateway", LocalPath: repo1Dir},
			AliasName: "gateway",
			Role:      "gateway",
		},
		{
			Repo:      db.Repo{ID: "r3", Name: "PaySrv", LocalPath: repo3Dir},
			AliasName: "pay-srv",
			Role:      "service",
		},
	}

	_, err = manager.PrepareWorkspace(proj, newRepos)
	if err != nil {
		t.Fatalf("PrepareWorkspace update failed: %v", err)
	}

	// order-srv link should be removed
	if _, err := os.Lstat(filepath.Join(wsPath, "order-srv")); !os.IsNotExist(err) {
		t.Errorf("expected orphan symlink order-srv to be removed")
	}

	// pay-srv link should be present
	dest3, err := os.Readlink(filepath.Join(wsPath, "pay-srv"))
	if err != nil || dest3 != repo3Dir {
		t.Fatalf("expected symlink pay-srv to %s, got %s (err: %v)", repo3Dir, dest3, err)
	}
}

func TestPrepareWorkspace_AliasWithSlash(t *testing.T) {
	dir := t.TempDir()
	m := &WorkspaceManager{BaseProjectsDir: dir}

	// Simulate a repo with group/project style ID/alias containing a slash
	repoDir := filepath.Join(dir, "phys-aimakers")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	proj := &db.Project{ID: "proj-slash", LocalPath: filepath.Join(dir, "proj-slash")}
	repos := []db.RepoWithAlias{
		{Repo: db.Repo{ID: "aimakersdu-bit/health", Name: "health", LocalPath: repoDir}, AliasName: "aimakersdu-bit/health", Role: "service"},
	}

	wsDir, err := m.PrepareWorkspace(proj, repos)
	if err != nil {
		t.Fatalf("PrepareWorkspace failed for slash alias: %v", err)
	}

	// The symlink should be a single flat segment with "/" replaced by "__"
	link := filepath.Join(wsDir, "aimakersdu-bit__health")
	fi, err := os.Lstat(link)
	if err != nil {
		// list what actually got created
		entries, _ := os.ReadDir(wsDir)
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected flat symlink %s; workspace contains: %v", link, names)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s should be a symlink, got mode %v", link, fi.Mode())
	}
	// It must resolve to the physical repo dir
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != repoDir {
		t.Fatalf("symlink target = %s, want %s", target, repoDir)
	}

	// Health check should report ok, not unlinked
	preview, err := m.CheckWorkspaceHealth(proj, repos)
	if err != nil {
		t.Fatalf("CheckWorkspaceHealth: %v", err)
	}
	if preview.BrokenCount != 0 || preview.HealthyCount != 1 {
		t.Fatalf("expected 1 healthy / 0 broken, got %d healthy / %d broken", preview.HealthyCount, preview.BrokenCount)
	}
}

func TestSanitizeAlias(t *testing.T) {
	cases := map[string]string{
		"aimakersdu-bit/health": "aimakersdu-bit__health",
		"group/sub/deep":        "group__sub__deep",
		"plain":                 "plain",
		"":                      "",
		"a\\b":                  "a__b",
	}
	for in, want := range cases {
		got := sanitizeAlias(in)
		if got != want {
			t.Errorf("sanitizeAlias(%q) = %q, want %q", in, got, want)
		}
	}
}
