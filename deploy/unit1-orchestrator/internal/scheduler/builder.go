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
)

// Builder handles the openwiki update and static export process for a single repo.
type Builder struct {
	OpenwikiCLI     string // path to openwiki CLI
	OpenwikiDistDir string // path to openwiki dist/ directory (for visualize assets)
	VendorAssetsDir string // path to pre-downloaded vendor/ assets
	StaticOutputDir string // base output dir (e.g. /var/www/openwiki-static)
	DefaultLanguage string // default language tag for init/update (e.g. "zh-CN")
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
	hasPath := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = e + ":/usr/local/bin:/opt/homebrew/bin"
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin")
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

	cmd.Env = env

	return cmd
}

// BuildRepo runs the full build pipeline for a single repository:
//  1. openwiki code --update --print --language zh-CN (incremental wiki generation)
//  2. Export graph.json via buildGraph()
//  3. Copy visualizer frontend assets (PAGE HTML + client.js + client-lib.js)
//  4. Copy pre-downloaded vendor/ libraries for intranet offline use
func (b *Builder) BuildRepo(repoID, repoPath, wikiDir string, onProgress ...func(string)) *BuildResult {
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

	// Resolve static output directory for this repo
	staticDir := filepath.Join(b.StaticOutputDir, repoID)

	// Step 0: Check if .openwiki exists; if not, run openwiki init first
	openwikiDir := filepath.Join(repoPath, ".openwiki")
	if _, err := os.Stat(openwikiDir); os.IsNotExist(err) {
		b.logStep(repoID, fmt.Sprintf("=== Step 0: openwiki init --language %s ===", lang), &logBuf, onProgress...)
		cmdInit := b.createOpenWikiCmd(repoPath, "init", "--language", lang)
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

	// Step 1: Run openwiki code --update --print --language <lang>
	b.logStep(repoID, fmt.Sprintf("=== Step 1: openwiki code --update --print --language %s ===", lang), &logBuf, onProgress...)
	cmd := b.createOpenWikiCmd(repoPath, "code", "--update", "--print", "--language", lang)
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

	// Step 2: Export graph.json
	b.logStep(repoID, "=== Step 2: Export graph.json ===", &logBuf, onProgress...)
	if err := b.exportGraph(wikiDir, staticDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("graph export failed: %v", err), &logBuf, onProgress...)
		// Non-fatal: continue with other steps
	} else {
		b.logStep(repoID, "graph.json exported successfully", &logBuf, onProgress...)
	}

	// Step 3: Copy visualizer frontend assets
	b.logStep(repoID, "=== Step 3: Copy visualizer assets ===", &logBuf, onProgress...)
	if err := b.copyVisualizerAssets(staticDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("copy visualizer assets failed: %v", err), &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "visualizer assets copied successfully", &logBuf, onProgress...)
	}

	// Step 4: Copy vendor libraries (intranet offline)
	b.logStep(repoID, "=== Step 4: Copy vendor libraries ===", &logBuf, onProgress...)
	if err := b.copyVendorAssets(staticDir, &logBuf); err != nil {
		b.logStep(repoID, fmt.Sprintf("copy vendor assets failed: %v", err), &logBuf, onProgress...)
	} else {
		b.logStep(repoID, "vendor libraries copied successfully", &logBuf, onProgress...)
	}

	result.Success = true
	result.Log = logBuf.String()
	b.logStep(repoID, "=== BUILD SUCCESSFUL ===", &logBuf, onProgress...)
	return result
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

// copyVisualizerAssets copies the compiled client.js, client-lib.js from openwiki dist.
func (b *Builder) copyVisualizerAssets(staticDir string, log *strings.Builder) error {
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		return err
	}

	distViz := filepath.Join(b.OpenwikiDistDir, "visualize")

	assets := map[string]string{
		"client.js":     filepath.Join(distViz, "client.js"),
		"client-lib.js": filepath.Join(distViz, "client-lib.js"),
	}

	for name, src := range assets {
		dst := filepath.Join(staticDir, name)
		if err := copyFile(src, dst); err != nil {
			log.WriteString(fmt.Sprintf("  skip %s: %v\n", name, err))
		} else {
			log.WriteString(fmt.Sprintf("  copied %s\n", name))
		}
	}

	// Post-process static client.js to resolve relative graph endpoint for exported static SPA
	clientDst := filepath.Join(staticDir, "client.js")
	if data, err := os.ReadFile(clientDst); err == nil {
		content := string(data)
		content = strings.Replace(content, `fetch("/api/graph")`, `fetch(new URL("api/graph", window.location.href).href)`, 1)
		content = strings.Replace(content, `new EventSource("/events")`, `new EventSource(new URL("events", window.location.href).href)`, 1)
		os.WriteFile(clientDst, []byte(content), 0644)
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
