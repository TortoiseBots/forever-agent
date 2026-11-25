package classrules

// The healer-assignment rule (HEAL-ASSIGNMENT). See the package doc for the measure, the
// thresholds and every INCONCLUSIVE case.

import (
	"fmt"
	"strings"
)

// healerTally is one healer's healing in one fight: its heal events, its healing by target class,
// and the non-tank party members it healed.
type healerTally struct {
	member      Member
	placed      int // heal events on a party member the setup named (a placed heal)
	onTanks     int // of the placed heals, on a party tank
	total       float64
	tank        float64
	restTargets map[string]bool
	other       int // heals on a pet or a unit the setup did not name, left out of the shares
	unreadable  int // placed heals without a readable amount
}

// share is the healer's share of its placed healing that landed on the tanks, 0 when it healed
// nothing placed.
func (h healerTally) share() float64 {
	if h.total <= 0 {
		return 0
	}
	return h.tank / h.total
}

// inSpan reports whether t falls inside one of the excluded spans (either end inclusive).
func inSpan(spans []Span, t float64) bool {
	for _, span := range spans {
		if span.A <= t && t <= span.B {
			return true
		}
	}
	return false
}

// healAssignmentRow judges one fight's healing: the main healer on the tanks, the other healers on
// the rest of the party.
func healAssignmentRow(rec Record) Row {
	row := Row{ID: RuleHealAssignment, Verdict: VerdictInconclusive}
	if !rec.EventsKnown {
		row.Reason = "no events in the record (events.jsonl is missing or unreadable)"
		return row
	}
	if rec.Window == nil {
		row.Reason = "no fight window: the recording carries no usable fight_start..fight_end, so no fight's healing can be placed"
		return row
	}
	lo, hi := rec.Window[0], rec.Window[1]
	if hi-lo < MinFightSeconds {
		row.Reason = fmt.Sprintf("the fight lasted %.0f s, under the %.0f s the rule needs", hi-lo, MinFightSeconds)
		return row
	}
	if len(rec.Heals) == 0 {
		row.Reason = "the recording holds no heal rows"
		return row
	}
	var tanks []Member
	healers := map[string]*healerTally{}
	order := []string{}
	for _, m := range rec.Members {
		switch m.Role {
		case RoleTank:
			tanks = append(tanks, m)
		case RoleHealer:
			healers[m.Unit] = &healerTally{member: m, restTargets: map[string]bool{}}
			order = append(order, m.Unit)
		}
	}
	if len(tanks) == 0 {
		row.Evidence = []string{fmt.Sprintf("party: %s", partyLine(rec.Members))}
		row.Reason = "no tank role in the party: the rule needs the tanks it measures the healing against"
		return row
	}
	if len(healers) == 0 {
		row.Evidence = []string{fmt.Sprintf("party: %s", partyLine(rec.Members))}
		row.Reason = "no party member carries the healer role"
		return row
	}
	// A role without a guid cannot be found in the events: its heals would be read as someone
	// else's, or as heals on no one, and the shares would be wrong. Missing evidence, never a FAIL.
	for _, t := range tanks {
		if t.GUID == "" {
			row.Evidence = []string{fmt.Sprintf("party: %s", partyLine(rec.Members))}
			row.Reason = fmt.Sprintf("the setup proves no guid for the tank %s, so its healing cannot be read", orNone(t))
			return row
		}
	}
	for _, unit := range order {
		if h := healers[unit]; h.member.GUID == "" {
			row.Evidence = []string{fmt.Sprintf("party: %s", partyLine(rec.Members))}
			row.Reason = fmt.Sprintf("the setup proves no guid for the healer %s, so its healing cannot be read", orNone(h.member))
			return row
		}
	}
	tankUnits := map[string]bool{}
	for _, t := range tanks {
		tankUnits[t.Unit] = true
	}
	// Only the fight counts: a heal before the pull, after the end or inside an excluded span
	// (a GM heal and the like) never sets a share.
	dropped := 0
	for _, heal := range rec.Heals {
		if heal.At < lo || heal.At > hi || inSpan(rec.Excluded, heal.At) {
			dropped++
			continue
		}
		h := healers[heal.Src]
		if h == nil {
			continue
		}
		if heal.Dst == "" {
			// A pet, or a unit whose guid never reached the setup: neither the tank nor the
			// rest of the party. Count it apart and leave it out of every share.
			h.other++
			continue
		}
		h.placed++
		if !heal.AmountKnown {
			h.unreadable++
			continue
		}
		// Every readable placed heal counts in the healer's total, whatever its target: the
		// shares are of the healing the healer actually did to the party.
		h.total += heal.Amount
		if heal.Dst == heal.Src {
			// A healer healing itself is neither the tank's healing nor the rest of the party's.
			continue
		}
		if tankUnits[heal.Dst] {
			h.tank += heal.Amount
			h.onTanks++
			continue
		}
		h.restTargets[heal.Dst] = true
	}

	var evidence []string
	evidence = append(evidence, fmt.Sprintf("party: %s", partyLine(rec.Members)))
	evidence = append(evidence, "the shares count the heal log's gross amount: the recording carries no overheal")
	if dropped > 0 {
		evidence = append(evidence, fmt.Sprintf("%d heal row(s) outside the fight window or in an excluded span, not counted", dropped))
	}
	other := 0
	var judged []*healerTally
	var under []string
	for _, unit := range order {
		h := healers[unit]
		other += h.other
		if h.placed < MinHealerHeals {
			under = append(under, fmt.Sprintf("%s: %d placed heal(s), under the %d-heal floor, not judged", orNone(h.member), h.placed, MinHealerHeals))
			continue
		}
		judged = append(judged, h)
	}
	if other > 0 {
		evidence = append(evidence, fmt.Sprintf("heals on pets or units the setup did not name, left out of the shares: %d", other))
	}
	if len(judged) == 0 {
		row.Evidence = sortedEvidence(append(evidence, under...))
		row.Reason = fmt.Sprintf("too few placed heals: no healer reaches %d heal events on a named party member", MinHealerHeals)
		return row
	}
	for _, h := range judged {
		if h.unreadable > 0 {
			row.Evidence = sortedEvidence(append(evidence, under...))
			row.Reason = fmt.Sprintf("%s: %d of %d placed heal rows carry no readable amount, so its shares cannot be measured",
				orNone(h.member), h.unreadable, h.placed)
			return row
		}
	}
	if len(judged) < 2 {
		row.Evidence = sortedEvidence(append(evidence, under...))
		row.Reason = fmt.Sprintf("one healer only (%s, %d placed heals): a split needs a second healer",
			orNone(judged[0].member), judged[0].placed)
		return row
	}
	main := judged[0]
	tied := false
	for _, h := range judged[1:] {
		switch {
		case h.total > main.total:
			main, tied = h, false
		case h.total == main.total:
			tied = true
		}
	}
	if tied {
		row.Evidence = sortedEvidence(append(evidence, under...))
		row.Reason = "no single main healer: two healers tie on healing"
		return row
	}

	restSet := map[string]bool{}
	for _, h := range judged {
		if h == main {
			continue
		}
		for unit := range h.restTargets {
			restSet[unit] = true
		}
	}
	for _, h := range judged {
		otherNote := ""
		if h.other > 0 {
			otherNote = fmt.Sprintf(", %d other heal(s) on pets or unnamed units", h.other)
		}
		if h == main {
			evidence = append(evidence, fmt.Sprintf("main healer %s: %d placed heal(s)%s, %.0f healing, %d%% to the tanks; the rule needs at least %d%%",
				orNone(h.member), h.placed, otherNote, h.total, roundPct(h.share()), roundPct(MainHealerTankShare)))
			continue
		}
		evidence = append(evidence, fmt.Sprintf("other healer %s: %d placed heal(s)%s, %.0f healing, %d%% to the tanks, %d non-tank ally(ies); the rule allows at most %d%%",
			orNone(h.member), h.placed, otherNote, h.total, roundPct(h.share()), len(h.restTargets), roundPct(OtherHealerTankShare)))
	}
	evidence = append(evidence, fmt.Sprintf("the other healers cover %d non-tank ally(ies) between them; the rule needs %d", len(restSet), MinRestTargets))
	evidence = append(evidence, under...)
	row.K = int64(main.onTanks)
	row.N = int64(main.placed)

	switch {
	case main.share() < MainHealerTankShare:
		row.Verdict = VerdictFail
		row.Reason = fmt.Sprintf("the main healer sends %d%% of its healing to the tanks, under the %d%% the rule needs",
			roundPct(main.share()), roundPct(MainHealerTankShare))
	case len(restSet) < MinRestTargets:
		row.Verdict = VerdictFail
		row.Reason = fmt.Sprintf("the other healers healed %d non-tank ally(ies) between them, under the %d the rule needs",
			len(restSet), MinRestTargets)
	default:
		over := ""
		for _, h := range judged {
			if h == main || h.share() <= OtherHealerTankShare {
				continue
			}
			over = fmt.Sprintf("the other healer %s sends %d%% of its healing to the tanks, over the %d%% the rule allows",
				orNone(h.member), roundPct(h.share()), roundPct(OtherHealerTankShare))
			break
		}
		if over != "" {
			row.Verdict = VerdictFail
			row.Reason = over
			break
		}
		row.Verdict = VerdictPass
		row.Reason = fmt.Sprintf("the main healer puts %d%% of its healing on the tanks and the other healers cover %d non-tank ally(ies) at %d%% or less on the tanks",
			roundPct(main.share()), len(restSet), roundPct(OtherHealerTankShare))
	}
	row.Evidence = sortedEvidence(evidence)
	return row
}

// partyLine names the party for the evidence: how many of each role, with the tanks' units.
func partyLine(members []Member) string {
	var tanks, healers int
	for _, m := range members {
		switch m.Role {
		case RoleTank:
			tanks++
		case RoleHealer:
			healers++
		}
	}
	var names []string
	for _, m := range members {
		if m.Role == RoleTank {
			names = append(names, m.Unit)
		}
	}
	line := fmt.Sprintf("%d member(s), %d tank(s), %d healer(s)", len(members), tanks, healers)
	if len(names) > 0 {
		line += " (tanks: " + strings.Join(names, ", ") + ")"
	}
	return line
}
