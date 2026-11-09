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
