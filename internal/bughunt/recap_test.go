package bughunt

// The deathrecap carry-through: the watch's summary death log carries the
// parsed `.deathrecap` timeline, the loader attaches it to the dated death of
// the same member, Hit carries the nearest death's timeline, and the ticket
// text prints it beside the evidence.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecapReachesHitsAndTickets(t *testing.T) {
	dir := t.TempDir()
	writeWatch(t, dir)
	runID := "r-20261120T100000-abcd"
	sumPath := filepath.Join(dir, runID+".forever-run.json")
	b, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatal(err)
	}
	var sum map[string]any
	if err := json.Unmarshal(b, &sum); err != nil {
		t.Fatal(err)
	}
	watch, _ := sum["watch"].(map[string]any)
	// The dead member is Cmage (the trace's name for gDps); the summary's
	// death log carries the event's own words (Why the name, Who the slot).
	watch["death_log"] = []any{map[string]any{
		"at_s": 2, "who": "dps", "why": "Cmage",
		"recap": []string{"Recap #1 of Cmage: map 309 instance 1, 1 frames, 0 events, offsets in seconds from the death.", "no casts recorded"},
	}}
	sum["watch"] = watch
	out, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sumPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadWatch(dir, "watchds")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(r.Deaths) != 1 || len(r.Deaths[0].Recap) != 2 {
		t.Fatalf("deaths %+v, want the DPS death with the 2-line timeline", r.Deaths)
	}
	h := r.Hit(9, 2, 0, gDps, "z fell 12 yd in 4.0 s before death (heuristic, no damage-source row)", []string{"9", "z-drop"})
	if len(h.Recap) != 2 || h.Recap[1] != "no casts recorded" {
		t.Fatalf("hit recap %+v", h.Recap)
	}
	tickets := BuildTickets([]Hit{h}, nil, func(int) bool { return true })
	if len(tickets) != 1 {
		t.Fatalf("tickets %+v", tickets)
	}
	if len(tickets[0].Evidence) != 3 || tickets[0].Evidence[1] != h.Clock+" watchds/"+r.Name+": deathrecap: "+h.Recap[0] || tickets[0].Evidence[2] != h.Clock+" watchds/"+r.Name+": deathrecap: no casts recorded" {
		t.Fatalf("ticket evidence %+v", tickets[0].Evidence)
	}
}

// TestRecapMatchesEachDeathByTime: a member's two deaths each take their own recap (nearest by time, each death
// consumed once), never the concatenation or the first death's.
func TestRecapMatchesEachDeathByTime(t *testing.T) {
	var sum foreverSummary
	raw := `{"watch":{"death_log":[
		{"at_s":10,"who":"dps","why":"Cmage","recap":["first"]},
		{"at_s":40,"who":"dps","why":"Cmage","recap":["second"]}]}}`
	if err := json.Unmarshal([]byte(raw), &sum); err != nil {
		t.Fatal(err)
	}
	r := &Run{
		Names:  map[string]string{gDps: "Cmage"},
		Roles:  map[string]string{gDps: "dps"},
		Deaths: []Death{{T: 11, Bot: gDps}, {T: 41, Bot: gDps}},
	}
	r.recapFrom(&sum)
	if len(r.Deaths[0].Recap) != 1 || r.Deaths[0].Recap[0] != "first" || len(r.Deaths[1].Recap) != 1 || r.Deaths[1].Recap[0] != "second" {
		t.Fatalf("deaths %+v, want each death to carry its own recap", r.Deaths)
	}
	// A log entry with no death left to take it attaches to nothing.
	r2 := &Run{Names: r.Names, Roles: r.Roles, Deaths: []Death{{T: 41, Bot: gDps}}}
	r2.recapFrom(&sum)
	if len(r2.Deaths[0].Recap) != 1 || r2.Deaths[0].Recap[0] != "second" {
		t.Fatalf("one death %+v, want the nearer (second) recap", r2.Deaths)
	}
}

func TestHitTakesTheNearestDeathsRecap(t *testing.T) {
	r := &Run{Deaths: []Death{
		{T: 1, Bot: gDps, Recap: []string{"early"}},
		{T: 12, Bot: gDps, Recap: []string{"near"}},
	}}
	h := r.Hit(9, 10, 5, gDps, "x", []string{"k"})
	if len(h.Recap) != 1 || h.Recap[0] != "near" {
		t.Fatalf("hit recap %+v, want the nearest death's", h.Recap)
	}
}
