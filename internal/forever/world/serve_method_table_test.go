package world

// #216: the method table has no "not ported" row left. Every old name is either implemented (its own method, tested
// by its own test) or refused with a documented reason: the refusal answers -32003 `<name>: <reason>`, `forever rpc
// list` prints the same reason, and docs/forever/METHOD-TABLE.md carries the same table (TestServeMethodTableDoc).

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
)

var updateMethodTable = flag.Bool("update-method-table", false, "rewrite docs/forever/METHOD-TABLE.md's generated block (the test still fails)")

// methodTableDoc is the doc whose generated block TestServeMethodTableDoc pins to the serve's table.
const methodTableDoc = "../../../docs/forever/METHOD-TABLE.md"

// The doc's generated block markers: everything between them is written from the serve's table.
const (
	methodTableBegin = "<!-- method-table:begin (generated: run go test ./internal/forever/world -run TestServeMethodTableDoc -update-method-table) -->"
	methodTableEnd   = "<!-- method-table:end -->"
)

// TestServeMethodTableRefusals: every refused name is registered, answers its documented reason and never "not
// ported", and the two lists are disjoint. Mutation: registering a name twice, dropping a reason, or keeping the old
// not-ported text fails here.
func TestServeMethodTableRefusals(t *testing.T) {
	ported, refused := ServeRPCMethods()
	if len(refused) != len(refusedMethods) {
		t.Fatalf("table: %d refused names registered, %d in the list", len(refused), len(refusedMethods))
	}
	seen := map[string]bool{}
	for _, name := range ported {
		if seen[name] {
			t.Fatalf("%q is in both lists", name)
		}
		seen[name] = true
	}
	for _, name := range refused {
		if seen[name] {
			t.Fatalf("%q is in both lists", name)
		}
		seen[name] = true
		why, ok := ServeRefusal(name)
		if !ok || why == "" {
			t.Fatalf("%q has no documented reason", name)
		}
		want := name + ": " + why
		if got := ServeRefusalError(name).Error(); got != want {
			t.Fatalf("%q: refusal %q, want %q", name, got, want)
		}
	}
	// The names the brief names one by one are on the right side of the table: the client brain and its tuning,
	// the client dungeon finder, the add-on's own buttons, the two step verbs and the Turtle toolkit are refused;
	// the add-on host, the run verbs, plan_check, bot_view and the three TBM methods are implemented.
	for _, name := range []string{
		"brain_start", "brain_status", "brain_stop", "tune_list", "tune_set",
		"lfg_auto", "lfg_join", "lfg_leave", "lfg_listing_role", "lfg_listings", "lfg_proposal_answer",
		"lfg_role_check_answer", "lfg_status",
		"addon_click",
		"tbm_buttons", "tbm_click", "dungeon_skip", "dungeon_stage", "tt",
	} {
		if _, ok := ServeRefusal(name); !ok {
			t.Fatalf("%q should be refused", name)
		}
	}
	for _, name := range []string{
		"addon_start", "addon_state", "addon_stop", "addon_tick",
		"dungeon_run", "dungeon_status", "dungeon_stop", "dungeon_pause", "dungeon_resume",
		"plan_check", "bot_view", "tbm", "tbm_handshake", "tbm_send",
	} {
		if _, ok := ServeRefusal(name); ok {
			t.Fatalf("%q should be implemented, not refused", name)
		}
		if !containsString(ported, name) {
			t.Fatalf("%q is not on the implemented list", name)
		}
	}
	// The reasons say what happened: no refusal is the old "not ported" text.
	for _, r := range refusedMethods {
		if strings.Contains(r.why, "not ported") {
			t.Fatalf("%q: reason %q still says not ported", r.name, r.why)
		}
	}
}

// TestServeRPCLines: `forever rpc list` prints one line per method, sorted by name: an implemented name bare, a
// refused one with its reason. Mutation: dropping the reason, or ordering the refused first, fails here.
func TestServeRPCLines(t *testing.T) {
	lines := ServeRPCLines()
	ported, refused := ServeRPCMethods()
	if len(lines) != len(ported)+len(refused) {
		t.Fatalf("%d lines for %d methods", len(lines), len(ported)+len(refused))
	}
	prev := ""
	impl := map[string]bool{}
	for _, n := range ported {
		impl[n] = true
	}
	ref := map[string]string{}
	for _, n := range refused {
		why, _ := ServeRefusal(n)
		ref[n] = why
	}
	for _, l := range lines {
		name := methodName(l)
		if name <= prev {
			t.Fatalf("lines are not sorted by name: %q after %q", l, prev)
		}
		prev = name
		if impl[name] {
			if l != name {
				t.Fatalf("implemented %q is marked: %q", name, l)
			}
			continue
		}
		why, ok := ref[name]
		if !ok {
			t.Fatalf("line %q is neither implemented nor refused", l)
		}
		if l != name+" (refused: "+why+")" {
			t.Fatalf("refused %q: line %q does not carry its reason", name, l)
		}
	}
}

// TestServeMethodTableDoc pins docs/forever/METHOD-TABLE.md's generated block to the serve's table: every method the
// socket answers, with a refused name's reason verbatim. A method added, renamed or refused without the doc fails
// here, so the doc cannot go stale. -update-method-table rewrites the block and still fails, so the diff is read.
func TestServeMethodTableDoc(t *testing.T) {
	want := methodTableBlock()
	raw, err := os.ReadFile(methodTableDoc)
	if err != nil {
		t.Fatalf("%s: %v", methodTableDoc, err)
	}
	doc := string(raw)
	start := strings.Index(doc, methodTableBegin)
	end := strings.Index(doc, methodTableEnd)
	if start < 0 || end < start {
		t.Fatalf("%s: the method-table block markers are missing (%q ... %q)", methodTableDoc, methodTableBegin, methodTableEnd)
	}
	start += len(methodTableBegin)
	got := doc[start:end]
	if strings.TrimSpace(got) == strings.TrimSpace(want) {
		return
	}
	if *updateMethodTable {
		next := doc[:start-len(methodTableBegin)] + methodTableBegin + want + "\n" + doc[end:]
		if err := os.WriteFile(methodTableDoc, []byte(next), 0o644); err != nil {
			t.Fatalf("%s: %v", methodTableDoc, err)
		}
		t.Fatalf("%s: rewritten from the serve's table: read the diff and commit it (the test fails on purpose)", methodTableDoc)
	}
	t.Fatalf("%s: the method table is stale (run with -update-method-table and read the diff)\n--- doc ---\n%s\n--- serve ---\n%s",
		methodTableDoc, strings.TrimSpace(got), strings.TrimSpace(want))
}

// methodTableBlock is the doc block the serve's table generates: a header, one row per method sorted by name, a
// refused row carrying its reason.
func methodTableBlock() string {
	var b strings.Builder
	b.WriteString("\n| Method | Status |\n|---|---|\n")
	for _, l := range ServeRPCLines() {
		name := methodName(l)
		if why, ok := ServeRefusal(name); ok {
			fmt.Fprintf(&b, "| `%s` | refused: %s |\n", name, why)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | implemented |\n", name)
	}
	return b.String()
}

// TestServeRefusalsOverTheSocket: every refused name answers its documented reason through the daemon's own
// socket, with the old caller's params left undecoded (a refusal is not an invalid-params error). Mutation: a name
// in the list that is not registered answers -32601 instead.
func TestServeRefusalsOverTheSocket(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	for _, r := range refusedMethods {
		a.mustFail(t, r.name, map[string]any{"op": "anything", "leg": 3}, -32003, r.name+": "+r.why)
	}
	a.mustFail(t, "no_such_method_xyz", nil, -32601, "Method not found")
	a.stop(t)
	f.wait(t)
}

func containsString(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}
