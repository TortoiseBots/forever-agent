package pet

// Hand-made bodies from the core's writers: every field is written in the order PetPackets.cpp writes it, and each
// fixture says which writer it reproduces. The 18-button bar is the Classic 1.60 tail (PetPackets.cpp:33-41); a
// fixture with the ten buttons of another build is here on purpose, to prove the parser refuses it instead of
// reading the count words as buttons.

import (
	"bytes"
	"math"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	testPet    = wire.GUID{Low: 0x00000000000A0B0C, High: 0x0F00000000000000}
	testTarget = wire.GUID{Low: 0x00000000000D0E0F, High: 0x0F00000000000000}
)

// spellsSpec is one SMSG_PET_SPELLS_MESSAGE body as written by PetPackets.cpp:23-66.
type spellsSpec struct {
	pet                  wire.GUID
	family, special      uint16
	limit                uint32
	command, flag, react uint8
	buttons              []uint32 // written in order; a real body has 18
	actions              []uint32
	cooldowns            []Cooldown
	history              []History
	// counts, when not nil, replaces the three count words (the over-bound and short-body cases).
	counts *[3]uint32
}

func spellsBody(s spellsSpec) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, s.pet)
	b.WriteU16(s.family)
	b.WriteU16(s.special)
	b.WriteU32(s.limit)
	b.WriteU8(s.command)
	b.WriteU8(s.flag)
	b.WriteU8(s.react)
	for _, v := range s.buttons {
		b.WriteU32(v)
	}
	if s.counts != nil {
		b.WriteU32((*s.counts)[0])
		b.WriteU32((*s.counts)[1])
		b.WriteU32((*s.counts)[2])
		return b.Bytes()
	}
	b.WriteU32(uint32(len(s.actions)))
	b.WriteU32(uint32(len(s.cooldowns)))
	b.WriteU32(uint32(len(s.history)))
	for _, a := range s.actions {
		b.WriteU32(a)
	}
	for _, c := range s.cooldowns {
		b.WriteI32(c.SpellID)
		b.WriteI32(c.DurationMS)
		b.WriteI32(c.CategoryMS)
		b.WriteFloat32(c.ModRate)
		b.WriteU16(c.Category)
	}
	for _, h := range s.history {
		b.WriteI32(h.Category)
		b.WriteI32(h.RecoveryMS)
		b.WriteFloat32(h.ChargeModRate)
		b.WriteU8(uint8(h.Consumed))
	}
	return b.Bytes()
}

// fullBar is what the core writes: the ten action buttons, then buttons 0-3, the fixed 0x16000000 and buttons 7-9
// (PetPackets.cpp:36-41).
func fullBar(bar [BarSlots]uint32) []uint32 {
	out := make([]uint32, 0, BarSlots+BarExtra)
	out = append(out, bar[:]...)
	out = append(out, bar[0], bar[1], bar[2], bar[3], 0x16000000, bar[7], bar[8], bar[9])
	return out
}

func TestParseSpellsReadsEighteenButtons(t *testing.T) {
	var bar [BarSlots]uint32
	for i := range bar {
		bar[i] = Button(uint32(100+i), ActPassive)
	}
	cd := Cooldown{SpellID: 172, DurationMS: 12345, CategoryMS: 6789, ModRate: 1.5, Category: 8}
	hist := History{Category: 4, RecoveryMS: 5000, ChargeModRate: 2, Consumed: -1}
	body := spellsBody(spellsSpec{
		pet: testPet, family: 1, limit: 30000, command: uint8(CmdFollow), react: ReactDefensive,
		buttons: fullBar(bar), actions: []uint32{0x01020304, 0x05060708}, cooldowns: []Cooldown{cd}, history: []History{hist},
	})
	s, err := ParseSpells(body)
	if err != nil {
		t.Fatalf("the 18-button body: %v", err)
	}
	if s.Cleared {
		t.Fatal("a body with a pet GUID is not a cleared bar")
	}
	if s.Pet != testPet || s.Family != 1 || s.TimeLimit != 30000 {
		t.Fatalf("pet %v family %d limit %d", s.Pet, s.Family, s.TimeLimit)
	}
	if s.Command != uint8(CmdFollow) || s.React != ReactDefensive {
		t.Fatalf("command %d react %d", s.Command, s.React)
	}
	if s.Bar != bar {
		t.Fatalf("bar %08X, want %08X", s.Bar, bar)
	}
	if s.Bar[3] != Button(103, ActPassive) {
		t.Fatalf("Bar[3] = %08X, want the first spell button", s.Bar[3])
	}
	if s.Extra[4] != 0x16000000 {
		t.Fatalf("Extra[4] = %08X, want 0x16000000", s.Extra[4])
	}
	if len(s.Actions) != 2 || s.Actions[0] != 0x01020304 || s.Actions[1] != 0x05060708 {
		t.Fatalf("actions %08X", s.Actions)
	}
	if len(s.Cooldowns) != 1 || s.Cooldowns[0] != cd {
		t.Fatalf("cooldowns %+v, want %+v", s.Cooldowns, cd)
	}
	if math.Abs(float64(s.Cooldowns[0].ModRate-1.5)) > 1e-6 {
		t.Fatalf("cooldown mod rate %v", s.Cooldowns[0].ModRate)
	}
	if len(s.History) != 1 || s.History[0] != hist {
		t.Fatalf("history %+v, want %+v", s.History, hist)
	}
}

// TestParseSpellsRejectsTenButtonBody: the same body with only the ten action buttons. The eight missing words are
// the count words, so a parser that does not read 18 buttons consumes the actions as buttons and reads a count out
// of the cooldown floats; it must not accept the body silently.
func TestParseSpellsRejectsTenButtonBody(t *testing.T) {
	var bar [BarSlots]uint32
	for i := range bar {
		bar[i] = Button(uint32(100+i), ActPassive)
	}
	body := spellsBody(spellsSpec{
		pet: testPet, family: 1, limit: 30000, command: uint8(CmdFollow), react: ReactDefensive,
		buttons: bar[:], actions: []uint32{0x01020304, 0x05060708},
		cooldowns: []Cooldown{{SpellID: 172, DurationMS: 12345, CategoryMS: 6789, ModRate: 1.5, Category: 8}},
		history:   []History{{Category: 4, RecoveryMS: 5000, ChargeModRate: 2, Consumed: -1}},
	})
	if _, err := ParseSpells(body); err == nil {
		t.Fatal("a ten-button body parsed: the counts were read as buttons")
	}
}

// TestParseSpellsClearedBar: Player::SendRemoveControlBar sends this same body with an empty GUID (Player.cpp, the
// 0.1.39 core), and a zero pet is the signal, not a missing bar.
func TestParseSpellsClearedBar(t *testing.T) {
	body := spellsBody(spellsSpec{buttons: make([]uint32, BarSlots+BarExtra)})
	s, err := ParseSpells(body)
	if err != nil {
		t.Fatalf("the cleared body: %v", err)
	}
	if !s.Cleared {
		t.Fatal("the empty GUID body was not reported as cleared")
	}
	if len(s.Actions) != 0 || len(s.Cooldowns) != 0 || len(s.History) != 0 {
		t.Fatalf("the cleared body carries %d/%d/%d entries", len(s.Actions), len(s.Cooldowns), len(s.History))
	}
}

func modeBody(pet wire.GUID, command, flag, react uint8) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, pet)
	b.WriteU8(command)
	b.WriteU8(flag)
	b.WriteU8(react)
	return b.Bytes()
}

func castFailedBody(castID wire.GUID, spell, reason, arg1, arg2 int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, castID)
	b.WriteI32(spell)
	b.WriteI32(reason)
	b.WriteI32(arg1)
	b.WriteI32(arg2)
	return b.Bytes()
}

func feedbackBody(response, spell int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(response)
	b.WriteI32(spell)
	return b.Bytes()
}

// TestParsersRequireExactBodies: every parser refuses one trailing byte and one missing byte.
func TestParsersRequireExactBodies(t *testing.T) {
	var bar [BarSlots]uint32
	valid := map[string]struct {
		parse func([]byte) error
		body  []byte
	}{
		"spells": {
			func(p []byte) error { _, err := ParseSpells(p); return err },
			spellsBody(spellsSpec{pet: testPet, buttons: fullBar(bar)}),
		},
		"mode": {
			func(p []byte) error { _, err := ParseMode(p); return err },
			modeBody(testPet, uint8(CmdStay), 0, ReactPassive),
		},
		"cast failed": {
			func(p []byte) error { _, err := ParseCastFailed(p); return err },
			castFailedBody(testPet, 172, 41, 0, 0),
		},
		"feedback": {
			func(p []byte) error { _, err := ParseFeedback(p); return err },
			feedbackBody(3, 172),
		},
	}
	for name, c := range valid {
		if err := c.parse(c.body); err != nil {
			t.Fatalf("%s: the valid body does not parse: %v", name, err)
		}
		if err := c.parse(append(append([]byte{}, c.body...), 0x00)); err == nil {
			t.Errorf("%s: one trailing byte was accepted", name)
		}
		if err := c.parse(c.body[:len(c.body)-1]); err == nil {
			t.Errorf("%s: one missing byte was accepted", name)
		}
	}
	// The three count words are the body's own sizes; a count past the bound is refused before anything is read.
	over := [3]uint32{MaxActions + 1, 0, 0}
	body := spellsBody(spellsSpec{pet: testPet, buttons: fullBar(bar), counts: &over})
	if _, err := ParseSpells(body); err == nil {
		t.Errorf("a body naming %d actions was accepted", MaxActions+1)
	}
}

// TestParseModeFieldOrder: SMSG_PET_MODE writes command, flag, react (PetPackets.cpp:239-247). A body with
// command=1 and react=2 must not come back as react=1.
func TestParseModeFieldOrder(t *testing.T) {
	m, err := ParseMode(modeBody(testPet, uint8(CmdFollow), 0, ReactAggressive))
	if err != nil {
		t.Fatalf("the mode body: %v", err)
	}
	if m.Command != uint8(CmdFollow) || m.Flag != 0 || m.React != ReactAggressive {
		t.Fatalf("command %d flag %d react %d, want 1/0/2", m.Command, m.Flag, m.React)
	}
	if m.Pet != testPet {
		t.Fatalf("pet %v", m.Pet)
	}
}

func TestBuilders(t *testing.T) {
	button := Button(CmdAttack, ActCommand)
	if button != 0x07000002 || ButtonAction(button) != CmdAttack || ButtonType(button) != ActCommand {
		t.Fatalf("Button(%d, %d) = %08X", CmdAttack, ActCommand, button)
	}

	action := BuildAction(testPet, button, testTarget)
	head := wire.NewWriter()
	wire.WritePackedGUID(head, testPet)
	head.WriteU32(0x07000002) // the action word is little-endian: 02 00 00 07
	wire.WritePackedGUID(head, testTarget)
	want := append(head.Bytes(), make([]byte, 12)...) // ActionPosition 0,0,0 (PetPackets.cpp:140-148)
	if !bytes.Equal(action, want) {
		t.Fatalf("build action = % X\nwant              % X", action, want)
	}

	for _, tc := range []struct {
		on   bool
		bit  byte
		name string
	}{{true, 0x80, "on"}, {false, 0x00, "off"}} {
		body := BuildSpellAutocast(testPet, 172, tc.on)
		head := wire.NewWriter()
		wire.WritePackedGUID(head, testPet)
		head.WriteU32(172)
		want := append(head.Bytes(), tc.bit) // Bits<1> then FlushBits: the bit is the MSB of its own byte
		if !bytes.Equal(body, want) {
			t.Fatalf("autocast %s = % X\nwant % X", tc.name, body, want)
		}
	}

	stop := BuildStopAttack(testPet)
	stopHead := wire.NewWriter()
	wire.WritePackedGUID(stopHead, testPet)
	if !bytes.Equal(stop, stopHead.Bytes()) {
		t.Fatalf("stop attack = % X, want the pet GUID alone", stop)
	}
}

// TestNames: the feedback names are PetDefines.h:102-109; the react and command names are UnitDefines.h:441-467.
func TestNames(t *testing.T) {
	for v, want := range map[uint8]string{
		ReactPassive: "passive", ReactDefensive: "defensive", ReactAggressive: "aggressive", ReactAssist: "assist",
	} {
		if got := ReactName(v); got != want {
			t.Errorf("ReactName(%d) = %q, want %q", v, got, want)
		}
	}
	for v, want := range map[uint8]string{
		uint8(CmdStay): "stay", uint8(CmdFollow): "follow", uint8(CmdAttack): "attack",
		uint8(CmdAbandon): "abandon", uint8(CmdMoveTo): "move_to",
	} {
		if got := CommandName(v); got != want {
			t.Errorf("CommandName(%d) = %q, want %q", v, got, want)
		}
	}
	for v, want := range map[int32]string{
		0: "none", 1: "dead", 2: "no_target", 3: "invalid_target", 4: "no_path",
	} {
		if got := FeedbackName(v); got != want {
			t.Errorf("FeedbackName(%d) = %q, want %q", v, got, want)
		}
	}
	if got := ReactName(9); got != "react_9" {
		t.Errorf("ReactName(9) = %q", got)
	}
	if got := CommandName(9); got != "command_9" {
		t.Errorf("CommandName(9) = %q", got)
	}
	if got := FeedbackName(9); got != "feedback_9" {
		t.Errorf("FeedbackName(9) = %q", got)
	}
}
