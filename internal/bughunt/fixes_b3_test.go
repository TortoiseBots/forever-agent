package bughunt

// The fix round after B3's two reviews: each finding has a test that failed against the first B3 commit.

import (
	"path/filepath"
	"strings"
	"testing"
)

// idleDps is a fight frame set (tank swinging, one dps silent) over 0..16 s, and the rows of the tank.
func idleDps(apply func(dps *Unit)) ([]*Frame, []CombatRow) {
	var frames []*Frame
	var rows []CombatRow
	for t := 0.0; t <= 16; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		dps := testUnit(gDps)
		if apply != nil {
			apply(dps)
		}
		frames = append(frames, frameAt(t, tank, dps))
		rows = append(rows, crow("melee", t, gTank, gid(0xB01), 0))
	}
	return frames, rows
}

func idleRun(frames []*Frame, rows []CombatRow) *Run {
	r := eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, frames, rows, nil, nil)
	return withClass(r, gDps, "mage")
}

// TestFinding1BaselineGoneOnlyWhenTheRuleRan: a baseline ticket turns gone only when its rule ran with all its
// inputs; a skipped rule and a rule that was not selected leave the ticket as it was.
func TestFinding1BaselineGoneOnlyWhenTheRuleRan(t *testing.T) {
	// The run has no combat log: rules 1 and 4 are skipped; rule 15 ran (it needs only frames and the dispel file).
	r := testRun("watch", map[string]string{gHeal: "healer", gTank: "tank"}, frameAt(0, testUnit(gHeal), testUnit(gTank)))
	withClass(r, gHeal, "priest")
	r.Names, r.Dispel = map[int]string{1: "x"}, map[int]string{17172: "magic"}
	hits, counts := Hunt([]*Run{r}, EventRules())
	mk := func(rule int, key ...string) Ticket {
		return Ticket{ID: TicketID(rule, key), Rule: rule, Key: key, Count: 2, Runs: 1, Cost: 9, Evidence: []string{"x"}, Status: "known"}
	}
	base := []Ticket{mk(1, "1", "dps"), mk(4, "4", "Rain of Fire"), mk(15, "15", "magic", "Hex"), mk(9, "9", "z-drop")}
	got := map[int]Ticket{}
	for _, tk := range BuildTickets(hits, base, completeOf(counts)) {
		got[tk.Rule] = tk
	}
	if got[15].Status != "gone" || got[15].Count != 0 {
		t.Fatalf("rule 15 ran in full, its ticket is gone: %+v", got[15])
	}
	for _, rule := range []int{1, 4, 9} { // skipped (1, 4) and not selected (9)
		if got[rule].Status != "known" || got[rule].Count != 2 || got[rule].Cost != 9 {
			t.Fatalf("rule %d did not run, its ticket keeps its state: %+v", rule, got[rule])
		}
	}
}

// TestFinding2LogRulesNeedACoveredWindow: a log whose times are unknown, whose tail is cut, whose sequence has a
// hole or that stops before the window answers nothing; the whole log is the control that gives the hit.
func TestFinding2LogRulesNeedACoveredWindow(t *testing.T) {
	// fightRows is the log in file order: a mob row at -2, the tank's swings up to upTo, mid in the middle (after 6 s),
	// and a mob row at 18 when the log runs on past the fight.
	fightRows := func(upTo float64, mid ...CombatRow) []CombatRow {
		rows := []CombatRow{crow("melee", -2, gid(0xB09), gid(0xB01), 0)}
		for at := 0.0; at <= upTo; at += 2 {
			rows = append(rows, crow("melee", at, gTank, gid(0xB01), 0))
			if at == 6 {
				rows = append(rows, mid...)
			}
		}
		if upTo >= 16 {
			rows = append(rows, crow("melee", 18, gid(0xB09), gid(0xB01), 0))
		}
		return rows
	}
	hitsOf := func(rows []CombatRow) ([]Hit, Counts) {
		frames, _ := idleDps(nil)
		return Hunt([]*Run{idleRun(frames, rows)}, []Rule{EventRules()[0]})
	}
	// Control: a timed log spanning the whole fight proves the silence.
	if hits, _ := hitsOf(fightRows(16)); len(hits) != 1 {
		t.Fatalf("control: %+v", hits)
	}
	cases := []struct {
		name string
		rows []CombatRow
		why  string
	}{
		{"no timed row (a run with no start)", []CombatRow{{Kind: "melee", Source: CombatActor{GUID: gTank}}}, "no timed rows"},
		{"log ends before the window", fightRows(6), "does not cover the window"},
		{"truncation marker in the fight", fightRows(16, CombatRow{Kind: "truncated", T: 6, TKnown: true}), "does not cover the window"},
		{"sequence hole in the fight", []CombatRow{
			{Seq: 1, Kind: "melee", T: -2, TKnown: true, Source: CombatActor{GUID: gid(0xB09)}},
			{Seq: 40, Kind: "melee", T: 18, TKnown: true, Source: CombatActor{GUID: gid(0xB09)}}}, "does not cover the window"},
		{"untimed row in the fight", fightRows(16, CombatRow{Kind: "spell_go", Source: CombatActor{GUID: gDps}}), "does not cover the window"},
	}
	for _, c := range cases {
		hits, counts := hitsOf(c.rows)
		if len(hits) != 0 {
			t.Fatalf("%s: the log proves nothing, yet %+v", c.name, hits)
		}
		found := false
		for k := range counts.SkipReasons {
			found = found || (strings.HasPrefix(k, "rule 1:") && strings.Contains(k, c.why))
		}
		if !found || counts.Complete(1) {
			t.Fatalf("%s: the skipped check is counted: %+v", c.name, counts)
		}
	}
}

// TestFinding3AuraOutOfViewIsUnknown: a debuff whose member leaves the commander's view is not a missed decurse and
// not a dispel: the episode is unknown from there, silent and counted.
func TestFinding3AuraOutOfViewIsUnknown(t *testing.T) {
	build := func(priestInFrame func(t float64) bool) *Run {
		var frames []*Frame
		for t := 0.0; t <= 14; t += 2 {
			tank := testUnit(gTank)
			tank.Combat = true
			paladin := testUnit(gDps3)
			paladin.X = 6
			units := []*Unit{tank, paladin}
			if priestInFrame(t) {
				priest := testUnit(gHeal)
				priest.Auras = []uint32{17172}
				units = append(units, priest)
			}
			frames = append(frames, frameAt(t, units...))
		}
		r := eventRun("watch", map[string]string{gHeal: "healer", gTank: "tank", gDps3: "dps"}, frames, nil,
			map[int]string{17172: "Hex"}, map[int]string{17172: "magic"})
		withClass(r, gHeal, "priest")
		withClass(r, gTank, "warrior")
		withClass(r, gDps3, "paladin")
		return r
	}
	// Control: in view for 14 s with the aura on to the end is a hit.
	if hits := Rule15(build(func(float64) bool { return true })); len(hits) != 1 {
		t.Fatalf("control: %+v", hits)
	}
	// The member leaves view after 12 s with the aura on: unknown, never a miss.
	r := build(func(t float64) bool { return t <= 12 })
	hits, counts := Hunt([]*Run{r}, []Rule{EventRules()[5]})
	if len(hits) != 0 {
		t.Fatalf("a unit leaving view is not a debuff ending: %+v", hits)
	}
	if counts.SkipReasons["rule 15: "+whyOutOfView] != 1 || counts.Complete(15) {
		t.Fatalf("unknown episode is counted: %+v", counts)
	}
}

// TestFinding4MealsAreNotIdleOrNotHealing: a bot eating, drinking or resting is rule 6's: rules 1 and 2 stay
// silent for it.
func TestFinding4MealsAreNotIdleOrNotHealing(t *testing.T) {
	for name, apply := range map[string]func(u *Unit){
		"drink": func(u *Unit) { u.Drink = true }, "eat": func(u *Unit) { u.Eat = true }, "rest": func(u *Unit) { u.Rest = true },
	} {
		frames, rows := idleDps(apply)
		if hits := Rule1(idleRun(frames, withSpan(rows, -2, 18))); len(hits) != 0 {
			t.Fatalf("rule 1, %s: %+v", name, hits)
		}
	}
	frames, rows := idleDps(nil)
	if hits := Rule1(idleRun(frames, withSpan(rows, -2, 18))); len(hits) != 1 {
		t.Fatalf("rule 1 control: %+v", hits)
	}
	build := func(apply func(u *Unit)) *Run {
		var frames []*Frame
		for t := 0.0; t <= 6; t++ {
			tank := testUnit(gTank)
			tank.Combat, tank.HP, tank.X = true, 30, 10
			heal := testUnit(gHeal)
			apply(heal)
			frames = append(frames, frameAt(t, tank, heal))
		}
		r := eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, frames,
			withSpan([]CombatRow{crow("melee", 0, gTank, gid(0xB01), 0)}, -2, 8), map[int]string{2050: "Flash of Light"}, nil)
		return withClass(r, gHeal, "priest")
	}
	if hits := Rule2(build(func(u *Unit) { u.Drink = true })); len(hits) != 0 {
		t.Fatalf("rule 2, drinking healer: %+v", hits)
	}
	if hits := Rule2(build(func(u *Unit) {})); len(hits) != 1 {
		t.Fatalf("rule 2 control: %+v", hits)
	}
}

// TestFinding5MalformedCombatLogLineIsAnError: the log is a required input; a bad line names the file and line, for
// a watch and for a repeat, while a missing file is only no log and blank lines are fine.
func TestFinding5MalformedCombatLogLineIsAnError(t *testing.T) {
	dir := t.TempDir()
	good := `{"seq":1,"kind":"melee","time":"2023-11-14T22:13:25.000+00:00","source":{"guid":"0x1"},"target":{"guid":"0x2"}}`
	path := filepath.Join(dir, "x.combatlog.jsonl")
	writeFile(t, path, good+"\n\n"+good+"\n")
	if rows, err := readCombatLog(path); err != nil || len(rows) != 2 {
		t.Fatalf("good log: %v %d", err, len(rows))
	}
	if rows, err := readCombatLog(filepath.Join(dir, "none.jsonl")); err != nil || rows != nil {
		t.Fatalf("missing log: %v %v", err, rows)
	}
	for name, bad := range map[string]string{"cut json": `{"seq":2,"kind":"mel`, "not json": "garbage", "wrong type": `{"seq":"two"}`} {
		writeFile(t, path, good+"\n"+bad+"\n"+good+"\n")
		_, err := readCombatLog(path)
		if err == nil || !strings.Contains(err.Error(), "x.combatlog.jsonl:2") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Both loaders pass it on.
	watch := t.TempDir()
	tracePath := writeWatch(t, watch)
	logPath := strings.TrimSuffix(tracePath, ".jsonl") + ".combatlog.jsonl"
	writeFile(t, logPath, good+"\n{broken\n")
	if _, err := LoadWatch(watch, "ds"); err == nil || !strings.Contains(err.Error(), ".combatlog.jsonl:2") {
		t.Fatalf("watch: %v", err)
	}
	rep := filepath.Join(t.TempDir(), "healers", "healer-priest-heal-latency", "rep1")
	writeFile(t, filepath.Join(rep, "events.jsonl"), "")
	writeFile(t, filepath.Join(rep, "samples.jsonl"),
		`{"t": 0, "unit": "bot", "guid": "0x000000000000064e", "hp": 100, "max_hp": 100, "power": 50, "max_power": 100, `+
			`"x": 0, "y": 0, "z": 0, "wall": "2023-11-14T22:13:20+00:00", "auras": []}`+"\n")
	writeFile(t, filepath.Join(rep, "raw-ta.jsonl"), good+"\nnot a row\n")
	if _, err := LoadTrials(rep, "trials"); err == nil || !strings.Contains(err.Error(), "raw-ta.jsonl:2") {
		t.Fatalf("trials: %v", err)
	}
}

// TestFinding6DispelKeepsOnlyDebuffs: the core's dispel column lists buffs too; only an entry the names data marks
// harmful (or the entry itself) is dispellable, so a long buff beside a priest is no missed decurse.
func TestFinding6DispelKeepsOnlyDebuffs(t *testing.T) {
	dir := t.TempDir()
	names := filepath.Join(dir, "names.json")
	dispel := filepath.Join(dir, "dispel.json")
	writeFile(t, names, `{"21562": {"name": "Power Word: Fortitude"}, "17172": {"name": "Hex", "harmful": true}}`)
	writeFile(t, dispel, `{"21562": 1, "17172": 1, "99": {"dispel": "poison", "harmful": true}, "98": 2}`)
	nm, harmful, err := LoadNamesHarmful(names)
	if err != nil {
		t.Fatal(err)
	}
	d, err := LoadDispel(dispel, harmful)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 2 || d[17172] != "magic" || d[99] != "poison" {
		t.Fatalf("only debuffs: %v", d)
	}
	// A 14 s Fortitude (a magic-dispel buff in the column) beside a priest and a paladin is not a miss.
	var frames []*Frame
	for t := 0.0; t <= 14; t += 2 {
		tank := testUnit(gTank)
		tank.Combat = true
		tank.Auras = []uint32{21562}
		priest := testUnit(gHeal)
		frames = append(frames, frameAt(t, tank, priest))
	}
	r := eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, frames, nil, nm, d)
	withClass(r, gHeal, "priest")
	withClass(r, gTank, "warrior")
	if hits := Rule15(r); len(hits) != 0 {
		t.Fatalf("a buff is no debuff: %+v", hits)
	}
}

// TestFinding7TicketKeysHoldNoDerivedText: rule 8's aura-life word and rule 1's act word are in the body, not the
// key, so one finding keeps one id however the measured text flips.
func TestFinding7TicketKeysHoldNoDerivedText(t *testing.T) {
	names := map[int]string{21562: "Power Word: Fortitude"}
	rule8 := func(removedAfter float64) Hit {
		var rows []CombatRow
		for _, at := range []float64{0, 10, 20, 30} {
			rows = append(rows, crow("spell_start", at-0.2, gHeal, gTank, 21562), crow("spell_go", at, gHeal, gTank, 21562))
		}
		rows = append(rows, crow("aura_applied", 0.1, gHeal, gTank, 21562), crow("aura_removed", 0.1+removedAfter, gHeal, gTank, 21562))
		r := eventRun("watch", map[string]string{gHeal: "healer"}, nil, rows, names, nil)
		hits := Rule8(r)
		if len(hits) != 1 {
			t.Fatalf("rule 8: %+v", hits)
		}
		return hits[0]
	}
	short, long := rule8(5), rule8(29)
	if TicketID(8, short.Key) != TicketID(8, long.Key) {
		t.Fatalf("one finding, two ids: %v %v", short.Key, long.Key)
	}
	if !strings.Contains(short.Ev, "short-aura") || !strings.Contains(long.Ev, "long-aura") {
		t.Fatalf("the word stays in the body: %q %q", short.Ev, long.Ev)
	}
	act := func(word string) Hit {
		frames, rows := idleDps(func(u *Unit) { u.Act = word })
		hits := Rule1(idleRun(frames, withSpan(rows, -2, 18)))
		if len(hits) != 1 {
			t.Fatalf("rule 1 %q: %+v", word, hits)
		}
		return hits[0]
	}
	a, b := act("follow"), act("")
	if TicketID(1, a.Key) != TicketID(1, b.Key) {
		t.Fatalf("one finding, two ids: %v %v", a.Key, b.Key)
	}
	if !strings.Contains(a.Ev, "act follow") {
		t.Fatalf("the act stays in the body: %q", a.Ev)
	}
}

// TestFinding8GoneTicketThatReturnsIsReopened.
func TestFinding8GoneTicketThatReturnsIsReopened(t *testing.T) {
	hits := ticketHits(t)
	id := TicketID(15, []string{"15", "magic", "Hex"})
	base := []Ticket{{ID: id, Rule: 15, Key: []string{"15", "magic", "Hex"}, FirstSeen: "2023-01-01T00:00:00Z", Status: "gone"}}
	for _, tk := range BuildTickets(hits, base, nil) {
		if tk.ID == id && (tk.Status != "reopened" || tk.FirstSeen != "2023-01-01T00:00:00Z") {
			t.Fatalf("reopened: %+v", tk)
		}
	}
	md := TicketsMarkdown([]Ticket{{ID: "x", Status: "reopened", Rule: 1}})
	if !strings.Contains(md, "1 reopened") {
		t.Fatalf("summary: %s", md)
	}
}

// TestFinding9RecastListDropsSealsAndSliceAndDice: legitimate rotations recast these, so they are no flip-flop.
func TestFinding9RecastListDropsSealsAndSliceAndDice(t *testing.T) {
	for _, n := range []string{"Seal of Righteousness", "Seal of Command", "Slice and Dice"} {
		if buffRE.MatchString(n) {
			t.Fatalf("%s must not be a recast-spam buff", n)
		}
	}
	for _, n := range []string{"Power Word: Fortitude", "Blessing of Might", "Arcane Intellect"} {
		if !buffRE.MatchString(n) {
			t.Fatalf("%s must stay", n)
		}
	}
}

// TestFinding10LoaderErrorLineIsDeterministic: with several bad entries the first in sorted order is reported every
// time, and a value equal to a key does not misplace the line.
func TestFinding10LoaderErrorLineIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "names.json")
	writeFile(t, path, "{\n \"5\": \"ok\",\n \"zz\": \"a\",\n \"aa\": \"b\",\n \"mm\": \"c\"\n}")
	for i := 0; i < 30; i++ {
		_, err := LoadNames(path)
		if err == nil || !strings.Contains(err.Error(), "names.json:4:") {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	dpath := filepath.Join(dir, "dispel.json")
	writeFile(t, dpath, "{\n \"9\": \"holy\",\n \"3\": \"unholy\"\n}")
	for i := 0; i < 30; i++ {
		_, err := LoadDispel(dpath, allHarmful(9, 3))
		if err == nil || !strings.Contains(err.Error(), "dispel.json:3:") {
			t.Fatalf("dispel run %d: %v", i, err)
		}
	}
	vpath := filepath.Join(dir, "value.json")
	writeFile(t, vpath, "{\n \"1\": \"x\",\n \"x\": \"1\"\n}")
	if _, err := LoadNames(vpath); err == nil || !strings.Contains(err.Error(), "value.json:3:") {
		t.Fatalf("value equal to key: %v", err)
	}
}

// TestFinding11EpisodeSpanIsCheckedNotOnlyTheFrames: frames up to MAX_GAP_S apart leave a hole between samples; a
// cast, heal or output inside the hole ends the episode's claim.
func TestFinding11EpisodeSpanIsCheckedNotOnlyTheFrames(t *testing.T) {
	// Rule 1: frames at 0, 2, 4 and 16; the member casts at 10, between the samples.
	var frames []*Frame
	for _, t := range []float64{0, 2, 4, 16} {
		tank := testUnit(gTank)
		tank.Combat = true
		frames = append(frames, frameAt(t, tank, testUnit(gDps)))
	}
	rows := withSpan([]CombatRow{crow("melee", 4, gTank, gid(0xB01), 0), crow("melee", 16, gTank, gid(0xB01), 0)}, -2, 18)
	if hits := Rule1(idleRun(frames, rows)); len(hits) != 1 {
		t.Fatalf("rule 1 control: %+v", hits)
	}
	cast := append(append([]CombatRow(nil), rows...), crow("spell_go", 10, gDps, gid(0xB01), 133))
	if hits := Rule1(idleRun(frames, cast)); len(hits) != 0 {
		t.Fatalf("rule 1, cast in the hole: %+v", hits)
	}
	// Rule 2: frames at 0 and 8; the healer heals at 4.
	var hf []*Frame
	for _, t := range []float64{0, 8} {
		tank := testUnit(gTank)
		tank.Combat, tank.HP, tank.X = true, 30, 10
		hf = append(hf, frameAt(t, tank, testUnit(gHeal)))
	}
	heal := func(rows []CombatRow) *Run {
		r := eventRun("watch", map[string]string{gTank: "tank", gHeal: "healer"}, hf, withSpan(rows, -2, 10),
			map[int]string{2050: "Flash of Light"}, nil)
		return withClass(r, gHeal, "priest")
	}
	if hits := Rule2(heal(nil)); len(hits) != 1 {
		t.Fatalf("rule 2 control: %+v", hits)
	}
	if hits := Rule2(heal([]CombatRow{crow("heal", 4, gHeal, gTank, 2050)})); len(hits) != 0 {
		t.Fatalf("rule 2, heal in the hole: %+v", hits)
	}
	// Rule 11: frames at 0 and 12 with the mana rising; the member lands a spell at 6.
	var cf []*Frame
	for i, t := range []float64{0, 12} {
		tank := testUnit(gTank)
		tank.Combat = true
		dps := testUnit(gDps)
		dps.Power = 50 + float64(i)*10
		cf = append(cf, frameAt(t, tank, dps))
	}
	starts := []CombatRow{crow("spell_start", 2, gDps, gid(0xB01), 116), crow("spell_start", 8, gDps, gid(0xB01), 116)}
	cancel := func(extra ...CombatRow) *Run {
		r := eventRun("watch", map[string]string{gTank: "tank", gDps: "dps"}, cf,
			withSpan(append(append([]CombatRow(nil), starts...), extra...), -2, 16), map[int]string{116: "Frostbolt"}, nil)
		return withClass(r, gDps, "mage")
	}
	if hits := Rule11(cancel()); len(hits) != 1 {
		t.Fatalf("rule 11 control: %+v", hits)
	}
	if hits := Rule11(cancel(crow("spell_damage", 6, gDps, gid(0xB01), 116))); len(hits) != 0 {
		t.Fatalf("rule 11, output in the hole: %+v", hits)
	}
}
