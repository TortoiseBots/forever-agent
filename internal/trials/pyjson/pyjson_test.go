package pyjson

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"testing"
)

// The expectations below were printed by Python 3.12 (repr, round, %g, %f) for the same
// inputs; they are the contract the report cards rely on.
func TestFormatFloatMatchesPythonRepr(t *testing.T) {
	// A runtime sum: Python sees 0.30000000000000004, Go constants would be exact 0.3.
	sum01 := 0.1
	sum01 += 0.2
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0.0"},
		{math.Copysign(0, -1), "-0.0"},
		{1, "1.0"},
		{-1, "-1.0"},
		{100, "100.0"},
		{1.5, "1.5"},
		{0.1, "0.1"},
		{sum01, "0.30000000000000004"},
		{1e15, "1000000000000000.0"},
		{1e16, "1e+16"},
		{1.2345678901234567e16, "1.2345678901234568e+16"},
		{1e-4, "0.0001"},
		{1e-5, "1e-05"},
		{2.675, "2.675"},
		{1234567.0, "1234567.0"},
		{12345.678, "12345.678"},
		{1.0 / 3, "0.3333333333333333"},
		{2.0 / 3, "0.6666666666666666"},
		{math.Pi, "3.141592653589793"},
		{1e300, "1e+300"},
		{1e-300, "1e-300"},
		{5e-324, "5e-324"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
		{99.99, "99.99"},
		{0.000123456789, "0.000123456789"},
		{123456780.0, "123456780.0"},
		{1e21, "1e+21"},
		{999999.9999, "999999.9999"},
	}
	for _, c := range cases {
		if got := FormatFloat(c.in); got != c.want {
			t.Errorf("FormatFloat(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRoundMatchesPython(t *testing.T) {
	sum01 := 0.1
	sum01 += 0.2
	cases := []struct {
		in   float64
		nd   int
		want float64
	}{
		{2.675, 2, 2.67},
		{0.125, 2, 0.12},
		{0.375, 2, 0.38},
		{1.005, 2, 1.0},
		{2.5, 0, 2.0},
		{-2.5, 0, -2.0},
		{3.5, 0, 4.0},
		{-3.5, 0, -4.0},
		{1.235, 2, 1.24},
		{4.35, 2, 4.35},
		{12345.678, 2, 12345.68},
		{12345.678, 1, 12345.7},
		{99.99, 1, 100.0},
		{999999.9999, 2, 1000000.0},
		{sum01, 2, 0.3},
		{1e-5, 2, 0.0},
		{1e300, 2, 1e300},
	}
	for _, c := range cases {
		got := Round(c.in, c.nd)
		if got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v", c.in, c.nd, got, c.want)
		}
	}
}

func TestFormatGAndF(t *testing.T) {
	g := []struct {
		prec int
		in   float64
		want string
	}{
		{3, 0, "0"},
		{3, 1, "1"},
		{3, 100, "100"},
		{3, 1e15, "1e+15"},
		{3, 1e16, "1e+16"},
		{3, 1.2345678901234568e16, "1.23e+16"},
		{3, 0.0001, "0.0001"},
		{3, 1e-5, "1e-05"},
		{3, 2.675, "2.67"},
		{3, 1.0 / 3, "0.333"},
		{3, 1e300, "1e+300"},
		{3, 99.99, "100"},
		{3, 0.000123456789, "0.000123"},
	}
	for _, c := range g {
		if got := FormatG(c.prec, c.in); got != c.want {
			t.Errorf("FormatG(%d, %v) = %q, want %q", c.prec, c.in, got, c.want)
		}
	}
	f := []struct {
		dec  int
		in   float64
		want string
	}{
		{1, 0, "0.0"},
		{1, 0.125, "0.1"},
		{1, 1.005, "1.0"},
		{1, 12345.678, "12345.7"},
		{0, 2.5, "2"},
		{0, 3.5, "4"},
		{0, -2.5, "-2"},
		{1, 100, "100.0"},
	}
	for _, c := range f {
		if got := FormatF(c.dec, c.in); got != c.want {
			t.Errorf("FormatF(%d, %v) = %q, want %q", c.dec, c.in, got, c.want)
		}
	}
}

func TestMarshalIndentSortedAndEscaped(t *testing.T) {
	doc := map[string]any{
		"b": []any{int64(1), 2.5, nil, true, "x"},
		"a": map[string]any{"z": "é", "y": []any{}},
	}
	got, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": {\n    \"y\": [],\n    \"z\": \"\\u00e9\"\n  },\n  \"b\": [\n    1,\n    2.5,\n    null,\n    true,\n    \"x\"\n  ]\n}"
	if string(got) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", got, want)
	}
	line, err := MarshalLine(map[string]any{"k": 1.0, "j": "a b"})
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != `{"j": "a b", "k": 1.0}` {
		t.Errorf("MarshalLine = %s", line)
	}
	if _, err := Marshal(map[string]any{"x": math.Inf(1)}); err == nil {
		t.Error("infinity must fail with allow_nan=False")
	}
	var buf bytes.Buffer
	if err := DumpLine(&buf, map[string]any{"t": 1.5}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\"t\": 1.5}\n" {
		t.Errorf("DumpLine = %q", buf.String())
	}
}

func TestUnmarshalKeepsIntAndFloat(t *testing.T) {
	doc, err := Unmarshal([]byte(`{"i": 3, "f": 3.0, "e": 1e3, "n": null, "o": {"x": [1, 2.5]}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := ObjectOrEmpty(doc)
	if _, ok := m["i"].(int64); !ok {
		t.Errorf("i = %T, want int64", m["i"])
	}
	if _, ok := m["f"].(float64); !ok {
		t.Errorf("f = %T, want float64", m["f"])
	}
	if f, _ := m["e"].(float64); f != 1000.0 {
		t.Errorf("e = %v", m["e"])
	}
	if _, ok := m["o"].(map[string]any)["x"].([]any)[0].(int64); !ok {
		t.Error("array integer lost its int type")
	}
}

// Python 3.12: json.loads keeps integers at arbitrary precision, json.loads("1e999") is inf,
// and json.loads rejects anything after the first value ("1 2" raises JSONDecodeError).
func TestUnmarshalKeepsArbitraryPrecisionIntegers(t *testing.T) {
	const digits = "123456789012345678901234567890"
	doc, err := Unmarshal([]byte(`{"big": ` + digits + `, "small": 5}`))
	if err != nil {
		t.Fatal(err)
	}
	m := ObjectOrEmpty(doc)
	if _, ok := m["big"].(*big.Int); !ok {
		t.Fatalf("big = %T, want *big.Int", m["big"])
	}
	if got := Str(m["big"]); got != digits {
		t.Errorf("Str(big) = %q, want %q", got, digits)
	}
	encoded, err := MarshalLine(map[string]any{"big": m["big"]})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"big": ` + digits + `}`; string(encoded) != want {
		t.Errorf("MarshalLine = %s, want %s", encoded, want)
	}
	if _, ok := m["small"].(int64); !ok {
		t.Errorf("small = %T, want int64", m["small"])
	}
}

func TestUnmarshalRejectsTrailingDataAndKeepsInf(t *testing.T) {
	for _, input := range []string{`1 2`, `{} x`, `null null`} {
		if _, err := Unmarshal([]byte(input)); err == nil {
			t.Errorf("Unmarshal(%q) accepted trailing data", input)
		}
	}
	for _, input := range []string{"1\n", "  {}\t", `[1, 2] `} {
		if _, err := Unmarshal([]byte(input)); err != nil {
			t.Errorf("Unmarshal(%q) = %v", input, err)
		}
	}
	doc, err := Unmarshal([]byte(`1e999`))
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := doc.(float64); !ok || !math.IsInf(f, 1) {
		t.Fatalf("1e999 = %T %v, want +inf", doc, doc)
	}
}

// Python 3.12: int(5.0) == 5, int(" 5 ") == 5, int("5.0") raises ValueError, int(True) == 1.
func TestPyIntMatchesPython(t *testing.T) {
	cases := []struct {
		in   any
		want any
		ok   bool
	}{
		{int64(5), int64(5), true},
		{5.0, int64(5), true},
		{5e18, int64(5000000000000000000), true},
		{-5.9, int64(-5), true},
		{true, int64(1), true},
		{" 5 ", int64(5), true},
		{"1_000", int64(1000), true},
		{"5.0", nil, false},
		{"0x10", nil, false},
		{[]any{int64(1)}, nil, false},
		{nil, nil, false},
	}
	for _, c := range cases {
		got, err := PyInt(c.in)
		if (err == nil) != c.ok {
			t.Errorf("PyInt(%#v) err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("PyInt(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
	gotBig, err := PyInt("123456789012345678901234567890")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBig.(*big.Int); !ok {
		t.Fatalf("PyInt(big string) = %T, want *big.Int", gotBig)
	}
	if _, err := PyInt(math.Inf(1)); !isPyType(err, "OverflowError") {
		t.Errorf("PyInt(+Inf) = %v, want OverflowError", err)
	}
	if _, err := PyInt(math.NaN()); !isPyType(err, "ValueError") {
		t.Errorf("PyInt(NaN) = %v, want ValueError", err)
	}
	if _, err := PyInt([]any{}); !isPyType(err, "TypeError") {
		t.Errorf("PyInt(list) = %v, want TypeError", err)
	}
}

func isPyType(err error, want string) bool {
	var conv *ConversionError
	return errors.As(err, &conv) && conv.PyType == want
}

// Python 3.12: json.loads accepts NaN, Infinity and -Infinity wherever a value can start;
// json.dumps(..., allow_nan=False) then refuses to write one back.
func TestUnmarshalNonFiniteTokens(t *testing.T) {
	doc, err := Unmarshal([]byte(`{"a": NaN, "b": [Infinity, -Infinity], "s": "NaN"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := ObjectOrEmpty(doc)
	if f, ok := m["a"].(float64); !ok || !math.IsNaN(f) {
		t.Errorf("a = %T %v, want NaN", m["a"], m["a"])
	}
	list := m["b"].([]any)
	if f, ok := list[0].(float64); !ok || !math.IsInf(f, 1) {
		t.Errorf("b[0] = %T %v, want +Inf", list[0], list[0])
	}
	if f, ok := list[1].(float64); !ok || !math.IsInf(f, -1) {
		t.Errorf("b[1] = %T %v, want -Inf", list[1], list[1])
	}
	if m["s"] != "NaN" {
		t.Errorf("string NaN changed to %#v", m["s"])
	}
	for _, input := range []string{`[NaNx]`, `[nan]`, `[+Infinity]`, `[Infin]`, `[Infinityx]`,
		`{NaN: 1}`, `{"a": 1, NaN: 2}`} {
		if _, err := Unmarshal([]byte(input)); err == nil {
			t.Errorf("Unmarshal(%q) accepted a non-token", input)
		}
	}
	if _, err := Marshal(m); err == nil {
		t.Error("Marshal wrote a NaN; allow_nan=False must refuse it")
	}
}
