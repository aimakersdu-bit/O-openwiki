package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openwiki/portal/internal/auth"
	"github.com/openwiki/portal/internal/config"
	"github.com/openwiki/portal/internal/db"
)

func setupTestPortalServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "portal_rbac_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "portal_test.db")
	if err := db.InitDB(dbPath); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to init db: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DBPath = dbPath
	cfg.HMACSecret = "portal-test-secret"

	server := NewServer(cfg, nil)

	cleanup := func() {
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}

	return server, cleanup
}

func createTestSession(t *testing.T, userID, displayName, role string) string {
	token := "token-" + userID
	sess := &db.Session{
		Token:       token,
		UserID:      userID,
		DisplayName: displayName,
		Role:        role,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}
	if err := db.SaveSession(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}
	_ = db.UpsertUser(&db.User{
		Username:    userID,
		DisplayName: displayName,
		Role:        role,
	})
	return token
}

func TestPortalRBACAndUserManagement(t *testing.T) {
	server, cleanup := setupTestPortalServer(t)
	defer cleanup()

	adminToken := createTestSession(t, "admin_user", "Super Admin", "super_admin")
	userToken := createTestSession(t, "regular_user", "Regular User", "user")

	// 1. Regular user blocked from accessing /portal/users -> 403
	reqUser := httptest.NewRequest(http.MethodGet, "/portal/users", nil)
	reqUser.AddCookie(&http.Cookie{Name: auth.CookieName, Value: userToken})
	recUser := httptest.NewRecorder()
	server.ServeHTTP(recUser, reqUser)
	if recUser.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for regular user, got %d", recUser.Code)
	}

	// 2. Super Admin can access /portal/users -> 200
	reqAdmin := httptest.NewRequest(http.MethodGet, "/portal/users", nil)
	reqAdmin.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recAdmin := httptest.NewRecorder()
	server.ServeHTTP(recAdmin, reqAdmin)
	if recAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for super_admin, got %d", recAdmin.Code)
	}

	// 3. Promote regular_user to group_leader
	setRolePayload, _ := json.Marshal(SetRoleRequest{
		UserID: "regular_user",
		Role:   "group_leader",
	})
	reqRole := httptest.NewRequest(http.MethodPost, "/portal/users/role", bytes.NewReader(setRolePayload))
	reqRole.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recRole := httptest.NewRecorder()
	server.ServeHTTP(recRole, reqRole)
	if recRole.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for set role, got %d", recRole.Code)
	}

	u, _ := db.GetUser("regular_user")
	if u.Role != "group_leader" {
		t.Fatalf("expected role group_leader, got %s", u.Role)
	}

	// 4. Grant domain to regular_user
	grantPayload, _ := json.Marshal(DomainGrantRequest{
		UserID:         "regular_user",
		BusinessDomain: "消费金融",
	})
	reqGrant := httptest.NewRequest(http.MethodPost, "/portal/users/grant", bytes.NewReader(grantPayload))
	reqGrant.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recGrant := httptest.NewRecorder()
	server.ServeHTTP(recGrant, reqGrant)
	if recGrant.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for grant domain, got %d", recGrant.Code)
	}

	domains, _ := db.GetUserDomains("regular_user")
	if len(domains) != 1 || domains[0] != "消费金融" {
		t.Fatalf("expected [消费金融], got %v", domains)
	}

	// 5. Revoke domain from regular_user
	reqRevoke := httptest.NewRequest(http.MethodPost, "/portal/users/revoke", bytes.NewReader(grantPayload))
	reqRevoke.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recRevoke := httptest.NewRecorder()
	server.ServeHTTP(recRevoke, reqRevoke)
	if recRevoke.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for revoke domain, got %d", recRevoke.Code)
	}

	domainsAfter, _ := db.GetUserDomains("regular_user")
	if len(domainsAfter) != 0 {
		t.Fatalf("expected 0 domains after revoke, got %v", domainsAfter)
	}

	// 6. Check audit logs were written
	reqAudit := httptest.NewRequest(http.MethodGet, "/portal/audit", nil)
	reqAudit.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recAudit := httptest.NewRecorder()
	server.ServeHTTP(recAudit, reqAudit)
	if recAudit.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for audit logs, got %d", recAudit.Code)
	}

	var logs []db.AuditLog
	if err := json.NewDecoder(recAudit.Body).Decode(&logs); err != nil {
		t.Fatalf("failed to decode audit logs: %v", err)
	}
	if len(logs) < 3 {
		t.Fatalf("expected at least 3 audit logs (set_role, grant, revoke), got %d", len(logs))
	}
}

func TestSignRequestHelper(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	secret := "shared-secret-test"
	SignRequest(req, secret, "leader_a", "group_leader", []string{"域1", "域2"})

	if req.Header.Get("X-OpenWiki-User") != "leader_a" {
		t.Fatalf("wrong user header: %s", req.Header.Get("X-OpenWiki-User"))
	}
	if req.Header.Get("X-OpenWiki-Role") != "group_leader" {
		t.Fatalf("wrong role header: %s", req.Header.Get("X-OpenWiki-Role"))
	}
	if req.Header.Get("X-OpenWiki-Domains") != "域1,域2" {
		t.Fatalf("wrong domains header: %s", req.Header.Get("X-OpenWiki-Domains"))
	}

	sig := req.Header.Get("X-OpenWiki-Sig")
	if sig == "" {
		t.Fatalf("missing X-OpenWiki-Sig header")
	}

	payload := "leader_a:group_leader:域1,域2"
	expected := ComputeHMAC(secret, payload)
	if sig != expected {
		t.Fatalf("signature mismatch: got %s, expected %s", sig, expected)
	}
}

func TestHandleApiGraphMultiSegmentRepoID(t *testing.T) {
	server, cleanup := setupTestPortalServer(t)
	defer cleanup()

	staticDir, err := os.MkdirTemp("", "portal_static_graph_*")
	if err != nil {
		t.Fatalf("failed to create static temp dir: %v", err)
	}
	defer os.RemoveAll(staticDir)

	server.config.StaticOutputDir = staticDir

	repoID := "bxbank/DEO/xone-auth"
	graphDir := filepath.Join(staticDir, repoID, "api")
	if err := os.MkdirAll(graphDir, 0755); err != nil {
		t.Fatalf("failed to create graph dir: %v", err)
	}
	expectedGraph := `{"nodes":[{"id":"quickstart"}]}`
	if err := os.WriteFile(filepath.Join(graphDir, "graph"), []byte(expectedGraph), 0644); err != nil {
		t.Fatalf("failed to write graph file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/graph", nil)
	req.Header.Set("Referer", "http://127.0.0.1:8080/wiki/bxbank/DEO/xone-auth/index.html")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/graph with multi-segment Referer, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != expectedGraph {
		t.Fatalf("expected graph body %s, got %s", expectedGraph, rec.Body.String())
	}
}

func TestPortalGitCredentialsSuperAdminOnly(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "portal_gitcreds_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "portal_test.db")
	if err := db.InitDB(dbPath); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.CloseDB()

	var proxiedRole string
	mockOrch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedRole = r.Header.Get("X-OpenWiki-Role")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"source":"database"}`))
	}))
	defer mockOrch.Close()

	cfg := config.DefaultConfig()
	cfg.DBPath = dbPath
	cfg.HMACSecret = "portal-test-secret"
	cfg.OrchestratorURL = mockOrch.URL
	server := NewServer(cfg, nil)

	adminToken := createTestSession(t, "admin_user", "Super Admin", "super_admin")
	leaderToken := createTestSession(t, "leader_user", "Group Leader", "group_leader")
	userToken := createTestSession(t, "regular_user", "Regular User", "user")

	// 1. Unauthenticated -> 401
	reqAnon := httptest.NewRequest(http.MethodGet, "/api/settings/git-credentials", nil)
	recAnon := httptest.NewRecorder()
	server.ServeHTTP(recAnon, reqAnon)
	if recAnon.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated request, got %d", recAnon.Code)
	}

	// 2. Regular user -> 403
	reqUser := httptest.NewRequest(http.MethodGet, "/api/settings/git-credentials", nil)
	reqUser.AddCookie(&http.Cookie{Name: auth.CookieName, Value: userToken})
	recUser := httptest.NewRecorder()
	server.ServeHTTP(recUser, reqUser)
	if recUser.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role, got %d", recUser.Code)
	}

	// 3. Group leader -> 403 on both GET and test endpoint
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/settings/git-credentials"},
		{http.MethodPost, "/api/settings/git-credentials/test"},
	} {
		reqLeader := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(`{}`))
		reqLeader.AddCookie(&http.Cookie{Name: auth.CookieName, Value: leaderToken})
		recLeader := httptest.NewRecorder()
		server.ServeHTTP(recLeader, reqLeader)
		if recLeader.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for group_leader on %s %s, got %d", tc.method, tc.path, recLeader.Code)
		}
	}

	// 4. Super Admin -> proxied 200 with HMAC role header
	reqAdmin := httptest.NewRequest(http.MethodPut, "/api/settings/git-credentials", bytes.NewBufferString(`{"enabled":true,"auth_type":"token","token":"t"}`))
	reqAdmin.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recAdmin := httptest.NewRecorder()
	server.ServeHTTP(recAdmin, reqAdmin)
	if recAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 for super_admin, got %d: %s", recAdmin.Code, recAdmin.Body.String())
	}
	if proxiedRole != "super_admin" {
		t.Fatalf("expected X-OpenWiki-Role=super_admin forwarded to orchestrator, got %q", proxiedRole)
	}
}

func TestBatchRetryFailedRBACAndProxy(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "portal_batch_retry_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "portal_test.db")
	if err := db.InitDB(dbPath); err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.CloseDB()

	var proxiedPath, proxiedQuery, proxiedRole string
	mockOrch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedPath = r.URL.Path
		proxiedQuery = r.URL.RawQuery
		proxiedRole = r.Header.Get("X-OpenWiki-Role")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"action":"retry-failed","retried_count":2}`))
	}))
	defer mockOrch.Close()

	cfg := config.DefaultConfig()
	cfg.DBPath = dbPath
	cfg.HMACSecret = "portal-test-secret"
	cfg.OrchestratorURL = mockOrch.URL
	server := NewServer(cfg, nil)

	adminToken := createTestSession(t, "admin_retry", "Super Admin", "super_admin")
	leaderToken := createTestSession(t, "leader_retry", "Group Leader", "group_leader")

	// 1. Group leader -> 403 Forbidden
	reqLeader := httptest.NewRequest(http.MethodPost, "/portal/batch/retry-failed?batch_id=b1", bytes.NewBufferString(`{}`))
	reqLeader.AddCookie(&http.Cookie{Name: auth.CookieName, Value: leaderToken})
	recLeader := httptest.NewRecorder()
	server.ServeHTTP(recLeader, reqLeader)
	if recLeader.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for group_leader on batch retry-failed, got %d", recLeader.Code)
	}

	// 2. Super admin -> 200 OK proxied to /api/batch/retry-failed
	reqAdmin := httptest.NewRequest(http.MethodPost, "/portal/batch/retry-failed?batch_id=b1", bytes.NewBufferString(`{"repo_ids":["r1","r2"]}`))
	reqAdmin.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	recAdmin := httptest.NewRecorder()
	server.ServeHTTP(recAdmin, reqAdmin)
	if recAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 for super_admin, got %d: %s", recAdmin.Code, recAdmin.Body.String())
	}
	if proxiedPath != "/api/batch/retry-failed" || proxiedQuery != "batch_id=b1" || proxiedRole != "super_admin" {
		t.Fatalf("unexpected proxied request: path=%q query=%q role=%q", proxiedPath, proxiedQuery, proxiedRole)
	}
}
