package scheduler_test

import (
	"testing"

	"github.com/openwiki/orchestrator/internal/scheduler"
)

func TestParseTokenUsage(t *testing.T) {
	sampleLog := `
=== Step 1: openwiki code --update --print --language zh-CN ===
Analyzing repository structure...
Chunk 1 processed.
[OpenWiki] OPENWIKI_USAGE {"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200}
Chunk 2 processed.
[OpenWiki] OPENWIKI_USAGE {"prompt_tokens":2500,"completion_tokens":550,"total_tokens":3050}
Wiki generated successfully.
`
	prompt, completion, total := scheduler.ParseTokenUsage(sampleLog)
	if prompt != 2500 || completion != 550 || total != 3050 {
		t.Fatalf("Expected prompt=2500, completion=550, total=3050, got %d, %d, %d", prompt, completion, total)
	}

	// Empty / no usage marker
	p0, c0, t0 := scheduler.ParseTokenUsage("No marker present")
	if p0 != 0 || c0 != 0 || t0 != 0 {
		t.Fatalf("Expected zeroes for log without usage marker, got %d, %d, %d", p0, c0, t0)
	}
}

func TestFormatETA(t *testing.T) {
	tests := []struct {
		seconds  int
		expected string
	}{
		{0, "0秒"},
		{45, "45秒"},
		{60, "1分钟0秒"},
		{95, "1分钟35秒"},
		{3600, "1小时0分钟0秒"},
		{3665, "1小时1分钟5秒"},
		{7320, "2小时2分钟0秒"},
	}

	for _, tc := range tests {
		got := scheduler.FormatETA(tc.seconds)
		if got != tc.expected {
			t.Errorf("FormatETA(%d) = %q, expected %q", tc.seconds, got, tc.expected)
		}
	}
}
