package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GitStatus describes whether a repo has upstream changes.
type GitStatus struct {
	HasUpdates bool
	LocalHead  string
	RemoteHead string
}

// GitCredentials defines the unified HTTP(S) Git authentication configuration.
type GitCredentials struct {
	Enabled        bool   `json:"enabled"`
	AuthType       string `json:"auth_type"` // "token" or "password"
	Host           string `json:"host"`      // Optional host filter; empty applies to all HTTP(S) repos
	Username       string `json:"username"`
	Password       string `json:"password,omitempty"`
	Token          string `json:"token,omitempty"`
	OverrideInline bool   `json:"override_inline"`
	SSLNoVerify    bool   `json:"ssl_no_verify"`
	Source         string `json:"source,omitempty"` // "database", "config", "env", "none"
}

var (
	gitCredsMu       sync.RWMutex
	gitCredsProvider func() GitCredentials
	staticGitCreds   *GitCredentials
	urlUserInfoRegex = regexp.MustCompile(`(https?://)([^/@\s]+@)`)
)

// SetGitCredentialsProvider registers a dynamic provider (e.g. reading live from SQLite)
// used by GitClone, GitFetchAndDiff, and GitPull. Pass nil to clear.
func SetGitCredentialsProvider(fn func() GitCredentials) {
	gitCredsMu.Lock()
	defer gitCredsMu.Unlock()
	gitCredsProvider = fn
}

// SetGitCredentials overrides static credentials in memory (primarily for tests/startup).
func SetGitCredentials(creds *GitCredentials) {
	gitCredsMu.Lock()
	defer gitCredsMu.Unlock()
	if creds == nil {
		staticGitCreds = nil
		return
	}
	copyVal := *creds
	staticGitCreds = &copyVal
}

// ResolveGitCredentials returns the currently active unified Git credentials.
func ResolveGitCredentials() GitCredentials {
	gitCredsMu.RLock()
	provider := gitCredsProvider
	staticVal := staticGitCreds
	gitCredsMu.RUnlock()

	if provider != nil {
		return provider()
	}
	if staticVal != nil {
		return *staticVal
	}
	return GitCredentialsFromEnv()
}

// GitCredentialsFromEnv reads fallback unified Git credentials from environment variables.
func GitCredentialsFromEnv() GitCredentials {
	user := firstNonEmpty(os.Getenv("GIT_HTTP_USERNAME"), os.Getenv("GIT_USERNAME"))
	pass := firstNonEmpty(os.Getenv("GIT_HTTP_PASSWORD"), os.Getenv("GIT_PASSWORD"))
	token := firstNonEmpty(os.Getenv("GIT_HTTP_TOKEN"), os.Getenv("GIT_TOKEN"))
	host := NormalizeGitHost(os.Getenv("GIT_HTTP_HOST"))
	sslNoVerify := strings.EqualFold(strings.TrimSpace(os.Getenv("GIT_SSL_NO_VERIFY")), "true")

	authType := "token"
	if token == "" && pass != "" {
		authType = "password"
	}
	enabled := token != "" || pass != ""
	source := "none"
	if enabled || sslNoVerify {
		source = "env"
	}

	return GitCredentials{
		Enabled:        enabled,
		AuthType:       authType,
		Host:           host,
		Username:       user,
		Password:       pass,
		Token:          token,
		OverrideInline: true,
		SSLNoVerify:    sslNoVerify,
		Source:         source,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// NormalizeGitHost strips scheme/path/trailing slashes from a host filter string.
func NormalizeGitHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			s = u.Host
		}
	}
	if idx := strings.Index(s, "/"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSuffix(s, "/")
}

// EffectiveGitSecret resolves the username & password/token pair that should be used for auth.
func EffectiveGitSecret(creds GitCredentials) (username, secret string, ok bool) {
	if !creds.Enabled {
		return "", "", false
	}
	user := strings.TrimSpace(creds.Username)
	authType := strings.ToLower(strings.TrimSpace(creds.AuthType))

	switch authType {
	case "password":
		if creds.Password == "" || user == "" {
			return "", "", false
		}
		return user, creds.Password, true
	case "token":
		if creds.Token == "" {
			return "", "", false
		}
		if user == "" {
			user = "oauth2"
		}
		return user, creds.Token, true
	default:
		if creds.Token != "" {
			if user == "" {
				user = "oauth2"
			}
			return user, creds.Token, true
		}
		if user != "" && creds.Password != "" {
			return user, creds.Password, true
		}
		return "", "", false
	}
}

func isHTTPGitURL(rawURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func matchesGitCredsHost(rawURL, hostFilter string) bool {
	if !isHTTPGitURL(rawURL) {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	filter := NormalizeGitHost(hostFilter)
	if filter == "" {
		return true
	}
	if strings.Contains(filter, ":") {
		return strings.EqualFold(u.Host, filter)
	}
	return strings.EqualFold(u.Hostname(), filter)
}

func stripURLUserInfo(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

func hasURLUserInfo(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return u.User != nil && (u.User.Username() != "" || func() bool { _, has := u.User.Password(); return has }())
}

func buildGitAuthCommand(ctx context.Context, dir, targetURL string, creds GitCredentials, args ...string) *exec.Cmd {
	var finalArgs []string
	var extraEnv []string

	if creds.SSLNoVerify {
		finalArgs = append(finalArgs, "-c", "http.sslVerify=false")
	}

	if targetURL == "" && dir != "" {
		if originURL, err := rawGitOutput(ctx, dir, "remote", "get-url", "origin"); err == nil {
			targetURL = originURL
		}
	}

	if isHTTPGitURL(targetURL) && matchesGitCredsHost(targetURL, creds.Host) {
		if user, secret, ok := EffectiveGitSecret(creds); ok {
			// Skip overriding if the URL keeps inline credentials and OverrideInline is false.
			if creds.OverrideInline || !hasURLUserInfo(targetURL) {
				finalArgs = append(finalArgs,
					"-c", "credential.helper=",
					"-c", `credential.helper=!f() { test "$1" = "get" && printf 'username=%s\npassword=%s\n' "$OPENWIKI_GIT_USER" "$OPENWIKI_GIT_PASS"; }; f`,
				)
				extraEnv = append(extraEnv,
					"OPENWIKI_GIT_USER="+user,
					"OPENWIKI_GIT_PASS="+secret,
				)
			}
		}
	}

	finalArgs = append(finalArgs, args...)
	cmd := exec.CommandContext(ctx, "git", finalArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
		"GIT_OPTIONAL_LOCKS=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd
}

func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return buildGitAuthCommand(ctx, dir, "", ResolveGitCredentials(), args...)
}

// RedactGitSecrets scrubs any credentials from error messages and log output.
func RedactGitSecrets(text string, creds GitCredentials) string {
	out := urlUserInfoRegex.ReplaceAllString(text, "${1}***@")
	for _, s := range []string{creds.Password, creds.Token} {
		if s != "" {
			out = strings.ReplaceAll(out, s, "***")
			escaped := url.QueryEscape(s)
			if escaped != "" && escaped != s {
				out = strings.ReplaceAll(out, escaped, "***")
			}
		}
	}
	return out
}

func syncOriginRemoteURLIfOverridden(ctx context.Context, repoPath string, creds GitCredentials) string {
	originURL, err := rawGitOutput(ctx, repoPath, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	if _, _, ok := EffectiveGitSecret(creds); ok && creds.OverrideInline && hasURLUserInfo(originURL) && matchesGitCredsHost(originURL, creds.Host) {
		cleanURL := stripURLUserInfo(originURL)
		if cleanURL != originURL {
			_ = rawRunGit(ctx, repoPath, "remote", "set-url", "origin", cleanURL)
			return cleanURL
		}
	}
	return originURL
}

// GitFetchAndDiff performs a git fetch and compares the local HEAD with the
// remote tracking branch. Returns whether there are new commits to pull.
func GitFetchAndDiff(repoPath, branch string) (*GitStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	creds := ResolveGitCredentials()
	originURL := syncOriginRemoteURLIfOverridden(ctx, repoPath, creds)

	if err := runGitWith(ctx, repoPath, originURL, creds, "fetch", "origin"); err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}

	localHead, err := gitOutput(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}

	remote := fmt.Sprintf("origin/%s", branch)
	remoteHead, err := gitOutput(ctx, repoPath, "rev-parse", remote)
	if err != nil {
		return nil, fmt.Errorf("git rev-parse %s: %w", remote, err)
	}

	return &GitStatus{
		HasUpdates: localHead != remoteHead,
		LocalHead:  localHead,
		RemoteHead: remoteHead,
	}, nil
}

// GitPull performs a git pull on the given repo directory.
func GitPull(repoPath, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	creds := ResolveGitCredentials()
	originURL := syncOriginRemoteURLIfOverridden(ctx, repoPath, creds)

	if branch != "" {
		return runGitWith(ctx, repoPath, originURL, creds, "pull", "origin", branch)
	}
	return runGitWith(ctx, repoPath, originURL, creds, "pull")
}

// GitClone clones a repository to the given local path.
func GitClone(gitURL, localPath, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	creds := ResolveGitCredentials()
	targetURL := strings.TrimSpace(gitURL)
	if _, _, ok := EffectiveGitSecret(creds); ok && creds.OverrideInline && hasURLUserInfo(targetURL) && matchesGitCredsHost(targetURL, creds.Host) {
		targetURL = stripURLUserInfo(targetURL)
	}

	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch, "--single-branch")
	}
	args = append(args, targetURL, localPath)

	cmd := buildGitAuthCommand(ctx, "", targetURL, creds, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %s: %w", RedactGitSecrets(stderr.String(), creds), err)
	}
	return nil
}

// TestGitConnection verifies connectivity and credentials using git ls-remote --heads.
func TestGitConnection(ctx context.Context, gitURL string, creds GitCredentials) error {
	targetURL := strings.TrimSpace(gitURL)
	if targetURL == "" {
		return fmt.Errorf("git_url 不能为空")
	}
	if _, _, ok := EffectiveGitSecret(creds); ok && creds.OverrideInline && hasURLUserInfo(targetURL) && matchesGitCredsHost(targetURL, creds.Host) {
		targetURL = stripURLUserInfo(targetURL)
	}

	cmd := buildGitAuthCommand(ctx, "", targetURL, creds, "ls-remote", "--heads", targetURL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(RedactGitSecrets(stderr.String(), creds))
		if msg == "" {
			msg = RedactGitSecrets(err.Error(), creds)
		}
		return fmt.Errorf("git ls-remote failed: %s", msg)
	}
	return nil
}

// SyncGitCredentialsFile best-effort updates the git-credentials store file if present or configured.
func SyncGitCredentialsFile(creds GitCredentials) error {
	credFile := strings.TrimSpace(os.Getenv("GIT_CREDENTIALS_FILE"))
	if credFile == "" {
		const defaultCredFile = "/root/.git-credentials"
		if _, err := os.Stat(defaultCredFile); err == nil {
			credFile = defaultCredFile
		}
	}
	if credFile == "" {
		return nil
	}

	user, secret, ok := EffectiveGitSecret(creds)
	if !ok || creds.Host == "" {
		return os.WriteFile(credFile, []byte(""), 0600)
	}

	host := NormalizeGitHost(creds.Host)
	u := &url.URL{
		Scheme: "https",
		User:   url.UserPassword(user, secret),
		Host:   host,
	}
	uHTTP := &url.URL{
		Scheme: "http",
		User:   url.UserPassword(user, secret),
		Host:   host,
	}
	if dir := filepath.Dir(credFile); dir != "" {
		_ = os.MkdirAll(dir, 0700)
	}
	content := u.String() + "\n" + uHTTP.String() + "\n"
	return os.WriteFile(credFile, []byte(content), 0600)
}

func injectGitCredentials(rawURL string) string {
	return injectGitCredentialsWith(rawURL, ResolveGitCredentials())
}

func injectGitCredentialsWith(rawURL string, creds GitCredentials) string {
	rawURL = strings.TrimSpace(rawURL)
	if !isHTTPGitURL(rawURL) || !matchesGitCredsHost(rawURL, creds.Host) {
		return rawURL
	}
	user, secret, ok := EffectiveGitSecret(creds)
	if !ok {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.User != nil && !creds.OverrideInline {
		return rawURL
	}
	u.User = url.UserPassword(user, secret)
	return u.String()
}

// GitCurrentHead returns the current HEAD commit hash.
func GitCurrentHead(repoPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return gitOutput(ctx, repoPath, "rev-parse", "HEAD")
}

func runGit(ctx context.Context, dir string, args ...string) error {
	creds := ResolveGitCredentials()
	return runGitWith(ctx, dir, "", creds, args...)
}

func runGitWith(ctx context.Context, dir, targetURL string, creds GitCredentials, args ...string) error {
	cmd := buildGitAuthCommand(ctx, dir, targetURL, creds, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), RedactGitSecrets(stderr.String(), creds))
	}
	return nil
}

func rawRunGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), stderr.String())
	}
	return nil
}

func rawGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCmd(ctx, dir, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

