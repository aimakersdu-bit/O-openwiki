package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPatchClientJS_GraphFetchRewritten(t *testing.T) {
	input := `const res = await fetch("/api/graph");`
	out := patchClientJS(input)
	if strings.Contains(out, `fetch("/api/graph")`) {
		t.Errorf("fetch(\"/api/graph\") should be rewritten to relative URL")
	}
	if !strings.Contains(out, "new URL(\"api/graph\"") {
		t.Errorf("expected relative URL rewrite for api/graph, got: %s", out)
	}
}

func TestPatchClientJS_EventSourceRewritten(t *testing.T) {
	input := `const es = new EventSource("/events");`
	out := patchClientJS(input)
	if strings.Contains(out, `EventSource("/events")`) {
		t.Errorf("EventSource(\"/events\") should be rewritten to relative URL")
	}
	if !strings.Contains(out, "new URL(\"events\"") {
		t.Errorf("expected relative URL rewrite for events, got: %s", out)
	}
}

func TestPatchClientJS_RewriteLinksAbsolute(t *testing.T) {
	// Simulates the original client.js rewriteLinks code from openwiki dist
	input := `        const clean = href.split("#")[0];
        const target = normalize(dir, clean).replace(/\.md$/, "");
        if (byId(target)) {`

	out := patchClientJS(input)

	// The old single-line target assignment should be gone
	if strings.Contains(out, "const target = normalize(dir, clean).replace") && !strings.Contains(out, "isAbs") {
		t.Errorf("rewriteLinks target line should be patched, but old line still present:\n%s", out)
	}

	// The patched code should contain isAbs / cleanPath logic
	if !strings.Contains(out, "isAbs") {
		t.Errorf("patched client.js should contain isAbs variable")
	}
	if !strings.Contains(out, "cleanPath") {
		t.Errorf("patched client.js should contain cleanPath variable")
	}
}

func TestPatchClientJS_NoopOnAlreadyPatched(t *testing.T) {
	// If the content was already patched, running patchClientJS again should be safe
	original := `        const clean = href.split("#")[0];
        const target = normalize(dir, clean).replace(/\.md$/, "");
        if (byId(target)) {`

	patched := patchClientJS(original)
	doublePatched := patchClientJS(patched)

	if doublePatched != patched {
		t.Errorf("double patching should be idempotent")
	}
}

func TestPatchClientJS_PreservesUnrelatedContent(t *testing.T) {
	input := `console.log("hello world");
const x = 42;`
	out := patchClientJS(input)
	if !strings.Contains(out, `console.log("hello world")`) {
		t.Errorf("unrelated content should be preserved")
	}
	if !strings.Contains(out, "const x = 42;") {
		t.Errorf("unrelated content should be preserved")
	}
}

func TestIngestKnowledge_CopiesAndNormalizesMarkdownFiles(t *testing.T) {
	tmp := t.TempDir()
	// doc/knowledge with nested MD files
	src := tmp + "/doc/knowledge"
	if err := os.MkdirAll(src+"/sub", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/a.md", []byte("# A 标题\n\n正文"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/sub/b.md", []byte("# B 标题"), 0644); err != nil {
		t.Fatal(err)
	}
	// a non-md file that must be ignored
	if err := os.WriteFile(src+"/notes.txt", []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}

	b := &Builder{}
	var logBuf strings.Builder
	if !b.ingestKnowledge("repo-x", tmp, &logBuf) {
		t.Fatalf("ingestKnowledge should return true when doc/knowledge exists with MD files")
	}

	dst := tmp + "/openwiki/knowledge"
	for _, rel := range []string{"a.md", "sub/b.md"} {
		file := dst + "/" + rel
		fi, err := os.Stat(file)
		if err != nil {
			t.Fatalf("expected file %s: %v", file, err)
		}
		if fi.IsDir() {
			t.Fatalf("%s should be a regular file, not a dir", file)
		}
	}

	// Verify normalization added frontmatter
	contentA, err := os.ReadFile(dst + "/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contentA), "type: Knowledge") {
		t.Errorf("a.md should have frontmatter type: Knowledge, got:\n%s", string(contentA))
	}

	// notes.txt must NOT be copied
	if _, err := os.Stat(dst + "/notes.txt"); !os.IsNotExist(err) {
		t.Fatalf("non-md file should not be copied")
	}
}

func TestIngestKnowledge_MultiDirectorySupport(t *testing.T) {
	tmp := t.TempDir()
	// Repo uses docs/ instead of doc/knowledge/
	src := tmp + "/docs"
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/guide.md", []byte("# 快速上手指南"), 0644); err != nil {
		t.Fatal(err)
	}

	b := &Builder{}
	var logBuf strings.Builder
	if !b.ingestKnowledge("repo-docs", tmp, &logBuf) {
		t.Fatalf("ingestKnowledge should succeed for docs/ directory")
	}

	dstFile := tmp + "/openwiki/knowledge/guide.md"
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("expected %s to exist: %v", dstFile, err)
	}
}

func TestIngestKnowledge_NoDirectoryReturnsFalse(t *testing.T) {
	tmp := t.TempDir()
	b := &Builder{}
	var logBuf strings.Builder
	if b.ingestKnowledge("repo-y", tmp, &logBuf) {
		t.Fatalf("ingestKnowledge should return false when doc/knowledge absent")
	}
}

func TestIngestKnowledge_IdempotentSync(t *testing.T) {
	tmp := t.TempDir()
	src := tmp + "/doc/knowledge"
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/a.md", []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	b := &Builder{}
	var logBuf strings.Builder
	if !b.ingestKnowledge("repo-z", tmp, &logBuf) {
		t.Fatal("first ingest should succeed")
	}
	// second run must not error and file must still exist
	if !b.ingestKnowledge("repo-z", tmp, &logBuf) {
		t.Fatal("second ingest should succeed (idempotent)")
	}
	data, err := os.ReadFile(tmp + "/openwiki/knowledge/a.md")
	if err != nil {
		t.Fatalf("file should exist: %v", err)
	}
	if !strings.Contains(string(data), "type: Knowledge") {
		t.Fatalf("content unexpected: %s", string(data))
	}
}

func TestVisualizerAssetsAndPermissions_WithRestrictiveUmask(t *testing.T) {
	oldMask := syscall.Umask(0077)
	defer syscall.Umask(oldMask)

	tmp := t.TempDir()
	staticOut := filepath.Join(tmp, "static")
	repoStatic := filepath.Join(staticOut, "bxbank/DEO/demo-repo")

	// Point VendorAssetsDir to the real deploy/unit1-orchestrator/assets/vendor directory,
	// with OpenwikiDistDir intentionally empty ("") to simulate intranet standalone deployment.
	assetsVendorDir, err := filepath.Abs("../../assets/vendor")
	if err != nil {
		t.Fatal(err)
	}

	b := NewBuilder("openwiki", assetsVendorDir, "", staticOut)
	var logBuf strings.Builder
	if err := b.copyVisualizerAssets(repoStatic, &logBuf); err != nil {
		t.Fatalf("copyVisualizerAssets failed: %v", err)
	}
	if err := b.copyVendorAssets(repoStatic, &logBuf); err != nil {
		t.Fatalf("copyVendorAssets failed: %v", err)
	}
	b.EnsureStaticPermissions(repoStatic)

	if strings.Contains(logBuf.String(), "skip client-lib.js") {
		t.Fatalf("expected client-lib.js to be copied, got log:\n%s", logBuf.String())
	}

	// Verify all required static files exist and have 0644 permissions (world-readable for Nginx)
	for _, relFile := range []string{"index.html", "client.js", "client-lib.js", "vendor/marked.min.js"} {
		fp := filepath.Join(repoStatic, relFile)
		info, err := os.Stat(fp)
		if err != nil {
			t.Fatalf("expected file %s to exist: %v", fp, err)
		}
		if info.Mode().Perm() != 0644 {
			t.Errorf("expected file %s perm 0644, got %04o", relFile, info.Mode().Perm())
		}
	}

	// Verify directories have 0755 permissions (other has rx for Nginx traversal)
	for _, dir := range []string{
		staticOut,
		filepath.Join(staticOut, "bxbank"),
		filepath.Join(staticOut, "bxbank/DEO"),
		repoStatic,
		filepath.Join(repoStatic, "vendor"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected dir %s to exist: %v", dir, err)
		}
		if info.Mode().Perm() != 0755 {
			t.Errorf("expected dir %s perm 0755, got %04o", dir, info.Mode().Perm())
		}
	}
}

func TestRepairStaticOutputDir_BackfillsClientLibAndFixesPerms(t *testing.T) {
	tmp := t.TempDir()
	staticOut := filepath.Join(tmp, "static")
	repoDir := filepath.Join(staticOut, "legacy-repo")
	if err := os.MkdirAll(repoDir, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(staticOut, 0700)
	_ = os.Chmod(repoDir, 0700)

	clientJS := filepath.Join(repoDir, "client.js")
	if err := os.WriteFile(clientJS, []byte("// client"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(clientJS, 0600)

	assetsVendorDir, err := filepath.Abs("../../assets/vendor")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder("openwiki", assetsVendorDir, "", staticOut)
	b.RepairStaticOutputDir()

	// Verify client-lib.js was backfilled and has 0644 permissions
	clientLib := filepath.Join(repoDir, "client-lib.js")
	info, err := os.Stat(clientLib)
	if err != nil {
		t.Fatalf("expected client-lib.js to be backfilled: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("expected client-lib.js perm 0644, got %04o", info.Mode().Perm())
	}

	// Verify directory and file permissions were repaired to 0755 / 0644
	dirInfo, _ := os.Stat(repoDir)
	if dirInfo.Mode().Perm() != 0755 {
		t.Errorf("expected repaired dir perm 0755, got %04o", dirInfo.Mode().Perm())
	}
	fileInfo, _ := os.Stat(clientJS)
	if fileInfo.Mode().Perm() != 0644 {
		t.Errorf("expected repaired file perm 0644, got %04o", fileInfo.Mode().Perm())
	}
}

func TestBuilderEnvInjectionForOpenAICompatible(t *testing.T) {
	b := &Builder{
		LLMBaseURL:    "http://127.0.0.1:3000/internal/llm/v1",
		LLMTimeoutSec: 300,
	}

	cmd := b.createOpenWikiCmdWithBuildID(123, "/tmp/mock-repo", "code", "--update")

	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// 1. 验证 Provider 设为 openai-compatible
	if envMap["OPENWIKI_PROVIDER"] != "openai-compatible" {
		t.Fatalf("expected OPENWIKI_PROVIDER=openai-compatible, got %q", envMap["OPENWIKI_PROVIDER"])
	}
	// 2. 验证 OPENAI_API_KEY 与 OPENAI_COMPATIBLE_API_KEY 均被注入为 ow-build-123
	if envMap["OPENAI_API_KEY"] != "ow-build-123" {
		t.Fatalf("expected OPENAI_API_KEY=ow-build-123, got %q", envMap["OPENAI_API_KEY"])
	}
	if envMap["OPENAI_COMPATIBLE_API_KEY"] != "ow-build-123" {
		t.Fatalf("expected OPENAI_COMPATIBLE_API_KEY=ow-build-123, got %q", envMap["OPENAI_COMPATIBLE_API_KEY"])
	}
	// 3. 验证 Base URL
	if envMap["OPENAI_BASE_URL"] != "http://127.0.0.1:3000/internal/llm/v1/build/123" {
		t.Fatalf("unexpected OPENAI_BASE_URL: %s", envMap["OPENAI_BASE_URL"])
	}
	if envMap["OPENAI_COMPATIBLE_BASE_URL"] != "http://127.0.0.1:3000/internal/llm/v1/build/123" {
		t.Fatalf("unexpected OPENAI_COMPATIBLE_BASE_URL: %s", envMap["OPENAI_COMPATIBLE_BASE_URL"])
	}
	// 4. 验证 OPENWIKI_OPENAI_COMPATIBLE_STREAMING 自动开启
	if envMap["OPENWIKI_OPENAI_COMPATIBLE_STREAMING"] != "true" {
		t.Fatalf("expected OPENWIKI_OPENAI_COMPATIBLE_STREAMING=true, got %q", envMap["OPENWIKI_OPENAI_COMPATIBLE_STREAMING"])
	}
}

func TestBuilder_BuildModesDefined(t *testing.T) {
	if BuildModeFull != "full" {
		t.Errorf("expected BuildModeFull == full, got %s", BuildModeFull)
	}
	if BuildModeKnowledgeOnly != "knowledge-only" {
		t.Errorf("expected BuildModeKnowledgeOnly == knowledge-only, got %s", BuildModeKnowledgeOnly)
	}
}


