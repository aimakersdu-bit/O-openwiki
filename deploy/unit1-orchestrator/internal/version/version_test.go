package version

import (
	"strings"
	"testing"
)

func TestGetReturnsDefaults(t *testing.T) {
	origVersion, origCommit, origBuild := Version, GitCommit, BuildTime
	defer func() { Version, GitCommit, BuildTime = origVersion, origCommit, origBuild }()

	Version, GitCommit, BuildTime = "unknown", "unknown", "unknown"
	info := Get()
	if info.Version != "unknown" {
		t.Fatalf("expected unknown, got %s", info.Version)
	}
	if info.GitCommit != "unknown" {
		t.Fatalf("expected unknown, got %s", info.GitCommit)
	}
}

func TestGetReflectsInjectedValues(t *testing.T) {
	origVersion, origCommit, origBuild := Version, GitCommit, BuildTime
	defer func() { Version, GitCommit, BuildTime = origVersion, origCommit, origBuild }()

	Version, GitCommit, BuildTime = "0.5.4", "abc1234", "2026-11-18T22:00:00Z"
	info := Get()
	if info.Version != "0.5.4" || info.GitCommit != "abc1234" || info.BuildTime != "2026-11-18T22:00:00Z" {
		t.Fatalf("unexpected: %+v", info)
	}
	if !strings.Contains(info.String(), "0.5.4") || !strings.Contains(info.String(), "abc1234") {
		t.Fatalf("String() should contain version and commit: %s", info.String())
	}
}
