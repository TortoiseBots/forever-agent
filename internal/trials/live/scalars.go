package live

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normGUID lower-cases a guid value for compare/display. The collector
// writes the Python "0x%016x" form through events.NormGUID; this one is
// only for client-side map keys.
func normGUID(v any) string {
	s, _ := v.(string)
	return strings.ToLower(strings.TrimSpace(s))
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
