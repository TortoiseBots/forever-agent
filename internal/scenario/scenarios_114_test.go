package scenario

// Round #114: the two shipped #114 scenarios must load and carry the steps the ticket's live checks need. The
// module side (the hint line, the deDE bot text) is a separate ticket, so the checks pin the shape the operator
// runs: one hint then silence, and a deDE status with a German bot reply.

import (
	"testing"
)

func TestScenarioAddonHintAndLocaleCheck(t *testing.T) {
	hint, err := Load("../../scenarios/forever-addon-hint.json", nil)
	if err != nil {
		t.Fatalf("forever-addon-hint.json: %v", err)
	}
	if hint.Vars["HINT_RE"] == "" || hint.Vars["WAIT_S"] != "90" {
		t.Fatalf("hint vars %v", hint.Vars)
	}
	if hint.Sessions["A"].Server != ServerForever {
		t.Fatalf("hint session A server %q", hint.Sessions["A"].Server)
	}
	if len(hint.Steps) != 4 {
		t.Fatalf("hint has %d steps, want 4", len(hint.Steps))
	}
	first, second := hint.Steps[1], hint.Steps[3]
	if first.Do != "expect" || first.Session != "A" || first.Match != "${HINT_RE}" || first.Kind != "" || first.TimeoutS != 90 {
		t.Fatalf("hint's first check is %+v", first)
	}
	if second.Do != "expect_not" || second.Session != "A" || second.Match != "${HINT_RE}" || second.WithinS != 120 {
		t.Fatalf("hint's second check is %+v", second)
	}

	locale, err := Load("../../scenarios/forever-locale-texts.json", nil)
	if err != nil {
		t.Fatalf("forever-locale-texts.json: %v", err)
	}
	for _, name := range []string{"BOT", "BOT_CMD", "DE_RE"} {
		if locale.Vars[name] == "" {
			t.Fatalf("locale vars %v lack %s", locale.Vars, name)
		}
	}
	if locale.Sessions["A"].Server != ServerForever {
		t.Fatalf("locale session A server %q", locale.Sessions["A"].Server)
	}
	var checked, greeted, heard bool
	for _, s := range locale.Steps {
		switch {
		case s.Do == "check_rpc" && s.Method == "status" && s.Path == "locale":
			checked = true
			if !s.HasEq || s.Equals != "deDE" {
				t.Fatalf("status locale check is %+v, want equals deDE", s)
			}
		case s.Do == "bot" && s.Session == "A" && s.Command == "${BOT_CMD}":
			greeted = true
		case s.Do == "expect" && s.Session == "A" && s.Match == "${DE_RE}" && s.TimeoutS == 30:
			heard = true
		}
	}
	if !checked || !greeted || !heard {
		t.Fatalf("locale steps: status check %v, bot step %v, deDE expect %v", checked, greeted, heard)
	}
}
