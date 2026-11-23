package scenario

// The shipped quest group offer scenario (#112): A waits ungrouped on a quest target for a same-quest bot's invite,
// accepts it, and the closing combat_near must count a party hit on A's target. QUEST_ID, TARGET_X/Y/Z and
// MOB_ENTRY have no default: the live runner names them, and the file must refuse to load without them.

import (
	"regexp"
	"strings"
	"testing"
)

func TestScenarioQuestGroupOfferCheck(t *testing.T) {
	vars := map[string]string{
		"QUEST_ID":  "25161",
		"TARGET_X":  "-8893",
		"TARGET_Y":  "-118",
		"TARGET_Z":  "3.4",
		"MOB_ENTRY": "6",
	}
	f, err := Load("../../scenarios/forever-quest-group-offer.json", vars)
	if err != nil {
		t.Fatal(err)
	}
	if sess, ok := f.Sessions["A"]; !ok || sess.Server != ServerForever {
		t.Fatalf("session A %+v", f.Sessions["A"])
	}
	if got := f.Vars["WAIT_S"]; got != "120" {
		t.Fatalf("var WAIT_S = %q, want 120", got)
	}
	if got := f.Vars["QUEST_ID"]; got != "25161" {
		t.Fatalf("var QUEST_ID = %q, want the override", got)
	}

	// Every required var is named when one is missing, and the load fails.
	_, err = Load("../../scenarios/forever-quest-group-offer.json", map[string]string{
		"TARGET_X": "-8893", "TARGET_Y": "-118", "TARGET_Z": "3.4", "MOB_ENTRY": "6",
	})
	if err == nil || !strings.Contains(err.Error(), "QUEST_ID") {
		t.Fatalf("load without QUEST_ID: %v", err)
	}

	var walk, expect, accept *Step
	var checks, attacks, sleeps int
	for i := range f.Steps {
		s := f.Steps[i]
		switch s.Do {
		case "rpc":
			switch s.Method {
			case "walk_to":
				walk = s
			case "attack":
				attacks++
			case "accept_invite":
				accept = s
			}
		case "sleep":
			if s.Seconds != 15 {
				t.Fatalf("step %d sleep %v, want 15", i+1, s.Seconds)
			}
			sleeps++
		case "expect":
			expect = s
		case "check_rpc":
			checks++
			if s.Method == "party" {
				if s.Path != "in_group" || !s.HasEq || s.Equals != true || s.WithinS != 5 {
					t.Fatalf("party check step %d: path %q eq %v within %v", i+1, s.Path, s.Equals, s.WithinS)
				}
				continue
			}
			if s.Method != "combat_near" {
				t.Fatalf("step %d check %s", i+1, s.Method)
			}
			if s.Params["who"] != "party" {
				t.Fatalf("combat_near who %v, want party (another player's hits are not the bot's)", s.Params["who"])
			}
			if s.Params["window_s"] != float64(40) || s.Path != "count" || !s.HasRe {
				t.Fatalf("combat_near step %d: %+v path %q re %v", i+1, s.Params, s.Path, s.HasRe)
			}
			if ok, _ := regexp.MatchString(s.MatchRe, "1"); !ok {
				t.Fatalf("combat_near match %q does not accept a hit count", s.MatchRe)
			}
			if ok, _ := regexp.MatchString(s.MatchRe, "0"); ok {
				t.Fatalf("combat_near match %q accepts zero hits", s.MatchRe)
			}
		}
	}
	if walk == nil || walk.Params["x"] != "${TARGET_X}" || walk.Params["y"] != "${TARGET_Y}" || walk.Params["z"] != "${TARGET_Z}" {
		t.Fatalf("walk_to %+v", walk)
	}
	if expect == nil || expect.Session != "A" || expect.Kind != "party" || expect.Match != "invite" {
		t.Fatalf("expect %+v", expect)
	}
	if expect.TimeoutS != 120 {
		t.Fatalf("expect timeout_s %v, want the WAIT_S default 120 resolved from the var", expect.TimeoutS)
	}
	if accept == nil || !accept.RecordResult {
		t.Fatalf("accept_invite %+v, want its result recorded", accept)
	}
	if checks != 2 || attacks != 3 || sleeps != 2 {
		t.Fatalf("%d checks, %d attacks, %d sleeps; want two checks, three attacks and two attack/sleep pairs", checks, attacks, sleeps)
	}
	// A --var override reaches the wait, and the numeric walk_to params.
	waited, err := Load("../../scenarios/forever-quest-group-offer.json", map[string]string{
		"QUEST_ID": "25161", "TARGET_X": "-8893", "TARGET_Y": "-118", "TARGET_Z": "3.4", "MOB_ENTRY": "6", "WAIT_S": "30",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range waited.Steps {
		if s.Do == "expect" && s.TimeoutS != 30 {
			t.Fatalf("expect timeout_s %v with WAIT_S=30", s.TimeoutS)
		}
	}
	got, err := expandParams(walk.Params, f.Vars)
	if err != nil {
		t.Fatal(err)
	}
	if pm := got.(map[string]any); pm["x"] != float64(-8893) || pm["z"] != 3.4 {
		t.Fatalf("walk_to coords %#v %#v", pm["x"], pm["z"])
	}
}
