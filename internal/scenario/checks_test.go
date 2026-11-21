package scenario

import (
	"strings"
	"testing"
)

// TestCheckDBInfersForeverFromSessions: a check_db step cannot name a session, so a file whose sessions are all
// Forever infers the Forever slot (schema and allowlist), a mixed file must say "server" on the step, and a
// Forever table is validated against the Forever allowlist, not the turtle one.
func TestCheckDBInfersForeverFromSessions(t *testing.T) {
	const db = `"db":{"host":"test1","container":"tortoise-db","schema":"mychars"}`
	foreverSessions := `"sessions":{"A":{"rpc":"127.0.0.1:1","server":"forever"},"B":{"rpc":"127.0.0.1:2","server":"forever"}}`
	f, err := Parse([]byte(`{"scenario":"t",`+foreverSessions+`,`+db+`,
		"steps":[{"do":"check_db","table":"mychars.characters","rows":1}]}`), nil)
	if err != nil {
		t.Fatalf("an all-Forever file's check_db was refused: %v", err)
	}
	q, err := (&run{f: f}).checkDBQuery(f.Steps[0])
	if err != nil {
		t.Fatalf("checkDBQuery: %v", err)
	}
	if !q.Forever || q.Schema != "mychars" {
		t.Fatalf("query %+v, want Forever with the file's schema", q)
	}
	if _, err := Parse([]byte(`{"scenario":"t",`+foreverSessions+`,`+db+`,
		"steps":[{"do":"check_db","table":"tw_char.characters","rows":1}]}`), nil); err == nil ||
		!strings.Contains(err.Error(), "Forever allowlist") {
		t.Fatalf("a turtle table was not checked against the Forever allowlist: %v", err)
	}
	mixed := `"sessions":{"A":{"rpc":"127.0.0.1:1","server":"forever"},"B":{"rpc":"127.0.0.1:2"}}`
	if _, err := Parse([]byte(`{"scenario":"t",`+mixed+`,`+db+`,
		"steps":[{"do":"check_db","item":{"char":"Atank","entry":1},"count":0}]}`), nil); err == nil ||
		!strings.Contains(err.Error(), "mixes") {
		t.Fatalf("a mixed-server file's check_db was not refused: %v", err)
	}
	if _, err := Parse([]byte(`{"scenario":"t",`+mixed+`,`+db+`,
		"steps":[{"do":"check_db","server":"forever","item":{"char":"Atank","entry":1},"count":0}]}`), nil); err != nil {
		t.Fatalf("a mixed-server file with the step's own server was refused: %v", err)
	}
}

func TestCheckDBQueryExpandsCharVar(t *testing.T) {
	f, err := Parse([]byte(`{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"db":{"host":"test1","container":"tortoise-db"},
		"steps":[
			{"do":"check_db","item":{"char":"${A.character}","entry":9240},"count":0},
			{"do":"check_db","item":{"chars":["${A.character}"],"entry":9240},"count":0}
		]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := &run{vars: map[string]string{"A.character": "Tacmdaron"}}
	q, err := rr.checkDBQuery(f.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if q.Item == nil || q.Item.Char != "Tacmdaron" {
		t.Fatalf("%+v", q.Item)
	}
	q2, err := rr.checkDBQuery(f.Steps[1])
	if err != nil {
		t.Fatal(err)
	}
	if q2.Item == nil || len(q2.Item.Chars) != 1 || q2.Item.Chars[0] != "Tacmdaron" {
		t.Fatalf("%+v", q2.Item)
	}
	bad := &run{vars: map[string]string{"A.character": "Bob'--"}}
	if _, err := bad.checkDBQuery(f.Steps[0]); err == nil || !strings.Contains(err.Error(), "bad char name") {
		t.Fatalf("char: got %v", err)
	}
	if _, err := bad.checkDBQuery(f.Steps[1]); err == nil || !strings.Contains(err.Error(), "bad char name") {
		t.Fatalf("chars: got %v", err)
	}
}
