package scheduler

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEffectiveGitSecretAndHostMatching(t *testing.T) {
	// Token mode defaults username to oauth2
	user, sec, ok := EffectiveGitSecret(GitCredentials{
		Enabled:  true,
		AuthType: "token",
		Token:    "glpat-abc123",
	})
	if !ok || user != "oauth2" || sec != "glpat-abc123" {
		t.Fatalf("unexpected token resolution: ok=%v user=%q sec=%q", ok, user, sec)
	}

	// Password mode requires username and password
	if _, _, ok := EffectiveGitSecret(GitCredentials{Enabled: true, AuthType: "password", Username: "", Password: "p"}); ok {
		t.Fatalf("expected password mode without username to be inactive")
	}
	user, sec, ok = EffectiveGitSecret(GitCredentials{
		Enabled:  true,
		AuthType: "password",
		Username: "svc-wiki",
		Password: "p@ss:word#1",
	})
	if !ok || user != "svc-wiki" || sec != "p@ss:word#1" {
		t.Fatalf("unexpected password resolution: ok=%v user=%q sec=%q", ok, user, sec)
	}

	// Host matching
	if !matchesGitCredsHost("https://gitlab.company.com/team/app.git", "") {
		t.Fatalf("empty host filter should match all HTTPS URLs")
	}
	if !matchesGitCredsHost("https://gitlab.company.com:8443/team/app.git", "https://gitlab.company.com/") {
		t.Fatalf("expected normalized host match without port")
	}
	if matchesGitCredsHost("https://github.com/team/app.git", "gitlab.company.com") {
		t.Fatalf("unexpected host match for different host")
	}
	if matchesGitCredsHost("git@gitlab.company.com:team/app.git", "") {
		t.Fatalf("SSH URL should not match HTTP credential filter")
	}
}

func TestGitAuthCommandRealHTTPServer(t *testing.T) {
	const expectedUser = "svc-git"
	const expectedPass = "s3cr3t@token#1!"
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(expectedUser+":"+expectedPass))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Create a real bare repo with 1 commit and dumb-HTTP server-info
	workDir := t.TempDir()
	bareDir := filepath.Join(t.TempDir(), "repo.git")
	if err := rawRunGit(ctx, workDir, "init"); err != nil {
		t.Fatalf("git init work: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	_ = rawRunGit(ctx, workDir, "config", "user.email", "test@openwiki.local")
	_ = rawRunGit(ctx, workDir, "config", "user.name", "OpenWiki Test")
	if err := rawRunGit(ctx, workDir, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := rawRunGit(ctx, workDir, "commit", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	branchName, err := rawGitOutput(ctx, workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse branch: %v", err)
	}
	if err := rawRunGit(ctx, "", "clone", "--bare", workDir, bareDir); err != nil {
		t.Fatalf("git clone --bare: %v", err)
	}
	if err := rawRunGit(ctx, bareDir, "update-server-info"); err != nil {
		t.Fatalf("git update-server-info: %v", err)
	}

	var authedHits atomic.Int32
	fs := http.FileServer(http.Dir(bareDir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expectedAuth {
			w.Header().Set("WWW-Authenticate", `Basic realm="git-test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authedHits.Add(1)
		fs.ServeHTTP(w, r)
	}))
	defer srv.Close()

	// 1. Valid unified credentials should authenticate real git ls-remote cleanly
	creds := GitCredentials{
		Enabled:        true,
		AuthType:       "token",
		Username:       expectedUser,
		Token:          expectedPass,
		OverrideInline: true,
	}
	SetGitCredentials(&creds)
	defer SetGitCredentials(nil)

	// Pass stale inline credentials in URL to prove OverrideInline replaces them
	urlWithOldCreds := strings.Replace(srv.URL, "http://", "http://olduser:oldpass@", 1)
	if err := TestGitConnection(ctx, urlWithOldCreds, creds); err != nil {
		t.Fatalf("expected TestGitConnection to succeed, got: %v", err)
	}
	if authedHits.Load() == 0 {
		t.Fatalf("expected authenticated HTTP hit on test git server")
	}

	// 2. GitClone + GitFetchAndDiff + GitPull using dynamic unified credentials
	clonedDir := filepath.Join(t.TempDir(), "cloned")
	if err := GitClone(urlWithOldCreds, clonedDir, branchName); err != nil {
		t.Fatalf("GitClone failed: %v", err)
	}
	// Verify origin stored in .git/config has NO plaintext secret
	storedOrigin, err := rawGitOutput(ctx, clonedDir, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("get origin url failed: %v", err)
	}
	if strings.Contains(storedOrigin, expectedPass) || strings.Contains(storedOrigin, "oldpass") {
		t.Fatalf("origin URL in .git/config leaked credentials: %s", storedOrigin)
	}
	status, err := GitFetchAndDiff(clonedDir, branchName)
	if err != nil {
		t.Fatalf("GitFetchAndDiff failed: %v", err)
	}
	if status.HasUpdates {
		t.Fatalf("expected no updates right after clone")
	}
	if err := GitPull(clonedDir, branchName); err != nil {
		t.Fatalf("GitPull failed: %v", err)
	}

	// 3. Wrong secret must fail and NEVER leak the secret in error message
	badCreds := GitCredentials{
		Enabled:  true,
		AuthType: "password",
		Username: expectedUser,
		Password: "wrong-secret-value-xyz",
	}
	err = TestGitConnection(ctx, srv.URL, badCreds)
	if err == nil {
		t.Fatalf("expected TestGitConnection to fail with wrong password")
	}
	if strings.Contains(err.Error(), "wrong-secret-value-xyz") {
		t.Fatalf("error leaked secret: %s", err.Error())
	}
}

func TestSyncOriginRemoteURLIfOverridden(t *testing.T) {
	tmpDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := rawRunGit(ctx, tmpDir, "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	if err := rawRunGit(ctx, tmpDir, "remote", "add", "origin", "https://olduser:oldsecret@gitlab.company.com/group/app.git"); err != nil {
		t.Fatalf("git remote add failed: %v", err)
	}

	creds := GitCredentials{
		Enabled:        true,
		AuthType:       "token",
		Token:          "newsecret",
		OverrideInline: true,
	}
	cleaned := syncOriginRemoteURLIfOverridden(ctx, tmpDir, creds)
	expected := "https://gitlab.company.com/group/app.git"
	if cleaned != expected {
		t.Fatalf("expected cleaned origin %q, got %q", expected, cleaned)
	}

	actual, err := rawGitOutput(ctx, tmpDir, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("git remote get-url failed: %v", err)
	}
	if actual != expected {
		t.Fatalf("expected stored origin %q, got %q", expected, actual)
	}
}

func TestSyncGitCredentialsFileAndRedaction(t *testing.T) {
	credFile := filepath.Join(t.TempDir(), ".git-credentials")
	t.Setenv("GIT_CREDENTIALS_FILE", credFile)

	creds := GitCredentials{
		Enabled:  true,
		AuthType: "password",
		Host:     "https://gitlab.company.com/group",
		Username: "svc-user",
		Password: "p@ss:word#1",
	}
	if err := SyncGitCredentialsFile(creds); err != nil {
		t.Fatalf("SyncGitCredentialsFile failed: %v", err)
	}

	raw, err := os.ReadFile(credFile)
	if err != nil {
		t.Fatalf("read cred file failed: %v", err)
	}
	if !strings.Contains(string(raw), "https://svc-user:p%40ss%3Aword%231@gitlab.company.com") {
		t.Fatalf("expected URL-encoded credentials in file, got %q", string(raw))
	}

	redacted := RedactGitSecrets("fatal: could not read from https://svc-user:p@ss:word#1@gitlab.company.com/a.git (token p@ss:word#1)", creds)
	if strings.Contains(redacted, "p@ss:word#1") {
		t.Fatalf("secret not redacted: %s", redacted)
	}
}
