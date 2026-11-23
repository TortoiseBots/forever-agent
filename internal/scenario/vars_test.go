package scenario

import (
	"regexp"
	"testing"
)

// TestExpandParamsSendsNumericVarsAsNumbers pins the rpc/check_rpc params rule: a whole ${name} whose var is a
// number goes out as a number; mixed text, a string value and a missing var stay strings (missing is still an
// error).
func TestExpandParamsSendsNumericVarsAsNumbers(t *testing.T) {
	vars := map[string]string{"x": "-8893", "z": "3.4", "name": "Ann", "mixed": "7yd"}
	got, err := expandParams(map[string]any{
		"x":   "${x}",
		"z":   "${z}",
		"n":   "${name}",
		"m":   "from ${mixed}",
		"who": "${name}s",
	}, vars)
	if err != nil {
		t.Fatal(err)
	}
	pm := got.(map[string]any)
	if pm["x"] != float64(-8893) || pm["z"] != 3.4 {
		t.Fatalf("numbers %#v %#v", pm["x"], pm["z"])
	}
	if pm["n"] != "Ann" || pm["m"] != "from 7yd" || pm["who"] != "Anns" {
		t.Fatalf("strings %#v %#v %#v", pm["n"], pm["m"], pm["who"])
	}
	if _, err := expandParams(map[string]any{"x": "${gone}"}, vars); err == nil {
		t.Fatal("a missing var expanded")
	}
}

// TestExpandQuotesValuesInMatch pins that a var value is regex-quoted inside a
// match (a name with a dot must not match anything) and verbatim elsewhere,
// and that $$ is a literal dollar.
func TestExpandQuotesValuesInMatch(t *testing.T) {
	vars := map[string]string{"a": "a.b", "gid": "7"}
	got, err := expand("x${a}y", vars, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != `xa\.by` {
		t.Fatalf("got %q", got)
	}
	re := regexp.MustCompile(got)
	if re.MatchString("xay") || re.MatchString("xaxb") || !re.MatchString("xa.by") {
		t.Fatalf("quoted %q matches wrongly", got)
	}
	got, err = expand("give ${gid} $${a}", vars, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "give 7 ${a}" {
		t.Fatalf("got %q", got)
	}
	if _, err := expand("${missing}", vars, false); err == nil {
		t.Fatal("unset var expanded")
	}
}
