package world

// The wait for a roll's result: the start's own RollTimeMs plus a slack, but never past the cap. RollTimeMs is the
// server's choice and nothing but the run's deadline bounds it otherwise, so a start that asks for days must not
// swallow the run.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/loot"
)

func TestRollResultWaitIsCapped(t *testing.T) {
	watch := func(ms uint32) *rollWatch { return &rollWatch{start: loot.StartRoll{RollTimeMs: ms}} }
	cases := []struct {
		name  string
		start uint32
		want  time.Duration
	}{
		{"no limit", 0, lootRollStartWait + lootRollSlack},
		{"the core's one minute", 60000, time.Minute + lootRollSlack},
		{"a start with days to run", 49 * 24 * 60 * 60 * 1000, lootRollWaitMax},
		{"the largest uint32", 1<<32 - 1, lootRollWaitMax},
	}
	for _, c := range cases {
		if got := rollResultWait([]*rollWatch{watch(c.start)}); got != c.want {
			t.Errorf("%s: a %d ms roll gets %s, want %s", c.name, c.start, got, c.want)
		}
	}

	// Several rolls wait for the longest of them, capped the same way.
	got := rollResultWait([]*rollWatch{watch(60000), watch(1<<32 - 1), watch(300)})
	if got != lootRollWaitMax {
		t.Errorf("the longest of three rolls gets %s, want the cap %s", got, lootRollWaitMax)
	}
	if got := rollResultWait([]*rollWatch{watch(300), watch(60000)}); got != time.Minute+lootRollSlack {
		t.Errorf("two ordinary rolls get %s, want %s", got, time.Minute+lootRollSlack)
	}
}
