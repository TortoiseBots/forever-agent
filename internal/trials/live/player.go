package live

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// Player.go: the slot character's own setup (trial["player"]): `.levelup`
// to the trial level, `.learn` of the `talents.spells` kit, the stance aura
// by `cast` (proved held in the player's auras), `.tb gm <player> off` +
// `.gm visible on` (the bots must see the player: `player_visible` proves
// against a named bot's `.tb state` with a numeric `dm`; without a hire yet
// the row stays UNVERIFIED-failed and the setup round finalizes it after
// provisioning), and the forced role + gear through TB-1
// (`role [<character>]`, `equip [<character>]` with the no-name form for
// the sender's own character: `.tb role tank`, `.tb equip <id>`). The
// player wears no gear on the non-gear trials; the tier kit strip lives in
// setup (no Forever source for the old strip path).
//
// A missing server capability (`.tb role <own character>` answered
// `not-a-bot`) makes the trial SKIPPED with needs=player_forced_role —
// never a FAIL. Every other failed step is a failed row the setup round
// retries once.

// PlayerSetup owns the player side of one repeat's setup.
type PlayerSetup struct {
	Client *Client
	TB     *tbcmd.Client
	Guard  *Guard
	Ledger *Ledger

	// Player is the slot character's name (the GM sender, the role/equip
	// target, the visibility anchor).
	Player string

	// Timeout bounds one wait inside a step (one cast, one GM line, one
	// read), not the whole step; 0 means 20 s.
	Timeout time.Duration

	// Poll is the pause between aura reads; 0 means 500 ms.
	Poll time.Duration
}

func (p *PlayerSetup) poll() time.Duration {
	if p.Poll > 0 {
		return p.Poll
	}
	return 500 * time.Millisecond
}

// OpTimeout is a real timeout of one setup wait (a cast, a GM line): the
// deadline of its own wait expired, not a refusal by the server and not a
// budget another wait used up. Rows name it "timed out".
type OpTimeout struct {
	Op    string
	After time.Duration
	Err   error
}

func (e *OpTimeout) Error() string {
	return fmt.Sprintf("%s timed out after %s", e.Op, e.After)
}
func (e *OpTimeout) Unwrap() error { return e.Err }

// timedOut wraps a context deadline error of op's own wait as an *OpTimeout;
// any other error (a refusal, a transport error) is returned unchanged.
func (p *PlayerSetup) timedOut(op string, err error) error {
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return &OpTimeout{Op: op, After: p.timeoutOf(), Err: err}
	}
	return err
}

func (p *PlayerSetup) timeoutOf() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 20 * time.Second
}

func (p *PlayerSetup) ctxOf(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, p.timeoutOf())
}

// SetupPlayer runs the whole player side: level, talents, stance, visible,
// forced role. It returns the rows (in trial order) and the first fatal
// error (a transport failure, never a failed row).
func (p *PlayerSetup) SetupPlayer(ctx context.Context, trial map[string]any) ([]map[string]any, error) {
	var rows []map[string]any
	if r, err := p.ensureLevel(ctx, trial); err != nil {
		return rows, err
	} else if r != nil {
		rows = append(rows, r)
	}
	if r, err := p.ensureTalents(ctx, trial); err != nil {
		return rows, err
	} else if r != nil {
		rows = append(rows, r)
	}
	if r, err := p.ensureStance(ctx, trial); err != nil {
		return rows, err
	} else if r != nil {
		rows = append(rows, r)
	}
	if r, err := p.ensureVisible(ctx); err != nil {
		return rows, err
	} else if r != nil {
		rows = append(rows, r)
	}
	if r, skip, err := p.ensureForcedRole(ctx, trial); err != nil {
		return rows, err
	} else if skip != "" {
		rows = append(rows, rowOf("player_forced_role", false, nil, "SKIPPED needs=player_forced_role: "+skip))
	} else if r != nil {
		rows = append(rows, r)
	}
	return rows, nil
}

// ensureLevel brings the player to the trial level via `.levelup` (signed
// delta) and reads it back from `status`.
func (p *PlayerSetup) ensureLevel(ctx context.Context, trial map[string]any) (map[string]any, error) {
	want := trialLevel(trial)
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	level := p.playerLevel(tctx)
	if level == want {
		return rowOf("player_level", true, level, fmt.Sprintf("level %d (target %d)", level, want)), nil
	}
	lowered := ""
	if level > want {
		lowered = fmt.Sprintf("lowered from %d; ", level)
	}
	// The core's `.levelup <n>` moves the level BY n (the delta, not an
	// absolute level), so the row re-reads before each step and waits for
	// the server to land it: a retried row must not add the delta twice.
	for attempt := 0; attempt < 3 && level != want; attempt++ {
		delta := want - level
		if err := p.Guard.Send(tctx, "levelup", TargetSelf, p.playerGUIDOf(tctx), itoa(delta)); err != nil {
			return rowOf("player_level", false, level, ".levelup refused: "+err.Error()), nil
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			if got := p.playerLevel(tctx); got != level {
				level = got
				break
			}
		}
	}
	return rowOf("player_level", level == want, level, fmt.Sprintf("%slevel %d (target %d, .levelup delta)", lowered, level, want)), nil
}

// playerLevel reads the session character's level from `status`.
func (p *PlayerSetup) playerLevel(ctx context.Context) int {
	st, err := p.Client.StatusOf(ctx)
	if err != nil {
		return 0
	}
	if f, ok := pyjson.Num(st.Raw["level"]); ok {
		return int(f)
	}
	return 0
}

// trialLevel reads trial["player"]["level"] (40 when absent or bad).
func trialLevel(trial map[string]any) int {
	player, _ := trial["player"].(map[string]any)
	if f, ok := pyjson.Num(player["level"]); ok && f >= 1 && f <= 60 {
		return int(f)
	}
	return 40
}

// ensureTalents learns the player's talent kit: `.learn` per spell id
// through the guard, proved by the typed `Learn` read-back of the sender's
// own character (`learned|known` ok).
func (p *PlayerSetup) ensureTalents(ctx context.Context, trial map[string]any) (map[string]any, error) {
	player, _ := trial["player"].(map[string]any)
	tal, _ := player["talents"].(map[string]any)
	var ids []uint32
	if list, ok := tal["spells"].([]any); ok {
		for _, e := range list {
			if f, ok := pyjson.Num(e); ok && f >= 1 {
				ids = append(ids, uint32(f))
			}
		}
	}
	level := trialLevel(trial)
	if len(ids) == 0 {
		if tree, _ := tal["tree"].(string); tree == "none" && level < 10 {
			return rowOf("player_talents", true, []any{}, "no talents (tree none, level < 10)"), nil
		}
		return rowOf("player_talents", false, []any{}, "talents missing: no spell ids"), nil
	}
	for _, id := range ids {
		// The guard learns on the sender's own character (self target):
		// `.learn <id>` after the selection dance serve does per call, each
		// line within its own deadline.
		if err := p.guardSend(ctx, "learn", p.playerGUIDOf(ctx), fmt.Sprint(id)); err != nil {
			what := "refused"
			var to *OpTimeout
			if errors.As(err, &to) {
				what = "timed out"
			}
			return rowOf("player_talents", false, missingIDs(ids, nil), ".learn "+what+": "+err.Error()), nil
		}
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.Learn(tctx, "", ids...)
	p.Ledger.Add(map[string]any{"kind": "player_talents", "reply": learnSummary(rep, err)})
	if err != nil {
		if ce, ok := err.(*tbcmd.CommandError); ok && ce.Code == "not-a-bot" {
			return rowOf("player_talents", false, nil, "SKIPPED needs=player_talents: .tb learn refuses the sender's own character ("+ce.Code+")"), nil
		}
		return rowOf("player_talents", false, missingIDs(ids, nil), ".tb learn read-back: "+err.Error()), nil
	}
	missing := []uint32{}
	for _, r := range rep.Results {
		switch r.Result {
		case "learned", "known":
		default:
			missing = append(missing, r.Spell)
		}
	}
	if len(missing) > 0 {
		return rowOf("player_talents", false, missing, "talents not learned: "+fmt.Sprint(missing)), nil
	}
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return rowOf("player_talents", true, out, "spells read back learned|known"), nil
}

// ensureStance casts the stance aura on the sender's own character (proved
// held in the player's auras from `units`/`unit`). A stance can be refused
// for a moment after a level jump (a spell in flight, a cooldown): the cast
// is retried, then the stance spell is learned and cast again, and the aura
// read-back decides.
func (p *PlayerSetup) ensureStance(ctx context.Context, trial map[string]any) (map[string]any, error) {
	player, _ := trial["player"].(map[string]any)
	stance := 0
	if f, ok := pyjson.Num(player["stance"]); ok {
		stance = int(f)
	}
	if player["stance"] == nil || stance <= 0 {
		return rowOf("player_stance", true, nil, "no stance/form for this trial"), nil
	}
	// Every wait below has its own deadline: a slow selection echo or a few
	// slow aura reads must never use up the budget of the cast or the
	// .learn after it (the live rerun's "cast refused (context deadline
	// exceeded) and .learn refused" was one shared 20 s step budget).
	self := p.playerGUIDOf(ctx)
	// Drop every aura first: the stance lands on a clean character.
	_ = p.guardSend(ctx, "unaura", self, "all")
	held := func() bool {
		for i := 0; i < 4; i++ {
			if p.auraHeld(ctx, self, stance) {
				return true
			}
			sleepCtx(ctx, p.poll())
		}
		return false
	}
	var lastErr error
	cast := func() {
		cctx, cancel := p.ctxOf(ctx)
		defer cancel()
		if err := p.Client.Cast(cctx, uint32(stance), self); err != nil && !IsNoReply(err) {
			lastErr = p.timedOut("cast", err)
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		cast()
		if held() {
			return rowOf("player_stance", true, stance, fmt.Sprintf("aura %d held", stance)), nil
		}
	}
	// The spell may be unknown: learn what the trial needs, cast again.
	if err := p.guardSend(ctx, "learn", self, itoa(stance)); err != nil {
		what := "refused"
		var to *OpTimeout
		if errors.As(err, &to) {
			what = "timed out"
		}
		return rowOf("player_stance", false, stance, "cast failed ("+errText(lastErr)+") and .learn "+what+": "+err.Error()), nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		cast()
		if held() {
			return rowOf("player_stance", true, stance, fmt.Sprintf("aura %d held after .learn", stance)), nil
		}
	}
	return rowOf("player_stance", false, stance, fmt.Sprintf("stance %d not held after 5 casts (%s)", stance, errText(lastErr))), nil
}

// guardSend sends one GM line on the player's own character within its own
// deadline; a deadline of that wait is an *OpTimeout.
func (p *PlayerSetup) guardSend(ctx context.Context, kind, self string, args ...string) error {
	sctx, cancel := p.ctxOf(ctx)
	defer cancel()
	return p.timedOut(".gm "+kind, p.Guard.Send(sctx, kind, TargetSelf, self, args...))
}

// errText is an error's text, or "no error".
func errText(err error) string {
	if err == nil {
		return "no error"
	}
	return err.Error()
}

// auraHeld reports whether guid's unit row holds aura id.
func (p *PlayerSetup) auraHeld(ctx context.Context, guid string, spell int) bool {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	units, err := p.Client.Units(tctx, 60)
	if err != nil {
		return false
	}
	for _, u := range units {
		if normGUIDOf(u.GUID) != normGUIDOf(guid) {
			continue
		}
		for _, a := range sortedSpellIDs(u.Raw["auras"]) {
			if f, ok := pyjson.Num(a); ok && int(f) == spell {
				return true
			}
		}
	}
	// The player is never in units: ask the unit rpc.
	u, err := p.Client.Unit(tctx, guid)
	if err != nil {
		return false
	}
	for _, a := range sortedSpellIDs(u.Raw["auras"]) {
		if f, ok := pyjson.Num(a); ok && int(f) == spell {
			return true
		}
	}
	return false
}

// playerGUIDOf reads the sender's own guid from `status`.
func (p *PlayerSetup) playerGUIDOf(ctx context.Context) string {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	st, err := p.Client.StatusOf(tctx)
	if err != nil {
		return ""
	}
	return normGUIDOf(st.GUID)
}

// ensureVisible runs `.tb gm <player> off` + `.gm visible on`: the player
// must be visible to the bots. Proof is a named bot's `.tb state` with a
// numeric `dm` — the module's own answer that the bot sees the player. The
// read needs a hire name (State is addressed to one bot), so the player
// side runs it when it can and the setup round finalizes it after
// provisioning; a State without a bot at a numeric dm keeps the row failed
// (never a pass), and the in-fight row re-proves it from the rep's events.
func (p *PlayerSetup) ensureVisible(ctx context.Context) (map[string]any, error) {
	return p.ensureVisibleFor(ctx, "")
}

// ensureVisibleFor proves visibility for one hire ("" defers the read: the
// commands are sent here and the setup round finalizes the row after the
// hire exists).
func (p *PlayerSetup) ensureVisibleFor(ctx context.Context, bot string) (map[string]any, error) {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	gm, err := p.TB.GameMaster(tctx, p.Player, false)
	p.Ledger.Add(map[string]any{"kind": "gm", "bot": p.Player, "reply": gmSummary(gm, err)})
	if err != nil {
		if ce, ok := err.(*tbcmd.CommandError); ok {
			return rowOf("player_visible", false, nil, ".tb gm "+p.Player+" off refused: error "+ce.Code), nil
		}
		return rowOf("player_visible", false, nil, ".tb gm "+p.Player+" off: "+err.Error()), nil
	}
	if err := p.Guard.Send(tctx, "gm_visible_on", TargetSelf, p.playerGUIDOf(tctx)); err != nil {
		return rowOf("player_visible", false, nil, ".gm visible on refused: "+err.Error()), nil
	}
	if bot == "" {
		return rowOf("player_visible", false, nil,
			"UNVERIFIED: .tb gm "+p.Player+" off; .gm visible on sent; no hire to read back from yet"), nil
	}
	tctx2, cancel2 := p.ctxOf(ctx)
	defer cancel2()
	bots, err := p.TB.State(tctx2, bot)
	p.Ledger.Add(map[string]any{"kind": "state", "bot": bot, "reply": stateSummary(bots, err)})
	if err != nil {
		return rowOf("player_visible", false, nil, ".tb state "+bot+" after .gm visible on: "+err.Error()), nil
	}
	for _, b := range bots {
		if strings.EqualFold(b.Name, bot) && b.DistMaster.Has && b.DistMaster.Kind == "" {
			return rowOf("player_visible", true, b.DistMaster.Yards,
				fmt.Sprintf(".tb gm %s off; .gm visible on; .tb state %s dm=%.1f (the bot sees the player)", p.Player, bot, b.DistMaster.Yards)), nil
		}
	}
	return rowOf("player_visible", false, nil,
		fmt.Sprintf("UNVERIFIED: .tb state %s has no numeric dm (the bot does not prove it sees the player)", bot)), nil
}

// stateSummary renders a State answer for the ledger.
func stateSummary(bots []tbcmd.BotState, err error) string {
	if err != nil {
		return err.Error()
	}
	parts := []string{}
	for _, b := range bots {
		dm := "no-dm"
		if b.DistMaster.Has {
			if b.DistMaster.Kind != "" {
				dm = "dm=" + b.DistMaster.Kind
			} else {
				dm = fmt.Sprintf("dm=%.1f", b.DistMaster.Yards)
			}
		}
		parts = append(parts, b.Name+" "+dm)
	}
	return strings.Join(parts, " ")
}

// ensureForcedRole stores the trial's forced role (or clears it) through
// TB-1's `.tb role [<character>] <role>` with the no-name form for the
// sender's own character. A `not-a-bot` capability refusal returns skip:
// the trial is SKIPPED with needs=player_forced_role.
func (p *PlayerSetup) ensureForcedRole(ctx context.Context, trial map[string]any) (map[string]any, string, error) {
	player, _ := trial["player"].(map[string]any)
	role := "clear"
	if player["forced_role"] != nil {
		if s, ok := player["forced_role"].(string); ok && s != "" {
			role = strings.ToLower(s)
		} else {
			role = "clear"
		}
	}
	switch role {
	case "tank", "healer", "dps", "none", "clear":
	default:
		return rowOf("player_forced_role", false, role, "unknown forced role "+role), "", nil
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.RoleSelf(tctx, role)
	p.Ledger.Add(map[string]any{"kind": "role", "role": role, "reply": roleSummary(rep, err)})
	if err != nil {
		if ce, ok := err.(*tbcmd.CommandError); ok && ce.Code == "not-a-bot" {
			return nil, ".tb role refuses the sender's own character (error not-a-bot)", nil
		}
		return rowOf("player_forced_role", false, role, ".tb role "+role+": "+err.Error()), "", nil
	}
	if len(rep.Results) == 0 {
		return rowOf("player_forced_role", false, role, ".tb role "+role+" answered no record"), "", nil
	}
	value := role
	if role == "clear" {
		value = "clear"
	}
	return rowOf("player_forced_role", true, value, ".tb role "+role+" ("+rep.Results[0].Status+")"), "", nil
}

func missingIDs(want []uint32, have map[uint32]bool) []uint32 {
	var out []uint32
	for _, id := range want {
		if !have[id] {
			out = append(out, id)
		}
	}
	return out
}

func gmSummary(rep tbcmd.GMReport, err error) string {
	if err != nil {
		return err.Error()
	}
	return "gm=" + boolWord(rep.GM)
}
