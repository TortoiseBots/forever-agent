package geartalents

// Translated scoring tests from the Python bench's test_gear.py, test_gear_proficiency.py, test_hire_talents.py.

import ()

// writeJSON writes a document the way the Python bench does (indent=2, sorted keys).

// writeJSONL writes JSON lines the way the bench writes events/samples/marks files.

// readJSON reads a JSON document.

// readJSONL reads a JSON-lines file into objects.

// fixtureText returns a file of tests/fixtures-liveN (copied to internal/trials/testdata).

// fixtureJSON decodes a JSON fixture.

// fixtureRows decodes a JSON-lines fixture.

// obj asserts a decoded value is an object.

// list asserts a decoded value is a list.

// num asserts a decoded value is a number.

// jsonEqual compares two decoded JSON values like Python's ==: numbers by value (1 == 1.0),
// lists element by element, objects by key set and value.

// assertEqual is Python's assertEqual for decoded JSON values.

// assertAlmostEqual is Python's assertAlmostEqual for one number (default 7 places).

// cloneRow is dict(row): a shallow copy of one decoded row.
