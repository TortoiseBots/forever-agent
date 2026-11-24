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

// runFightScore runs the command and returns its output, or fails the test with the exit code and stderr.
func runFightScore(t *testing.T, args ...string) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(context.Background(), append([]string{"fight-score"}, args...), &out, &errOut); code != exitOK {
		t.Fatalf("fight-score %v: exit %d: %s", args, code, errOut.String())
	}
	return out.String(), errOut.String()
}

// scoreOracle reads the --json reading's oracle findings.
func scoreOracle(t *testing.T, args ...string) []kbspells.Finding {
	t.Helper()
	out, _ := runFightScore(t, append([]string{"--json"}, args...)...)
	var r struct {
		Oracle []kbspells.Finding `json:"oracle"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	return r.Oracle
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
// for nothing under the Forever taunt list (a warning), while the same fight without the option still reads it as a taunt.
func TestFightScoreForeverRows(t *testing.T) {
	path := foreverFightPath("paladin")
	out, _ := runFightScore(t, "--set", "spell_oracle=forever", "--ot", "Palatank", path)
	if l := scoreLine(t, out, "ot_taunt", "Palatank"); !strings.Contains(l, "warn") || !strings.Contains(l, "no taunt") || !strings.Contains(l, "0x") {
		t.Fatalf("Forever ot_taunt: %q", l)
	}
	if l := scoreLine(t, out, "mt_sunder"); !strings.Contains(l, "ok") || !strings.Contains(l, "2.0/10 s") || !strings.Contains(l, "Sunder Armor 8x") {
		t.Fatalf("Forever mt_sunder: %q", l)
	}

	// The Turtle path is unchanged when the option is not set: the same row names the Turtle taunt.
	out, _ = runFightScore(t, "--ot", "Palatank", path)
	if l := scoreLine(t, out, "ot_taunt", "Palatank"); !strings.Contains(l, "ok") || !strings.Contains(l, "Hand of Reckoning 2x") {
		t.Fatalf("Turtle ot_taunt: %q", l)
	}
}

// TestFightScoreForeverTurtleOracleOffList pins that the same Forever fight read without the option is still judged by the
// Turtle data: the Forever-only spell of every class is off the Turtle bot's lists, which the Forever run does not report.
func TestFightScoreForeverTurtleOracleOffList(t *testing.T) {
	for _, c := range foreverClasses {
		t.Run(c.class, func(t *testing.T) {
			f := findingFor(t, scoreOracle(t, foreverFightPath(c.class)), c.bot)
			if !slices.Contains(f.OffList, c.only) {
				t.Fatalf("%s: Turtle oracle: off-list %v lacks %q: %+v", c.bot, f.OffList, c.only, f)
			}
		})
	}
}

// TestFightScoreForeverModuleRecord: the module's fight record is read in both forms it is stored in — the dashboard detail
// ({fight, warnings}) and the history file's bare fight line — with the same Forever reading.
func TestFightScoreForeverModuleRecord(t *testing.T) {
	path := foreverFightPath("rogue")
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
	detail := scoreOracle(t, "--set", "spell_oracle=forever", path)
	for _, other := range []string{bare, dgram} {
		if line := scoreOracle(t, "--set", "spell_oracle=forever", other); !reflect.DeepEqual(detail, line) {
			t.Fatalf("%s and the detail differ:\n%+v\n%+v", filepath.Base(other), detail, line)
		}
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
