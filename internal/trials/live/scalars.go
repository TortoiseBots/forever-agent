package live

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// normGUID is canonGUIDAny: the same form for a guid read from a JSON row.
func normGUID(v any) string { return canonGUIDAny(v) }

// canonGUID is the bench's one spelling of a guid, used by every path that compares or keys guids (the unit and
// status rows, the setup document, the collector, the guard, the stranger check). A hex guid ("0x" prefix) is lower
// case with its leading zeros dropped and at least sixteen digits, so 0x0801...0b and 0x801...0b are the same guid
// and every significant digit of a 128-bit player guid stays; two different guids never merge. The zero guid (0x0, an
// empty value) is "" so it can never stand for a party member or match a unit. A text without the "0x" prefix is
// never read as a number: it passes through lower-cased.
func canonGUID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "0x") {
		return s
	}
	digits := s[2:]
	for _, r := range digits {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return s
		}
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return ""
	}
	if len(digits) < 16 {
		digits = strings.Repeat("0", 16-len(digits)) + digits
	}
	return "0x" + digits
}

// canonGUIDAny is canonGUID for a JSON value: a string as is, a whole number (a guid an old document wrote as a
// number) as its hex form when a float64 holds it exactly, anything else (a fraction, a number past 2^53 that a float
// cannot hold, null) as "".
func canonGUIDAny(v any) string {
	switch t := v.(type) {
	case string:
		return canonGUID(t)
	case float64:
		if t >= 1 && t == math.Floor(t) && t < 1<<53 {
			return canonGUID(fmt.Sprintf("0x%x", uint64(t)))
		}
	case int:
		if t > 0 {
			return canonGUID(fmt.Sprintf("0x%x", t))
		}
	case int64:
		if t > 0 {
			return canonGUID(fmt.Sprintf("0x%x", t))
		}
	case uint64:
		if t > 0 {
			return canonGUID(fmt.Sprintf("0x%x", t))
		}
	}
	return ""
}

// boolOf is Python's truthiness for a JSON scalar used as a flag.
func boolOf(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != "" && t != "0" && !strings.EqualFold(t, "false")
	default:
		return true
	}
}

// numOf is a JSON number as float64.
func numOf(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func uintOf(v any) uint32 {
	if f, ok := numOf(v); ok && f >= 0 {
		return uint32(f)
	}
	return 0
}

func intOf(v any) int64 {
	if f, ok := numOf(v); ok {
		return int64(f)
	}
	return 0
}

// errf formats a runner error naming the step.
func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }
