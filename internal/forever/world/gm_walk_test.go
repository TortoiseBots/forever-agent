package world

// The mark tap over hand-made chat lines: whispers count, say from the GM counts, other chatter and other
// senders are unknown, marks with no position are ignored.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/gmwalk"
)

func TestGMWalkTapMarks(t *testing.T) {
	s := &session{}
	rec := &gmwalk.Recorder{}
	rec.AddSample(gmwalk.Sample{Map: 1, X: 5, Y: 5, Z: 5})
	o := (&GMWalkOptions{Follow: "Gm", MarksFrom: GMWalkFromGM}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeWhisper, SenderName: "Gm", Text: "point gate"})
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeSay, SenderName: "Gm", Text: "zone lava 25"})
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeWhisper, SenderName: "Gm", Text: "hello there"})
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeWhisper, SenderName: "Other", Text: "point far"})
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeParty, SenderName: "Gm", Text: "point nope"})
	// A party line is not a whisper or a nearby say: it never reaches the mark parser and is not counted.
	if tap.marks != 2 || tap.unknown != 2 {
		t.Fatalf("marks %d unknown %d, want 2 and 2", tap.marks, tap.unknown)
	}
	data := rec.Build(0)
	if len(data.Points) != 1 || len(data.Zones) != 1 {
		t.Fatalf("data: %+v", data)
	}
}

func TestGMWalkTapAnySender(t *testing.T) {
	s := &session{}
	rec := &gmwalk.Recorder{}
	rec.AddSample(gmwalk.Sample{Map: 1})
	o := (&GMWalkOptions{Follow: "Gm", MarksFrom: GMWalkFromAny}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeWhisper, SenderName: "Other", Text: "point far"})
	if tap.marks != 1 {
		t.Fatalf("any-sender whisper must mark: %+v", tap)
	}
}

func TestGMWalkTapCaseInsensitiveName(t *testing.T) {
	s := &session{}
	rec := &gmwalk.Recorder{}
	rec.AddSample(gmwalk.Sample{Map: 1})
	o := (&GMWalkOptions{Follow: "Gm", MarksFrom: GMWalkFromGM}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeWhisper, SenderName: "gM", Text: "split"})
	if tap.marks != 1 {
		t.Fatalf("sender match must be case-insensitive: %+v", tap)
	}
}

func TestGMWalkTapNoPosition(t *testing.T) {
	s := &session{}
	rec := &gmwalk.Recorder{}
	o := (&GMWalkOptions{Follow: "Gm"}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, chat.Line{Type: chat.TypeWhisper, SenderName: "Gm", Text: "point early"})
	if tap.ignored != 1 || tap.marks != 0 {
		t.Fatalf("mark with no position: %+v", tap)
	}
}

func TestGMWalkOptionsDefaults(t *testing.T) {
	o := (&GMWalkOptions{Follow: "Gm"}).withDefaults()
	if o.MarksFrom != GMWalkFromGM || o.Sample != GMWalkSample || o.ToleranceYd != GMWalkTolerance || o.FollowYd != GMWalkFollowYd {
		t.Fatalf("defaults: %+v", o)
	}
}
