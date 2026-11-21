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
		frames = append(frames, frameAt(t, tank, priest, dps))
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

// TestRule18UnknownWithoutDispelRows: an older recording with no dispel kind is UNKNOWN, counted.
func TestRule18UnknownWithoutDispelRows(t *testing.T) {
	tac := tacticsFile()
	frames := tacticsRun(8)
	combat := withSpan([]CombatRow{
		crow("spell_go", 10, mobGUID(tacCultist, 1), gTank, tacDebuff),
	}, 6, 14)
	r := eventRun("watch", tacticsRoles(), frames, combat, nil, nil)
	withTacticsClasses(r)
	hits, counts := Hunt([]*Run{r}, TacticsRules(tac)[2:3])
	if len(hits) != 0 {
		t.Fatalf("no dispel kind, yet %+v", hits)
	}
	if counts.SkipReasons["rule 18: log has no dispel rows"] != 1 || counts.Complete(18) {
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

// TestRule19UnknownWithoutDatedKill: both mobs share the pull but neither has a dated kill: UNKNOWN, counted.
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
		t.Fatalf("undated kill, yet %+v", hits)
	}
	if counts.SkipReasons["rule 19: a kill has no dated death row or frame"] == 0 || counts.Complete(19) {
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
