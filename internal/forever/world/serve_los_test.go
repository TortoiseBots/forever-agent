package world

// The los method (#259) against the fake module: one `.tb los` line through the session's command channel, the
// module's one-line answer read strictly, a refusal returned as the call's result with its code, and a module build
// without the command answering a clear "not supported" error rather than a parse error or a timeout.

import (
	"encoding/json"
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
