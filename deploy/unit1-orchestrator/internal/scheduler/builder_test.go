package scheduler

import (
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
