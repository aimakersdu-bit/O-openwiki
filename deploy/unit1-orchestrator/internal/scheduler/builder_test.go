package scheduler

import (
	"os"
	"strings"
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

func TestIngestKnowledge_LinksMarkdownFiles(t *testing.T) {
	tmp := t.TempDir()
	// doc/knowledge with nested MD files
	src := tmp + "/doc/knowledge"
	if err := os.MkdirAll(src+"/sub", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/a.md", []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/sub/b.md", []byte("# B"), 0644); err != nil {
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
		link := dst + "/" + rel
		fi, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("expected symlink %s: %v", link, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s should be a symlink", link)
		}
	}
	// notes.txt must NOT be linked
	if _, err := os.Lstat(dst + "/notes.txt"); !os.IsNotExist(err) {
		t.Fatalf("non-md file should not be linked")
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

func TestIngestKnowledge_IdempotentRelink(t *testing.T) {
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
	// second run must not error and link must still resolve
	if !b.ingestKnowledge("repo-z", tmp, &logBuf) {
		t.Fatal("second ingest should succeed (idempotent)")
	}
	target, err := os.Readlink(tmp + "/openwiki/knowledge/a.md")
	if err != nil {
		t.Fatalf("symlink should still resolve: %v", err)
	}
	if !strings.HasSuffix(target, "/doc/knowledge/a.md") {
		t.Fatalf("symlink target unexpected: %s", target)
	}
}
