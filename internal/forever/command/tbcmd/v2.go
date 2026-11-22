package tbcmd

// The typed layer's decoder for reply grammar version 2. The rules, in my own words (see the round notes):
//
//   - A line is words separated by one space. The first word is the record kind; every other word is key=value;
//     the last word of every line of a tokened reply is #token.
//   - A report starts with <command>-begin #token and ends with <command>-end count=<lines> records=<records>
//     ... #token. count counts the lines between the two markers, continuation lines included; records counts the
//     lines that are not continuations. Only an error is a single-line reply and has no markers.
//   - A key is [A-Za-z][A-Za-z0-9_]{0,23} and appears at most once per line, and at most once in a record (its base
//     line plus its continuation lines). Case is kept as it arrives: the module writes a camel-case key (`onTaxi` on
//     a state line), and a key the typed layer does not know is left for the caller's Extra map, never refused for
//     its case. A continuation line is <kind>+ and repeats the record's identity key as its first key, immediately
//     after its base line.
//   - A value is percent-encoded: every byte outside [A-Za-z0-9._-] is %XX with upper-case hex; a '%' is always
//     an escape. Inside a value, ';' separates list items, ':' separates the parts of one item and ',' separates
//     coordinates; those three never come from encoding data.
//   - A line is at most 3,000 bytes. An empty value is the empty string.
//
// Everything here is strict: a bad percent sequence, a repeated key, a line over the cap, a missing begin, a count
// that does not match the lines, a token that is not the run's, a free word or a missing required key is
// ErrBadReport (or ErrBadValue for a value that cannot be read), never a zero substituted for "not known".

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// maxReplyLineBytes is the grammar's cap on one reply line (the chat text field itself holds 4,095).
const maxReplyLineBytes = 3000

// record is one decoded record: its base line merged with its continuation lines. values holds the raw
// (percent-encoded) values; text helpers decode at the leaf, so a list is split before its items are decoded.
type record struct {
	kind   string
	values map[string]string
	keys   []string
}

func (r record) has(key string) bool {
	_, ok := r.values[key]
	return ok
}

func (r record) rawValue(key string) (string, bool) {
	v, ok := r.values[key]
	return v, ok
}

// text returns a scalar value, decoded. An absent key is not known.
func (r record) text(key string) (string, bool, error) {
	v, ok := r.values[key]
	if !ok {
		return "", false, nil
	}
	s, err := decodePart(v)
	if err != nil {
		return "", false, r.bad(key, err)
	}
	return s, true, nil
}

// requireText returns a required scalar value, decoded.
func (r record) requireText(key string) (string, error) {
	v, ok := r.values[key]
	if !ok {
		return "", r.missing(key)
	}
	s, err := decodePart(v)
	if err != nil {
		return "", r.bad(key, err)
	}
	return s, nil
}

// requireInt returns a required decimal number inside [min,max].
func (r record) requireInt(key string, min, max int) (int, error) {
	v, ok := r.values[key]
	if !ok {
		return 0, r.missing(key)
	}
	return r.intValue(key, v, min, max)
}

// optionalInt returns an optional number; present is false when the key is absent. A number the module never writes
// empty (the cursors seq=, ev=, leg=, step=, last=, and every other optional number) must be a number when it is
// there: an empty value is ErrBadValue, never "not known". The keys the command reference marks "empty when none"
// use optionalIntEmpty instead.
func (r record) optionalInt(key string, min, max int) (n int, present bool, err error) {
	v, ok := r.values[key]
	if !ok {
		return 0, false, nil
	}
	n, err = r.intValue(key, v, min, max)
	return n, true, err
}

// optionalIntEmpty is optionalInt for the two state numbers the command reference marks "empty when none":
// tgtentry= and tgthp= (the engine's target; the module writes them empty when it has none). The empty value is
// "not known", exactly as an absent key is: it never becomes a zero the caller could act on. It is used nowhere
// else; every other optional number is strict.
func (r record) optionalIntEmpty(key string, min, max int) (n int, present bool, err error) {
	v, ok := r.values[key]
	if !ok || v == "" {
		return 0, false, nil
	}
	n, err = r.intValue(key, v, min, max)
	return n, true, err
}

func (r record) intValue(key, enc string, min, max int) (int, error) {
	s, err := decodePart(enc)
	if err != nil {
		return 0, r.bad(key, err)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%w: %s=%q is not a number in %d..%d", ErrBadValue, key, s, min, max)
	}
	return n, nil
}

// requireUint32 returns a required unsigned 32-bit number; a negative or overflowing value is refused.
func (r record) requireUint32(key string) (uint32, error) {
	s, err := r.requireText(key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q is not a uint32", ErrBadValue, key, s)
	}
	return uint32(n), nil
}

func (r record) requireBool(key string) (bool, error) {
	s, err := r.requireText(key)
	if err != nil {
		return false, err
	}
	switch s {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, fmt.Errorf("%w: %s=%q is not yes/no", ErrBadValue, key, s)
}

func (r record) optionalBool(key string) (v, present bool, err error) {
	s, ok, err := r.text(key)
	if err != nil || !ok {
		return false, false, err
	}
	switch s {
	case "yes":
		return true, true, nil
	case "no":
		return false, true, nil
	}
	return false, true, fmt.Errorf("%w: %s=%q is not yes/no", ErrBadValue, key, s)
}

func (r record) floatValue(key, s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: %s=%q is not a number", ErrBadValue, key, s)
	}
	return f, nil
}

// list splits a required ';' list and decodes each item. An empty value is an empty list; an empty item is bad.
func (r record) list(key string) ([]string, error) {
	v, ok := r.values[key]
	if !ok {
		return nil, r.missing(key)
	}
	return r.listValue(key, v)
}

func (r record) optionalList(key string) ([]string, bool, error) {
	v, ok := r.values[key]
	if !ok {
		return nil, false, nil
	}
	l, err := r.listValue(key, v)
	return l, true, err
}

func (r record) listValue(key, enc string) ([]string, error) {
	if enc == "" {
		return nil, nil
	}
	parts := strings.Split(enc, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, r.bad(key, fmt.Errorf("an empty list item"))
		}
		s, err := decodePart(p)
		if err != nil {
			return nil, r.bad(key, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// parts splits one value on ':' and decodes each part. It is used for the structured values (a target label,
// ord, stuck, a threat pair) before any part is decoded.
func (r record) parts(key string) ([]string, error) {
	v, ok := r.values[key]
	if !ok {
		return nil, r.missing(key)
	}
	enc := strings.Split(v, ":")
	out := make([]string, len(enc))
	for i, p := range enc {
		s, err := decodePart(p)
		if err != nil {
			return nil, r.bad(key, err)
		}
		out[i] = s
	}
	return out, nil
}

// extra decodes every key the caller does not type. Unknown keys are allowed and kept, never an error.
func (r record) extra(known map[string]bool) (map[string]string, error) {
	var out map[string]string
	for _, k := range r.keys {
		if known[k] {
			continue
		}
		s, err := decodePart(r.values[k])
		if err != nil {
			return nil, r.bad(k, err)
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[k] = s
	}
	return out, nil
}

func (r record) missing(key string) error {
	return fmt.Errorf("%w: %s has no %s", ErrBadReport, r.kind, key)
}

func (r record) bad(key string, err error) error {
	return fmt.Errorf("%w: %s=%q: %v", ErrBadReport, key, r.values[key], err)
}

// ---- line and report decoding ----

func validKey(k string) bool {
	if len(k) == 0 || len(k) > 24 {
		return false
	}
	for i := range len(k) {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}
	return true
}

func validKind(k string) bool {
	base := strings.TrimSuffix(k, "+")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		return false
	}
	for i := range len(base) {
		switch {
		case base[i] >= 'a' && base[i] <= 'z', base[i] >= '0' && base[i] <= '9', base[i] == '-':
		default:
			return false
		}
	}
	return true
}

func isUpperHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	default:
		return c - 'A' + 10
	}
}

// checkEncoded validates one raw value: only the literal set the grammar allows, and every '%' a complete,
// upper-case escape of a printable byte.
func checkEncoded(s string) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' {
			if i+2 >= len(s) || !isUpperHex(s[i+1]) || !isUpperHex(s[i+2]) {
				return fmt.Errorf("%% at byte %d is not two upper-case hex digits", i)
			}
			v := unhex(s[i+1])<<4 | unhex(s[i+2])
			if v < 0x20 || v == 0x7f {
				return fmt.Errorf("%%%c%c is a control byte", s[i+1], s[i+2])
			}
			i += 2
			continue
		}
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == ';' || c == ':' || c == ',':
		default:
			return fmt.Errorf("byte %q must be percent-encoded", c)
		}
	}
	return nil
}

// decodePart decodes one already-validated encoded part.
func decodePart(s string) (string, error) {
	if !strings.ContainsRune(s, '%') {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
		i += 2
	}
	return b.String(), nil
}

// decodeLine turns one collected line into a kind and its raw values, refusing anything the grammar does not
// allow. The token must be present (the collector matched it to the run).
func decodeLine(ln command.ReplyLine) (record, error) {
	if len(ln.Raw) > maxReplyLineBytes {
		return record{}, fmt.Errorf("%w: a line of %d bytes is over the %d-byte cap", ErrBadReport, len(ln.Raw), maxReplyLineBytes)
	}
	raw := ln.Raw
	if raw != strings.TrimSpace(raw) || strings.Contains(raw, "  ") || strings.ContainsAny(raw, "\t\r\n") {
		return record{}, fmt.Errorf("%w: %q is not words separated by one space", ErrBadReport, raw)
	}
	if ln.Bad != "" {
		return record{}, fmt.Errorf("%w: %s", ErrBadReport, ln.Bad)
	}
	if ln.Token == "" {
		return record{}, fmt.Errorf("%w: %q has no #token", ErrBadReport, raw)
	}
	if len(ln.Words) > 0 {
		return record{}, fmt.Errorf("%w: %q has the free word %q", ErrBadReport, raw, ln.Words[0])
	}
	first := raw
	if i := strings.IndexByte(first, ' '); i >= 0 {
		first = first[:i]
	}
	if strings.ContainsRune(first, '=') {
		return record{}, fmt.Errorf("%w: %q starts with key=value, not a kind word", ErrBadReport, raw)
	}
	if !validKind(ln.Kind) {
		return record{}, fmt.Errorf("%w: %q is not a kind word", ErrBadReport, ln.Kind)
	}
	r := record{kind: ln.Kind, values: make(map[string]string, len(ln.Keys))}
	for _, k := range ln.Keys {
		if !validKey(k) {
			return record{}, fmt.Errorf("%w: %q is not a key", ErrBadReport, k)
		}
		v := ln.Fields[k]
		if err := checkEncoded(v); err != nil {
			return record{}, fmt.Errorf("%w: %s=%q: %v", ErrBadReport, k, v, err)
		}
		r.values[k] = v
		r.keys = append(r.keys, k)
	}
	return r, nil
}

// v2Report is one decoded multi-line reply: its markers, its merged records and the end line's fields.
type v2Report struct {
	Begin   record
	End     record
	Records []record
}

// parseReport decodes a multi-line reply. beginKind, endKind and dataKind are the command's own markers; identity
// is the key every record of the kind carries (and every continuation repeats).
func parseReport(rep *command.Reply, beginKind, endKind, dataKind, identity string) (*v2Report, error) {
	if rep == nil {
		return nil, fmt.Errorf("%w: no reply", ErrBadReport)
	}
	if !rep.Complete || len(rep.Lines) < 2 {
		return nil, fmt.Errorf("%w: the reply ended without its %s line", ErrBadReport, endKind)
	}
	if rep.Token == "" {
		return nil, fmt.Errorf("%w: the run had no token", ErrBadReport)
	}
	for _, ln := range rep.Lines {
		if ln.Token != rep.Token {
			return nil, fmt.Errorf("%w: a line carries token %q, the run's is %q", ErrBadReport, ln.Token, rep.Token)
		}
	}
	begin, err := decodeLine(rep.Lines[0])
	if err != nil {
		return nil, err
	}
	if begin.kind != beginKind {
		return nil, fmt.Errorf("%w: the reply starts with %q, not %q (a missing begin marker)", ErrBadReport, begin.kind, beginKind)
	}
	if len(begin.values) > 0 {
		return nil, fmt.Errorf("%w: %s carries a field", ErrBadReport, beginKind)
	}
	end, err := decodeLine(rep.Lines[len(rep.Lines)-1])
	if err != nil {
		return nil, err
	}
	if end.kind != endKind {
		return nil, fmt.Errorf("%w: the reply ends with %q, not %q", ErrBadReport, end.kind, endKind)
	}
	between := len(rep.Lines) - 2
	count, err := end.requireInt("count", 0, math.MaxInt32)
	if err != nil {
		return nil, err
	}
	if count != between {
		return nil, fmt.Errorf("%w: %s count=%d, %d line(s) between the markers", ErrBadReport, endKind, count, between)
	}
	records, err := end.requireInt("records", 0, math.MaxInt32)
	if err != nil {
		return nil, err
	}
	out := &v2Report{Begin: begin, End: end}
	cur := -1
	for i := 1; i < len(rep.Lines)-1; i++ {
		ln, err := decodeLine(rep.Lines[i])
		if err != nil {
			return nil, err
		}
		cont := strings.HasSuffix(ln.kind, "+")
		base := strings.TrimSuffix(ln.kind, "+")
		if base != dataKind {
			return nil, fmt.Errorf("%w: a %q line inside a %s reply", ErrBadReport, ln.kind, dataKind)
		}
		if !cont {
			out.Records = append(out.Records, ln)
			cur = len(out.Records) - 1
			continue
		}
		if cur < 0 {
			return nil, fmt.Errorf("%w: a %s+ line before any %s line", ErrBadReport, dataKind, dataKind)
		}
		rec := &out.Records[cur]
		if len(ln.keys) == 0 || ln.keys[0] != identity {
			return nil, fmt.Errorf("%w: %s+ must repeat %s= as its first key", ErrBadReport, dataKind, identity)
		}
		idv, ok := ln.values[identity]
		if !ok {
			return nil, fmt.Errorf("%w: %s+ has no %s=", ErrBadReport, dataKind, identity)
		}
		if idv != rec.values[identity] {
			return nil, fmt.Errorf("%w: %s+ for %q follows %q", ErrBadReport, dataKind, idv, rec.values[identity])
		}
		for _, k := range ln.keys {
			if k == identity {
				continue // v2 repeats the identity on a continuation; the value was checked above
			}
			if rec.has(k) {
				return nil, fmt.Errorf("%w: %s+ repeats %s", ErrBadReport, dataKind, k)
			}
			rec.values[k] = ln.values[k]
			rec.keys = append(rec.keys, k)
		}
	}
	if records != len(out.Records) {
		return nil, fmt.Errorf("%w: %s records=%d, %d record(s) between the markers", ErrBadReport, endKind, records, len(out.Records))
	}
	for _, rec := range out.Records {
		if !rec.has(identity) {
			return nil, fmt.Errorf("%w: %s has no %s=", ErrBadReport, dataKind, identity)
		}
	}
	if err := uniqueBy(out.Records, identity); err != nil {
		return nil, err
	}
	return out, nil
}

// uniqueBy refuses two records with the same identity value.
func uniqueBy(recs []record, key string) error {
	seen := make(map[string]bool, len(recs))
	for _, rec := range recs {
		v := rec.values[key]
		if seen[v] {
			return fmt.Errorf("%w: two %s lines for %q", ErrBadReport, rec.kind, v)
		}
		seen[v] = true
	}
	return nil
}

// moduleError finds the error line of a reply: the only single-line form, `error code=... cmd=... [msg=...] #token`,
// with no other key and no other line.
func moduleError(rep *command.Reply) (*CommandError, bool, error) {
	for _, ln := range rep.Lines {
		if ln.Kind != "error" {
			continue
		}
		if len(rep.Lines) != 1 {
			return nil, true, fmt.Errorf("%w: an error line inside a %d-line reply", ErrBadReport, len(rep.Lines))
		}
		r, err := decodeLine(ln)
		if err != nil {
			return nil, true, err
		}
		for _, k := range r.keys {
			if k != "code" && k != "cmd" && k != "msg" {
				return nil, true, fmt.Errorf("%w: error carries %s=", ErrBadReport, k)
			}
		}
		code, err := r.requireText("code")
		if err != nil {
			return nil, true, err
		}
		cmd, err := r.requireText("cmd")
		if err != nil {
			return nil, true, err
		}
		msg, hasMsg, err := r.text("msg")
		if err != nil {
			return nil, true, err
		}
		modal := CommandError{Code: code, Cmd: cmd, HasMsg: hasMsg}
		if hasMsg {
			modal.Msg = msg
		}
		return &modal, true, nil
	}
	return nil, false, nil
}
