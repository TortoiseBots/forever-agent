package live

// situations_more.go: the situation kinds and options beyond tank_damage
// (trl/live/situations.py). The arena fight loop in situations.go carries
// caster_mob, pack, cc_target, debuff and the option-carrying tank trials;
// this file adds their triggers, the out-of-combat window (ooc_buffs) and
// the out-of-combat hurt window (ooc_hurt).

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

const (
	// ooc_hurt: the victim is set inside hp_band every this many seconds
	// when the trial names none.
	hurtEveryDefault = 20.0
	// the bound on waiting for a Drink/Food aura to end before the ooc
	// window opens (the bench's MEAL_WAIT_S).
	mealWaitS = 30.0
	// the hire's aura row drops a tick or two after `.unaura`.
	prepullAuraWaitS = 3.0
	// the fresh `aura_applied` of the tested debuff must show in this window.
	debuffAuraWaitS = 3.0
	// out of combat nothing re-casts the stance; this is the re-cast gap.
	stanceRecastGapS = 5.0
	// a refilled unmarked mob needs a moment to become a real target.
	ccGapTailS = 3.0
	// the wait between a debuff going missing and the refresh apply.
	debuffRefreshS = 10.0
)

// drinkAuraIDs are the Drink/Food auras the ooc window never strips (keep in
// sync with the trial's aura list; the Python DRINK_AURA_IDS).
var drinkAuraIDs = map[int]bool{
	430: true, 431: true, 432: true, 1133: true, 1135: true, 1137: true,
	10250: true, 22734: true, 24355: true, 25696: true, 26261: true,
	26402: true, 26473: true, 26475: true, 29007: true,
}

// threatCasts maps a stance aura to the threat spell the player uses on the
// current selection (the Python THREAT_CAST).
var threatCasts = map[int]int{71: 355, 2457: 7386, 9634: 6795, 5487: 6795}

// ccAck matches the module's crowd-control acknowledgement.
//
//	TBM:ACTION_ACK|cc moon|bot:<name>|<count>|<name>
var ccAck = regexp.MustCompile(`TBM:ACTION_ACK\|cc moon(?: [^|]*)?\|[^|]*\|(\d+)\|`)

// ---- auras --------------------------------------------------------------

// unitAuraIDs reads one unit's aura ids. known is false when the read or the
// aura list is unusable: an unreadable list is UNKNOWN, never "no aura".
func unitAuraIDs(ctx context.Context, c *Client, guid string) (ids map[int]bool, known bool) {
	u, err := c.Unit(ctx, guid)
	if err != nil {
		return nil, false
	}
	list, ok := u.Raw["auras"].([]any)
	if !ok {
		return nil, false
	}
	out := map[int]bool{}
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		id, ok := numOf(m["spell_id"])
		if !ok {
			return nil, false
		}
		out[int(id)] = true
	}
	return out, true
}

// hireAuraIDs reads the hire's aura ids; nil,false when there is no hire or
// the read is unusable.
func (t *tankRun) hireAuraIDs(ctx context.Context) (map[int]bool, bool) {
	guid := t.r.Guids.Bot
	if guid == "" {
		return map[int]bool{}, true
	}
	return unitAuraIDs(ctx, t.r.Client, guid)
}

// hasDrink reports a Drink/Food aura in the set.
func hasDrink(ids map[int]bool) bool {
	for id := range ids {
		if drinkAuraIDs[id] {
			return true
		}
	}
	return false
}

func drinkList(ids map[int]bool) string {
	var out []string
	for id := range ids {
		if drinkAuraIDs[id] {
			out = append(out, itoa(id))
		}
	}
	sortStrings(out)
	return strings.Join(out, ", ")
}

func sortStrings(s []string) { sort.Strings(s) }

// guardClassOf names the target class of an in-scope guid for the guard's
// own hands.
func (t *tankRun) guardClassOf(guid string) GuardTarget {
	if t.r.Guard == nil {
		return ""
	}
	return t.r.Guard.where(guid)
}

// ---- prepull options ----------------------------------------------------

// clearPrepullAuras strips the trial's `prepull_unaura` auras off the hire
// and proves them gone: a refused line or an aura still up ends the repeat
// INVALID before the summon (the Python clear_prepull_auras).
func (t *tankRun) clearPrepullAuras(ctx context.Context) string {
	hire := t.r.Guids.Bot
	if hire == "" {
		why := "prepull unaura: no hire identity"
		t.r.Marks.Add(map[string]any{"kind": "prepull_unaura_failed", "reason": why})
		return why
	}
	for _, spell := range t.prepull {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := t.r.Guard.Send(cctx, "unaura", TargetHire, hire, itoa(spell))
		cancel()
		if err != nil {
			t.sit.logf("prepull unaura %d refused: %v", spell, err)
			why := fmt.Sprintf("prepull unaura %d refused: %s", spell, trimLen(err.Error(), 120))
			t.r.Marks.Add(map[string]any{"kind": "prepull_unaura_failed", "reason": why})
			return why
		}
		t.r.Marks.Add(map[string]any{"kind": "prepull_unaura", "spell": spell})
	}
	wanted := map[int]bool{}
	for _, spell := range t.prepull {
		wanted[spell] = true
	}
	deadline := t.sit.now().Add(time.Duration(prepullAuraWaitS * float64(time.Second)))
	for {
		have, known := t.hireAuraIDs(ctx)
		if known {
			left := false
			for spell := range wanted {
				if have[spell] {
					left = true
				}
			}
			if !left {
				return ""
			}
		}
		if !t.sit.now().Before(deadline) {
			have, known := t.hireAuraIDs(ctx)
			var why string
			if !known {
				why = "prepull unaura: no fresh aura sample of the hire"
			} else {
				var still []string
				for spell := range wanted {
					if have[spell] {
						still = append(still, itoa(spell))
					}
				}
				sortStrings(still)
				why = "prepull unaura left aura(s) " + strings.Join(still, ", ") + " on the hire"
			}
			t.sit.logf("%s", why)
			t.r.Marks.Add(map[string]any{"kind": "prepull_unaura_failed", "reason": why})
			return why
		}
		sleepCtx(ctx, 300*time.Millisecond)
	}
}

// resetHireCooldowns clears the reused hire's cooldowns before the pull and
// reads them back: the row is ok only when the serve's unit row lists the
// hire's cooldowns and the list is empty. A refused line, a hire that cannot
// be read, or a serve whose unit row carries no cooldown list leaves the row
// failed with the reason (the repeat is INVALID: the cooldown reset is not a
// claim a send can make).
func (t *tankRun) resetHireCooldowns(ctx context.Context) {
	hire := t.r.Guids.Bot
	row := map[string]any{"id": "hire_cooldowns_reset", "value": nil}
	finish := func(ok bool, evidence string) {
		row["ok"], row["evidence"] = ok, evidence
		t.r.ExtraChecks = append(t.r.ExtraChecks, row)
	}
	if hire == "" {
		finish(false, "no hire identity")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := t.r.Guard.Send(cctx, "cooldown", TargetHire, hire)
	cancel()
	if err != nil {
		t.r.Marks.Add(map[string]any{"kind": "cooldown_reset_refused", "evidence": trimLen(err.Error(), 120)})
		finish(false, "cooldown refused: "+trimLen(err.Error(), 120))
		return
	}
	u, rerr := t.r.Client.Unit(ctx, hire)
	if rerr != nil {
		finish(false, "cooldown sent; the hire could not be read back: "+trimLen(rerr.Error(), 100))
		return
	}
	list, has := u.Raw["cooldowns"].([]any)
	switch {
	case !has:
		finish(false, "cooldown sent; the serve's unit row carries no cooldown list, so the reset cannot be read back")
	case len(list) > 0:
		finish(false, fmt.Sprintf("cooldown sent; the hire still holds %d cooldown(s)", len(list)))
	default:
		t.r.Marks.Add(map[string]any{"kind": "cooldowns_reset"})
		finish(true, "cooldown sent; the hire's cooldown list reads back empty")
	}
}

// ---- the tested debuff --------------------------------------------------

// stripDebuff proves the tested debuff absent just before the pull, stripping
// a copy left by an earlier repeat. A proof that cannot be made ends the
// repeat INVALID: a refresh emits no `aura_applied`, so a stale copy would
// leave the rule without its trigger.
func (t *tankRun) stripDebuff(ctx context.Context, spell int) string {
	guid := t.victimGUID()
	have, known := unitAuraIDs(ctx, t.r.Client, guid)
	if !known {
		why := "debuff " + itoa(spell) + ": no fresh aura read of the " + t.victim
		t.r.Marks.Add(map[string]any{"kind": "debuff_strip_failed", "reason": why})
		return why
	}
	if !have[spell] {
		t.r.Marks.Add(map[string]any{"kind": "debuff_absent", "spell": spell})
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := t.r.Guard.RemoveAura(cctx, guid, spell)
	cancel()
	if err != nil {
		why := "debuff " + itoa(spell) + " unaura refused: " + trimLen(err.Error(), 120)
		t.sit.logf("%s", why)
		t.r.Marks.Add(map[string]any{"kind": "debuff_strip_failed", "reason": why})
		return why
	}
	deadline := t.sit.now().Add(time.Duration(prepullAuraWaitS * float64(time.Second)))
	for {
		ids, ok := unitAuraIDs(ctx, t.r.Client, guid)
		if ok && !ids[spell] {
			t.r.Marks.Add(map[string]any{"kind": "debuff_stripped", "spell": spell})
			return ""
		}
		if !t.sit.now().Before(deadline) {
			why := "debuff " + itoa(spell) + " still up after .unaura"
			if !ok {
				why = "debuff " + itoa(spell) + ": no fresh aura read after the unaura"
			}
			t.sit.logf("%s", why)
			t.r.Marks.Add(map[string]any{"kind": "debuff_strip_failed", "reason": why})
			return why
		}
		sleepCtx(ctx, 300*time.Millisecond)
	}
}

// applyDebuff applies the tested debuff in combat and proves a fresh
// `aura_applied` row (retried once). False when neither apply produced it.
func (t *tankRun) applyDebuff(ctx context.Context, spell int) bool {
	guid := t.victimGUID()
	class := t.guardClassOf(guid)
	for attempt := 1; attempt <= 2; attempt++ {
		seq := t.r.Collect.LastRawSeq()
		sentAt := time.Now()
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := t.r.Guard.Send(cctx, "aura", class, guid, itoa(spell))
		cancel()
		if err != nil {
			t.sit.logf("debuff refused: %v", err)
		}
		deadline := t.sit.now().Add(time.Duration(debuffAuraWaitS * float64(time.Second)))
		for {
			if t.r.Collect.AuraOnSince(seq, spell, guid, sentAt) {
				t.r.Marks.Add(map[string]any{"kind": "debuff_applied", "spell": spell,
					"attempt": attempt, "fresh": true})
				return true
			}
			if !t.sit.now().Before(deadline) || ctx.Err() != nil {
				break
			}
			sleepCtx(ctx, 200*time.Millisecond)
		}
		t.r.Marks.Add(map[string]any{"kind": "debuff_apply", "spell": spell,
			"attempt": attempt, "fresh": false})
	}
	return false
}

// refreshDebuff keeps the tested debuff up for the whole repeat: a sampled
// aura list that loses the spell for 10 s gets one more apply (same GM path).
func (t *tankRun) refreshDebuff(ctx context.Context) {
	key := t.sampleKey()
	sample := t.r.Collect.LastSample()
	row, _ := sample[key].(map[string]any)
	if row == nil {
		return
	}
	if _, ok := row["auras"].([]any); !ok {
		// an unreadable aura list is unknown, never "the debuff is gone"
		return
	}
	present := false
	for _, v := range sortedSpellIDs(row["auras"]) {
		if int(intOf(v)) == t.debuffSpell {
			present = true
		}
	}
	if present {
		t.debuffGoneAt = time.Time{}
		return
	}
	now := t.sit.now()
	if t.debuffGoneAt.IsZero() {
		t.debuffGoneAt = now.Add(time.Duration(debuffRefreshS * float64(time.Second)))
		return
	}
	if !now.Before(t.debuffGoneAt) {
		t.applyDebuff(ctx, t.debuffSpell)
		t.debuffGoneAt = time.Time{}
	}
}

// sampleKey is the collector's key of the victim.
func (t *tankRun) sampleKey() string {
	if idx := extraIndex(t.victim); idx > 0 {
		return "extra" + itoa(idx)
	}
	if t.victim == "bot" {
		return "bot"
	}
	return "player"
}

// ---- cc_target ----------------------------------------------------------

// ccTargets is every summon except the marked one.
func (t *tankRun) ccTargets() []string {
	var out []string
	for _, g := range t.summoned {
		if t.marked != "" && normGUIDOf(g) == t.marked {
			continue
		}
		out = append(out, g)
	}
	return out
}

// attackOne selects guid and starts one short attack.
func (t *tankRun) attackOne(ctx context.Context, guid string) {
	_ = t.r.Client.Target(ctx, guid)
	_, _ = t.r.Client.Attack(ctx, guid, 2500)
}

// runCCPull marks the second summon with the moon icon order and engages
// every unmarked mob (the marked one is never attacked).
func (t *tankRun) runCCPull(ctx context.Context) {
	if len(t.summoned) < 2 {
		return
	}
	t.marked = normGUIDOf(t.summoned[1])
	t.r.Guids.Marked = t.marked
	t.r.ExtraChecks = append(t.r.ExtraChecks, t.markAndCC(ctx, t.marked))
	t.engageMarked(ctx)
	t.engageCCTargets(ctx)
}

// defaultCCAuras are the Polymorph ranks (the trial's `opportunity.has_aura`
// names its own list).
var defaultCCAuras = []int{118, 12824, 12825, 12826, 28270, 28271, 28272}

// ccAuraSet is the crowd-control aura ids that prove the mark: the trial's
// `opportunity.has_aura`, else the Polymorph ranks.
func (t *tankRun) ccAuraSet() map[int]bool {
	set := map[int]bool{}
	if opp, ok := pyjson.Object(t.r.Trial["opportunity"]); ok && opp != nil {
		if list, ok := pyjson.List(opp["has_aura"]); ok {
			for _, v := range list {
				if f, ok := pyjson.Num(v); ok && f > 0 {
					set[int(f)] = true
				}
			}
		}
	}
	if len(set) == 0 {
		for _, id := range defaultCCAuras {
			set[id] = true
		}
	}
	return set
}

// markAndCC puts the crowd-control assignment on the bot for the selected
// mob. On Forever the `mark` RPC is not ported, so the module's own
// acknowledgement is only the order's answer, and the raid icon cannot be read
// back: the row stays failed until a crowd-control aura is READ on the marked
// mob (checkCCMark flips it), so the mark is never claimed on an ACK alone.
func (t *tankRun) markAndCC(ctx context.Context, guid string) map[string]any {
	if t.r.BotName == "" {
		return map[string]any{"id": "cc_mark_set", "ok": false, "value": nil,
			"evidence": "cc_target needs a hired bot to own the mark"}
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := t.r.Client.Target(cctx, guid); err != nil {
		return map[string]any{"id": "cc_mark_set", "ok": false, "value": nil,
			"evidence": "selecting the mark target failed: " + trimLen(err.Error(), 120)}
	}
	res, err := t.r.Guard.CCOrder(cctx, t.r.BotName)
	line := strings.Join(res.Lines, "\n")
	ack := ccAck.FindStringSubmatch(line)
	acked := false
	if ack != nil {
		if n, cerr := strconv.Atoi(ack[1]); cerr == nil && n >= 1 {
			acked = true
		}
	}
	evidence := "the `mark` method is not ported on Forever; the module's cc order sets the icon itself; .bot cc moon " +
		t.r.BotName + " -> " + firstLine(line)
	if err != nil {
		acked = false
		evidence += "; order refused: " + trimLen(err.Error(), 120)
	}
	t.r.Ledger.Add(map[string]any{"kind": "cc_mark", "guid": guid, "mark": "",
		"bot": t.r.BotName, "reply": line})
	row := map[string]any{"id": "cc_mark_set", "ok": false, "value": firstLine(line)}
	if acked {
		row["evidence"] = evidence + "; the icon cannot be read back: waiting for a crowd-control aura on the marked mob"
		t.ccRow = row
	} else {
		row["evidence"] = evidence
	}
	return row
}

// checkCCMark flips the cc_mark_set row to ok the first time a crowd-control
// aura is read on the marked mob; an unreadable aura list proves nothing.
func (t *tankRun) checkCCMark(ctx context.Context) {
	if t.ccRow == nil || t.ccProven || t.marked == "" {
		return
	}
	ids, known := unitAuraIDs(ctx, t.r.Client, t.marked)
	if !known {
		return
	}
	set := t.ccAuraSet()
	for id := range ids {
		if set[id] {
			t.ccProven = true
			t.ccRow["ok"] = true
			t.ccRow["evidence"] = strOf(t.ccRow["evidence"]) + "; aura " + itoa(id) + " read on the marked mob"
			return
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "no TBM:ACTION_ACK"
	}
	return s
}

// engageMarked taunts the marked mob into combat (Taunt deals no damage: an
// attack on the sheep would break the crowd control the trial watches).
func (t *tankRun) engageMarked(ctx context.Context) {
	if t.marked == "" {
		return
	}
	t.walkNear(ctx, t.marked, 4)
	_ = t.r.Client.Cast(ctx, 355, t.marked)
	if !t.waitInCombat(ctx, t.marked, 5*time.Second) {
		_ = t.r.Client.Cast(ctx, 355, t.marked)
		t.waitInCombat(ctx, t.marked, 5*time.Second)
	}
	// The player's last selection is an unmarked mob: `threat` casts on the
	// selection and must never taunt the sheep.
	if unmarked := t.ccTargets(); len(unmarked) > 0 {
		_ = t.r.Client.Target(ctx, unmarked[0])
	}
}

// engageCCTargets attacks every unmarked summon, waits for all of them to be
// in combat, and replaces mobs_ready with the row that also needs them
// fighting and the marked mob in combat.
func (t *tankRun) engageCCTargets(ctx context.Context) {
	targets := t.ccTargets()
	if len(targets) == 0 {
		return
	}
	t.walkNear(ctx, targets[0], 25)
	for _, guid := range targets {
		t.attackOne(ctx, guid)
	}
	deadline := t.sit.now().Add(10 * time.Second)
	for {
		all := true
		for _, guid := range targets {
			u, err := t.r.Client.Unit(ctx, guid)
			if err != nil || !u.InCombat {
				all = false
			}
		}
		if all || !t.sit.now().Before(deadline) || ctx.Err() != nil {
			break
		}
		sleepCtx(ctx, 500*time.Millisecond)
	}
	row := t.mobsReadyEngaged(ctx, targets, t.marked)
	for i, c := range t.r.ExtraChecks {
		if strOf(c["id"]) == "mobs_ready" {
			t.r.ExtraChecks[i] = row
			return
		}
	}
	t.r.ExtraChecks = append(t.r.ExtraChecks, row)
}

// mobsReadyEngaged is mobs_ready with the cc_target additions.
func (t *tankRun) mobsReadyEngaged(ctx context.Context, engaged []string, marked string) map[string]any {
	alive := []any{}
	for _, g := range t.summoned {
		u, err := t.r.Client.Unit(ctx, g)
		if err != nil || u.Dead {
			continue
		}
		alive = append(alive, normGUIDOf(g))
	}
	ok := len(alive) > 0
	evidence := fmt.Sprintf("%d/%d summons alive, level %d-%d",
		len(alive), len(t.summoned), t.levelLo, t.levelHi)
	fighting := 0
	for _, g := range engaged {
		if u, err := t.r.Client.Unit(ctx, g); err == nil && u.InCombat {
			fighting++
		}
	}
	ok = ok && fighting == len(engaged)
	evidence += fmt.Sprintf("; %d/%d unmarked summons in combat", fighting, len(engaged))
	if marked != "" {
		in := false
		if u, err := t.r.Client.Unit(ctx, marked); err == nil && u.InCombat {
			in = true
		}
		ok = ok && in
		word := "no"
		if in {
			word = "yes"
		}
		evidence += "; marked " + marked + " in combat: " + word
	}
	return map[string]any{"id": "mobs_ready", "ok": ok, "value": alive, "evidence": evidence}
}

// waitInCombat waits until guid reads in combat.
func (t *tankRun) waitInCombat(ctx context.Context, guid string, timeout time.Duration) bool {
	deadline := t.sit.now().Add(timeout)
	for {
		if u, err := t.r.Client.Unit(ctx, guid); err == nil && u.InCombat {
			return true
		}
		if !t.sit.now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		sleepCtx(ctx, 500*time.Millisecond)
	}
}

// trackCCGap writes the `cc_gap` excluded span while only the marked mob is
// alive, and closes it once an unmarked one has been up for the tail. One
// read of the summons' states feeds the whole decision: an unreadable list
// opens and closes nothing.
func (t *tankRun) trackCCGap(ctx context.Context) {
	states, err := t.summonStates(ctx, t.summoned)
	if err != nil {
		t.sit.logf("cc gap check skipped: summon states unreadable (%v)", err)
		return
	}
	unmarked := 0
	for g, st := range states {
		if st == "alive" && (t.marked == "" || normGUIDOf(g) != t.marked) {
			unmarked++
		}
	}
	if unmarked == 0 && t.gapStart == "" {
		t.gapStart = t.r.Marks.NowISO()
		return
	}
	if unmarked > 0 && t.gapStart != "" {
		t.r.Marks.Excluded(t.gapStart, t.r.Marks.NowISOOffset(time.Duration(ccGapTailS*float64(time.Second))), "cc_gap")
		t.gapStart = ""
	}
}

// walkNear walks the character to within want yards of guid (the serve's
// walk_to plans the route). An unreadable unit is not walked to.
func (t *tankRun) walkNear(ctx context.Context, guid string, want float64) bool {
	u, err := t.r.Client.Unit(ctx, guid)
	if err != nil {
		return false
	}
	st, err := t.r.Client.StatusOf(ctx)
	if err != nil {
		return false
	}
	dx := float64(u.X) - float64(st.X)
	dy := float64(u.Y) - float64(st.Y)
	dist := math.Hypot(dx, dy)
	if dist <= want || dist <= 0 {
		return true
	}
	k := math.Max(0, dist-want) / dist
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_ = t.r.Client.WalkTo(cctx, float64(st.X)+dx*k, float64(st.Y)+dy*k, float64(u.Z))
	return true
}

// threat casts the stance's threat spell on the current selection (the
// player's own pull keeps the mobs on the tank).
func (t *tankRun) threat(ctx context.Context) {
	player, _ := t.r.Trial["player"].(map[string]any)
	stance, _ := intOpt(player, "stance")
	spell, ok := threatCasts[stance]
	if !ok {
		return
	}
	_ = t.r.Client.Cast(ctx, uint32(spell), "")
}

// ---- energy drain -------------------------------------------------------

// drainTick empties the rogue hire's bar on the trial's schedule after the
// fight starts; the marker is stamped BEFORE the line goes out. The
// `energy_drained` setup row is ok only while every drain was read back at or
// under its target; a refused line or an unreadable bar fails it.
func (t *tankRun) drainTick(now time.Time) {
	if t.fightStart.IsZero() || t.r.Guids.Bot == "" {
		return
	}
	first, _ := numOpt(t.energyDrain, "first_at_s")
	every, _ := numOpt(t.energyDrain, "every_s")
	to, _ := numOpt(t.energyDrain, "to")
	if t.drainNext.IsZero() {
		t.drainNext = t.fightStart.Add(time.Duration(first * float64(time.Second)))
	}
	if now.Before(t.drainNext) {
		return
	}
	t.drainNext = now.Add(time.Duration(every * float64(time.Second)))
	maxPower := 100
	if row, ok := t.r.Collect.LastSample()["bot"].(map[string]any); ok {
		if v, ok := numOf(row["max_power"]); ok && v > 0 {
			maxPower = int(v)
		}
	}
	t.r.Marks.Add(map[string]any{"kind": "energy_drain", "to": int(to), "wall": t.r.Marks.NowISO()})
	cctx, cancel := context.WithTimeout(context.Background(), governorCallTimeout)
	defer cancel()
	if err := t.r.Guard.Send(cctx, "modify_energy", TargetHire, t.r.Guids.Bot,
		itoa(int(to)), itoa(maxPower)); err != nil {
		t.r.Marks.Add(map[string]any{"kind": "energy_drain_refused", "evidence": trimLen(err.Error(), 120)})
		t.drainRow(false, "modify energy refused: "+trimLen(err.Error(), 120))
		return
	}
	u, rerr := t.r.Client.Unit(cctx, t.r.Guids.Bot)
	switch {
	case rerr != nil:
		t.drainRow(false, "modify energy sent; the hire could not be read back: "+trimLen(rerr.Error(), 100))
	case float64(u.Power) > to+25:
		t.drainRow(false, fmt.Sprintf("modify energy sent; the hire reads %d energy, target %d", u.Power, int(to)))
	default:
		t.drainRow(true, fmt.Sprintf("energy read back at %d after .modify energy %d", u.Power, int(to)))
	}
}

// drainRow keeps the one `energy_drained` row: a failed drain fails it for
// the whole repeat, later good ones never turn it back.
func (t *tankRun) drainRow(ok bool, evidence string) {
	for _, row := range t.r.ExtraChecks {
		if strOf(row["id"]) == "energy_drained" {
			if !ok && boolOf(row["ok"]) {
				row["ok"], row["evidence"] = false, evidence
			}
			return
		}
	}
	t.r.ExtraChecks = append(t.r.ExtraChecks, map[string]any{"id": "energy_drained",
		"ok": ok, "value": nil, "evidence": evidence})
}

// ---- caster proof -------------------------------------------------------

// casterCastRow is the `mob_caster_casts` setup row of a caster_mob trial:
// a spell_start or spell_go by a summoned mob in the repeat's combat log.
func (t *tankRun) casterCastRow() map[string]any {
	cast := t.r.Collect.MobCasts(t.summoned)
	ok := cast != nil
	evidence := "no spell_start by the summoned mob in the window"
	var value any
	if ok {
		evidence = "spell_start by a summoned mob"
		value = cast["spell_id"]
	}
	return map[string]any{"id": "mob_caster_casts", "ok": ok, "value": value, "evidence": evidence}
}

// ---- ooc_buffs ----------------------------------------------------------

// runOOC is the out-of-combat window: the hire stands in a quiet arena while
// the trial's buffs are observed. It never `.unaura all`s an active meal
// (that cuts the drink and can leave the AI asleep in it): the window waits
// the meal out and re-applies the trial's mana level afterwards.
func (t *tankRun) runOOC(ctx context.Context) (map[string]any, error) {
	t.sitOpts, _ = pyjson.Object(t.r.Trial["situation"])
	if t.sitOpts == nil {
		t.sitOpts = map[string]any{}
	}
	t.parseSpecialOptions()
	wait, _ := numOpt(t.sitOpts, "ooc_wait_s")
	if wait <= 0 {
		wait = 60
	}
	// The window runs in a quiet arena like every trial: it sends `.unaura all`
	// to the tester and the hire wherever they stand, so the arena, the quiet
	// probe and the stranger check come first.
	pl, _ := t.r.Trial["player"].(map[string]any)
	level, _ := intOpt(pl, "level")
	if level <= 0 {
		level = 1
	}
	arena, why := t.acquireArena(ctx, level)
	if arena == nil {
		return map[string]any{"ok": false, "ended": "no arena", "invalid_reason": why}, nil
	}
	if name := t.r.BotName; name != "" {
		if why := t.bringHire(ctx, name); why != "" {
			return hireNotBrought(why), nil
		}
		if why := proveHireEngines(ctx, t.tb, t.r, t.sit); why != "" {
			return map[string]any{"ok": false, "ended": "no_engine", "invalid_reason": why}, nil
		}
	}
	var lastStranger time.Time
	if t.strangerTick(ctx, &lastStranger) {
		return t.strangerOutcome(), nil
	}
	player := t.r.Guids.Player
	if player == "" {
		if st, err := t.r.Client.StatusOf(ctx); err == nil {
			player = normGUIDOf(st.GUID)
		}
	}
	if player != "" {
		if err := t.r.Guard.Send(ctx, "unaura", TargetSelf, player, "all"); err != nil {
			t.sit.logf("ooc cleanup unaura refused: %s", trimLen(err.Error(), 120))
		}
	}
	hire := t.r.Guids.Bot
	have, known := t.hireAuraIDs(ctx)
	if hire != "" && !known {
		// one retry: a transient unit read is not "no meal".
		have, known = t.hireAuraIDs(ctx)
	}
	meal := "none"
	switch {
	case hire != "" && !known:
		t.sit.logf("ooc meal: hire aura read unknown, retrying within the %.0f s meal wait", mealWaitS)
		meal = t.waitOutMeal(ctx, nil)
	case hire != "" && hasDrink(have):
		t.sit.logf("ooc meal: a Drink/Food aura is up on the hire (%s), waiting it out", drinkList(have))
		meal = t.waitOutMeal(ctx, have)
	default:
		if hire != "" {
			if err := t.r.Guard.Send(ctx, "unaura", TargetHire, hire, "all"); err != nil {
				t.sit.logf("ooc cleanup unaura refused: %s", trimLen(err.Error(), 120))
			}
		}
	}
	if meal == "aborted" {
		return map[string]any{"ok": false, "ended": "disconnected", "invalid_reason": "interrupted"}, nil
	}
	if meal == "stranger" {
		return t.strangerOutcome(), nil
	}
	if meal == "timeout" {
		return map[string]any{"ok": false, "ended": "meal", "invalid_reason": "meal still active"}, nil
	}
	if meal == "unknown" {
		return map[string]any{"ok": false, "ended": "meal", "invalid_reason": "meal state unknown"}, nil
	}
	if meal != "none" {
		if !t.restoreWindowMana(ctx) {
			return map[string]any{"ok": false, "ended": "meal", "invalid_reason": "mana reset failed"}, nil
		}
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_start", "mobs": []any{}, "ooc": true})
	deadline := t.sit.now().Add(time.Duration(wait * float64(time.Second)))
	invalid := ""
	for t.sit.now().Before(deadline) {
		if why := t.abortReason(); why != "" {
			invalid = why
			break
		}
		if t.strangerTick(ctx, &lastStranger) {
			break
		}
		sleepCtx(ctx, time.Second)
	}
	if t.strangerWhy != "" {
		t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "real_player"})
		return t.strangerOutcome(), nil
	}
	end := "ooc"
	if invalid != "" {
		end = "disconnected"
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": end})
	if invalid != "" {
		return map[string]any{"ok": false, "ended": "disconnected", "invalid_reason": invalid}, nil
	}
	return map[string]any{"ok": true, "ended": "ooc"}, nil
}

// strangerTick looks for a real player near the arena at most every
// strangerEvery (a zero `last` forces the look); true, with t.strangerWhy
// set, when one is there or the unit list cannot be read.
func (t *tankRun) strangerTick(ctx context.Context, last *time.Time) bool {
	if t.strangerWhy != "" {
		return true
	}
	now := t.sit.now()
	if !last.IsZero() && now.Sub(*last) < strangerEvery {
		return false
	}
	*last = now
	if why, near := t.strangerNear(ctx); near {
		t.strangerWhy = why
		return true
	}
	return false
}

// strangerOutcome is the repeat's end when a real player came near: INVALID,
// nothing further is done to the tester or the hire.
func (t *tankRun) strangerOutcome() map[string]any {
	return map[string]any{"ok": false, "ended": "real_player", "invalid_reason": t.strangerWhy}
}

// waitOutMeal waits (bounded) for a Drink/Food aura to end. "ended" is a
// readable list with no meal; "timeout" a meal still up at the deadline;
// "unknown" no readable list at the deadline; "aborted" a lost session.
func (t *tankRun) waitOutMeal(ctx context.Context, have map[int]bool) string {
	bound := time.Duration(mealWaitS * float64(time.Second))
	if t.sit.MealWait > 0 {
		bound = t.sit.MealWait
	}
	deadline := t.sit.now().Add(bound)
	seen := map[int]bool{}
	for id := range have {
		if drinkAuraIDs[id] {
			seen[id] = true
		}
	}
	var lastStranger time.Time
	for t.sit.now().Before(deadline) {
		if t.abortReason() != "" {
			return "aborted"
		}
		if t.strangerTick(ctx, &lastStranger) {
			return "stranger"
		}
		ids, known := t.hireAuraIDs(ctx)
		if known {
			seen = map[int]bool{}
			for id := range ids {
				if drinkAuraIDs[id] {
					seen[id] = true
				}
			}
			if len(seen) == 0 {
				t.sit.logf("ooc meal: the setup meal ended, opening the window on a clean hire")
				return "ended"
			}
		}
		sleepCtx(ctx, time.Second)
	}
	if len(seen) > 0 {
		t.sit.logf("ooc meal: still up after %.0f s (aura(s) %s), the repeat is invalid: no scoring window",
			mealWaitS, drinkList(seen))
		return "timeout"
	}
	t.sit.logf("ooc meal: no readable aura list after %.0f s, the repeat is invalid: no scoring window", mealWaitS)
	return "unknown"
}

// restoreWindowMana puts the hire back on the trial's `bot.mana_pct` after a
// meal restored it. An unreadable max mana or a refused line is false: the
// window must not score a bot far above the trial's level.
func (t *tankRun) restoreWindowMana(ctx context.Context) bool {
	bot, _ := t.r.Trial["bot"].(map[string]any)
	pct, ok := numOpt(bot, "mana_pct")
	if !ok || t.r.Guids.Bot == "" {
		return true
	}
	u, err := t.r.Client.Unit(ctx, t.r.Guids.Bot)
	if err != nil {
		t.sit.logf("ooc mana reset failed: max mana unreadable (%s)", trimLen(err.Error(), 120))
		return false
	}
	maxp := int(u.MaxPower)
	if maxp <= 0 {
		t.sit.logf("ooc mana reset failed: the hire's max mana is unknown")
		return false
	}
	target := int(float64(maxp) * pct / 100)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = t.r.Guard.Send(cctx, "modify_mana", TargetHire, t.r.Guids.Bot, itoa(target), itoa(maxp))
	cancel()
	if err != nil {
		t.sit.logf("ooc mana reset failed: %s", trimLen(err.Error(), 120))
		return false
	}
	t.r.Marks.Add(map[string]any{"kind": "ooc_mana_reset", "pct": pct})
	return true
}

// ---- ooc_hurt -----------------------------------------------------------

// runOOCHurt is the out-of-combat hurt window: no mobs, the victim (and the
// hire) stand in a quiet arena, and the victim is put at the middle of
// hp_band at the window start and every hurt_every_s after. The floor
// governor stays on, floor-only, and logs loudly if it ever fires.
func (t *tankRun) runOOCHurt(ctx context.Context) (map[string]any, error) {
	t.sitOpts, _ = pyjson.Object(t.r.Trial["situation"])
	if t.sitOpts == nil {
		t.sitOpts = map[string]any{}
	}
	t.parseSpecialOptions()
	player, _ := t.r.Trial["player"].(map[string]any)
	level, _ := intOpt(player, "level")
	if level <= 0 {
		level = 1
	}
	t.victim = "player"
	if v, _ := t.sitOpts["victim"].(string); v != "" {
		t.victim = v
	}
	t.band = [2]float64{40, 90}
	if list, ok := pyjson.List(t.sitOpts["hp_band"]); ok && len(list) == 2 {
		lo, _ := pyjson.Num(list[0])
		hi, _ := pyjson.Num(list[1])
		t.band = [2]float64{lo, hi}
	}
	t.floor, _ = intOpt(t.sitOpts, "floor_pct")
	if t.floor == 0 {
		t.floor = 30
	}
	t.maxRescues, _ = intOpt(t.sitOpts, "max_rescues_per_min")
	if t.maxRescues == 0 {
		t.maxRescues = maxRescuesPerMin
	}
	arena, why := t.acquireArena(ctx, level)
	if arena == nil {
		return map[string]any{"ok": false, "ended": "no arena", "invalid_reason": why}, nil
	}
	// The hire stands in the arena too: a healer out of range would score a
	// scene that is not the trial's.
	if name := t.r.BotName; name != "" {
		if why := t.bringHire(ctx, name); why != "" {
			return hireNotBrought(why), nil
		}
		if why := proveHireEngines(ctx, t.tb, t.r, t.sit); why != "" {
			return map[string]any{"ok": false, "ended": "no_engine", "invalid_reason": why}, nil
		}
	}
	var lastStranger time.Time
	if t.strangerTick(ctx, &lastStranger) {
		return t.strangerOutcome(), nil
	}
	wait, _ := numOpt(t.sitOpts, "ooc_wait_s")
	if wait <= 0 {
		wait = 60
	}
	gov := &Governor{Guard: t.r.Guard, Marks: t.r.Marks, Victim: t.sampleKey(),
		Band: [2]float64{t.band[0], 101}, Floor: t.floor, VictimGUID: t.victimGUID(),
		Now: t.sit.now, MaxRescues: t.maxRescues, OOC: true,
		Stopped: func() bool { return t.r.Collect.Lost() != "" },
		Log:     func(line string) { t.sit.logf("%s", line) }}
	if t.victim != "player" {
		name := t.r.BotName
		if idx := extraIndex(t.victim); idx > 0 && idx-1 < len(t.r.ExtraNames) {
			name = t.r.ExtraNames[idx-1]
		}
		gov.Hurt = func(pct float64) error {
			cctx, cancel := context.WithTimeout(context.Background(), governorCallTimeout)
			defer cancel()
			_, err := t.tb.FreeTextHurt(cctx, name, uint8(math.Round(pct)))
			return err
		}
		gov.Refill = func() error {
			cctx, cancel := context.WithTimeout(context.Background(), governorCallTimeout)
			defer cancel()
			_, err := t.tb.FreeTextHurt(cctx, name, 100)
			return err
		}
	}
	hook := t.r.Collect.TickHook(gov.Tick)
	defer t.r.Collect.RemoveTickHook(hook)
	// An extra_bots victim leaves the player standing: a floor-only governor
	// keeps the player from dying ungoverned.
	var guardHook int
	var guardGov *Governor
	if extraIndex(t.victim) > 0 {
		guardGov = &Governor{Guard: t.r.Guard, Marks: t.r.Marks, Victim: "player",
			Band: [2]float64{t.band[0], 101}, Floor: t.floor, VictimGUID: t.r.Guids.Player,
			Now: t.sit.now, MaxRescues: t.maxRescues, OOC: true,
			Stopped: func() bool { return t.r.Collect.Lost() != "" },
			Log:     func(line string) { t.sit.logf("%s", line) }}
		guardHook = t.r.Collect.TickHook(guardGov.Tick)
		defer t.r.Collect.RemoveTickHook(guardHook)
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_start", "mobs": []any{}, "ooc": true})
	start := t.sit.now()
	deadline := start.Add(time.Duration(wait * float64(time.Second)))
	nextHurt := start
	invalid := ""
	ended := "ooc"
	var stanceState time.Time
	for t.sit.now().Before(deadline) {
		if why := t.abortReason(); why != "" {
			invalid = why
			break
		}
		if t.strangerTick(ctx, &lastStranger) {
			break
		}
		if e := gov.EndedNow(); e != "" {
			ended = e
			break
		}
		if guardGov != nil {
			if e := guardGov.EndedNow(); e != "" {
				ended = e
				break
			}
		}
		sample := t.r.Collect.LastSample()
		if t.keepStance(ctx, sample, &stanceState) {
			// the stance was down out of combat: re-cast
		}
		now := t.sit.now()
		if !now.Before(nextHurt) {
			// the hurt line damages the tester or a bot: look again right before
			// it, whatever the cadence of the loop's own check.
			lastStranger = time.Time{}
			if t.strangerTick(ctx, &lastStranger) {
				break
			}
			what, detail, pctValue := t.hurtTick(ctx, sample)
			switch what {
			case "set":
				t.r.Marks.Add(map[string]any{"kind": "hurt", "hp": pctValue})
				nextHurt = now.Add(time.Duration(t.hurtEvery * float64(time.Second)))
			case "wait":
				t.sit.logf("hurt tick waits: %s", detail)
			default:
				t.sit.logf("hurt tick skipped: %s", detail)
				nextHurt = now.Add(time.Duration(t.hurtEvery * float64(time.Second)))
			}
		}
		sleepCtx(ctx, time.Second)
	}
	if t.strangerWhy != "" {
		t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "real_player"})
		return t.strangerOutcome(), nil
	}
	if invalid != "" {
		ended = "disconnected"
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": ended})
	if invalid != "" {
		return map[string]any{"ok": false, "ended": "disconnected", "invalid_reason": invalid}, nil
	}
	return map[string]any{"ok": true, "ended": ended}, nil
}

// hurtTick is one hurt attempt: set|in_band|skip|wait with a detail and the
// resulting percent. The core's `.modify hp` sets current AND maximum health,
// so a victim above the band is brought down with `.damage`; a victim below
// the band is left to its healer (a GM raise would shrink its maximum).
func (t *tankRun) hurtTick(ctx context.Context, sample map[string]any) (string, string, float64) {
	if sample == nil {
		return "wait", "no sample yet", 0
	}
	victim, _ := sample[t.sampleKey()].(map[string]any)
	hire, _ := sample["bot"].(map[string]any)
	if victim == nil || hire == nil {
		who := "victim"
		if victim != nil {
			who = "hire"
		}
		return "wait", "no sample for the " + who, 0
	}
	if boolOf(victim["in_combat"]) {
		return "skip", "victim in combat", 0
	}
	if boolOf(hire["in_combat"]) {
		return "skip", "hire in combat", 0
	}
	hp := int(intOf(victim["hp"]))
	maxHP := int(intOf(victim["max_hp"]))
	if hp <= 0 || maxHP <= 0 {
		return "wait", "victim hp unreadable", 0
	}
	percent := 100 * float64(hp) / float64(maxHP)
	if percent >= t.band[0] && percent <= t.band[1] {
		return "in_band", fmt.Sprintf("victim already at %.1f%%, inside the band", percent), round2(percent)
	}
	middle := (t.band[0] + t.band[1]) / 2
	target := int(math.Max(1, math.Min(float64(maxHP), middle*float64(maxHP)/100)))
	if target >= hp {
		return "skip", fmt.Sprintf("victim at %.1f%% is below the band; the core's .modify hp would shrink its maximum, so the healer is left to restore it", percent), 0
	}
	if t.victim == "player" {
		class := TargetSelf
		if err := t.r.Guard.Send(ctx, "damage", class, t.victimGUID(), itoa(hp-target)); err != nil {
			return "skip", "hurt line refused: " + trimLen(err.Error(), 120), 0
		}
	} else {
		name := t.r.BotName
		if idx := extraIndex(t.victim); idx > 0 && idx-1 < len(t.r.ExtraNames) {
			name = t.r.ExtraNames[idx-1]
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := t.tb.FreeTextHurt(cctx, name, uint8(math.Round(100*float64(target)/float64(maxHP))))
		cancel()
		if err != nil {
			return "skip", "hurt line refused: " + trimLen(err.Error(), 120), 0
		}
	}
	return "set", "", round2(100 * float64(target) / float64(maxHP))
}

// keepStance re-casts the trial's stance out of combat when the sample shows
// it down (threat() only runs in a fight).
func (t *tankRun) keepStance(ctx context.Context, sample map[string]any, last *time.Time) bool {
	player, _ := t.r.Trial["player"].(map[string]any)
	stance, ok := intOpt(player, "stance")
	if !ok || stance == 0 || sample == nil {
		return false
	}
	row, _ := sample["player"].(map[string]any)
	if row == nil {
		return false
	}
	list, ok := row["auras"].([]any)
	if !ok {
		return false
	}
	for _, v := range list {
		if int(intOf(v)) == stance {
			return false
		}
	}
	now := t.sit.now()
	if !last.IsZero() && now.Sub(*last) < time.Duration(stanceRecastGapS*float64(time.Second)) {
		return false
	}
	*last = now
	if err := t.r.Client.Cast(ctx, uint32(stance), t.r.Guids.Player); err != nil {
		t.sit.logf("stance %d re-cast failed: %s", stance, trimLen(err.Error(), 120))
		return false
	}
	t.sit.logf("stance %d was down out of combat: re-cast", stance)
	return true
}

// RepGap is the trial's `situation.min_rep_gap_s` as a duration (0 when the
// trial names none). The runner waits it between repeats; see the notes for
// why it is the full value and not the Python's potion-wall read.
func RepGap(trial map[string]any) time.Duration {
	sit, _ := pyjson.Object(trial["situation"])
	if sit == nil {
		return 0
	}
	secs, ok := numOpt(sit, "min_rep_gap_s")
	if !ok || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// ThreatSpell is the threat spell a stance's fight casts (355 Taunt for
// Defensive Stance, 0 when the stance has none).
func ThreatSpell(stance int) int { return threatCasts[stance] }
