package main

// forever dungeon-play: the dry run of Ragefire Chasm, the flag checks before any network, the verdict's rules and
// what the config carries from the flags.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/spelljudge"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

const playBotsArg = "Botone,Bottwo,Botthree,Botfour"

func runPlayCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"dungeon-play"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func rfcPlan() string { return filepath.Join("..", "..", "dungeons", "ragefire_chasm.json") }

// TestDungeonPlayDryClearsRFC is the round's first case: the whole of Ragefire Chasm on the scripted world through
// the command, a PASS verdict, and the files a run leaves.
func TestDungeonPlayDryClearsRFC(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runPlayCmd(t, "--dry", "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero",
		"--out", out, "--map")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for _, want := range []string{"play: stop clear", "clear=true", "gm_lines=0", "judge: ", "verdict: PASS"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output has no %q:\n%s", want, stdout)
		}
	}
	sums, _ := filepath.Glob(filepath.Join(out, "*.forever-run.json"))
	if len(sums) != 1 {
		t.Fatalf("summaries = %v; want one", sums)
	}
	raw, err := os.ReadFile(sums[0])
	if err != nil {
		t.Fatal(err)
	}
	var sum driver.RunSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	if !sum.Clear || sum.Mode != "play" || sum.Play == nil || sum.Play.GMLines != 0 || sum.Play.Minutes <= 0 {
		t.Fatalf("summary: clear=%v mode=%q play=%+v", sum.Clear, sum.Mode, sum.Play)
	}
	if sum.Play.MaxMinutes != 90 || sum.Play.MaxWipes != 2 {
		t.Fatalf("budget = %d min, %d wipes; want Ragefire Chasm's 90 and 2", sum.Play.MaxMinutes, sum.Play.MaxWipes)
	}
	if svgs, _ := filepath.Glob(filepath.Join(out, "*.svg")); len(svgs) != 1 {
		t.Fatalf("map files = %v; want the run's svg (stdout %s)", svgs, stdout)
	}
	if cards, _ := filepath.Glob(filepath.Join(out, "*card*")); len(cards) == 0 {
		t.Fatalf("no report card in %s", out)
	}
	// The verdict never names a character.
	for _, name := range strings.Split(playBotsArg, ",") {
		if strings.Contains(stdout, name) {
			t.Fatalf("output names %q:\n%s", name, stdout)
		}
	}
}

// TestDungeonPlayTimeStopFails: a run cut short by its time budget is not a clear and the summary says so.
func TestDungeonPlayTimeStopFails(t *testing.T) {
	out := t.TempDir()
	code, stdout, _ := runPlayCmd(t, "--dry", "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero",
		"--out", out, "--max-minutes", "1")
	if code == 0 {
		t.Fatalf("exit 0 for a run stopped by its time:\n%s", stdout)
	}
	if !strings.Contains(stdout, "clear=false") || !strings.Contains(stdout, "verdict: FAIL") || !strings.Contains(stdout, "not-clear") {
		t.Fatalf("output:\n%s", stdout)
	}
	sums, _ := filepath.Glob(filepath.Join(out, "*.forever-run.json"))
	if len(sums) != 1 {
		t.Fatalf("summaries = %v", sums)
	}
	raw, _ := os.ReadFile(sums[0])
	var sum driver.RunSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Clear {
		t.Fatalf("the summary says clear for a stop %q", sum.StopReason)
	}
}

func TestDungeonPlayFlagErrors(t *testing.T) {
	base := []string{"--dry", "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero"}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	cases := map[string][]string{
		"no plan":      {"--dry", "--bots", playBotsArg, "--character", "Hero"},
		"no bots":      {"--dry", "--plan", rfcPlan(), "--character", "Hero"},
		"no character": {"--dry", "--plan", rfcPlan(), "--bots", playBotsArg},
		"three bots":   {"--dry", "--plan", rfcPlan(), "--bots", "A,B,C", "--character", "Hero"},
		"five bots":    {"--dry", "--plan", rfcPlan(), "--bots", "A,B,C,D,E", "--character", "Hero"},
		"same bot":     {"--dry", "--plan", rfcPlan(), "--bots", "A,B,C,A", "--character", "Hero"},
		"wipes 0":      with("--max-wipes", "0"),
		"wipes 21":     with("--max-wipes", "21"),
		"minutes 1441": with("--max-minutes", "1441"),
		"minutes -1":   with("--max-minutes", "-1"),
		"map no out":   with("--map"),
		"min-active":   with("--min-active", "-1"),
		"stray arg":    with("extra"),
	}
	for name, args := range cases {
		if code, _, _ := runPlayCmd(t, args...); code != exitUsage {
			t.Errorf("%s: exit %d; want %d", name, code, exitUsage)
		}
	}
	if code, _, stderr := runPlayCmd(t, "--dry", "--plan", filepath.Join(t.TempDir(), "nope.json"), "--bots", playBotsArg,
		"--character", "Hero"); code != driver.ExitPlanRefused || !strings.Contains(stderr, "the plan is refused") {
		t.Errorf("a missing plan: exit %d stderr %q", code, stderr)
	}
	// A raid plan is refused by the player route before any network.
	raid := filepath.Join("..", "..", "dungeons", "blackwing_lair.json")
	if code, _, stderr := runPlayCmd(t, "--dry", "--plan", raid, "--bots", playBotsArg, "--character", "Hero"); code != driver.ExitPlanRefused {
		t.Errorf("a raid plan: exit %d stderr %q", code, stderr)
	}
	// Live mode without a login is a usage error, and nothing is sent.
	if code, _, _ := runPlayCmd(t, "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero"); code != exitUsage {
		t.Errorf("live without a login: exit %d", code)
	}
}

// TestPlayConfigCarriesFlags: --max-wipes and --max-minutes reach the run's config (a flag that is read and dropped
// would give every run the default budget).
func TestPlayConfigCarriesFlags(t *testing.T) {
	cfg := playConfig("ragefire_chasm", [4]string{"A", "B", "C", "D"}, 45, 5, "", true, "out", "id")
	if cfg.MaxWipes != 5 || cfg.MaxMinutes != 45 || !cfg.Disband || cfg.Out != "out" || cfg.Bots[3] != "D" || cfg.RunID != "id" {
		t.Fatalf("config = %+v", cfg)
	}
}

func okJudge(roles ...string) []spelljudge.Finding {
	var fs []spelljudge.Finding
	for _, r := range roles {
		fs = append(fs, spelljudge.Finding{Finding: kbspells.Finding{Bot: r, Role: r, Level: "ok"}})
	}
	return fs
}

func goodPlaySummary() driver.RunSummary {
	return driver.RunSummary{
		Clear: true, Wipes: 1, DurationS: 80 * 60, StopReason: "clear",
		Play: &driver.PlayRecord{Minutes: 80, MaxMinutes: 90, MaxWipes: 2, CombatRows: 900, LinesByPrefix: map[string]int{".bot add": 4}},
	}
}

func TestPlayVerdictRules(t *testing.T) {
	four := okJudge("tank", "heal", "dps1", "dps2")
	cases := []struct {
		name   string
		mut    func(*driver.RunSummary)
		judge  []spelljudge.Finding
		cap    int
		want   bool
		reason string
	}{
		{"all good", nil, four, 90, true, ""},
		{"not clear", func(s *driver.RunSummary) { s.Clear = false; s.StopReason = "wipe-budget" }, four, 90, false, "not-clear"},
		{"third wipe", func(s *driver.RunSummary) { s.Wipes = 3 }, four, 90, false, "wipes"},
		{"two wipes pass", func(s *driver.RunSummary) { s.Wipes = 2 }, four, 90, true, ""},
		{"over the milestone time", func(s *driver.RunSummary) { s.Play.Minutes = 90.5 }, four, 90, false, "minutes"},
		{"a longer cap is the plan's own", func(s *driver.RunSummary) { s.Play.Minutes = 100 }, four, 150, true, ""},
		{"a gm line", func(s *driver.RunSummary) { s.Play.GMLines = 1 }, four, 90, false, "gm-lines"},
		{"no play record", func(s *driver.RunSummary) { s.Play = nil }, four, 90, false, "no-play-record"},
		{"a bot fails the judge", nil, append(okJudge("tank", "heal", "dps1"), spelljudge.Finding{Finding: kbspells.Finding{Bot: "dps2", Role: "dps", Level: "fail", Text: "wrong class"}}), 90, false, "judge"},
		{"a bot was not judged", nil, append(okJudge("tank", "heal", "dps1"), spelljudge.Finding{Finding: kbspells.Finding{Bot: "dps2", Role: "dps", Level: "na"}}), 90, false, "judge"},
		{"only three bots judged", nil, okJudge("tank", "heal", "dps1"), 90, false, "judge"},
		{"no findings at all", nil, nil, 90, false, "judge"},
		{"combat rows dropped", func(s *driver.RunSummary) { s.Play.CombatDropped = 3 }, four, 90, false, "dropped"},
		{"combat log truncated", func(s *driver.RunSummary) { s.Play.CombatTruncated = true }, four, 90, false, "truncated"},
		{"no combat rows at all", func(s *driver.RunSummary) { s.Play.CombatRows = 0 }, four, 90, false, "no rows"},
		{"combat write failed", func(s *driver.RunSummary) { s.Play.CombatError = "write: disk full" }, four, 90, false, "write failed"},
	}
	for _, tc := range cases {
		sum := goodPlaySummary()
		if tc.mut != nil {
			tc.mut(&sum)
		}
		// The wipe budget the run ran under never loosens the milestone's two.
		got, reasons := playVerdict(sum, tc.judge, true, tc.cap)
		if got != tc.want {
			t.Errorf("%s: pass = %v (%v); want %v", tc.name, got, reasons, tc.want)
		}
		if tc.reason != "" && !strings.Contains(strings.Join(reasons, " "), tc.reason) {
			t.Errorf("%s: reasons %v do not name %q", tc.name, reasons, tc.reason)
		}
	}
	// A dry run has no live combat log: the judge is skipped and the verdict stands on the rest.
	sum := goodPlaySummary()
	if got, _ := playVerdict(sum, nil, false, 90); !got {
		t.Error("a dry run with the judge skipped failed")
	}
}

// TestDungeonPlayJudgesItsOwnTrace: a folder that holds another run, newer by name and with no combat log, does not
// change the verdict of the run just played: the judge reads the trace the run wrote, not the newest one in the folder.
func TestDungeonPlayJudgesItsOwnTrace(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runPlayCmd(t, "--dry", "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	traces, _ := filepath.Glob(filepath.Join(out, "r-*.jsonl"))
	var own string
	for _, f := range traces {
		if !strings.HasSuffix(f, ".combatlog.jsonl") {
			own = f
		}
	}
	if own == "" {
		t.Fatalf("no trace in %s", out)
	}
	// A later run in the same folder: the same trace and summary, an empty combat log.
	stem := strings.TrimSuffix(own, ".jsonl")
	other := filepath.Join(out, "r-99991231T235959-ffff")
	for _, ext := range []string{".jsonl", ".forever-run.json"} {
		raw, err := os.ReadFile(stem + ext)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(other+ext, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(other+".combatlog.jsonl", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mine, err := playJudge(own, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 4 {
		t.Fatalf("own findings = %v", mine)
	}
	for _, f := range mine {
		if f.Level == "na" || f.Level == "fail" || f.Casts == 0 {
			t.Fatalf("own trace: %s is %s with %d casts (%s)", f.Bot, f.Level, f.Casts, f.Text)
		}
	}
	// The folder's newest run is the other one: reading by folder would have judged its empty combat log.
	byDir, err := playJudge(out, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	empty := 0
	for _, f := range byDir {
		if f.Casts == 0 {
			empty++
		}
	}
	if empty == 0 {
		t.Fatalf("the folder read judged %v; want the other run's empty log to show (the test's trap is not set)", byDir)
	}
}

// TestPlayCmdLastLegChecksTheName: --last-leg is checked against the plan's legs before anything is sent, and the
// usage message lists them.
func TestPlayCmdLastLegChecksTheName(t *testing.T) {
	route, err := driver.LoadPlayRoute(rfcPlan())
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runPlayCmd(t, "--dry", "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero", "--last-leg", "nowhere")
	if code != exitUsage {
		t.Fatalf("exit %d stderr %q; want a usage error", code, stderr)
	}
	if !strings.Contains(stderr, "nowhere") || !strings.Contains(stderr, route.Legs[0].Name) || !strings.Contains(stderr, route.Legs[len(route.Legs)-1].Name) {
		t.Fatalf("stderr = %q; want it to name the bad leg and list every leg", stderr)
	}
}

// TestPlayCmdLastLegDry: a dry run cut at a leg clears once that leg's boss is down.
func TestPlayCmdLastLegDry(t *testing.T) {
	route, err := driver.LoadPlayRoute(rfcPlan())
	if err != nil {
		t.Fatal(err)
	}
	name := route.Legs[3].Name
	code, stdout, stderr := runPlayCmd(t, "--dry", "--plan", rfcPlan(), "--bots", playBotsArg, "--character", "Hero", "--last-leg", name)
	// A dry run without --out has no record to judge, so the verdict fails; the run's own clear is what the flag is
	// about, and the single BOSS state is the proof that the legs after it were not walked.
	if !strings.Contains(stdout, "clear=true") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if n := strings.Count(stdout, " BOSS "); n != 1 {
		t.Fatalf("the run entered %d boss states; want one (the cut leg's)\n%s", n, stdout)
	}
}

// raidSuitePlan is a twenty-member raid plan the player route takes: every leg of Timbermaw Hold is a pack and a boss, with
// no timed stand pull, hold or ambush (the Zul'Gurub plan's stand pulls are refused by the player route).
func raidSuitePlan() string { return filepath.Join("..", "..", "dungeons", "timbermaw_hold.json") }

// raidFlags are a twenty-member raid group's flags: two tanks, three healers and fourteen damage dealers. Every name is
// letters only, as the server's name rule wants.
func raidFlags() []string {
	names := func(prefix string, n int) string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("%s%c", prefix, 'a'+i))
		}
		return strings.Join(out, ",")
	}
	return []string{"--tanks", names("Raidtank", 2), "--healers", names("Raidheal", 3), "--dps", names("Raiddps", 14)}
}

// TestDungeonPlayRaidFlags: the raid flags exclude --bots and each other, and a dry run of a twenty-member raid plan
// with a twenty-member group stops at its first boss and passes the raid's verdict.
func TestDungeonPlayRaidFlags(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"bots and the raid flags", append([]string{"--dry", "--plan", raidSuitePlan(), "--character", "Hero", "--bots", playBotsArg}, raidFlags()...), "exclude each other"},
		{"layout alone", []string{"--dry", "--plan", raidSuitePlan(), "--character", "Hero", "--layout", filepath.Join(t.TempDir(), "layout.txt")}, "names --tanks, --healers and --dps"},
		{"one damage dealer", []string{"--dry", "--plan", raidSuitePlan(), "--character", "Hero", "--tanks", "Raidtanka,Raidtankb", "--healers", "Raidheala", "--dps", "Raiddpsa"}, "at least 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr := runPlayCmd(t, c.args...)
			if code != exitUsage {
				t.Fatalf("exit %d stderr %q; want a usage error", code, stderr)
			}
			if !strings.Contains(stderr, c.want) {
				t.Fatalf("stderr = %q; want it to say %q", stderr, c.want)
			}
		})
	}
	// A raid plan through --bots is the five-bot route's refusal, before anything is sent.
	if code, _, stderr := runPlayCmd(t, "--dry", "--plan", raidSuitePlan(), "--bots", playBotsArg, "--character", "Hero"); code != driver.ExitPlanRefused {
		t.Fatalf("exit %d stderr %q; want the plan refused", code, stderr)
	}
	// The dry raid: the whole machine over the scripted world, stopping where the raid's own watch takes over. --out
	// writes the run's record, and the raid's verdict reads it.
	args := append([]string{"--dry", "--plan", raidSuitePlan(), "--character", "Hero", "--out", t.TempDir()}, raidFlags()...)
	code, stdout, stderr := runPlayCmd(t, args...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	for _, want := range []string{"play: stop raid-boss-reached", "clear=false", "gm_lines=0", "verdict: PASS"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output has no %q:\n%s", want, stdout)
		}
	}
}
