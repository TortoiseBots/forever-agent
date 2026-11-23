package scenario

// The two summoning/ritual scenario files (#110): both must load, and each must keep the steps that make its
// subject testable (the summon offer answered by the target, the altar used and expected to change).

import "testing"

func TestScenarioRitualFilesCheck(t *testing.T) {
	ritual, err := Load("../../scenarios/forever-ritual-summon.json", nil)
	if err != nil {
		t.Fatalf("forever-ritual-summon.json: %v", err)
	}
	altar, err := Load("../../scenarios/forever-uldaman-altar.json", nil)
	if err != nil {
		t.Fatalf("forever-uldaman-altar.json: %v", err)
	}

	// The ritual file: the target's summon_request is expected and answered, and its map is checked against MAP.
	var sawExpect, sawAccept, sawMapCheck bool
	for _, s := range ritual.Steps {
		switch {
		case s.Do == "expect" && s.Session == "B" && s.Kind == "summon_request":
			sawExpect = true
		case s.Do == "rpc" && s.Session == "B" && s.Method == "accept_summon":
			sawAccept = true
		case s.Do == "check_rpc" && s.Session == "B" && s.Method == "status" && s.Path == "map":
			sawMapCheck = true
		}
	}
	if !sawExpect || !sawAccept || !sawMapCheck {
		t.Fatalf("the ritual file lost a step: expect %v, accept %v, map check %v", sawExpect, sawAccept, sawMapCheck)
	}
	if ritual.Vars["MAP"] == "" || ritual.Vars["BOT1"] == "" || ritual.Vars["BOT2"] == "" {
		t.Fatalf("the ritual file's vars: %v", ritual.Vars)
	}

	// The altar file: the object is looked up by entry, saved, used and expected to change.
	var sawLookup, sawUse, sawChange bool
	for _, s := range altar.Steps {
		switch {
		case s.Do == "rpc" && s.Method == "gameobjects":
			sawLookup = true
		case s.Do == "rpc" && s.Method == "use_object":
			sawUse = true
		case s.Do == "expect" && s.Kind == "gameobject":
			sawChange = true
		}
	}
	if !sawLookup || !sawUse || !sawChange {
		t.Fatalf("the altar file lost a step: lookup %v, use %v, change %v", sawLookup, sawUse, sawChange)
	}
}

// TestRunnerCheckRPCMatchExpandsVars pins that a check_rpc match is a template: ${MAP} is the declared var (quoted),
// so a number read from a method can be compared with a var.
func TestRunnerCheckRPCMatchExpandsVars(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"vars":{"MAP":"1"},"steps":[
		{"do":"check_rpc","session":"A","method":"status","path":"map","match":"^${MAP}$"}
	]}`)
	a := &fakeCaller{}
	a.results = map[string]fakeResult{"status": {Result: map[string]any{"map": 1}}}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Passed != 1 || res.Failed != 0 || res.Exit != 0 {
		t.Fatalf("%+v", res)
	}
}
