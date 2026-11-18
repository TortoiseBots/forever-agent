package pooled

// Translated scoring tests from the Python bench's test_addpickup_pooled.py, test_counterspell_pooled.py, test_json_safe.py.

import (
	"bytes"
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

// readJSONL reads a JSON-lines file into objects.

// fixtureText returns a file of tests/fixtures-liveN (copied to internal/trials/testdata).

// fixtureJSON decodes a JSON fixture.

// fixtureRows decodes a JSON-lines fixture.

// obj asserts a decoded value is an object.

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

// assertEqual is Python's assertEqual for decoded JSON values.

// assertAlmostEqual is Python's assertAlmostEqual for one number (default 7 places).

// cloneRow is dict(row): a shallow copy of one decoded row.
