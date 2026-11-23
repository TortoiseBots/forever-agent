package scenario

// The shipped camp scenario: A checks the camp is free, works the nearest mob three rounds, and the closing
// combat_near over watch_s must count no stranger hits.

import (
	"testing"
)

func TestScenarioCampStepAsideCheck(t *testing.T) {
	f, err := Load("../../scenarios/forever-camp-step-aside.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess, ok := f.Sessions["A"]; !ok || sess.Server != ServerForever {
		t.Fatalf("session A %+v", f.Sessions["A"])
	}
	for name, want := range map[string]string{
		"camp_x": "-8893", "camp_y": "-118", "camp_z": "3.4", "mob_entry": "6", "watch_s": "180",
	} {
		if got := f.Vars[name]; got != want {
			t.Fatalf("var %s = %q, want %q", name, got, want)
		}
	}

	var checks, attacks, sleeps int
	baseline, final := -1, -1
	var walk *Step
	for i := range f.Steps {
		s := f.Steps[i]
		switch s.Do {
		case "rpc":
			switch s.Method {
			case "walk_to":
				walk = s
			case "attack":
				attacks++
			}
		case "sleep":
			if s.Seconds != 20 {
				t.Fatalf("step %d sleep %v, want 20", i+1, s.Seconds)
			}
			sleeps++
		case "check_rpc":
			if s.Method != "combat_near" {
				continue
			}
			checks++
			if s.Path != "count" || !s.HasEq {
				t.Fatalf("step %d check %s eq %v", i+1, s.Path, s.HasEq)
			}
			if v, _ := s.Equals.(float64); v != 0 {
				t.Fatalf("step %d equals %v, want 0", i+1, s.Equals)
			}
			if baseline < 0 {
				baseline = i
			}
			final = i
		}
	}
	if walk == nil || walk.Params["x"] != "${camp_x}" || walk.Params["y"] != "${camp_y}" || walk.Params["z"] != "${camp_z}" {
		t.Fatalf("walk_to %+v", walk)
	}
	if checks != 2 || baseline < 0 || final == baseline {
		t.Fatalf("%d combat_near checks, baseline %d final %d", checks, baseline, final)
	}
	if attacks != 3 || sleeps != 3 {
		t.Fatalf("%d attacks, %d sleeps; want three attack/sleep pairs", attacks, sleeps)
	}

	base := f.Steps[baseline]
	if base.Params["who"] != "others" || base.Params["window_s"] != float64(5) || base.Name != "camp busy" || base.OnFail != "stop" {
		t.Fatalf("baseline %+v", base.Params)
	}
	last := f.Steps[final]
	if last.Params["who"] != "others" || last.Params["radius"] != float64(30) || last.Params["window_s"] != "${watch_s}" {
		t.Fatalf("final %+v", last.Params)
	}
	// The numeric vars reach the params as numbers (a params decoder takes numbers, not their text).
	got, err := expandParams(last.Params, f.Vars)
	if err != nil {
		t.Fatal(err)
	}
	if pm := got.(map[string]any); pm["window_s"] != float64(180) {
		t.Fatalf("final window_s %#v", pm["window_s"])
	}
	got, err = expandParams(walk.Params, f.Vars)
	if err != nil {
		t.Fatal(err)
	}
	if pm := got.(map[string]any); pm["x"] != float64(-8893) || pm["z"] != 3.4 {
		t.Fatalf("walk_to coords %#v %#v", pm["x"], pm["z"])
	}
}
