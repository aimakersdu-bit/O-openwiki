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

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func setupGitCredsTestServer(t *testing.T, cfgMod func(*config.Config)) (*Server, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "api_gitcreds_test_*")
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
	if cfgMod != nil {
		cfgMod(cfg)
	}

	server := NewServer(cfg, nil, nil, nil, nil)
	cleanup := func() {
		scheduler.SetGitCredentialsProvider(nil)
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}
	return server, cleanup
}

func TestGitCredentialsRBAC(t *testing.T) {
	server, cleanup := setupGitCredsTestServer(t, nil)
	defer cleanup()
	secret := server.config.HMACSecret

	cases := []struct {
		name   string
		method string
		path   string
		role   string
		want   int
	}{
		{"user GET forbidden", http.MethodGet, "/api/settings/git-credentials", "user", http.StatusForbidden},
		{"group_leader GET forbidden", http.MethodGet, "/api/settings/git-credentials", "group_leader", http.StatusForbidden},
		{"group_leader PUT forbidden", http.MethodPut, "/api/settings/git-credentials", "group_leader", http.StatusForbidden},
		{"user test forbidden", http.MethodPost, "/api/settings/git-credentials/test", "user", http.StatusForbidden},
		{"super_admin GET ok", http.MethodGet, "/api/settings/git-credentials", "super_admin", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyBytes []byte
			if tc.method == http.MethodPut || tc.method == http.MethodPost {
				bodyBytes = []byte(`{"enabled":true,"auth_type":"token","token":"abc"}`)
			}
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			signReq(req, secret, "tester", tc.role, "金融科技")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("expected status %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGitCredentialsLifecycleAndMasking(t *testing.T) {
	t.Setenv("GIT_HTTP_USERNAME", "")
	t.Setenv("GIT_HTTP_PASSWORD", "")
	t.Setenv("GIT_HTTP_TOKEN", "")
	t.Setenv("GIT_HTTP_HOST", "")
	t.Setenv("GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), ".git-credentials"))

	server, cleanup := setupGitCredsTestServer(t, func(cfg *config.Config) {
		cfg.GitHTTPUsername = "cfg-user"
		cfg.GitHTTPToken = "cfg-token-9988"
	})
	defer cleanup()
	secret := server.config.HMACSecret

	// 1. Initial GET falls back to config without exposing plaintext token
	req1 := httptest.NewRequest(http.MethodGet, "/api/settings/git-credentials", nil)
	signReq(req1, secret, "admin", "super_admin", "")
	rec1 := httptest.NewRecorder()
	server.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("initial GET failed: %d %s", rec1.Code, rec1.Body.String())
	}
	if strings.Contains(rec1.Body.String(), "cfg-token-9988") {
		t.Fatalf("GET response leaked plaintext token: %s", rec1.Body.String())
	}
	var initResp gitCredentialsResponse
	_ = json.NewDecoder(rec1.Body).Decode(&initResp)
	if !initResp.Enabled || !initResp.HasToken || initResp.Source != "config" || initResp.Username != "cfg-user" {
		t.Fatalf("unexpected initial config response: %+v", initResp)
	}

	// 2. Save unified token credentials in DB
	putPayload := `{
		"enabled": true,
		"auth_type": "token",
		"host": "https://gitlab.bank.local/team",
		"username": "wiki-bot",
		"token": "glpat-SecretToken99",
		"override_inline": true,
		"ssl_no_verify": true
	}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/settings/git-credentials", bytes.NewBufferString(putPayload))
	req2.Header.Set("Content-Type", "application/json")
	signReq(req2, secret, "admin", "super_admin", "")
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("PUT failed: %d %s", rec2.Code, rec2.Body.String())
	}
	if strings.Contains(rec2.Body.String(), "glpat-SecretToken99") {
		t.Fatalf("PUT response leaked plaintext token: %s", rec2.Body.String())
	}

	var saveResp gitCredentialsResponse
	_ = json.NewDecoder(rec2.Body).Decode(&saveResp)
	if saveResp.Source != "database" || saveResp.Host != "gitlab.bank.local" || !saveResp.HasToken || !saveResp.SSLNoVerify {
		t.Fatalf("unexpected PUT response: %+v", saveResp)
	}

	// Verify scheduler live provider resolves new DB credentials immediately
	liveCreds := scheduler.ResolveGitCredentials()
	if !liveCreds.Enabled || liveCreds.Token != "glpat-SecretToken99" || liveCreds.Host != "gitlab.bank.local" {
		t.Fatalf("unexpected scheduler resolved credentials: %+v", liveCreds)
	}

	// 3. Partial update (host only, empty token) preserves saved token
	patchPayload := `{"host": "git.internal.bank", "ssl_no_verify": false}`
	req3 := httptest.NewRequest(http.MethodPut, "/api/settings/git-credentials", bytes.NewBufferString(patchPayload))
	req3.Header.Set("Content-Type", "application/json")
	signReq(req3, secret, "admin", "super_admin", "")
	rec3 := httptest.NewRecorder()
	server.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("partial PUT failed: %d %s", rec3.Code, rec3.Body.String())
	}
	liveCreds2 := scheduler.ResolveGitCredentials()
	if liveCreds2.Token != "glpat-SecretToken99" || liveCreds2.Host != "git.internal.bank" || liveCreds2.SSLNoVerify {
		t.Fatalf("expected token preserved after partial update, got %+v", liveCreds2)
	}

	// 4. Switching to enabled password mode without password returns 400
	badPayload := `{"enabled": true, "auth_type": "password", "username": "u1"}`
	req4 := httptest.NewRequest(http.MethodPut, "/api/settings/git-credentials", bytes.NewBufferString(badPayload))
	req4.Header.Set("Content-Type", "application/json")
	signReq(req4, secret, "admin", "super_admin", "")
	rec4 := httptest.NewRecorder()
	server.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for password mode without password, got %d: %s", rec4.Code, rec4.Body.String())
	}
}

