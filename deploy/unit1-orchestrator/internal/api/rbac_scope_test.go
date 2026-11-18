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

func setupTestRBACServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "api_rbac_test_*")
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
	cfg.HMACSecret = "test-hmac-secret-123456"

	server := NewServer(cfg, nil, nil, nil, nil)

	cleanup := func() {
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}

	return server, cleanup
}

func signReq(req *http.Request, secret, user, role, domains string) {
	req.Header.Set("X-OpenWiki-User", user)
	req.Header.Set("X-OpenWiki-Role", role)
	req.Header.Set("X-OpenWiki-Domains", domains)
	payload := user + ":" + role + ":" + domains
	sig := ComputeHMAC(secret, payload)
	req.Header.Set("X-OpenWiki-Sig", sig)
}

func TestHMACSignatureVerification(t *testing.T) {
	server, cleanup := setupTestRBACServer(t)
	defer cleanup()

	secret := server.config.HMACSecret

	// 1. Valid signature
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	signReq(req, secret, "leader_zhang", "group_leader", "金融科技")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid HMAC, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Missing signature when headers are sent
	reqMissing := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	reqMissing.Header.Set("X-OpenWiki-User", "leader_zhang")
	recMissing := httptest.NewRecorder()
	server.ServeHTTP(recMissing, reqMissing)
	if recMissing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing HMAC signature, got %d", recMissing.Code)
	}

	// 3. Forged signature
	reqForged := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	reqForged.Header.Set("X-OpenWiki-User", "leader_zhang")
	reqForged.Header.Set("X-OpenWiki-Role", "super_admin") // Trying privilege escalation
	reqForged.Header.Set("X-OpenWiki-Sig", "badsignature123456")
	recForged := httptest.NewRecorder()
	server.ServeHTTP(recForged, reqForged)
	if recForged.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for invalid HMAC signature, got %d", recForged.Code)
	}
}

func TestLeaderAllowedInOwnDomain(t *testing.T) {
	server, cleanup := setupTestRBACServer(t)
	defer cleanup()

	secret := server.config.HMACSecret

	// Seed repo in "金融科技" domain
	_ = db.SaveRepo(&db.Repo{
		ID:             "fintech-pay",
		Name:           "Payment Service",
		GitURL:         "https://example.com/pay.git",
		Branch:         "main",
		LocalPath:      "/tmp/repos/pay",
		Status:         "active",
		BusinessDomain: "金融科技",
	})

	// 1. Group leader creates project in "金融科技" with "fintech-pay"
	createBody, _ := json.Marshal(CreateProjectRequest{
		ID:             "proj-fintech-hub",
		Name:           "Fintech Core Hub",
		BusinessDomain: "金融科技",
		Repos: []ProjectRepoInput{
			{RepoID: "fintech-pay", AliasName: "payment", Role: "core"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(createBody))
	signReq(req, secret, "leader_zhang", "group_leader", "金融科技,智能客服")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for project in leader's domain, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Leader updates the project
	updateBody, _ := json.Marshal(CreateProjectRequest{
		ID:             "proj-fintech-hub",
		Name:           "Fintech Core Hub (Updated)",
		BusinessDomain: "金融科技",
		Description:    "Updated description",
	})
	reqUpdate := httptest.NewRequest(http.MethodPut, "/api/projects", bytes.NewReader(updateBody))
	signReq(reqUpdate, secret, "leader_zhang", "group_leader", "金融科技,智能客服")
	recUpdate := httptest.NewRecorder()
	server.ServeHTTP(recUpdate, reqUpdate)

	if recUpdate.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for update project, got %d: %s", recUpdate.Code, recUpdate.Body.String())
	}

	// 3. Leader lists projects and sees their project
	reqList := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	signReq(reqList, secret, "leader_zhang", "group_leader", "金融科技")
	recList := httptest.NewRecorder()
	server.ServeHTTP(recList, reqList)

	var list []ProjectResponse
	json.NewDecoder(recList.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != "proj-fintech-hub" {
		t.Fatalf("expected 1 project in listing, got %d", len(list))
	}
}

func TestLeaderBlockedOnCrossDomainRepo(t *testing.T) {
	server, cleanup := setupTestRBACServer(t)
	defer cleanup()

	secret := server.config.HMACSecret

	// Seed repo in "零售业务" domain
	_ = db.SaveRepo(&db.Repo{
		ID:             "retail-crm",
		Name:           "Retail CRM",
		GitURL:         "https://example.com/crm.git",
		Branch:         "main",
		LocalPath:      "/tmp/repos/crm",
		Status:         "active",
		BusinessDomain: "零售业务",
	})

	// 1. Leader (authorized for "金融科技" only) attempts to create project in "零售业务" domain -> 403
	createWrongDomain, _ := json.Marshal(CreateProjectRequest{
		ID:             "proj-retail-hack",
		Name:           "Retail Unauthorized",
		BusinessDomain: "零售业务",
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(createWrongDomain))
	signReq(req1, secret, "leader_zhang", "group_leader", "金融科技")
	rec1 := httptest.NewRecorder()
	server.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for unauthorized project domain, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Leader attempts to create project in "金融科技" but including "retail-crm" (cross-domain repo) -> 403
	createCrossRepo, _ := json.Marshal(CreateProjectRequest{
		ID:             "proj-mixed",
		Name:           "Mixed Project",
		BusinessDomain: "金融科技",
		Repos: []ProjectRepoInput{
			{RepoID: "retail-crm", AliasName: "crm", Role: "ext"},
		},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(createCrossRepo))
	signReq(req2, secret, "leader_zhang", "group_leader", "金融科技")
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when adding cross-domain repo, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// 3. Super Admin creates project in "零售业务" with "retail-crm" -> 201 Created (Break-glass)
	createAdmin, _ := json.Marshal(CreateProjectRequest{
		ID:             "proj-retail-crm-admin",
		Name:           "Retail Admin Hub",
		BusinessDomain: "零售业务",
		Repos: []ProjectRepoInput{
			{RepoID: "retail-crm", AliasName: "crm", Role: "core"},
		},
	})
	reqAdmin := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(createAdmin))
	signReq(reqAdmin, secret, "admin", "super_admin", "")
	recAdmin := httptest.NewRecorder()
	server.ServeHTTP(recAdmin, reqAdmin)

	if recAdmin.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for super_admin, got %d: %s", recAdmin.Code, recAdmin.Body.String())
	}

	// 4. Leader cannot delete retail project -> 403
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/projects?id=proj-retail-crm-admin", nil)
	signReq(reqDel, secret, "leader_zhang", "group_leader", "金融科技")
	recDel := httptest.NewRecorder()
	server.ServeHTTP(recDel, reqDel)

	if recDel.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when leader deletes project in other domain, got %d", recDel.Code)
	}

	// 5. Leader lists projects: proj-retail-crm-admin should be filtered out
	reqList := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	signReq(reqList, secret, "leader_zhang", "group_leader", "金融科技")
	recList := httptest.NewRecorder()
	server.ServeHTTP(recList, reqList)

	var list []ProjectResponse
	json.NewDecoder(recList.Body).Decode(&list)
	for _, p := range list {
		if p.ID == "proj-retail-crm-admin" {
			t.Fatalf("unauthorized project appeared in leader project list: %s", p.ID)
		}
	}
}
