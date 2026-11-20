package live

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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

// ClassifyGuard names the template kind of raw text, or "" when no template
// matches it whole.
func ClassifyGuard(text string) string {
	for _, c := range guardClassifiers {
		m := c.re.FindStringSubmatch(text)
		if len(m) == 0 {
			continue
		}
		if m[0] == strings.TrimSpace(text) {
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
}

// GuardOptions carries the ledger, the marks file (for the gm rows), the
// player's guid (the "self" target), and the dry-run/abort switches.
type GuardOptions struct {
	Ledger     *Ledger
	Marks      *Marks
	PlayerGUID string
	DryRun     bool
	Abort      func() string
	OnGM       func(kind, text, target string)
}

// NewGuard returns the runner's GM guard over client.
func NewGuard(client *Client, opts GuardOptions) *Guard {
	return &Guard{client: client, ledger: opts.Ledger, marks: opts.Marks,
		playerGUID: normGUIDOf(opts.PlayerGUID), dryRun: opts.DryRun,
		aborted: opts.Abort, onGM: opts.OnGM}
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
	out := tmpl
	for i, a := range args {
		out = strings.ReplaceAll(out, "{"+strconv.Itoa(i)+"}", a)
	}
	if strings.Contains(out, "{") {
		return "", &GmRefused{Text: kind + " (missing args)"}
	}
	return out, nil
}

// Send renders kind with args at target of class and sends it through the
// single `gm` call site of this package.
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
	if g.dryRun {
		return nil
	}
	return g.client.GM(ctx, line) // the one gm call site in this package
}

// SendText classifies raw text against the closed set and sends it only on a
// whole-line match; anything else is refused and never sent.
func (g *Guard) SendText(ctx context.Context, text string, class GuardTarget, target string) error {
	kind := ClassifyGuard(strings.TrimSpace(text))
	if kind == "" {
		return &GmRefused{Text: text}
	}
	return g.Send(ctx, kind, class, target, classifyArgs(text, kind)...)
}

// classifyArgs re-extracts the template args from raw text.
func classifyArgs(text, kind string) []string {
	for _, c := range guardClassifiers {
		if c.kind != kind {
			continue
		}
		if m := c.re.FindStringSubmatch(strings.TrimSpace(text)); len(m) > 1 && m[0] == strings.TrimSpace(text) {
			return m[1:]
		}
	}
	return nil
}

func targetOrNil(target string) any {
	if normGUIDOf(target) == "" {
		return nil
	}
	return target
}
