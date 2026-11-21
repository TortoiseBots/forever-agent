package bughunt

import (
	"strings"
	"testing"
)

// crow is one combat row on the run's clock.
func crow(kind string, t float64, src, dst string, spell int) CombatRow {
	return CombatRow{Kind: kind, T: t, TKnown: true,
		Source: CombatActor{GUID: src}, Target: CombatActor{GUID: dst}, SpellID: spell}
}

// eventRun is a run with a combat log and the live-checked lexicons.
func eventRun(kind string, roles map[string]string, frames []*Frame, combat []CombatRow, names, dispel map[int]string) *Run {
	r := testRun(kind, roles, frames...)
	r.Combat = combat
	r.SpellNames, r.Dispel = names, dispel
	return r
}

// withSpan puts a mob's rows at a and b around the rows, so the log provably spans the window the rules look at
// (the mob is no subject, so it adds no finding of its own).
func withSpan(rows []CombatRow, a, b float64) []CombatRow {
	out := []CombatRow{crow("melee", a, gid(0xB09), gid(0xB01), 0)}
	out = append(out, rows...)
	return append(out, crow("melee", b, gid(0xB09), gid(0xB01), 0))
}

// withClass sets one member's class.
func withClass(r *Run, bot, class string) *Run {
	r.Classes[bot] = class
	return r
}

// TestRule15MissedDecurse is the round's first test: a removable debuff left on a member for 14 s while an able
// dispeller stood six yards away, alive and not casting.
func TestRule15MissedDecurse(t *testing.T) {
	heal := testUnit(gHeal)
	heal.HP, heal.MaxHP = 100, 100
	dispel := map[int]string{17172: "magic"}
	names := map[int]string{17172: "Hex"}
	var frames []*Frame
	for t := 0.0; t <= 14; t += 2 {
		tank := testUnit(gTank)
		tank.Combat, tank.X, tank.Y = true, 2, 0
		priest := testUnit(gHeal)
		priest.X, priest.Y = 0, 0
		if t <= 12 {
			priest.Auras = []uint32{17172}
		}
		paladin := testUnit(gDps3)
		paladin.X, paladin.Y = 6, 0
		frames = append(frames, frameAt(t, tank, priest, paladin))
	}
	r := eventRun("watch", map[string]string{gHeal: "healer", gTank: "tank", gDps3: "paladin"}, frames, nil, names, dispel)
	withClass(r, gHeal, "priest")
	withClass(r, gTank, "warrior")
	withClass(r, gDps3, "paladin")
	hits := Rule15(r)
	if len(hits) != 1 {
		t.Fatalf("hits %d: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Bot != gHeal || h.Rule != 15 || h.Dur != 14 {
		t.Fatalf("hit %+v", h)
	}
	if len(h.Key) != 3 || h.Key[0] != "15" || h.Key[1] != "magic" || h.Key[2] != "Hex" {
		t.Fatalf("key %v", h.Key)
	}
	if !strings.Contains(h.Ev, "1 able dispeller(s) alive, nearest 6 yd") {
		t.Fatalf("evidence %q", h.Ev)
	}
}

// TestRule15NearMisses: the debuff's duration, the dispeller's class, distance and cast bar, a must-stay type and
// an unknown class each keep the rule silent.
func TestRule15NearMisses(t *testing.T) {
	build := func(upTo float64, dispellerAt float64, casting bool, aura uint32, typ string) *Run {
		var frames []*Frame
		for t := 0.0; t <= upTo; t += 2 {
			tank := testUnit(gTank)
			tank.Combat = true
			priest := testUnit(gHeal)
			if t <= upTo-2 {
				priest.Auras = []uint32{aura}
			}
			paladin := testUnit(gDps3)
			paladin.X, paladin.Casting = dispellerAt, casting
			frames = append(frames, frameAt(t, tank, priest, paladin))
		}
		dispel := map[int]string{17172: typ}
		if typ == "" {
			dispel = map[int]string{}
		}
		r := eventRun("watch", map[string]string{gHeal: "healer", gTank: "tank", gDps3: "dps"}, frames, nil,
			map[int]string{17172: "Hex"}, dispel)
		withClass(r, gHeal, "priest")
		withClass(r, gTank, "warrior")
		withClass(r, gDps3, "paladin")
		return r
	}
	cases := []struct {
		name                   string
		run                    *Run
		wantSkip, wantDispelNo string
	}{
		{"duration under the minimum", build(6, 6, false, 17172, "magic"), "", ""},
		{"dispeller out of range", build(14, 40, false, 17172, "magic"), "", ""},
		{"dispeller casting", build(14, 6, true, 17172, "magic"), "", ""},
		{"a must-stay debuff", build(14, 6, false, 17172, "never"), "", ""},
		{"no dispel file", build(14, 6, false, 17172, ""), "no dispel types", ""},
	}
	for _, c := range cases {
		if c.wantSkip != "" {
			if got := skipDispel(c.run); got != c.wantSkip {
				t.Fatalf("%s: skip %q, want %q", c.name, got, c.wantSkip)
			}
			continue
		}
		if hits := Rule15(c.run); len(hits) != 0 {
			t.Fatalf("%s: hits %+v", c.name, hits)
		}
	}
}

// TestRule15UnknownClass: a run that names no class cannot judge a dispeller, so the rule is a skipped check.
func TestRule15UnknownClass(t *testing.T) {
	f := frameAt(0, testUnit(gHeal))
	r := eventRun("watch", map[string]string{}, []*Frame{f}, nil, map[int]string{17172: "Hex"}, map[int]string{17172: "magic"})
	if got := skipDispel(r); got != "no classes" {
		t.Fatalf("skip %q", got)
	}
}

// TestRule1IdleInCombat: a member still and silent for 12 s of a fight is one hit; a cast in the middle splits it.
func TestRule1IdleInCombat(t *testing.T) {
	var frames []*Frame
	for t := 0.0; t <= 16; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		dps := testUnit(gDps)
		dps.Combat = false
		frames = append(frames, frameAt(t, tank, dps))
	}
	combat := []CombatRow{crow("melee", 0, gTank, gid(0xB01), 0)}
	for at := 2.0; at <= 16; at += 2 {
		combat = append(combat, crow("melee", at, gTank, gid(0xB01), 0))
	}
	combat = withSpan(combat, -2, 18)
	r := eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, frames, combat, nil, nil)
	withClass(r, gDps, "mage")
	hits := Rule1(r)
	if len(hits) != 1 {
		t.Fatalf("hits %d: %+v", len(hits), hits)
	}
	if hits[0].Bot != gDps || hits[0].T != 4 || hits[0].Dur != 12 {
		t.Fatalf("hit %+v", hits[0])
	}
	if len(hits[0].Key) != 2 || hits[0].Key[0] != "1" || hits[0].Key[1] != "dps" {
		t.Fatalf("key %v", hits[0].Key)
	}
	// A cast of its own within a second of the middle frames breaks the idle stretch into two short ones.
	casts := append(append([]CombatRow(nil), combat...), crow("spell_go", 10, gDps, gid(0xB01), 133))
	if hits := Rule1(eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, frames, casts, nil, nil)); len(hits) != 0 {
		t.Fatalf("cast mid-episode: %+v", hits)
	}
	// A moving member is not idle.
	moving := make([]*Frame, 0, len(frames))
	for _, f := range frames {
		m := *f.Units[gDps]
		m.Moving = true
		nf := frameAt(f.T, f.Units[gTank], &m)
		moving = append(moving, nf)
	}
	if hits := Rule1(eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, moving, combat, nil, nil)); len(hits) != 0 {
		t.Fatalf("moving: %+v", hits)
	}
}

// TestRule1HealerWithNobodyHurt: a healer with its party at full health is idle by right, not by fault.
func TestRule1HealerWithNobodyHurt(t *testing.T) {
	var frames []*Frame
	for t := 0.0; t <= 16; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		heal := testUnit(gHeal)
		frames = append(frames, frameAt(t, tank, heal))
	}
	var combat []CombatRow
	for at := 0.0; at <= 16; at += 2 {
		combat = append(combat, crow("melee", at, gTank, gid(0xB01), 0))
	}
	combat = withSpan(combat, -2, 18)
	r := eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, frames, combat, nil, nil)
	if hits := Rule1(r); len(hits) != 0 {
		t.Fatalf("nobody hurt: %+v", hits)
	}
	// One member under 95 % gives the healer work: the same frames now hold a hit.
	for _, f := range frames {
		f.Units[gTank].HP = 50
	}
	if hits := Rule1(r); len(hits) != 1 {
		t.Fatalf("hurt party: %+v", hits)
	}
}

// TestRule2HealerNotHealing: a healer at 50 % mana leaves a tank at 30 % for 6 s; a heal in the middle splits it.
func TestRule2HealerNotHealing(t *testing.T) {
	build := func() []*Frame {
		var frames []*Frame
		for t := 0.0; t <= 6; t++ {
			tank := testUnit(gTank)
			tank.Combat, tank.HP, tank.X = true, 30, 10
			heal := testUnit(gHeal)
			heal.Power, heal.MaxPower, heal.PType = 50, 100, 0
			frames = append(frames, frameAt(t, tank, heal))
		}
		return frames
	}
	combat := withSpan([]CombatRow{crow("melee", 0, gTank, gid(0xB01), 0)}, -2, 8)
	r := eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, build(), combat,
		map[int]string{2050: "Flash of Light"}, nil)
	withClass(r, gHeal, "priest")
	hits := Rule2(r)
	if len(hits) != 1 {
		t.Fatalf("hits %d: %+v", len(hits), hits)
	}
	if hits[0].Bot != gHeal || hits[0].Dur != 6 || len(hits[0].Key) != 2 || hits[0].Key[1] != "priest" {
		t.Fatalf("hit %+v", hits[0])
	}
	if !strings.Contains(hits[0].Ev, "healer idle on "+gTank) {
		t.Fatalf("evidence %q", hits[0].Ev)
	}
	// A heal row in the middle is the healer working.
	withHeal := append(append([]CombatRow(nil), combat...), crow("heal", 3, gHeal, gTank, 2050))
	if hits := Rule2(eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, build(), withHeal,
		map[int]string{2050: "Flash of Light"}, nil)); len(hits) != 0 {
		t.Fatalf("heal mid-episode: %+v", hits)
	}
	// A named heal-spell completion counts the same way.
	withShield := append(append([]CombatRow(nil), combat...), crow("spell_go", 3, gHeal, gTank, 17))
	if hits := Rule2(eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, build(), withShield,
		map[int]string{17: "Power Word: Shield"}, nil)); len(hits) != 0 {
		t.Fatalf("shield mid-episode: %+v", hits)
	}
	// An ally further than 40 yd is not the healer's to heal.
	far := build()
	for _, f := range far {
		f.Units[gTank].X = 50
	}
	if hits := Rule2(eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, far, combat,
		map[int]string{2050: "Flash of Light"}, nil)); len(hits) != 0 {
		t.Fatalf("ally out of range: %+v", hits)
	}
	// Low mana is a need, not a waste of a healer's time.
	empty := build()
	for _, f := range empty {
		f.Units[gHeal].Power = 5
	}
	if hits := Rule2(eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, empty, combat,
		map[int]string{2050: "Flash of Light"}, nil)); len(hits) != 0 {
		t.Fatalf("low mana: %+v", hits)
	}
}

// TestRule4StandingInAoE: five ticks of a named AoE on one member over 4 s are one hit; two ticks are not.
func TestRule4StandingInAoE(t *testing.T) {
	names := map[int]string{12345: "Rain of Fire", 133: "Frostbolt"}
	var ticks []CombatRow
	for i := 0; i <= 4; i++ {
		ticks = append(ticks, crow("periodic", float64(i), gid(0xB01), gDps, 12345))
	}
	r := eventRun("watch", map[string]string{gDps: "dps"}, nil, ticks, names, nil)
	hits := Rule4(r)
	if len(hits) != 1 {
		t.Fatalf("hits %d: %+v", len(hits), hits)
	}
	if hits[0].Bot != gDps || hits[0].T != 0 || hits[0].Dur != 4 || len(hits[0].Key) != 2 || hits[0].Key[1] != "Rain of Fire" {
		t.Fatalf("hit %+v", hits[0])
	}
	if !strings.Contains(hits[0].Ev, "ticked 5 times over 4.0 s") {
		t.Fatalf("evidence %q", hits[0].Ev)
	}
	// Two ticks are too short.
	if hits := Rule4(eventRun("watch", map[string]string{gDps: "dps"}, nil, ticks[:2], names, nil)); len(hits) != 0 {
		t.Fatalf("two ticks: %+v", hits)
	}
	// A direct spell is not an area effect.
	var direct []CombatRow
	for i := 0; i <= 4; i++ {
		direct = append(direct, crow("spell_damage", float64(i), gid(0xB01), gDps, 133))
	}
	if hits := Rule4(eventRun("watch", map[string]string{gDps: "dps"}, nil, direct, names, nil)); len(hits) != 0 {
		t.Fatalf("direct damage: %+v", hits)
	}
}

// TestRule8BuffFlipFlop: four genuine casts of one buff on one target in 30 s are a hit; three are not, and a cast
// with no start and no aura is not a genuine cast.
func TestRule8BuffFlipFlop(t *testing.T) {
	names := map[int]string{21562: "Power Word: Fortitude"}
	var rows []CombatRow
	for _, at := range []float64{0, 10, 20, 30} {
		rows = append(rows, crow("spell_start", at-0.2, gHeal, gTank, 21562))
		rows = append(rows, crow("spell_go", at, gHeal, gTank, 21562))
	}
	rows = append(rows, crow("aura_applied", 0.1, gHeal, gTank, 21562))
	rows = append(rows, crow("aura_removed", 30, gHeal, gTank, 21562))
	r := eventRun("watch", map[string]string{gHeal: "healer"}, nil, rows, names, nil)
	withClass(r, gHeal, "priest")
	hits := Rule8(r)
	if len(hits) != 1 {
		t.Fatalf("hits %d: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Bot != gHeal || h.Dur != 30 || len(h.Key) != 2 || h.Key[1] != "Power Word: Fortitude" {
		t.Fatalf("hit %+v", h)
	}
	if !strings.Contains(h.Ev, "long-aura") {
		t.Fatalf("the aura-life word belongs in the evidence: %q", h.Ev)
	}
	if h.Note != "aura_life=29.9" {
		t.Fatalf("note %q", h.Note)
	}
	if hits := Rule8(eventRun("watch", map[string]string{gHeal: "healer"}, nil, rows[:6], names, nil)); len(hits) != 0 {
		t.Fatalf("three casts: %+v", hits)
	}
	// No start and no aura: the completion is a periodic row, not a cast.
	bare := []CombatRow{}
	for _, at := range []float64{0, 10, 20, 30} {
		bare = append(bare, crow("spell_go", at, gHeal, gTank, 21562))
	}
	if hits := Rule8(eventRun("watch", map[string]string{gHeal: "healer"}, nil, bare, names, nil)); len(hits) != 0 {
		t.Fatalf("no genuineness: %+v", hits)
	}
}

// TestRule11CastCancel: four started casts that never land while mana rises over 12 s are one hit; a landed
// spell, a flat mana bar and a single start are not.
func TestRule11CastCancel(t *testing.T) {
	buildFrames := func(power func(t float64) float64) []*Frame {
		var frames []*Frame
		for t := 0.0; t <= 12; t += 2 {
			tank := testUnit(gTank)
			tank.Combat = true
			dps := testUnit(gDps)
			dps.Power, dps.MaxPower, dps.PType = power(t), 100, 0
			frames = append(frames, frameAt(t, tank, dps))
		}
		return frames
	}
	rising := func(t float64) float64 { return 50 + t }
	var starts []CombatRow
	for _, at := range []float64{0, 4, 8, 10} {
		starts = append(starts, crow("spell_start", at, gDps, gid(0xB01), 116))
	}
	starts = append(starts, crow("spell_fail", 0.4, gDps, gid(0xB01), 116))
	starts[len(starts)-1].Reason = "interrupted"
	r := eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, buildFrames(rising), withSpan(starts, -2, 16),
		map[int]string{116: "Frostbolt"}, nil)
	withClass(r, gDps, "mage")
	hits := Rule11(r)
	if len(hits) != 1 {
		t.Fatalf("hits %d: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Bot != gDps || h.Dur != 12 || len(h.Key) != 4 || h.Key[3] != "cast-cancel-signal" {
		t.Fatalf("hit %+v", h)
	}
	if !strings.Contains(h.Ev, "fail reasons interrupted x1") {
		t.Fatalf("evidence %q", h.Ev)
	}
	// A landing spell breaks the silence.
	landed := append(append([]CombatRow(nil), starts...), crow("spell_damage", 6, gDps, gid(0xB01), 116))
	if hits := Rule11(eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, buildFrames(rising), withSpan(landed, -2, 16),
		map[int]string{116: "Frostbolt"}, nil)); len(hits) != 0 {
		t.Fatalf("output landed: %+v", hits)
	}
	// A flat mana bar is not the cancel signal.
	if hits := Rule11(eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"},
		buildFrames(func(float64) float64 { return 50 }), withSpan(starts, -2, 16), map[int]string{116: "Frostbolt"}, nil)); len(hits) != 0 {
		t.Fatalf("flat mana: %+v", hits)
	}
	// One started cast is not a pattern.
	if hits := Rule11(eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, buildFrames(rising),
		withSpan(starts[:2], -2, 16), map[int]string{116: "Frostbolt"}, nil)); len(hits) != 0 {
		t.Fatalf("one start: %+v", hits)
	}
}

// TestUnknownSpellNeverMatches: an id without a name is `spell <id>` and matches no name rule.
func TestUnknownSpellNeverMatches(t *testing.T) {
	const unknown = 999
	r := testRun("watch", map[string]string{gHeal: "healer"})
	if got := r.SpellName(unknown); got != "spell 999" {
		t.Fatalf("name %q", got)
	}
	var casts []CombatRow
	for _, at := range []float64{0, 10, 20, 30} {
		casts = append(casts, crow("spell_start", at-0.2, gHeal, gTank, unknown))
		casts = append(casts, crow("spell_go", at, gHeal, gTank, unknown))
	}
	var ticks []CombatRow
	for i := 0; i <= 4; i++ {
		ticks = append(ticks, crow("periodic", float64(i), gid(0xB01), gDps, unknown))
	}
	r.Combat = append(casts, ticks...)
	r.SpellNames = map[int]string{}
	if hits := Rule8(r); len(hits) != 0 {
		t.Fatalf("rule 8 matched an unnamed id: %+v", hits)
	}
	if hits := Rule4(r); len(hits) != 0 {
		t.Fatalf("rule 4 matched an unnamed id: %+v", hits)
	}
}

// TestEventRulesSkipWithoutInput: no combat log skips the log rules; no names or dispel skips the ones that name a
// spell, and the reason is the counts' own text.
func TestEventRulesSkipWithoutInput(t *testing.T) {
	r := testRun("watch", map[string]string{gHeal: "healer"}, frameAt(0, testUnit(gHeal)))
	for _, c := range []struct {
		rule   Rule
		reason string
	}{
		{Rule{Number: 1, Skipped: skipNoCombatLog}, "no combat log"},
		{Rule{Number: 2, Skipped: skipHealNeedsNames}, "no combat log"},
		{Rule{Number: 15, Skipped: skipDispel}, "no dispel types"},
	} {
		if got := c.rule.Skipped(r); got != c.reason {
			t.Fatalf("rule %d: skip %q, want %q", c.rule.Number, got, c.reason)
		}
	}
	r.Combat = []CombatRow{crow("melee", 0, gTank, gid(0xB01), 0)}
	if got := skipHealNeedsNames(r); got != "no spell names" {
		t.Fatalf("names skip %q", got)
	}
	if _, counts := Hunt([]*Run{r}, EventRules()); counts.SkipReasons["rule 2: no spell names"] != 1 {
		t.Fatalf("counts %+v", counts)
	}
}

// TestRule15DispelEventEndsTheDebuff: the frame list still shows the aura to the end (a stale aura row), and a
// dispel row says it came off at 6 s: the episode is 6 s, under the 8 s minimum, so no hit. The same frames
// without the row are the rule's 30 s hit.
func TestRule15DispelEventEndsTheDebuff(t *testing.T) {
	dispel := map[int]string{17172: "magic"}
	names := map[int]string{17172: "Hex"}
	var frames []*Frame
	for t := 0.0; t <= 30; t += 2 {
		tank := testUnit(gTank)
		tank.Combat, tank.X, tank.Y = true, 2, 0
		priest := testUnit(gHeal)
		priest.Auras = []uint32{17172}
		paladin := testUnit(gDps3)
		paladin.X, paladin.Y = 6, 0
		frames = append(frames, frameAt(t, tank, priest, paladin))
	}
	roles := map[string]string{gHeal: "healer", gTank: "tank", gDps3: "paladin"}
	without := eventRun("watch", roles, frames, nil, names, dispel)
	withClass(without, gHeal, "priest")
	withClass(without, gTank, "warrior")
	withClass(without, gDps3, "paladin")
	if hits := Rule15(without); len(hits) != 1 || hits[0].Dur != 30 {
		t.Fatalf("without the dispel row: hits %+v", hits)
	}

	row := crow("dispel", 6, gDps3, gHeal, 527)
	row.Dispelled = []DispelledAura{{SpellID: 17172, Harmful: true}}
	with := eventRun("watch", roles, frames, []CombatRow{row}, names, dispel)
	withClass(with, gHeal, "priest")
	withClass(with, gTank, "warrior")
	withClass(with, gDps3, "paladin")
	if hits := Rule15(with); len(hits) != 0 {
		t.Fatalf("the dispel at 6 s did not end the episode: %+v", hits)
	}

	// a later dispel (16 s) leaves a 16 s episode: a hit that ends where the row says, not at the frames' end
	late := crow("dispel", 16, gDps3, gHeal, 527)
	late.Dispelled = []DispelledAura{{SpellID: 17172, Harmful: true}}
	withLate := eventRun("watch", roles, frames, []CombatRow{late}, names, dispel)
	withClass(withLate, gHeal, "priest")
	withClass(withLate, gTank, "warrior")
	withClass(withLate, gDps3, "paladin")
	hits := Rule15(withLate)
	if len(hits) != 1 || hits[0].Dur != 16 {
		t.Fatalf("the 16 s dispel: hits %+v, want one 16 s hit", hits)
	}
}

// TestRule15SecondEpisodeKeepsItsOwnDispel: one aura reapplied after a gap is two episodes, and each has its own
// dispel row. The index must keep every removal time: with only the first, the second episode (22-40 s) would be
// judged as never dispelled because its 26 s row is later than its start.
func TestRule15SecondEpisodeKeepsItsOwnDispel(t *testing.T) {
	dispel := map[int]string{17172: "magic"}
	names := map[int]string{17172: "Hex"}
	var frames []*Frame
	for t := 0.0; t <= 40; t += 2 {
		tank := testUnit(gTank)
		tank.Combat, tank.X, tank.Y = true, 2, 0
		priest := testUnit(gHeal)
		if (t >= 0 && t <= 10) || t >= 22 {
			priest.Auras = []uint32{17172}
		}
		paladin := testUnit(gDps3)
		paladin.X, paladin.Y = 6, 0
		frames = append(frames, frameAt(t, tank, priest, paladin))
	}
	roles := map[string]string{gHeal: "healer", gTank: "tank", gDps3: "paladin"}
	first := crow("dispel", 6, gDps3, gHeal, 527)
	first.Dispelled = []DispelledAura{{SpellID: 17172, Harmful: true}}
	second := crow("dispel", 26, gDps3, gHeal, 527)
	second.Dispelled = []DispelledAura{{SpellID: 17172, Harmful: true}}
	run := eventRun("watch", roles, frames, []CombatRow{first, second}, names, dispel)
	withClass(run, gHeal, "priest")
	withClass(run, gTank, "warrior")
	withClass(run, gDps3, "paladin")
	if hits := Rule15(run); len(hits) != 0 {
		t.Fatalf("the second episode's own dispel did not end it: %+v", hits)
	}
	// With only the first row the second episode is undispelled: 22-40 s is a hit, and it is the row at 26 s that
	// changes that — the first row cannot excuse it.
	onlyFirst := eventRun("watch", roles, frames, []CombatRow{first}, names, dispel)
	withClass(onlyFirst, gHeal, "priest")
	withClass(onlyFirst, gTank, "warrior")
	withClass(onlyFirst, gDps3, "paladin")
	if hits := Rule15(onlyFirst); len(hits) != 1 {
		t.Fatalf("without the second row: hits %+v, want the 22-40 s episode", hits)
	}
}
