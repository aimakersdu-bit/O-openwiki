package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestComputeStaggeredSchedule_RangeAndFormat(t *testing.T) {
	repos := []string{
		"aimakersdu-bit/health",
		"zk1MTest",
		"openwiki/core",
		"antigravity/frontend",
		"infra/gateway",
		"service/user-auth",
		"data/analytics",
		"repo/sample-1",
		"repo/sample-2",
		"repo/sample-3",
	}

	startHour := 1
	endHour := 5

	for _, repoID := range repos {
		cronExpr := ComputeStaggeredSchedule(repoID, startHour, endHour)
		fields := strings.Fields(cronExpr)
		if len(fields) != 5 {
			t.Fatalf("expected 5 cron fields for %s, got: %s", repoID, cronExpr)
		}

		minute, err := strconv.Atoi(fields[0])
		if err != nil || minute < 0 || minute > 59 {
			t.Errorf("invalid minute %q for repo %s in %s", fields[0], repoID, cronExpr)
		}

		hour, err := strconv.Atoi(fields[1])
		if err != nil || hour < startHour || hour >= endHour {
			t.Errorf("hour %q for repo %s out of bounds [%d, %d)", fields[1], repoID, startHour, endHour)
		}

		if fields[2] != "*" || fields[3] != "*" || fields[4] != "*" {
			t.Errorf("expected daily wildcard fields (* * *), got: %s", cronExpr)
		}
	}
}

func TestComputeStaggeredSchedule_Deterministic(t *testing.T) {
	repoID := "zk1MTest"
	first := ComputeStaggeredSchedule(repoID, 1, 5)
	for i := 0; i < 5; i++ {
		repeat := ComputeStaggeredSchedule(repoID, 1, 5)
		if repeat != first {
			t.Errorf("expected deterministic schedule, iteration %d gave %s vs %s", i, repeat, first)
		}
	}
}

func TestComputeStaggeredSchedule_Dispersion(t *testing.T) {
	// Verify that different repoIDs get different schedules and don't all collide on the same minute/hour
	scheds := make(map[string]bool)
	for i := 0; i < 20; i++ {
		repo := fmt.Sprintf("sample-org/repo-%d", i)
		s := ComputeStaggeredSchedule(repo, 1, 5)
		scheds[s] = true
	}

	// In a 4-hour window (4*60 = 240 slots), 20 repos should produce at least 10 distinct slots
	if len(scheds) < 10 {
		t.Errorf("insufficient schedule dispersion: 20 repos produced only %d unique slots", len(scheds))
	}
}

func TestComputeStaggeredSchedule_FallbackInvalidWindow(t *testing.T) {
	// If start >= end, should safely fallback to default 1-5 AM
	s := ComputeStaggeredSchedule("any-repo", 5, 2)
	fields := strings.Fields(s)
	hour, _ := strconv.Atoi(fields[1])
	if hour < 1 || hour >= 5 {
		t.Errorf("fallback failed: expected hour in [1, 5), got %d", hour)
	}
}
