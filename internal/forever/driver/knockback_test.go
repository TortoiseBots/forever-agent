package driver

// The knockback rows a watch drains also land on the trace as knockback and knockback_land events, so the landing's
// position reads beside the frames and the mobs.

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trace"
)

func TestCombatEventsWriteKnockbacks(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	rec, err := NewWatchRecorder(dir, "r-knock", "plan", 1, 1, start, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := []json.RawMessage{
		json.RawMessage(`{"seq":1,"kind":"knockback","time":"2026-01-02T03:04:10.500+00:00","source":{"guid":"0xAB"},"pos":{"x":1,"y":2,"z":3},"self":true,"horz_speed":12.5,"vert_speed":9.5}`),
		json.RawMessage(`{"seq":2,"kind":"knockback_land","time":"2026-01-02T03:04:12.000+00:00","source":{"guid":"0xAB"},"pos":{"x":4,"y":5,"z":6},"air_ms":1500,"drop":3}`),
		json.RawMessage(`{"seq":3,"kind":"melee","time":"2026-01-02T03:04:12.500+00:00","source":{"guid":"0xAB"}}`),
	}
	rec.CombatEvents(rows)
	rec.Finish(start.Add(20*time.Second), "clear", "")
	tf, err := trace.Read(filepath.Join(dir, "r-knock.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var takes, lands []trace.Event
	for _, e := range tf.Events {
		switch e.Ev {
		case "knockback":
			takes = append(takes, e)
		case "knockback_land":
			lands = append(lands, e)
		}
	}
	if len(takes) != 1 || len(lands) != 1 {
		t.Fatalf("takeoffs %d, landings %d", len(takes), len(lands))
	}
	if takes[0].T != 10500 || lands[0].T != 12000 {
		t.Fatalf("trace times %d and %d, want 10500 and 12000", takes[0].T, lands[0].T)
	}
	if takes[0].D["self"] != true || takes[0].D["speed_xy"] != float64(12.5) {
		t.Fatalf("takeoff fields %v", takes[0].D)
	}
	if lands[0].D["air_ms"] != float64(1500) || lands[0].D["drop"] != float64(3) {
		t.Fatalf("landing fields %v", lands[0].D)
	}
	pos, ok := lands[0].D["pos"].(map[string]any)
	if !ok || pos["z"] != float64(6) {
		t.Fatalf("landing position %v", lands[0].D["pos"])
	}
}
