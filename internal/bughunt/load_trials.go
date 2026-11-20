package bughunt

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// trialsKinds are the normalised event kinds the checker reads.
var trialsKinds = map[string]bool{
	"cast": true, "cast_start": true, "damage": true, "heal": true,
	"aura_on": true, "aura_off": true, "death": true, "cast_fail": true,
}

// trialsClasses are the words a repeat's trial name may carry for its bot's class.
var trialsClasses = map[string]bool{
	"warrior": true, "priest": true, "mage": true, "rogue": true, "hunter": true,
	"warlock": true, "paladin": true, "druid": true, "shaman": true,
}

// botRoleEvidence reads the role a setup check reports for another bot: "Bot <name> role set to <role>".
var botRoleEvidence = regexp.MustCompile(`Bot (\w+) role set to (\w+)`)

// trialMemberUnits are the unit words a member's sample row carries: the bot, its up to three extras and the player.
// A hostile's row is "mob:<guid>"; any other word names nothing the checker can use, so it is a corrupt row.
var trialMemberUnits = map[string]bool{"player": true, "bot": true, "extra1": true, "extra2": true, "extra3": true}

// checkTrialSample checks one samples.jsonl row against the fields the checker reads. The Python loader reads them by
// key (an absent one is a KeyError and a failed load), so a row that lacks one, or carries a non-number where a number
// belongs, is a corrupt recording: coercing it to zero would collapse its time, hide a unit or invent a death. A mob
// row carries hp, max_hp and the position; a member row also carries power and max_power.
func checkTrialSample(s map[string]any) error {
	unit, ok := s["unit"].(string)
	if !ok || unit == "" {
		return errors.New("no unit")
	}
	mob := strings.HasPrefix(unit, "mob:") && len(unit) > len("mob:")
	if !mob && !trialMemberUnits[unit] {
		return fmt.Errorf("unknown unit %.20q", unit)
	}
	if g, ok := s["guid"].(string); !ok || g == "" {
		return errors.New("no guid")
	}
	if _, ok := s["t"].(float64); !ok {
		return errors.New("no numeric t")
	}
	fields := []string{"hp", "max_hp", "x", "y", "z"}
	if !mob {
		fields = append(fields, "power", "max_power")
	}
	for _, k := range fields {
		if _, ok := s[k].(float64); !ok {
			return fmt.Errorf("no numeric %s", k)
		}
	}
	return nil
}

// LoadTrials loads a trials repeat directory: events.jsonl (normalised events), samples.jsonl (1 Hz units) and
// setup.json (the bot's name, the extras and the setup checks), the files the Python runner wrote. This is the
// port of the Python checker's load_trials: the same identity keys, role and class words, frame fields and subject
// set.
//
// events.jsonl and samples.jsonl are required: a missing, unreadable or corrupt one is an error naming the file and
// the line, never an empty run that reads as clean. samples.jsonl must hold at least one row, and every row must carry
// the fields the checker reads with their types (a numeric t, a recognised unit, a guid, and the unit kind's health,
// power and position numbers): one bad row is an error naming the file and that line, never a row silently coerced to
// zero, and a file with no usable frame is an error too. A file whose only content is a cut-short last line is an
// error as well. setup.json is optional: a repeat without it loads with no bot name, extras or setup roles. The
// samples are ordered by time on load (a stable sort, so rows of one time keep their file order) and an exactly
// repeated row is kept once. A trials repeat carries no meal telemetry, so rule 13 is a skipped check there.
func LoadTrials(repDir, ds string) (*Run, error) {
	clean := filepath.Clean(repDir)
	st, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s: not a trials repeat directory", repDir)
	}
	rep := filepath.Base(clean)
	trial := filepath.Base(filepath.Dir(clean))
	parent := filepath.Base(filepath.Dir(filepath.Dir(clean)))
	r := &Run{
		Kind:      "trials",
		Name:      parent + "/" + trial + "/" + rep,
		DS:        ds,
		Path:      clean,
		Roles:     map[string]string{},
		Classes:   map[string]string{},
		Subjects:  map[string]bool{},
		CastTimes: map[string][]float64{},
	}
	g2n := map[string]string{}
	events, err := readTrialsJSONL(filepath.Join(clean, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, tr := range events {
		e := tr.Row
		kind := rowStr(e, "kind")
		if kind == "" {
			continue
		}
		if sg, sn := rowStr(e, "src_guid"), rowStr(e, "src"); sg != "" && sn != "" {
			g2n[sg] = sn
		}
		if dg, dn := rowStr(e, "dst_guid"), rowStr(e, "dst"); dg != "" && dn != "" {
			g2n[dg] = dn
		}
		t := rowNumOK(e, "t")
		if trialsKinds[kind] {
			ev := Event{
				T:       t,
				Kind:    kind,
				Src:     rowStr(e, "src"),
				Dst:     rowStr(e, "dst"),
				Spell:   rowStr(e, "spell"),
				SpellID: int(rowNumOK(e, "spell_id")),
				SrcRole: rowStr(e, "src_role"),
				DstRole: rowStr(e, "dst_role"),
			}
			if v, ok := e["amount"]; ok && v != nil {
				amount := rowNumOK(e, "amount")
				ev.Amount = &amount
			}
			r.Events = append(r.Events, ev)
		}
		if kind == "death" {
			who := rowStr(e, "dst")
			if who == "" {
				who = rowStr(e, "src")
			}
			r.Deaths = append(r.Deaths, Death{T: t, Bot: who})
		}
	}
	setup := readSetup(filepath.Join(clean, "setup.json"))
	parts := strings.Split(strings.Split(trial, "@")[0], "-")
	brole, bcls := "dps", "?"
	if len(parts) > 0 && (parts[0] == "tank" || parts[0] == "healer" || parts[0] == "dps") {
		brole = parts[0]
	}
	if len(parts) > 1 && trialsClasses[parts[1]] {
		bcls = parts[1]
	}
	bot := rowStr(setup, "bot_name")
	if bot != "" {
		r.Roles[bot], r.Classes[bot] = brole, bcls
		r.Subjects[bot] = true
	}
	if extras, ok := setup["extra_names"].([]any); ok {
		for _, x := range extras {
			if n, ok := x.(string); ok && n != "" {
				r.Subjects[n] = true
			}
		}
	}
	if checks, ok := setup["checks"].([]any); ok {
		for _, c := range checks {
			check, ok := c.(map[string]any)
			if !ok {
				continue
			}
			id := rowStr(check, "id")
			if id != "extra_bots_role" && id != "bot_role" {
				continue
			}
			for _, m := range botRoleEvidence.FindAllStringSubmatch(rowStr(check, "evidence"), -1) {
				r.Roles[m[1]] = m[2]
			}
		}
	}
	// The run's clock comes from the first sample that carries a wall time while the repeat has one time group: the
	// Python sets it only there. The samples are ordered by time first, so that sample is the earliest one.
	samplesPath := filepath.Join(clean, "samples.jsonl")
	samples, err := readTrialsJSONL(samplesPath)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("%s: no samples", samplesPath)
	}
	// Every row is schema-checked before anything is built from it: the Python loader reads the fields it needs by key,
	// so a row that lacks one is a failed load there, and a row the checker coerces to zero here would collapse times
	// or hide a unit. One bad row fails the load, and the error names the first one.
	rows := make([]map[string]any, 0, len(samples))
	for _, s := range samples {
		if err := checkTrialSample(s.Row); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", samplesPath, s.Line, err)
		}
		rows = append(rows, s.Row)
	}
	rows = orderSamples(rows)
	seenT := map[float64]bool{}
	nT := 0
	for _, s := range rows {
		t := rowNumOK(s, "t")
		if !seenT[t] {
			seenT[t] = true
			nT++
		}
		if r.T0 == 0 && nT == 1 {
			if wall := rowStr(s, "wall"); wall != "" {
				r.T0 = parseISO(wall) - t
			}
		}
	}
	groups := groupSamples(rows)
	if len(groups) == 0 {
		return nil, fmt.Errorf("%s: no usable frames", samplesPath)
	}
	for _, g := range groups {
		f := &Frame{T: g.t, Units: map[string]*Unit{}}
		for _, s := range g.rows {
			unit := rowStr(s, "unit")
			guid := rowStr(s, "guid")
			if strings.HasPrefix(unit, "mob:") {
				f.Mobs = append(f.Mobs, &Mob{
					G:      guid,
					Entry:  entryOf(guid),
					HPPct:  100 * rowNumOK(s, "hp") / maxf(rowNumOK(s, "max_hp"), 1),
					X:      rowNumOK(s, "x"),
					Y:      rowNumOK(s, "y"),
					Z:      rowNumOK(s, "z"),
					Combat: truthy(s["in_combat"]),
					Target: rowStr(s, "target"),
					Name:   g2n[guid],
				})
				continue
			}
			n := g2n[guid]
			if n == "" {
				n = unit
			}
			if unit == "player" {
				if _, ok := r.Roles[n]; !ok {
					r.Roles[n] = "player"
				}
			}
			mp := rowNumOK(s, "max_power")
			pt := 0
			switch mp {
			case 1000:
				pt = 1
			case 100:
				pt = 3
			}
			u := &Unit{
				G:        guid,
				Alive:    rowNumOK(s, "hp") > 0,
				Combat:   truthy(s["in_combat"]),
				HP:       rowNumOK(s, "hp"),
				MaxHP:    maxf(rowNumOK(s, "max_hp"), 1),
				Power:    rowNumOK(s, "power"),
				MaxPower: maxf(mp, 1),
				PType:    pt,
				X:        rowNumOK(s, "x"),
				Y:        rowNumOK(s, "y"),
				Z:        rowNumOK(s, "z"),
				Casting:  truthy(s["casting"]),
				Target:   rowStr(s, "target"),
			}
			if auras, ok := s["auras"].([]any); ok {
				for _, a := range auras {
					if n, ok := a.(float64); ok {
						u.Auras = append(u.Auras, uint32(n))
					}
				}
			}
			f.addUnit(n, u)
			switch unit {
			case "bot", "extra1", "extra2", "extra3":
				r.Subjects[n] = true
			}
		}
		for _, u := range f.Units {
			if u.Combat {
				f.GC = true
				break
			}
		}
		r.Frames = append(r.Frames, f)
	}
	for _, e := range r.Events {
		switch e.Kind {
		case "cast", "cast_start", "damage", "heal":
			if e.Src != "" {
				r.CastTimes[e.Src] = append(r.CastTimes[e.Src], e.T)
			}
		}
	}
	for _, ts := range r.CastTimes {
		sort.Float64s(ts)
	}
	r.finalise()
	return r, nil
}

// sampleGroup is the samples of one time: the Python groups by t with an OrderedDict.
type sampleGroup struct {
	t    float64
	rows []map[string]any
}

// groupSamples groups ordered samples by t; the rows of one time keep their order.
func groupSamples(samples []map[string]any) []sampleGroup {
	var groups []sampleGroup
	byT := map[float64]int{}
	for _, s := range samples {
		t := rowNumOK(s, "t")
		i, ok := byT[t]
		if !ok {
			i = len(groups)
			byT[t] = i
			groups = append(groups, sampleGroup{t: t})
		}
		groups[i].rows = append(groups[i].rows, s)
	}
	return groups
}

// orderSamples orders the samples by time with a stable sort, so rows of one time keep the file's order, and keeps
// an exactly repeated row once: a retried or re-merged recording repeats rows, and a duplicated sample would put its
// mob in the frame twice. The frame list and every window a rule reads then move forward in time.
func orderSamples(samples []map[string]any) []map[string]any {
	seen := make(map[string]bool, len(samples))
	uniq := samples[:0]
	for _, s := range samples {
		b, err := json.Marshal(s)
		if err != nil {
			uniq = append(uniq, s)
			continue
		}
		if seen[string(b)] {
			continue
		}
		seen[string(b)] = true
		uniq = append(uniq, s)
	}
	sort.SliceStable(uniq, func(i, j int) bool { return rowNumOK(uniq[i], "t") < rowNumOK(uniq[j], "t") })
	return uniq
}

// trialsRow is one row of a required trials JSONL file with the line it came from, so a bad row's error can name it.
type trialsRow struct {
	Line int
	Row  map[string]any
}

// readTrialsJSONL reads a required trials JSONL file. A file that cannot be opened or read is an error naming it, as
// is a line that is not a JSON object, with the line number; blank lines are skipped. A cut-short last line is the
// one exception: a writer killed mid-row leaves a file that does not end with a newline, and that line is dropped,
// as readJSONL drops it. A file whose only row is cut short is an error, not an empty input.
func readTrialsJSONL(path string) ([]trialsRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	endsNL := true
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], st.Size()-1); err == nil {
			endsNL = last[0] == '\n'
		}
	}
	var out []trialsRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line, badLine, badText := 0, 0, ""
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(text), &row); err != nil || row == nil {
			if badLine == 0 {
				badLine, badText = line, text
			}
			continue
		}
		out = append(out, trialsRow{Line: line, Row: row})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if badLine != 0 && (endsNL || badLine != line || len(out) == 0) {
		return nil, fmt.Errorf("%s:%d: not a JSON object: %.60q", path, badLine, badText)
	}
	return out, nil
}

// readSetup reads a repeat's setup.json, an optional file; a missing or invalid one is an empty object.
func readSetup(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var setup map[string]any
	if err := json.Unmarshal(b, &setup); err != nil {
		return nil
	}
	return setup
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
