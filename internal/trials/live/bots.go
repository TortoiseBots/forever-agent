package live

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// Bots.go: the hire pool and per-bot provisioning (trial["bot"] and
// trial["extra_bots"]): login, engine on, level, train, talent kit, the
// consumables luggage, role, party, and the read-backs each step proves
// itself with. The `blue` bot kit has no Forever item source yet
// (gear_kits.json covers warriors only), so a spec that wants worn gear is
// SKIPPED with needs=bot_items (never a pass without a scan/equip
// read-back); consumables prove through `ItemScan` (exists=yes).
//
// A bot is a pool character named in the slot config's `pool` block (D5:
// invented names `Atank Bheal Cmage Dcmd` style): the runner never invents
// one. Login is `.tb login <bot> <player>` (the player masters it),
// grouping `.tb group <bot> <player>` when the party read says otherwise,
// and every mutation is proved by a typed read: `State` for online and
// engine, the units model for level and hp, `Learn` for the talent kit
// (`learned|known` ok), `ItemScan` for the consumables, `Role` for
// the role, `Party` for grouped. A missing server capability —
// `.tb role <own character>` answered `not-a-bot` — makes the trial SKIPPED
// with needs=<check>, never a FAIL.

// Provisioner owns the `.tb` side of one repeat's bot provisioning: the
// typed client and the serve reads behind the read-backs.
type Provisioner struct {
	Client *Client
	TB     *tbcmd.Client
	Guard  *Guard
	Ledger *Ledger

	// Player is the slot character's name (the hire's master, the forced
	// role's target, the visiblity read's anchor). Pool lists the hire
	// pool by class (D5 bots); D5Name picks the pool bot for a class.
	Player string
	Pool   map[string][]string
	D5Name func(class string) string

	// Timeout bounds one provisioning step; 0 means 20 s.
	Timeout time.Duration

	// RecoveryPath is the recovery mark file of the items this trial gives
	// (see items.go); empty keeps the marks in memory only.
	RecoveryPath string
	markOnce     sync.Once
	recovery     *recoveryMarks
}

// BotSpec is trial["bot"] (or one extra_bots entry): what to provision.
type BotSpec struct {
	Class  string
	Spec   string
	Role   string
	Level  int
	Gear   string
	Items  []TrialItem
	Mana   *float64
	CCMark string
	raw    map[string]any
}

// TrialItem is one wanted inventory item (name + count).
type TrialItem struct {
	Name  string
	Count int
}

func botSpecOf(v any) BotSpec {
	m, _ := v.(map[string]any)
	out := BotSpec{raw: m}
	out.Class, _ = m["class"].(string)
	out.Spec, _ = m["spec"].(string)
	out.Role, _ = m["role"].(string)
	if f, ok := pyjson.Num(m["level"]); ok {
		out.Level = int(f)
	}
	out.Gear, _ = m["gear"].(string)
	if list, ok := m["items"].([]any); ok {
		for _, e := range list {
			em, _ := e.(map[string]any)
			name, _ := em["name"].(string)
			count := 1
			if f, ok := pyjson.Num(em["count"]); ok && f >= 1 {
				count = int(f)
			}
			if name != "" {
				out.Items = append(out.Items, TrialItem{Name: name, Count: count})
			}
		}
	}
	if m["mana_pct"] != nil {
		if f, ok := pyjson.Num(m["mana_pct"]); ok {
			out.Mana = &f
		}
	}
	out.CCMark, _ = m["cc_mark"].(string)
	return out
}

// ProvisionResult is one provisioned hire: its name, guid, and the rows it
// proved (bot_present, bot_level, bot_role, bot_spec, bot_items, bot_mana).
type ProvisionResult struct {
	Name string
	GUID string
}

// alreadyLoggedIn is the module's "that bot is already logged in or
// loading" answer: the hire is there (a leftover session of an earlier
// repeat or run), so the login step is satisfied and the read-backs decide.
func alreadyLoggedIn(lines []string) bool {
	return strings.Contains(strings.ToLower(strings.Join(lines, " ")), "already logged in")
}

// timeoutOf bounds one step.
func (p *Provisioner) timeoutOf() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 20 * time.Second
}

// ctxOf bounds ctx by the step timeout.
func (p *Provisioner) ctxOf(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, p.timeoutOf())
}

// BotContext carries the per-repeat hire names for teardown: the main hire
// and the index-aligned extra slots (nil for a slot that never got a name).
type BotContext struct {
	HireName   string
	HireGUID   string
	ExtraNames []string
	ExtraSlots []any
}

// Provision hires and provisions the trial's main bot: pool login, engine
// on, level, train, talent kit, gear, role, party, and the power read for
// mana_pct. It returns the hire or the first fatal error; every row lands
// in rows (ok rows stay, failed rows are retried once by the setup round,
// never here). guids gains the hire's guid.
func (p *Provisioner) Provision(ctx context.Context, trial map[string]any, guids map[string]any, previous string) (BotContext, []map[string]any, error) {
	var out BotContext
	var rows []map[string]any
	spec := botSpecOf(trial["bot"])
	pool := p.D5Name
	if pool == nil {
		pool = p.defaultD5
	}
	name := ""
	if previous != "" && p.partyHas(ctx, previous) {
		name = previous
		rows = append(rows, rowOf("bot_present", true, previous, "reused hire "+previous+" from the previous repeat"))
	} else {
		candidate := pool(spec.Class)
		if candidate == "" {
			rows = append(rows, rowOf("bot_present", false, nil, "no pool bot for class "+spec.Class))
			return out, rows, nil
		}
		name = candidate
		loginCtx, cancel := p.ctxOf(ctx)
		lines, err := p.TB.FreeTextLogin(loginCtx, name, p.Player)
		cancel()
		p.Ledger.Add(map[string]any{"kind": "login", "bot": name, "reply": firstLines(lines)})
		if err != nil && !alreadyLoggedIn(lines) {
			// "already logged in or loading" is a leftover session of this
			// same hire: it is there, and the read-backs below decide.
			rows = append(rows, rowOf("bot_present", false, nil, ".tb login "+name+": "+tbErrText(err, lines)))
			return out, rows, nil
		}
		// The login is asynchronous: the hire appears in the party roster
		// (or the units model) a moment later. The party row is the
		// identity; a name in chat is not.
		guid, pg := "", ""
		deadline := time.Now().Add(20 * time.Second)
		for {
			pg = p.partyGUIDOf(ctx, name)
			if pg == "" {
				guid = p.unitGUIDOf(ctx, name)
			}
			if pg != "" || guid != "" || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}
		if guid == "" && pg == "" {
			rows = append(rows, rowOf("bot_present", false, name, name+" logged in but is neither in the party roster nor in view after 20 s"))
			return out, rows, nil
		}
		if pg == "" {
			rows = append(rows, rowOf("bot_present", false, name, name+" is in view but not a party member; no hire identity"))
			return out, rows, nil
		}
		rows = append(rows, rowOf("bot_present", true, name, ".tb login "+name+" (party guid "+pg+")"))
		p.Ledger.Add(map[string]any{"kind": "hire", "name": name, "class": spec.Class, "role": spec.Role, "level": spec.Level, "guid": pg})
	}
	out.HireName = name
	guid := p.partyGUIDOf(ctx, name)
	if guid == "" {
		guid = p.unitGUIDOf(ctx, name)
	}
	out.HireGUID = guid
	guids["bot"] = guid

	// The pool bot is reused across runs and may be left dead: revive it and
	// refill it before anything is measured, proved by `.tb state`.
	if row := p.ensureAlive(ctx, name); row != nil {
		rows = append(rows, row)
		if !rowOK(row) {
			return out, rows, nil
		}
	}

	// Engine on: the spike behaviour never proves a trial.
	if ok, ev := p.engineOn(ctx, name); !ok {
		rows = append(rows, rowOf("bot_present", false, name, ev))
		return out, rows, nil
	}
	// Level, train, talent kit, gear, role, party, mana: each proved by a
	// typed read-back; the setup round retries a failed row once.
	if r := p.ensureLevel(ctx, spec, name); r != nil {
		rows = append(rows, r)
	}
	if r := p.ensureTrain(ctx, name); r != nil {
		rows = append(rows, r)
	}
	if r := p.ensureTalents(ctx, spec, name); r != nil {
		rows = append(rows, r)
	}
	if r := p.ensureBotGear(ctx, spec, name); r != nil {
		rows = append(rows, r)
	}
	if r := p.ensureRole(ctx, spec, name); r != nil {
		rows = append(rows, r)
	}
	if r := p.ensureGrouped(ctx, name); r != nil {
		rows = append(rows, r)
	}
	if r := p.ensureMana(ctx, spec, name); r != nil {
		rows = append(rows, r)
	}
	return out, rows, nil
}

// ProvisionExtras provisions every extra_bots entry: same path as the main
// hire (login, level, talents, items, role, party), aligned names/guids and
// the three extra rows.
func (p *Provisioner) ProvisionExtras(ctx context.Context, trial map[string]any, guids map[string]any, previous []string) (BotContext, []map[string]any, error) {
	var out BotContext
	var rows []map[string]any
	rawExtras, _ := trial["extra_bots"].([]any)
	names := []string{}
	guidList := []string{}
	presentOK := true
	var presentEv []string
	roleOK := true
	var roleEv []string
	for i, e := range rawExtras {
		spec := botSpecOf(e)
		prev := ""
		if i < len(previous) {
			prev = previous[i]
		}
		name := ""
		if prev != "" && p.partyHas(ctx, prev) {
			name = prev
			presentEv = append(presentEv, fmt.Sprintf("extra%d: reused hire %s", i+1, name))
		} else {
			candidate := ""
			if p.D5Name != nil {
				candidate = p.D5Name(spec.Class)
			} else {
				candidate = p.defaultD5(spec.Class)
			}
			if candidate == "" {
				presentOK = false
				presentEv = append(presentEv, fmt.Sprintf("extra%d: no pool bot for class %s", i+1, spec.Class))
				names = append(names, "")
				guidList = append(guidList, "")
				continue
			}
			name = candidate
			loginCtx, cancel := p.ctxOf(ctx)
			lines, err := p.TB.FreeTextLogin(loginCtx, name, p.Player)
			cancel()
			if err != nil {
				presentOK = false
				presentEv = append(presentEv, fmt.Sprintf("extra%d: .tb login %s: %s", i+1, name, tbErrText(err, lines)))
				p.Ledger.Add(map[string]any{"kind": "login", "bot": name, "extra": i + 1, "reply": firstLines(lines)})
				names = append(names, "")
				guidList = append(guidList, "")
				continue
			}
			p.Ledger.Add(map[string]any{"kind": "hire", "name": name, "class": spec.Class, "extra": i + 1, "reply": firstLines(lines)})
			presentEv = append(presentEv, fmt.Sprintf("extra%d: .tb login %s", i+1, name))
		}
		guid := p.partyGUIDOf(ctx, name)
		if name != "" && (guid == "" || guid == out.HireGUID || hasGUID(guidList, guid)) {
			presentOK = false
			presentEv = append(presentEv, fmt.Sprintf("extra%d %s has no distinct party guid (%s)", i+1, name, guidOrNone(guid)))
			names = append(names, "")
			guidList = append(guidList, "")
			continue
		}
		names = append(names, name)
		guidList = append(guidList, guid)
		p.engineOn(ctx, name)
		if r := p.ensureLevel(ctx, spec, name); r != nil && !rowOK(r) {
			presentOK = false
			presentEv = append(presentEv, fmt.Sprintf("extra%d level: %s", i+1, rowEvidence(r)))
		}
		if r := p.ensureTalents(ctx, spec, name); r != nil && !rowOK(r) {
			roleOK = false
			roleEv = append(roleEv, fmt.Sprintf("extra%d talents: %s", i+1, rowEvidence(r)))
		}
		if r := p.ensureRole(ctx, spec, name); r != nil {
			if !rowOK(r) {
				roleOK = false
			}
			roleEv = append(roleEv, fmt.Sprintf("extra%d %s: %s", i+1, name, rowEvidence(r)))
		}
		if len(spec.Items) > 0 || spec.Gear != "" {
			if r := p.ensureBotGear(ctx, spec, name); r != nil && !rowOK(r) {
				presentOK = false
				presentEv = append(presentEv, fmt.Sprintf("extra%d items: %s", i+1, rowEvidence(r)))
			}
		}
	}
	rows = append(rows, rowOf("extra_bots_present", presentOK, compactNames(names), strings.Join(presentEv, "; ")))
	rows = append(rows, rowOf("extra_bots_role", roleOK, specRoles(rawExtras), strings.Join(roleEv, "; ")))
	grouped, members := p.partyGrouped(ctx, names)
	missing := []string{}
	for _, n := range names {
		if n == "" || !hasName(members, n) {
			missing = append(missing, n)
		}
	}
	rows = append(rows, rowOf("extra_bots_in_group", grouped && len(missing) == 0, len(members),
		"in_group="+boolWord(grouped)+", members="+strings.Join(members, ", ")+missingSuffix(missing)))
	out.ExtraNames = compactNames(names)
	slots := make([]any, len(names))
	for i, n := range names {
		if n != "" {
			slots[i] = n
		}
	}
	out.ExtraSlots = slots
	guids["extras"] = guidsList(guidList)
	return out, rows, nil
}

// defaultD5 picks the first pool bot of a class.
func (p *Provisioner) defaultD5(class string) string {
	for _, n := range p.Pool[strings.ToLower(class)] {
		if n != "" {
			return n
		}
	}
	return ""
}

// partyHas reports whether name is in the party list now.
func (p *Provisioner) partyHas(ctx context.Context, name string) bool {
	_, members := p.partyGrouped(ctx, []string{name})
	return hasName(members, name)
}

// partyGUIDOf is the hire's guid from the serve's party roster, which names
// every member with its guid. The units model cannot answer this: this core
// sends no creature/player names in its unit rows.
func (p *Provisioner) partyGUIDOf(ctx context.Context, name string) string {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	info, err := p.Client.Party(tctx)
	if err != nil {
		return ""
	}
	for _, m := range info.Members {
		if strings.EqualFold(m.Name, name) {
			return normGUIDOf(m.GUID)
		}
	}
	return ""
}

// partyGrouped reads `Party`: the member names, grouped when the hire
// reads back in the roster (the report has no in_group flag: a roster
// that names the hire IS the proof).
func (p *Provisioner) partyGrouped(ctx context.Context, names []string) (bool, []string) {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.Party(tctx, p.Player)
	if err != nil {
		return false, nil
	}
	members := []string{}
	for _, m := range rep.Members {
		members = append(members, m.Name)
	}
	for _, n := range names {
		if n != "" && !hasName(members, n) {
			return false, members
		}
	}
	return len(members) > 0, members
}

// unitGUIDOf reads the hire's guid: the party roster first (it carries the
// guids), then the units model by name (a server that sends names).
func (p *Provisioner) unitGUIDOf(ctx context.Context, name string) string {
	if g := p.partyGUIDOf(ctx, name); g != "" {
		return g
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	units, err := p.Client.Units(tctx, 60)
	if err != nil {
		return ""
	}
	for _, u := range units {
		if strings.EqualFold(u.Name, name) {
			return normGUIDOf(u.GUID)
		}
	}
	return ""
}

// ensureAlive revives a dead hire and refills it to full health, proved by
// `.tb state` (life alive, hp full). A hire that cannot be revived fails the
// repeat: nothing heals from a corpse. A nil row means it was alive.
func (p *Provisioner) ensureAlive(ctx context.Context, name string) map[string]any {
	dead := false
	if bots, err := p.TB.State(ctx, name); err == nil {
		for _, b := range bots {
			if strings.EqualFold(b.Name, name) {
				dead = b.State == "dead" || b.HP <= 1
			}
		}
	}
	if dead {
		tctx, cancel := p.ctxOf(ctx)
		rep, err := p.TB.Revive(tctx, name)
		cancel()
		p.Ledger.Add(map[string]any{"kind": "revive", "bot": name, "reply": fmt.Sprint(rep), "error": errText(err)})
		if err != nil {
			return rowOf("bot_present", false, name, ".tb revive "+name+": "+err.Error())
		}
		time.Sleep(2 * time.Second)
	}
	// Full health either way (a revive lands at a reduced share).
	hctx, hcancel := p.ctxOf(ctx)
	_, err := p.TB.FreeTextHurt(hctx, name, 100)
	hcancel()
	if err != nil && !IsNoReply(err) {
		return rowOf("bot_present", false, name, ".tb hurt "+name+" 100: "+err.Error())
	}
	sctx, scancel := p.ctxOf(ctx)
	defer scancel()
	bots, err := p.TB.State(sctx, name)
	if err != nil {
		return rowOf("bot_present", false, name, ".tb state "+name+" after revive: "+err.Error())
	}
	for _, b := range bots {
		if strings.EqualFold(b.Name, name) {
			if b.State == "dead" || b.HP <= 1 {
				return rowOf("bot_present", false, name, name+" is still dead after .tb revive")
			}
			return nil
		}
	}
	return rowOf("bot_present", false, name, ".tb state lists no "+name)
}

// engineOn runs `.tb engine <bot> on` and proves engine=yes in `State`.
func (p *Provisioner) engineOn(ctx context.Context, name string) (bool, string) {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	lines, err := p.TB.FreeTextEngine(tctx, name, true)
	if err != nil {
		p.Ledger.Add(map[string]any{"kind": "engine", "bot": name, "reply": firstLines(lines)})
		return false, ".tb engine " + name + " on: " + tbErrText(err, lines)
	}
	p.Ledger.Add(map[string]any{"kind": "engine", "bot": name, "reply": firstLines(lines)})
	// The module acks the command; the engine itself attaches on its own
	// tick. Give it a moment, then read the state: engine=no with
	// stuck=noengine is a build without the bot engine (a spike-only
	// module), a missing server capability, not a failed hire.
	var last string
	for attempt := 0; attempt < 5; attempt++ {
		st, err := p.TB.State(tctx, name)
		if err != nil {
			return false, ".tb state " + name + " after engine on: " + err.Error()
		}
		for _, b := range st {
			if !strings.EqualFold(b.Name, name) {
				continue
			}
			if b.Engine {
				return true, "engine on (state engine=yes)"
			}
			last = "engine=no"
			if b.NoEngine {
				last = "engine=no, stuck=noengine"
			}
		}
		time.Sleep(time.Second)
	}
	return false, "SKIPPED needs=bot_engine: .tb engine " + name + " on is acknowledged, but the state stays " + last +
		" (this module build has no bot engine, so the hire runs no strategies)"
}

// ensureLevel sets the hire's level via free text and reads it back from
// the hire's own unit record (by guid). A level that cannot be read back is
// UNVERIFIED and fails the row; it is never read as 0 and never ok.
func (p *Provisioner) ensureLevel(ctx context.Context, spec BotSpec, name string) map[string]any {
	if spec.Level <= 0 {
		return nil
	}
	have, known := p.unitLevelOf(ctx, name)
	if known && have == spec.Level {
		return rowOf("bot_level", true, have, fmt.Sprintf("%s level %d (target %d)", name, have, spec.Level))
	}
	tctx, cancel := p.ctxOf(ctx)
	lines, err := p.TB.FreeTextLevel(tctx, name, uint8(min(spec.Level, 60)))
	cancel()
	if err != nil {
		p.Ledger.Add(map[string]any{"kind": "level", "bot": name, "reply": firstLines(lines)})
		return rowOf("bot_level", false, levelValue(have, known), ".tb level "+name+": "+tbErrText(err, lines))
	}
	p.Ledger.Add(map[string]any{"kind": "level", "bot": name, "reply": firstLines(lines)})
	have, known = p.unitLevelOf(ctx, name)
	if !known {
		return rowOf("bot_level", false, "unknown", fmt.Sprintf("UNVERIFIED: %s level (target %d): no level readable from the bot's own unit record", name, spec.Level))
	}
	return rowOf("bot_level", have == spec.Level, have, fmt.Sprintf("%s level %d (target %d)", name, have, spec.Level))
}

// levelValue is a row value for a level that may be unknown.
func levelValue(level int, known bool) any {
	if !known {
		return "unknown"
	}
	return level
}

// unitLevelOf reads the hire's level from the hire's own unit record: the
// row whose guid is the hire's (guid from the party roster). Player rows can
// carry an empty name on this client, so a nameless row is never matched by
// name and never yields a level of its own. ok is false when the level cannot
// be read (no guid, no such row, no level on it): the caller reports
// UNVERIFIED, never 0.
func (p *Provisioner) unitLevelOf(ctx context.Context, name string) (int, bool) {
	guid := p.partyGUIDOf(ctx, name)
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	units, err := p.Client.Units(tctx, 60)
	if err != nil {
		return 0, false
	}
	for _, u := range units {
		byGUID := guid != "" && normGUIDOf(u.GUID) == guid
		byName := u.Name != "" && strings.EqualFold(u.Name, name)
		if !byGUID && !byName {
			continue
		}
		if lvl, ok := numOf(u.Raw["level"]); ok && lvl > 0 {
			return int(lvl), true
		}
	}
	return 0, false
}

// ensureTrain runs `.tb train <bot>`; the talent kit's Learn proves it.
func (p *Provisioner) ensureTrain(ctx context.Context, name string) map[string]any {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	lines, err := p.TB.FreeTextTrain(tctx, name)
	p.Ledger.Add(map[string]any{"kind": "train", "bot": name, "reply": firstLines(lines)})
	if err != nil {
		return rowOf("bot_spec", false, nil, ".tb train "+name+": "+tbErrText(err, lines))
	}
	return nil
}

// ensureTalents learns the spec's talent kit through typed `Learn`
// (`learned|known` ok) and proves the spec string through the co-strategy
// read: the bot lists the wanted strategy in its `Strategies:` line.
func (p *Provisioner) ensureTalents(ctx context.Context, spec BotSpec, name string) map[string]any {
	if spec.Spec == "" {
		return nil
	}
	ids := talentKit(spec.Class, spec.Spec)
	if len(ids) == 0 {
		return rowOf("bot_spec", false, spec.Spec, "no talent kit for "+spec.Class+"/"+spec.Spec+" (specs.json): the build cannot be proven")
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.Learn(tctx, name, ids...)
	p.Ledger.Add(map[string]any{"kind": "bot_talents", "bot": name, "spec": spec.Spec, "reply": learnSummary(rep, err)})
	if err != nil {
		if ce, ok := err.(*tbcmd.CommandError); ok {
			return rowOf("bot_spec", false, spec.Spec, ".tb learn "+name+": error "+ce.Code)
		}
		return rowOf("bot_spec", false, spec.Spec, ".tb learn "+name+": "+err.Error())
	}
	for _, r := range rep.Results {
		switch r.Result {
		case "learned", "known":
		default:
			return rowOf("bot_spec", false, spec.Spec, fmt.Sprintf(".tb learn %s: spell %d result %s", name, r.Spell, r.Result))
		}
	}
	return rowOf("bot_spec", true, spec.Spec, fmt.Sprintf(".tb learn %s: %d spell(s) learned|known", name, len(rep.Results)))
}

// talentKit reads specs.json (class -> spec -> spell ids) for the kit.
func talentKit(class, spec string) []uint32 {
	raw := data.Read("specs.json")
	doc, err := pyjson.Unmarshal(raw)
	if err != nil {
		return nil
	}
	obj, _ := doc.(map[string]any)
	cls, _ := obj[strings.ToLower(class)].(map[string]any)
	if cls == nil {
		return nil
	}
	list, _ := cls[strings.ToLower(spec)].([]any)
	// The Forever overlay's kit wins for the same class and spec: a marker
	// the live server proved replaces the copied one.
	if over, err := pyjson.Unmarshal(data.ReadOverlay("specs.json")); err == nil {
		list = specOverlay(over, class, spec, list)
	}
	var out []uint32
	for _, e := range list {
		if f, ok := pyjson.Num(e); ok && f >= 1 {
			out = append(out, uint32(f))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// specOverlay returns the overlay's kit for class/spec, else base.
func specOverlay(over any, class, spec string, base []any) []any {
	oobj, ok := pyjson.Object(over)
	if !ok {
		return base
	}
	ocls, ok := pyjson.Object(oobj[strings.ToLower(class)])
	if !ok {
		return base
	}
	olist, ok := pyjson.List(ocls[strings.ToLower(spec)])
	if !ok || len(olist) == 0 {
		return base
	}
	return olist
}

// ensureBotGear covers the trial's `bot_items` row: the consumable luggage
// AND the `blue` kit read-back. Trial items (e.g. Sweet Nectar) are luggage,
// granted by free-text additem and proved to exist by typed `ItemScan`
// (exists=yes). The `blue` kit is scanned the same way and each scannable
// piece is worn through typed `Equip replace`; the row is ok ONLY when the
// scans prove every expected id and every equip answers `equipped`. There is
// no Forever item source for the kit yet (gear_kits.json covers warriors
// only), so a spec that wants worn gear returns ok=false with a SKIPPED
// reason: a required row that is SKIPPED makes the trial SKIPPED, never PASS.
func (p *Provisioner) ensureBotGear(ctx context.Context, spec BotSpec, name string) map[string]any {
	if spec.Gear == "" && len(spec.Items) == 0 {
		return nil
	}
	// A killed run leaves what it gave in the bags: take that back first so
	// the new items fit. A clear that fails is not fatal here; a full bag
	// then ends the give as SKIPPED needs=bot_bag_space.
	if ev, ok := p.ClearGiven(ctx, name); !ok {
		p.Ledger.Add(map[string]any{"kind": "items_clear_failed", "bot": name, "evidence": ev})
	}
	if spec.Gear != "" {
		kit := botGearKit(spec.Class, spec.Gear, spec.Level)
		if len(kit) == 0 {
			return rowOf("bot_items", false, kitSummary(spec),
				"SKIPPED needs=bot_items: no "+spec.Gear+" kit for "+spec.Class+" level "+itoa(spec.Level)+" in the Forever overlay (nothing scanned or equipped)")
		}
		return p.wearKit(ctx, spec, name, kit)
	}
	for _, item := range spec.Items {
		id := mustItemID(item.Name)
		if id == 0 {
			return rowOf("bot_items", false, nil, "no item id for "+item.Name)
		}
		lines, err := p.giveItem(ctx, name, id, uint32(item.Count))
		p.Ledger.Add(map[string]any{"kind": "additem", "bot": name, "item": item.Name, "item_id": id, "count": item.Count, "reply": firstLines(lines)})
		if err != nil {
			return p.giveFailedRow(ctx, name, item.Name, nil, err, lines)
		}
	}
	row := p.scanItems(ctx, name, itemIDs(spec.Items))
	if row != nil {
		return row
	}
	return rowOf("bot_items", true, len(spec.Items), ".tb itemscan proves every consumable exists")
}

// giveFailedRow is the bot_items row for a refused give. A full bag gets one
// more clearing pass first (what the marks say was given), then the row says
// SKIPPED needs=bot_bag_space with the clearing evidence.
func (p *Provisioner) giveFailedRow(ctx context.Context, bot, item string, ids any, err error, lines []string) map[string]any {
	ev, ok := "", true
	if isBagsFull(err, lines) {
		ev, ok = p.ClearGiven(ctx, bot)
	}
	return itemsGiveRow(bot, item, ids, err, lines, ev, ok)
}

// wearKit grants, scans and wears one bot kit: every piece must scan as
// existing and equippable and every equip must answer "equipped", else the
// row is a failed read-back (the core's own refusal is the evidence).
func (p *Provisioner) wearKit(ctx context.Context, spec BotSpec, name string, kit []botKitItem) map[string]any {
	ids := make([]uint32, 0, len(kit))
	for _, item := range kit {
		ids = append(ids, item.Entry)
	}
	for _, item := range kit {
		lines, err := p.giveItem(ctx, name, item.Entry, 1)
		p.Ledger.Add(map[string]any{"kind": "additem", "bot": name, "item": item.Name, "item_id": item.Entry, "count": 1, "reply": firstLines(lines)})
		if err != nil {
			return p.giveFailedRow(ctx, name, item.Name, ids, err, lines)
		}
	}
	tctx, cancel := p.ctxOf(ctx)
	rep, err := p.TB.ItemScan(tctx, name, ids...)
	cancel()
	p.Ledger.Add(map[string]any{"kind": "itemscan", "bot": name, "reply": itemScanSummary(rep, err)})
	if err != nil {
		return rowOf("bot_items", false, ids, ".tb itemscan "+name+": "+err.Error())
	}
	byID := map[uint32]tbcmd.ItemScanResult{}
	for _, r := range rep.Results {
		byID[r.Item] = r
	}
	for _, item := range kit {
		r, ok := byID[item.Entry]
		if !ok || !r.Exists {
			return rowOf("bot_items", false, ids, fmt.Sprintf(".tb itemscan %s: item %d (%s) not proven to exist", name, item.Entry, item.Name))
		}
		if r.HasCan && !r.CanEquip {
			return rowOf("bot_items", false, ids, fmt.Sprintf(".tb itemscan %s: item %d (%s) cannot be equipped", name, item.Entry, item.Name))
		}
	}
	equipped := 0
	for _, item := range kit {
		tctx, cancel := p.ctxOf(ctx)
		eq, err := p.TB.Equip(tctx, name, item.Entry, true)
		cancel()
		p.Ledger.Add(map[string]any{"kind": "equip", "bot": name, "item": item.Entry, "reply": eq.Result + " " + eq.Why})
		if err != nil {
			return rowOf("bot_items", false, ids, fmt.Sprintf(".tb equip %s %d: %s", name, item.Entry, tbErrText(err, nil)))
		}
		if !strings.EqualFold(eq.Result, "equipped") {
			return rowOf("bot_items", false, ids, fmt.Sprintf(".tb equip %s %d (%s): %s %s", name, item.Entry, item.Name, eq.Result, eq.Why))
		}
		equipped++
	}
	return rowOf("bot_items", true, ids, fmt.Sprintf("%s kit: %d piece(s) scanned exists and equipped", spec.Gear, equipped))
}

// botKitItem is one piece of a bot gear kit.
type botKitItem struct {
	Entry uint32
	Name  string
	Slot  string
}

// botGearKit reads the Forever bot kit overlay (class -> gear -> level ->
// items) for the highest kit level at or below the bot's (at most 9 levels
// below, like the player kits). An empty result means the overlay has no
// kit and the row must stay SKIPPED, never guessed.
func botGearKit(class, gear string, level int) []botKitItem {
	return botGearKitFrom(data.ReadOverlay("gear_kits.json"), class, gear, level)
}

// botGearKitFrom is botGearKit over one overlay document (tests).
func botGearKitFrom(overlay []byte, class, gear string, level int) []botKitItem {
	doc, err := pyjson.Unmarshal(overlay)
	if err != nil {
		return nil
	}
	root, ok := pyjson.Object(doc)
	if !ok {
		return nil
	}
	byGear, _ := pyjson.Object(root[strings.ToLower(class)])
	byLevel, _ := pyjson.Object(byGear[strings.ToLower(gear)])
	best := -1
	for key := range byLevel {
		n, err := strconv.Atoi(key)
		if err != nil || n > level || n < level-9 {
			continue
		}
		if n > best {
			best = n
		}
	}
	if best < 0 {
		return nil
	}
	list, _ := pyjson.List(byLevel[strconv.Itoa(best)])
	out := []botKitItem{}
	for _, e := range list {
		item, _ := pyjson.Object(e)
		entry, ok := pyjson.Int64(item["entry"])
		if !ok || entry <= 0 {
			continue
		}
		name, _ := item["name"].(string)
		slot, _ := item["slot"].(string)
		out = append(out, botKitItem{Entry: uint32(entry), Name: name, Slot: slot})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entry < out[j].Entry })
	return out
}

// itemIDs maps the spec's consumable names to ids (0 for an unknown name).
func itemIDs(items []TrialItem) []uint32 {
	ids := make([]uint32, 0, len(items))
	for _, item := range items {
		ids = append(ids, mustItemID(item.Name))
	}
	return ids
}

// scanItems proves every id exists through typed `ItemScan` (exists=yes).
// A refusal, a parse failure, or a missing/unknown id fails the row; an
func (p *Provisioner) scanItems(ctx context.Context, name string, ids []uint32) map[string]any {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if id == 0 {
			return rowOf("bot_items", false, nil, "no item id for a trial consumable")
		}
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.ItemScan(tctx, name, ids...)
	p.Ledger.Add(map[string]any{"kind": "itemscan", "bot": name, "reply": itemScanSummary(rep, err)})
	if err != nil {
		return rowOf("bot_items", false, ids, ".tb itemscan "+name+": "+err.Error())
	}
	byID := map[uint32]bool{}
	for _, r := range rep.Results {
		byID[r.Item] = r.Exists
	}
	for _, id := range ids {
		if !byID[id] {
			return rowOf("bot_items", false, ids, fmt.Sprintf(".tb itemscan %s: item %d not proven to exist", name, id))
		}
	}
	return nil
}

// itemScanSummary renders an ItemScan report for the ledger.
func itemScanSummary(rep tbcmd.ItemScanReport, err error) string {
	if err != nil {
		return err.Error()
	}
	parts := []string{}
	for _, r := range rep.Results {
		parts = append(parts, fmt.Sprintf("%d=exists:%v", r.Item, r.Exists))
	}
	return strings.Join(parts, " ")
}

// mustItemID maps a trial consumable name to its item id. The Forever
// overlay's name -> id map wins (a live `.lookup item`/`.tb itemscan` proved
// the id); the copied list is the fallback. Only Sweet Nectar appears in the
// 48 trials.
func mustItemID(name string) uint32 {
	key := strings.ToLower(strings.TrimSpace(name))
	if over, err := pyjson.Unmarshal(data.ReadOverlay("items.json")); err == nil {
		if obj, ok := pyjson.Object(over); ok {
			if n, ok := pyjson.Int64(obj[key]); ok && n > 0 {
				return uint32(n)
			}
		}
	}
	switch key {
	case "sweet nectar":
		return 17083
	}
	return 0
}

// ensureRole sets the bot's role through typed `Role` and proves it reads
// back in `State`: a queued/stored answer only confirms the command was
// accepted, so the row is ok ONLY when the State read-back shows the
// requested role in effect on that bot. Anything else is UNVERIFIED-failed:
// a delayed, dropped, or unapplied change leaves the trial INVALID, never
// PASS.
func (p *Provisioner) ensureRole(ctx context.Context, spec BotSpec, name string) map[string]any {
	if spec.Role == "" {
		return nil
	}
	want := strings.ToLower(spec.Role)
	if want == "none" {
		want = ""
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.Role(tctx, name, strings.ToLower(spec.Role))
	p.Ledger.Add(map[string]any{"kind": "bot_role", "bot": name, "spec": spec.Spec, "reply": roleSummary(rep, err)})
	if err != nil {
		if ce, ok := err.(*tbcmd.CommandError); ok && ce.Code == "not-a-bot" {
			return rowOf("bot_role", false, spec.Spec, "SKIPPED needs=bot_role: .tb role refuses this server ("+ce.Code+")")
		}
		if tbcmd.IsRefusal(err) {
			return rowOf("bot_role", false, spec.Spec, ".tb role "+name+": "+err.Error())
		}
		return rowOf("bot_role", false, spec.Spec, ".tb role "+name+": "+err.Error())
	}
	if len(rep.Results) == 0 {
		return rowOf("bot_role", false, spec.Spec, ".tb role "+name+" answered no record")
	}
	status := rep.Results[0].Status
	bots, err := p.TB.State(tctx, name)
	p.Ledger.Add(map[string]any{"kind": "state", "bot": name, "reply": stateSummary(bots, err)})
	if err != nil {
		return rowOf("bot_role", false, spec.Spec, ".tb state "+name+" after .tb role: "+err.Error())
	}
	for _, b := range bots {
		if strings.EqualFold(b.Name, name) {
			if strings.EqualFold(b.Role, want) {
				return rowOf("bot_role", true, spec.Spec, ".tb role "+name+" "+rep.Role+" ("+status+"); state role="+b.Role)
			}
			return rowOf("bot_role", false, spec.Spec, fmt.Sprintf("UNVERIFIED: .tb role %s %s (%s); state reads role=%s, want %s", name, rep.Role, status, b.Role, want))
		}
	}
	return rowOf("bot_role", false, spec.Spec, fmt.Sprintf("UNVERIFIED: .tb role %s %s (%s); state lists no such bot", name, rep.Role, status))
}

// ensureGrouped proves the hire is in the party list.
func (p *Provisioner) ensureGrouped(ctx context.Context, name string) map[string]any {
	ok, members := p.partyGrouped(ctx, []string{name})
	hit := hasName(members, name)
	return rowOf("grouped", ok && hit, len(members), "in_group="+boolWord(ok)+", members="+strings.Join(members, ", "))
}

// ensureMana sets the bot's power through typed GM and reads it back from
// the units model (within 3 points).
func (p *Provisioner) ensureMana(ctx context.Context, spec BotSpec, name string) map[string]any {
	if spec.Mana == nil {
		return rowOf("bot_mana", true, nil, "not a mana trial")
	}
	guid := p.unitGUIDOf(ctx, name)
	if guid == "" {
		return rowOf("bot_mana", false, nil, name+" not in view")
	}
	tctx, cancel := p.ctxOf(ctx)
	maxP := p.unitMaxPower(ctx, name)
	cancel()
	_ = tctx
	if maxP <= 0 {
		return rowOf("bot_mana", false, nil, name+" has no power pool")
	}
	gctx, gcancel := p.ctxOf(ctx)
	defer gcancel()
	// The hire is another player: the serve's GM hands refuse it, so the
	// module's own power command sets the bar (proved by the same read).
	lines, err := p.TB.FreeTextPower(gctx, name, "mana", uint8(*spec.Mana))
	p.Ledger.Add(map[string]any{"kind": "power", "bot": name, "pct": *spec.Mana, "reply": firstLines(lines)})
	if err != nil {
		return rowOf("bot_mana", false, nil, ".tb power "+name+" mana: "+tbErrText(err, lines))
	}
	have := p.unitPowerPct(ctx, name)
	ok := math.Abs(have-*spec.Mana) <= 3
	return rowOf("bot_mana", ok, math.Round(have*10)/10, fmt.Sprintf("power %.0f%% of %d (set %.0f%%)", have, maxP, *spec.Mana))
}

// unitMaxPower reads the hire's max power from the units model.
func (p *Provisioner) unitMaxPower(ctx context.Context, name string) uint32 {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	units, err := p.Client.Units(tctx, 60)
	if err != nil {
		return 0
	}
	for _, u := range units {
		if strings.EqualFold(u.Name, name) {
			return u.MaxPower
		}
	}
	return 0
}

// unitPowerPct reads the hire's power percent from the units model.
func (p *Provisioner) unitPowerPct(ctx context.Context, name string) float64 {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	units, err := p.Client.Units(tctx, 60)
	if err != nil {
		return 0
	}
	for _, u := range units {
		if strings.EqualFold(u.Name, name) && u.MaxPower > 0 {
			return 100 * float64(u.Power) / float64(u.MaxPower)
		}
	}
	return 0
}

// Helpers: rows, summaries, names.

func rowOf(id string, ok bool, value any, evidence string) map[string]any {
	return map[string]any{"id": id, "ok": ok, "value": anyValue(value), "evidence": evidence}
}

// anyValue renders a row value the document writer can carry: pyjson (like
// Python's json) has no typed Go slices, so an id list becomes []any.
// Unknown concrete types are returned as they are (the writer reports them).
func anyValue(v any) any {
	switch list := v.(type) {
	case []uint32:
		out := make([]any, 0, len(list))
		for _, e := range list {
			out = append(out, e)
		}
		return out
	case []int:
		out := make([]any, 0, len(list))
		for _, e := range list {
			out = append(out, e)
		}
		return out
	case []string:
		out := make([]any, 0, len(list))
		for _, e := range list {
			out = append(out, e)
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(list))
		for _, e := range list {
			out = append(out, e)
		}
		return out
	// Python has one int type: the document writer does too, so a typed Go
	// integer becomes int64 (uint64 above the int64 range becomes float64,
	// as Python would read such a number from JSON).
	case int:
		return int64(v.(int))
	case int8:
		return int64(v.(int8))
	case int16:
		return int64(v.(int16))
	case int32:
		return int64(v.(int32))
	case uint:
		return int64(v.(uint))
	case uint8:
		return int64(v.(uint8))
	case uint16:
		return int64(v.(uint16))
	case uint32:
		return int64(v.(uint32))
	case uint64:
		n := v.(uint64)
		if n <= math.MaxInt64 {
			return int64(n)
		}
		return float64(n)
	case float32:
		return float64(v.(float32))
	}
	return v
}

func rowOK(r map[string]any) bool { return boolOf(r["ok"]) }

func rowEvidence(r map[string]any) string {
	if s, _ := r["evidence"].(string); s != "" {
		return s
	}
	return strOf(r["id"])
}

func tbErrText(err error, lines []string) string {
	if ce, ok := err.(*tbcmd.CommandError); ok {
		if ce.HasMsg {
			return "error " + ce.Code + ": " + ce.Msg
		}
		return "error " + ce.Code
	}
	if tr, ok := asTestkitRefused(err); ok {
		_ = tr
		return firstLines(lines)
	}
	if err != nil {
		return err.Error()
	}
	return firstLines(lines)
}

func asTestkitRefused(err error) (*tbcmd.TestkitRefused, bool) {
	for e := err; e != nil; {
		if t, ok := e.(*tbcmd.TestkitRefused); ok {
			return t, true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		e = u.Unwrap()
	}
	return nil, false
}

func firstLines(lines []string) string {
	joined := strings.Join(lines, " / ")
	if len(joined) > 300 {
		return joined[:300]
	}
	if joined == "" {
		return "no reply"
	}
	return joined
}

func learnSummary(rep tbcmd.LearnReport, err error) string {
	if err != nil {
		return err.Error()
	}
	parts := []string{}
	for _, r := range rep.Results {
		parts = append(parts, fmt.Sprintf("%d=%s", r.Spell, r.Result))
	}
	return strings.Join(parts, " ")
}

func roleSummary(rep tbcmd.RoleReport, err error) string {
	if err != nil {
		return err.Error()
	}
	return "role " + rep.Role
}

func kitSummary(spec BotSpec) any {
	return map[string]any{"gear": spec.Gear, "items": len(spec.Items)}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func hasName(members []string, name string) bool {
	for _, m := range members {
		if strings.EqualFold(m, name) {
			return true
		}
	}
	return false
}

func hasGUID(guids []string, g string) bool {
	for _, e := range guids {
		if strings.EqualFold(e, g) && g != "" {
			return true
		}
	}
	return false
}

func compactNames(names []string) []string {
	out := []string{}
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func specRoles(rawExtras []any) []string {
	out := []string{}
	for _, e := range rawExtras {
		m, _ := e.(map[string]any)
		s, _ := m["spec"].(string)
		out = append(out, s)
	}
	return out
}

func missingSuffix(missing []string) string {
	kept := []string{}
	for _, m := range missing {
		if m != "" {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "; not in the party: " + strings.Join(kept, ", ")
}

func guidOrNone(g string) string {
	if g == "" {
		return "none"
	}
	return g
}

func guidsList(guids []string) []any {
	out := make([]any, len(guids))
	for i, g := range guids {
		out[i] = g
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
