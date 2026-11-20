package world

// The raw gm method's boundary: a line the core runs as one of the unit commands (life, health, power, auras,
// damage) is checked against the same flag, allowlist and player rule as gm_kill, gm_set_hp and gm_aura; every
// other line keeps working with no test-bench flag at all. The tests are against the fake over real sockets, so
// "refused" is also "the fake saw no line", and the classifier itself is pinned without a session.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/rpc"
)

func TestServeGMHandCommand(t *testing.T) {
	cases := []struct {
		text  string
		class serveGMHandClass
		name  string
	}{
		// The core's unit commands, however the caller spells the mark or the case.
		{".die", serveGMHandSelected, ""},
		{".DIE", serveGMHandSelected, ""},
		{"die", serveGMHandSelected, ""},
		{"  .DiE  ", serveGMHandSelected, ""},
		{".kill", serveGMHandSelected, ""},
		{".damage 5", serveGMHandSelected, ""},
		{".dam", serveGMHandSelected, ""}, // the core's own abbreviation (hasStringAbbr)
		{".modify hp 100", serveGMHandSelected, ""},
		{".modify    MANA 100", serveGMHandSelected, ""},
		{".modify rage 5", serveGMHandSelected, ""},
		{".mod h 5", serveGMHandSelected, ""},
		{".modify energy 5", serveGMHandSelected, ""},
		{".modify power 1 5", serveGMHandSelected, ""},
		{".modify runicpower 5", serveGMHandSelected, ""},
		{".aura 845", serveGMHandSelected, ""},
		{".AURA 845", serveGMHandSelected, ""},
		{".au 845", serveGMHandSelected, ""},
		{".unaura 845", serveGMHandSelected, ""},
		{".aura self 845", serveGMHandSelf, ""},
		{".unaura SELF", serveGMHandSelf, ""},
		{".revive", serveGMHandSelected, ""},
		{".revive Testchar", serveGMHandNamed, "Testchar"},
		{".revi Testchar", serveGMHandNamed, "Testchar"},

		// Not unit commands: the ordinary GM console and plain chat.
		{".tb status", serveGMHandNone, ""},
		{".gps", serveGMHandNone, ""},
		{".server info", serveGMHandNone, ""},
		{".modify money 5", serveGMHandNone, ""},
		{".modify", serveGMHandNone, ""},
		{".modify speed 2", serveGMHandNone, ""},
		{".respawn", serveGMHandNone, ""},
		{"hello", serveGMHandNone, ""},
		{"", serveGMHandNone, ""},
		// The core does not run these as commands: a doubled mark is chat, and a delimiter right after the mark
		// is not a command line.
		{"..die", serveGMHandNone, ""},
		{". die", serveGMHandNone, ""},
		{"! .die", serveGMHandNone, ""},
	}
	for _, c := range cases {
		class, name := serveGMHandCommand(c.text)
		if class != c.class || name != c.name {
			t.Fatalf("%q answered class %d name %q, want class %d name %q", c.text, class, name, c.class, c.name)
		}
	}
}

// TestServeRawGMRefusesUnitCommands: with --allow-gm-units on but nothing registered, every spelling of a unit line
// is the typed -32006 and sends nothing; the raw path cannot do what the typed hands refuse.
func TestServeRawGMRefusesUnitCommands(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	units := waitServeUnits(t, a)
	creature := units[0].GUID
	// The caller selects the unit the way target does; the gate must still refuse it.
	a.mustCall(t, "target", map[string]any{"guid": creature})

	for _, text := range []string{
		".die", ".DIE", "die", ".kill", ".damage 5",
		".modify hp 100", ".modify    HP 100", ".mod h 5", ".modify mana 100",
		".aura 845", ".au 845", ".unaura 845", ".aura self 845",
	} {
		a.mustFail(t, "gm", map[string]any{"text": text}, rpc.CodeForbidden, "not registered")
	}
	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if len(lines) != 0 {
		t.Fatalf("a refused raw unit line sent %v", lines)
	}

	// The trial's registration is the same one the typed hands use: with the creature registered the line runs.
	var reg ServeTrialUnits
	json.Unmarshal(a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature}}), &reg)
	var killed ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".die"}), &killed)
	if killed.Sent != ".die" {
		t.Fatalf("the permitted raw .die sent %q", killed.Sent)
	}
	var hp ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".modify HP 100"}), &hp)
	if hp.Sent != ".modify HP 100" {
		t.Fatalf("the permitted raw .modify sent %q", hp.Sent)
	}
	f.mu.Lock()
	lines = append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if !containsLine(lines, ".die") || !containsLine(lines, ".modify HP 100") {
		t.Fatalf("the fake saw %v, want the permitted lines", lines)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeRawGMPassesOrdinaryCommands: without the test-bench flag the raw method still runs ordinary GM lines.
// The unit lines stay shut, and none of them is sent.
func TestServeRawGMPassesOrdinaryCommands(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)

	var out ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".tb status"}), &out)
	if out.Sent != ".tb status" {
		t.Fatalf("the raw .tb line sent %q", out.Sent)
	}
	if !containsLine(out.Lines, "SERVE-BOT-ANSWER") {
		t.Fatalf("the raw .tb line's answer is %v", out.Lines)
	}
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".gps"}), &out)
	if out.Sent != ".gps" {
		t.Fatalf("the raw .gps line sent %q", out.Sent)
	}

	a.mustFail(t, "gm", map[string]any{"text": ".die"}, rpc.CodeForbidden, "--allow-gm-units")
	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	for _, ln := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ln)), ".die") {
			t.Fatalf("a refused raw .die was sent anyway: %v", lines)
		}
	}
	a.stop(t)
	f.wait(t)
}

// TestServeRawGMRevive: .revive is gated too, through both the raw line and the gm_revive method. Another player is
// refused even when registered; this session's own character passes with the flag and its guid registered.
func TestServeRawGMRevive(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	waitServeUnits(t, a)
	var st struct {
		GUID      GUID   `json:"guid"`
		Character string `json:"character"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if st.Character == "" {
		t.Fatal("status has no character name")
	}

	// The refused forms send nothing, and no name other than the session's own character is ever revived.
	a.mustFail(t, "gm", map[string]any{"text": ".revive Someoneelse"}, rpc.CodeForbidden, "other than this session's character")
	a.mustFail(t, "gm_revive", map[string]any{"name": "Someoneelse"}, rpc.CodeForbidden, "other than this session's character")
	// Even registered, another player is refused: the player rule comes before the allowlist.
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{st.GUID, {Low: 0x2222, High: 0x0100000000000000}}})
	a.mustFail(t, "gm_revive", map[string]any{"name": "Someoneelse"}, rpc.CodeForbidden, "other than this session's character")
	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if len(lines) != 0 {
		t.Fatalf("a refused revive sent %v", lines)
	}

	// The session's own name passes (self is on the allowlist); so does the raw form.
	var out ServeCommand
	want := ".revive " + st.Character
	json.Unmarshal(a.mustCall(t, "gm_revive", map[string]any{"name": st.Character}), &out)
	if !strings.EqualFold(out.Sent, want) {
		t.Fatalf("gm_revive sent %q, want %q", out.Sent, want)
	}
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": want}), &out)
	if !strings.EqualFold(out.Sent, want) {
		t.Fatalf("the raw .revive sent %q, want %q", out.Sent, want)
	}
	a.stop(t)
	f.wait(t)
}
