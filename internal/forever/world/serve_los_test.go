package world

// The los method (#259) against the fake module: one `.tb los` line through the session's command channel, the
// module's one-line answer read strictly, a refusal returned as the call's result with its code, and a module build
// without the command answering a clear "not supported" error rather than a parse error or a timeout.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestServeLOSAnswer: the method sends the line the typed client builds and answers the module's record field by
// field.
func TestServeLOSAnswer(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		if !strings.HasPrefix(line, ".tb los ") {
			return nil
		}
		return []string{"los a=Tanka b=target map=530 los=1 dist=12.5 same_map=1 in_world=1 #T"}
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeLOS
	json.Unmarshal(a.mustCall(t, "los", map[string]any{"a": "Tanka", "b": "target"}), &out)
	if !out.Clear || out.Reason != "" || !out.SameMap || !out.InWorld {
		t.Fatalf("answer %+v", out)
	}
	if out.Map != 530 || out.DistYD == nil || *out.DistYD != 12.5 {
		t.Fatalf("answer %+v: map %d dist %v", out, out.Map, out.DistYD)
	}
	if out.A != "Tanka" || out.B != "target" {
		t.Fatalf("answer sides %q %q", out.A, out.B)
	}
	lines := f.serveTBLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], ".tb los Tanka target ") {
		t.Fatalf("the fake saw %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeLOSGuidAnswersTheName: the module answers a resolved guid side with the unit's NAME, and the method returns
// that name in the side's field. The plan scout sends a spot and a pack guid, so this is its every successful query.
func TestServeLOSGuidAnswersTheName(t *testing.T) {
	const guid = "0000000000000000000000000000BEEF"
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		if !strings.HasPrefix(line, ".tb los ") {
			return nil
		}
		return []string{"los a=-7.00,0.00,0.00 b=ForeverRat map=700 los=1 dist=3.0 same_map=1 in_world=1 #T"}
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeLOS
	json.Unmarshal(a.mustCall(t, "los", map[string]any{"a": "-7,0,0", "b": "0x" + guid}), &out)
	if out.Reason != "" || !out.Clear || out.Map != 700 {
		t.Fatalf("answer %+v", out)
	}
	if out.A != "-7.00,0.00,0.00" || out.B != "ForeverRat" {
		t.Fatalf("answer sides %q %q, want the spot echoed with two decimals and the unit's name", out.A, out.B)
	}
	if out.DistYD == nil || *out.DistYD != 3 {
		t.Fatalf("answer distance %v, want 3", out.DistYD)
	}
	lines := f.serveTBLines()
	if len(lines) != 1 || !strings.Contains(lines[0], strings.ToLower(guid)) {
		t.Fatalf("the fake module saw %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeLOSRefusalIsAResult: a refusal the module writes into its one line (no access, a rate, two maps) is the
// call's result with reason set — a polling tool needs the code — and the fields the refusal hid stay absent.
func TestServeLOSRefusalIsAResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want ServeLOS
	}{
		{"no access hides the map", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=no-access #T",
			ServeLOS{A: "Tanka", B: "Healera", Reason: "no-access"}},
		{"rate states nothing", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=rate #T",
			ServeLOS{A: "Tanka", B: "Healera", Reason: "rate"}},
		{"different maps give no distance", "los a=Tanka b=Healera map=530 los=0 dist= same_map=0 in_world=1 reason=different-maps #T",
			ServeLOS{A: "Tanka", B: "Healera", Map: 530, InWorld: true, Reason: "different-maps"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.serveMode = true
			f.serveCommander = func(line string) []string {
				if !strings.HasPrefix(line, ".tb los ") {
					return nil
				}
				return []string{tc.line}
			}
			defer f.close()
			steps := &stepLog{}
			a := startServeDaemon(t, f, steps)
			waitServeState(t, a, "online", steps)

			var out ServeLOS
			json.Unmarshal(a.mustCall(t, "los", map[string]any{"a": "Tanka", "b": "Healera"}), &out)
			if out.Reason != tc.want.Reason || out.A != tc.want.A || out.B != tc.want.B || out.Map != tc.want.Map {
				t.Fatalf("answer %+v, want %+v", out, tc.want)
			}
			if tc.want.Reason == "no-access" && out.DistYD != nil {
				t.Fatalf("a no-access answer states a distance: %v", *out.DistYD)
			}
			if tc.want.Reason == "different-maps" && out.DistYD != nil {
				t.Fatalf("a different-maps answer states a distance: %v", *out.DistYD)
			}
			a.stop(t)
			f.wait(t)
		})
	}
}

// TestServeLOSUnsupported: a server whose module build has no los command answers a clear error naming the module,
// never a parse error or a timeout, and the scout's fallback reads it as "not supported" rather than "no sight".
func TestServeLOSUnsupported(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	// The core refuses the word before any module code runs: `.tb` has no los subcommand on this build.
	f.serveCommander = func(string) []string { return []string{"Subcommand 'los' does not exist."} }
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "los", map[string]any{"a": "Tanka", "b": "Healera"}, -32003, "los: not supported by this module build")
	a.stop(t)
	f.wait(t)
}

// TestServeLOSMalformedLine: an answer that is not the module's record is refused, never read as a zero sight answer.
func TestServeLOSMalformedLine(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"a missing key", "los a=Tanka b=Healera map=0 los=1 same_map=1 in_world=1 #T"},
		{"the retry hint the module never writes", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=rate retry_ms=250 #T"},
		{"another pair", "los a=Other b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.serveMode = true
			f.serveCommander = func(line string) []string {
				if !strings.HasPrefix(line, ".tb los ") {
					return nil
				}
				return []string{tc.line}
			}
			defer f.close()
			steps := &stepLog{}
			a := startServeDaemon(t, f, steps)
			waitServeState(t, a, "online", steps)

			a.mustFail(t, "los", map[string]any{"a": "Tanka", "b": "Healera"}, -32003, "not a los record")
			a.stop(t)
			f.wait(t)
		})
	}
}

// TestServeLOSParams: two spots need their map, and a side that is not a name, target, guid or spot is refused before
// any line goes out.
func TestServeLOSParams(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(string) []string { return nil }
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "los", map[string]any{"a": "1,2,3", "b": "4,5,6"}, -32602, "two spots need map")
	a.mustFail(t, "los", map[string]any{"a": "Tanka", "b": "Healera", "map": 700}, -32602, "two spots")
	a.mustFail(t, "los", map[string]any{"a": "1,2,3", "b": "4,5,6", "map": 0}, -32602, "1-65535")
	a.mustFail(t, "los", map[string]any{"a": "", "b": "Tanka"}, -32602, "empty")
	if lines := f.serveTBLines(); len(lines) != 0 {
		t.Fatalf("a refused call sent %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

// The los method below GM (part of #88): a session whose account is not a GM may still run the one read-only sight
// line the player run sends, because the module answers that shape for a player account; every other line of the
// method keeps the guard, and a module that says the line aloud is refused as the module's own answer.

// sightGuid is a creature guid as the daemon prints one.
const sightGuid = "0000000000000000000000000000BEEF"

// TestServeLOSNonGMSightShapeSkipsProbe: the player run's shape (a spot and a guid) goes out without the GM probe and
// the module's answer is the call's result.
func TestServeLOSNonGMSightShapeSkipsProbe(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveNoGM = true
	f.serveCommander = func(line string) []string {
		if !strings.HasPrefix(line, ".tb los ") {
			return nil
		}
		return []string{"los a=-7.00,0.00,0.00 b=ForeverRat map=700 los=1 dist=3.0 same_map=1 in_world=1 #T"}
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeLOS
	json.Unmarshal(a.mustCall(t, "los", map[string]any{"a": "-7,0,0", "b": sightGuid}), &out)
	if !out.Clear || out.Reason != "" || out.B != "ForeverRat" || out.DistYD == nil || *out.DistYD != 3.0 {
		t.Fatalf("answer %+v", out)
	}
	if lines := f.serveTBLines(); len(lines) != 1 || !strings.HasPrefix(lines[0], ".tb los -7,0,0 "+strings.ToLower(sightGuid)) {
		t.Fatalf("the fake saw %q; want the one sight line", lines)
	}
	if f.serveGMProbed() {
		t.Fatal("the non-GM sight call sent the GM probe")
	}
	a.stop(t)
	f.wait(t)
}

// TestServeLOSNonGMOtherShapeStillProbes: a shape outside the player run's sight line ("target" is not a side the
// player line takes) keeps the guard, and the account's missing permission is the call's error.
func TestServeLOSNonGMOtherShapeStillProbes(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveNoGM = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "los", map[string]any{"a": "target", "b": sightGuid}, -32003, "not a GM")
	if !f.serveGMProbed() {
		t.Fatal("the call did not probe the GM permission")
	}
	if lines := f.serveTBLines(); len(lines) != 0 {
		t.Fatalf("a guarded call sent %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeLOSNonGMSaidAloud: a module that says the sight line aloud gives the account no sight query; the call is
// refused with the module's own answer and the session's guard is not poisoned (a `.bot` line still goes out).
func TestServeLOSNonGMSaidAloud(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveNoGM = true
	f.serveEchoTB = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "los", map[string]any{"a": "-7,0,0", "b": sightGuid}, -32003, "said aloud")
	// The guard is not poisoned: a `.bot` owner line still goes out.
	_, rerr := a.call(t, "bot", map[string]any{"line": ".bot who"})
	f.mu.Lock()
	said := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if rerr != nil || !slices.Contains(said, ".bot who") {
		t.Fatalf("the bot line did not go out after the sight echo: %q (%+v)", said, rerr)
	}
	if n := len(f.serveTBLines()); n != 1 {
		t.Fatalf("%d sight line(s) reached the server, want the one the call sent", n)
	}
	if f.serveGMProbed() {
		t.Fatal("the said-aloud path sent the GM probe")
	}
	a.stop(t)
	f.wait(t)
}

// TestServeLOSResetsSaidAloudForLaterCall: a said-aloud sight answer refuses only that call; nothing caches it across
// serve los calls, so once the account holds GM (or another call fails for another reason) later calls answer
// normally instead of failing "said aloud" forever.
func TestServeLOSResetsSaidAloudForLaterCall(t *testing.T) {
	t.Run("after GM confirm", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		f.serveNoGM = true
		f.serveEchoTB = true
		defer f.close()
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		waitServeState(t, a, "online", steps)

		a.mustFail(t, "los", map[string]any{"a": "-7,0,0", "b": sightGuid}, -32003, "said aloud")

		f.mu.Lock()
		f.serveNoGM = false
		f.serveEchoTB = false
		f.serveCommander = func(line string) []string {
			if !strings.HasPrefix(line, ".tb los ") {
				return nil
			}
			return []string{"los a=target b=ForeverRat map=700 los=1 dist=4.5 same_map=1 in_world=1 #T"}
		}
		f.mu.Unlock()

		var out ServeLOS
		json.Unmarshal(a.mustCall(t, "los", map[string]any{"a": "target", "b": sightGuid}), &out)
		if !out.Clear || out.Reason != "" || out.B != "ForeverRat" || out.DistYD == nil || *out.DistYD != 4.5 {
			t.Fatalf("answer after GM confirm = %+v", out)
		}
		a.stop(t)
		f.wait(t)
	})

	t.Run("later failure keeps its own error", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		f.serveNoGM = true
		f.serveEchoTB = true
		defer f.close()
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		waitServeState(t, a, "online", steps)

		a.mustFail(t, "los", map[string]any{"a": "-7,0,0", "b": sightGuid}, -32003, "said aloud")
		a.mustFail(t, "los", map[string]any{"a": "target", "b": sightGuid}, -32003, "not a GM")

		a.stop(t)
		f.wait(t)
	})
}

// TestServeBotTBLineStillGuarded: the `bot` method's `.tb` line is not the player run's sight shape, so an unconfirmed
// account is refused and nothing is sent.
func TestServeBotTBLineStillGuarded(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveNoGM = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "bot", map[string]any{"line": ".tb state"}, -32003, "not a GM")
	if !f.serveGMProbed() {
		t.Fatal("the `.tb` bot line did not probe the GM permission")
	}
	if lines := f.serveTBLines(); len(lines) != 0 {
		t.Fatalf("the guarded line was sent: %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

// serveGMProbed reports whether the fake saw the `.gm` probe.
func (f *fakeServer) serveGMProbed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serveGMSent
}
