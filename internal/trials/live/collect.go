package live

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/events"
)

// sampleUnitsYards is the units radius of one sampler tick (Python
// SAMPLE_UNITS_YARDS).
const sampleUnitsYards = 60

// disconnectSubs ends the repeat: the Python DISCONNECT pattern.
var disconnectSubs = []string{"disconnect", "logged out", "session lost", "session ended", "session closed", "left the world"}

// Guids is the setup.json guid table the collector samples: player, bot,
// pet, extras (up to 3) and mobs, plus marked for the normaliser.
type Guids struct {
	Player string
	Bot    string
	Pet    string
	Extras []string
	Mobs   []string
	Marked string
}

// GuidsOf reads the guids table of a setup.json doc.
func GuidsOf(setup map[string]any) Guids {
	var g Guids
	obj, _ := setup["guids"].(map[string]any)
	if obj == nil {
		return g
	}
	g.Player = events.NormGUID(obj["player"])
	g.Bot = events.NormGUID(obj["bot"])
	g.Pet = events.NormGUID(obj["pet"])
	g.Marked = events.NormGUID(obj["marked"])
	if list, ok := obj["extras"].([]any); ok {
		for _, e := range list {
			if s := events.NormGUID(e); s != "" {
				g.Extras = append(g.Extras, s)
			}
		}
	}
	if len(g.Extras) > 3 {
		g.Extras = g.Extras[:3]
	}
	if list, ok := obj["mobs"].([]any); ok {
		for _, e := range list {
			if s := events.NormGUID(e); s != "" {
				g.Mobs = append(g.Mobs, s)
			}
		}
	}
	return g
}

// Collector is one repeat's collectors: the event pump (events ≤ 900 ms
// wait, dedupe by seq, an events_dropped mark on ring drops) and the 1 Hz
// sampler with the exact keys of collect.py _rows. Together with the pump's
// ~1 call/s the sampler's status+units ticks stay at ≤ 4 calls/s.
type Collector struct {
	client  *Client
	repDir  string
	marks   *Marks
	guids   *Guids
	ledger  *Ledger
	rawPath string
	samples string

	mu        sync.Mutex
	seq       map[any]bool
	raw       []map[string]any
	rawRows   int
	samplesN  int
	lost      string
	onLost    func(reason string)
	tickHooks []tickHook

	stop context.CancelFunc
	wg   sync.WaitGroup

	last     map[string]any
	hookNext int
}

// NewCollector returns the collector for repDir; guids is the setup.json
// guid table (GuidsOf), ledger may be nil. The table is held by pointer: the
// situation adds the guids of the mobs it summons mid-repeat, and the
// sampler must see them.
func NewCollector(client *Client, repDir string, marks *Marks, guids *Guids, ledger *Ledger) *Collector {
	return &Collector{client: client, repDir: repDir, marks: marks, guids: guids,
		ledger: ledger, rawPath: filepath.Join(repDir, "raw-ta.jsonl"),
		samples: filepath.Join(repDir, "samples.jsonl"), seq: map[any]bool{}}
}

// SetGuids points the collector at the repeat's guid table.
func (c *Collector) SetGuids(g *Guids) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.guids = g
}

// OnLost sets the hook the pump and sampler call once with the reason.
func (c *Collector) OnLost(fn func(reason string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onLost = fn
}

// tickHook is one sampler hook with its handle: function values cannot be
// compared, so removal matches the handle.
type tickHook struct {
	id int
	fn func(sample map[string]any)
}

// TickHook adds a sampler hook (situation governors observe through it)
// and returns its handle for RemoveTickHook.
func (c *Collector) TickHook(fn func(sample map[string]any)) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hookNext++
	c.tickHooks = append(c.tickHooks, tickHook{id: c.hookNext, fn: fn})
	return c.hookNext
}

// RemoveTickHook drops a hook added by TickHook; an unknown handle is a
// no-op.
func (c *Collector) RemoveTickHook(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.tickHooks {
		if c.tickHooks[i].id == id {
			c.tickHooks = append(c.tickHooks[:i], c.tickHooks[i+1:]...)
			return
		}
	}
}

// LastSample is the sampler's latest compact sample (nil before the first).
func (c *Collector) LastSample() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// Lost is the disconnect reason, "" while the session lives.
func (c *Collector) Lost() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lost
}

// RawRows and Samples count what the collector wrote.
func (c *Collector) RawRows() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rawRows
}

// SamplesN counts the sample rows written.
func (c *Collector) SamplesN() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.samplesN
}

func (c *Collector) markLost(reason string) {
	c.mu.Lock()
	if c.lost != "" {
		c.mu.Unlock()
		return
	}
	c.lost = reason
	hook := c.onLost
	stop := c.stop
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	if hook != nil {
		hook(reason)
	}
}

// Start launches the pump and the sampler; Stop waits for both (bounded, so
// a wedged agent cannot hang the run).
func (c *Collector) Start(ctx context.Context) {
	// The files exist even when the serve sends nothing: the normaliser
	// reads raw-ta.jsonl and samples.jsonl, missing or not.
	_ = os.MkdirAll(c.repDir, 0o755)
	for _, path := range []string{c.rawPath, c.samples} {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_ = f.Close()
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.stop = cancel
	c.mu.Unlock()
	c.wg.Add(2)
	go func() { defer c.wg.Done(); c.pump(ctx) }()
	go func() { defer c.wg.Done(); c.sample(ctx) }()
}

// Stop ends both loops, waiting at most 40 s.
func (c *Collector) Stop() {
	c.mu.Lock()
	stop := c.stop
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
	}
}

// pump drains events pages, dedupes by seq, and appends raw-ta.jsonl.
func (c *Collector) pump(ctx context.Context) {
	var since uint64
	for {
		if ctx.Err() != nil {
			return
		}
		page, err := c.client.Events(ctx, since, 1000, 900)
		if err != nil {
			if IsDisconnected(err) {
				c.markLost(trimReason(err.Error()))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		since = page.Next
		if page.Dropped {
			c.marks.Add(map[string]any{"kind": "events_dropped", "dropped": true,
				"oldest": page.Oldest, "next": page.Next})
		}
		for _, row := range page.Events {
			seq := row["seq"]
			c.mu.Lock()
			if c.seq[seqKey(seq)] {
				c.mu.Unlock()
				continue
			}
			c.seq[seqKey(seq)] = true
			c.raw = append(c.raw, row)
			c.rawRows++
			c.mu.Unlock()
			appendJSONL(c.rawPath, row)
			if kind, _ := row["kind"].(string); kind == "session" {
				if text := eventText(row); isDisconnect(text) {
					c.markLost("disconnected: " + trimLen(text, 120))
					return
				}
			}
		}
		if len(page.Events) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// sample ticks at 1 Hz: status + units(60) into samples.jsonl.
func (c *Collector) sample(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		begin := time.Now()
		st, err := c.client.StatusOf(ctx)
		if err != nil {
			if IsDisconnected(err) {
				c.markLost(trimReason(err.Error()))
				return
			}
			continue
		}
		if st.State != "in_world" {
			c.markLost("disconnected: agent state " + orUnknown(st.State))
			return
		}
		units, err := c.client.Units(ctx, sampleUnitsYards)
		if err != nil {
			if IsDisconnected(err) {
				c.markLost(trimReason(err.Error()))
				return
			}
			continue
		}
		rows, sample := c.rows(st, units, ctx)
		for _, row := range rows {
			appendJSONL(c.samples, row)
		}
		c.mu.Lock()
		c.samplesN += len(rows)
		c.last = sample
		hooks := append([]tickHook(nil), c.tickHooks...)
		c.mu.Unlock()
		for _, hook := range hooks {
			hook.fn(sample)
		}
		_ = begin
	}
}

// rows builds one tick's sample rows plus the governor's compact sample.
func (c *Collector) rows(st Status, units []Unit, ctx context.Context) ([]map[string]any, map[string]any) {
	wall := c.marks.NowISO()
	t := c.marks.Rel(wall)
	mono := int64(0)
	if m, ok := st.Raw["mono_ms"]; ok {
		mono = intOf(m)
	}
	hasMono := false
	if _, ok := st.Raw["mono_ms"]; ok && st.Raw["mono_ms"] != nil {
		hasMono = true
	}
	out := []map[string]any{}
	byGUID := map[string]Unit{}
	for _, u := range units {
		byGUID[strings.ToLower(u.GUID)] = u
	}
	row := func(label, guid string, unit map[string]any, inCombat any, target string, x, y, z any) {
		r := map[string]any{
			"t": t, "wall": wall, "unit": label, "guid": guid,
			"hp": unit["health"], "max_hp": unit["max_health"],
			"power": unit["power"], "max_power": unit["max_power"],
			"auras":     sortedSpellIDs(unit["auras"]),
			"casting":   unit["casting"],
			"in_combat": inCombat,
			"target":    target,
			"x":         x, "y": y, "z": z,
		}
		if hasMono {
			r["mono_ms"] = mono
		}
		out = append(out, r)
	}
	ownGUID := strings.ToLower(st.GUID)
	own := map[string]any{}
	if u, ok := byGUID[ownGUID]; ok {
		own = u.Raw
	} else if ownGUID != "" {
		// units never lists the player: ask the unit rpc, {} when gone.
		if u, err := c.client.Unit(ctx, st.GUID); err == nil {
			own = u.Raw
		}
	}
	playerUnit := map[string]any{
		"health": st.Health, "max_health": st.MaxHealth,
		"power": st.Power, "max_power": st.MaxPower,
		"auras": ownAuras(own), "casting": own["casting"],
	}
	ownCombat := ownInCombat(st, own, units)
	row("player", ownGUID, playerUnit, ownCombat, strings.ToLower(st.Target), st.Raw["x"], st.Raw["y"], st.Raw["z"])
	unitRow := func(key, label, guid string) (Unit, bool) {
		if guid == "" {
			return Unit{}, false
		}
		u, ok := byGUID[strings.ToLower(guid)]
		if !ok {
			return Unit{}, false
		}
		m := map[string]any{
			"health": u.Health, "max_health": u.MaxHealth,
			"power": u.Power, "max_power": u.MaxPower,
			"auras": u.Raw["auras"], "casting": u.Raw["casting"],
		}
		_ = key
		row(label, strings.ToLower(guid), m, u.InCombat, strings.ToLower(u.Target), u.Raw["x"], u.Raw["y"], u.Raw["z"])
		return u, true
	}
	guids := Guids{}
	if c.guids != nil {
		guids = *c.guids
	}
	botUnit, botOK := unitRow("bot", "bot", guids.Bot)
	_, _ = unitRow("pet", "pet", guids.Pet)
	extras := map[int]Unit{}
	for i, g := range guids.Extras {
		if u, ok := unitRow("extra", "extra"+itoa(i+1), g); ok {
			extras[i+1] = u
		}
	}
	for _, g := range guids.Mobs {
		unitRow("mob", "mob:"+strings.ToLower(g), g)
	}
	sample := map[string]any{
		"player": compactSample(st.Health, st.MaxHealth, ownAuras(own), ownCombat, st.Dead),
		"bot":    nil,
	}
	if botOK {
		sample["bot"] = compactSample(botUnit.Health, botUnit.MaxHealth, botUnit.Raw["auras"], botUnit.InCombat, botUnit.Dead)
	}
	for i, g := range guids.Extras {
		_ = g
		if u, ok := extras[i+1]; ok {
			sample["extra"+itoa(i+1)] = compactSample(u.Health, u.MaxHealth, u.Raw["auras"], u.InCombat, u.Dead)
		}
	}
	return out, sample
}

func compactSample(hp, maxHP uint32, auras any, inCombat, dead bool) map[string]any {
	return map[string]any{"hp": hp, "max_hp": maxHP, "pct": pct(hp, maxHP),
		"auras": sortedSpellIDs(auras), "in_combat": inCombat, "dead": dead}
}

// ownAuras is the player's aura list from its own unit row.
func ownAuras(own map[string]any) any {
	if own == nil {
		return []any{}
	}
	return own["auras"]
}

// ownInCombat is the player's combat flag: status first when it carries
// one, else the own unit row, else a targeted hostile in combat.
func ownInCombat(st Status, own map[string]any, units []Unit) bool {
	if v, ok := st.Raw["in_combat"]; ok {
		return boolOf(v)
	}
	if v, ok := own["in_combat"]; ok && own != nil {
		return boolOf(v)
	}
	tg := strings.ToLower(st.Target)
	if tg == "" {
		return false
	}
	for _, u := range units {
		if strings.ToLower(u.GUID) == tg && !u.Dead {
			if h, _ := u.Raw["hostile"].(bool); h && u.InCombat {
				return true
			}
		}
	}
	return false
}

// pct is round(100*hp/max, 1), 0 on bad input.
func pct(hp, maxHP uint32) float64 {
	if maxHP == 0 {
		return 0
	}
	return round2(100*float64(hp)/float64(maxHP)*10) / 10
}

// EngagedAt returns the first combat row from src at dst (a cast start or
// go, a melee swing, spell damage or a periodic tick): the proof a passive
// target that never turns InCombat was engaged. A row of the dummy casting
// at the bot is not this proof, so source and target are both matched.
func (c *Collector) EngagedAt(src, dst string) map[string]any {
	src, dst = strings.ToLower(src), strings.ToLower(dst)
	if src == "" || dst == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, row := range c.raw {
		switch kind, _ := row["kind"].(string); kind {
		case "spell_start", "spell_go", "melee", "spell_damage", "periodic":
		default:
			continue
		}
		if rowGUID(row, "source") == src && rowGUID(row, "target") == dst {
			return row
		}
	}
	return nil
}

// rowGUID is a row's normalised source/target guid (flat or nested).
func rowGUID(row map[string]any, side string) string {
	if v := row[side+"_guid"]; v != nil {
		if s, ok := v.(string); ok && s != "" {
			return strings.ToLower(s)
		}
	}
	if flat, ok := row[side].(map[string]any); ok {
		if g, ok := flat["guid"].(string); ok {
			return strings.ToLower(g)
		}
	}
	return ""
}

// sortedSpellIDs sorts a unit's aura list to spell ids, as the sampler's
// `auras` key does.
func sortedSpellIDs(auras any) []any {
	var ids []int64
	switch list := auras.(type) {
	case []any:
		for _, a := range list {
			if m, ok := a.(map[string]any); ok {
				if id, ok := numOf(m["spell_id"]); ok {
					ids = append(ids, int64(id))
				}
			} else if id, ok := numOf(a); ok {
				ids = append(ids, int64(id))
			}
		}
	default:
		return []any{}
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

func seqKey(seq any) any {
	switch t := seq.(type) {
	case string, float64, bool, nil:
		return t
	default:
		return jsonKey(seq)
	}
}

func jsonKey(v any) string {
	b, _ := json.Marshal(v)
	return "json:" + string(b)
}

func eventText(row map[string]any) string {
	if s, ok := row["text"].(string); ok {
		return s
	}
	if d, ok := row["data"].(map[string]any); ok {
		if s, ok := d["text"].(string); ok {
			return s
		}
	}
	return ""
}

func isDisconnect(text string) bool {
	s := strings.ToLower(text)
	for _, sub := range disconnectSubs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func trimReason(s string) string { return trimLen(s, 120) }

func trimLen(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func appendJSONL(path string, row any) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(row)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}
