package bughunt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trace"
)

// writeWatch writes a watch recording: a two-frame trace, the summary with the instance report's bosses, and a
// two-row combat log.
func writeWatch(t *testing.T, dir string) string {
	t.Helper()
	runID := "r-20261120T100000-abcd"
	tracePath := filepath.Join(dir, runID+".jsonl")
	var lines []any
	env := func(k string, ms int64) trace.Envelope {
		return trace.Envelope{V: trace.Version, K: k, Run: runID, Src: trace.Producer, T: ms}
	}
	lines = append(lines, trace.Header{
		Envelope: env(trace.KindHeader, 0), Schema: trace.Schema, Scope: "party",
		StartUnix: 1_700_000_000, IntervalMS: 2000,
	})
	for _, m := range []trace.Member{
		{G: gTank, Name: "Atank", Role: "tank", Class: "warrior"},
		{G: gHeal, Name: "Bheal", Role: "heal", Class: "priest"},
		{G: gDps, Name: "Cmage", Role: "dps", Class: "mage"},
	} {
		lines = append(lines, trace.PartyRecord{Envelope: env(trace.KindParty, 0), Member: m})
	}
	lines = append(lines, trace.MobRecord{
		Envelope: env(trace.KindMobs, 0),
		MobInfo:  trace.MobInfo{G: gid(0xB01), Entry: 1234, Name: "mob"},
	})
	dl := float32(12.5)
	frame := func(ms int64, dpsAlive bool) trace.Frame {
		return trace.Frame{
			Envelope: env(trace.KindFrame, ms),
			Via:      "session",
			Bots: []trace.Bot{
				{G: gTank, Alive: true, Combat: true, State: "c", HP: 100, MaxHP: 100, Power: 0, MaxPow: 100, PType: 1},
				{G: gHeal, Alive: true, Combat: true, State: "c", HP: 90, MaxHP: 100, Power: 50, MaxPow: 100, Move: "follow", DL: &dl},
				{G: gDps, Alive: dpsAlive, Combat: false, State: "n", HP: 100, MaxHP: 100, Power: 100, MaxPow: 100, Life: "dead", Stuck: "blocked:no path"},
			},
			Mobs: []trace.Mob{{
				G: gid(0xB01), HPPct: 80, X: 1, Y: 2, Z: 3, Combat: true, Victim: gHeal,
				Top: []trace.TopEntry{{G: gHeal, Threat: 100}},
			}},
		}
	}
	lines = append(lines, frame(0, true), frame(2000, false))
	f, err := os.Create(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sum := map[string]any{
		"run_id":   runID,
		"instance": 309,
		"bosses":   map[string]bool{"5678": true},
		"watch":    map[string]any{"state": "running", "frames": 2, "bosses": map[string]string{"1234": "alive", "5678": "dead"}},
	}
	b, _ := json.Marshal(sum)
	if err := os.WriteFile(filepath.Join(dir, runID+".forever-run.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var combat []byte
	for _, row := range []CombatRow{
		{Seq: 1, Kind: "spell_start", Source: CombatActor{GUID: gHeal}, Target: CombatActor{GUID: gTank}, SpellID: 6066},
		{Seq: 2, Kind: "heal", Source: CombatActor{GUID: gHeal}, Target: CombatActor{GUID: gTank}, SpellID: 6066, Amount: 120},
	} {
		line, _ := json.Marshal(row)
		combat = append(append(combat, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".combatlog.jsonl"), combat, 0o644); err != nil {
		t.Fatal(err)
	}
	return tracePath
}

// TestLoadWatchRecording: the watch folder's trace, party rows, summary bosses and combat log load into one run.
func TestLoadWatchRecording(t *testing.T) {
	dir := t.TempDir()
	writeWatch(t, dir)
	r, err := LoadWatch(dir, "watchds")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r.Kind != "watch" || r.Name != "r-20261120T100000-abcd" || r.DS != "watchds" {
		t.Fatalf("run %q/%q/%q", r.Kind, r.Name, r.DS)
	}
	if r.T0 != 1_700_000_000 {
		t.Fatalf("T0 %v", r.T0)
	}
	if r.Role(gTank) != "tank" || r.Class(gTank) != "warrior" || r.Role(gHeal) != "heal" || r.Class(gHeal) != "priest" {
		t.Fatalf("party roles/classes %v %v", r.Roles, r.Classes)
	}
	if !r.IsSubject(gTank) || !r.IsSubject(gHeal) || !r.IsSubject(gDps) || len(r.Subjects) != 3 {
		t.Fatalf("subjects %v", r.Subjects)
	}
	if len(r.Frames) != 2 || len(r.Frames[0].Units) != 3 || len(r.Frames[0].Mobs) != 1 {
		t.Fatalf("frames %d, units %d, mobs %d", len(r.Frames), len(r.Frames[0].Units), len(r.Frames[0].Mobs))
	}
	u := r.Frames[0].Units[gHeal]
	if u.Move != "follow" || u.DL == nil || *u.DL != 12.5 || !u.Combat || u.PType != 0 {
		t.Fatalf("healer frame %+v", u)
	}
	if dps := r.Frames[0].Units[gDps]; dps.Stuck != "blocked:no path" {
		t.Fatalf("dps frame %+v", dps)
	}
	m := r.Frames[0].Mobs[0]
	if m.Entry != 1234 || m.Victim != gHeal || len(m.Top) != 1 || m.Top[0].Threat != 100 {
		t.Fatalf("mob %+v", m)
	}
	if _, ok := r.BossEntries[1234]; !ok {
		t.Fatalf("boss entries %v, want 1234 from the instance report", r.BossEntries)
	}
	if _, ok := r.BossEntries[5678]; !ok {
		t.Fatalf("boss entries %v, want 5678 from the summary's dead set", r.BossEntries)
	}
	if len(r.Combat) != 2 || r.Combat[0].Seq != 1 || r.Combat[1].Amount != 120 || r.Combat[1].Kind != "heal" {
		t.Fatalf("combat rows %+v", r.Combat)
	}
	if len(r.Deaths) != 1 || r.Deaths[0].Bot != gDps || r.Deaths[0].T != 2 {
		t.Fatalf("deaths %+v, want the DPS at t=2", r.Deaths)
	}
	if got, want := r.Clock(0), "22:13:20Z"; got != want {
		t.Fatalf("clock %q, want %q", got, want)
	}
}

// TestLoadWatchOneFile takes a trace file directly, with no summary or combat log beside it.
func TestLoadWatchOneFile(t *testing.T) {
	dir := t.TempDir()
	p := writeWatch(t, dir)
	r, err := LoadWatch(p, "ds")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(r.Frames) != 2 || len(r.Combat) != 2 {
		t.Fatalf("frames %d, combat %d", len(r.Frames), len(r.Combat))
	}
}

// TestLoadRosterFallback: a roster record names a member's role and class, and a run with no party rows takes the
// roster's members as its subjects.
func TestLoadRosterFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roster.jsonl")
	record := func(at, name, guid, class, role string) string {
		b, _ := json.Marshal(map[string]any{
			"at":   at,
			"bots": []any{map[string]any{"name": name, "guid": guid, "class": class, "role": role}},
		})
		return string(b) + "\n"
	}
	body := record("2026-11-20T10:00:00Z", "Atank", gTank, "1", "tank") +
		record("2026-11-20T10:00:02Z", "Bheal", gHeal, "4", "healer")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ro, err := LoadRoster(path)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if ro.Roles[gTank] != "tank" || ro.Classes[gTank] != "warrior" {
		t.Fatalf("tank %q/%q", ro.Roles[gTank], ro.Classes[gTank])
	}
	if ro.Roles[gHeal] != "healer" || ro.Classes[gHeal] != "priest" {
		t.Fatalf("healer %q/%q", ro.Roles[gHeal], ro.Classes[gHeal])
	}
	r := &Run{Roles: map[string]string{}, Classes: map[string]string{}, Subjects: map[string]bool{}}
	r.UseRoster(ro)
	if r.Role(gTank) != "tank" || r.Class(gHeal) != "priest" {
		t.Fatalf("fallback %v %v", r.Roles, r.Classes)
	}
	if !r.IsSubject(gTank) || !r.IsSubject(gHeal) || len(r.Subjects) != 2 {
		t.Fatalf("subjects %v, want the roster's members", r.Subjects)
	}
	// A member the frames already know keeps the frames' word.
	r2 := &Run{
		Roles:    map[string]string{gTank: "dps"},
		Classes:  map[string]string{},
		Subjects: map[string]bool{gTank: true},
	}
	r2.UseRoster(ro)
	if r2.Role(gTank) != "dps" || len(r2.Subjects) != 1 {
		t.Fatalf("a known member was overwritten: %v %v", r2.Roles, r2.Subjects)
	}
}

// TestLoadTrialsRepeat: the live1 repeat loads with the Python loader's identity keys, role and class words, frames
// and cast times.
func TestLoadTrialsRepeat(t *testing.T) {
	dir := filepath.Join("testdata", "live1", "healers", "healer-priest-heal-latency", "rep1")
	r, err := LoadTrials(dir, "live1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r.Kind != "trials" || r.Name != "healers/healer-priest-heal-latency/rep1" || r.DS != "live1" {
		t.Fatalf("run %q/%q/%q", r.Kind, r.Name, r.DS)
	}
	if r.Role("Bheal") != "healer" || r.Class("Bheal") != "priest" {
		t.Fatalf("bot role/class %q/%q, want the trial name's", r.Role("Bheal"), r.Class("Bheal"))
	}
	if r.Role("Atank") != "tank" {
		t.Fatalf("Atank role %q, want the setup check's", r.Role("Atank"))
	}
	if r.Role("player") != "player" {
		t.Fatalf("player role %q", r.Role("player"))
	}
	for _, want := range []string{"Bheal", "Atank", "Dcmd"} {
		if !r.IsSubject(want) {
			t.Fatalf("subjects %v lack %q", r.Subjects, want)
		}
	}
	// The player is a unit of the frame, not a subject: the Python adds only bot/extra1..3 to the subject set.
	if r.IsSubject("player") {
		t.Fatalf("subjects %v include the player", r.Subjects)
	}
	if len(r.Frames) != 5 {
		t.Fatalf("%d frames, want 5 sample times", len(r.Frames))
	}
	f := r.Frames[0]
	if f.T != 20.76 || !f.GC || !f.HasGCSince || f.GCSince != 20.76 {
		t.Fatalf("frame %+v", f)
	}
	if len(f.Units) != 3 || len(f.Mobs) != 1 {
		t.Fatalf("frame units %d, mobs %d", len(f.Units), len(f.Mobs))
	}
	bot := f.Units["Bheal"]
	if bot == nil || bot.G != "0x000000000000064e" || bot.HP != 1567 || bot.MaxPower != 3636 || bot.PType != 0 || !bot.Combat {
		t.Fatalf("bot unit %+v", bot)
	}
	player := f.Units["player"]
	if player == nil || player.PType != 1 || player.Combat {
		t.Fatalf("player unit %+v (max_power 1000 is the Python's rage type)", player)
	}
	mob := f.Mobs[0]
	if mob.Entry != 2734 || !mob.Combat || mob.Target != "0x000000000000064f" {
		t.Fatalf("mob %+v", mob)
	}
	if got := mob.HPPct; got < 32.8 || got > 32.9 {
		t.Fatalf("mob hp %v, want 743/2263 in percent", got)
	}
	if got := r.CastTimes["Bheal"]; len(got) != 3 || got[0] != 45.46 || got[2] != 173.03 {
		t.Fatalf("cast times %v", got)
	}
	if len(r.Events) != 3 || len(r.Deaths) != 0 {
		t.Fatalf("events %d, deaths %d", len(r.Events), len(r.Deaths))
	}
	if got, want := r.Clock(0), "02:20:03Z"; got != want {
		t.Fatalf("clock %q, want %q", got, want)
	}
}

// TestEpisodesBreakOnGap: frames more than MAX_GAP_S apart are two episodes, not one.
func TestEpisodesBreakOnGap(t *testing.T) {
	roles := map[string]string{gDps: "dps"}
	frames := []*Frame{frameAt(0, testUnit(gDps)), frameAt(20, testUnit(gDps))}
	r := testRun("watch", roles, frames...)
	eps := r.Episodes(func(*Frame, string, *Unit) (string, bool) { return "always", true }, -1, false)
	if len(eps) != 2 {
		t.Fatalf("%d episodes across a 16 s gap, want 2: %+v", len(eps), eps)
	}
}

// trialsDir is an empty repeat directory named as the checker expects: the trial's name carries the bot's role and
// class words.
func trialsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "situations", "healer-priest-heal-latency", "rep1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// jsonLine is one JSONL row.
func jsonLine(t *testing.T, row map[string]any) string {
	t.Helper()
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeTrialsRaw writes a file verbatim, so a test can leave it empty or cut it short.
func writeTrialsRaw(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTrialsFile writes lines as a JSONL file; nil leaves the file out, an empty slice writes an empty file.
func writeTrialsFile(t *testing.T, dir, name string, lines []string) {
	t.Helper()
	if lines == nil {
		return
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	writeTrialsRaw(t, dir, name, b.String())
}

// trialsCast is an events.jsonl row that maps the healer's guid to its name, as the runner's events do.
func trialsCast(healer string) map[string]any {
	return map[string]any{"kind": "cast", "src": "Bheal", "src_guid": healer, "t": 1.0, "spell": "Renew"}
}

// TestLoadTrialsRequiredFiles: events.jsonl and samples.jsonl are required, so a missing, unreadable or corrupt one
// is an error naming the file, never a successful empty run; setup.json stays optional.
func TestLoadTrialsRequiredFiles(t *testing.T) {
	healer := gid(0x64e)
	samples := func(t *testing.T) []string {
		return []string{jsonLine(t, map[string]any{
			"t": 0.0, "unit": "bot", "guid": healer, "hp": 1500, "max_hp": 1500, "power": 3000, "max_power": 3636,
			"x": -11920.0, "y": -1281.0, "z": 85.0,
		})}
	}
	events := func(t *testing.T) []string { return []string{jsonLine(t, trialsCast(healer))} }
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name:  "events.jsonl is missing",
			setup: func(t *testing.T, dir string) { writeTrialsFile(t, dir, "samples.jsonl", samples(t)) },
			want:  "events.jsonl",
		},
		{
			name:  "samples.jsonl is missing",
			setup: func(t *testing.T, dir string) { writeTrialsFile(t, dir, "events.jsonl", events(t)) },
			want:  "samples.jsonl",
		},
		{
			name: "events.jsonl cannot be read",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "samples.jsonl", samples(t))
				if err := os.MkdirAll(filepath.Join(dir, "events.jsonl"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "events.jsonl",
		},
		{
			name: "events.jsonl is not JSON",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "samples.jsonl", samples(t))
				writeTrialsRaw(t, dir, "events.jsonl", "not an object\n")
			},
			want: "events.jsonl:1",
		},
		{
			name: "events.jsonl has an oversized line",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "samples.jsonl", samples(t))
				writeTrialsRaw(t, dir, "events.jsonl", strings.Repeat("x", 9<<20)+"\n")
			},
			want: "events.jsonl",
		},
		{
			name: "samples.jsonl is corrupt in the middle",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "events.jsonl", events(t))
				writeTrialsFile(t, dir, "samples.jsonl", []string{samples(t)[0], "not an object", samples(t)[0]})
			},
			want: "samples.jsonl:2",
		},
		{
			name: "samples.jsonl is only a cut-short row",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "events.jsonl", events(t))
				writeTrialsRaw(t, dir, "samples.jsonl", `{"t": 0, "unit": "bot"`)
			},
			want: "samples.jsonl",
		},
		{
			name: "samples.jsonl is empty",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "events.jsonl", events(t))
				writeTrialsFile(t, dir, "samples.jsonl", []string{})
			},
			want: "no samples",
		},
		{
			name: "an empty events.jsonl is a repeat without events",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "events.jsonl", []string{})
				writeTrialsFile(t, dir, "samples.jsonl", samples(t))
			},
		},
		{
			name: "an invalid setup.json is still optional",
			setup: func(t *testing.T, dir string) {
				writeTrialsFile(t, dir, "events.jsonl", events(t))
				writeTrialsFile(t, dir, "samples.jsonl", samples(t))
				writeTrialsRaw(t, dir, "setup.json", "{not json")
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := trialsDir(t)
			c.setup(t, dir)
			r, err := LoadTrials(dir, "testds")
			if c.want == "" {
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				if r == nil {
					t.Fatal("nil run without an error")
				}
				return
			}
			if err == nil {
				t.Fatalf("loaded %+v without an error, want %q", r, c.want)
			}
			if r != nil {
				t.Fatalf("run %+v beside the error %v, want nil", r, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// TestLoadTrialsSampleSchema: a samples row that is valid JSON but lacks a field the checker reads, or carries the
// wrong type, is a corrupt recording. The load fails naming the file and the first bad line, never coercing the row to
// zero; a row kind that needs no power needs none, and a cut-short last line is still dropped.
func TestLoadTrialsSampleSchema(t *testing.T) {
	healer := gid(0x64e)
	mobGUID := gid(0xB01)
	good := func() map[string]any {
		return map[string]any{
			"t": 0.0, "unit": "bot", "guid": healer, "hp": 1500, "max_hp": 1500, "power": 3000, "max_power": 3636,
			"x": -11920.0, "y": -1281.0, "z": 85.0,
		}
	}
	mob := func() map[string]any {
		return map[string]any{
			"t": 0.0, "unit": "mob:" + mobGUID, "guid": mobGUID, "hp": 500, "max_hp": 1000,
			"x": -11925.0, "y": -1281.0, "z": 85.0,
		}
	}
	after := func(bad map[string]any) func() string {
		return func() string { return jsonLine(t, good()) + "\n" + jsonLine(t, bad) + "\n" }
	}
	for _, c := range []struct {
		name    string
		content func() string
		want    string
	}{
		{
			name:    "an empty object",
			content: func() string { return "{}\n" },
			want:    "samples.jsonl:1: no unit",
		},
		{
			name:    "a row without t",
			content: after(func() map[string]any { b := good(); delete(b, "t"); return b }()),
			want:    "samples.jsonl:2: no numeric t",
		},
		{
			name:    "a t that is text",
			content: after(func() map[string]any { b := good(); b["t"] = "0"; return b }()),
			want:    "samples.jsonl:2: no numeric t",
		},
		{
			name:    "an unknown unit",
			content: after(func() map[string]any { b := good(); b["unit"] = "gargoyle"; return b }()),
			want:    "samples.jsonl:2: unknown unit",
		},
		{
			name:    "a row without guid",
			content: after(func() map[string]any { b := good(); delete(b, "guid"); return b }()),
			want:    "samples.jsonl:2: no guid",
		},
		{
			name:    "a member row without power",
			content: after(func() map[string]any { b := good(); delete(b, "power"); return b }()),
			want:    "samples.jsonl:2: no numeric power",
		},
		{
			name:    "a member row with a text max_hp",
			content: after(func() map[string]any { b := good(); b["max_hp"] = "1500"; return b }()),
			want:    "samples.jsonl:2: no numeric max_hp",
		},
		{
			name:    "a mob row without max_hp",
			content: after(func() map[string]any { b := mob(); delete(b, "max_hp"); return b }()),
			want:    "samples.jsonl:2: no numeric max_hp",
		},
		{
			name:    "a mob row without a position",
			content: after(func() map[string]any { b := mob(); delete(b, "z"); return b }()),
			want:    "samples.jsonl:2: no numeric z",
		},
		{
			name: "a mob row needs no power",
			content: func() string {
				return jsonLine(t, good()) + "\n" + jsonLine(t, mob()) + "\n"
			},
		},
		{
			name: "a cut-short last line after a valid row is dropped",
			content: func() string {
				return jsonLine(t, good()) + "\n" + `{"t": 1, "unit": "bot"`
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := trialsDir(t)
			writeTrialsFile(t, dir, "events.jsonl", []string{jsonLine(t, trialsCast(healer))})
			writeTrialsRaw(t, dir, "samples.jsonl", c.content())
			r, err := LoadTrials(dir, "testds")
			if c.want == "" {
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				if r == nil || len(r.Frames) == 0 {
					t.Fatalf("run %+v without frames", r)
				}
				return
			}
			if err == nil {
				t.Fatalf("loaded %+v without an error, want %q", r, c.want)
			}
			if r != nil {
				t.Fatalf("run %+v beside the error %v, want nil", r, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// TestLoadTrialsOrdersSamples: samples written out of time order load in time order (a stable sort), an exactly
// repeated row is kept once, and the run's clock is the earliest sample's wall time.
func TestLoadTrialsOrdersSamples(t *testing.T) {
	dir := trialsDir(t)
	healer := gid(0x64e)
	writeTrialsFile(t, dir, "events.jsonl", []string{jsonLine(t, trialsCast(healer))})
	writeTrialsFile(t, dir, "setup.json", []string{jsonLine(t, map[string]any{"bot_name": "Bheal"})})
	bot := func(tm float64, wall string) string {
		return jsonLine(t, map[string]any{
			"t": tm, "unit": "bot", "guid": healer, "hp": 1500, "max_hp": 1500, "power": 3000, "max_power": 3636,
			"x": -11920.0, "y": -1281.0, "z": 85.0, "wall": wall,
		})
	}
	mob := jsonLine(t, map[string]any{
		"t": 10.0, "unit": "mob:0xf130000aae3d0d9b", "guid": "0xf130000aae3d0d9b",
		"hp": 500, "max_hp": 1000, "in_combat": false, "x": -11925.0, "y": -1281.0, "z": 85.0,
	})
	writeTrialsFile(t, dir, "samples.jsonl", []string{
		bot(20, "2026-11-08T03:30:00Z"),
		bot(0, "2026-11-08T03:20:24Z"),
		bot(10, ""),
		mob,
		mob,
	})
	r, err := LoadTrials(dir, "testds")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []float64{0, 10, 20}
	if len(r.Frames) != len(want) {
		t.Fatalf("%d frames, want %d: %+v", len(r.Frames), len(want), r.Frames)
	}
	for i, f := range r.Frames {
		if f.T != want[i] {
			t.Fatalf("frame %d at t=%v, want %v: the samples are not in time order", i, f.T, want[i])
		}
	}
	if got := len(r.Frames[1].Mobs); got != 1 {
		t.Fatalf("%d mobs in the t=10 frame, want the repeated sample kept once", got)
	}
	if got, wantClock := r.Clock(0), "03:20:24Z"; got != wantClock {
		t.Fatalf("clock %q, want %q (the earliest sample's wall time)", got, wantClock)
	}
	if got := len(r.framesBetween(0, 20)); got != 3 {
		t.Fatalf("framesBetween(0, 20) = %d frames, want 3 in order", got)
	}
	eps := r.Episodes(func(*Frame, string, *Unit) (string, bool) { return "always", true }, -1, false)
	var subject *Episode
	for i := range eps {
		if eps[i].Bot == "Bheal" {
			subject = &eps[i]
		}
	}
	if subject == nil || subject.T0 != 0 || subject.T1 != 20 {
		t.Fatalf("episode %+v, want Bheal 0..20 across the reordered samples", subject)
	}
}

// TestRule13SilentOnTrials: a low-mana healer in a stationary trials repeat gets no rule 13 hit: the repeat records
// no meal telemetry, so "no drink" is unknown, not a fact.
func TestRule13SilentOnTrials(t *testing.T) {
	dir := trialsDir(t)
	healer := gid(0x64e)
	writeTrialsFile(t, dir, "events.jsonl", []string{jsonLine(t, trialsCast(healer))})
	writeTrialsFile(t, dir, "setup.json", []string{jsonLine(t, map[string]any{"bot_name": "Bheal"})})
	var samples []string
	for i := range 7 {
		tm := float64(i) * 5
		samples = append(samples, jsonLine(t, map[string]any{
			"t": tm, "unit": "bot", "guid": healer, "hp": 1500, "max_hp": 1500,
			"power": 727, "max_power": 3636, "in_combat": false,
			"x": -11920.0, "y": -1281.0, "z": 85.0, "wall": "2026-11-08T03:20:24Z",
		}))
		for k := range 3 {
			samples = append(samples, jsonLine(t, map[string]any{
				"t": tm, "unit": fmt.Sprintf("extra%d", k+1), "guid": gid(uint64(0xB00 + k)),
				"hp": 1500, "max_hp": 1500, "power": 100, "max_power": 100, "in_combat": false,
				"x": float64(k), "y": 0.0, "z": 85.0,
			}))
		}
	}
	writeTrialsFile(t, dir, "samples.jsonl", samples)
	r, err := LoadTrials(dir, "testds")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(r.Frames) != 7 {
		t.Fatalf("%d frames, want 7", len(r.Frames))
	}
	if r.ReportsMeals() {
		t.Fatal("a trials repeat reports meals: the samples carry no eat/drink field")
	}
	if hits := Rule13(r); len(hits) != 0 {
		t.Fatalf("%d rule 13 hits on a trials repeat, want none (the meal input is unknown): %+v", len(hits), hits)
	}
}
