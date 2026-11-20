package world

// Round A1's serve methods: the session's combat log through `events`, a unit's auras, status's mono_ms and the GM
// hands that act on one unit (select, wait for the server's echo, then one line). The fake sends the packets and
// scripts its answers; the rows are the trials runner's raw row shape.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// trialsEventRow and trialsEventPage are local copies of the events result: the test decodes the wire JSON, so it
// pins the field names an outside caller (the trials runner) reads.
type trialsEventRow struct {
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"`
	MonoMS  int64  `json:"mono_ms"`
	Source  actor  `json:"source"`
	Target  actor  `json:"target"`
	SpellID int32  `json:"spell_id"`
	Amount  int32  `json:"amount"`
	Reason  string `json:"reason"`
}

type actor struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

type trialsEventPage struct {
	Events  []trialsEventRow `json:"events"`
	Next    int              `json:"next"`
	Oldest  int              `json:"oldest"`
	Dropped int              `json:"dropped"`
}

// readEvents posts events and decodes one page.
func (a *serveAgent) readEvents(t *testing.T, since, limit, waitMS int) trialsEventPage {
	t.Helper()
	var page trialsEventPage
	json.Unmarshal(a.mustCall(t, "events", map[string]any{"since": since, "limit": limit, "wait_ms": waitMS}), &page)
	return page
}

// waitEvents polls events until it has want rows or the deadline passes.
func (a *serveAgent) waitEvents(t *testing.T, since, want int) trialsEventPage {
	t.Helper()
	rows := []trialsEventRow{}
	next := since
	deadline := time.Now().Add(10 * time.Second)
	for len(rows) < want && time.Now().Before(deadline) {
		page := a.readEvents(t, next, 1000, 1000)
		if len(page.Events) == 0 {
			continue
		}
		rows = append(rows, page.Events...)
		next = page.Next
	}
	if len(rows) < want {
		t.Fatalf("events answered %d rows, want %d", len(rows), want)
	}
	return trialsEventPage{Events: rows, Next: next}
}

func TestServeEventsKinds(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveEventsBurst = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	units := waitServeUnits(t, a)
	creature := units[0].GUID

	var st struct {
		GUID   GUID  `json:"guid"`
		MonoMS int64 `json:"mono_ms"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if st.MonoMS <= 0 {
		t.Fatalf("status carries no mono_ms: %+v", st)
	}

	page := a.waitEvents(t, 0, 10)
	rows := page.Events
	wantKinds := []string{
		"spell_start", "spell_go", "spell_fail", "melee", "spell_damage",
		"periodic", "heal", "aura_applied", "aura_removed", "death",
	}
	if len(rows) != len(wantKinds) {
		t.Fatalf("%d rows, want %d: %+v", len(rows), len(wantKinds), rows)
	}
	for i, want := range wantKinds {
		if rows[i].Kind != want {
			t.Fatalf("row %d is %q, want %q (rows %+v)", i, rows[i].Kind, want, rows)
		}
		if rows[i].Seq != i+1 {
			t.Fatalf("row %d has seq %d, want %d", i, rows[i].Seq, i+1)
		}
		if rows[i].MonoMS <= 0 {
			t.Fatalf("row %d (%s) carries no mono_ms", i, rows[i].Kind)
		}
	}
	self := st.GUID.String()
	creatureText := creature.String()
	// The caster of the cast rows and the blow of the melee row are the creature; the target is this character.
	for _, i := range []int{0, 1, 3} {
		if rows[i].Source.GUID != creatureText {
			t.Fatalf("row %s source %q, want the creature %q", rows[i].Kind, rows[i].Source.GUID, creatureText)
		}
		if rows[i].Target.GUID != self {
			t.Fatalf("row %s target %q, want this character %q", rows[i].Kind, rows[i].Target.GUID, self)
		}
	}
	if rows[0].SpellID != 5019 || rows[1].SpellID != 5019 || rows[2].SpellID != 5021 {
		t.Fatalf("cast rows name spells %d/%d/%d", rows[0].SpellID, rows[1].SpellID, rows[2].SpellID)
	}
	if rows[2].Reason == "" {
		t.Fatalf("the spell_fail row has no reason: %+v", rows[2])
	}
	if rows[3].Amount != 12 || rows[4].Amount != 34 || rows[5].Amount != 9 || rows[6].Amount != 40 {
		t.Fatalf("amounts %d/%d/%d/%d, want 12/34/9/40", rows[3].Amount, rows[4].Amount, rows[5].Amount, rows[6].Amount)
	}
	if rows[7].SpellID != 845 || rows[8].SpellID != 845 {
		t.Fatalf("aura rows name %d/%d, want 845", rows[7].SpellID, rows[8].SpellID)
	}
	if rows[9].Target.GUID != creatureText {
		t.Fatalf("the death row's target is %q, want the creature %q", rows[9].Target.GUID, creatureText)
	}
	if page.Next != 10 {
		t.Fatalf("next %d, want 10", page.Next)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeEventsAcrossReconnect(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	f.serveEventsOne = true
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	first := a.waitEvents(t, 0, 1)
	if first.Events[0].Seq != 1 {
		t.Fatalf("the first session's row has seq %d, want 1", first.Events[0].Seq)
	}
	cursor := first.Next

	dropServingSocket(t, f)
	// `events` is a read: it answers from the ring while the link is down, never a typed action error.
	if page := a.readEvents(t, cursor, 1000, 200); page.Next < cursor {
		t.Fatalf("events went back to %d while reconnecting, cursor %d", page.Next, cursor)
	}
	waitServeState(t, a, "online", steps)
	deadline := time.Now().Add(15 * time.Second)
	for f.serveSessionCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.serveSessionCount() < 2 {
		t.Fatalf("the fake never ran a second session\n%s", steps.joined())
	}
	waitServeState(t, a, "online", steps)

	// The ring outlives the drop: the next row is above the cursor, never a reused seq.
	second := a.waitEvents(t, cursor, 1)
	if got := second.Events[0].Seq; got <= cursor {
		t.Fatalf("the row after the reconnect has seq %d, want above %d", got, cursor)
	}
	if got := second.Events[0].Seq; got != cursor+1 {
		t.Fatalf("the row after the reconnect has seq %d, want %d", got, cursor+1)
	}
	if second.Dropped != 0 {
		t.Fatalf("the reconnect page dropped %d rows", second.Dropped)
	}
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown after the re-entry: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
}

func TestServeUnitAuras(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveAuraBurst = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	units := waitServeUnits(t, a)
	var st struct {
		GUID GUID `json:"guid"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)

	var row struct {
		GUID  GUID `json:"guid"`
		Auras []struct {
			SpellID     int32 `json:"spell_id"`
			Caster      GUID  `json:"caster"`
			Stacks      int32 `json:"stacks"`
			RemainingMS int64 `json:"remaining_ms"`
		} `json:"auras"`
	}
	json.Unmarshal(a.mustCall(t, "unit", map[string]any{"guid": units[0].GUID}), &row)
	if len(row.Auras) != 1 {
		t.Fatalf("the unit has %d auras, want 1: %+v\n%s", len(row.Auras), row, steps.joined())
	}
	au := row.Auras[0]
	if au.SpellID != 845 || au.Stacks != 3 {
		t.Fatalf("aura %+v, want spell 845 with 3 stacks", au)
	}
	if au.Caster != st.GUID {
		t.Fatalf("aura caster %s, want this character %s", au.Caster, st.GUID)
	}
	// The record said 5 s were left; the read is a moment later, so it is below that and still positive.
	if au.RemainingMS <= 0 || au.RemainingMS > 5000 {
		t.Fatalf("aura remaining %d ms, want (0, 5000]", au.RemainingMS)
	}
	a.stop(t)
	f.wait(t)
}

func TestAuraDiffRows(t *testing.T) {
	// The dispatch's own rule: a gained aura is one row, a refresh is none, a removal is one.
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	unit := wire.GUID{Low: 0x11, High: 0x0100000000000000}
	s.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{
		GUID: unit, ObjectType: objects.TypeUnit, ValuesComplete: true,
		Info: objects.Info{Type: objects.TypeUnit, HasObject: true, HasUnit: true},
	}}}, time.Unix(0, 0))
	send := func(p []byte) {
		t.Helper()
		s.handleCombat(worldconn.Packet{Core: wire.SMSGAuraUpdate, Payload: p})
	}

	send(buildAuraUpdate(unit, 0, 100))
	rows, _, _, _ := s.CombatLog().Since(0, 10)
	if len(rows) != 1 || rows[0].Kind != "aura_applied" || rows[0].SpellID != 100 {
		t.Fatalf("the gained aura wrote %+v", rows)
	}
	if rows[0].Target.GUID != unit.String() {
		t.Fatalf("the aura row's target is %q, want %q", rows[0].Target.GUID, unit.String())
	}

	// A refresh carries the slot's data again: the aura was already on, so no row.
	send(buildAuraUpdate(unit, 0, 100))
	if rows, _, _, _ := s.CombatLog().Since(1, 10); len(rows) != 0 {
		t.Fatalf("a refresh wrote %+v", rows)
	}

	send(buildAuraRemove(unit, 0))
	rows, _, _, _ = s.CombatLog().Since(1, 10)
	if len(rows) != 1 || rows[0].Kind != "aura_removed" || rows[0].SpellID != 100 {
		t.Fatalf("the removal wrote %+v", rows)
	}
}

func TestServeGMKillSelectsFirst(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	units := waitServeUnits(t, a)
	creature := units[0].GUID
	var st struct {
		GUID GUID `json:"guid"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	// The bench's own registration: the creature and this session's character are the trial's units.
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature, st.GUID}})

	// An unknown guid sends nothing at all, not even the GM guard's probe.
	a.mustFail(t, "gm_kill", map[string]any{"guid": "0x0000000000000000000000000000DEAD"}, -32003, "not in view")
	f.mu.Lock()
	if len(f.serveSelections) != 0 || len(f.serveGMLines) != 0 {
		t.Fatalf("an unknown guid sent %v and %v", f.serveSelections, f.serveGMLines)
	}
	f.mu.Unlock()

	var killed ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_kill", map[string]any{"guid": creature}), &killed)
	if killed.Sent != ".die" {
		t.Fatalf("gm_kill sent %q, want .die", killed.Sent)
	}
	var hp ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_set_hp", map[string]any{"guid": st.GUID, "hp": 1234}), &hp)
	if hp.Sent != ".modify hp 1234" {
		t.Fatalf("gm_set_hp sent %q", hp.Sent)
	}
	var aura ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_aura", map[string]any{"guid": creature, "spell": 845}), &aura)
	if aura.Sent != ".aura 845" {
		t.Fatalf("gm_aura sent %q", aura.Sent)
	}
	var unaura ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_aura", map[string]any{"guid": creature, "spell": 845, "remove": true}), &unaura)
	if unaura.Sent != ".unaura 845" {
		t.Fatalf("gm_aura remove sent %q", unaura.Sent)
	}

	a.mustFail(t, "gm_set_hp", map[string]any{"guid": st.GUID}, -32003, "needs hp")
	a.mustFail(t, "gm_aura", map[string]any{"guid": creature, "spell": 0}, -32003, "positive spell")
	// A player is not killed by this hand: that line kills a character.
	a.mustFail(t, "gm_kill", map[string]any{"guid": st.GUID}, -32003, "is a player")

	f.mu.Lock()
	selections := append([]wire.GUID(nil), f.serveSelections...)
	order := append([]string(nil), f.serveOrder...)
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if len(selections) != 4 {
		t.Fatalf("%d selections, want 4 (kill, hp, aura, unaura): %v", len(selections), selections)
	}
	if selections[0] != wire.GUID(creature) || selections[1] != wire.GUID(st.GUID) || selections[2] != wire.GUID(creature) || selections[3] != wire.GUID(creature) {
		t.Fatalf("selections %v", selections)
	}
	// The .die line is sent after the selection packet that named the creature and before the next selection
	// (the guard's own .gm probe may sit between the two, and does).
	firstSelect, nextSelect, dieSay := -1, -1, -1
	say := 0
	for i, kind := range order {
		switch kind {
		case "select":
			if firstSelect < 0 {
				firstSelect = i
			} else if nextSelect < 0 {
				nextSelect = i
			}
		case "say":
			if say < len(lines) && lines[say] == ".die" {
				dieSay = i
			}
			say++
		}
	}
	if firstSelect < 0 || dieSay < 0 {
		t.Fatalf("no select or no .die in the order %v (lines %v)", order, lines)
	}
	if dieSay < firstSelect || (nextSelect >= 0 && dieSay > nextSelect) {
		t.Fatalf("the order is %v (lines %v); .die must follow the creature's selection", order, lines)
	}
	if !strings.Contains(strings.Join(lines, "|"), ".die") {
		t.Fatalf("the fake never saw .die: %v", lines)
	}
	a.stop(t)
	f.wait(t)
}

// ---- server packet builders (the writers cited on each) ----

// buildUnitValuesUpdate wraps one values body in SMSG_UPDATE_OBJECT's single-block shape.
func buildUnitValuesUpdate(guid wire.GUID, body []byte) []byte {
	block := wire.NewWriter()
	block.WriteU8(0) // UPDATETYPE_VALUES
	wire.WritePackedGUID(block, guid)
	block.WriteU32(uint32(len(body)))
	block.WriteBytes(body)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// buildTargetValues is the character's own target field (retail bit 22) set to target: the server's echo of a
// selection.
func buildTargetValues(guid, target wire.GUID) []byte {
	return buildUnitValuesUpdate(guid, unitValuesBody(22, func(v *wire.Buffer) {
		wire.WritePackedGUID(v, target)
	}))
}

// buildAuraUpdateFrom is SMSG_AURA_UPDATE with one aura that carries a caster, a stack count and a duration
// (AuraUpdate::Write, SpellPackets.cpp:154-164; AuraDataInfo at :94-140).
func buildAuraUpdateFrom(unit, caster wire.GUID, slot uint16, spellID int32, stacks uint8, durationMS, remainingMS uint32) []byte {
	b := wire.NewWriter()
	b.WriteBit(false) // not UpdateAll
	b.WriteBits(1, 9)
	b.FlushBits()
	wire.WritePackedGUID(b, unit)
	b.WriteU16(slot)
	b.WriteBit(true)
	b.FlushBits()
	wire.WritePackedGUID(b, wire.EmptyGUID) // CastID
	b.WriteI32(spellID)
	b.WriteI32(0) // visual
	b.WriteI32(0) // script visual
	b.WriteU16(0x11)
	b.WriteU32(0)
	b.WriteU16(0)
	b.WriteU8(stacks)
	b.WriteI32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteBit(true)  // cast unit
	b.WriteBit(false) // cast item
	b.WriteBit(true)  // duration
	b.WriteBit(true)  // remaining
	b.WriteBit(false) // time mod
	b.WriteBits(0, 6)
	b.WriteBits(0, 6)
	b.WriteBit(false) // content tuning
	b.FlushBits()
	wire.WritePackedGUID(b, caster)
	b.WriteU32(durationMS)
	b.WriteU32(remainingMS)
	return b.Bytes()
}

// buildNonMeleeDamage is SMSG_SPELL_NON_MELEE_DAMAGE_LOG in the shape internal/forever/combat parses (the two counts,
// then the four closing bits; no log data, no tuning).
func buildNonMeleeDamage(me, caster, castID wire.GUID, spellID, damage int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, me)
	wire.WritePackedGUID(b, caster)
	wire.WritePackedGUID(b, castID)
	b.WriteI32(spellID)
	b.WriteI32(0) // SpellXSpellVisualID
	b.WriteI32(0) // ScriptVisualID
	b.WriteI32(damage)
	b.WriteI32(damage)
	b.WriteI32(0) // overkill
	b.WriteU8(1)  // school mask
	b.WriteI32(0) // absorbed
	b.WriteI32(0) // resisted
	b.WriteI32(0) // shield block
	b.WriteI32(0) // reflecting
	b.WriteI32(0) // flags
	b.WriteU32(0) // viewers
	b.WriteU32(0) // supporters
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// buildHealLog is SMSG_SPELL_HEAL_LOG with no supporters and the four closing bits false.
func buildHealLog(target, caster wire.GUID, spellID, amount int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, target)
	wire.WritePackedGUID(b, caster)
	b.WriteI32(spellID)
	b.WriteI32(amount)
	b.WriteI32(amount)
	b.WriteI32(0) // overheal
	b.WriteI32(0) // absorbed
	b.WriteU32(0) // supporters
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// buildPeriodicLog is SMSG_SPELL_PERIODIC_AURA_LOG with one effect and no log data.
func buildPeriodicLog(target, caster wire.GUID, spellID int32, effect, amount int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, target)
	wire.WritePackedGUID(b, caster)
	b.WriteI32(spellID)
	b.WriteU32(1)
	b.WriteI32(effect)
	b.WriteI32(amount)
	b.WriteI32(amount)
	b.WriteI32(0) // overheal or kill
	b.WriteI32(1) // school mask or power
	b.WriteI32(0) // absorbed or amp
	b.WriteI32(0) // resisted
	b.WriteU32(0) // supporters
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteBit(false) // no log data
	b.FlushBits()
	return b.Bytes()
}

// TestTrialAuraBuildersParse keeps the hand-built packets honest: each one is the parser's own shape.
func TestTrialAuraBuildersParse(t *testing.T) {
	self := wire.GUID{Low: 0x77, High: 0x0800000000000000}
	unit := wire.GUID{Low: 0x11, High: 0x0100000000000000}
	au, err := combat.ParseAuraUpdate(buildAuraUpdateFrom(unit, self, 2, 845, 3, 10000, 5000))
	if err != nil || len(au.Auras) != 1 || au.Auras[0].Data == nil {
		t.Fatalf("aura update: %+v, %v", au, err)
	}
	if au.Auras[0].Data.SpellID != 845 || au.Auras[0].Data.Applications != 3 || au.Auras[0].Data.CastUnit == nil || *au.Auras[0].Data.CastUnit != self {
		t.Fatalf("aura data: %+v", au.Auras[0].Data)
	}
	dmg, err := combat.ParseSpellNonMeleeDamage(buildNonMeleeDamage(self, unit, self, 5025, 34))
	if err != nil || dmg.Damage != 34 || dmg.SpellID != 5025 {
		t.Fatalf("non-melee: %+v, %v", dmg, err)
	}
	heal, err := combat.ParseSpellHeal(buildHealLog(unit, self, 2061, 40))
	if err != nil || heal.OriginalHeal != 40 {
		t.Fatalf("heal: %+v, %v", heal, err)
	}
	per, err := combat.ParseSpellPeriodicAuraLog(buildPeriodicLog(self, unit, 770, 8, 9))
	if err != nil || len(per.Effects) != 1 || !per.Effects[0].PeriodicTickIsHeal() {
		t.Fatalf("periodic: %+v, %v", per, err)
	}
}

// TestServeEventsKeepTheLoginBurst: a row the client's dispatch writes while the replacement session is still
// logging in is kept. The fake writes one melee row before SMSG_LOGIN_VERIFY_WORLD, so before the ring is handed to
// the session that row lands in the fresh session's own ring; the reconnect must hand the retained ring over before
// that dispatch, not replace it after, or the row is silently discarded. The row after the burst is the session's
// own, and the seqs run on: nothing is reused and nothing is lost.
func TestServeEventsKeepTheLoginBurst(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	f.serveLoginEvents = true
	f.serveEventsOne = true
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	// The first session's login burst is a row, and its post-login row is the next seq.
	first := a.waitEvents(t, 0, 2)
	if first.Events[0].Seq != 1 || first.Events[0].Kind != "melee" || first.Events[0].Amount != 3 {
		t.Fatalf("the first session's login-burst row is %+v", first.Events[0])
	}
	if first.Events[1].Seq != 2 || first.Events[1].Kind != "melee" || first.Events[1].Amount != 7 {
		t.Fatalf("the first session's post-login row is %+v", first.Events[1])
	}
	cursor := first.Next

	dropServingSocket(t, f)
	waitServeState(t, a, "online", steps)
	deadline := time.Now().Add(15 * time.Second)
	for f.serveSessionCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.serveSessionCount() < 2 {
		t.Fatalf("the fake never ran a second session\n%s", steps.joined())
	}
	waitServeState(t, a, "online", steps)

	// Both rows of the re-entry are readable: the one dispatched during its login (seq 3) and the one after it
	// (seq 4). A ring swapped in only after the login keeps just the second, so this fails before the handoff.
	second := a.waitEvents(t, cursor, 2)
	if got := second.Events[0].Seq; got != cursor+1 {
		t.Fatalf("the re-entry's login-burst row has seq %d, want %d (rows %+v)", got, cursor+1, second.Events)
	}
	if second.Events[0].Kind != "melee" || second.Events[0].Amount != 3 {
		t.Fatalf("the re-entry's login-burst row is %+v, want the burst's melee 3", second.Events[0])
	}
	if got := second.Events[1].Seq; got != cursor+2 {
		t.Fatalf("the re-entry's next row has seq %d, want %d", got, cursor+2)
	}
	if second.Events[1].Amount != 7 {
		t.Fatalf("the re-entry's post-login row is %+v", second.Events[1])
	}
	if second.Dropped != 0 {
		t.Fatalf("the re-entry page dropped %d rows", second.Dropped)
	}
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown after the re-entry: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
}

// TestMoverWithoutTheCreateBlock: the login burst's own character create block can still be in flight when serve
// answers its first read. A session without a mover answers from the login position instead of dereferencing nil:
// the packet that builds the mover is not a precondition of the model reads.
func TestMoverWithoutTheCreateBlock(t *testing.T) {
	report := &Report{Counts: make(map[string]int), Position: [4]float32{11, 12, 13, 0.5}}
	s := newSession(Options{Logf: func(string, ...any) {}}, report, context.Background())
	defer s.pumpCancel()
	if s.mover != nil {
		t.Fatal("a new session already has a mover")
	}
	if x, y, z, o := s.MoverX(), s.MoverY(), s.MoverZ(), s.MoverO(); x != 11 || y != 12 || z != 13 || o != 0.5 {
		t.Fatalf("the login position answered %.1f %.1f %.1f %.2f, want 11 12 13 0.5", x, y, z, o)
	}
	units, err := s.serveUnits(serveUnitsParams{})
	if err != nil || len(units) != 0 {
		t.Fatalf("units before the create block: %d rows, %v", len(units), err)
	}
}
