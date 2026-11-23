package world

// `gm_set_hp`'s current-only form: the core's `.modify currenthp` sets the selected unit's current health and leaves
// its maximum alone (cs_modify.cpp:152-175), so a trial can make a hurt unit without replacing its maximum. The old
// form is `.modify hp`, unchanged.

import (
	"encoding/json"
	"testing"
)

func TestServeGMSetHPCurrentOnly(t *testing.T) {
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
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature, st.GUID}})

	var hp ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_set_hp", map[string]any{"guid": creature, "hp": 50, "current_only": true}), &hp)
	if hp.Sent != ".modify currenthp 50" {
		t.Fatalf("gm_set_hp current_only sent %q, want .modify currenthp 50", hp.Sent)
	}
	// Without the flag the line is the old one: current and maximum both set.
	json.Unmarshal(a.mustCall(t, "gm_set_hp", map[string]any{"guid": creature, "hp": 50}), &hp)
	if hp.Sent != ".modify hp 50" {
		t.Fatalf("gm_set_hp sent %q, want .modify hp 50", hp.Sent)
	}
	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if !containsLine(lines, ".modify currenthp 50") || !containsLine(lines, ".modify hp 50") {
		t.Fatalf("the fake saw %v, want both modify lines", lines)
	}
	a.stop(t)
	f.wait(t)
}
