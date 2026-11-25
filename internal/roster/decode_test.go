package roster

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// botRecord returns one valid bot record; a value of nil deletes that key.
func botRecord(changes map[string]any) map[string]any {
	b := map[string]any{
		"name": "Ember", "guid": 1, "class": "Druid", "level": 60,
		"hp": 900, "max_hp": 1000, "power": 450, "max_power": 500, "power_type": "mana",
		"map": 0, "zone": "Elwynn Forest", "instance": "",
		"x": 1.5, "y": 2.5, "z": 3.5, "o": 1.2,
		"target": "", "group": "g1", "kind": "pool", "state": "moving",
	}
	for k, v := range changes {
		if v == nil {
			delete(b, k)
		} else {
			b[k] = v
		}
	}
	return b
}

func rosterJSON(t *testing.T, bots ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"bots": bots})
	if err != nil {
		t.Fatalf("marshalling the fake answer: %v", err)
	}
	return body
}

func decodeOne(t *testing.T, bots ...map[string]any) *Roster {
	t.Helper()
	r, err := DecodeRoster(rosterJSON(t, bots...))
	if err != nil {
		t.Fatalf("DecodeRoster: %v", err)
	}
	return r
}

// TestEachRequiredFieldIsRejected is review point 2: the fields the watch rules read are not
// allowed to default to zero.
func TestEachRequiredFieldIsRejected(t *testing.T) {
	for _, field := range []string{"hp", "max_hp", "map", "instance", "x", "y", "z", "state"} {
		t.Run(field, func(t *testing.T) {
			r := decodeOne(t, botRecord(map[string]any{field: nil}))
			if len(r.Bots) != 0 {
				t.Fatalf("a record with %s missing was kept: %+v", field, r.Bots)
			}
			if len(r.Rejected) != 1 {
				t.Fatalf("rejections = %+v, want one", r.Rejected)
			}
			if !strings.Contains(r.Rejected[0].Reason, field) {
				t.Fatalf("reason %q does not name %s", r.Rejected[0].Reason, field)
			}
			if r.Rejected[0].Index != 0 || r.Rejected[0].Name != "Ember" {
				t.Fatalf("rejection = %+v, want index 0 and the name", r.Rejected[0])
			}
		})
	}
}

func TestRequiredFieldValuesAreChecked(t *testing.T) {
	cases := []struct {
		name    string
		changes map[string]any
		want    string
	}{
		{"hp over max", map[string]any{"hp": 1001}, "hp"},
		{"negative hp", map[string]any{"hp": -1}, "hp"},
		{"zero max hp while live", map[string]any{"hp": 0, "max_hp": 0}, "max_hp"},
		{"negative max hp", map[string]any{"max_hp": -5, "hp": 0}, "max_hp"},
		{"negative map", map[string]any{"map": -1}, "map"},
		{"state not a string", map[string]any{"state": true}, "expected a string or a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := decodeOne(t, botRecord(tc.changes))
			if len(r.Bots) != 0 || len(r.Rejected) != 1 {
				t.Fatalf("bots %+v, rejections %+v", r.Bots, r.Rejected)
			}
			if !strings.Contains(r.Rejected[0].Reason, tc.want) {
				t.Fatalf("reason %q does not name %q", r.Rejected[0].Reason, tc.want)
			}
		})
	}

	// A dead bot may have max_hp 0: the corpse has no maximum to compare against.
	dead := decodeOne(t, botRecord(map[string]any{"state": "dead", "hp": 0, "max_hp": 0}))
	if len(dead.Bots) != 1 || len(dead.Rejected) != 0 {
		t.Fatalf("a dead bot with max_hp 0: bots %+v, rejections %+v", dead.Bots, dead.Rejected)
	}
}

func TestNonFinitePositionIsRejected(t *testing.T) {
	const body = `{"bots":[{"name":"Ember","guid":1,"kind":"pool","state":"moving","hp":1,"max_hp":1,"map":0,"instance":"","x":1e999,"y":0,"z":0}]}`
	r, err := DecodeRoster([]byte(body))
	if err != nil {
		t.Fatalf("DecodeRoster: %v", err)
	}
	if len(r.Bots) != 0 || len(r.Rejected) != 1 {
		t.Fatalf("bots %+v, rejections %+v, want the record rejected", r.Bots, r.Rejected)
	}
}

func TestOneBadRecordDoesNotDropTheGoodOnes(t *testing.T) {
	r := decodeOne(t,
		botRecord(map[string]any{"name": "Ember", "guid": 1}),
		botRecord(map[string]any{"name": "Cinder", "guid": 2, "hp": nil}),
		botRecord(map[string]any{"name": "Ash", "guid": 3, "state": "idle"}),
	)
	if len(r.Bots) != 2 || r.Bots[0].Name != "Ember" || r.Bots[1].Name != "Ash" {
		t.Fatalf("bots = %+v, want Ember and Ash", r.Bots)
	}
	if len(r.Rejected) != 1 || r.Rejected[0].Index != 1 || r.Rejected[0].Name != "Cinder" {
		t.Fatalf("rejections = %+v, want index 1 for Cinder", r.Rejected)
	}
	if r.Bots[0].HP != 900 || r.Bots[0].X != 1.5 {
		t.Fatalf("the good record lost its values: %+v", r.Bots[0])
	}
}

func TestABadRecordNeverBecomesAFlag(t *testing.T) {
	tr := NewTracker(Config{StuckAfter: time.Second, LoneCombatAfter: time.Second, GroupDistance: 10})
	good := rosterJSON(t,
		botRecord(map[string]any{"name": "Ember", "guid": 1, "state": "idle"}),
		botRecord(map[string]any{"name": "Cinder", "guid": 2, "state": "idle"}),
	)
	first, err := DecodeRoster(good)
	if err != nil {
		t.Fatalf("DecodeRoster: %v", err)
	}
	if ev := tr.Observe(baseTime, first); len(ev) != 0 {
		t.Fatalf("baseline = %+v", ev)
	}

	// Ember's record loses hp: it is rejected, so the bot is absent, not dead, not stuck.
	bad := rosterJSON(t,
		botRecord(map[string]any{"name": "Ember", "guid": 1, "hp": nil, "state": "dead"}),
		botRecord(map[string]any{"name": "Cinder", "guid": 2, "state": "dead", "hp": 0, "max_hp": 0}),
	)
	second, err := DecodeRoster(bad)
	if err != nil {
		t.Fatalf("DecodeRoster: %v", err)
	}
	if len(second.Bots) != 1 || len(second.Rejected) != 1 {
		t.Fatalf("bots %+v, rejections %+v", second.Bots, second.Rejected)
	}
	events := tr.Observe(baseTime.Add(time.Second), second)
	if len(events) != 1 || events[0].Bot != "Cinder" || events[0].Kind != EventDeath {
		t.Fatalf("events = %+v, want only Cinder's death", events)
	}
}

func TestRejectedRecordsAreSkippedNotFatal(t *testing.T) {
	r := decodeOne(t, botRecord(map[string]any{"hp": nil}), botRecord(map[string]any{"name": "Ash", "guid": 3}))
	if len(r.Rejected) != 1 || len(r.Bots) != 1 {
		t.Fatalf("bots %+v, rejections %+v", r.Bots, r.Rejected)
	}
	if r.Bots[0].Name != "Ash" {
		t.Fatalf("kept %q, want Ash", r.Bots[0].Name)
	}
}

// A rejection carries the guid when the record has one, so a watch can identify the bot across
// a reorder instead of keying on the array position.
func TestRejectionCarriesTheGUID(t *testing.T) {
	r := decodeOne(t, botRecord(map[string]any{"name": "Cinder", "guid": 2, "hp": nil}))
	if len(r.Rejected) != 1 {
		t.Fatalf("rejections = %+v, want one", r.Rejected)
	}
	if r.Rejected[0].Name != "Cinder" || r.Rejected[0].GUID != "2" {
		t.Fatalf("rejection = %+v, want name Cinder and guid 2", r.Rejected[0])
	}
}

// TestMacroStatesAreKept is #236 point 2: the port's own states (busy, stalled, resting) are a bot's state like any
// other, so a record in one is in the roster and not a rejection; the watch rules still read only moving, combat and
// dead, so a busy bot is not a stuck flag.
func TestMacroStatesAreKept(t *testing.T) {
	for _, state := range []string{StateBusy, StateStalled, StateResting} {
		t.Run(state, func(t *testing.T) {
			r := decodeOne(t, botRecord(map[string]any{"state": state}))
			if len(r.Rejected) != 0 || len(r.Bots) != 1 {
				t.Fatalf("bots %+v, rejections %+v, want the record kept", r.Bots, r.Rejected)
			}
			if got := string(r.Bots[0].State); got != state {
				t.Fatalf("state %q, want %q", got, state)
			}
		})
	}

	// A busy bot stands still for longer than the stuck threshold and is still not stuck: the rule is about a bot
	// whose state says moving.
	tr := NewTracker(Config{StuckAfter: time.Second})
	at := time.Unix(0, 0)
	tr.Observe(at, sample(testBot("Ember", "1", "", StateBusy, 0, 0, 0)))
	for i := 1; i <= 3; i++ {
		if ev := tr.Observe(at.Add(time.Duration(i)*time.Second), sample(testBot("Ember", "1", "", StateBusy, 0, 0, 0))); len(ev) != 0 {
			t.Fatalf("a busy bot fired %+v, want no event", ev)
		}
	}
}
