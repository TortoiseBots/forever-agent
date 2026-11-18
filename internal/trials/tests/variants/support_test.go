package variants

// Translated scoring tests from the Python bench's test_variants.py, test_extra_bots.py.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// writeJSON writes a document the way the Python bench does (indent=2, sorted keys).
func writeJSON(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pyjson.Dump(path, doc); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeJSONL writes JSON lines the way the bench writes events/samples/marks files.
func writeJSONL(t *testing.T, path string, rows []any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, row := range rows {
		if err := pyjson.DumpLine(&b, row); err != nil {
			t.Fatalf("encode row for %s: %v", path, err)
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readJSON reads a JSON document.
func readJSON(t *testing.T, path string) any {
	t.Helper()
	doc, err := pyjson.UnmarshalFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return doc
}

// readJSONL reads a JSON-lines file into objects.

// fixtureText returns a file of tests/fixtures-liveN (copied to internal/trials/testdata).

// fixtureJSON decodes a JSON fixture.

// fixtureRows decodes a JSON-lines fixture.

// obj asserts a decoded value is an object.
func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := pyjson.Object(v)
	if !ok {
		t.Fatalf("want an object, got %T", v)
	}
	return m
}

// list asserts a decoded value is a list.
func list(t *testing.T, v any) []any {
	t.Helper()
	l, ok := pyjson.List(v)
	if !ok {
		t.Fatalf("want a list, got %T", v)
	}
	return l
}

// num asserts a decoded value is a number.

// jsonEqual compares two decoded JSON values like Python's ==: numbers by value (1 == 1.0),
// lists element by element, objects by key set and value.
func jsonEqual(a, b any) bool {
	fa, aNum := pyjson.Float(a)
	fb, bNum := pyjson.Float(b)
	if aNum || bNum {
		return aNum && bNum && fa == fb
	}
	switch av := a.(type) {
	case nil:
		return b == nil
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok {
			if bList, okList := pyjson.List(b); okList {
				bv = bList
			} else {
				return false
			}
		}
		if len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			w, present := bv[k]
			if !present || !jsonEqual(v, w) {
				return false
			}
		}
		return true
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// assertEqual is Python's assertEqual for decoded JSON values.
func assertEqual(t *testing.T, want, got any) {
	t.Helper()
	if !jsonEqual(want, got) {
		t.Fatalf("not equal:\nwant %v\ngot  %v", want, got)
	}
}

// assertAlmostEqual is Python's assertAlmostEqual for one number (default 7 places).

// cloneRow is dict(row): a shallow copy of one decoded row.
func cloneRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = v
	}
	return out
}
