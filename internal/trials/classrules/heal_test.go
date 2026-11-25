package classrules

// Synthetic recordings for the healer-assignment rule: one that PASSes, the FAIL cases each
// threshold names, the fight-window and excluded-span cases, and every INCONCLUSIVE case the rule
// must report instead of a FAIL. The party is a tank, the hired healer, a second healer and a damage
// dealer, so both "the main healer on the tanks" and "the other healers on the rest" have something
// to measure.

import (
	"strings"
	"testing"
)

// testParty is the party every case below stages by changing one member's role.
func testParty(playerRole, botRole, extra1Role, extra2Role string) []Member {
	return []Member{
		{Unit: "$player", Name: "Atank", GUID: "0x01", Class: "warrior", Role: playerRole},
		{Unit: "$bot", Name: "Bheal", GUID: "0x02", Class: "priest", Spec: "holy", Role: botRole},
		{Unit: "$extra1", Name: "Cheal", GUID: "0x03", Class: "paladin", Spec: "holy", Role: extra1Role},
		{Unit: "$extra2", Name: "Ddps", GUID: "0x04", Class: "druid", Spec: "balance", Role: extra2Role},
	}
}

func healerParty() []Member { return testParty(RoleTank, RoleHealer, RoleHealer, RoleDPS) }

// withFight gives a case the fight every case below fights in unless it stages the window itself:
// 240 s is well over MinFightSeconds.
func withFight(rec Record) Record {
	rec.Window = &[2]float64{0, 240}
	return rec
}

// healsOf is n heals of `amount` by one unit on `dst`, all with a readable amount.
func healsOf(unit, dst string, n int, amount float64) []Heal {
	return healsAt(unit, dst, n, amount, 0)
}

// healsAt is healsOf starting at the given second.
func healsAt(unit, dst string, n int, amount, start float64) []Heal {
	out := make([]Heal, 0, n)
	for i := range n {
		out = append(out, Heal{At: start + float64(i), Src: unit, Dst: dst, Amount: amount, AmountKnown: true})
	}
	return out
}

// spread is one healer's healing over several targets: one heal of `amount` per target entry.
func spread(unit string, amount float64, targets ...string) []Heal {
	out := make([]Heal, 0, len(targets))
	for i, dst := range targets {
		out = append(out, Heal{At: float64(i), Src: unit, Dst: dst, Amount: amount, AmountKnown: true})
	}
	return out
}

// restHeals is one healer's healing split over the two non-tank party members (the other healer
// and the damage dealer), which is the "rest of the party" the rule wants covered.
func restHeals(unit string, n int, amount float64) []Heal {
	half := n / 2
	out := healsOf(unit, "$extra2", half, amount)
	return append(out, healsOf(unit, "$bot", n-half, amount)...)
}

// passRecording is the rule's PASS case: the main healer keeps its healing on the tank while the
// other healer works the rest of the party.
func passRecording() Record {
	heals := healsOf("$bot", "$player", 20, 100)
	heals = append(heals, healsOf("$extra1", "$player", 2, 100)...)
	heals = append(heals, restHeals("$extra1", 8, 100)...)
	return withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
}

func concat(parts ...[]Heal) []Heal {
	var out []Heal
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func TestHealAssignmentVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		rec     Record
		verdict string
		reason  string // a fragment the reason must carry
	}{
		{name: "main healer on the tanks, other healer on the rest", rec: passRecording(), verdict: VerdictPass},
		{
			name: "main healer keeps 42 % of its healing on the tank",
			rec: withFight(Record{Members: healerParty(), EventsKnown: true, Heals: concat(
				healsOf("$bot", "$player", 5, 100),
				healsOf("$bot", "$extra2", 7, 100),
				healsOf("$extra1", "$player", 2, 100),
				restHeals("$extra1", 8, 100),
			)}),
			verdict: VerdictFail, reason: "main healer sends 42%",
		},
		{
			name: "other healer piles 70 % on the tanks",
			rec: withFight(Record{Members: healerParty(), EventsKnown: true, Heals: concat(
				healsOf("$bot", "$player", 20, 100),
				healsOf("$extra1", "$player", 7, 100),
				spread("$extra1", 100, "$extra2", "$extra2", "$bot"),
			)}),
			verdict: VerdictFail, reason: "other healer $extra1 (Cheal) healer sends 70%",
		},
		{
			name: "other healer never leaves the tank",
			rec: withFight(Record{Members: healerParty(), EventsKnown: true, Heals: concat(
				healsOf("$bot", "$player", 20, 100),
				healsOf("$extra1", "$player", 2, 100),
				healsOf("$extra1", "$extra2", 8, 100),
			)}),
			verdict: VerdictFail, reason: "1 non-tank ally",
		},
		{name: "no events file", rec: Record{Members: healerParty()}, verdict: VerdictInconclusive, reason: "no events"},
		{name: "no heal rows", rec: withFight(Record{Members: healerParty(), EventsKnown: true}), verdict: VerdictInconclusive, reason: "no heal rows"},
		{
			name: "too few heals",
			rec: withFight(Record{Members: healerParty(), EventsKnown: true,
				Heals: concat(healsOf("$bot", "$player", 5, 100), healsOf("$extra1", "$extra2", 5, 100))}),
			verdict: VerdictInconclusive, reason: "too few placed heals",
		},
		{
			name: "one healer only",
			rec: withFight(Record{Members: testParty(RoleTank, RoleHealer, RoleDPS, RoleDPS), EventsKnown: true,
				Heals: healsOf("$bot", "$player", 20, 100)}),
			verdict: VerdictInconclusive, reason: "one healer only",
		},
		{
			name: "no tank role in the party",
			rec: withFight(Record{Members: testParty(RoleDPS, RoleHealer, RoleHealer, RoleDPS), EventsKnown: true,
				Heals: passRecording().Heals}),
			verdict: VerdictInconclusive, reason: "no tank role",
		},
		{
			name:    "no healer role in the party",
			rec:     withFight(Record{Members: testParty(RoleTank, RoleDPS, RoleDPS, RoleDPS), EventsKnown: true, Heals: passRecording().Heals}),
			verdict: VerdictInconclusive, reason: "no party member carries the healer role",
		},
		{
			name: "the setup proves no guid for the tank",
			rec: func() Record {
				rec := passRecording()
				rec.Members = append([]Member(nil), rec.Members...)
				rec.Members[0].GUID = ""
				return rec
			}(),
			verdict: VerdictInconclusive, reason: "no guid for the tank",
		},
		{
			name: "the setup proves no guid for a healer",
			rec: func() Record {
				rec := passRecording()
				rec.Members = append([]Member(nil), rec.Members...)
				rec.Members[2].GUID = ""
				return rec
			}(),
			verdict: VerdictInconclusive, reason: "no guid for the healer",
		},
		{
			name: "a heal row without an amount",
			rec: func() Record {
				rec := passRecording()
				rec.Heals = append(rec.Heals, Heal{At: 30, Src: "$bot", Dst: "$player"})
				return rec
			}(),
			verdict: VerdictInconclusive, reason: "no readable amount",
		},
		{
			name: "two healers tie on healing",
			rec: withFight(Record{Members: healerParty(), EventsKnown: true, Heals: concat(
				healsOf("$bot", "$player", 10, 100),
				healsOf("$extra1", "$extra2", 10, 100),
			)}),
			verdict: VerdictInconclusive, reason: "no single main healer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := healAssignmentRow(tc.rec)
			if row.Verdict != tc.verdict {
				t.Fatalf("verdict %s, want %s (%s)\n%s", row.Verdict, tc.verdict, row.Reason,
					strings.Join(row.Evidence, "\n"))
			}
			if tc.verdict != VerdictPass && row.Reason == "" {
				t.Fatal("a non-pass row carries no reason")
			}
			if !strings.Contains(row.Reason, tc.reason) {
				t.Fatalf("reason %q does not carry %q", row.Reason, tc.reason)
			}
		})
	}
}

// TestHealAssignmentWindow: only the fight counts. A recording with no usable window is
// INCONCLUSIVE, a fight under MinFightSeconds is INCONCLUSIVE, and heals before the pull or inside
// an excluded span never set a share.
func TestHealAssignmentWindow(t *testing.T) {
	t.Run("no fight window", func(t *testing.T) {
		rec := passRecording()
		rec.Window = nil
		row := healAssignmentRow(rec)
		if row.Verdict != VerdictInconclusive || !strings.Contains(row.Reason, "no fight window") {
			t.Fatalf("verdict %s (%s), want INCONCLUSIVE on the missing window", row.Verdict, row.Reason)
		}
	})

	t.Run("a fight under the minimum", func(t *testing.T) {
		rec := passRecording()
		rec.Window = &[2]float64{0, MinFightSeconds - 1}
		row := healAssignmentRow(rec)
		if row.Verdict != VerdictInconclusive || !strings.Contains(row.Reason, "under the 30 s the rule needs") {
			t.Fatalf("verdict %s (%s), want INCONCLUSIVE on the short fight", row.Verdict, row.Reason)
		}
		rec.Window = &[2]float64{0, MinFightSeconds}
		if row := healAssignmentRow(rec); row.Verdict != VerdictPass {
			t.Fatalf("a fight at the minimum is %s, want PASS (%s)", row.Verdict, row.Reason)
		}
	})

	t.Run("heals outside the fight do not count", func(t *testing.T) {
		rec := passRecording()
		// Twenty more heals by the main healer on a non-tank before the pull: counted, they
		// would drop its tank share to 20/40 and fail it.
		rec.Heals = append(rec.Heals, healsAt("$bot", "$extra2", 20, 100, -20)...)
		row := healAssignmentRow(rec)
		if row.Verdict != VerdictPass {
			t.Fatalf("20 heals before the pull are %s, want PASS (%s)", row.Verdict, row.Reason)
		}
		if !strings.Contains(strings.Join(row.Evidence, "\n"), "not counted") {
			t.Fatalf("the evidence does not say rows were left out: %s", strings.Join(row.Evidence, "\n"))
		}
	})

	t.Run("excluded heals never count", func(t *testing.T) {
		// The main healer splits its healing 20/30 between the tank and one ally: 40% on the
		// tank, a FAIL. A span over the ally's 30 heals leaves only the tank's, a PASS.
		heals := concat(
			healsOf("$bot", "$player", 20, 100),
			healsAt("$bot", "$extra2", 30, 100, 20),
			restHeals("$extra1", 10, 100),
		)
		rec := withFight(Record{Members: healerParty(), EventsKnown: true, Heals: heals})
		if row := healAssignmentRow(rec); row.Verdict != VerdictFail {
			t.Fatalf("without the span the main healer's 40%% is %s, want FAIL", row.Verdict)
		}
		rec.Excluded = []Span{{A: 20, B: 60, Why: "gm_heal"}}
		row := healAssignmentRow(rec)
		if row.Verdict != VerdictPass {
			t.Fatalf("with the span the remaining 20 tank heals are %s, want PASS (%s)", row.Verdict, row.Reason)
		}
		if !strings.Contains(strings.Join(row.Evidence, "\n"), "excluded span") {
			t.Fatalf("the evidence does not say rows were excluded: %s", strings.Join(row.Evidence, "\n"))
		}
	})
}

// TestHealAssignmentOtherTargets: a heal on a pet or a unit the setup did not name is left out of
// the shares and counted apart, so one pet heal does not silence the rule; only placed heals below
// the floor make it INCONCLUSIVE.
func TestHealAssignmentOtherTargets(t *testing.T) {
	t.Run("a pet heal is left out, not evidence against the rule", func(t *testing.T) {
		rec := passRecording()
		rec.Heals = append(rec.Heals, Heal{At: 100, Src: "$bot", Dst: "", Amount: 500, AmountKnown: true})
		row := healAssignmentRow(rec)
		if row.Verdict != VerdictPass {
			t.Fatalf("one pet heal is %s, want PASS (%s)", row.Verdict, row.Reason)
		}
		joined := strings.Join(row.Evidence, "\n")
		if !strings.Contains(joined, "left out of the shares: 1") {
			t.Fatalf("the evidence does not carry the other count: %s", joined)
		}
		if row.K != 20 || row.N != 20 {
			t.Fatalf("k/n %d/%d, want the main healer's placed 20/20", row.K, row.N)
		}
	})

	t.Run("placed heals below the floor", func(t *testing.T) {
		// The main healer spends 20 heals on a pet and only 9 on the party.
		rec := withFight(Record{Members: healerParty(), EventsKnown: true, Heals: concat(
			healsOf("$bot", "", 20, 100),
			healsOf("$bot", "$player", 9, 100),
			restHeals("$extra1", 8, 100),
		)})
		row := healAssignmentRow(rec)
		if row.Verdict != VerdictInconclusive || !strings.Contains(row.Reason, "too few placed heals") {
			t.Fatalf("verdict %s (%s), want INCONCLUSIVE under the placed floor", row.Verdict, row.Reason)
		}
	})
}

// TestHealAssignmentThresholds pins each threshold at its boundary: the value at the threshold
// passes and the smallest step past it fails (or, for the heal floor, stops being judged). A
// changed constant breaks these cases, so a threshold edit cannot slip through.
func TestHealAssignmentThresholds(t *testing.T) {
	t.Run("main healer share at the threshold passes", func(t *testing.T) {
		// 60 of 100 heals on the tank is exactly MainHealerTankShare (60 %); the other healer's
		// 50 heals stay off the tank so the main healer keeps the larger total.
		heals := concat(
			healsOf("$bot", "$player", 60, 10),
			healsOf("$bot", "$extra2", 40, 10),
			restHeals("$extra1", 50, 10),
		)
		rec := withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictPass {
			t.Fatalf("60 %% on the tank is %s, want PASS (%s)", row.Verdict, row.Reason)
		}
		// 59 of 100 is one percent under it.
		heals = concat(
			healsOf("$bot", "$player", 59, 10),
			healsOf("$bot", "$extra2", 41, 10),
			restHeals("$extra1", 50, 10),
		)
		rec = withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictFail {
			t.Fatalf("59 %% on the tank is %s, want FAIL", row.Verdict)
		}
	})

	t.Run("other healer share at the threshold passes", func(t *testing.T) {
		// Four of ten heals on the tank is exactly OtherHealerTankShare (40 %), spread over two
		// allies.
		heals := concat(
			healsOf("$bot", "$player", 20, 10),
			healsOf("$extra1", "$player", 4, 10),
			spread("$extra1", 10, "$extra2", "$extra2", "$extra2", "$extra2", "$bot", "$bot"),
		)
		rec := withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictPass {
			t.Fatalf("40 %% on the tanks is %s, want PASS (%s)", row.Verdict, row.Reason)
		}
		// 41 of 100 is one percent over it (ten heals, so the other healer clears the heal floor).
		heals = concat(
			healsOf("$bot", "$player", 20, 10),
			spread("$extra1", 10, "$player", "$player", "$player", "$player"),
			spread("$extra1", 1, "$player"),
			spread("$extra1", 10, "$extra2", "$extra2", "$extra2"),
			spread("$extra1", 14.5, "$bot", "$bot"),
		)
		rec = withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictFail {
			t.Fatalf("41 %% on the tanks is %s, want FAIL", row.Verdict)
		}
	})

	t.Run("the heal floor judges exactly its own count", func(t *testing.T) {
		heals := concat(healsOf("$bot", "$player", 20, 100), restHeals("$extra1", MinHealerHeals, 100))
		rec := withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictPass {
			t.Fatalf("%d heals is %s, want a judged PASS (%s)", MinHealerHeals, row.Verdict, row.Reason)
		}
		heals = concat(healsOf("$bot", "$player", 20, 100), restHeals("$extra1", MinHealerHeals-1, 100))
		rec = withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictInconclusive {
			t.Fatalf("%d heals is %s, want INCONCLUSIVE", MinHealerHeals-1, row.Verdict)
		}
	})

	t.Run("the rest-target count at its floor passes", func(t *testing.T) {
		// Exactly MinRestTargets allies (the damage dealer and the other healer), with the tank
		// share at the other-healer limit so nothing else decides the verdict.
		heals := concat(
			healsOf("$bot", "$player", 20, 100),
			healsOf("$extra1", "$player", 4, 100),
			healsOf("$extra1", "$extra2", 3, 100),
			healsOf("$extra1", "$bot", 3, 100),
		)
		rec := withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
		if row := healAssignmentRow(rec); row.Verdict != VerdictPass {
			t.Fatalf("%d rest allies is %s, want PASS (%s)", MinRestTargets, row.Verdict, row.Reason)
		}
	})
}

// TestHealAssignmentEvidenceAndCounts: a PASS quotes the party and both healers' shares, says the
// shares are gross healing, and its k/n is the main healer's heals on the tanks over all its
// placed heals.
func TestHealAssignmentEvidenceAndCounts(t *testing.T) {
	row := healAssignmentRow(passRecording())
	joined := strings.Join(row.Evidence, "\n")
	for _, want := range []string{"party:", "main healer $bot (Bheal) healer", "other healer $extra1 (Cheal)",
		"gross amount", "no overheal"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence does not name %q:\n%s", want, joined)
		}
	}
	if row.K != 20 || row.N != 20 {
		t.Fatalf("k/n %d/%d, want the main healer's 20/20 heals on the tank", row.K, row.N)
	}
}

// TestHealAssignmentSelfHeals: a healer's heals on itself count in its own total but are neither the
// tank's healing nor the rest's, so they lower the tank share rather than counting as cover (and a
// self-inflated total must not make a healer the wrong main healer).
func TestHealAssignmentSelfHeals(t *testing.T) {
	heals := concat(
		healsOf("$bot", "$player", 6, 100),
		healsOf("$bot", "$bot", 4, 100),
		restHeals("$extra1", 10, 90),
	)
	rec := withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
	row := healAssignmentRow(rec)
	if row.Verdict != VerdictPass {
		t.Fatalf("60 %% of 1000 on the tank is %s, want PASS (%s)", row.Verdict, row.Reason)
	}
	if row.K != 6 || row.N != 10 {
		t.Fatalf("k/n %d/%d, want 6/10", row.K, row.N)
	}
	if !strings.Contains(strings.Join(row.Evidence, "\n"), "60% to the tanks") {
		t.Fatalf("the evidence does not read the 600 of 1000 total: %s", strings.Join(row.Evidence, "\n"))
	}

	// The same healer with the same tank heals, but its self-heals put its total above the other
	// healer's: omitting them from the total would pick the other healer as main and report 0%.
	heals = concat(
		healsOf("$bot", "$player", 5, 100),
		healsOf("$bot", "$bot", 5, 100),
		healsOf("$extra1", "$extra2", 10, 90),
	)
	rec = withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
	row = healAssignmentRow(rec)
	if row.Verdict != VerdictFail || !strings.Contains(row.Reason, "50%") {
		t.Fatalf("verdict %s (%s), want the main healer's 50%% share", row.Verdict, row.Reason)
	}
	if row.K != 5 || row.N != 10 {
		t.Fatalf("k/n %d/%d, want the main healer's 5/10", row.K, row.N)
	}
}

// TestHealAssignmentUnreadableSelfHeal: an unreadable self-heal is missing evidence like any other,
// never a share computed around it.
func TestHealAssignmentUnreadableSelfHeal(t *testing.T) {
	heals := concat(
		healsOf("$bot", "$player", 20, 100),
		[]Heal{{At: 30, Src: "$bot", Dst: "$bot"}},
		restHeals("$extra1", 10, 100),
	)
	rec := withFight(Record{Members: healerParty(), Heals: heals, EventsKnown: true})
	row := healAssignmentRow(rec)
	if row.Verdict != VerdictInconclusive || !strings.Contains(row.Reason, "no readable amount") {
		t.Fatalf("verdict %s (%s), want INCONCLUSIVE on the unreadable self-heal", row.Verdict, row.Reason)
	}
}
