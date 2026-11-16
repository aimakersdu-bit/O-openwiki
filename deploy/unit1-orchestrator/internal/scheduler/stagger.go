package scheduler

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// DefaultNightWindowStart is the default start hour (01:00) for night builds.
const DefaultNightWindowStart = 1

// DefaultNightWindowEnd is the default end hour (05:00) for night builds.
const DefaultNightWindowEnd = 5

// ComputeStaggeredSchedule generates a deterministic, jittered cron expression for a repository
// within the specified night window [startHour, endHour).
//
// The output is a standard 5-part cron string: "minute hour * * *"
// - minute is in [0, 59]
// - hour is in [startHour, endHour)
//
// If startHour >= endHour or hours are out of the 24-hour range, safe defaults (1..5) are used.
func ComputeStaggeredSchedule(repoID string, startHour, endHour int) string {
	if startHour < 0 || startHour > 23 || endHour < 1 || endHour > 24 || startHour >= endHour {
		startHour = DefaultNightWindowStart
		endHour = DefaultNightWindowEnd
	}

	hourSpan := uint64(endHour - startHour)

	// Compute deterministic SHA-256 hash of the repo ID
	hash := sha256.Sum256([]byte(repoID))
	// Use 8 bytes of hash as a 64-bit integer
	val := binary.BigEndian.Uint64(hash[:8])

	minute := int(val % 60)
	hourOffset := int((val / 60) % hourSpan)
	hour := startHour + hourOffset

	return fmt.Sprintf("%d %d * * *", minute, hour)
}
