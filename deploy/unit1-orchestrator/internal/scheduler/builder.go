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
}

// NewBuilder creates a new Builder instance.
func NewBuilder(cli, vendorDir, distDir, outputDir string) *Builder {
	if cli == "" {
		cli = "openwiki"
	}
	if outputDir == "" {
		outputDir = "/var/www/openwiki-static"
	}
	return &Builder{
		OpenwikiCLI:     cli,
		VendorAssetsDir: vendorDir,
		OpenwikiDistDir: distDir,
		StaticOutputDir: outputDir,
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
//  1. openwiki code --update --print (incremental wiki generation)
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

// BuildRepo runs the full build pipeline for a single repository:
//  1. openwiki code --update --print (incremental wiki generation)
//  2. Export graph.json via buildGraph()
//  3. Copy visualizer frontend assets (PAGE HTML + client.js + client-lib.js)
//  4. Copy pre-downloaded vendor/ libraries for intranet offline use
func (b *Builder) BuildRepo(repoID, repoPath, wikiDir string, onProgress ...func(string)) *BuildResult {
	var logBuf strings.Builder
	result := &BuildResult{}

	// Resolve wiki directory
	if wikiDir == "" {
		wikiDir = filepath.Join(repoPath, "openwiki")
	}

	// Resolve static output directory for this repo
	staticDir := filepath.Join(b.StaticOutputDir, repoID)

	// Step 0: Check if .openwiki exists; if not, run openwiki init first
	openwikiDir := filepath.Join(repoPath, ".openwiki")
	if _, err := os.Stat(openwikiDir); os.IsNotExist(err) {
		b.logStep(repoID, "=== Step 0: openwiki init ===", &logBuf, onProgress...)
		cmdInit := exec.Command(b.OpenwikiCLI, "init")
		cmdInit.Dir = repoPath
		var stdoutInit, stderrInit bytes.Buffer
		cmdInit.Stdout = &stdoutInit
		cmdInit.Stderr = &stderrInit
		if err := cmdInit.Run(); err != nil {
			msg := fmt.Sprintf("init stdout: %s\ninit stderr: %s\nopenwiki init warning (continuing update): %v", stdoutInit.String(), stderrInit.String(), err)
			b.logStep(repoID, msg, &logBuf, onProgress...)
		} else {
			b.logStep(repoID, "openwiki init completed successfully", &logBuf, onProgress...)
		}
	}

	// Step 1: Run openwiki code --update --print
	b.logStep(repoID, "=== Step 1: openwiki code --update --print ===", &logBuf, onProgress...)
	cmd := exec.Command(b.OpenwikiCLI, "code", "--update", "--print")
	cmd.Dir = repoPath
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
