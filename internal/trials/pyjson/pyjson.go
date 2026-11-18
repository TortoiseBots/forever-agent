// Package pyjson reads and writes JSON the way the Python trials bench does.
//
// The bench's report cards, verdicts and event rows are compared byte for byte with the
// Python tool (tools/trialsparity), so this package keeps Python's JSON semantics:
//
//   - json.loads turns an integer literal into an int and anything with a fraction or an
//     exponent into a float; the two stay distinct (Python prints 1 and 1.0 differently).
//   - json.dumps(sort_keys=True) sorts object keys; indent=2 uses ", " and ": " separators
//     and Python's float repr (shortest round-trip digits, ".0" on whole floats, e-notation
//     only outside 1e-4..1e16).
//   - allow_nan=False is the default here: a NaN or an infinity is an error, never a
//     non-standard Infinity/NaN token.
//
// A decoded document is map[string]any, []any, string, bool, int64, float64 or nil.
package pyjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Unmarshal decodes JSON like Python's json.loads: object -> map[string]any,
// array -> []any, integer -> int64 (a *big.Int outside that range), other number -> float64.
// Like json.loads it rejects anything after the first value, and a number it cannot represent
// is an error rather than a silent 0.
func Unmarshal(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("pyjson: trailing data after the first JSON value")
	}
	return convert(raw)
}

// UnmarshalFile reads and decodes a JSON file.
func UnmarshalFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Unmarshal(data)
}

func convert(v any) (any, error) {
	switch val := v.(type) {
	case json.Number:
		text := string(val)
		if strings.ContainsAny(text, ".eE") {
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				// Python's json returns inf for 1e999; a syntax error cannot occur here.
				if errors.Is(err, strconv.ErrRange) {
					return f, nil
				}
				return nil, fmt.Errorf("pyjson: number %q: %w", text, err)
			}
			return f, nil
		}
		i, err := strconv.ParseInt(text, 10, 64)
		if err == nil {
			return i, nil
		}
		if errors.Is(err, strconv.ErrRange) {
			// Python keeps arbitrary precision: out-of-range integers become *big.Int.
			bi, ok := new(big.Int).SetString(text, 10)
			if !ok {
				return nil, fmt.Errorf("pyjson: integer %q", text)
			}
			return bi, nil
		}
		return nil, fmt.Errorf("pyjson: integer %q: %w", text, err)
	case map[string]any:
		for k, item := range val {
			converted, err := convert(item)
			if err != nil {
				return nil, err
			}
			val[k] = converted
		}
		return val, nil
	case []any:
		for i, item := range val {
			converted, err := convert(item)
			if err != nil {
				return nil, err
			}
			val[i] = converted
		}
		return val, nil
	}
	return v, nil
}

// Get is doc[key] for an object, nil for anything else (Python's dict .get on a non-dict
// would raise, so callers only use it where the bench does).
func Get(doc any, key string) any {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	return m[key]
}

// Object returns doc as an object; ok is false for a non-object.
func Object(doc any) (map[string]any, bool) {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, false
	}
	return m, true
}

// ObjectOrEmpty returns doc as an object, or an empty one.
func ObjectOrEmpty(doc any) map[string]any {
	if m, ok := doc.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// List returns doc as a list; ok is false for a non-list. A Go []string counts as a list:
// the engine hands its evidence lines over that way.
func List(doc any) ([]any, bool) {
	switch l := doc.(type) {
	case []any:
		return l, true
	case []string:
		out := make([]any, len(l))
		for i, item := range l {
			out[i] = item
		}
		return out, true
	}
	return nil, false
}

// IsBool reports whether v is a JSON boolean.
func IsBool(v any) bool {
	_, ok := v.(bool)
	return ok
}

// IsInt reports whether v is a JSON integer (not a boolean, though Python's bool is an int
// subclass, which callers that follow isinstance() must handle themselves). An integer
// outside int64 is a *big.Int, as Python's int is arbitrary precision.
func IsInt(v any) bool {
	switch v.(type) {
	case int64, *big.Int:
		return true
	}
	return false
}

// Int64 returns v as a machine integer when it fits (bools are not integers here). A
// *big.Int is not converted: callers that need the exact value use IsInt/Str.
func Int64(v any) (int64, bool) {
	i, ok := v.(int64)
	return i, ok
}

// IsNum reports whether v is a JSON number or boolean: Python's isinstance(v, (int, float))
// is true for bool, and the bench's `_num` relies on that.
func IsNum(v any) bool {
	switch v.(type) {
	case int64, float64, bool, *big.Int:
		return true
	}
	return false
}

// Num converts a number (or bool, as Python's float(bool) would) to a float.
func Num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case *big.Int:
		f, _ := new(big.Float).SetInt(n).Float64()
		if math.IsInf(f, 0) {
			return 0.0, false
		}
		return f, true
	case bool:
		if n {
			return 1.0, true
		}
		return 0.0, true
	}
	return 0.0, false
}

// NumOrZero converts a number to float, 0 when it is not one.
func NumOrZero(v any) float64 {
	if f, ok := Num(v); ok {
		return f
	}
	return 0.0
}

// PyInt is Python's int(v): an int (int64, or *big.Int outside that range), a bool, a float
// (truncated toward zero) or a decimal string. It fails where Python raises TypeError or
// ValueError (NaN/inf, "5.0", a list, ...), so callers can catch exactly what the bench does.
func PyInt(v any) (any, error) {
	switch n := v.(type) {
	case int64, *big.Int:
		return n, nil
	case bool:
		if n {
			return int64(1), nil
		}
		return int64(0), nil
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("pyjson: cannot convert %v to int", n)
		}
		const limit = 1 << 62
		if n >= -limit && n <= limit {
			return int64(n), nil
		}
		bi, _ := new(big.Float).SetFloat64(n).Int(nil)
		return bi, nil
	case string:
		text := strings.TrimSpace(n)
		if text == "" {
			return nil, errors.New("pyjson: invalid literal for int()")
		}
		if strings.Contains(text, "_") {
			// Python allows single underscores between digits only.
			body := text
			if body[0] == '+' || body[0] == '-' {
				body = body[1:]
			}
			if body == "" || strings.HasPrefix(body, "_") || strings.HasSuffix(body, "_") ||
				strings.Contains(body, "__") {
				return nil, fmt.Errorf("pyjson: invalid literal for int(): %q", n)
			}
			text = strings.ReplaceAll(text, "_", "")
		}
		bi, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return nil, fmt.Errorf("pyjson: invalid literal for int(): %q", n)
		}
		if bi.IsInt64() {
			return bi.Int64(), nil
		}
		return bi, nil
	}
	return nil, fmt.Errorf("pyjson: cannot convert %T to int", v)
}

// Float converts a value to float when the bench's float(value) would accept it (a number,
// a bool, or a numeric string); ok is false on a TypeError/ValueError.
func Float(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case *big.Int:
		f, _ := new(big.Float).SetInt(n).Float64()
		if math.IsInf(f, 0) {
			return 0.0, false
		}
		return f, true
	case bool:
		if n {
			return 1.0, true
		}
		return 0.0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0.0, false
		}
		return f, true
	}
	return 0.0, false
}

// Str renders a value the way Python's str() does for the scalars the bench prints.
func Str(v any) string {
	switch s := v.(type) {
	case nil:
		return "None"
	case string:
		return s
	case bool:
		if s {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(s, 10)
	case *big.Int:
		return s.String()
	case float64:
		return FormatFloat(s)
	}
	return fmt.Sprint(v)
}

// Truthy is Python's bool(v).
func Truthy(v any) bool {
	switch val := v.(type) {
	case nil:
		return false
	case bool:
		return val
	case string:
		return val != ""
	case int64:
		return val != 0
	case *big.Int:
		return val.Sign() != 0
	case float64:
		return val != 0
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	}
	return true
}

// FormatFloat is Python's repr() for a float: shortest round-trip digits, a ".0" on whole
// floats, and e-notation only when the decimal exponent is below -4 or at least 16.
func FormatFloat(f float64) string {
	if math.IsNaN(f) {
		return "nan"
	}
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	neg := ""
	if f < 0 {
		neg = "-"
		f = -f
	}
	// Shortest round-trip digits and the exponent of the first digit.
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expText, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mant, ".", "", 1)
	// digits has no trailing zeros except for "1" style mantissas.
	if exp >= -4 && exp < 16 {
		var b strings.Builder
		b.WriteString(neg)
		if exp < 0 {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", -exp-1))
			b.WriteString(digits)
			return b.String()
		}
		if len(digits) <= exp+1 {
			b.WriteString(digits)
			b.WriteString(strings.Repeat("0", exp+1-len(digits)))
			b.WriteString(".0")
			return b.String()
		}
		b.WriteString(digits[:exp+1])
		b.WriteString(".")
		b.WriteString(digits[exp+1:])
		return b.String()
	}
	var b strings.Builder
	b.WriteString(neg)
	b.WriteString(digits[:1])
	if len(digits) > 1 {
		b.WriteString(".")
		b.WriteString(digits[1:])
	}
	b.WriteString("e")
	if exp < 0 {
		b.WriteString("-")
		exp = -exp
	} else {
		b.WriteString("+")
	}
	b.WriteString(fmt.Sprintf("%02d", exp))
	return b.String()
}

// Round is Python's round(x, nd): the exact binary value rounded to nd decimals, ties to
// even. It is not math.Round(x*10**nd)/10**nd, which double-rounds.
func Round(x float64, nd int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	if x == 0 {
		return math.Copysign(0, x)
	}
	rat := new(big.Rat).SetFloat64(x)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(nd))), nil)
	if nd >= 0 {
		rat.Mul(rat, new(big.Rat).SetInt(scale))
	} else {
		rat.Quo(rat, new(big.Rat).SetInt(scale))
	}
	// Round the rational to the nearest integer, ties to even.
	num := new(big.Int).Set(rat.Num())
	den := rat.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Lsh(rem.Abs(rem), 1)
	switch twice.Cmp(den) {
	case 1:
		if rat.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	case 0:
		if q.Bit(0) == 1 {
			if rat.Sign() > 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	out := new(big.Rat).SetInt(q)
	if nd >= 0 {
		out.Quo(out, new(big.Rat).SetInt(scale))
	} else {
		out.Mul(out, new(big.Rat).SetInt(scale))
	}
	res, _ := out.Float64()
	if res == 0 {
		return math.Copysign(0, x)
	}
	return res
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// SumFloat is Python 3.12's sum() over floats: Neumaier compensated summation, so a card
// mean matches the Python card bit for bit.
func SumFloat(vals []float64) float64 {
	total, c := 0.0, 0.0
	for _, f := range vals {
		t := total + f
		if math.Abs(total) >= math.Abs(f) {
			c += (total - t) + f
		} else {
			c += (f - t) + total
		}
		total = t
	}
	return total + c
}

// FormatG is Python's "%.<prec>g" % v.
func FormatG(prec int, v float64) string {
	if math.IsInf(v, 1) {
		return "inf"
	}
	if math.IsInf(v, -1) {
		return "-inf"
	}
	if math.IsNaN(v) {
		return "nan"
	}
	return strconv.FormatFloat(v, 'g', prec, 64)
}

// FormatF is Python's "%.<dec>f" % v.
func FormatF(dec int, v float64) string {
	return strconv.FormatFloat(v, 'f', dec, 64)
}

// Marshal is json.dumps(v, indent=2, sort_keys=True, allow_nan=False).
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, v, "  ", "", true); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// MarshalLine is json.dumps(v, sort_keys=True, allow_nan=False): one line, Python's
// default ", " and ": " separators.
func MarshalLine(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, v, "", "", false); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Dump writes json.dumps(v, indent=2, sort_keys=True, allow_nan=False) to path.
func Dump(path string, v any) error {
	data, err := Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// DumpLine writes one json.dumps(v, sort_keys=True) line and a newline to w.
func DumpLine(w *bytes.Buffer, v any) error {
	data, err := MarshalLine(v)
	if err != nil {
		return err
	}
	w.Write(data)
	w.WriteByte('\n')
	return nil
}

// ErrNotJSON is returned for a value the bench cannot serialise.
var ErrNotJSON = errors.New("pyjson: unsupported value")

func writeValue(b *bytes.Buffer, v any, indent, cur string, pretty bool) error {
	switch val := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if val {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, val)
	case int64:
		b.WriteString(strconv.FormatInt(val, 10))
	case *big.Int:
		b.WriteString(val.String())
	case int:
		b.WriteString(strconv.Itoa(val))
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return fmt.Errorf("pyjson: %v is not allowed with allow_nan=False", val)
		}
		b.WriteString(FormatFloat(val))
	case []any:
		if len(val) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				b.WriteByte(',')
				if pretty {
					b.WriteByte('\n')
					b.WriteString(cur + indent)
				} else {
					b.WriteByte(' ')
				}
			} else if pretty {
				b.WriteByte('\n')
				b.WriteString(cur + indent)
			}
			if err := writeValue(b, item, indent, cur+indent, pretty); err != nil {
				return err
			}
		}
		if pretty {
			b.WriteByte('\n')
			b.WriteString(cur)
		}
		b.WriteByte(']')
	case []string:
		items := make([]any, len(val))
		for i, item := range val {
			items[i] = item
		}
		return writeValue(b, items, indent, cur, pretty)
	case map[string]any:
		if len(val) == 0 {
			b.WriteString("{}")
			return nil
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
				if pretty {
					b.WriteByte('\n')
					b.WriteString(cur + indent)
				} else {
					b.WriteByte(' ')
				}
			} else if pretty {
				b.WriteByte('\n')
				b.WriteString(cur + indent)
			}
			writeString(b, k)
			b.WriteString(": ")
			if err := writeValue(b, val[k], indent, cur+indent, pretty); err != nil {
				return err
			}
		}
		if pretty {
			b.WriteByte('\n')
			b.WriteString(cur)
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("%w: %T", ErrNotJSON, v)
	}
	return nil
}

const hexDigits = "0123456789abcdef"

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&0xf])
			case r < utf8.RuneSelf:
				b.WriteByte(byte(r))
			case r > 0xffff:
				r -= 0x10000
				hi := 0xd800 + (r >> 10)
				lo := 0xdc00 + (r & 0x3ff)
				writeU(b, hi)
				writeU(b, lo)
			default:
				writeU(b, r)
			}
		}
	}
	b.WriteByte('"')
}

func writeU(b *bytes.Buffer, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xf])
	b.WriteByte(hexDigits[(r>>8)&0xf])
	b.WriteByte(hexDigits[(r>>4)&0xf])
	b.WriteByte(hexDigits[r&0xf])
}
