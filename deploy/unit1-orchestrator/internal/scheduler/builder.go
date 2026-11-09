package scheduler

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Builder handles the openwiki update and static export process for a single repo.
type Builder struct {
	OpenwikiCLI     string // path to openwiki CLI
	OpenwikiDistDir string // path to openwiki dist/ directory (for visualize assets)
	VendorAssetsDir string // path to pre-downloaded vendor/ assets
	StaticOutputDir string // base output dir (e.g. /var/www/openwiki-static)
	DefaultLanguage string // default language tag for init/update (e.g. "zh-CN")
	LLMBaseURL      string // optional base URL for internal LLM proxy
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
	}
}

// SetLLMBaseURL sets the base URL for internal LLM gateway forwarding.
func (b *Builder) SetLLMBaseURL(url string) {
	b.LLMBaseURL = url
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
	if os.Getenv("OPENWIKI_HOME") == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			env = append(env, fmt.Sprintf("OPENWIKI_HOME=%s", filepath.Join(home, ".openwiki")))
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

	// LLM Proxy Environment Injection
	if b.LLMBaseURL != "" {
		env = append(env, fmt.Sprintf("OPENAI_BASE_URL=%s", b.LLMBaseURL))
		env = append(env, fmt.Sprintf("OPENAI_COMPATIBLE_BASE_URL=%s", b.LLMBaseURL))
	}
	if buildID > 0 {
		env = append(env, fmt.Sprintf("OPENAI_API_KEY=ow-build-%d", buildID))
	}

	cmd.Env = env

	return cmd
}

// BuildRepo runs the full build pipeline for a single repository.
func (b *Builder) BuildRepo(repoID, repoPath, wikiDir string, onProgress ...func(string)) *BuildResult {
	return b.BuildRepoWithBuildID(0, repoID, repoPath, wikiDir, onProgress...)
}

// BuildRepoWithBuildID runs the full build pipeline for a single repository with build attribution.
func (b *Builder) BuildRepoWithBuildID(buildID int64, repoID, repoPath, wikiDir string, onProgress ...func(string)) *BuildResult {
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
	stagingDir := filepath.Join(b.StaticOutputDir, fmt.Sprintf("%s_tmp_%d", strings.ReplaceAll(repoID, "/", "_"), time.Now().UnixNano()))
	defer func() {
		// Clean up staging directory if it still exists (e.g. on build failure)
		if _, err := os.Stat(stagingDir); err == nil {
			os.RemoveAll(stagingDir)
		}
	}()

	// Step 0: Check if .openwiki exists; if not, run openwiki init first
	openwikiDir := filepath.Join(repoPath, ".openwiki")
	if _, err := os.Stat(openwikiDir); os.IsNotExist(err) {
		b.logStep(repoID, fmt.Sprintf("=== Step 0: openwiki init --language %s ===", lang), &logBuf, onProgress...)
		cmdInit := b.createOpenWikiCmdWithBuildID(buildID, repoPath, "init", "--language", lang)
		var stdoutInit, stderrInit bytes.Buffer
		cmdInit.Stdout = &stdoutInit
		cmdInit.Stderr = &stderrInit
		if err := cmdInit.Run(); err != nil {
			msg := fmt.Sprintf("init stdout: %s\ninit stderr: %s\nopenwiki init failed: %v", stdoutInit.String(), stderrInit.String(), err)
			b.logStep(repoID, msg, &logBuf, onProgress...)
			result.Log = logBuf.String()
			result.Error = fmt.Sprintf("openwiki init failed: %v", err)
			b.logStep(repoID, "ERROR: "+result.Error, &logBuf, onProgress...)
			return result
		}
		b.logStep(repoID, "openwiki init completed successfully", &logBuf, onProgress...)
	}

	// Step 0.5: Knowledge Ingestion (doc/knowledge -> openwiki/knowledge, symlink-based, non-invasive)
	b.logStep(repoID, "=== Step 0.5: Knowledge Ingestion ===", &logBuf, onProgress...)
	if ingested := b.ingestKnowledge(repoID, repoPath, &logBuf, onProgress...); ingested {
		b.logStep(repoID, "Knowledge documents linked to openwiki/knowledge/", &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "No doc/knowledge directory found, skipping knowledge ingestion", &logBuf, onProgress...)
	}

	// Step 1: Run openwiki code --update --print --language <lang>
	b.logStep(repoID, fmt.Sprintf("=== Step 1: openwiki code --update --print --language %s ===", lang), &logBuf, onProgress...)
	cmd := b.createOpenWikiCmdWithBuildID(buildID, repoPath, "code", "--update", "--print", "--language", lang)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := fmt.Sprintf("stdout: %s\nstderr: %s", stdout.String(), stderr.String())
		b.logStep(repoID, msg, &logBuf, onProgress...)
		result.Log = logBuf.String()
		result.Error = fmt.Sprintf("openwiki update failed: %v", err)
		b.logStep(repoID, "ERROR: "+result.Error, &logBuf, onProgress...)
		return result
	}
	b.logStep(repoID, fmt.Sprintf("stdout: %s\nopenwiki update completed successfully", stdout.String()), &logBuf, onProgress...)

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

	// Step 4: Copy vendor libraries (intranet offline) into staging directory
	b.logStep(repoID, "=== Step 4: Copy vendor libraries ===", &logBuf, onProgress...)
	if err := b.copyVendorAssets(stagingDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("copy vendor assets failed: %v", err), &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "vendor libraries copied successfully", &logBuf, onProgress...)
	}

	// Step 5: Atomic Directory Swap (Zero-Downtime Release)
	b.logStep(repoID, "=== Step 5: Atomic directory swap ===", &logBuf, onProgress...)
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
		b.logStep(repoID, "atomic swap completed successfully", &logBuf, onProgress...)
	}

	result.Success = true
	result.Log = logBuf.String()
	b.logStep(repoID, "=== BUILD SUCCESSFUL ===", &logBuf, onProgress...)
	return result
}

// DeployVisualizer populates staticDir with graph.json, index.html, client.js and vendor assets for wikiDir.
func (b *Builder) DeployVisualizer(wikiDir, staticDir string) error {
	var logBuf strings.Builder
	_ = b.exportGraph(wikiDir, staticDir, &logBuf)
	_ = b.copyVisualizerAssets(staticDir, &logBuf)
	_ = b.copyVendorAssets(staticDir, &logBuf)
	return nil
}

// exportGraph calls node to run buildGraph() and writes the JSON output.
func (b *Builder) exportGraph(wikiDir, staticDir string, log *strings.Builder) error {
	script := fmt.Sprintf(`
		import("./dist/visualize/graph.js")
			.then(m => m.buildGraph("%s"))
			.then(g => process.stdout.write(JSON.stringify(g)))
	`, wikiDir)

	cmd := exec.Command("node", "--input-type=module", "-e", script)
	if b.OpenwikiDistDir != "" {
		cmd.Dir = filepath.Dir(b.OpenwikiDistDir)
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
	return os.WriteFile(filepath.Join(apiDir, "graph"), graphJSON, 0644)
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

// copyVisualizerAssets copies the compiled client.js, client-lib.js from openwiki dist.
func (b *Builder) copyVisualizerAssets(staticDir string, log *strings.Builder) error {
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		return err
	}

	distViz := filepath.Join(b.OpenwikiDistDir, "visualize")
	assetsDir := filepath.Dir(b.VendorAssetsDir)

	clientSrc := filepath.Join(assetsDir, "client.js")
	if _, err := os.Stat(clientSrc); os.IsNotExist(err) {
		clientSrc = filepath.Join(distViz, "client.js")
	}

	clientLibSrc := filepath.Join(assetsDir, "client-lib.js")
	if _, err := os.Stat(clientLibSrc); os.IsNotExist(err) {
		clientLibSrc = filepath.Join(distViz, "client-lib.js")
	}

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
		os.WriteFile(clientDst, []byte(patched), 0644)
	}

	// Always copy index.html template to static output directory
	indexPath := filepath.Join(staticDir, "index.html")
	templatePath := filepath.Join(filepath.Dir(b.VendorAssetsDir), "index.html")
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

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// ingestKnowledge detects a doc/knowledge directory in the repository and symlinks
// its Markdown files into openwiki/knowledge/ so the openwiki agent can discover and
// reference them during wiki generation, static export, and graph building.
// It is non-invasive: source files remain owned by the repo. Repos without doc/knowledge
// are silently skipped (returns false).
func (b *Builder) ingestKnowledge(repoID, repoPath string, logBuf *strings.Builder, onProgress ...func(string)) bool {
	knowledgeSrcDir := filepath.Join(repoPath, "doc", "knowledge")
	info, err := os.Stat(knowledgeSrcDir)
	if err != nil || !info.IsDir() {
		return false
	}

	knowledgeDstDir := filepath.Join(repoPath, "openwiki", "knowledge")
	if err := os.MkdirAll(knowledgeDstDir, 0755); err != nil {
		b.logStep(repoID, fmt.Sprintf("create knowledge dir failed: %v", err), logBuf, onProgress...)
		return false
	}

	count := 0
	_ = filepath.Walk(knowledgeSrcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		relPath, _ := filepath.Rel(knowledgeSrcDir, path)
		dstFile := filepath.Join(knowledgeDstDir, relPath)
		_ = os.MkdirAll(filepath.Dir(dstFile), 0755)
		_ = os.Remove(dstFile) // idempotent: remove then recreate
		if err := os.Symlink(path, dstFile); err != nil {
			b.logStep(repoID, fmt.Sprintf("link knowledge %s failed: %v", relPath, err), logBuf, onProgress...)
			return nil
		}
		count++
		return nil
	})

	b.logStep(repoID, fmt.Sprintf("Ingested %d knowledge MD files", count), logBuf, onProgress...)
	return count > 0
}

// ExportActiveGraph re-exports api/graph for the currently active deployed version of a repo (used after QA creates/updates wiki docs).
func (b *Builder) ExportActiveGraph(repoID, wikiDir string) error {
	staticDir := filepath.Join(b.StaticOutputDir, repoID)
	var logBuf strings.Builder
	return b.exportGraph(wikiDir, staticDir, &logBuf)
}
