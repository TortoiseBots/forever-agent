package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

// The Forever fixtures are synthetic fight records in the module's own shape ({fight, warnings} with bots' name/class/role
// and {n, c} spell lines), one per class: a warrior tank, the class's bot with the spell lists of its Forever spec, and for
// the paladin an off-tank casting the Turtle-only taunt Hand of Reckoning. They carry no real recording, account or character.

// foreverClassCase is one class's fixture: the bot to read, its Forever spec, and the spell it casts that the Turtle oracle
// does not know (so the two oracles can be told apart).
type foreverClassCase struct {
	class  string
	bot    string
	spec   string
	only   string
	onlyID uint32
}

var foreverClasses = []foreverClassCase{
	{"warrior", "Warriorbot", "arms", "Rend", 772},
	{"paladin", "Paladinbot", "holy", "Purify", 1152},
	{"hunter", "Hunterbot", "beast_mastery", "Concussive Shot", 5116},
	{"rogue", "Roguebot", "assassination", "Expose Armor", 8647},
	{"priest", "Priestbot", "shadow", "Silence", 15487},
	{"shaman", "Shamanbot", "elemental", "Frost Shock", 8056},
	{"mage", "Magebot", "fire", "Counterspell", 2139},
	{"warlock", "Warlockbot", "affliction", "Amplify Curse", 18288},
	{"druid", "Druidbot", "restoration", "Rebirth", 20484},
}

func foreverFightPath(class string) string { return "testdata/fights/forever/" + class + "-1.json" }

// runFightScoreRaw runs the command and returns its exit code and both streams without judging the code.
func runFightScoreRaw(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), append([]string{"fight-score"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// runFightScore runs the command and returns its output, or fails the test with the exit code and stderr.
func runFightScore(t *testing.T, args ...string) (string, string) {
	t.Helper()
	code, out, errOut := runFightScoreRaw(t, args...)
	if code != exitOK {
		t.Fatalf("fight-score %v: exit %d: %s", args, code, errOut)
	}
	return out, errOut
}

// scoreResult is the reading as --json writes it.
type scoreResult struct {
	Fight       string             `json:"fight"`
	SpellOracle string             `json:"spell_oracle"`
	Oracle      []kbspells.Finding `json:"oracle"`
}

// scoreJSON reads the --json reading.
func scoreJSON(t *testing.T, args ...string) scoreResult {
	t.Helper()
	out, _ := runFightScore(t, append([]string{"--json"}, args...)...)
	var r scoreResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	return r
}

// scoreOracle reads the --json reading's oracle findings.
func scoreOracle(t *testing.T, args ...string) []kbspells.Finding {
	t.Helper()
	return scoreJSON(t, args...).Oracle
}

func findingFor(t *testing.T, fs []kbspells.Finding, bot string) kbspells.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Bot == bot {
			return f
		}
	}
	t.Fatalf("no oracle finding for %s in %+v", bot, fs)
	return kbspells.Finding{}
}

// TestFightScoreForeverClasses scores one recorded Forever fight per class with the Forever spell data: the class's bot is
// judged ok under its Forever spec, and the spell it casts that only the Forever oracle knows is on its list rather than
// off it. The Forever-only name's ids are the Forever table's own, so the rows read Forever names/ids rather than Turtle ones.
func TestFightScoreForeverClasses(t *testing.T) {
	table := foreverspells.Default()
	if !table.Loaded() {
		t.Fatal("no Forever spell table in this build")
	}
	for _, c := range foreverClasses {
		t.Run(c.class, func(t *testing.T) {
			path := foreverFightPath(c.class)
			f := findingFor(t, scoreOracle(t, "--set", "spell_oracle=forever", path), c.bot)
			if f.Level != "ok" || f.Spec != c.spec {
				t.Fatalf("%s: not judged ok as %s: %+v", c.bot, c.spec, f)
			}
			if len(f.Missing) != 0 || len(f.Never) != 0 {
				t.Fatalf("%s: Forever lists not used: %+v", c.bot, f)
			}
			if slices.Contains(f.OffList, c.only) {
				t.Fatalf("%s: %s is off the Forever lists: %+v", c.bot, c.only, f)
			}
			if !slices.Contains(table.IDsByName(c.only), c.onlyID) {
				t.Fatalf("%s: Forever ids %v lack %d", c.only, table.IDsByName(c.only), c.onlyID)
			}
			// The printed score rows name the fight's Forever spells: the tank's Sunder Armor row is the debugger's reading.
			text, _ := runFightScore(t, "--set", "spell_oracle=forever", path)
			if l := scoreLine(t, text, "mt_sunder"); !strings.Contains(l, "Sunder Armor 8x") || !strings.Contains(l, "2.0/10 s") {
				t.Fatalf("%s: mt_sunder: %q", c.class, l)
			}
			if l := scoreLine(t, text, "bot(s) judged ok"); strings.Contains(l, "0 bot(s) judged ok") {
				t.Fatalf("%s: summary: %q", c.class, l)
			}
		})
	}
}

// TestFightScoreForeverRows proves the printed rows come from the Forever data: the paladin off-tank's Turtle-only taunt counts
// for nothing under the Forever taunt list (a warning), while the legacy Turtle record without the option still reads it as a
// taunt (TestFightScoreWalk2Parity pins that path).
func TestFightScoreForeverRows(t *testing.T) {
	path := foreverFightPath("paladin")
	out, _ := runFightScore(t, "--set", "spell_oracle=forever", "--ot", "Palatank", path)
	if l := scoreLine(t, out, "ot_taunt", "Palatank"); !strings.Contains(l, "warn") || !strings.Contains(l, "no taunt") || !strings.Contains(l, "0x") {
		t.Fatalf("Forever ot_taunt: %q", l)
	}
	if l := scoreLine(t, out, "mt_sunder"); !strings.Contains(l, "ok") || !strings.Contains(l, "2.0/10 s") || !strings.Contains(l, "Sunder Armor 8x") {
		t.Fatalf("Forever mt_sunder: %q", l)
	}
}

// moduleRecordForms writes one fixture's dashboard detail ({fight, warnings}), bare fight object and COMBAT_FIGHT datagram
// into a temp dir and returns the three paths, detail first.
func moduleRecordForms(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Fight json.RawMessage `json:"fight"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Fight) == 0 {
		t.Fatalf("%s: %v", path, err)
	}
	dir := t.TempDir()
	bare := filepath.Join(dir, "fight.json")
	if err := os.WriteFile(bare, env.Fight, 0o600); err != nil {
		t.Fatal(err)
	}
	// The COMBAT_FIGHT datagram the module sends and the daemon stores: the fight under the same key, beside its envelope.
	datagram, err := json.Marshal(map[string]json.RawMessage{
		"v": {0x31}, "session": {0x31}, "seq": {0x31}, "ts": {0x31}, "type": json.RawMessage(`"COMBAT_FIGHT"`), "fight": env.Fight,
	})
	if err != nil {
		t.Fatal(err)
	}
	dgram := filepath.Join(dir, "datagram.json")
	if err := os.WriteFile(dgram, datagram, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{path, bare, dgram}
}

// TestFightScoreForeverModuleRecord: the module's fight record is read in both forms it is stored in — the dashboard detail
// ({fight, warnings}) and the history file's bare fight line — with the same Forever reading.
func TestFightScoreForeverModuleRecord(t *testing.T) {
	forms := moduleRecordForms(t, foreverFightPath("rogue"))
	detail := scoreOracle(t, "--set", "spell_oracle=forever", forms[0])
	for _, other := range forms[1:] {
		if line := scoreOracle(t, "--set", "spell_oracle=forever", other); !reflect.DeepEqual(detail, line) {
			t.Fatalf("%s and the detail differ:\n%+v\n%+v", filepath.Base(other), detail, line)
		}
	}
}

// TestFightScoreModuleRecordNeedsOracle: the module's fight record format is itself the Forever mark — no core writes a
// build/core/source field into a fight, but the module's record carries the per-bot tail_spells (and, on newer emitters,
// boss_phase/boss_phase_entry and dropped) the legacy Turtle dump does not. Every module fixture scored without
// spell_oracle=forever is refused with a message that says how to select it, and no partial reading is printed.
func TestFightScoreModuleRecordNeedsOracle(t *testing.T) {
	for _, c := range foreverClasses {
		t.Run(c.class, func(t *testing.T) {
			path := foreverFightPath(c.class)
			code, out, errOut := runFightScoreRaw(t, path)
			if code != exitUsage {
				t.Fatalf("%s: exit %d, want %d (a refusal): %s", path, code, exitUsage, out)
			}
			if strings.TrimSpace(out) != "" {
				t.Fatalf("%s: printed a reading without the oracle:\n%s", path, out)
			}
			if !strings.Contains(errOut, "spell_oracle=forever") || !strings.Contains(errOut, "module") {
				t.Fatalf("%s: stderr does not tell the user to select the oracle: %q", path, errOut)
			}
			// A numeric spell_oracle alone still selects the Turtle data: the module record is refused the same way.
			if code, _, errOut := runFightScoreRaw(t, "--set", "spell_oracle=30", path); code != exitUsage || !strings.Contains(errOut, "spell_oracle=forever") {
				t.Fatalf("%s with only a seconds threshold: exit %d, stderr %q", path, code, errOut)
			}
		})
	}
	// The wrapper is not the format: the bare fight object and the COMBAT_FIGHT datagram are the same module record and
	// are refused without the option too.
	for _, form := range moduleRecordForms(t, foreverFightPath("rogue"))[1:] {
		if code, _, errOut := runFightScoreRaw(t, form); code != exitUsage || !strings.Contains(errOut, "spell_oracle=forever") {
			t.Fatalf("%s: exit %d, stderr %q", filepath.Base(form), code, errOut)
		}
	}
}

// TestFightScoreLegacyTurtleWarnsUnderForever: the legacy Turtle fight record — no module keys — scored with
// spell_oracle=forever still scores, but says so on stderr and names the oracle in the reading; the Forever taunt list
// really judged it (the paladin off-tank's Hand of Reckoning reads as no taunt).
func TestFightScoreLegacyTurtleWarnsUnderForever(t *testing.T) {
	path := "testdata/fights/combat-fight-5-3.json"
	code, out, errOut := runFightScoreRaw(t, "--set", "spell_oracle=forever", "--mt", "Ragemeister", "--ot", "Brun", path)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"warning:", "legacy Turtle", "forever"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr lacks %q: %q", want, errOut)
		}
	}
	if header := scoreLines(out)[0]; !strings.Contains(header, "spell oracle forever") {
		t.Fatalf("text header does not name the oracle: %q", header)
	}
	if l := scoreLine(t, out, "ot_taunt", "Brun"); !strings.Contains(l, "warn") || !strings.Contains(l, "no taunt") {
		t.Fatalf("Forever ot_taunt on the legacy record: %q", l)
	}
	if r := scoreJSON(t, "--set", "spell_oracle=forever", path); r.SpellOracle != "forever" {
		t.Fatalf("--json spell_oracle: %q", r.SpellOracle)
	}
}

// TestFightScoreNamesOracle: every reading names the oracle that produced it, in the JSON field and the text header.
func TestFightScoreNamesOracle(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"legacy turtle", []string{"testdata/fights/combat-fight-5-3.json"}, "turtle"},
		{"legacy turtle seconds", []string{"--set", "spell_oracle=30", "testdata/fights/combat-fight-5-3.json"}, "turtle"},
		{"module forever", []string{"--set", "spell_oracle=forever", foreverFightPath("warrior")}, "forever"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if r := scoreJSON(t, c.args...); r.SpellOracle != c.want {
				t.Fatalf("--json spell_oracle = %q, want %q", r.SpellOracle, c.want)
			}
			text, errOut := runFightScore(t, c.args...)
			if header := scoreLines(text)[0]; !strings.Contains(header, "spell oracle "+c.want) {
				t.Fatalf("text header does not name the oracle: %q", header)
			}
			if strings.Contains(errOut, "warning:") {
				t.Fatalf("unexpected warning on a matching record: %q", errOut)
			}
		})
	}
}

// TestFightScoreForeverKeepsSeconds: a numeric spell_oracle still sets the active-seconds threshold under the Forever oracle,
// so a fight too short for a bot is not judged rather than scored against the wrong lists.
func TestFightScoreForeverKeepsSeconds(t *testing.T) {
	f := findingFor(t, scoreOracle(t, "--set", "spell_oracle=forever", "--set", "spell_oracle=100", foreverFightPath("mage")), "Magebot")
	if f.Level != "na" || !strings.Contains(f.Text, "not judged: 40 s active of 100 s") {
		t.Fatalf("Magebot: %+v", f)
	}
}
