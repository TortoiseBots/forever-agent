package live

// dummy3.go: the small helpers dummy2.go names, kept together so the diff
// stays reviewable. None of them touch the server.

import (
	"math"
	"sort"
	"strings"
)

// roundFloat rounds a coordinate the way the tank run's go_xyz does.
func roundFloat(f float64) float64 { return math.Round(f) }

// equalFold is strings.EqualFold without importing strings at the call.
func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// sortUnits orders fresh summons nearest first (the claim's rule).
func sortUnits(units []Unit) {
	sort.SliceStable(units, func(i, j int) bool { return units[i].Distance < units[j].Distance })
}

// unitEntry is the unit's entry, -1 when the read failed.
func unitEntry(u Unit, err error) int {
	if err != nil {
		return -1
	}
	return u.Entry
}
