package pbc

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func rosterRecord(vals ...string) string { return strings.Join(vals, ",") }

// goodRosterRecord is one record in the shape this client reads.
func goodRosterRecord(id, name string) string {
	return rosterRecord(id, name, "12", "WARRIOR", "HUMAN", "TANK", "Elwynn", "1", "91", "40", "7",
		"follow", "combat", "0", "-", "1", "0", "1", "71")
}

func TestEscapeRoundTrip(t *testing.T) {
	cases := []string{
		"",
		"plain",
		"a|b",
		`a\b`,
		`a\pb`,
		`\`,
		`|`,
		`name\|with both`,
		"multi|byte|é|",
	}
	for _, s := range cases {
		esc := Escape(s)
		if got := Unescape(esc); got != s {
			t.Fatalf("Unescape(Escape(%q)) = %q (escaped %q)", s, got, esc)
		}
	}
	if got := Escape(`a|b`); got != `a\pb` {
		t.Fatalf("a pipe escapes as backslash-p, got %q", got)
	}
	if got := Escape(`a\b`); got != `a\\b` {
		t.Fatalf("a backslash escapes as two, got %q", got)
	}
	// A backslash before any other byte drops itself, as the server's reader does.
	if got := Unescape(`a\zb`); got != "azb" {
		t.Fatalf("Unescape dropped the wrong byte: %q", got)
	}
	if got := Unescape(`trailing\`); got != `trailing\` {
		t.Fatalf("a trailing backslash is kept: %q", got)
	}
}

func TestSplitFields(t *testing.T) {
	body := BuildBody([]string{"one", "a|b", `c\d`, ""})
	if body != `one|a\pb|c\\d|` {
		t.Fatalf("body %q", body)
	}
	got := SplitFields(body)
	want := []string{"one", "a|b", `c\d`, ""}
	if len(got) != len(want) {
		t.Fatalf("fields %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("field %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFormatParsePart(t *testing.T) {
	text := FormatPart(0x1234abcd, 2, 3, TypeRosterResp, "a|b")
	if text != "1|1234abcd|2|3|ROSTER_RESP|a|b" {
		t.Fatalf("frame %q", text)
	}
	p, err := ParsePart(text)
	if err != nil {
		t.Fatal(err)
	}
	if p.Seq != 0x1234abcd || p.Index != 2 || p.Total != 3 || p.Type != TypeRosterResp || p.Payload != "a|b" {
		t.Fatalf("part %+v", p)
	}
}

func TestParsePartRefusals(t *testing.T) {
	cases := []struct {
		name string
		text string
		err  error
	}{
		{"not a frame", "ROSTER_REQ|1", ErrNotFrame},
		{"wrong version", "2|1|1|1|PONG|x", ErrNotFrame},
		{"zero seq", "1|00000000|1|1|PONG|x", ErrNotFrame},
		{"index zero", "1|1|0|1|PONG|x", ErrPartIndex},
		{"count zero", "1|1|1|0|PONG|x", ErrPartIndex},
		{"index past count", "1|1|3|2|PONG|x", ErrPartIndex},
		{"too many parts", "1|1|1|65|PONG|x", ErrTooManyParts},
		{"bad type", "1|1|1|1|PONG!|x", ErrType},
		{"empty type", "1|1|1|1||x", ErrType},
		{"oversized payload", "1|1|1|1|PONG|" + strings.Repeat("x", MaxFrame+1), ErrFrameTooBig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParsePart(c.text)
			if !errors.Is(err, c.err) {
				t.Fatalf("err %v, want %v", err, c.err)
			}
		})
	}
}

// splitParts parses a request's texts.
func splitParts(t *testing.T, texts []string) []Part {
	t.Helper()
	out := make([]Part, 0, len(texts))
	for _, text := range texts {
		p, err := ParsePart(text)
		if err != nil {
			t.Fatalf("part %q: %v", text, err)
		}
		out = append(out, p)
	}
	return out
}

func TestBuildRequestChunks(t *testing.T) {
	// A body just over two chunks, with a pipe in it: the pipe is escaped, so a cut may fall inside the pair.
	fields := []string{strings.Repeat("a", 150), "b|c", strings.Repeat("d", 60)}
	texts, err := BuildRequest(7, TypeRosterReq, fields)
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 {
		t.Fatalf("%d part(s), want 2: %q", len(texts), texts)
	}
	parts := splitParts(t, texts)
	asm := NewAssembler()
	now := time.Unix(1000, 0)
	var res Result
	for i, p := range parts {
		if p.Seq != 7 || p.Index != i+1 || p.Total != 2 || p.Type != TypeRosterReq {
			t.Fatalf("part %d = %+v", i, p)
		}
		var done bool
		res, done, err = asm.Add(p, now.Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if done != (i == len(parts)-1) {
			t.Fatalf("part %d: done=%v", i, done)
		}
	}
	if res.Body != BuildBody(fields) {
		t.Fatalf("reassembled %q, want %q", res.Body, BuildBody(fields))
	}
	if got := SplitFields(res.Body); len(got) != len(fields) || got[1] != "b|c" {
		t.Fatalf("fields %q", got)
	}
	if asm.Pending() != 0 {
		t.Fatalf("%d body(ies) left behind", asm.Pending())
	}
}

func TestAssemblerOutOfOrder(t *testing.T) {
	texts, err := BuildRequest(9, TypeRosterResp, []string{"1", strings.Repeat("x", 500)})
	if err != nil {
		t.Fatal(err)
	}
	parts := splitParts(t, texts)
	if len(parts) < 3 {
		t.Fatalf("%d part(s)", len(parts))
	}
	asm := NewAssembler()
	now := time.Unix(2000, 0)
	order := []int{len(parts) - 1, 0}
	for i := 1; i < len(parts)-1; i++ {
		order = append(order, i)
	}
	var done int
	var res Result
	for _, i := range order {
		if i >= len(parts) {
			continue
		}
		var got bool
		res, got, err = asm.Add(parts[i], now)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if got {
			done++
		}
	}
	// The duplicate of the last part is ignored, and the loop walks every index once.
	if done != 1 || res.Type != TypeRosterResp || len(SplitFields(res.Body)) != 2 {
		t.Fatalf("done=%d res=%+v", done, res)
	}
}

func TestAssemblerMissingChunkExpires(t *testing.T) {
	texts, err := BuildRequest(11, TypeRosterResp, []string{"1", strings.Repeat("y", 250)})
	if err != nil {
		t.Fatal(err)
	}
	parts := splitParts(t, texts)
	asm := NewAssembler()
	now := time.Unix(3000, 0)
	for i, p := range parts {
		if i == 1 {
			continue
		}
		if _, done, err := asm.Add(p, now); err != nil || done {
			t.Fatalf("part %d: done=%v err=%v", i, done, err)
		}
	}
	if asm.Pending() != 1 {
		t.Fatalf("%d body(ies) pending, want 1", asm.Pending())
	}
	if n := asm.Expire(now.Add(DefaultTTL - time.Millisecond)); n != 0 {
		t.Fatalf("expired %d body(ies) before the TTL", n)
	}
	if n := asm.Expire(now.Add(DefaultTTL)); n != 1 {
		t.Fatalf("expired %d body(ies) at the TTL, want 1", n)
	}
	if asm.Pending() != 0 {
		t.Fatalf("%d body(ies) after the TTL", asm.Pending())
	}
	// The late part starts a fresh body; it is never reported complete from the stale one.
	if _, done, err := asm.Add(parts[1], now.Add(DefaultTTL)); err != nil || done {
		t.Fatalf("a late part completed a stale body: done=%v err=%v", done, err)
	}
}

func TestAssemblerDuplicateAndConflict(t *testing.T) {
	asm := NewAssembler()
	now := time.Unix(4000, 0)
	a := Part{Seq: 5, Index: 1, Total: 2, Type: TypeRosterResp, Payload: "aa"}
	b := Part{Seq: 5, Index: 2, Total: 2, Type: TypeRosterResp, Payload: "bb"}
	if _, done, err := asm.Add(a, now); err != nil || done {
		t.Fatalf("first part: done=%v err=%v", done, err)
	}
	if _, done, err := asm.Add(a, now); err != nil || done {
		t.Fatalf("duplicate part: done=%v err=%v", done, err)
	}
	res, done, err := asm.Add(b, now)
	if err != nil || !done || res.Body != "aabb" {
		t.Fatalf("res=%+v done=%v err=%v", res, done, err)
	}
	// A part count that disagrees starts over instead of gluing bodies of different shapes.
	if _, done, err := asm.Add(a, now); err != nil || done {
		t.Fatalf("restart: done=%v err=%v", done, err)
	}
	c := Part{Seq: 5, Index: 1, Total: 3, Type: TypeRosterResp, Payload: "cc"}
	if _, done, err := asm.Add(c, now); err != nil || done {
		t.Fatalf("conflicting count: done=%v err=%v", done, err)
	}
	if asm.Pending() != 1 {
		t.Fatalf("conflicting counts left %d body(ies)", asm.Pending())
	}
}

func TestAssemblerOversized(t *testing.T) {
	asm := NewAssembler()
	now := time.Unix(5000, 0)
	if _, _, err := asm.Add(Part{Seq: 1, Index: 1, Total: MaxParts + 1, Type: TypeRosterResp}, now); !errors.Is(err, ErrTooManyParts) {
		t.Fatalf("a part count past the cap: %v", err)
	}
	// Two parts whose payloads together pass MaxFrame are dropped.
	half := MaxFrame/2 + 1
	if _, done, err := asm.Add(Part{Seq: 2, Index: 1, Total: 2, Type: TypeRosterResp, Payload: strings.Repeat("x", half)}, now); err != nil || done {
		t.Fatalf("first half: done=%v err=%v", done, err)
	}
	if _, _, err := asm.Add(Part{Seq: 2, Index: 2, Total: 2, Type: TypeRosterResp, Payload: strings.Repeat("x", half)}, now); !errors.Is(err, ErrFrameTooBig) {
		t.Fatalf("a body past MaxFrame: %v", err)
	}
	if asm.Pending() != 0 {
		t.Fatalf("the oversized body was kept")
	}
}

func TestBuildRequestRefusals(t *testing.T) {
	if _, err := BuildRequest(0, TypeRosterReq, nil); err == nil {
		t.Fatal("sequence zero accepted")
	}
	if _, err := BuildRequest(FirstServerSeq, TypeRosterReq, nil); err == nil {
		t.Fatal("a server-range sequence accepted for a request")
	}
	if _, err := BuildRequest(1, "bad type", nil); !errors.Is(err, ErrType) {
		t.Fatalf("bad type: %v", err)
	}
	// A body over 64 parts is refused before any part is built.
	if _, err := BuildRequest(1, TypeRosterReq, []string{strings.Repeat("z", MaxChunk*MaxParts+1)}); !errors.Is(err, ErrTooManyParts) {
		t.Fatalf("oversized body: %v", err)
	}
}

func TestParseRoster(t *testing.T) {
	fields := []string{"2", goodRosterRecord("42", "Raveni"), goodRosterRecord("43", "Bramble")}
	rows, err := ParseRoster(fields)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d row(s)", len(rows))
	}
	r := rows[0]
	if r.ID != 42 || r.Name != "Raveni" || r.Level != 12 || r.Class != "WARRIOR" || r.Role != "TANK" ||
		!r.Online || r.HPPercent != 91 || r.ManaPercent != 40 || r.Distance != 7 || !r.Headless || !r.Grouped ||
		r.Saved || r.SpecID != 71 || r.GroupID != 0 {
		t.Fatalf("row %+v", r)
	}
	// A record with appended values is accepted and the extras kept raw.
	long := goodRosterRecord("44", "Long") + ",extra,more"
	rows, err = ParseRoster([]string{"1", long})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0].Raw) != 2 || rows[0].Raw[0] != "extra" {
		t.Fatalf("extras %q", rows[0].Raw)
	}
}

func TestParseRosterDrift(t *testing.T) {
	short := rosterRecord("1", "Name", "12", "WARRIOR", "HUMAN", "TANK", "Zone", "1")
	if _, err := ParseRoster([]string{"1", short}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a short record: %v", err)
	}
	if _, err := ParseRoster([]string{"2", goodRosterRecord("1", "A")}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a count with a missing record: %v", err)
	}
	if _, err := ParseRoster(nil); !errors.Is(err, ErrNoFields) {
		t.Fatalf("no fields: %v", err)
	}
	if _, err := ParseRoster([]string{"x"}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a bad count: %v", err)
	}
	bad := strings.Split(goodRosterRecord("1", "A"), ",")
	bad[2] = "twelve"
	if _, err := ParseRoster([]string{"1", strings.Join(bad, ",")}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a non-numeric level: %v", err)
	}
	// A count that understates the records is drift too: the answer is not the shape asked for.
	if _, err := ParseRoster([]string{"1", goodRosterRecord("1", "A"), goodRosterRecord("2", "B")}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a count with an extra record: %v", err)
	}
	// A negative level would wrap into a huge uint32; it is drift, not a level.
	neg := strings.Split(goodRosterRecord("1", "A"), ",")
	neg[2] = "-1"
	if _, err := ParseRoster([]string{"1", strings.Join(neg, ",")}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a negative level wrapped: %v", err)
	}
}

func TestParseDetail(t *testing.T) {
	fields := []string{
		"42", "name=Raveni", "level=12", "class=WARRIOR", "race=HUMAN", "hp=300", "hp_max=400",
		"mana=100", "mana_max=200", "map=0", "pos_x=-8912.5", "pos_y=554.25", "pos_z=93.75",
		"zone_id=12", "area_id=87", "indoors=1", "role=TANK", "spec_id=71", "xp=900", "xp_for_level=4500",
		"rest_xp=0", "last_rule=combat", "intent_depth=3", "future_key=future value",
	}
	d, err := ParseDetail(fields, 42)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != 42 || !d.Known || !d.Live || !d.Present["hp"] || !d.Present["pos_x"] || !d.Present["future_key"] ||
		d.HP != 300 || d.HPMax != 400 || d.Mana != 100 ||
		d.ManaMax != 200 || d.Map != 0 || d.X != -8912.5 || d.Y != 554.25 || d.Z != 93.75 || d.ZoneID != 12 ||
		d.AreaID != 87 || !d.Indoors || d.Role != "TANK" || d.SpecID != 71 || d.XP != 900 ||
		d.XPForLevel != 4500 || d.LastRule != "combat" || d.IntentDepth != 3 {
		t.Fatalf("detail %+v", d)
	}
	if d.Extra["future_key"] != "future value" {
		t.Fatalf("unknown key %v", d.Extra)
	}
	// An answer with the character fields but no snapshot is known and not live; presence, not the zero value,
	// is what says the snapshot came.
	d, err = ParseDetail([]string{"42", "name=Raveni", "level=12", "class=WARRIOR", "race=HUMAN"}, 42)
	if err != nil || !d.Known || d.Live || d.Present["hp"] || d.HP != 0 {
		t.Fatalf("no snapshot: %+v err=%v", d, err)
	}
	// A snapshot with zero values is live: presence is the signal.
	d, err = ParseDetail([]string{"42", "hp=0", "hp_max=0", "map=0", "role=UNKNOWN"}, 42)
	if err != nil || !d.Live || !d.Present["hp"] {
		t.Fatalf("zero snapshot: %+v err=%v", d, err)
	}
	// A renamed key is drift the client keeps raw rather than guesses at.
	d, err = ParseDetail([]string{"42", "hp_cur=5", "name=Raveni"}, 42)
	if err != nil || d.Live || d.Extra["hp_cur"] != "5" {
		t.Fatalf("renamed key: %+v err=%v", d, err)
	}
	// A key this client does not know is kept, never dropped.
	raw := []string{"41", "notakeyvalue"}
	d, err = ParseDetail([]string{raw[0], raw[1]}, 41)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Extra) != 1 {
		t.Fatalf("an unkeyed field was dropped: %v", d.Extra)
	}
	// The id-only answer is the module's "I do not know this bot": a typed not-found, not a zero-value detail.
	if d, err = ParseDetail([]string{"7"}, 7); !errors.Is(err, ErrBotUnknown) || d.ID != 7 || d.Known || d.Name != "" {
		t.Fatalf("id-only: %+v err=%v", d, err)
	}
	if _, err := ParseDetail([]string{"8"}, 7); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("the wrong id: %v", err)
	}
	// A known numeric key whose value cannot be read, and a non-finite position, are drift: fall back, do not guess.
	for _, f := range [][]string{
		{"42", "hp=lots"},
		{"42", "indoors=2"},
		{"42", "pos_x=NaN"},
		{"42", "pos_y=+Inf"},
	} {
		if _, err := ParseDetail(f, 42); !errors.Is(err, ErrProtoMismatch) {
			t.Fatalf("%q accepted: %v", f, err)
		}
	}
	if _, err := ParseDetail([]string{"x"}, 7); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a bad id: %v", err)
	}
	if _, err := ParseDetail(nil, 7); !errors.Is(err, ErrNoFields) {
		t.Fatalf("no fields: %v", err)
	}
}

func TestParseEvent(t *testing.T) {
	e, err := ParseEvent([]string{"error", "42", "auth", "not your bot"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Severity != "error" || e.BotID != 42 || e.Name != "auth" || e.Detail != "not your bot" {
		t.Fatalf("event %+v", e)
	}
	if _, err := ParseEvent([]string{"error", "42", "auth"}); !errors.Is(err, ErrProtoMismatch) {
		t.Fatalf("a short event: %v", err)
	}
}

func FuzzParsePart(f *testing.F) {
	f.Add("1|1234abcd|1|1|ROSTER_RESP|0|1,Name")
	f.Add("1|1|2|3|PONG|\x00\xff\\p")
	f.Add("")
	f.Add("1|1|1|1|PONG|" + strings.Repeat("x", MaxFrame+10))
	f.Fuzz(func(t *testing.T, text string) {
		p, err := ParsePart(text)
		if err != nil {
			return
		}
		if p.Seq == 0 || p.Index < 1 || p.Index > p.Total || p.Total > MaxParts || !ValidType(p.Type) {
			t.Fatalf("accepted an impossible part %+v", p)
		}
		if len(p.Payload) > MaxFrame {
			t.Fatalf("accepted a %d-byte payload", len(p.Payload))
		}
		// Whatever the payload, the splitter must not loop or panic, and its inverse must round-trip the fields.
		fields := SplitFields(p.Payload)
		if got := SplitFields(BuildBody(fields)); len(got) != len(fields) {
			t.Fatalf("field count changed: %d -> %d", len(fields), len(got))
		}
	})
}

func FuzzRoster(f *testing.F) {
	f.Add([]byte("2"))
	f.Add([]byte("1\x00" + goodRosterRecord("42", "Raveni")))
	f.Add([]byte("\xff\x00,|\\p"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// The body is arbitrary bytes: split it and parse the result, as the session does.
		fields := SplitFields(string(data))
		rows, err := ParseRoster(fields)
		if err != nil {
			if !errors.Is(err, ErrProtoMismatch) && !errors.Is(err, ErrNoFields) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		for range rows {
			// Every row parsed: the parser's own bounds are the check, and the loop must not panic on any input.
		}
	})
}

func TestAssemblerKeyedBySender(t *testing.T) {
	// The same sequence number from two senders must never mix: the bodies are separate and each completes.
	a := wire.GUID{Low: 1, High: 0x0800000000000000}
	b := wire.GUID{Low: 2, High: 0x0800000000000000}
	asm := NewAssembler()
	now := time.Unix(6000, 0)
	if _, done, err := asm.Add(Part{Sender: a, Seq: 5, Index: 1, Total: 2, Type: TypeRosterResp, Payload: "aa"}, now); err != nil || done {
		t.Fatalf("a first: done=%v err=%v", done, err)
	}
	if _, done, err := asm.Add(Part{Sender: b, Seq: 5, Index: 1, Total: 2, Type: TypeRosterResp, Payload: "bb"}, now); err != nil || done {
		t.Fatalf("b first: done=%v err=%v", done, err)
	}
	if asm.Pending() != 2 {
		t.Fatalf("%d bodies, want 2", asm.Pending())
	}
	res, done, err := asm.Add(Part{Sender: a, Seq: 5, Index: 2, Total: 2, Type: TypeRosterResp, Payload: "AA"}, now)
	if err != nil || !done || res.Body != "aaAA" || res.Sender != a {
		t.Fatalf("a done=%v res=%+v err=%v", done, res, err)
	}
	res, done, err = asm.Add(Part{Sender: b, Seq: 5, Index: 2, Total: 2, Type: TypeRosterResp, Payload: "BB"}, now)
	if err != nil || !done || res.Body != "bbBB" || res.Sender != b {
		t.Fatalf("b done=%v res=%+v err=%v", done, res, err)
	}
}

func TestAssemblerPendingCap(t *testing.T) {
	asm := NewAssembler()
	now := time.Unix(7000, 0)
	for i := 0; i < MaxPending; i++ {
		p := Part{Seq: uint32(i + 1), Index: 1, Total: 2, Type: TypeRosterResp, Payload: "x"}
		if _, done, err := asm.Add(p, now); err != nil || done {
			t.Fatalf("body %d: done=%v err=%v", i, done, err)
		}
	}
	if _, _, err := asm.Add(Part{Seq: 999, Index: 1, Total: 2, Type: TypeRosterResp, Payload: "x"}, now); !errors.Is(err, ErrTooManyBodies) {
		t.Fatalf("a body past the pending cap: %v", err)
	}
	// The TTL frees the room: after it, a new body is accepted.
	if n := asm.Expire(now.Add(DefaultTTL)); n != MaxPending {
		t.Fatalf("expired %d, want %d", n, MaxPending)
	}
	if _, done, err := asm.Add(Part{Seq: 999, Index: 1, Total: 2, Type: TypeRosterResp, Payload: "x"}, now.Add(DefaultTTL)); err != nil || done {
		t.Fatalf("after the TTL: done=%v err=%v", done, err)
	}
}

func TestAssemblerMaxFrameIsTheModulesBound(t *testing.T) {
	// 64 parts of 240 bytes are exactly MaxFrame and must reassemble; one more byte is refused.
	if MaxFrame != MaxParts*240 {
		t.Fatalf("MaxFrame %d, want %d (the module's part cap times its chunk)", MaxFrame, MaxParts*240)
	}
	asm := NewAssembler()
	now := time.Unix(8000, 0)
	chunk := strings.Repeat("z", 240)
	var last Result
	for i := 1; i <= MaxParts; i++ {
		res, done, err := asm.Add(Part{Seq: 1, Index: i, Total: MaxParts, Type: TypeRosterResp, Payload: chunk}, now)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if done != (i == MaxParts) {
			t.Fatalf("part %d: done=%v", i, done)
		}
		last = res
	}
	if len(last.Body) != MaxFrame {
		t.Fatalf("body %d bytes, want %d", len(last.Body), MaxFrame)
	}
	asm = NewAssembler()
	for i := 1; i <= MaxParts; i++ {
		payload := chunk
		if i == MaxParts {
			payload = chunk + "z"
		}
		if _, _, err := asm.Add(Part{Seq: 2, Index: i, Total: MaxParts, Type: TypeRosterResp, Payload: payload}, now); err != nil {
			if i == MaxParts && errors.Is(err, ErrFrameTooBig) {
				return
			}
			t.Fatalf("oversized part %d: %v", i, err)
		}
	}
	t.Fatal("a body one byte over MaxFrame was accepted")
}

func TestParseLfg(t *testing.T) {
	st, err := ParseLfg([]string{"inside", "Ragefire Chasm", "4", "90", "5", "5", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "inside" || st.Name != "Ragefire Chasm" || st.WaitedSec != 4 || st.FormWaitSec != 90 ||
		st.Members != 5 || st.Size != 5 || !st.CanLeave || len(st.Raw) != 0 {
		t.Fatalf("status %+v", st)
	}
	// A phase the module may add later, a fifth appended field, and an idle answer with no dungeon name.
	st, err = ParseLfg([]string{"FORMING", "", "0", "90", "1", "5", "0", "future"})
	if err != nil || st.Phase != "forming" || st.CanLeave || len(st.Raw) != 1 || st.Raw[0] != "future" {
		t.Fatalf("idle/extras: %+v err=%v", st, err)
	}
	// Drift: a short answer, a phase this client does not know, numbers that cannot be read or are negative, a
	// can-leave that is not 0/1.
	for _, fields := range [][]string{
		{"inside", "Ragefire Chasm", "1", "90", "5", "5"},
		{"entering soon", "Ragefire Chasm", "1", "90", "5", "5", "0"},
		{"inside", "Ragefire Chasm", "x", "90", "5", "5", "0"},
		{"inside", "Ragefire Chasm", "-1", "90", "5", "5", "0"},
		{"inside", "Ragefire Chasm", "1", "90", "5", "5", "2"},
	} {
		if _, err := ParseLfg(fields); !errors.Is(err, ErrProtoMismatch) {
			t.Fatalf("%q accepted: %v", fields, err)
		}
	}
}
