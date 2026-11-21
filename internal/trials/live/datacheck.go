package live

// datacheck.go: `trials forever-data-check`, the live proof of the data the
// trials and kits name. Nothing is guessed: every spell id is asked of the
// server (`.tb learn` on a scratch bot), every item entry through
// `.tb itemscan`, and every mob entry through `.npc add temp` plus a read of
// its level and a kill. The report is forever-data-report.json with the ok,
// unknown and differs rows; the lead fixes the overlays from it.
//
// This file is transport-free: the plan and the report are pure, and a
// DataProbe does the asking, so a fake probe covers the command offline.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/gear"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/spells"
	"github.com/pjerra/forever-agent/internal/trials/trialdefs"
)

// SpellCheck is one spell id (or a named spell's resolved ids) to learn.
type SpellCheck struct {
	ID     int
	Name   string
	Source string
	Known  bool // true when the embedded spell table knows the id
}

// ItemCheck is one item entry to scan.
type ItemCheck struct {
	Entry       int
	Name        string
	Source      string
	FromOverlay bool
}

// MobCheck is one mob entry to summon and read.
type MobCheck struct {
	Entry    int
	Role     string
	LevelMin int
	LevelMax int
}

// DataCheckPlan is everything the trials and kits name.
type DataCheckPlan struct {
	Spells []SpellCheck
	Items  []ItemCheck
	Mobs   []MobCheck
}

// ItemAnswer is an itemscan answer: Answered is false when the server's
// reply held no row for the entry (or refused), which is UNKNOWN and never a
// missing item; Exists is the server's own `exists` flag.
type ItemAnswer struct {
	Answered bool
	Exists   bool
	CanEquip bool
	Note     string
}

// SpawnAnswer is one mob probe: OK when a unit THIS probe spawned appeared
// (its level is Level); Reason says why not; Halt stops the remaining spawns
// (a stranger came near, the lease was lost, a spawn could not be told apart
// from another unit).
type SpawnAnswer struct {
	OK     bool
	Level  int
	Reason string
	Halt   bool
}

// DataProbe asks the live server. Learn answers the module's own word
// (learned, known, unknown, invalid, not-learned) or `refused` / `no-row` when
// the module gave no answer for the spell; ItemScan answers what the reply
// held; Spawn answers whether a unit the probe spawned itself appeared and at
// which level; Kill removes that unit and proves it dead.
type DataProbe interface {
	Learn(ctx context.Context, spell int) (string, error)
	ItemScan(ctx context.Context, entry int) (ItemAnswer, error)
	Spawn(ctx context.Context, entry int) (SpawnAnswer, error)
	Kill(ctx context.Context, entry int) error
}

// BuildDataCheckPlan collects every spell, item and mob entry the embedded
// trials, the gear kits, the spec markers and the Forever overlays name.
func BuildDataCheckPlan(trials []map[string]any) DataCheckPlan {
	plan := DataCheckPlan{}
	spellSeen := map[int]bool{}
	itemSeen := map[int]bool{}
	mobSeen := map[string]bool{}
	addSpell := func(id int, name, source string) {
		if id <= 0 {
			if name != "" {
				plan.Spells = append(plan.Spells, SpellCheck{ID: 0, Name: name, Source: source})
			}
			return
		}
		if spellSeen[id] {
			return
		}
		spellSeen[id] = true
		plan.Spells = append(plan.Spells, SpellCheck{ID: id, Name: name, Source: source,
			Known: spells.HasID(int64(id))})
	}
	addItem := func(entry int, name, source string, overlay bool) {
		if entry <= 0 {
			if name != "" {
				plan.Items = append(plan.Items, ItemCheck{Entry: 0, Name: name, Source: source, FromOverlay: overlay})
			}
			return
		}
		if itemSeen[entry] {
			return
		}
		itemSeen[entry] = true
		plan.Items = append(plan.Items, ItemCheck{Entry: entry, Name: name, Source: source, FromOverlay: overlay})
	}
	for _, trial := range trials {
		id := pyjson.Str(trial["id"])
		player, _ := pyjson.Object(trial["player"])
		if player != nil {
			addSpell(int(intOf(player["stance"])), "", id+"/player.stance")
			if talents, _ := pyjson.Object(player["talents"]); talents != nil {
				for _, v := range listOf(talents["spells"]) {
					addSpell(int(intOf(v)), "", id+"/player.talents")
				}
			}
		}
		sit, _ := pyjson.Object(trial["situation"])
		if sit != nil {
			if debuff, _ := pyjson.Object(sit["debuff"]); debuff != nil {
				addSpell(int(intOf(debuff["spell_id"])), "", id+"/situation.debuff")
			}
		}
		collectNames(trial, id, addSpell)
		for _, key := range []string{"bot", "extra_bots"} {
			list := listOf(trial[key])
			if key == "bot" && trial["bot"] != nil {
				list = []any{trial["bot"]}
			}
			for _, raw := range list {
				bot, _ := pyjson.Object(raw)
				if bot == nil {
					continue
				}
				for _, itemRaw := range listOf(bot["items"]) {
					item, _ := pyjson.Object(itemRaw)
					name := pyjson.Str(item["name"])
					addResolvedItem(&plan, name, id+"/"+key+".items", addItem)
				}
			}
		}
	}
	// The kits: every gear entry (base and Forever overlay) and the spec
	// markers the setup learns.
	for _, entry := range gear.AllEntries() {
		addItem(int(entry), "", "gear kit", false)
	}
	for _, item := range overlayKitItems() {
		addItem(item.entry, item.name, "forever/gear_kits.json", true)
	}
	for name, entry := range overlayItemNames() {
		addItem(entry, name, "forever/items.json", true)
	}
	for _, spell := range overlaySpecMarkers() {
		addSpell(spell, "", "forever/specs.json")
	}
	// The mob table (base plus overlay): each entry is a spawn the trials
	// may ask for.
	for role, rows := range LoadMobs() {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Entry < rows[j].Entry })
		for _, m := range rows {
			key := itoa(m.Entry) + "/" + role
			if m.Entry <= 0 || mobSeen[key] {
				continue
			}
			mobSeen[key] = true
			plan.Mobs = append(plan.Mobs, MobCheck{Entry: m.Entry, Role: role, LevelMin: m.LevelMin, LevelMax: m.LevelMax})
		}
	}
	sort.SliceStable(plan.Spells, func(i, j int) bool {
		if plan.Spells[i].ID != plan.Spells[j].ID {
			return plan.Spells[i].ID < plan.Spells[j].ID
		}
		if plan.Spells[i].Name != plan.Spells[j].Name {
			return plan.Spells[i].Name < plan.Spells[j].Name
		}
		return plan.Spells[i].Source < plan.Spells[j].Source
	})
	sort.SliceStable(plan.Items, func(i, j int) bool {
		if plan.Items[i].Entry != plan.Items[j].Entry {
			return plan.Items[i].Entry < plan.Items[j].Entry
		}
		if plan.Items[i].Name != plan.Items[j].Name {
			return plan.Items[i].Name < plan.Items[j].Name
		}
		return plan.Items[i].Source < plan.Items[j].Source
	})
	sort.SliceStable(plan.Mobs, func(i, j int) bool {
		if plan.Mobs[i].Entry != plan.Mobs[j].Entry {
			return plan.Mobs[i].Entry < plan.Mobs[j].Entry
		}
		return plan.Mobs[i].Role < plan.Mobs[j].Role
	})
	return plan
}

// listOf is Python's `value or []` for a list.
func listOf(v any) []any {
	if list, ok := pyjson.List(v); ok {
		return list
	}
	return nil
}

type overlayItem struct {
	entry int
	name  string
}

func overlayKitItems() []overlayItem {
	doc, err := pyjson.Unmarshal(data.ReadOverlay("gear_kits.json"))
	if err != nil {
		return nil
	}
	classes, _ := pyjson.Object(doc)
	var out []overlayItem
	for _, cls := range classes {
		kits, _ := pyjson.Object(cls)
		for _, kit := range kits {
			levels, _ := pyjson.Object(kit)
			for _, items := range levels {
				for _, raw := range listOf(items) {
					m, _ := pyjson.Object(raw)
					out = append(out, overlayItem{entry: int(intOf(m["entry"])), name: pyjson.Str(m["name"])})
				}
			}
		}
	}
	return out
}

func overlayItemNames() map[string]int {
	out := map[string]int{}
	doc, err := pyjson.Unmarshal(data.ReadOverlay("items.json"))
	if err != nil {
		return out
	}
	m, _ := pyjson.Object(doc)
	for name, v := range m {
		out[strings.ToLower(name)] = int(intOf(v))
	}
	return out
}

func overlaySpecMarkers() []int {
	doc, err := pyjson.Unmarshal(data.ReadOverlay("specs.json"))
	if err != nil {
		return nil
	}
	classes, _ := pyjson.Object(doc)
	var out []int
	for _, specs := range classes {
		m, _ := pyjson.Object(specs)
		for _, ids := range m {
			for _, v := range listOf(ids) {
				out = append(out, int(intOf(v)))
			}
		}
	}
	return out
}

// addResolvedItem resolves a trial item name through the Forever overlay
// (`items.json`); an unresolved name is still reported (unknown), never
// dropped.
func addResolvedItem(plan *DataCheckPlan, name, source string, add func(int, string, string, bool)) {
	entry := overlayItemNames()[strings.ToLower(name)]
	add(entry, name, source, entry > 0)
}

// collectNames walks the rule/check trees for numeric spell ids and named
// spells ("Counterspell", "Kick", "Polymorph", ...).
func collectNames(trial map[string]any, id string, add func(int, string, string)) {
	// collect walks a value: numeric ids under the spell-bearing keys, string
	// names under a `spell` key.
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch t := v.(type) {
		case map[string]any:
			for k, sub := range t {
				p := path + "/" + k
				switch k {
				case "aura", "has_aura", "dst_has_aura", "src_has_aura", "spells":
					for _, item := range listOf(sub) {
						add(int(intOf(item)), "", id+p)
					}
				case "spell":
					if s, ok := sub.(string); ok && s != "" {
						ids := spells.IDsFor(s)
						if len(ids) == 0 {
							add(0, s, id+p)
							continue
						}
						var keys []int64
						for sid := range ids {
							keys = append(keys, sid)
						}
						sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
						for _, sid := range keys {
							add(int(sid), s, id+p)
						}
						continue
					}
					add(int(intOf(sub)), "", id+p)
				}
				walk(sub, p)
			}
		case []any:
			for i, sub := range t {
				walk(sub, path+"/"+itoa(i))
			}
		}
	}
	walk(trial["rules"], id+"/rules")
	walk(trial["opportunity"], id+"/opportunity")
	walk(trial["setup_checks"], id+"/setup_checks")
}

// ---- the report ---------------------------------------------------------

// DataCheckRow is one asked entry.
type DataCheckRow struct {
	Kind     string         `json:"kind"`
	ID       any            `json:"id"`
	Name     string         `json:"name,omitempty"`
	Source   string         `json:"source,omitempty"`
	Role     string         `json:"role,omitempty"`
	Asked    string         `json:"asked"`
	Result   string         `json:"result"`
	Evidence string         `json:"evidence,omitempty"`
	Expected map[string]any `json:"expected,omitempty"`
	Got      map[string]any `json:"got,omitempty"`
}

// DataCheckReport is forever-data-report.json: ok, unknown and differs, each
// a list of rows, plus the counts.
type DataCheckReport struct {
	Server    string         `json:"server"`
	CheckedAt string         `json:"checked_at"`
	Ok        []DataCheckRow `json:"ok"`
	Unknown   []DataCheckRow `json:"unknown"`
	Differs   []DataCheckRow `json:"differs"`
	Counts    map[string]int `json:"counts"`
	// Leftover is every spawned unit teardown could not prove dead or gone
	// (a failed data check never claims a clean slot).
	Leftover []string `json:"leftover,omitempty"`
}

// RunDataCheck asks every entry of the plan through probe. It stops on a
// transport error (an unanswerable server is not a data verdict) and never
// invents a row: a nil plan entry is skipped, not reported ok.
func RunDataCheck(ctx context.Context, probe DataProbe, plan DataCheckPlan, server, checkedAt string) (*DataCheckReport, error) {
	report := &DataCheckReport{Server: server, CheckedAt: checkedAt,
		Ok: []DataCheckRow{}, Unknown: []DataCheckRow{}, Differs: []DataCheckRow{},
		Counts: map[string]int{}}
	add := func(row DataCheckRow) {
		switch row.Result {
		case "ok":
			report.Ok = append(report.Ok, row)
		case "differs":
			report.Differs = append(report.Differs, row)
		default:
			row.Result = "unknown"
			report.Unknown = append(report.Unknown, row)
		}
	}
	stopped := ""
	for _, s := range plan.Spells {
		if stopped == "" && ctx.Err() != nil {
			stopped = "interrupted"
		}
		if stopped != "" {
			add(DataCheckRow{Kind: "spell", ID: s.ID, Name: s.Name, Source: s.Source,
				Asked: "not asked", Result: "unknown", Evidence: "not asked: " + stopped})
			continue
		}
		if s.ID <= 0 {
			add(DataCheckRow{Kind: "spell", ID: s.Name, Name: s.Name, Source: s.Source,
				Asked: "spell name could not be resolved", Result: "unknown",
				Evidence: "no id in the embedded spell table"})
			continue
		}
		asked := fmt.Sprintf(".tb learn <scratch> %d", s.ID)
		res, err := probe.Learn(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("learn %d: %w", s.ID, err)
		}
		row := DataCheckRow{Kind: "spell", ID: s.ID, Name: s.Name, Source: s.Source, Asked: asked}
		switch res {
		case "learned", "known":
			row.Result, row.Evidence = "ok", res
		case "unknown":
			// The module's own word for a spell id the server does not have:
			// the only answer that can contradict the embedded table.
			row.Result, row.Evidence = "unknown", "the server answered `unknown` for the spell"
			if s.Known {
				row.Result = "differs"
				row.Expected = map[string]any{"id": s.ID, "in": "embedded spells.json"}
				row.Got = map[string]any{"learn": res}
			}
		default:
			// invalid, not-learned, refused, no-row: the server did not say
			// the spell is missing, so nothing is claimed about the table.
			row.Result, row.Evidence = "unknown", "no data answer: "+res
		}
		add(row)
	}
	for _, it := range plan.Items {
		if stopped == "" && ctx.Err() != nil {
			stopped = "interrupted"
		}
		if stopped != "" {
			add(DataCheckRow{Kind: "item", ID: it.Entry, Name: it.Name, Source: it.Source,
				Asked: "not asked", Result: "unknown", Evidence: "not asked: " + stopped})
			continue
		}
		if it.Entry <= 0 {
			add(DataCheckRow{Kind: "item", ID: it.Name, Name: it.Name, Source: it.Source,
				Asked: "item name has no entry in forever/items.json", Result: "unknown",
				Evidence: "the name could not be resolved to an item entry"})
			continue
		}
		asked := fmt.Sprintf(".tb itemscan <scratch> %d", it.Entry)
		ans, err := probe.ItemScan(ctx, it.Entry)
		if err != nil {
			return nil, fmt.Errorf("itemscan %d: %w", it.Entry, err)
		}
		row := DataCheckRow{Kind: "item", ID: it.Entry, Name: it.Name, Source: it.Source, Asked: asked}
		switch {
		case !ans.Answered:
			row.Result = "unknown"
			row.Evidence = "no itemscan answer for the entry"
			if ans.Note != "" {
				row.Evidence += ": " + ans.Note
			}
		case ans.Exists:
			row.Result = "ok"
			row.Got = map[string]any{"exists": true, "canequip": ans.CanEquip}
		case it.FromOverlay:
			row.Result = "differs"
			row.Expected = map[string]any{"exists": true, "source": it.Source}
			row.Got = map[string]any{"exists": false}
		default:
			row.Result = "unknown"
			row.Evidence = "item not found"
		}
		add(row)
	}
	for _, m := range plan.Mobs {
		if stopped == "" && ctx.Err() != nil {
			stopped = "interrupted"
		}
		asked := fmt.Sprintf(".npc add temp %d + units + kill", m.Entry)
		if stopped != "" {
			add(DataCheckRow{Kind: "mob", ID: m.Entry, Role: m.Role, Asked: "not asked",
				Result: "unknown", Evidence: "not spawned: " + stopped})
			continue
		}
		ans, err := probe.Spawn(ctx, m.Entry)
		if err != nil {
			return nil, fmt.Errorf("spawn %d: %w", m.Entry, err)
		}
		row := DataCheckRow{Kind: "mob", ID: m.Entry, Role: m.Role, Asked: asked}
		switch {
		case !ans.OK:
			row.Result = "unknown"
			row.Evidence = "no unit of the entry was proved this probe's own"
			if ans.Reason != "" {
				row.Evidence += ": " + ans.Reason
			}
		case m.LevelMax > 0 && (ans.Level < m.LevelMin || ans.Level > m.LevelMax):
			row.Result = "differs"
			row.Expected = map[string]any{"level_min": m.LevelMin, "level_max": m.LevelMax}
			row.Got = map[string]any{"level": ans.Level}
		default:
			row.Result = "ok"
			row.Got = map[string]any{"level": ans.Level}
		}
		if kerr := probe.Kill(ctx, m.Entry); kerr != nil && row.Result == "ok" {
			row.Result = "unknown"
			row.Evidence = "the summoned entry could not be killed: " + trimLen(kerr.Error(), 120)
		}
		add(row)
		if ans.Halt && stopped == "" {
			stopped = ans.Reason
			if stopped == "" {
				stopped = "the probe halted"
			}
		}
	}
	report.Counts["ok"] = len(report.Ok)
	report.Counts["unknown"] = len(report.Unknown)
	report.Counts["differs"] = len(report.Differs)
	report.Counts["asked"] = len(report.Ok) + len(report.Unknown) + len(report.Differs)
	return report, nil
}

// WriteDataCheck writes the report as Python-style JSON (sorted keys,
// indent 2).
func WriteDataCheck(path string, report *DataCheckReport) error {
	doc := map[string]any{
		"server": report.Server, "checked_at": report.CheckedAt,
		"ok": rowsAny(report.Ok), "unknown": rowsAny(report.Unknown),
		"differs": rowsAny(report.Differs), "counts": map[string]any{
			"ok": report.Counts["ok"], "unknown": report.Counts["unknown"],
			"differs": report.Counts["differs"], "asked": report.Counts["asked"]},
	}
	if len(report.Leftover) > 0 {
		left := make([]any, 0, len(report.Leftover))
		for _, g := range report.Leftover {
			left = append(left, g)
		}
		doc["leftover"] = left
	}
	return dumpDoc(path, doc)
}

func rowsAny(rows []DataCheckRow) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		m := map[string]any{"kind": r.Kind, "id": r.ID, "asked": r.Asked, "result": r.Result}
		if r.Name != "" {
			m["name"] = r.Name
		}
		if r.Source != "" {
			m["source"] = r.Source
		}
		if r.Role != "" {
			m["role"] = r.Role
		}
		if r.Evidence != "" {
			m["evidence"] = r.Evidence
		}
		if r.Expected != nil {
			m["expected"] = r.Expected
		}
		if r.Got != nil {
			m["got"] = r.Got
		}
		out = append(out, m)
	}
	return out
}

// EmbeddedTrials loads every embedded base trial (the fixture folder is not
// part of the live data the trials name).
func EmbeddedTrials() []map[string]any {
	var out []map[string]any
	for _, path := range trialdefs.Paths() {
		if strings.HasPrefix(path, "_fixtures/") {
			continue
		}
		raw, err := trialdefs.Read(path)
		if err != nil {
			continue
		}
		doc, err := pyjson.Unmarshal(raw)
		if err != nil {
			continue
		}
		if obj, ok := pyjson.Object(doc); ok {
			out = append(out, obj)
		}
	}
	return out
}
