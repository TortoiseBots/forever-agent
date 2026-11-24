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
// `bot_talents` rows, and the events' casts. The files are the runner's own: a missing or corrupt one
// leaves the matching Known flag false, so a rule reports the missing data instead of judging it.
func LoadRep(repDir string, trial map[string]any) Record {
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
	if name := pyjson.Str(setup["bot_name"]); name != "" {
		rec.Bot = name
	}
	equipped := map[int64]bool{}
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
			for _, idVal := range ids {
				if id, ok := pyjson.Int64(idVal); ok && id > 0 {
					equipped[id] = true
					rec.EquippedKnown = true
				}
			}
		}
	}
	ledger, ledgerOK := readLedger(filepath.Join(repDir, "ledger.jsonl"))
	if ledgerOK {
		talents := map[int64]bool{}
		for _, row := range ledger {
			switch pyjson.Str(row["kind"]) {
			case "equip":
				if rec.Bot != "" && !strings.EqualFold(pyjson.Str(row["bot"]), rec.Bot) {
					continue
				}
				if !strings.Contains(strings.ToLower(pyjson.Str(row["reply"])), "equipped") {
					continue
				}
				if id, ok := pyjson.Int64(row["item"]); ok && id > 0 {
					equipped[id] = true
					rec.EquippedKnown = true
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
			}
		}
		rec.Talents = sortedIDs(talents)
	}
	ids := make([]int64, 0, len(equipped))
	for id := range equipped {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		rec.Equipped = append(rec.Equipped, Item{Entry: id})
	}

	// The casts are the normalised events' `cast` rows of this bot (one per cast), by spell name.
	if rows, err := events.ReadJSONL(filepath.Join(repDir, "events.jsonl")); err == nil {
		var first, last float64
		seen := false
		for _, row := range rows {
			t, hasT := pyjson.Float(row["t"])
			if hasT {
				if !seen || t < first {
					first = t
				}
				if !seen || t > last {
					last = t
				}
				seen = true
			}
			if pyjson.Str(row["kind"]) != "cast" {
				continue
			}
			if rec.Bot == "" || !strings.EqualFold(pyjson.Str(row["src"]), rec.Bot) {
				continue
			}
			name := strings.TrimSpace(pyjson.Str(row["spell"]))
			if name != "" {
				rec.Casts[name]++
			}
		}
		if rec.ActiveS = scoredSeconds(repDir); rec.ActiveS <= 0 && seen && last > first {
			rec.ActiveS = last - first
		}
	} else {
		rec.ActiveS = scoredSeconds(repDir)
	}
	if rec.ActiveS <= 0 {
		rec.ActiveS = pyjson.NumOrZero(trial["duration_s"])
	}
	return rec
}

// scoredSeconds is the repeat's scored time from its verdicts.json, 0 when the file or the field is
// absent (a pre-scored or unreadable repeat).
func scoredSeconds(repDir string) float64 {
	doc, err := pyjson.UnmarshalFile(filepath.Join(repDir, "verdicts.json"))
	if err != nil {
		return 0
	}
	obj, _ := pyjson.Object(doc)
	return pyjson.NumOrZero(obj["scored_s"])
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
		if s := strings.TrimSpace(pyjson.Str(v)); s != "" {
			return s
		}
	}
	return ""
}

func intOf(f float64) int { return int(f) }
