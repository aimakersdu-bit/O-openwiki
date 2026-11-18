package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BuildMode specifies the execution granularity for building a repository wiki.
type BuildMode string

const (
	// BuildModeFull performs code diff checks, init/update LLM wiki generation, knowledge ingestion, and static graph export.
	BuildModeFull BuildMode = "full"
	// BuildModeKnowledgeOnly skips heavy LLM code scans (init/update) and only ingests custom knowledge & exports static visualizer assets.
	BuildModeKnowledgeOnly BuildMode = "knowledge-only"
)

// Builder handles the openwiki update and static export process for a single repo.
type Builder struct {
	mu              sync.RWMutex
	OpenwikiCLI     string // path to openwiki CLI
	OpenwikiDistDir string // path to openwiki dist/ directory (for visualize assets)
	VendorAssetsDir string // path to pre-downloaded vendor/ assets
	StaticOutputDir string // base output dir (e.g. /var/www/openwiki-static)
	DefaultLanguage string // default language tag for init/update (e.g. "zh-CN")
	LLMBaseURL      string // optional base URL for internal LLM proxy
	LLMTimeoutSec   int    // configured per-request LLM timeout in seconds (default 600)
	OpenwikiModel   string // configured default model id (e.g. "glm-5.2")

	buildTimeoutSec int  // hard cap per openwiki CLI invocation (0 = unlimited)
	buildTimeoutSet bool // whether buildTimeoutSec was explicitly configured
}

// NewBuilder creates a new Builder instance.
func NewBuilder(cli, vendorDir, distDir, outputDir string, defaultLang ...string) *Builder {
	if cli == "" {
		cli = "openwiki"
	}
	if outputDir == "" {
		outputDir = "/var/www/openwiki-static"
	}
	lang := "zh-CN"
	if len(defaultLang) > 0 && defaultLang[0] != "" {
		lang = defaultLang[0]
	}
	return &Builder{
		OpenwikiCLI:     cli,
		VendorAssetsDir: vendorDir,
		OpenwikiDistDir: distDir,
		StaticOutputDir: outputDir,
		DefaultLanguage: lang,
		LLMTimeoutSec:   600,
	}
}

// SetLLMBaseURL sets the base URL for internal LLM gateway forwarding.
func (b *Builder) SetLLMBaseURL(url string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.LLMBaseURL = url
}

// SetLLMTimeoutSec updates the LLM request timeout propagated to openwiki CLI subprocesses.
func (b *Builder) SetLLMTimeoutSec(sec int) {
	if sec <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.LLMTimeoutSec = sec
}

// GetLLMTimeoutSec returns the configured LLM request timeout in seconds (default 600).
func (b *Builder) GetLLMTimeoutSec() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.LLMTimeoutSec <= 0 {
		return 600
	}
	return b.LLMTimeoutSec
}

// BuildResult holds the outcome of a build.
type BuildResult struct {
	Success bool
	Log     string
	Error   string
	GitHead string
}

// BuildRepo runs the full build pipeline for a single repository:
//  1. openwiki code --update --print --language zh-CN (incremental wiki generation)
//  2. Export graph.json via buildGraph()
//  3. Copy visualizer frontend assets (PAGE HTML + client.js + client-lib.js)
//  4. Copy pre-downloaded vendor/ libraries for intranet offline use
//
// logStep prints to console log and appends to in-memory log buffer, triggering onProgress if provided.
func (b *Builder) logStep(repoID, msg string, logBuf *strings.Builder, onProgress ...func(string)) {
	log.Printf("[build][%s] %s", repoID, msg)
	logBuf.WriteString(msg)
	if !strings.HasSuffix(msg, "\n") {
		logBuf.WriteString("\n")
	}
	if len(onProgress) > 0 && onProgress[0] != nil {
		onProgress[0](logBuf.String())
	}
}

// createOpenWikiCmd constructs an exec.Cmd with enriched PATH and OPENWIKI_HOME environment.
func (b *Builder) createOpenWikiCmd(repoPath string, args ...string) *exec.Cmd {
	return b.createOpenWikiCmdWithBuildID(0, repoPath, args...)
}

// createOpenWikiCmdWithBuildID constructs an exec.Cmd with LLM proxy environment variables and build attribution.
func (b *Builder) createOpenWikiCmdWithBuildID(buildID int64, repoPath string, args ...string) *exec.Cmd {
	return b.createOpenWikiCmdWithHome(buildID, "", repoPath, args...)
}

// createOpenWikiCmdWithHome constructs an exec.Cmd with LLM proxy environment variables, build attribution, and isolated task OPENWIKI_HOME.
func (b *Builder) createOpenWikiCmdWithHome(buildID int64, taskHomeDir, repoPath string, args ...string) *exec.Cmd {
	cli := b.OpenwikiCLI
	if cli == "" {
		cli = "openwiki"
	}

	var cmd *exec.Cmd

	// If cli is not absolute, attempt to locate openwiki binary or fall back to node dist/cli/cli.js
	if !filepath.IsAbs(cli) {
		searchPaths := []string{
			"/usr/local/bin/openwiki",
			"/opt/homebrew/bin/openwiki",
			filepath.Join(os.Getenv("HOME"), ".openwiki", "bin", "openwiki"),
		}
		found := false
		for _, p := range searchPaths {
			if _, err := os.Stat(p); err == nil {
				cli = p
				found = true
				break
			}
		}

		if !found {
			// Fallback to node dist/cli/cli.js if distDir is known
			cliJS := ""
			if b.OpenwikiDistDir != "" {
				cliJS = filepath.Join(b.OpenwikiDistDir, "cli", "cli.js")
			}
			if cliJS != "" {
				if _, err := os.Stat(cliJS); err == nil {
					cmd = exec.Command("node", append([]string{cliJS}, args...)...)
				}
			}
		}
	}

	if cmd == nil {
		cmd = exec.Command(cli, args...)
	}

	cmd.Dir = repoPath

	// Environment setup
	env := os.Environ()
	// Prepend the openwiki CLI's directory to PATH so that `#!/usr/bin/env node`
	// resolves to the correct Node version bundled with the CLI.
	cliDir := ""
	if b.OpenwikiCLI != "" {
		cliDir = filepath.Dir(b.OpenwikiCLI)
	}
	hasPath := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			extra := "/usr/local/bin:/opt/homebrew/bin"
			if cliDir != "" {
				env[i] = "PATH=" + cliDir + ":" + strings.TrimPrefix(e, "PATH=") + ":" + extra
			} else {
				env[i] = e + ":" + extra
			}
			hasPath = true
			break
		}
	}
	if !hasPath {
		base := "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
		if cliDir != "" {
			env = append(env, "PATH="+cliDir+":"+base)
		} else {
			env = append(env, "PATH="+base)
		}
	}
	if b.OpenwikiDistDir != "" {
		env = append(env, fmt.Sprintf("OPENWIKI_DIST_DIR=%s", b.OpenwikiDistDir))
	}

	// Task-isolated OPENWIKI_HOME & OPENWIKI_CONFIG_DIR prevents concurrent SQLite lock contention
	if taskHomeDir != "" {
		env = upsertEnv(env, "OPENWIKI_HOME", taskHomeDir)
		env = upsertEnv(env, "OPENWIKI_CONFIG_DIR", taskHomeDir)
	} else if os.Getenv("OPENWIKI_HOME") == "" && os.Getenv("OPENWIKI_CONFIG_DIR") == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			defaultHome := filepath.Join(home, ".openwiki")
			env = append(env, fmt.Sprintf("OPENWIKI_HOME=%s", defaultHome))
			env = append(env, fmt.Sprintf("OPENWIKI_CONFIG_DIR=%s", defaultHome))
		}
	}

	// Inject Node v22 ESM json import shim & require polyfill via NODE_OPTIONS if loader file exists
	shimCandidates := []string{
		filepath.Join(filepath.Dir(b.OpenwikiDistDir), "deploy", "unit1-orchestrator", "scripts", "json-import-shim.mjs"),
		"/Users/bigc/openwiki/deploy/unit1-orchestrator/scripts/json-import-shim.mjs",
	}
	for _, shim := range shimCandidates {
		if _, err := os.Stat(shim); err == nil {
			nodeOpts := fmt.Sprintf("NODE_OPTIONS=--import=%s --experimental-loader=%s", shim, shim)
			if existingOpts := os.Getenv("NODE_OPTIONS"); existingOpts != "" {
				nodeOpts = fmt.Sprintf("NODE_OPTIONS=%s --import=%s --experimental-loader=%s", existingOpts, shim, shim)
			}
			env = append(env, nodeOpts)
			break
		}
	}

	// LLM Proxy & Timeout Environment Injection
	b.mu.RLock()
	llmBaseURL := b.LLMBaseURL
	llmTimeoutSec := b.LLMTimeoutSec
	openwikiModel := b.OpenwikiModel
	b.mu.RUnlock()
	if llmTimeoutSec <= 0 {
		llmTimeoutSec = 600
	}

	if openwikiModel != "" {
		env = setEnvIfUnset(env, "OPENWIKI_MODEL_ID", openwikiModel)
	}

	if llmBaseURL != "" {
		baseURL := strings.TrimSuffix(llmBaseURL, "/")
		if buildID > 0 {
			baseURL = fmt.Sprintf("%s/build/%d", baseURL, buildID)
		}
		env = upsertEnv(env, "OPENAI_BASE_URL", baseURL)
		env = upsertEnv(env, "OPENAI_COMPATIBLE_BASE_URL", baseURL)

		// When routing through LLMProxy, default to standard OpenAI-compatible gateway (/v1/chat/completions)
		env = setEnvIfUnset(env, "OPENWIKI_PROVIDER", "openai-compatible")
		env = setEnvIfUnset(env, "OPENWIKI_OPENAI_COMPATIBLE_STREAMING", "true")

		// When routing through LLMProxy, give the outer CLI client more headroom than
		// a single proxy attempt timeout so proxy retries/backoff complete without racing,
		// and keep inner CLI retries minimal unless explicitly overridden by the user.
		cliTimeoutSec := llmTimeoutSec*2 + 120
		env = upsertEnv(env, "OPENWIKI_LLM_TIMEOUT_SEC", fmt.Sprintf("%d", cliTimeoutSec))
		env = setEnvIfUnset(env, "OPENWIKI_PROVIDER_RETRY_ATTEMPTS", "1")
	} else {
		env = upsertEnv(env, "OPENWIKI_LLM_TIMEOUT_SEC", fmt.Sprintf("%d", llmTimeoutSec))
	}
	if buildID > 0 {
		keyVal := fmt.Sprintf("ow-build-%d", buildID)
		env = upsertEnv(env, "OPENAI_API_KEY", keyVal)
		env = upsertEnv(env, "OPENAI_COMPATIBLE_API_KEY", keyVal)
	} else if llmBaseURL != "" {
		// Provide placeholder credentials for buildID == 0 so CLI provider checks pass
		env = setEnvIfUnset(env, "OPENAI_API_KEY", "ow-build-0")
		env = setEnvIfUnset(env, "OPENAI_COMPATIBLE_API_KEY", "ow-build-0")
	}
	// Avoid 30s PostHog shutdown timeout hangs in private / air-gapped environments unless explicitly configured.
	env = setEnvIfUnset(env, "OPENWIKI_TELEMETRY_DISABLED", "1")
	// Enable verbose CLI diagnostic logs for evidence in private / air-gapped environments
	env = setEnvIfUnset(env, "OPENWIKI_DEBUG", "1")

	cmd.Env = env

	return cmd
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func setEnvIfUnset(env []string, key, value string) []string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

// resolveSiblingTmpDir locates or computes the .tmp directory sibling to the current .openwiki directory.
// Returns the .tmp directory path and the base .openwiki directory path.
func resolveSiblingTmpDir() (string, string) {
	baseOpenwiki := os.Getenv("OPENWIKI_CONFIG_DIR")
	if baseOpenwiki == "" {
		baseOpenwiki = os.Getenv("OPENWIKI_HOME")
	}
	if baseOpenwiki == "" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			baseOpenwiki = filepath.Join(home, ".openwiki")
		}
	}
	if baseOpenwiki == "" {
		baseOpenwiki = ".openwiki"
	}
	baseOpenwiki = filepath.Clean(baseOpenwiki)

	var tmpParent string
	if filepath.Base(baseOpenwiki) == ".openwiki" {
		tmpParent = filepath.Dir(baseOpenwiki)
	} else {
		tmpParent = baseOpenwiki
	}
	return filepath.Join(tmpParent, ".tmp"), baseOpenwiki
}

// BuildRepo runs the full build pipeline for a single repository.
func (b *Builder) BuildRepo(repoID, repoPath, wikiDir string, onProgress ...func(string)) *BuildResult {
	return b.BuildRepoWithBuildID(0, repoID, repoPath, wikiDir, onProgress...)
}

// BuildRepoWithBuildID runs the full build pipeline for a single repository with build attribution.
func (b *Builder) BuildRepoWithBuildID(buildID int64, repoID, repoPath, wikiDir string, onProgress ...func(string)) *BuildResult {
	return b.BuildRepoWithMode(buildID, repoID, repoPath, wikiDir, BuildModeFull, onProgress...)
}

// BuildRepoWithMode runs the build pipeline for a repository according to the specified BuildMode.
func (b *Builder) BuildRepoWithMode(buildID int64, repoID, repoPath, wikiDir string, mode BuildMode, onProgress ...func(string)) *BuildResult {
	return b.BuildRepoWithModeCtx(context.Background(), buildID, repoID, repoPath, wikiDir, mode, onProgress...)
}

// BuildRepoWithModeCtx is BuildRepoWithMode with cancellation: cancelling ctx (e.g. CancelBatch)
// kills the running openwiki CLI process group.
func (b *Builder) BuildRepoWithModeCtx(ctx context.Context, buildID int64, repoID, repoPath, wikiDir string, mode BuildMode, onProgress ...func(string)) *BuildResult {
	var logBuf strings.Builder
	result := &BuildResult{}

	lang := b.DefaultLanguage
	if lang == "" {
		lang = "zh-CN"
	}

	// Resolve wiki directory
	if wikiDir == "" {
		wikiDir = filepath.Join(repoPath, "openwiki")
	}

	// Resolve static output directory for this repo, and create a staging directory for zero-downtime atomic swap
	finalStaticDir := filepath.Join(b.StaticOutputDir, repoID)
	defer InvalidateCompletenessCache(finalStaticDir, wikiDir)
	stagingDir := filepath.Join(b.StaticOutputDir, fmt.Sprintf("%s_tmp_%d", strings.ReplaceAll(repoID, "/", "_"), time.Now().UnixNano()))

	// Setup isolated temporary OPENWIKI_HOME under sibling .tmp directory (only when CLI execution is needed)
	var taskHomeDir string
	if mode != BuildModeKnowledgeOnly {
		tmpRootDir, baseOpenwiki := resolveSiblingTmpDir()
		_ = os.MkdirAll(tmpRootDir, 0755)

		safeRepoName := strings.ReplaceAll(strings.ReplaceAll(repoID, "/", "_"), "\\", "_")
		taskHomeDir = filepath.Join(tmpRootDir, fmt.Sprintf("build_%d_%s_%d", buildID, safeRepoName, time.Now().UnixNano()))
		if err := os.MkdirAll(taskHomeDir, 0700); err != nil {
			b.logStep(repoID, fmt.Sprintf("WARNING: failed to create task home %s: %v, falling back to base home", taskHomeDir, err), &logBuf, onProgress...)
			taskHomeDir = ""
		} else if baseOpenwiki != "" {
			// Inherit base .env if present (e.g. DeepSeek tokens or private configs)
			srcEnv := filepath.Join(baseOpenwiki, ".env")
			if envData, err := os.ReadFile(srcEnv); err == nil {
				_ = os.WriteFile(filepath.Join(taskHomeDir, ".env"), envData, 0600)
			}
		}
	}

	defer func() {
		// Clean up staging directory if it still exists (e.g. on build failure)
		if _, err := os.Stat(stagingDir); err == nil {
			os.RemoveAll(stagingDir)
		}

		// Clean up task temporary home ONLY if build succeeded without any CLI error.
		// If init or update failed (even if offline static export continued), preserve for troubleshooting.
		if taskHomeDir != "" {
			if result.Success && result.Error == "" {
				if _, err := os.Stat(taskHomeDir); err == nil {
					_ = os.RemoveAll(taskHomeDir)
				}
			} else {
				b.logStep(repoID, fmt.Sprintf("Build failed; preserving temporary home directory for troubleshooting: %s", taskHomeDir), &logBuf, onProgress...)
			}
		}
		// Always sync final logBuf snapshot so deferred troubleshooting messages are persisted in DB
		result.Log = logBuf.String()
	}()

	ranInit := false
	if mode != BuildModeKnowledgeOnly {
		// Step 0: Check if wiki directory is complete; if not, run openwiki code --init --print first
		if !b.IsWikiComplete(wikiDir) {
			b.logStep(repoID, fmt.Sprintf("=== Step 0: openwiki code --init --print --language %s ===", lang), &logBuf, onProgress...)
			cmdInit := b.createOpenWikiCmdWithHome(buildID, taskHomeDir, repoPath, "code", "--init", "--print", "--language", lang)
			if err := b.runCommandWithStreaming(ctx, cmdInit, repoID, &logBuf, onProgress...); err != nil {
				result.Log = logBuf.String()
				result.Error = fmt.Sprintf("openwiki init failed: %v", err)
				b.logStep(repoID, "ERROR: "+result.Error, &logBuf, onProgress...)
				return result
			}
			b.logStep(repoID, "openwiki init completed successfully", &logBuf, onProgress...)
			ranInit = true
		}
	} else {
		b.logStep(repoID, "Knowledge-only build mode active: skipping openwiki code --init", &logBuf, onProgress...)
	}

	// Step 0.5: Knowledge Ingestion (doc/knowledge -> openwiki/knowledge, symlink-based, non-invasive)
	b.logStep(repoID, "=== Step 0.5: Knowledge Ingestion ===", &logBuf, onProgress...)
	if ingested := b.ingestKnowledge(repoID, repoPath, &logBuf, onProgress...); ingested {
		b.logStep(repoID, "Knowledge documents linked to openwiki/knowledge/", &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "No doc/knowledge directory found, skipping knowledge ingestion", &logBuf, onProgress...)
	}

	// Step 1: Run openwiki code --update --print --language <lang> (skip if init just ran or in knowledge-only mode)
	if mode == BuildModeKnowledgeOnly {
		b.logStep(repoID, "Knowledge-only mode: skipping Step 1 openwiki code --update", &logBuf, onProgress...)
	} else if ranInit {
		b.logStep(repoID, "=== Step 1: openwiki code --update skipped ===", &logBuf, onProgress...)
		b.logStep(repoID, "openwiki init already generated fresh wiki, skipping redundant openwiki update", &logBuf, onProgress...)
	} else {
		b.logStep(repoID, fmt.Sprintf("=== Step 1: openwiki code --update --print --language %s ===", lang), &logBuf, onProgress...)
		cmd := b.createOpenWikiCmdWithHome(buildID, taskHomeDir, repoPath, "code", "--update", "--print", "--language", lang)
		if err := b.runCommandWithStreaming(ctx, cmd, repoID, &logBuf, onProgress...); err != nil {
			result.Error = fmt.Sprintf("openwiki update failed: %v", err)
			b.logStep(repoID, "WARNING: "+result.Error+" (continuing with offline graph export & static site release to ensure custom knowledge docs are published)", &logBuf, onProgress...)
		} else {
			b.logStep(repoID, "openwiki update completed successfully", &logBuf, onProgress...)
		}
	}

	// Get current git head
	head, _ := GitCurrentHead(repoPath)
	result.GitHead = head

	// Step 2: Export graph.json into staging directory
	b.logStep(repoID, "=== Step 2: Export graph.json ===", &logBuf, onProgress...)
	if err := b.exportGraph(wikiDir, stagingDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("graph export failed: %v", err), &logBuf, onProgress...)
		// Non-fatal: continue with other steps
	} else {
		b.logStep(repoID, "graph.json exported successfully", &logBuf, onProgress...)
	}

	// Step 3: Copy visualizer frontend assets into staging directory
	b.logStep(repoID, "=== Step 3: Copy visualizer assets ===", &logBuf, onProgress...)
	if err := b.copyVisualizerAssets(stagingDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("copy visualizer assets failed: %v", err), &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "visualizer assets copied successfully", &logBuf, onProgress...)
	}
	b.copyKnowledgeAssets(wikiDir, stagingDir, &logBuf)

	// Step 4: Copy vendor libraries (intranet offline) into staging directory
	b.logStep(repoID, "=== Step 4: Copy vendor libraries ===", &logBuf, onProgress...)
	if err := b.copyVendorAssets(stagingDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("copy vendor assets failed: %v", err), &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "vendor libraries copied successfully", &logBuf, onProgress...)
	}

	// Step 5: Atomic Directory Swap (Zero-Downtime Release)
	b.logStep(repoID, "=== Step 5: Atomic directory swap ===", &logBuf, onProgress...)
	b.EnsureStaticPermissions(stagingDir)
	if err := os.MkdirAll(filepath.Dir(finalStaticDir), 0755); err != nil {
		b.logStep(repoID, fmt.Sprintf("mkdir static base failed: %v", err), &logBuf, onProgress...)
	}
	oldDir := finalStaticDir + "_old"
	_ = os.RemoveAll(oldDir)

	if _, err := os.Stat(finalStaticDir); err == nil {
		if err := os.Rename(finalStaticDir, oldDir); err != nil {
			b.logStep(repoID, fmt.Sprintf("rename active static dir failed: %v", err), &logBuf, onProgress...)
		}
	}

	if err := os.Rename(stagingDir, finalStaticDir); err != nil {
		b.logStep(repoID, fmt.Sprintf("atomic swap staging to final failed: %v", err), &logBuf, onProgress...)
		// Rollback if rename failed
		if _, errOld := os.Stat(oldDir); errOld == nil {
			_ = os.Rename(oldDir, finalStaticDir)
		}
	} else {
		_ = os.RemoveAll(oldDir)
		b.EnsureStaticPermissions(finalStaticDir)
		b.logStep(repoID, "atomic swap completed successfully", &logBuf, onProgress...)
	}

	result.Success = true
	result.Log = logBuf.String()
	b.logStep(repoID, "=== BUILD SUCCESSFUL ===", &logBuf, onProgress...)
	return result
}

// DeployVisualizer populates staticDir with graph.json, index.html, client.js, client-lib.js and vendor assets for wikiDir.
func (b *Builder) DeployVisualizer(wikiDir, staticDir string) error {
	var logBuf strings.Builder
	_ = b.exportGraph(wikiDir, staticDir, &logBuf)
	_ = b.copyVisualizerAssets(staticDir, &logBuf)
	b.copyKnowledgeAssets(wikiDir, staticDir, &logBuf)
	_ = b.copyVendorAssets(staticDir, &logBuf)
	b.EnsureStaticPermissions(staticDir)
	return nil
}

// resolveVisualizerFile locates a visualizer asset (e.g. client.js, client-lib.js, index.html, graph.js)
// across configured assets/dist directories and standard offline installation paths.
func (b *Builder) resolveVisualizerFile(filename string) string {
	assetsDir := filepath.Dir(b.VendorAssetsDir)
	var candidates []string
	if assetsDir != "" && assetsDir != "." {
		candidates = append(candidates, filepath.Join(assetsDir, filename))
	}
	if b.OpenwikiDistDir != "" {
		candidates = append(candidates, filepath.Join(b.OpenwikiDistDir, "visualize", filename))
	}
	candidates = append(candidates,
		filepath.Join("/app/assets", filename),
		filepath.Join("/opt/openwiki/assets", filename),
		filepath.Join("/usr/local/lib/node_modules/openwiki/dist/visualize", filename),
		filepath.Join("/opt/homebrew/lib/node_modules/openwiki/dist/visualize", filename),
	)
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	if assetsDir != "" && assetsDir != "." {
		return filepath.Join(assetsDir, filename)
	}
	return filepath.Join(b.OpenwikiDistDir, "visualize", filename)
}

// exportGraph calls node to run buildGraph() and writes the JSON output.
func (b *Builder) exportGraph(wikiDir, staticDir string, log *strings.Builder) error {
	graphJS := b.resolveVisualizerFile("graph.js")
	importTarget := "./dist/visualize/graph.js"
	if absGraphJS, err := filepath.Abs(graphJS); err == nil {
		if _, statErr := os.Stat(absGraphJS); statErr == nil {
			importTarget = absGraphJS
		}
	}

	script := fmt.Sprintf(`
		import(%q)
			.then(m => m.buildGraph(%q))
			.then(g => process.stdout.write(JSON.stringify(g)))
	`, importTarget, wikiDir)

	cmd := exec.Command("node", "--input-type=module", "-e", script)
	if b.OpenwikiDistDir != "" {
		if info, err := os.Stat(filepath.Dir(b.OpenwikiDistDir)); err == nil && info.IsDir() {
			cmd.Dir = filepath.Dir(b.OpenwikiDistDir)
		}
	}
	graphJSON, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("node buildGraph: %w", err)
	}

	// Write to staticDir/api/graph (no extension, matching client fetch path)
	apiDir := filepath.Join(staticDir, "api")
	if err := os.MkdirAll(apiDir, 0755); err != nil {
		return err
	}
	_ = os.Chmod(apiDir, 0755)
	graphFile := filepath.Join(apiDir, "graph")
	if err := os.WriteFile(graphFile, graphJSON, 0644); err != nil {
		return err
	}
	_ = os.Chmod(graphFile, 0644)
	return nil
}

// patchClientJS post-processes the visualizer client.js for static SPA export:
//  1. Rewrites fetch("/api/graph") and EventSource("/events") to relative URLs
//  2. Patches rewriteLinks to resolve absolute /openwiki/*.md links to graph node IDs
func patchClientJS(content string) string {
	content = strings.Replace(content, `fetch("/api/graph")`, `fetch(new URL("api/graph", window.location.href).href)`, 1)
	content = strings.Replace(content, `new EventSource("/events")`, `new EventSource(new URL("events", window.location.href).href)`, 1)
	// Patch rewriteLinks: absolute paths like /openwiki/architecture/foo.md should
	// resolve directly to node IDs (architecture/foo) without going through normalize().
	oldTarget := `const target = normalize(dir, clean).replace(/\.md$/, "");`
	newTarget := `const isAbs = clean.startsWith("/");` +
		`const cleanPath = clean.replace(/^\/?(?:openwiki\/)?/, "");` +
		`const target = isAbs ? cleanPath.replace(/\.md$/, "") : normalize(dir, cleanPath).replace(/\.md$/, "");`
	content = strings.Replace(content, oldTarget, newTarget, 1)
	return content
}

// copyVisualizerAssets copies the compiled client.js, client-lib.js from openwiki assets/dist.
func (b *Builder) copyVisualizerAssets(staticDir string, log *strings.Builder) error {
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		return err
	}
	_ = os.Chmod(staticDir, 0755)

	clientSrc := b.resolveVisualizerFile("client.js")
	clientLibSrc := b.resolveVisualizerFile("client-lib.js")

	assets := map[string]string{
		"client.js":     clientSrc,
		"client-lib.js": clientLibSrc,
	}

	for name, src := range assets {
		dst := filepath.Join(staticDir, name)
		if err := copyFile(src, dst); err != nil {
			log.WriteString(fmt.Sprintf("  skip %s: %v\n", name, err))
		} else {
			log.WriteString(fmt.Sprintf("  copied %s\n", name))
		}
	}

	// Post-process static client.js for exported static SPA
	clientDst := filepath.Join(staticDir, "client.js")
	if data, err := os.ReadFile(clientDst); err == nil {
		patched := patchClientJS(string(data))
		_ = os.WriteFile(clientDst, []byte(patched), 0644)
		_ = os.Chmod(clientDst, 0644)
	}

	// Always copy index.html template to static output directory
	indexPath := filepath.Join(staticDir, "index.html")
	templatePath := b.resolveVisualizerFile("index.html")
	if err := copyFile(templatePath, indexPath); err != nil {
		log.WriteString(fmt.Sprintf("  skip index.html: %v\n", err))
	} else {
		log.WriteString("  copied index.html\n")
	}

	return nil
}

// copyVendorAssets copies pre-downloaded vendor/ JS libraries for intranet use.
func (b *Builder) copyVendorAssets(staticDir string, log *strings.Builder) error {
	vendorSrc := b.VendorAssetsDir
	vendorDst := filepath.Join(staticDir, "vendor")

	if _, err := os.Stat(vendorSrc); os.IsNotExist(err) {
		return fmt.Errorf("vendor assets dir not found: %s", vendorSrc)
	}

	if err := os.MkdirAll(vendorDst, 0755); err != nil {
		return err
	}
	_ = os.Chmod(vendorDst, 0755)

	entries, err := os.ReadDir(vendorSrc)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(vendorSrc, entry.Name())
		dst := filepath.Join(vendorDst, entry.Name())
		if err := copyFile(src, dst); err != nil {
			log.WriteString(fmt.Sprintf("  skip vendor/%s: %v\n", entry.Name(), err))
		} else {
			log.WriteString(fmt.Sprintf("  copied vendor/%s\n", entry.Name()))
		}
	}

	return nil
}

// copyFile copies a single file from src to dst and explicitly sets 0644 permissions
// so Nginx worker processes (e.g. www-data/nginx) can always read it regardless of OS umask.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, 0644)
}

// EnsureStaticPermissions forces 0755 on all directories (including ancestor directories up to
// StaticOutputDir and its parent) and 0644 on all regular files under targetDir, bypassing restrictive OS umasks (0027/0077).
func (b *Builder) EnsureStaticPermissions(targetDir string) {
	if b.StaticOutputDir != "" {
		parent := filepath.Dir(b.StaticOutputDir)
		if parent != "" && parent != "/" && parent != "." {
			_ = os.Chmod(parent, 0755)
		}
		_ = os.Chmod(b.StaticOutputDir, 0755)

		// Also chmod intermediate directories between StaticOutputDir and targetDir (e.g. org/group subdirs)
		if rel, err := filepath.Rel(b.StaticOutputDir, targetDir); err == nil && !strings.HasPrefix(rel, "..") {
			cur := b.StaticOutputDir
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if part == "" || part == "." {
					continue
				}
				cur = filepath.Join(cur, part)
				_ = os.Chmod(cur, 0755)
			}
		}
	}

	_ = filepath.Walk(targetDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(p, 0755)
		} else {
			_ = os.Chmod(p, 0644)
		}
		return nil
	})
}

// RepairStaticOutputDir repairs permissions (0755 for dirs, 0644 for files) across the entire
// StaticOutputDir and backfills missing client-lib.js into any previously built static wiki directory.
func (b *Builder) RepairStaticOutputDir() {
	if b.StaticOutputDir == "" {
		return
	}
	if _, err := os.Stat(b.StaticOutputDir); os.IsNotExist(err) {
		return
	}

	clientLibSrc := b.resolveVisualizerFile("client-lib.js")
	hasClientLibSrc := false
	if info, err := os.Stat(clientLibSrc); err == nil && !info.IsDir() {
		hasClientLibSrc = true
	}

	_ = filepath.Walk(b.StaticOutputDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(p, 0755)
			if hasClientLibSrc {
				clientJS := filepath.Join(p, "client.js")
				clientLibDst := filepath.Join(p, "client-lib.js")
				if _, err := os.Stat(clientJS); err == nil {
					if _, err := os.Stat(clientLibDst); os.IsNotExist(err) {
						if err := copyFile(clientLibSrc, clientLibDst); err == nil {
							log.Printf("[builder] Backfilled missing client-lib.js into %s", p)
						}
					}
				}
			}
		} else {
			_ = os.Chmod(p, 0644)
		}
		return nil
	})

	parent := filepath.Dir(b.StaticOutputDir)
	if parent != "" && parent != "/" && parent != "." {
		_ = os.Chmod(parent, 0755)
	}
	_ = os.Chmod(b.StaticOutputDir, 0755)
}

// ingestKnowledge detects a knowledge directory (doc/knowledge, docs, wiki, or .openwikirc)
// and normalizes/copies its Markdown files and image assets into openwiki/knowledge/
// so the openwiki agent and graph builder can discover and reference them.
func (b *Builder) ingestKnowledge(repoID, repoPath string, logBuf *strings.Builder, onProgress ...func(string)) bool {
	srcDir := FindKnowledgeSourceDir(repoPath)
	if srcDir == "" {
		return false
	}

	knowledgeDstDir := filepath.Join(repoPath, "openwiki", "knowledge")
	count, err := IngestKnowledgeDocs(srcDir, knowledgeDstDir)
	if err != nil {
		b.logStep(repoID, fmt.Sprintf("ingest knowledge from %s failed: %v", srcDir, err), logBuf, onProgress...)
		return false
	}

	relSrc, _ := filepath.Rel(repoPath, srcDir)
	if relSrc == "" {
		relSrc = srcDir
	}
	b.logStep(repoID, fmt.Sprintf("Ingested %d knowledge docs from %s into openwiki/knowledge/", count, relSrc), logBuf, onProgress...)
	return count > 0
}

// copyKnowledgeAssets copies non-markdown static assets (images, svg, etc.) from openwiki/knowledge
// into the staging/static directory so that image links in user docs resolve in the browser.
func (b *Builder) copyKnowledgeAssets(wikiDir, staticDir string, log *strings.Builder) {
	knowledgeDir := filepath.Join(wikiDir, "knowledge")
	if info, err := os.Stat(knowledgeDir); err != nil || !info.IsDir() {
		return
	}

	_ = filepath.Walk(knowledgeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if validAssetExts[ext] {
			rel, err := filepath.Rel(wikiDir, path)
			if err != nil {
				return nil
			}
			dst := filepath.Join(staticDir, rel)
			_ = os.MkdirAll(filepath.Dir(dst), 0755)
			if err := copyFile(path, dst); err == nil {
				log.WriteString(fmt.Sprintf("  copied knowledge asset %s\n", rel))
			}
			// If asset is inside knowledge/ subdirectory, also copy to staticDir root
			// so relative Markdown links like `_assets/foo.png` in docs resolve cleanly.
			relWithoutKnowledge := strings.TrimPrefix(rel, "knowledge"+string(filepath.Separator))
			if relWithoutKnowledge != rel {
				dstRoot := filepath.Join(staticDir, relWithoutKnowledge)
				_ = os.MkdirAll(filepath.Dir(dstRoot), 0755)
				_ = copyFile(path, dstRoot)
			}
		}
		return nil
	})
}

// requiredStaticFiles lists the static-site assets that must exist for a repo's
// visualizer page to render. Missing any one means the previous build never
// completed its static-export phase.
var requiredStaticFiles = []string{
	"index.html",
	"client.js",
	"client-lib.js",
	"api/graph",
	"vendor/force-graph.min.js",
	"vendor/marked.min.js",
	"vendor/dompurify.min.js",
	"vendor/mermaid.min.js",
}

// IsStaticOutputComplete reports whether the deployed static site for repoID has
// all required assets. It is used by the pipeline to decide whether a "no git
// updates" run may safely skip rebuilding, or whether a prior build left the
// static output missing/incomplete and must be regenerated.
func (b *Builder) IsStaticOutputComplete(repoID string) bool {
	staticDir := filepath.Join(b.StaticOutputDir, repoID)
	return IsStaticDirComplete(staticDir)
}

// IsStaticDirComplete checks that staticDir exists, contains every required
// asset file with non-zero size, and has valid api/graph JSON.
func IsStaticDirComplete(staticDir string) bool {
	if info, err := os.Stat(staticDir); err != nil || !info.IsDir() {
		InvalidateCompletenessCache(staticDir, "")
		return false
	}
	var graphInfo os.FileInfo
	for _, rel := range requiredStaticFiles {
		target := filepath.Join(staticDir, rel)
		fi, err := os.Stat(target)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			InvalidateCompletenessCache(staticDir, "")
			return false
		}
		if rel == "api/graph" {
			graphInfo = fi
		}
	}
	if graphInfo == nil {
		InvalidateCompletenessCache(staticDir, "")
		return false
	}
	modTime := graphInfo.ModTime().UnixNano()
	size := graphInfo.Size()
	if cached, ok := getCachedStaticComplete(staticDir, modTime, size); ok {
		return cached
	}

	// 验证 api/graph 具有合法非空的 JSON 结构
	valid := false
	graphBytes, err := os.ReadFile(filepath.Join(staticDir, "api", "graph"))
	if err == nil && len(graphBytes) >= 10 {
		var g struct {
			Nodes []interface{} `json:"nodes"`
			Links []interface{} `json:"links"`
		}
		if err := json.Unmarshal(graphBytes, &g); err == nil {
			valid = true
		}
	}
	setCachedStaticComplete(staticDir, modTime, size, valid)
	return valid
}

func isStaticDirComplete(staticDir string) bool {
	return IsStaticDirComplete(staticDir)
}

// IsWikiComplete reports whether the wiki directory for a repo contains at least
// one Markdown document, indicating that a prior openwiki init/update produced
// real wiki content rather than leaving the directory empty/absent.
func (b *Builder) IsWikiComplete(wikiDir string) bool {
	return IsWikiDirComplete(wikiDir)
}

// IsWikiDirComplete returns true when wikiDir exists and contains either
// .last-update.json or substantial markdown documentation (> 100 bytes).
func IsWikiDirComplete(wikiDir string) bool {
	if wikiDir == "" {
		return false
	}
	if cached, ok := getCachedWikiComplete(wikiDir); ok {
		return cached
	}

	info, err := os.Stat(wikiDir)
	if err != nil || !info.IsDir() {
		setCachedWikiComplete(wikiDir, false)
		return false
	}

	// 1. 若存在 OpenWiki 官方完成标记戳，直接判定完整
	lastUpdateFile := filepath.Join(wikiDir, ".last-update.json")
	if fi, err := os.Stat(lastUpdateFile); err == nil && !fi.IsDir() && fi.Size() > 0 {
		setCachedWikiComplete(wikiDir, true)
		return true
	}

	// 2. 检查是否有有效的大于 100 字节的实质性文档
	hasSubstantialDoc := false
	_ = filepath.Walk(wikiDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil || fi.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") && fi.Size() > 100 {
			hasSubstantialDoc = true
			return filepath.SkipDir
		}
		return nil
	})
	setCachedWikiComplete(wikiDir, hasSubstantialDoc)
	return hasSubstantialDoc
}

func isWikiDirComplete(wikiDir string) bool {
	return IsWikiDirComplete(wikiDir)
}

// ExportActiveGraph re-exports api/graph for the currently active deployed version of a repo (used after QA creates/updates wiki docs).
func (b *Builder) ExportActiveGraph(repoID, wikiDir string) error {
	staticDir := filepath.Join(b.StaticOutputDir, repoID)
	defer InvalidateCompletenessCache(staticDir, wikiDir)
	var logBuf strings.Builder
	return b.exportGraph(wikiDir, staticDir, &logBuf)
}
