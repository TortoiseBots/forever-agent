package playdata

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// LoadRep builds the rules' record from one repeat directory: the trial's bot block (class, spec,
// role, level), the setup's bot name and its proven `bot_items` read-back, the ledger's equip and
// `bot_talents` rows, the samples' bot combat flags, and the events' casts. The files are the
// runner's own: a missing or corrupt one leaves the matching Known flag false, so a rule reports
// the missing data instead of judging it.
func LoadRep(repDir string, trial map[string]any, tables Tables) Record {
	rec := Record{Spec: "", Casts: map[string]int{}}
	bot, _ := pyjson.Object(trial["bot"])
	rec.Class = firstString(bot["class"], trial["class"])
	rec.Spec = firstString(bot["spec"], trial["spec"])
	rec.Role = firstString(bot["role"], trial["role"])
	rec.Level = intOf(pyjson.NumOrZero(bot["level"]))
	rec.Bot = firstString(bot["name"])

	setup := map[string]any{}
	if doc, err := pyjson.UnmarshalFile(filepath.Join(repDir, "setup.json")); err == nil {
		setup, _ = pyjson.Object(doc)
	}
	if name := firstString(setup["bot_name"]); name != "" {
		rec.Bot = name
	}
	// The setup's bot_items read-back is the final equipment state: it is taken after the last
	// equip, so it supersedes the ledger's equip rows (a replaced piece is not worn twice). A later
	// read-back replaces an earlier one.
	var readback []int64
	if checks, ok := pyjson.List(setup["checks"]); ok {
		for _, checkVal := range checks {
			check, ok := pyjson.Object(checkVal)
			if !ok || pyjson.Str(check["id"]) != "bot_items" {
				continue
			}
			if !pyjson.Truthy(check["ok"]) {
				continue
			}
			ids, ok := pyjson.List(check["value"])
			if !ok {
				continue
			}
			var proved []int64
			for _, idVal := range ids {
				if id, ok := pyjson.Int64(idVal); ok && id > 0 {
					proved = append(proved, id)
				}
			}
			if len(proved) > 0 {
				readback = proved
			}
		}
	}
	ledger, ledgerOK := readLedger(filepath.Join(repDir, "ledger.jsonl"))
	var equips []int64
	talents := map[int64]bool{}
	if ledgerOK {
		for _, row := range ledger {
			switch pyjson.Str(row["kind"]) {
			case "equip":
				if rec.Bot != "" && !strings.EqualFold(pyjson.Str(row["bot"]), rec.Bot) {
					continue
				}
				if !equippedReply(pyjson.Str(row["reply"])) {
					continue
				}
				if id, ok := pyjson.Int64(row["item"]); ok && id > 0 {
					equips = append(equips, id)
				}
			case "bot_talents":
				if rec.Bot != "" && !strings.EqualFold(pyjson.Str(row["bot"]), rec.Bot) {
					continue
				}
				for _, id := range learnedIDs(pyjson.Str(row["reply"])) {
					talents[id] = true
				}
				if len(talents) > 0 {
					rec.TalentsKnown = true
				}
				// A read-back that declares itself the full learned set is complete, even
				// when it lists nothing. The runner's marker-kit read makes no such claim.
				if pyjson.Truthy(row["complete"]) {
					rec.TalentsComplete = true
					rec.TalentsKnown = true
				}
			}
		}
		rec.Talents = sortedIDs(talents)
	}
	rec.Equipped = equipmentSnapshot(readback, equips, tables)
	rec.EquippedKnown = len(rec.Equipped) > 0

	// The casts are the normalised events' `cast` rows of this bot (one per cast), by spell name.
	if rows, err := events.ReadJSONL(filepath.Join(repDir, "events.jsonl")); err == nil {
		for _, row := range rows {
			if pyjson.Str(row["kind"]) != "cast" {
				continue
			}
			if rec.Bot == "" || !strings.EqualFold(pyjson.Str(row["src"]), rec.Bot) {
				continue
			}
			name := firstString(row["spell"])
			if name != "" {
				rec.Casts[name]++
			}
		}
	}
	// Active time is the bot's own combat time, never the repeat's scored time: setup, waiting
	// and travel are not a fight.
	rec.Combat, rec.ActiveKnown = combatWindows(repDir)
	for _, w := range rec.Combat {
		rec.ActiveS += w.To - w.From
	}
	return rec
}

// equippedReply reports an equip row whose module answer was `equipped`. The runner writes
// "<result> <why>", so a refusal that mentions the word ("cannot be equipped") is not one.
func equippedReply(reply string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(reply)), "equipped")
}

// equipmentSnapshot is the repeat's final equipment. The setup's bot_items read-back wins when it
// proved one: it is the state after the last equip. Without it the ledger's successful equip rows
// are replayed in order, each piece replacing the one before it in the same slot (rings and
// trinkets keep two), so history never counts a replaced piece as worn. A piece whose slot neither
// the tables nor the bench's kits name cannot replace anything; it is kept once so the gear rule
// reports it as unproven.
func equipmentSnapshot(readback, equips []int64, tables Tables) []Item {
	if len(readback) > 0 {
		items := make([]Item, 0, len(readback))
		for _, id := range readback {
			items = append(items, Item{Entry: id})
		}
		sortItems(items)
		return items
	}
	bySlot := map[string][]Item{}
	var unplaced []Item
	unplacedSeen := map[int64]bool{}
	for _, id := range equips {
		slot := itemSlot(id, tables)
		if slot == "" {
			if !unplacedSeen[id] {
				unplacedSeen[id] = true
				unplaced = append(unplaced, Item{Entry: id})
			}
			continue
		}
		key := strings.ToLower(strings.TrimSpace(slot))
		pieces := bySlot[key]
		seen := false
		for _, piece := range pieces {
			if piece.Entry == id {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		pieces = append(pieces, Item{Entry: id, Slot: slot})
		if hold := slotPieces(key); len(pieces) > hold {
			pieces = pieces[len(pieces)-hold:]
		}
		bySlot[key] = pieces
	}
	var items []Item
	for _, key := range sortedKeys(bySlot) {
		items = append(items, bySlot[key]...)
	}
	items = append(items, unplaced...)
	sortItems(items)
	return items
}

// slotPieces is how many pieces one slot name holds: the player tables group both rings under
// "Rings" and both trinkets under "Trinkets"; every other slot holds one.
func slotPieces(slot string) int {
	switch slot {
	case "rings", "trinkets":
		return 2
	}
	return 1
}

// itemSlot names the slot an item is worn in: the player tables' one slot, else the bench's gear
// kits. "" when neither names it.
func itemSlot(id int64, tables Tables) string {
	if tables != nil {
		if info, ok := tables.Item(id); ok && len(info.Slots) == 1 {
			return info.Slots[0]
		}
	}
	if _, slot := kitSlot(id); slot != "" {
		return slot
	}
	return ""
}

// sortItems orders equipment by item id (then slot), so a record's list is stable whatever map
// order found the pieces.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Entry != items[j].Entry {
			return items[i].Entry < items[j].Entry
		}
		return items[i].Slot < items[j].Slot
	})
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// combatWindows reads samples.jsonl's bot rows as the stretches where the bot's own in_combat flag
// was set, joining samples at most CombatGapS apart into one window. known is false when the file
// or any bot sample is missing: nothing then proves combat, so a rule must not read the gap as
// idleness.
func combatWindows(repDir string) ([]CombatWindow, bool) {
	rows, err := events.ReadJSONL(filepath.Join(repDir, "samples.jsonl"))
	if err != nil {
		return nil, false
	}
	var times []float64
	botSamples := 0
	for _, row := range rows {
		if !strings.EqualFold(pyjson.Str(row["unit"]), "bot") {
			continue
		}
		botSamples++
		if !pyjson.Truthy(row["in_combat"]) {
			continue
		}
		if t, ok := pyjson.Float(row["t"]); ok {
			times = append(times, t)
		}
	}
	if botSamples == 0 {
		return nil, false
	}
	sort.Float64s(times)
	var windows []CombatWindow
	for _, t := range times {
		if n := len(windows); n > 0 && t-windows[n-1].To <= CombatGapS {
			windows[n-1].To = t
			continue
		}
		windows = append(windows, CombatWindow{From: t, To: t})
	}
	return windows, true
}

// readLedger reads ledger.jsonl's rows, skipping a line the runner cut short or that is not an object
// (the runner appends as it goes: a killed run leaves its last line partial).
func readLedger(path string) ([]map[string]any, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		doc, err := pyjson.Unmarshal([]byte(line))
		if err != nil {
			continue
		}
		if row, ok := pyjson.Object(doc); ok {
			out = append(out, row)
		}
	}
	return out, true
}

// learnedRe finds the id and result of each `<id>=learned|known` part of a learn read-back.
var learnedRe = regexp.MustCompile(`(\d+)=(learned|known)\b`)

// learnedIDs reads a bot_talents reply ("762=learned 14520=known") as the spell ids the module proved
// the bot learned or already knew.
func learnedIDs(reply string) []int64 {
	var out []int64
	for _, m := range learnedRe.FindAllStringSubmatch(reply, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func sortedIDs(set map[int64]bool) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func firstString(values ...any) string {
	for _, v := range values {
		if v == nil {
			continue
		}
		if s := strings.TrimSpace(pyjson.Str(v)); s != "" {
			return s
		}
	}
	return ""
}

func intOf(f float64) int { return int(f) }
