package scenario

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
)

// TestForeverDrainUsesEventLog is the failing case the round names: a fake
// that answers only event_log. A runner that drains through `events` gets
// -32601 and fails; the Forever path must use event_log.
func TestForeverDrainUsesEventLog(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774","server":"forever"}},"steps":[
		{"do":"expect","session":"A","kind":"chat","match":"hello","timeout_s":5}
	]}`)
	a := &fakeForeverCaller{char: "A"}
	a.script = func(call int) {
		if call == 1 {
			a.add(foreverChatEvent("hello"))
		}
	}
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": bindForeverFake(a, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passed != 1 || res.Exit != 0 {
		t.Fatalf("%+v", res)
	}
	if n := a.callCount("events"); n != 0 {
		t.Fatalf("Forever drain called events %d times", n)
	}
	if n := a.callCount("event_log"); n == 0 {
		t.Fatal("Forever drain never called event_log")
	}
}

// TestForeverEventsFails pins the other half: a serve slot that never learned
// event_log answers -32601 to it, so a drain through `events` is an ERROR.
func TestForeverEventsFails(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[
		{"do":"mark"}
	]}`)
	a := &fakeCaller{}
	a.eventsErr = &RPCError{Code: -32601, Message: "method not found"}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("%+v", res)
	}
}

// TestForeverOnlineIsInWorld pins the start probe: a serve status with state
// "online" counts as in the world; anything else is an ERROR. The turtle
// default (no server key) still passes with no state word.
func TestForeverOnlineIsInWorld(t *testing.T) {
	mkfile := func(server string) *File {
		sess := `"A":{"rpc":"127.0.0.1:8774"}`
		if server != "" {
			sess = `"A":{"rpc":"127.0.0.1:8774","server":` + server + `}`
		}
		return parseFile(t, `{"scenario":"t","sessions":{`+sess+`},"steps":[{"do":"mark"}]}`)
	}
	// online: in world.
	a := &fakeForeverCaller{char: "Atank", status: map[string]any{"character": "Atank", "state": "online", "guid": "0x00000000000000010000000000000001"}}
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": bindForeverFake(a, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	if res, err := r.Run(context.Background(), mkfile(`"forever"`)); err != nil || res.Errors != 0 {
		t.Fatalf("online: %+v %v", res, err)
	}
	// reconnecting: not in world.
	b := &fakeForeverCaller{char: "Atank", status: map[string]any{"character": "Atank", "state": "reconnecting"}}
	clk = newFakeClock()
	r = &Runner{Sessions: map[string]Caller{"A": bindForeverFake(b, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	if res, err := r.Run(context.Background(), mkfile(`"forever"`)); err != nil || res.Errors != 1 {
		t.Fatalf("reconnecting should ERROR: %+v %v", res, err)
	}
	// turtle default unchanged: passes without a state word.
	c := &fakeCaller{char: "Atank"}
	clk = newFakeClock()
	if res, _ := runWith(t, mkfile(``), map[string]*fakeCaller{"A": c}, nil, clk); res.Errors != 0 {
		t.Fatalf("turtle in_world broke: %+v", res)
	}
}

// TestForeverItemCountSQL pins the Forever item-count SELECT: itemEntry and
// owner_guid at core 25fc7ca, names from the file's db block.
func TestForeverItemCountSQL(t *testing.T) {
	q := DBQuery{Item: &ItemQuery{Chars: []string{"Zed", "Atank"}, Entry: 1234}, Op: "==", Want: 1, Forever: true}
	got, err := BuildSQL(q)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT COALESCE(SUM(ii.count), 0) FROM characters.item_instance ii JOIN characters.characters c ON c.guid = ii.owner_guid WHERE c.name IN ('Atank', 'Zed') AND ii.itemEntry = 1234"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// TestFileServerKey pins the file-level "server" key: sessions without
// their own key inherit it, and a bad file value is refused at load.
func TestFileServerKey(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[{"do":"mark"}]}`)
	if f.Server != ServerForever || f.Sessions["A"].Server != ServerForever {
		t.Fatalf("file server did not inherit: %+v", f)
	}
	if _, err := Parse([]byte(`{"scenario":"t","server":"nope","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[{"do":"mark"}]}`), nil); err == nil {
		t.Fatal("bad file server accepted")
	}
}

// TestTurtleFileUnchanged pins the default: no "server" key means turtle,
// with the tw_ table names the old SQL renders.
func TestTurtleFileUnchanged(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[{"do":"mark"}]}`)
	if f.Sessions["A"].Server != ServerTurtle {
		t.Fatalf("default server %q, want turtle", f.Sessions["A"].Server)
	}
	q := DBQuery{Item: &ItemQuery{Char: "Atank", Entry: 9240}, Op: "==", Want: 1}
	got, err := BuildSQL(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "SELECT COALESCE(SUM(ii.count), 0) FROM tw_char.item_instance ii JOIN tw_char.characters c") {
		t.Fatalf("turtle SQL changed: %q", got)
	}
}

// TestForeverDoStepsCallServeMethods pins do-step routing on a Forever slot:
// gm/addon_send call the serve method of the same name; bot calls serve bot;
// rpc names the serve method.
func TestForeverDoStepsCallServeMethods(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774","server":"forever"}},"steps":[
		{"do":"gm","session":"A","text":".go xyz 1 2 3"},
		{"do":"bot","session":"A","command":"state Bheal"},
		{"do":"rpc","session":"A","method":"party"}
	]}`)
	a := &fakeForeverCaller{char: "Atank"}
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": bindForeverFake(a, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("%+v", res)
	}
	if n := a.callCount("gm"); n != 1 {
		t.Fatalf("gm calls: %d, want 1", n)
	}
	if n := a.callCount("bot"); n != 1 {
		t.Fatalf("bot calls: %d, want 1", n)
	}
	if n := a.callCount("party"); n != 1 {
		t.Fatalf("party calls: %d, want 1", n)
	}
	var gmParams struct {
		Text string `json:"text"`
	}
	calls := a.called("gm")
	if err := json.Unmarshal(calls[0].Params, &gmParams); err != nil || gmParams.Text != ".go xyz 1 2 3" {
		t.Fatalf("gm params %s %v", calls[0].Params, err)
	}
	var botParams struct {
		Line string `json:"line"`
	}
	bcalls := a.called("bot")
	if err := json.Unmarshal(bcalls[0].Params, &botParams); err != nil || botParams.Line != ".tb state Bheal" {
		t.Fatalf("bot params %s %v", bcalls[0].Params, err)
	}
	// The transcript prints the line that went out, once (before the fix it was the hardcoded `.bot state
	// Bheal`, which this module never answers).
	wantRaw := "  gm[A] .tb state Bheal"
	found := false
	for _, rec := range res.Records {
		if rec.T == "action" && rec.Step == 2 && rec.Raw == wantRaw && rec.Norm == wantRaw {
			found = true
		}
	}
	if !found {
		t.Fatalf("the bot step's raw line is not %q: %+v", wantRaw, res.Records)
	}
}

// TestForeverNotPortedIsError pins that a do-step naming a method the serve
// side does not port (commander today) is an ERROR naming the method, never a FAIL.
func TestForeverNotPortedIsError(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774","server":"forever"}},"steps":[
		{"do":"check_rpc","session":"A","method":"commander","params":{"op":"status"},"path":"reply","match":".*","name":"cmd"}
	]}`)
	a := &fakeForeverCaller{char: "Atank", notPorted: map[string]bool{"commander": true}}
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": bindForeverFake(a, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Errors != 1 || res.Failed != 0 || res.Exit != 15 {
		t.Fatalf("not-ported must ERROR, not FAIL: %+v", res)
	}
	found := false
	for _, c := range res.Checks {
		if strings.Contains(c.Desc, "commander") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ERROR does not name the method: %+v", res.Checks)
	}
}

// TestForeverCheckDBAllowlist pins the Forever table set: the three
// characters-database tables pass, tw_ ones are refused on a Forever slot.
func TestForeverCheckDBAllowlist(t *testing.T) {
	for _, table := range []string{"characters.characters", "characters.item_instance", "characters.character_inventory"} {
		q := DBQuery{Table: table, Op: "==", Want: 1, Forever: true}
		if _, err := BuildSQL(q); err != nil {
			t.Fatalf("table %s refused: %v", table, err)
		}
	}
	q := DBQuery{Table: "tw_char.characters", Op: "==", Want: 1, Forever: true}
	if _, err := BuildSQL(q); err == nil {
		t.Fatal("tw_ table allowed on a Forever slot")
	}
	if _, err := parseFileRaw(`{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"db":{"host":"h","container":"c"},"steps":[{"do":"check_db","server":"forever","table":"tw_char.characters","rows":1}]}`); err == nil {
		t.Fatal("tw_ table accepted in a forever check_db step")
	}
	if _, err := parseFileRaw(`{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"db":{"host":"h","container":"c"},"steps":[{"do":"check_db","server":"forever","table":"characters.item_instance","rows":1}]}`); err != nil {
		t.Fatalf("Forever table refused: %v", err)
	}
}

// foreverChatEvent builds a chat event the fake serves through event_log.
func foreverChatEvent(text string) events.Event {
	e := events.Event{Kind: events.KindChat, Text: text, Source: "Atank"}
	events.Stamp(&e)
	return e
}

func parseFileRaw(data string) (*File, error) {
	return Parse([]byte(data), nil)
}
