package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestResolveSiblingTmpDir(t *testing.T) {
	// Case 1: OPENWIKI_HOME ends with .openwiki
	t.Setenv("OPENWIKI_HOME", "/data/disk0/openwiki/.openwiki")
	tmpDir, base := resolveSiblingTmpDir()
	if tmpDir != "/data/disk0/openwiki/.tmp" {
		t.Fatalf("expected /data/disk0/openwiki/.tmp, got %s", tmpDir)
	}
	if base != "/data/disk0/openwiki/.openwiki" {
		t.Fatalf("expected base /data/disk0/openwiki/.openwiki, got %s", base)
	}

	// Case 2: OPENWIKI_HOME is a parent directory
	t.Setenv("OPENWIKI_HOME", "/data/disk0/openwiki")
	tmpDir2, _ := resolveSiblingTmpDir()
	if tmpDir2 != "/data/disk0/openwiki/.tmp" {
		t.Fatalf("expected /data/disk0/openwiki/.tmp, got %s", tmpDir2)
	}
}

func TestBuilderTaskHomeIsolation(t *testing.T) {
	b := &Builder{}
	mockHome := "/data/disk0/openwiki/.tmp/build_999_test_repo"

	cmd := b.createOpenWikiCmdWithHome(999, mockHome, "/tmp/mock-repo", "code", "--update")
	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["OPENWIKI_HOME"] != mockHome {
		t.Fatalf("expected OPENWIKI_HOME=%s, got %s", mockHome, envMap["OPENWIKI_HOME"])
	}
	if envMap["OPENWIKI_CONFIG_DIR"] != mockHome {
		t.Fatalf("expected OPENWIKI_CONFIG_DIR=%s, got %s", mockHome, envMap["OPENWIKI_CONFIG_DIR"])
	}
}

func TestBuilderTaskHomeLifecycle_SuccessDeletes_FailurePreserves(t *testing.T) {
	rootDir := t.TempDir()
	baseOpenwiki := filepath.Join(rootDir, ".openwiki")
	if err := os.MkdirAll(baseOpenwiki, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseOpenwiki, ".env"), []byte("OPENAI_API_KEY=test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENWIKI_HOME", baseOpenwiki)

	tmpSiblingDir := filepath.Join(rootDir, ".tmp")

	// 准备伪造的成功脚本与失败脚本
	okScript := filepath.Join(rootDir, "mock-openwiki-ok.sh")
	if err := os.WriteFile(okScript, []byte("#!/bin/sh\nmkdir -p openwiki\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	failScript := filepath.Join(rootDir, "mock-openwiki-fail.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/sh\necho 'database is locked' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// 场景 1: BuildModeKnowledgeOnly 不应创建任何 .tmp/build_* 子目录
	repoKnowledge := filepath.Join(rootDir, "repo-knowledge")
	_ = os.MkdirAll(repoKnowledge, 0755)
	bKnowledge := NewBuilder(okScript, "", "", filepath.Join(rootDir, "static"))
	resKnowledge := bKnowledge.BuildRepoWithMode(101, "org/repo-k", repoKnowledge, "", BuildModeKnowledgeOnly)
	if !resKnowledge.Success {
		t.Fatalf("expected knowledge-only build to succeed, got error: %s", resKnowledge.Error)
	}
	if entries, err := os.ReadDir(tmpSiblingDir); err == nil && len(entries) > 0 {
		t.Fatalf("expected no .tmp/build_* directories for KnowledgeOnly mode, found %d", len(entries))
	}

	// 场景 2: Full 模式成功构建后，应自动删除 .tmp/build_* 目录
	repoSuccess := filepath.Join(rootDir, "repo-success")
	_ = os.MkdirAll(repoSuccess, 0755)
	bSuccess := NewBuilder(okScript, "", "", filepath.Join(rootDir, "static"))
	resSuccess := bSuccess.BuildRepoWithMode(102, "org/repo-ok", repoSuccess, "", BuildModeFull)
	if !resSuccess.Success || resSuccess.Error != "" {
		t.Fatalf("expected full build to succeed cleanly, got success=%v err=%q", resSuccess.Success, resSuccess.Error)
	}
	if entries, err := os.ReadDir(tmpSiblingDir); err != nil || len(entries) != 0 {
		t.Fatalf("expected .tmp directory to be empty after successful build, got len=%d err=%v", len(entries), err)
	}

	// 场景 3: Step 0 (init) 失败时，必须保留 .tmp/build_* 目录，且 result.Log 中包含该排查路径
	repoInitFail := filepath.Join(rootDir, "repo-init-fail")
	_ = os.MkdirAll(repoInitFail, 0755)
	bInitFail := NewBuilder(failScript, "", "", filepath.Join(rootDir, "static"))
	resInitFail := bInitFail.BuildRepoWithMode(103, "org/repo-init-fail", repoInitFail, "", BuildModeFull)
	if resInitFail.Success {
		t.Fatalf("expected init failure to return Success=false")
	}
	entriesInitFail, err := os.ReadDir(tmpSiblingDir)
	if err != nil || len(entriesInitFail) != 1 {
		t.Fatalf("expected 1 preserved .tmp/build_* dir after init failure, got %d (err=%v)", len(entriesInitFail), err)
	}
	preservedInitPath := filepath.Join(tmpSiblingDir, entriesInitFail[0].Name())
	if !strings.Contains(resInitFail.Log, preservedInitPath) {
		t.Fatalf("expected result.Log to contain preserved path %q, got log:\n%s", preservedInitPath, resInitFail.Log)
	}
	if _, err := os.Stat(filepath.Join(preservedInitPath, ".env")); err != nil {
		t.Fatalf("expected .env to be copied into preserved task home: %v", err)
	}
	_ = os.RemoveAll(preservedInitPath)

	// 场景 4: Step 1 (update) 失败但触发离线发布降级时（result.Error != ""），同样必须保留 .tmp/build_* 目录
	repoUpdateFail := filepath.Join(rootDir, "repo-update-fail")
	_ = os.MkdirAll(filepath.Join(repoUpdateFail, "openwiki"), 0755) // 已有完整 openwiki 目录，跳过 init 直接进 update
	_ = os.WriteFile(filepath.Join(repoUpdateFail, "openwiki", ".last-update.json"), []byte(`{"updatedAt":"2026-11-18T20:00:00Z"}`), 0644)
	bUpdateFail := NewBuilder(failScript, "", "", filepath.Join(rootDir, "static"))
	resUpdateFail := bUpdateFail.BuildRepoWithMode(104, "org/repo-update-fail", repoUpdateFail, "", BuildModeFull)
	if resUpdateFail.Error == "" {
		t.Fatalf("expected update failure to set result.Error")
	}
	entriesUpdateFail, err := os.ReadDir(tmpSiblingDir)
	if err != nil || len(entriesUpdateFail) != 1 {
		t.Fatalf("expected 1 preserved .tmp/build_* dir after update failure, got %d (err=%v)", len(entriesUpdateFail), err)
	}
	preservedUpdatePath := filepath.Join(tmpSiblingDir, entriesUpdateFail[0].Name())
	if !strings.Contains(resUpdateFail.Log, preservedUpdatePath) {
		t.Fatalf("expected result.Log to contain preserved path %q on update failure, got log:\n%s", preservedUpdatePath, resUpdateFail.Log)
	}
}

func TestRunCommandWithStreamingAndTimeout(t *testing.T) {
	b := &Builder{}
	var logBuf strings.Builder

	// Test 1: Successful command with real-time streaming
	cmd := exec.Command("sh", "-c", "echo 'line 1' && echo 'line 2' >&2 && echo 'line 3'")
	var progressSnapshots []string
	err := b.runCommandWithStreaming(context.Background(), cmd, "test-stream", &logBuf, func(s string) {
		progressSnapshots = append(progressSnapshots, s)
	})
	if err != nil {
		t.Fatalf("expected command to succeed, got %v", err)
	}
	out := logBuf.String()
	if !strings.Contains(out, "line 1") || !strings.Contains(out, "line 2") || !strings.Contains(out, "line 3") {
		t.Fatalf("expected all stdout/stderr lines in logBuf, got %q", out)
	}
	if len(progressSnapshots) == 0 {
		t.Fatalf("expected onProgress callbacks during streaming")
	}

	// Test 2: Timeout kills hanging command
	t.Setenv("OPENWIKI_BUILD_TIMEOUT_SEC", "1")
	var timeoutLog strings.Builder
	cmdHang := exec.Command("sh", "-c", "echo 'starting' && sleep 10 && echo 'finished'")
	start := time.Now()
	errHang := b.runCommandWithStreaming(context.Background(), cmdHang, "test-hang", &timeoutLog)
	elapsed := time.Since(start)

	if errHang == nil {
		t.Fatal("expected hanging command to time out and return error")
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("expected timeout in ~1s, took %v", elapsed)
	}
	if !strings.Contains(timeoutLog.String(), "timed out after") {
		t.Fatalf("expected timeout message in log, got %q", timeoutLog.String())
	}
}

func TestIsStaticDirComplete(t *testing.T) {
	tmp := t.TempDir()
	repoStatic := filepath.Join(tmp, "static", "org/repo-x")

	// Empty: not complete
	if isStaticDirComplete(repoStatic) {
		t.Fatal("expected incomplete when dir missing")
	}

	// Create the directory and all required files
	requiredFiles := []string{
		"index.html", "client.js", "client-lib.js", "api/graph",
		"vendor/force-graph.min.js", "vendor/marked.min.js",
		"vendor/dompurify.min.js", "vendor/mermaid.min.js",
	}
	for _, rel := range requiredFiles {
		full := filepath.Join(repoStatic, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		content := []byte("x")
		if rel == "api/graph" {
			content = []byte(`{"nodes":[],"links":[]}`)
		}
		if err := os.WriteFile(full, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if !isStaticDirComplete(repoStatic) {
		t.Fatal("expected complete when all required files present")
	}

	// Remove one required file -> incomplete again
	if err := os.Remove(filepath.Join(repoStatic, "api/graph")); err != nil {
		t.Fatal(err)
	}
	if isStaticDirComplete(repoStatic) {
		t.Fatal("expected incomplete after removing api/graph")
	}
}

func TestIsWikiDirComplete(t *testing.T) {
	tmp := t.TempDir()

	// Missing dir
	if isWikiDirComplete(filepath.Join(tmp, "nope")) {
		t.Fatal("expected false for missing wiki dir")
	}

	// Empty dir (no md)
	emptyDir := filepath.Join(tmp, "empty-wiki")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if isWikiDirComplete(emptyDir) {
		t.Fatal("expected false for wiki dir with no markdown")
	}

	// Dir with a substantial .md file (> 100 bytes)
	wikiDir := filepath.Join(tmp, "wiki")
	nested := filepath.Join(wikiDir, "architecture")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "overview.md"), []byte(strings.Repeat("# Overview\nDetailed substantial documentation.\n", 5)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wikiDir, "notes.txt"), []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isWikiDirComplete(wikiDir) {
		t.Fatal("expected true when wiki dir contains at least one substantial .md file")
	}
}

func TestIsStaticOutputCompleteViaBuilder(t *testing.T) {
	tmp := t.TempDir()
	b := NewBuilder("openwiki", "", "", tmp)

	// No static dir yet
	if b.IsStaticOutputComplete("org/repo-y") {
		t.Fatal("expected false before any static output exists")
	}

	// Populate required files
	repoStatic := filepath.Join(tmp, "org/repo-y")
	for _, rel := range []string{
		"index.html", "client.js", "client-lib.js", "api/graph",
		"vendor/force-graph.min.js", "vendor/marked.min.js",
		"vendor/dompurify.min.js", "vendor/mermaid.min.js",
	} {
		full := filepath.Join(repoStatic, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		content := []byte("x")
		if rel == "api/graph" {
			content = []byte(`{"nodes":[],"links":[]}`)
		}
		if err := os.WriteFile(full, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if !b.IsStaticOutputComplete("org/repo-y") {
		t.Fatal("expected true after populating all required static assets")
	}
}

func TestIsStaticDirComplete_EmptyFilesAndCorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	staticDir := filepath.Join(tmpDir, "static-check")
	_ = os.MkdirAll(filepath.Join(staticDir, "vendor"), 0755)
	_ = os.MkdirAll(filepath.Join(staticDir, "api"), 0755)

	// 创建所有必需文件，但均为 0 字节
	for _, rel := range requiredStaticFiles {
		_ = os.WriteFile(filepath.Join(staticDir, rel), []byte(""), 0644)
	}

	// 0 字节空文件必须判定为不完整
	if isStaticDirComplete(staticDir) {
		t.Fatalf("expected isStaticDirComplete to return false for 0-byte placeholder files")
	}

	// 写入非空常规文件，但 api/graph 写入损坏的 JSON
	for _, rel := range requiredStaticFiles {
		_ = os.WriteFile(filepath.Join(staticDir, rel), []byte("non-empty content"), 0644)
	}
	_ = os.WriteFile(filepath.Join(staticDir, "api", "graph"), []byte("invalid-json{"), 0644)

	// api/graph JSON 无效应判定为不完整
	if isStaticDirComplete(staticDir) {
		t.Fatalf("expected isStaticDirComplete to return false for corrupt api/graph JSON")
	}

	// 写入有效 graph JSON
	validGraph := `{"nodes":[{"id":"test","label":"Test"}],"links":[]}`
	_ = os.WriteFile(filepath.Join(staticDir, "api", "graph"), []byte(validGraph), 0644)

	// 现在全部有效，应判定为完整
	if !isStaticDirComplete(staticDir) {
		t.Fatalf("expected isStaticDirComplete to return true for complete valid assets")
	}
}

func TestIsWikiDirComplete_RequiresSubstantialDocs(t *testing.T) {
	tmpDir := t.TempDir()
	wikiDir := filepath.Join(tmpDir, "wiki-check")
	_ = os.MkdirAll(wikiDir, 0755)

	// 只有 1 个小于 20 字节的空 md 文件（例如残缺创建），应判定为不完整
	_ = os.WriteFile(filepath.Join(wikiDir, "stub.md"), []byte("# Title\n"), 0644)
	if isWikiDirComplete(wikiDir) {
		t.Fatalf("expected isWikiDirComplete to return false for tiny stub markdown")
	}

	// 存在 .last-update.json 完成戳，应判定为完整
	_ = os.WriteFile(filepath.Join(wikiDir, ".last-update.json"), []byte(`{"updatedAt":"2026-11-18T20:00:00Z"}`), 0644)
	InvalidateCompletenessCache("", wikiDir)
	if !isWikiDirComplete(wikiDir) {
		t.Fatalf("expected isWikiDirComplete to return true when .last-update.json exists")
	}

	// 测试无 .last-update.json 但有 > 100 字节的实质性文档
	wikiDir2 := filepath.Join(tmpDir, "wiki-substantial")
	_ = os.MkdirAll(wikiDir2, 0755)
	substantialContent := strings.Repeat("This is a detailed wiki documentation page that exceeds 100 bytes.\n", 3)
	_ = os.WriteFile(filepath.Join(wikiDir2, "architecture.md"), []byte(substantialContent), 0644)
	if !isWikiDirComplete(wikiDir2) {
		t.Fatalf("expected isWikiDirComplete to return true when substantial markdown exists")
	}
}

func TestCompletenessCache_StaticAndWikiInvalidation(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "static-cache-test")
	_ = os.MkdirAll(filepath.Join(staticDir, "vendor"), 0755)
	_ = os.MkdirAll(filepath.Join(staticDir, "api"), 0755)

	for _, rel := range requiredStaticFiles {
		_ = os.WriteFile(filepath.Join(staticDir, rel), []byte("non-empty"), 0644)
	}
	graphPath := filepath.Join(staticDir, "api", "graph")
	_ = os.WriteFile(graphPath, []byte(`{"nodes":[{"id":"n1"}],"links":[]}`), 0644)

	InvalidateCompletenessCache(staticDir, "")
	if !IsStaticDirComplete(staticDir) {
		t.Fatal("expected IsStaticDirComplete=true on valid static dir")
	}

	// Corrupt api/graph with different size -> cache must detect size/mtime change and return false
	_ = os.WriteFile(graphPath, []byte(`{"bad`), 0644)
	if IsStaticDirComplete(staticDir) {
		t.Fatal("expected IsStaticDirComplete=false immediately after api/graph is corrupted")
	}

	// Restore valid graph -> returns true again
	_ = os.WriteFile(graphPath, []byte(`{"nodes":[{"id":"n1"},{"id":"n2"}],"links":[]}`), 0644)
	if !IsStaticDirComplete(staticDir) {
		t.Fatal("expected IsStaticDirComplete=true after restoring valid api/graph")
	}

	// Wiki cache test: create valid wiki dir, verify true, delete .last-update.json, invalidate, verify false
	wikiDir := filepath.Join(tmp, "wiki-cache-test")
	_ = os.MkdirAll(wikiDir, 0755)
	marker := filepath.Join(wikiDir, ".last-update.json")
	_ = os.WriteFile(marker, []byte(`{"updatedAt":"2026-11-19T00:00:00Z"}`), 0644)
	InvalidateCompletenessCache("", wikiDir)
	if !IsWikiDirComplete(wikiDir) {
		t.Fatal("expected IsWikiDirComplete=true when marker exists")
	}
	_ = os.Remove(marker)
	InvalidateCompletenessCache("", wikiDir)
	if IsWikiDirComplete(wikiDir) {
		t.Fatal("expected IsWikiDirComplete=false after marker removed and cache invalidated")
	}
}
