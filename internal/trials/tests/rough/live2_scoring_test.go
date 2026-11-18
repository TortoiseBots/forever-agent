package rough

// Translated from tests/test_live2.py: the FloorReasonTest case (score.cut_short_reason).
// The other classes of that file need the live runner (RaceAgent, guardian/setup/teardown).

import (
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

func TestFloorReasonTest_AFloorEndNamesItsRescues(t *testing.T) {
	// live2 rep1 'cut short (27 s, ended: floor)': six gm_heal rescues, five of them misdirected.
	marks := []rules.Row{
		{"kind": "setup_ok", "t": 0.0, "wall": "2026-11-08T04:04:09.730+01:00"},
		{"kind": "fight_start", "t": 11.07, "wall": "2026-11-08T04:04:20.797+01:00"},
	}
	for i := range 6 {
		marks = append(marks, rules.Row{
			"kind": "gm_heal", "hp": int64(325), "max_hp": int64(1189), "pct": 27.3, "to": int64(564),
			"t": float64(37 + i), "wall": fmt.Sprintf("2026-11-08T04:04:%02d.912+01:00", 46+i),
		})
	}
	marks = append(marks, rules.Row{
		"kind": "fight_end", "ended": "floor", "t": 46.1, "wall": "2026-11-08T04:04:55.828+01:00",
	})
	got := score.CutShortReason(marks, 27.1)
	want := "cut short (27 s, ended: floor after 6 rescues)"
	if got != want {
		t.Fatalf("cut short reason: got %q, want %q", got, want)
	}
	got = score.CutShortReason([]rules.Row{{"kind": "fight_end", "ended": "wiped"}}, 5.0)
	if want := "cut short (5 s, ended: wiped)"; got != want {
		t.Fatalf("cut short reason: got %q, want %q", got, want)
	}
}
