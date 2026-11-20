package live

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The closed template set of gm_guard.py for ForeverCore, rendered here
// instead of raw text: levelup learn unaura aura damage modify_hp
// modify_mana modify_rage modify_energy npc_add_temp go_xyz additem cooldown
// die gm_off gm_visible_on lookup_item. `.tb` lines go through TBM only.
// There is one `gm` call site in this package (Guard.Send); the test
// TestGuardOneCallSite greps for it.
var guardTemplates = map[string]string{
	"levelup":       ".levelup {0}",
	"learn":         ".learn {0}",
	"unaura":        ".unaura {0}",
	"aura":          ".aura {0}",
	"damage":        ".damage {0}",
	"modify_hp":     ".modify hp {0} {1}",
	"modify_mana":   ".modify mana {0} {1}",
	"modify_rage":   ".modify rage {0} {1}",
	"modify_energy": ".modify energy {0} {1}",
	"npc_add_temp":  ".npc add temp {0}",
	"go_xyz":        ".go xyz {0} {1} {2} {3}",
	"additem":       ".additem {0} {1}",
	"cooldown":      ".cooldown",
	"die":           ".die",
	"gm_off":        ".gm off",
	"gm_visible_on": ".gm visible on",
	"lookup_item":   ".lookup item {0}",
}

// guardClassifiers is the CLASSIFY table: raw text is refused unless it
// matches one of these whole-line patterns.
var guardClassifiers = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`\.gm visible on`), "gm_visible_on"},
	{regexp.MustCompile(`\.gm off`), "gm_off"},
	{regexp.MustCompile(`\.levelup (-?\d{1,2})`), "levelup"},
	{regexp.MustCompile(`\.learn (\d{1,6})`), "learn"},
	{regexp.MustCompile(`\.unaura (all|\d{1,6})`), "unaura"},
	{regexp.MustCompile(`\.aura (\d{1,6})`), "aura"},
	{regexp.MustCompile(`\.damage (\d{1,6})`), "damage"},
	{regexp.MustCompile(`\.modify hp (\d{1,6}) (\d{1,6})`), "modify_hp"},
	{regexp.MustCompile(`\.modify mana (\d{1,6}) (\d{1,6})`), "modify_mana"},
	{regexp.MustCompile(`\.modify rage (\d{1,3}) (\d{1,3})`), "modify_rage"},
	{regexp.MustCompile(`\.modify energy (\d{1,3}) (\d{1,3})`), "modify_energy"},
	{regexp.MustCompile(`\.npc add temp (\d{1,6})`), "npc_add_temp"},
	{regexp.MustCompile(`\.go xyz (-?\d{1,6}) (-?\d{1,6}) (-?\d{1,6})(?: (\d{1,2}))?`), "go_xyz"},
	{regexp.MustCompile(`\.additem (\d{1,6}) (-?\d{1,2})`), "additem"},
	{regexp.MustCompile(`\.cooldown`), "cooldown"},
	{regexp.MustCompile(`\.die`), "die"},
	{regexp.MustCompile(`\.lookup item ([A-Za-z0-9 ':.\-]{1,60})`), "lookup_item"},
}

// guardTargets bounds each kind to a target class, as TARGETS does.
var guardTargets = map[string][]string{
	"gm_visible_on": {"none", "self"},
	"gm_off":        {"none", "self"},
	"levelup":       {"self"},
	"learn":         {"self"},
	"unaura":        {"self", "hire"},
	"aura":          {"self", "hire"},
	"damage":        {"self"},
	"modify_hp":     {"self", "hire"},
	"modify_mana":   {"hire"},
	"modify_rage":   {"hire"},
	"modify_energy": {"hire"},
	"npc_add_temp":  {"none"},
	"go_xyz":        {"none", "self"},
	"additem":       {"hire"},
	"cooldown":      {"self", "hire"},
	"die":           {"summon"},
	"lookup_item":   {"none"},
}

// GmRefused is raw text outside the closed set: never sent.
type GmRefused struct{ Text string }

func (e *GmRefused) Error() string {
	return fmt.Sprintf("gm refused (outside the closed set): %q", e.Text)
}

// guardArgCounts is the closed template arity: Send rejects extra args, and
// SendText's re-extracted groups must match it, so unfilled {n} slots and
// trailing text can never reach the server.
var guardArgCounts = map[string]int{
	"levelup": 1, "learn": 1, "unaura": 1, "aura": 1, "damage": 1,
	"modify_hp": 2, "modify_mana": 2, "modify_rage": 2, "modify_energy": 2,
	"npc_add_temp": 1, "go_xyz": 4, "additem": 2, "cooldown": 0, "die": 0,
	"gm_off": 0, "gm_visible_on": 0, "lookup_item": 1,
}

// ClassifyGuard names the template kind of raw text, or "" when no template
// matches it whole. The match is on the single line with no surrounding
// whitespace or control characters: leading/trailing newlines never pass.
func ClassifyGuard(text string) string {
	if text == "" || strings.TrimSpace(text) != text || strings.ContainsAny(text, "\r\n\x00") {
		return ""
	}
	for _, c := range guardClassifiers {
		m := c.re.FindStringSubmatch(text)
		if len(m) == 0 {
			continue
		}
		if m[0] == text {
			return c.kind
		}
	}
	return ""
}

// GuardTarget is one send target class: who the line may act on.
type GuardTarget string

const (
	TargetNone   GuardTarget = "none"
	TargetSelf   GuardTarget = "self"
	TargetHire   GuardTarget = "hire"
	TargetSummon GuardTarget = "summon"
)

// Guard is the only path for GM text: Send renders a closed template after
// checking the target class, and SendText classifies raw text against the
// same closed set and refuses anything else.
type Guard struct {
	client     *Client
	playerGUID string
	ledger     *Ledger
	marks      *Marks
	onGM       func(kind, text, target string)
	dryRun     bool
	aborted    func() string

	// hire, extras and summons are the rest of the trial's unit scope: the
	// guids the serve's GM-unit allowlist must hold before a hand acts.
	hire     string
	extras   []string
	summons  func() []string
	mu       sync.Mutex
	lastUnit string
}

// GuardOptions carries the ledger, the marks file (for the gm rows), the
// player's guid (the "self" target), and the dry-run/abort switches.
type GuardOptions struct {
	Ledger     *Ledger
	Marks      *Marks
	PlayerGUID string
	HireGUID   string
	ExtraGUIDs []string
	// Summons reports the guids this repeat summoned so far (the ledger's
	// own rows); a hand on a summon must find it there first.
	Summons func() []string
	DryRun  bool
	Abort   func() string
	OnGM    func(kind, text, target string)
}

// AbortReason reports the runner's abort reason ("", while the run holds
// its lease): the aborted callback the runner wires from Hold.RenewErr.
func (g *Guard) AbortReason() string {
	if g == nil || g.aborted == nil {
		return ""
	}
	return g.aborted()
}

// NewGuard returns the runner's GM guard over client.
func NewGuard(client *Client, opts GuardOptions) *Guard {
	g := &Guard{client: client, ledger: opts.Ledger, marks: opts.Marks,
		playerGUID: normGUIDOf(opts.PlayerGUID), dryRun: opts.DryRun,
		aborted: opts.Abort, onGM: opts.OnGM, hire: normGUIDOf(opts.HireGUID),
		summons: opts.Summons}
	for _, e := range opts.ExtraGUIDs {
		if v := normGUIDOf(e); v != "" {
			g.extras = append(g.extras, v)
		}
	}
	return g
}

// SetHire sets the hire guid once the setup knows it (the guard is built
// before provisioning).
func (g *Guard) SetHire(guid string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hire = normGUIDOf(guid)
}

// normGUIDOf normalises one guid the way the runner compares them.
func normGUIDOf(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// allowedTarget reports whether kind may act on targetClass for target guid.
func (g *Guard) allowedTarget(kind string, class GuardTarget, target string) bool {
	want, ok := guardTargets[kind]
	if !ok {
		return false
	}
	hit := false
	for _, w := range want {
		if w == string(class) {
			hit = true
		}
	}
	if !hit {
		return false
	}
	tg := normGUIDOf(target)
	if tg == "" && (class == TargetSelf || class == TargetHire || class == TargetSummon) {
		return false
	}
	if class == TargetSelf && g.playerGUID != "" && tg != g.playerGUID {
		return false
	}
	return true
}

// render fills a template's {0}..{n} slots.
func render(kind string, args []string) (string, error) {
	tmpl, ok := guardTemplates[kind]
	if !ok {
		return "", &GmRefused{Text: kind}
	}
	want, ok := guardArgCounts[kind]
	if !ok {
		return "", &GmRefused{Text: kind}
	}
	if len(args) != want {
		return "", &GmRefused{Text: kind + " (wrong arg count)"}
	}
	out := tmpl
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\r\n\x00{}") {
			return "", &GmRefused{Text: kind + " (bad arg)"}
		}
		out = strings.ReplaceAll(out, "{"+strconv.Itoa(i)+"}", a)
	}
	if strings.Contains(out, "{") || ClassifyGuard(out) != kind {
		return "", &GmRefused{Text: kind + " (not in the closed set)"}
	}
	return out, nil
}

// selectionKinds are the raw lines the core reads off the selected unit, so
// the guard selects the target first (the old daemon's rule: the core
// applies a selection and a GM line on different threads).
var selectionKinds = map[string]bool{
	"damage": true, "modify_hp": true, "modify_mana": true, "modify_rage": true,
	"modify_energy": true, "aura": true, "unaura": true, "die": true,
	"levelup": true, "learn": true, "cooldown": true,
}

// unitLineKinds are the raw lines the serve's GM-unit boundary classifies:
// they must find their target on the trial_units allowlist.
var unitLineKinds = map[string]bool{
	"damage": true, "modify_hp": true, "modify_mana": true, "modify_rage": true,
	"modify_energy": true, "aura": true, "unaura": true, "die": true,
}

// scope is every guid this trial's GM hands may touch: the player, the hire,
// the extras and this repeat's summons.
func (g *Guard) scope() []string {
	g.mu.Lock()
	player, hire := g.playerGUID, g.hire
	extras := append([]string(nil), g.extras...)
	g.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = normGUIDOf(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	add(player)
	add(hire)
	for _, e := range extras {
		add(e)
	}
	if g.summons != nil {
		for _, v := range g.summons() {
			add(v)
		}
	}
	sort.Strings(out)
	return out
}

// ensureUnits registers the current unit scope with the serve (trial_units)
// when it changed since the last hand. A serve without --allow-gm-units
// answers -32006, which the caller reports: the hand never goes out blind.
func (g *Guard) ensureUnits(ctx context.Context) error {
	guids := g.scope()
	key := strings.Join(guids, ",")
	g.mu.Lock()
	same := key == g.lastUnit
	g.mu.Unlock()
	if same {
		return nil
	}
	if _, err := g.client.TrialUnits(ctx, guids); err != nil {
		return fmt.Errorf("trial_units: %w", err)
	}
	g.mu.Lock()
	g.lastUnit = key
	g.mu.Unlock()
	return nil
}

// selectUnit selects guid and waits up to 4 s for the server's echo in the
// character's target field.
func (g *Guard) selectUnit(ctx context.Context, guid string) error {
	want := normGUIDOf(guid)
	if want == "" {
		return &GmRefused{Text: "select: no guid"}
	}
	if err := g.client.Target(ctx, want); err != nil {
		return fmt.Errorf("target %s: %w", want, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := g.client.StatusOf(ctx)
		if err == nil && normGUIDOf(st.Target) == want {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("selection not confirmed for %s", want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Send renders kind with args at target of class and sends it through the
// single `gm` call site of this package. A line the core reads off the
// selection selects its target first and proves the echo.
func (g *Guard) Send(ctx context.Context, kind string, class GuardTarget, target string, args ...string) error {
	line, err := render(kind, args)
	if err != nil {
		return err
	}
	if !g.allowedTarget(kind, class, target) {
		return &GmRefused{Text: line}
	}
	if fn := g.aborted; fn != nil && fn() != "" {
		return &Disconnected{err: fmt.Errorf("aborted: %s", fn())}
	}
	if !g.dryRun {
		if unitLineKinds[kind] {
			if err := g.ensureUnits(ctx); err != nil {
				return err
			}
		}
		if selectionKinds[kind] {
			if err := g.selectUnit(ctx, target); err != nil {
				return err
			}
		}
	}
	g.record(kind, line, target)
	if g.dryRun {
		return nil
	}
	err = g.client.GM(ctx, line) // the one gm call site in this package
	if IsNoReply(err) {
		// The line ran; this core prints nothing for it. The caller's
		// read-back proves the effect.
		if g.marks != nil {
			g.marks.Add(map[string]any{"kind": "gm_silent", "kind_detail": kind, "target": targetOrNil(target)})
		}
		return nil
	}
	return err
}

// record writes the marks and ledger rows of one GM action.
func (g *Guard) record(kind, line, target string) {
	if g.marks != nil {
		g.marks.Add(map[string]any{"kind": "gm", "line": line, "kind_detail": kind,
			"target": targetOrNil(target)})
	}
	if g.ledger != nil {
		g.ledger.Add(map[string]any{"kind": "gm", "line": line, "target": target})
	}
	if fn := g.onGM; fn != nil {
		fn(kind, line, target)
	}
}

// where reports the class a guid belongs to in this trial's scope, or "".
func (g *Guard) where(guid string) GuardTarget {
	v := normGUIDOf(guid)
	switch {
	case v == "":
		return ""
	case v == g.playerGUID:
		return TargetSelf
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if v == g.hire {
		return TargetHire
	}
	for _, e := range g.extras {
		if v == e {
			return TargetHire
		}
	}
	if g.summons != nil {
		for _, s := range g.summons() {
			if normGUIDOf(s) == v {
				return TargetSummon
			}
		}
	}
	return ""
}

// hand is the shared body of the typed GM hands: the abort check, the unit
// allowlist and the ledger row, then the one RPC.
func (g *Guard) hand(ctx context.Context, kind, guid string, want GuardTarget, call func() error) error {
	if fn := g.aborted; fn != nil && fn() != "" {
		return &Disconnected{err: fmt.Errorf("aborted: %s", fn())}
	}
	class := g.where(guid)
	if class == "" || class != want {
		return &GmRefused{Text: kind + " " + guid + " (outside this trial's scope)"}
	}
	if g.dryRun {
		g.record(kind, kind+" "+guid, guid)
		return nil
	}
	if err := g.ensureUnits(ctx); err != nil {
		return err
	}
	g.record(kind, kind+" "+guid, guid)
	err := call()
	if IsNoReply(err) {
		if g.marks != nil {
			g.marks.Add(map[string]any{"kind": "gm_silent", "kind_detail": kind, "target": guid})
		}
		return nil
	}
	return err
}

// Kill kills one ledgered summon through the serve's gm_kill hand. A guid
// the repeat did not summon is refused before anything is sent.
func (g *Guard) Kill(ctx context.Context, guid string) error {
	return g.hand(ctx, "gm_kill", guid, TargetSummon, func() error {
		return g.client.GMKill(ctx, normGUIDOf(guid))
	})
}

// SetHP sets one in-scope unit's health through gm_set_hp (the core sets
// current AND maximum health, so callers pass the unit's own maximum for a
// refill).
func (g *Guard) SetHP(ctx context.Context, guid string, hp int) error {
	class := g.where(guid)
	if class != TargetSelf && class != TargetHire {
		return &GmRefused{Text: "gm_set_hp " + guid + " (outside this trial's scope)"}
	}
	return g.hand(ctx, "gm_set_hp", guid, class, func() error {
		return g.client.GMSetHP(ctx, normGUIDOf(guid), hp)
	})
}

// RemoveAura strips one aura from an in-scope unit through gm_aura.
func (g *Guard) RemoveAura(ctx context.Context, guid string, spell int) error {
	class := g.where(guid)
	if class != TargetSelf && class != TargetHire {
		return &GmRefused{Text: "gm_aura " + guid + " (outside this trial's scope)"}
	}
	return g.hand(ctx, "gm_aura", guid, class, func() error {
		return g.client.GMAura(ctx, normGUIDOf(guid), spell, true)
	})
}

// SendText classifies raw text against the closed set and sends it only on a
// whole-line match; anything else is refused and never sent.
func (g *Guard) SendText(ctx context.Context, text string, class GuardTarget, target string) error {
	kind := ClassifyGuard(text)
	if kind == "" {
		return &GmRefused{Text: text}
	}
	return g.Send(ctx, kind, class, target, classifyArgs(text, kind)...)
}

// classifyArgs re-extracts the template args from raw text.
func classifyArgs(text, kind string) []string {
	if ClassifyGuard(text) == "" {
		return nil
	}
	for _, c := range guardClassifiers {
		if c.kind != kind {
			continue
		}
		if m := c.re.FindStringSubmatch(text); len(m) > 1 && m[0] == text {
			return m[1:]
		}
	}
	return nil
}

// targetOrNil stores a nil target for ledger rows without one.
func targetOrNil(target string) any {
	if normGUIDOf(target) == "" {
		return nil
	}
	return target
}
