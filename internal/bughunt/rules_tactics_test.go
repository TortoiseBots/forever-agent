package bughunt

// The trash-tactics rules' tests: one hit case and the UNKNOWN case per rule, on hand-made combat rows (never
// captures). A hostile tactics file is an error naming the file and the line.

import (
	"strings"
	"testing"
)

// tacticIDs are the hand-made entries and spells the fixtures use.
const (
	tacCultist  = 113
	tacEnforcer = 114
	tacHeal     = 34887
	tacDebuff   = 34888
	tacPoly     = 118
)

// tacticsFile is the two-entry tactics the fixtures judge against.
func tacticsFile() *Tactics {
	return &Tactics{
		Dungeon: "example",
		Creatures: map[uint32]*TacticEntry{
			tacCultist: {
				Entry: tacCultist, Name: "Searing Blade Cultist",
				MustInterrupt: []int{tacHeal},
				CCImmune:      map[string]bool{},
				MustDispel:    []TacticDispel{{Spell: tacDebuff, Type: "magic", WithinS: 8}},
				Priority:      1,
			},
			tacEnforcer: {
				Entry: tacEnforcer, Name: "Searing Blade Enforcer",
				CCImmune: map[string]bool{"stun": true},
				Priority: 2,
			},
		},
		CCSpells: map[int][]string{tacPoly: {"polymorph"}, 20066: {"shackle"}},
	}
}

// tacticsRun is a fight's frames: the tank swinging, the rogue free beside the cultist, the priest free beside
// the victim. The rogue is an interrupter class; the priest dispels magic.
func tacticsRun(frames int) []*Frame {
	var out []*Frame
	for t := 0.0; t <= float64(frames)*2; t += 2 {
		tank := testUnit(gTank)
		tank.Combat, tank.X, tank.Y = true, 0, 0
		rogue := testUnit(gDps)
		rogue.X, rogue.Y = 3, 0
		priest := testUnit(gHeal)
		priest.X, priest.Y = 6, 0
		mob1 := &Mob{G: mobGUID(tacCultist, 1), Entry: tacCultist, Name: "Cultist", HPPct: 100, X: 5, Y: 0, Z: 50, Combat: true}
		mob2 := &Mob{G: mobGUID(tacEnforcer, 2), Entry: tacEnforcer, Name: "Enforcer", HPPct: 100, X: 8, Y: 0, Z: 50, Combat: true}
		f := frameAt(t, tank, rogue, priest)
		f.Mobs = []*Mob{mob1, mob2}
		f.GC = true
		out = append(out, f)
	}
	return out
}

// mobGUID is a hostile guid in the trace's one form: the entry in the high word, a counter in the low word.
// Frames carry the Entry field too, so rules join by guid and read the entry from the frame.
func mobGUID(entry uint32, n uint64) string {
	return gid((uint64(entry) << 32) | n)
}

// tacticsRoles classes and subjects the fixtures' runs.
func tacticsRoles() map[string]string {
	return map[string]string{gTank: "tank", gDps: "dps", gHeal: "healer"}
}

// tacticsClasses names the classes: warrior tank, rogue DPS, priest healer.
func withTacticsClasses(r *Run) *Run {
	withClass(r, gTank, "warrior")
	withClass(r, gDps, "rogue")
	withClass(r, gHeal, "priest")
	return r
}

// TestRule16MissedInterrupt: the cultist's listed heal completes at t=10 with the rogue free, and no interrupt
// row answers it. The span rows around it prove the log covers the window.
func TestRule16MissedInterrupt(t *testing.T) {
	tac := tacticsFile()
	frames := tacticsRun(8)
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		func() CombatRow {
			// A kick that stopped another pack's cast: proves the interrupt kind exists in this recording
			// without answering the cultist's cast.
			row := crow("interrupt", 6, gDps, mobGUID(tacEnforcer, 2), 1766)
			row.InterruptedSpellID = 99991
			return row
		}(),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), frames, combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	withTacticsClasses(r)
	hits := Rule16(r, tac)
	if len(hits) != 1 {
		t.Fatalf("hits %+v", hits)
	}
	h := hits[0]
	if h.Rule != 16 || (h.Bot != gDps && h.Bot != gHeal && h.Bot != gTank) {
		t.Fatalf("hit %+v", h)
	}
	if len(h.Key) != 3 || h.Key[0] != "16" || h.Key[2] != "Dark Heal" {
		t.Fatalf("key %v", h.Key)
	}
	if !strings.Contains(h.Ev, "with no interrupt row") || !strings.Contains(h.Ev, "spell_go@10") {
		t.Fatalf("evidence %q", h.Ev)
	}
	// An interrupt row for the same mob and spell answers the cast: no hit.
	answered := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		func() CombatRow {
			row := crow("interrupt", 11, gDps, mobGUID(tacCultist, 1), 1766)
			row.InterruptedSpellID = tacHeal
			return row
		}(),
	}, 6, 14)
	if hits := Rule16(eventRun("watch", tacticsRoles(), frames, answered, map[int]string{tacHeal: "Dark Heal"}, nil), tac); len(hits) != 0 {
		t.Fatalf("answered cast: %+v", hits)
	}
}

// TestRule16UnknownWithoutInterruptRows: a recording with no interrupt kind cannot say whether the cast was
// kicked: UNKNOWN, counted, never a finding.
func TestRule16UnknownWithoutInterruptRows(t *testing.T) {
	tac := tacticsFile()
	frames := tacticsRun(8)
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), frames, combat, nil, nil)
	withTacticsClasses(r)
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[0:1])
	if len(hits) != 0 {
		t.Fatalf("no interrupt kind, yet %+v", hits)
	}
	if counts.SkipReasons["rule 16: log has no interrupt rows"] != 1 || counts.Complete(16) {
		t.Fatalf("the skipped check is counted: %+v", counts)
	}
}

// TestRule17CCOnImmune: a Polymorph completion on the stun-immune enforcer is not the rule's business, but a
// Shackle on it is one hit; an unlisted CC spell is unknown, never a finding.
func TestRule17CCOnImmune(t *testing.T) {
	tac := tacticsFile()
	tac.Creatures[tacEnforcer].CCImmune["shackle"] = true
	frames := tacticsRun(8)
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, gHeal, mobGUID(tacEnforcer, 2), 20066),
		crow("spell_go", 12, gHeal, mobGUID(tacEnforcer, 2), 118),
		crow("spell_go", 14, gHeal, mobGUID(tacEnforcer, 2), 99999),
	}, 6, 16)
	r := eventRun("watch", tacticsRoles(), frames, combat, map[int]string{118: "Polymorph", 20066: "Shackle"}, nil)
	withTacticsClasses(r)
	hits := Rule17(r, tac)
	if len(hits) != 1 {
		t.Fatalf("hits %+v", hits)
	}
	h := hits[0]
	if h.Rule != 17 || len(h.Key) != 3 || h.Key[2] != "shackle" {
		t.Fatalf("hit %+v", h)
	}
	if !strings.Contains(h.Ev, "spell_go@10") {
		t.Fatalf("evidence %q", h.Ev)
	}
}

// TestRule17UnknownWithoutCCKinds: a recording with no spell_go, miss, resist or immune rows cannot judge CC:
// UNKNOWN, counted.
func TestRule17UnknownWithoutCCKinds(t *testing.T) {
	tac := tacticsFile()
	r := testRun("watch", tacticsRoles(), frameAt(0, testUnit(gTank)))
	r.Combat = []CombatRow{crow("heal", 0, gHeal, gTank, 2050), crow("heal", 2, gHeal, gTank, 2050)}
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[1:2])
	if len(hits) != 0 {
		t.Fatalf("no CC kind, yet %+v", hits)
	}
	if counts.SkipReasons["rule 17: "+ccAttemptReason] != 1 || counts.Complete(17) {
		t.Fatalf("the skipped check is counted: %+v", counts)
	}
}

// TestRule18ListedDebuffLeftOn: the cultist's magic debuff is applied at t=2 and dispelled at t=14, past the 8 s
// limit with the priest free. The frames carry the aura; the rules read the rows.
func TestRule18ListedDebuffLeftOn(t *testing.T) {
	tac := tacticsFile()
	var frames []*Frame
	for t := 0.0; t <= 16; t += 2 {
		tank := testUnit(gTank)
		tank.Combat, tank.X = true, 2
		priest := testUnit(gHeal)
		if t >= 2 {
			priest.Auras = []uint32{tacDebuff}
		}
		if t >= 14 {
			priest.Auras = nil
		}
		dps := testUnit(gDps3)
		dps.X = 6
		f := frameAt(t, tank, priest, dps)
		f.Mobs = []*Mob{{G: mobGUID(tacCultist, 1), Entry: tacCultist, HPPct: 100, X: 5, Y: 0, Z: 50, Combat: true}}
		f.GC = true
		frames = append(frames, f)
	}
	combat := withSpan([]CombatRow{
		crow("spell_go", 2, mobGUID(tacCultist, 1), gHeal, tacDebuff),
		func() CombatRow {
			row := crow("dispel", 14, gDps3, gHeal, 527)
			row.Dispelled = []DispelledAura{{SpellID: tacDebuff, Harmful: true}}
			return row
		}(),
	}, -2, 18)
	r := eventRun("watch", map[string]string{gHeal: "healer", gTank: "tank", gDps3: "dps"}, frames, combat,
		map[int]string{tacDebuff: "Hex"}, map[int]string{tacDebuff: "magic"})
	withClass(r, gHeal, "priest")
	withClass(r, gTank, "warrior")
	withClass(r, gDps3, "paladin")
	hits := Rule18(r, tac)
	if len(hits) != 1 {
		t.Fatalf("hits %+v", hits)
	}
	h := hits[0]
	if h.Rule != 18 || len(h.Key) != 3 || h.Key[1] != "Hex" || h.Key[2] != "magic" {
		t.Fatalf("hit %+v", h)
	}
	if h.Dur != 12 || !strings.Contains(h.Ev, "limit 8 s") {
		t.Fatalf("hit %+v", h)
	}
}

// TestRule18UnknownWithoutAuraRemovals: an older recording with no aura_removed kind cannot tell an expired
// debuff from one still up: an episode with no removal is UNKNOWN, counted, never a finding.
func TestRule18UnknownWithoutAuraRemovals(t *testing.T) {
	tac := tacticsFile()
	frames := tacticsRun(8)
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacDebuff),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), frames, combat, nil, nil)
	withTacticsClasses(r)
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[2:3])
	if len(hits) != 0 {
		t.Fatalf("no aura removal kind, yet %+v", hits)
	}
	if counts.SkipReasons["rule 18: log has no aura_removed rows, so an unpulled debuff's expiry is unknown"] != 1 || counts.Complete(18) {
		t.Fatalf("the skipped check is counted: %+v", counts)
	}
}

// TestRule19KillOrder: the enforcer (priority 2) dies at t=10 while the cultist (priority 1) is still up. The
// frames show both together; the death row dates the kill.
func TestRule19KillOrder(t *testing.T) {
	tac := tacticsFile()
	var frames []*Frame
	for t := 0.0; t <= 16; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		mobs := []*Mob{}
		if t <= 10 {
			mobs = append(mobs, &Mob{G: mobGUID(tacEnforcer, 2), Entry: tacEnforcer, HPPct: 100, X: 8, Y: 0, Z: 50, Combat: true})
		}
		mobs = append(mobs, &Mob{G: mobGUID(tacCultist, 1), Entry: tacCultist, HPPct: 100, X: 5, Y: 0, Z: 50, Combat: true})
		f := frameAt(t, tank)
		f.Mobs = mobs
		f.GC = true
		frames = append(frames, f)
	}
	death := crow("death", 10, mobGUID(tacEnforcer, 2), mobGUID(tacEnforcer, 2), 0)
	combat := withSpan([]CombatRow{death}, 8, 12)
	r := eventRun("watch", map[string]string{gTank: "tank"}, frames, combat, nil, nil)
	withClass(r, gTank, "warrior")
	hits := Rule19(r, tac)
	if len(hits) != 1 {
		t.Fatalf("hits %+v", hits)
	}
	h := hits[0]
	if h.Rule != 19 || len(h.Key) != 4 || h.Key[1] != "example" {
		t.Fatalf("hit %+v", h)
	}
	if !strings.Contains(h.Ev, "death@10") {
		t.Fatalf("evidence %q", h.Ev)
	}
}

// TestRule19UnknownWithoutDatedKill: without a death kind the recording cannot date a kill (a frame gap is not a
// death): UNKNOWN, counted.
func TestRule19UnknownWithoutDatedKill(t *testing.T) {
	tac := tacticsFile()
	var frames []*Frame
	for t := 0.0; t <= 10; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		f := frameAt(t, tank)
		f.Mobs = []*Mob{
			{G: mobGUID(tacCultist, 1), Entry: tacCultist, HPPct: 100, X: 5, Y: 0, Z: 50, Combat: true},
			{G: mobGUID(tacEnforcer, 2), Entry: tacEnforcer, HPPct: 100, X: 8, Y: 0, Z: 50, Combat: true},
		}
		f.GC = true
		frames = append(frames, f)
	}
	combat := withSpan([]CombatRow{crow("melee", 6, gTank, mobGUID(tacCultist, 1), 0)}, 4, 12)
	r := eventRun("watch", map[string]string{gTank: "tank"}, frames, combat, nil, nil)
	withClass(r, gTank, "warrior")
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[3:4])
	if len(hits) != 0 {
		t.Fatalf("no death kind, yet %+v", hits)
	}
	if counts.SkipReasons["rule 19: log has no death rows"] != 1 || counts.Complete(19) {
		t.Fatalf("the skipped check is counted: %+v", counts)
	}
}

// TestTacticsSkipWithoutFile: no tactics file skips all four rules.
func TestTacticsSkipWithoutFile(t *testing.T) {
	r := testRun("watch", tacticsRoles(), frameAt(0, testUnit(gTank)))
	r.Combat = []CombatRow{crow("melee", 0, gTank, gid(0xB01), 0)}
	for _, rule := range TacticsRules(nil) {
		if got := rule.Skipped(r); got != "no tactics file" {
			t.Fatalf("rule %d: skip %q", rule.Number, got)
		}
	}
	if _, counts := Hunt([]*Run{r}, TacticsRules(nil)); counts.SkipReasons["rule 16: no tactics file"] != 1 {
		t.Fatalf("counts %+v", counts)
	}
}

// nonInterrupterClasses names the tank and healer as classes without an interrupt, so a rule-16 test judges one
// member: the rogue.
func nonInterrupterClasses(r *Run) *Run {
	withClass(r, gTank, "druid")
	withClass(r, gHeal, "druid")
	withClass(r, gDps, "rogue")
	return r
}

// interruptingRow is an interrupt row for a spell the cultist did not cast: the kind exists without answering the
// cast under test.
func interruptingRow(t float64) CombatRow {
	row := crow("interrupt", t, gDps, mobGUID(tacEnforcer, 2), 1766)
	row.InterruptedSpellID = 99991
	return row
}

// TestRule16InterrupterCooldown: the rogue's own Kick started 6 s before the cast, inside its 10 s cooldown, so it
// was not free; the tank and healer have no interrupt. No finding.
func TestRule16InterrupterCooldown(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		crow("spell_start", 4, gDps, mobGUID(tacCultist, 1), 1766),
		interruptingRow(4.5),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), tacticsRun(8), combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	nonInterrupterClasses(r)
	if hits := Rule16(r, tac); len(hits) != 0 {
		t.Fatalf("kick on cooldown, yet %+v", hits)
	}
}

// TestRule16NotFreeWhenStunned: the rogue's own spell_fail names a stun in the 2 s before the completion.
func TestRule16NotFreeWhenStunned(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		func() CombatRow {
			row := crow("spell_fail", 9, gDps, mobGUID(tacCultist, 1), 1766)
			row.Reason = "stunned"
			return row
		}(),
		interruptingRow(4.5),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), tacticsRun(8), combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	nonInterrupterClasses(r)
	if hits := Rule16(r, tac); len(hits) != 0 {
		t.Fatalf("stunned interrupter, yet %+v", hits)
	}
}

// TestRule16PerClassRange: the rogue 15 yd and the warrior 40 yd from the caster are outside Kick's and Pummel's
// 5 yd: the old 30 yd radius would call both free.
func TestRule16PerClassRange(t *testing.T) {
	tac := tacticsFile()
	frames := tacticsRun(8)
	for _, f := range frames {
		f.Units[gTank].X, f.Units[gTank].Y = 0, 40
		f.Units[gDps].X, f.Units[gDps].Y = 20, 0
	}
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		interruptingRow(4.5),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), frames, combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	withClass(r, gTank, "warrior")
	withClass(r, gDps, "rogue")
	withClass(r, gHeal, "druid")
	if hits := Rule16(r, tac); len(hits) != 0 {
		t.Fatalf("out of interrupt range, yet %+v", hits)
	}
}

// TestRule16MobNotInCombat: a caster the frames show out of combat cannot be judged (the tactics cast is a pull
// that did not happen): UNKNOWN, counted.
func TestRule16MobNotInCombat(t *testing.T) {
	tac := tacticsFile()
	frames := tacticsRun(8)
	for _, f := range frames {
		for _, m := range f.Mobs {
			m.Combat = false
		}
	}
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		interruptingRow(4.5),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), frames, combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	withTacticsClasses(r)
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[0:1])
	if len(hits) != 0 {
		t.Fatalf("mob out of combat, yet %+v", hits)
	}
	if counts.SkipReasons["rule 16: the caster's mob was not in combat"] != 1 || counts.Complete(16) {
		t.Fatalf("the skipped check is counted: %+v", counts)
	}
}

// TestRule16OneHitPerCast: a cast's spell_go rows (one per target) are one event.
func TestRule16OneHitPerCast(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		crow("spell_go", 10, mobGUID(tacCultist, 1), gDps, tacHeal),
		interruptingRow(4.5),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), tacticsRun(8), combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	withTacticsClasses(r)
	if hits := Rule16(r, tac); len(hits) != 1 {
		t.Fatalf("one cast, %+v", hits)
	}
}

// TestRule17OneHitPerCast: a spell_go and its immune row at the same moment are one cast, one finding.
func TestRule17OneHitPerCast(t *testing.T) {
	tac := tacticsFile()
	tac.CCSpells[tacPoly] = []string{"polymorph", "stun"}
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, gHeal, mobGUID(tacEnforcer, 2), tacPoly),
		crow("immune", 10, gHeal, mobGUID(tacEnforcer, 2), tacPoly),
	}, 6, 12)
	r := eventRun("watch", tacticsRoles(), tacticsRun(8), combat, map[int]string{tacPoly: "Polymorph"}, nil)
	withTacticsClasses(r)
	hits := Rule17(r, tac)
	if len(hits) != 1 || !strings.Contains(hits[0].Ev, "immune@10") {
		t.Fatalf("one cast: %+v", hits)
	}
}

// TestRule17AoECastInnocent: one cast that hit two enforcers is an AoE; the immune one's share is not a wrong try.
func TestRule17AoECastInnocent(t *testing.T) {
	tac := tacticsFile()
	tac.CCSpells[tacPoly] = []string{"polymorph", "stun"}
	frames := tacticsRun(8)
	for _, f := range frames {
		f.Mobs = append(f.Mobs, &Mob{G: mobGUID(tacEnforcer, 3), Entry: tacEnforcer, HPPct: 100,
			X: 9, Y: 0, Z: 50, Combat: true})
	}
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, gHeal, mobGUID(tacEnforcer, 2), tacPoly),
		crow("spell_go", 10, gHeal, mobGUID(tacEnforcer, 3), tacPoly),
	}, 6, 12)
	r := eventRun("watch", tacticsRoles(), frames, combat, map[int]string{tacPoly: "Polymorph"}, nil)
	withTacticsClasses(r)
	if hits := Rule17(r, tac); len(hits) != 0 {
		t.Fatalf("AoE cast, yet %+v", hits)
	}
}

// debuffFrames is a fight where the healer is the victim and a paladin stands six yards away, able to dispel
// magic. The enforcer is in the frames so the rules know its entry.
func debuffFrames() []*Frame {
	var frames []*Frame
	for t := 0.0; t <= 18; t += 2 {
		tank := testUnit(gTank)
		tank.Combat, tank.X = true, 2
		priest := testUnit(gHeal)
		if t >= 2 && t < 14 {
			priest.Auras = []uint32{tacDebuff}
		}
		dps := testUnit(gDps3)
		dps.X = 6
		f := frameAt(t, tank, priest, dps)
		f.Mobs = []*Mob{
			{G: mobGUID(tacCultist, 1), Entry: tacCultist, HPPct: 100, X: 5, Y: 0, Z: 50, Combat: true},
			{G: mobGUID(tacEnforcer, 2), Entry: tacEnforcer, HPPct: 100, X: 8, Y: 0, Z: 50, Combat: true},
		}
		f.GC = true
		frames = append(frames, f)
	}
	return frames
}

// debuffRun is the fixture's run: priest victim, paladin dispeller, warrior tank.
func debuffRun(combat []CombatRow) *Run {
	r := eventRun("watch", map[string]string{gHeal: "healer", gTank: "tank", gDps3: "dps"}, debuffFrames(),
		combat, map[int]string{tacDebuff: "Hex"}, map[int]string{tacDebuff: "magic"})
	withClass(r, gHeal, "priest")
	withClass(r, gTank, "warrior")
	withClass(r, gDps3, "paladin")
	return r
}

// TestRule18AuraRemovedEndsEpisode: the aura came off at t=6, four seconds after the cast: inside the 8 s limit.
func TestRule18AuraRemovedEndsEpisode(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 2, mobGUID(tacCultist, 1), gHeal, tacDebuff),
		crow("aura_removed", 6, mobGUID(tacCultist, 1), gHeal, tacDebuff),
	}, -2, 20)
	if hits := Rule18(debuffRun(combat), tac); len(hits) != 0 {
		t.Fatalf("removed inside the limit, yet %+v", hits)
	}
}

// TestRule18NaturalExpiry: the aura row says six seconds remained at t=2, so it expired at 8: inside the limit,
// with no removal row at all.
func TestRule18NaturalExpiry(t *testing.T) {
	tac := tacticsFile()
	applied := crow("aura_applied", 2, mobGUID(tacCultist, 1), gHeal, tacDebuff)
	applied.RemainingMS = 6000
	combat := withSpan([]CombatRow{
		applied,
		crow("aura_removed", 1, mobGUID(tacCultist, 1), gTank, 99999),
	}, -2, 20)
	if hits := Rule18(debuffRun(combat), tac); len(hits) != 0 {
		t.Fatalf("expired inside the limit, yet %+v", hits)
	}
}

// TestRule18VictimDeathEndsEpisode: the victim died four seconds in: no living member to dispel.
func TestRule18VictimDeathEndsEpisode(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 2, mobGUID(tacCultist, 1), gHeal, tacDebuff),
		crow("death", 6, gHeal, gHeal, 0),
		crow("aura_removed", 1, mobGUID(tacCultist, 1), gTank, 99999),
	}, -2, 20)
	if hits := Rule18(debuffRun(combat), tac); len(hits) != 0 {
		t.Fatalf("victim died inside the limit, yet %+v", hits)
	}
}

// TestRule18CasterOwnLimit: the enforcer lists the same spell with a 30 s limit; its 12 s application is inside
// it. The cultist's 8 s limit (the strictest in the file) must not judge the enforcer's cast.
func TestRule18CasterOwnLimit(t *testing.T) {
	tac := tacticsFile()
	tac.Creatures[tacEnforcer].MustDispel = []TacticDispel{{Spell: tacDebuff, Type: "magic", WithinS: 30}}
	combat := withSpan([]CombatRow{
		crow("spell_go", 2, mobGUID(tacEnforcer, 2), gHeal, tacDebuff),
		func() CombatRow {
			row := crow("dispel", 14, gDps3, gHeal, 527)
			row.Dispelled = []DispelledAura{{SpellID: tacDebuff, Harmful: true}}
			return row
		}(),
	}, -2, 20)
	if hits := Rule18(debuffRun(combat), tac); len(hits) != 0 {
		t.Fatalf("the caster's own 30 s limit, yet %+v", hits)
	}
}

// TestRule18StillUpAtEnd: the recording has aura_removed rows for another spell, so an aura with none is still up
// at the log's end: the 8 s limit is passed.
func TestRule18StillUpAtEnd(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 2, mobGUID(tacCultist, 1), gHeal, tacDebuff),
		crow("aura_removed", 1, mobGUID(tacCultist, 1), gTank, 99999),
	}, -2, 20)
	hits := Rule18(debuffRun(combat), tac)
	if len(hits) != 1 || !strings.Contains(hits[0].Ev, "still up at the end") {
		t.Fatalf("still up: %+v", hits)
	}
}

// sameEntryFrames shows three enforcers and the cultist, all in combat, for the whole fight.
func sameEntryFrames() []*Frame {
	var frames []*Frame
	for t := 0.0; t <= 16; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		f := frameAt(t, tank)
		f.Mobs = []*Mob{
			{G: mobGUID(tacCultist, 1), Entry: tacCultist, HPPct: 100, X: 5, Y: 0, Z: 50, Combat: true},
			{G: mobGUID(tacEnforcer, 2), Entry: tacEnforcer, HPPct: 100, X: 8, Y: 0, Z: 50, Combat: true},
			{G: mobGUID(tacEnforcer, 3), Entry: tacEnforcer, HPPct: 100, X: 9, Y: 0, Z: 50, Combat: true},
			{G: mobGUID(tacEnforcer, 4), Entry: tacEnforcer, HPPct: 100, X: 10, Y: 0, Z: 50, Combat: true},
		}
		f.GC = true
		frames = append(frames, f)
	}
	return frames
}

// TestRule19SameEntryPerGUID: the two enforcer GUIDs die at their own times; the living one is not the kill.
func TestRule19SameEntryPerGUID(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("death", 8, mobGUID(tacEnforcer, 2), mobGUID(tacEnforcer, 2), 0),
		crow("death", 12, mobGUID(tacEnforcer, 3), mobGUID(tacEnforcer, 3), 0),
	}, 6, 14)
	r := eventRun("watch", map[string]string{gTank: "tank"}, sameEntryFrames(), combat, nil, nil)
	withClass(r, gTank, "warrior")
	hits := Rule19(r, tac)
	if len(hits) != 2 {
		t.Fatalf("two kills, %+v", hits)
	}
	if hits[0].T != 8 || hits[1].T != 12 || hits[0].Key[2] == hits[1].Key[2] {
		t.Fatalf("per-guid kills: %+v", hits)
	}
}

// TestRule19UnpulledHigherMob: the cultist is in the frames but never in combat: there is no pull to order.
func TestRule19UnpulledHigherMob(t *testing.T) {
	tac := tacticsFile()
	frames := sameEntryFrames()
	for _, f := range frames {
		for _, m := range f.Mobs {
			if m.Entry == tacCultist {
				m.Combat = false
			}
		}
	}
	combat := withSpan([]CombatRow{
		crow("death", 8, mobGUID(tacEnforcer, 2), mobGUID(tacEnforcer, 2), 0),
	}, 6, 14)
	r := eventRun("watch", map[string]string{gTank: "tank"}, frames, combat, nil, nil)
	withClass(r, gTank, "warrior")
	if hits := Rule19(r, tac); len(hits) != 0 {
		t.Fatalf("unpulled mob, yet %+v", hits)
	}
}

// TestRule19HigherOutOfGroupReach: the cultist stands 5 yd from the enforcer (the old mob-to-mob reach) but 50 yd
// from the tank: the group could not have killed it first.
func TestRule19HigherOutOfGroupReach(t *testing.T) {
	tac := tacticsFile()
	frames := sameEntryFrames()
	for _, f := range frames {
		for _, m := range f.Mobs {
			switch m.Entry {
			case tacCultist:
				m.X = 50
			case tacEnforcer:
				m.X = 45
			}
		}
	}
	combat := withSpan([]CombatRow{
		crow("death", 8, mobGUID(tacEnforcer, 2), mobGUID(tacEnforcer, 2), 0),
	}, 6, 14)
	r := eventRun("watch", map[string]string{gTank: "tank"}, frames, combat, nil, nil)
	withClass(r, gTank, "warrior")
	if hits := Rule19(r, tac); len(hits) != 0 {
		t.Fatalf("out of group reach, yet %+v", hits)
	}
}

// TestRule18UnlistedCaster: an unlisted mob casting the same spell is not judged: the spell's listing is not that
// entry's tactic.
func TestRule18UnlistedCaster(t *testing.T) {
	tac := tacticsFile()
	unknown := mobGUID(999, 7)
	frames := debuffFrames()
	for _, f := range frames {
		f.Mobs = append(f.Mobs, &Mob{G: unknown, Entry: 999, HPPct: 100, X: 7, Y: 0, Z: 50, Combat: true})
	}
	combat := withSpan([]CombatRow{
		crow("spell_go", 2, unknown, gHeal, tacDebuff),
		func() CombatRow {
			row := crow("dispel", 14, gDps3, gHeal, 527)
			row.Dispelled = []DispelledAura{{SpellID: tacDebuff, Harmful: true}}
			return row
		}(),
	}, -2, 20)
	r := eventRun("watch", map[string]string{gHeal: "healer", gTank: "tank", gDps3: "dps"}, frames,
		combat, map[int]string{tacDebuff: "Hex"}, map[int]string{tacDebuff: "magic"})
	withClass(r, gHeal, "priest")
	withClass(r, gTank, "warrior")
	withClass(r, gDps3, "paladin")
	if hits := Rule18(r, tac); len(hits) != 0 {
		t.Fatalf("unlisted caster, yet %+v", hits)
	}
}

// TestRule16UnknownWithoutClass: no member's class is known, so "nobody could interrupt" is not certain: UNKNOWN.
func TestRule16UnknownWithoutClass(t *testing.T) {
	tac := tacticsFile()
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacHeal),
		interruptingRow(4.5),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), tacticsRun(8), combat, map[int]string{tacHeal: "Dark Heal"}, nil)
	for _, g := range []string{gTank, gDps, gHeal} {
		delete(r.Classes, g)
	}
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[0:1])
	if len(hits) != 0 {
		t.Fatalf("unknown classes, yet %+v", hits)
	}
	if counts.SkipReasons["rule 16: a member's class is unknown"] != 1 || counts.Complete(16) {
		t.Fatalf("the skipped check is counted: %+v", counts)
	}
}
