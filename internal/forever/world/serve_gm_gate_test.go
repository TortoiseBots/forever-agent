package world

// The GM unit hands' boundary: a serving daemon refuses gm_kill, gm_set_hp and gm_aura unless it was started as a
// test bench (--allow-gm-units) and the target is a guid the runner registered through trial_units; a player other
// than the session's own character is refused whatever the allowlist says. Every refusal is a typed rpc.Error and
// sends nothing, so a caller can prove the boundary from the fake's own packet record.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

func TestServeGMUnitsOffByDefault(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	units := waitServeUnits(t, a)
	creature := units[0].GUID
	var st struct {
		GUID GUID `json:"guid"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)

	// The allowlist cannot even be set: without the flag there is no GM unit path at all.
	a.mustFail(t, "trial_units", map[string]any{"guids": []string{creature.String()}}, rpc.CodeForbidden, "--allow-gm-units")
	a.mustFail(t, "gm_kill", map[string]any{"guid": creature}, rpc.CodeForbidden, "--allow-gm-units")
	a.mustFail(t, "gm_set_hp", map[string]any{"guid": st.GUID, "hp": 100}, rpc.CodeForbidden, "--allow-gm-units")
	a.mustFail(t, "gm_aura", map[string]any{"guid": creature, "spell": 845}, rpc.CodeForbidden, "--allow-gm-units")

	f.mu.Lock()
	selections, lines := append([]wire.GUID(nil), f.serveSelections...), append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if len(selections) != 0 || len(lines) != 0 {
		t.Fatalf("a refused GM hand sent selections %v and lines %v", selections, lines)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeGMUnitsAllowlist(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	units := waitServeUnits(t, a)
	creature := units[0].GUID
	var st struct {
		GUID GUID `json:"guid"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)

	// The flag alone opens nothing: the runner's registration does.
	a.mustFail(t, "gm_kill", map[string]any{"guid": creature}, rpc.CodeForbidden, "not registered")
	a.mustFail(t, "gm_set_hp", map[string]any{"guid": st.GUID, "hp": 100}, rpc.CodeForbidden, "not registered")
	a.mustFail(t, "gm_aura", map[string]any{"guid": creature, "spell": 845}, rpc.CodeForbidden, "not registered")

	var reg ServeTrialUnits
	json.Unmarshal(a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature, st.GUID}}), &reg)
	if len(reg.GUIDs) != 2 || reg.GUIDs[0] != creature || reg.GUIDs[1] != st.GUID {
		t.Fatalf("trial_units registered %v", reg.GUIDs)
	}
	var aura ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_aura", map[string]any{"guid": creature, "spell": 845}), &aura)
	if aura.Sent != ".aura 845" {
		t.Fatalf("gm_aura sent %q", aura.Sent)
	}

	// Registration replaces the list: a unit left out is refused again, before any selection is sent.
	json.Unmarshal(a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature}}), &reg)
	f.mu.Lock()
	before := len(f.serveSelections)
	f.mu.Unlock()
	a.mustFail(t, "gm_set_hp", map[string]any{"guid": st.GUID, "hp": 100}, rpc.CodeForbidden, "not registered")
	f.mu.Lock()
	after := len(f.serveSelections)
	f.mu.Unlock()
	if after != before {
		t.Fatalf("a refused gm_set_hp sent a selection (%d -> %d)", before, after)
	}

	// A zero guid is a malformed registration, not an allow-everything.
	a.mustFail(t, "trial_units", map[string]any{"guids": []string{"0x0"}}, rpc.CodeInvalidParams, "zero guid")

	// A registered guid whose unit the model does not hold (a stale one from before the creature left view) is
	// refused by name, still typed and still with nothing sent.
	stale := GUID{Low: 0xDEAD, High: 0x0F00000000000000}
	json.Unmarshal(a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{stale}}), &reg)
	f.mu.Lock()
	before = len(f.serveSelections)
	f.mu.Unlock()
	a.mustFail(t, "gm_aura", map[string]any{"guid": stale, "spell": 845}, -32003, "not in view")
	f.mu.Lock()
	after = len(f.serveSelections)
	f.mu.Unlock()
	if after != before {
		t.Fatalf("a stale guid sent a selection (%d -> %d)", before, after)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeGMUnitRefusesAnotherPlayer is the player rule without a socket: a player guid is refused even when the
// runner registered it, and only this session's own character passes.
func TestServeGMUnitRefusesAnotherPlayer(t *testing.T) {
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	self := wire.GUID{Low: 0x11, High: 0x0100000000000000}
	other := wire.GUID{Low: 0x22, High: 0x0100000000000000}
	s.self, s.selfSeen = self, true
	st := &serveState{allowGMUnits: true, gmUnits: map[wire.GUID]struct{}{self: {}, other: {}}}
	st.install(s, 1)

	err := s.serveGMUnit(&objects.Object{GUID: other, Type: objects.TypePlayer}, "gm_set_hp")
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeForbidden {
		t.Fatalf("another player's gm_set_hp answered %v, want the typed refusal", err)
	}
	if !strings.Contains(re.Message, "other than this session's character") {
		t.Fatalf("the refusal is %q", re.Message)
	}
	if err := s.serveGMUnit(&objects.Object{GUID: self, Type: objects.TypePlayer}, "gm_set_hp"); err != nil {
		t.Fatalf("the session's own character was refused: %v", err)
	}
	// The flag is the outer wall: with it off even the registered own character is refused.
	st.allowGMUnits = false
	if err := s.serveGMUnit(&objects.Object{GUID: self, Type: objects.TypePlayer}, "gm_set_hp"); err == nil {
		t.Fatal("a daemon without --allow-gm-units allowed a GM hand")
	}
}

// TestServeGMUnitsSurviveReconnect: the allowlist is the runner's registration for the trial, not the session's, so
// a re-entry keeps it and the creature it names is still open to the hands.
func TestServeGMUnitsSurviveReconnect(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	waitServeState(t, a, "online", steps)
	units := waitServeUnits(t, a)
	creature := units[0].GUID
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature}})

	dropServingSocket(t, f)
	deadline := time.Now().Add(15 * time.Second)
	for f.serveSessionCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.serveSessionCount() < 2 {
		t.Fatalf("the fake never ran a second session\n%s", steps.joined())
	}
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	var aura ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_aura", map[string]any{"guid": creature, "spell": 845}), &aura)
	if aura.Sent != ".aura 845" {
		t.Fatalf("the hand after the reconnect sent %q", aura.Sent)
	}
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown after the re-entry: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
}
