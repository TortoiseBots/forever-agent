package world

// The spell book round (ticket #93 R5): SMSG_SEND_KNOWN_SPELLS, SMSG_LEARNED_SPELLS, SMSG_SUPERCEDED_SPELLS and
// SMSG_UNLEARNED_SPELLS keep one book and one event each, and the `spells` serve method answers that book with the
// generated data's metadata. The data is injected through Options.spellTable, so no test reads the embedded build.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// The hand-made spell data: three Frostbolt ranks (mage, line 6, levels 4/8/14), a passive mage spell and its
// names. Ids 116/205/837 are the real Frostbolt ranks of the 1.60.1 data; the passive row is this fixture's own.
const (
	r5SpellsTSV = `# forever-spells v1 source=TEST-SCHEMA
id	name	class	skill_line	rank	level	recovery_ms	category_recovery_ms	category	gcd_ms	gcd_category	mechanic	dispel	attr0
116	Frostbolt	mage	6	1	4	0	0	0	1500	133	0	1	0
205	Frostbolt	mage	6	2	8	0	0	0	1500	133	0	1	0
837	Frostbolt	mage	6	3	14	0	0	0	1500	133	0	1	0
999	Test Passive	mage	6	0	1	0	0	0	1500	133	0	1	64
`
	r5NamesTSV = `# forever-spell-names v1 source=TEST-SCHEMA
id	name
116	Frostbolt
205	Frostbolt
837	Frostbolt
999	Test Passive
`
)

func spellbookTestSession(t *testing.T) *session {
	t.Helper()
	tab, err := foreverspells.Parse(strings.NewReader(r5SpellsTSV), strings.NewReader(r5NamesTSV))
	if err != nil {
		t.Fatalf("the test data: %v", err)
	}
	return &session{report: &Report{Counts: map[string]int{}}, opts: Options{spellTable: tab}}
}

// dispatchSpellPacket runs one server packet through the session's dispatch, the path the reader feeds.
func dispatchSpellPacket(t *testing.T, s *session, core uint32, payload []byte) {
	t.Helper()
	if err := s.handle(context.Background(), event{p: worldconn.Packet{Core: core, Payload: payload}}); err != nil {
		t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

// r5KnownPayload is SMSG_SEND_KNOWN_SPELLS (SpellPackets.cpp:48-63): the login bit, both counts, both lists.
func r5KnownPayload(known, favorite []int32) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	b.WriteU32(uint32(len(known)))
	b.WriteU32(uint32(len(favorite)))
	for _, id := range known {
		b.WriteU32(uint32(id))
	}
	for _, id := range favorite {
		b.WriteU32(uint32(id))
	}
	return b.Bytes()
}

// writeR5LearnedEntry writes one LearnedSpellInfo (SpellPackets.cpp:485-504).
func writeR5LearnedEntry(b *wire.Buffer, e combat.LearnedSpell) {
	b.WriteI32(e.SpellID)
	b.WriteBit(e.Favorite)
	b.WriteBit(e.EquipSlot != nil)
	b.WriteBit(e.Superceded != nil)
	b.WriteBit(e.TraitDef != nil)
	b.FlushBits()
	if e.EquipSlot != nil {
		b.WriteI32(*e.EquipSlot)
	}
	if e.Superceded != nil {
		b.WriteI32(*e.Superceded)
	}
	if e.TraitDef != nil {
		b.WriteI32(*e.TraitDef)
	}
}

// r5LearnedPayload is SMSG_LEARNED_SPELLS (SpellPackets.cpp:506-520).
func r5LearnedPayload(entries []combat.LearnedSpell, suppress, byAura bool) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(entries)))
	b.WriteU32(0) // SpecializationID
	b.WriteI32(0) // MinActionBarSlot
	for _, e := range entries {
		writeR5LearnedEntry(b, e)
	}
	b.WriteBit(suppress)
	b.WriteBit(byAura)
	b.FlushBits()
	return b.Bytes()
}

// r5SupercededPayload is SMSG_SUPERCEDED_SPELLS (SpellPackets.cpp:522-530): the count and the same entries.
func r5SupercededPayload(entries []combat.LearnedSpell) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(entries)))
	for _, e := range entries {
		writeR5LearnedEntry(b, e)
	}
	return b.Bytes()
}

// r5UnlearnedPayload is SMSG_UNLEARNED_SPELLS (SpellPackets.cpp:607-618).
func r5UnlearnedPayload(ids []int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(ids)))
	for _, id := range ids {
		b.WriteU32(uint32(id))
	}
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// r5SpellEvents is every spell book event the session wrote, oldest first.
func r5SpellEvents(t *testing.T, s *session) []Event {
	t.Helper()
	rows, _, _, _ := s.EventLog().Since(0, EventPageMax)
	out := []Event{}
	for _, e := range rows {
		switch e.Kind {
		case events.KindSpellLearned, events.KindSpellRemoved, events.KindSpellSuperseded:
			out = append(out, e)
		}
	}
	return out
}

// TestSpellBookLearnSupersedeUnlearn: a learn that supersedes an old rank replaces it in the book and writes one
// spell_superseded event carrying the old id; an unlearn removes the id and writes spell_removed. The favourites
// list of SEND_KNOWN_SPELLS is not the known list.
func TestSpellBookLearnSupersedeUnlearn(t *testing.T) {
	s := spellbookTestSession(t)
	dispatchSpellPacket(t, s, wire.SMSGSendKnownSpells, r5KnownPayload([]int32{116, 205}, []int32{9999}))
	if got := s.spellbook().ids(); !slices.Equal(got, []int32{116, 205}) {
		t.Fatalf("after SEND_KNOWN_SPELLS the book is %v, want [116 205] (the favourites list is not known)", got)
	}
	dispatchSpellPacket(t, s, wire.SMSGLearnedSpells,
		r5LearnedPayload([]combat.LearnedSpell{{SpellID: 837, Superceded: new(int32(205))}}, false, false))
	if got := s.spellbook().ids(); !slices.Equal(got, []int32{116, 837}) {
		t.Fatalf("after the superseding learn the book is %v, want [116 837]; a kept old rank is the supersede mutation", got)
	}
	if !slices.Equal(s.known, []int32{116, 837}) || !slices.Equal(s.report.KnownSpells, []int32{116, 837}) {
		t.Fatalf("the known list is %v and the report's %v, want [116 837]", s.known, s.report.KnownSpells)
	}
	evs := r5SpellEvents(t, s)
	if len(evs) != 1 {
		t.Fatalf("%d spell events after one superseding learn, want only the superseded one", len(evs))
	}
	if e := evs[0]; e.Kind != events.KindSpellSuperseded || e.SpellID != 837 || e.Text != "Frostbolt" {
		t.Fatalf("the event is %s %d %q, want spell_superseded 837 Frostbolt", e.Kind, e.SpellID, e.Text)
	}
	if e := evs[0]; e.Data == nil || e.Data["was"] != int32(205) {
		t.Fatalf("the superseded event says %v, want was 205", e.Data)
	}

	dispatchSpellPacket(t, s, wire.SMSGUnlearnedSpells, r5UnlearnedPayload([]int32{116}))
	if got := s.spellbook().ids(); !slices.Equal(got, []int32{837}) {
		t.Fatalf("after the unlearn the book is %v, want [837]", got)
	}
	if !slices.Equal(s.known, []int32{837}) {
		t.Fatalf("the known list is %v, want [837]", s.known)
	}
	evs = r5SpellEvents(t, s)
	if len(evs) != 2 || evs[1].Kind != events.KindSpellRemoved || evs[1].SpellID != 116 || evs[1].Text != "Frostbolt" {
		t.Fatalf("the unlearn event is %+v", evs[1:])
	}
	rows, err := s.serveSpells(serveSpellsParams{Highest: true})
	if err != nil {
		t.Fatalf("serveSpells: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the book serves %+v, want only Frostbolt 837", rows)
	}
	if r := rows[0]; r.SpellID != 837 || r.Rank != 3 || r.GCDMS != 1500 {
		t.Fatalf("the served rank is %+v, want 837 rank 3 with gcd 1500", r)
	}
}

// TestSpellBookPlainLearnAndSupercededPacket: a learn without a superseded id writes spell_learned, and
// SMSG_SUPERCEDED_SPELLS replaces one id with another and writes the same event.
func TestSpellBookPlainLearnAndSupercededPacket(t *testing.T) {
	s := spellbookTestSession(t)
	dispatchSpellPacket(t, s, wire.SMSGSendKnownSpells, r5KnownPayload([]int32{116}, nil))
	dispatchSpellPacket(t, s, wire.SMSGLearnedSpells, r5LearnedPayload([]combat.LearnedSpell{{SpellID: 205}}, true, true))
	if got := s.spellbook().ids(); !slices.Equal(got, []int32{116, 205}) {
		t.Fatalf("the book is %v, want [116 205]", got)
	}
	evs := r5SpellEvents(t, s)
	if len(evs) != 1 || evs[0].Kind != events.KindSpellLearned || evs[0].SpellID != 205 || evs[0].Text != "Frostbolt" {
		t.Fatalf("the learn event is %+v", evs)
	}

	dispatchSpellPacket(t, s, wire.SMSGSupercededSpells,
		r5SupercededPayload([]combat.LearnedSpell{{SpellID: 837, Superceded: new(int32(205))}}))
	if got := s.spellbook().ids(); !slices.Equal(got, []int32{116, 837}) {
		t.Fatalf("the book is %v, want [116 837]", got)
	}
	evs = r5SpellEvents(t, s)
	if len(evs) != 2 || evs[1].Kind != events.KindSpellSuperseded || evs[1].SpellID != 837 || evs[1].Data["was"] != int32(205) {
		t.Fatalf("the superseded event is %+v", evs[1:])
	}
}

// TestSpellsServeHighestAmongKnown: highest is the highest rank among the KNOWN ids of the name, never a table
// rank the character does not know.
func TestSpellsServeHighestAmongKnown(t *testing.T) {
	s := spellbookTestSession(t)
	dispatchSpellPacket(t, s, wire.SMSGSendKnownSpells, r5KnownPayload([]int32{116, 205}, nil))
	rows, err := s.serveSpells(serveSpellsParams{Highest: true})
	if err != nil {
		t.Fatalf("serveSpells: %v", err)
	}
	if len(rows) != 1 || rows[0].SpellID != 205 || rows[0].Rank != 2 {
		t.Fatalf("highest of the known ranks is %+v, want 205 rank 2 (837 is a table rank the character lacks)", rows)
	}

	dispatchSpellPacket(t, s, wire.SMSGSendKnownSpells, r5KnownPayload([]int32{116, 205, 837}, nil))
	all, err := s.serveSpells(serveSpellsParams{})
	if err != nil {
		t.Fatalf("serveSpells: %v", err)
	}
	if len(all) != 3 || all[0].SpellID != 116 || all[1].SpellID != 205 || all[2].SpellID != 837 {
		t.Fatalf("the rows are %+v, want the three ranks by rank", all)
	}
	if all[0].Highest || all[1].Highest || !all[2].Highest {
		t.Fatalf("highest flags %v/%v/%v, want only 837", all[0].Highest, all[1].Highest, all[2].Highest)
	}
	if top := all[2]; !top.Meta || top.Name != "Frostbolt" || top.Class != "mage" || top.GCDMS != 1500 || top.Level != 14 {
		t.Fatalf("the highest row is %+v", top)
	}
	only, _ := s.serveSpells(serveSpellsParams{Highest: true})
	if len(only) != 1 || only[0].SpellID != 837 || only[0].GCDMS != 1500 {
		t.Fatalf("highest is %+v, want only 837 with gcd 1500", only)
	}
}

// TestSpellsServeFilters: the spell id and name filters, the passive rule and a spell the table does not hold.
func TestSpellsServeFilters(t *testing.T) {
	s := spellbookTestSession(t)
	dispatchSpellPacket(t, s, wire.SMSGSendKnownSpells, r5KnownPayload([]int32{116, 999, 4242}, nil))

	rows, err := s.serveSpells(serveSpellsParams{})
	if err != nil {
		t.Fatalf("serveSpells: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows %+v, want 116 and 4242 (the passive spell is hidden)", len(rows), rows)
	}
	for _, r := range rows {
		if r.SpellID == 999 {
			t.Fatalf("the passive spell is listed without passive:true: %+v", r)
		}
	}
	withPassive, _ := s.serveSpells(serveSpellsParams{Passive: true})
	if len(withPassive) != 3 {
		t.Fatalf("%d rows with passive:true, want 3", len(withPassive))
	}
	var passive *ServeSpellRow
	for i := range withPassive {
		if withPassive[i].SpellID == 999 {
			passive = &withPassive[i]
		}
	}
	if passive == nil || !passive.Passive {
		t.Fatalf("999 is not listed as passive: %+v", withPassive)
	}

	named, _ := s.serveSpells(serveSpellsParams{Name: "frost"})
	if len(named) != 1 || named[0].SpellID != 116 {
		t.Fatalf("name filter frost: %+v", named)
	}
	byID, _ := s.serveSpells(serveSpellsParams{SpellID: 4242})
	if len(byID) != 1 {
		t.Fatalf("spell_id filter: %+v", byID)
	}
	if r := byID[0]; r.Meta || r.Name != "" || r.Rank != 0 || r.Class != "" {
		t.Fatalf("a spell the table lacks is %+v, want meta false and no name", r)
	}
}

// TestSpellsMethodTable: `spells` is served, not listed as not ported.
func TestSpellsMethodTable(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	if !slices.Contains(ported, "spells") {
		t.Fatal("spells is not a served method")
	}
	if slices.Contains(notPorted, "spells") {
		t.Fatal("spells is still on the not-ported list")
	}
}
