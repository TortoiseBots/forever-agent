package world

// #216: bot_view is the serve's own snapshot of its bots — the party roster joined to the object store — not the
// 1.12 perception feed. The tests pin the join: a party row for every bot but this character, the unit row when the
// model holds the unit, and a name in neither answered honestly (in_party and in_view false).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestServeBotViewParty(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Tanka", "Heala"}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var view ServeBotView
	json.Unmarshal(a.mustCall(t, "bot_view", nil), &view)
	if !view.Party {
		t.Fatalf("no party: %+v", view)
	}
	if view.Self != "XXXXXX" {
		t.Fatalf("self %q", view.Self)
	}
	if len(view.Bots) != 2 {
		t.Fatalf("bots %+v: want the two party bots, not this character", view.Bots)
	}
	// Both bots are in the roster and neither's own GUID is in the object store (the fake sends no create block
	// for a party member): the roster row stays, in_view is false. A unit of the same name is never substituted.
	if b := view.Bots[0]; b.Name != "Tanka" || !b.InParty || b.InView || b.Unit != nil || wire.GUID(b.GUID).IsZero() {
		t.Fatalf("Tanka: %+v", b)
	}
	if b := view.Bots[1]; b.Name != "Heala" || !b.InParty || b.InView || b.Unit != nil {
		t.Fatalf("Heala: %+v", b)
	}

	// The explicit list narrows the snapshot and a name that is nowhere is answered honestly.
	json.Unmarshal(a.mustCall(t, "bot_view", map[string]any{"bots": []string{"Heala"}, "wait_ms": 50}), &view)
	if len(view.Bots) != 1 || view.Bots[0].Name != "Heala" || !view.Bots[0].InParty {
		t.Fatalf("named bot_view: %+v", view.Bots)
	}
	json.Unmarshal(a.mustCall(t, "bot_view", map[string]any{"bots": []string{"Ghostly"}}), &view)
	if len(view.Bots) != 1 || view.Bots[0].Name != "Ghostly" || view.Bots[0].InParty || view.Bots[0].InView {
		t.Fatalf("unknown name: %+v", view.Bots)
	}
	a.mustFail(t, "bot_view", map[string]any{"bots": []string{" "}}, -32003, "a bot name is empty")
	a.stop(t)
	f.wait(t)
}

func TestServeBotViewNoParty(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveLeaderName = "Tanka"
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var view ServeBotView
	json.Unmarshal(a.mustCall(t, "bot_view", nil), &view)
	if view.Party || len(view.Bots) != 0 {
		t.Fatalf("no party: %+v", view)
	}
	if len(view.Notes) != 1 || !strings.Contains(view.Notes[0], "name the bots to view") {
		t.Fatalf("notes %v", view.Notes)
	}
	// A named unit outside a party is still found by the object store's name lookup.
	json.Unmarshal(a.mustCall(t, "bot_view", map[string]any{"bots": []string{"Tanka"}}), &view)
	if len(view.Bots) != 1 || !view.Bots[0].InView || view.Bots[0].InParty || view.Bots[0].Unit == nil {
		t.Fatalf("named unit: %+v", view.Bots)
	}
	a.stop(t)
	f.wait(t)
}
