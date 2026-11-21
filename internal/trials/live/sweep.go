package live

// sweep.go: `trials sweep --classes warrior,priest,mage,rogue`. A class
// sweep runs the trials whose hired bot class is asked, tanks first, one
// repeat per trial, and writes one card per trial plus sweep.md. A class
// whose trials are not asked is skipped BY FLAG, never silently dropped:
// druid, paladin, shaman, hunter and warlock join the sweep as their TB
// slices land.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// SweepTrial is one planned trial of a class sweep.
type SweepTrial struct {
	// Arg is what RunTrial is asked for (the trial id unless a test runs a
	// trial file); empty means ID.
	Arg       string
	ID        string
	Class     string
	Role      string
	Title     string
	Situation string
	Trial     map[string]any
}

// SweepSkip is a trial the classes did not ask for, with the reason.
type SweepSkip struct {
	ID     string
	Class  string
	Reason string
}

// SweepPlan is the sweep's order and its skips: the asked trials, tanks
// first (a tank trial's fights are the ones a class problem shows in first),
// then the rest, each group by id; and every trial whose class is not asked.
func SweepPlan(classes []string, trials []map[string]any, args []string) ([]SweepTrial, []SweepSkip) {
	want := map[string]bool{}
	for _, c := range classes {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			want[c] = true
		}
	}
	matches := func(id string) bool {
		if len(args) == 0 {
			return true
		}
		for _, a := range args {
			if a == id {
				return true
			}
			if ok, _ := matchGlob(a, id); ok {
				return true
			}
		}
		return false
	}
	var picked []SweepTrial
	var skipped []SweepSkip
	for _, trial := range trials {
		id := pyjson.Str(trial["id"])
		if id == "" {
			continue
		}
		class := trialClass(trial)
		role := ""
		if bot, _ := pyjson.Object(trial["bot"]); bot != nil {
			role = pyjson.Str(bot["role"])
		}
		if !want[class] {
			skipped = append(skipped, SweepSkip{ID: id, Class: class,
				Reason: "class " + orDash(class) + " not requested (join by flag)"})
			continue
		}
		if !matches(id) {
			skipped = append(skipped, SweepSkip{ID: id, Class: class, Reason: "not matched by the trial arguments"})
			continue
		}
		situation := ""
		if sit, _ := pyjson.Object(trial["situation"]); sit != nil {
			situation = pyjson.Str(sit["kind"])
			if situation == "" {
				situation = "tank_damage"
			}
		}
		picked = append(picked, SweepTrial{ID: id, Class: class, Role: role,
			Title: pyjson.Str(trial["title"]), Situation: situation, Trial: trial})
	}
	sort.SliceStable(picked, func(i, j int) bool {
		ti, tj := picked[i].Role == "tank", picked[j].Role == "tank"
		if ti != tj {
			return ti
		}
		return picked[i].ID < picked[j].ID
	})
	sort.SliceStable(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })
	return picked, skipped
}

// trialClass is the hired bot's class (the class a sweep asks about); a
// trial without a bot falls back to the player's class.
func trialClass(trial map[string]any) string {
	if bot, _ := pyjson.Object(trial["bot"]); bot != nil {
		if c := strings.ToLower(pyjson.Str(bot["class"])); c != "" {
			return c
		}
	}
	if player, _ := pyjson.Object(trial["player"]); player != nil {
		return strings.ToLower(pyjson.Str(player["class"]))
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// matchGlob is a small `*`/`?` matcher (no import of path/filepath so the
// id pattern stays platform-neutral).
func matchGlob(pattern, s string) (bool, error) {
	var rec func(p, t string) bool
	rec = func(p, t string) bool {
		for len(p) > 0 {
			switch p[0] {
			case '*':
				for len(p) > 0 && p[0] == '*' {
					p = p[1:]
				}
				if p == "" {
					return true
				}
				for i := 0; i <= len(t); i++ {
					if rec(p, t[i:]) {
						return true
					}
				}
				return false
			case '?':
				if t == "" {
					return false
				}
				p, t = p[1:], t[1:]
			default:
				if t == "" || p[0] != t[0] {
					return false
				}
				p, t = p[1:], t[1:]
			}
		}
		return t == ""
	}
	return rec(pattern, s), nil
}

// SweepRow is one sweep.md row.
type SweepRow struct {
	Trial   SweepTrial
	Verdict string
	Reason  string
	Card    string
}

// SweepMarkdown renders sweep.md: the order, one row per trial with its
// verdict and card path, and the trials the class flag skipped.
func SweepMarkdown(rows []SweepRow, skipped []SweepSkip) string {
	var b strings.Builder
	b.WriteString("# Trials sweep\n\n")
	b.WriteString("Tanks first, then the rest, each group by trial id; one repeat and one card per trial.\n\n")
	b.WriteString("| # | trial | class | role | situation | verdict | notes | card |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for i, row := range rows {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s | %s |\n",
			i+1, row.Trial.ID, orDash(row.Trial.Class), orDash(row.Trial.Role),
			orDash(row.Trial.Situation), orDash(row.Verdict), mdCell(row.Reason), row.Card)
	}
	if len(skipped) > 0 {
		b.WriteString("\nSkipped by the class flag:\n\n")
		for _, s := range skipped {
			fmt.Fprintf(&b, "- %s (%s): %s\n", s.ID, orDash(s.Class), s.Reason)
		}
	}
	return b.String()
}

// TrialDirOf is the dir RunTrial writes one trial's repeats and scored
// trial.json into: Out/<id>. The runner and the sweep's reader both go
// through it, so they can never name two different paths.
func TrialDirOf(out, id string) string { return joinPath(out, id) }

// TrialJSONPath is the scored trial file RunTrial leaves for id under out.
func TrialJSONPath(out, id string) string { return joinPath(TrialDirOf(out, id), "trial.json") }

// RunJSONPath is the run record RunTrial rewrites under out.
func RunJSONPath(out string) string { return joinPath(out, "RUN.json") }

// TrialResult reads one trial's result from the paths RunTrial wrote under
// out (the runner's Out, RUN.json there and trial.json under Out/<id>) and
// returns the sweep's single verdict: SKIPPED from RUN.json, INVALID when a
// repeat was invalid (its reason is the first invalid repeat's), FAIL when a
// rule failed, PASS only when at least one rule passed and none was
// INCONCLUSIVE or INVALID, else INCONCLUSIVE. A file that is missing or
// unreadable is INCONCLUSIVE with that reason: never PASS.
func TrialResult(out, id string) (verdict, reason string) {
	run := map[string]any{}
	if raw, err := readFile(RunJSONPath(out)); err == nil {
		if doc, derr := pyjson.Unmarshal(raw); derr == nil {
			run, _ = pyjson.Object(doc)
		}
	}
	if skipped, ok := pyjson.Object(run["skipped"]); ok {
		for sid, why := range skipped {
			if pyjson.Truthy(why) {
				return "SKIPPED", sid + ": " + pyjson.Str(why)
			}
		}
	}
	rows, _ := pyjson.List(run["reps"])
	invalid, invalidWhy := false, ""
	for _, raw := range rows {
		m, _ := pyjson.Object(raw)
		if m == nil || pyjson.Str(m["trial"]) != id {
			continue
		}
		if !pyjson.Truthy(m["valid"]) {
			invalid = true
			if invalidWhy == "" {
				invalidWhy = pyjson.Str(m["ended"])
			}
		}
	}
	if invalid {
		if invalidWhy == "" {
			invalidWhy = "no valid repeat"
		}
		return "INVALID", invalidWhy
	}
	raw, err := readFile(TrialJSONPath(out, id))
	if err != nil {
		return "INCONCLUSIVE", "no scored trial file: " + trimLen(err.Error(), 120)
	}
	doc, derr := pyjson.Unmarshal(raw)
	if derr != nil {
		return "INCONCLUSIVE", "scored trial file unreadable: " + trimLen(derr.Error(), 120)
	}
	trial, _ := pyjson.Object(doc)
	fail, pass, open := false, false, ""
	verdicts, _ := pyjson.List(trial["verdicts"])
	for _, raw := range verdicts {
		m, _ := pyjson.Object(raw)
		switch pyjson.Str(m["verdict"]) {
		case "FAIL":
			fail = true
			if reason == "" {
				reason = pyjson.Str(m["id"]) + ": " + pyjson.Str(m["reason"])
			}
		case "PASS":
			pass = true
		case "INCONCLUSIVE", "INVALID":
			if open == "" {
				open = pyjson.Str(m["id"]) + " " + pyjson.Str(m["verdict"])
			}
		}
	}
	switch {
	case fail:
		return "FAIL", reason
	case open != "":
		return "INCONCLUSIVE", "rule not scored: " + open
	case pass:
		return "PASS", ""
	default:
		return "INCONCLUSIVE", "no rule was scored"
	}
}

// RunSweep runs the plan trial by trial, one run dir (base/<id>) and one
// runner each, and returns one row per planned trial: the ones that ran with
// their verdict, and every one that did not run as SKIPPED with why the
// sweep stopped. It stops at an interrupt, a lost session or a teardown that
// could not be proven (the next trial would start on a dirty slot).
func RunSweep(ctx context.Context, base string, plan []SweepTrial, repeats int,
	newRunner func(dir string) *Runner, logf func(string)) []SweepRow {
	if logf == nil {
		logf = func(string) {}
	}
	rows := make([]SweepRow, 0, len(plan))
	stopped := ""
	for _, st := range plan {
		card := joinPath(st.ID, "card.md")
		if stopped == "" && ctx.Err() != nil {
			stopped = "interrupted"
		}
		if stopped != "" {
			rows = append(rows, SweepRow{Trial: st, Verdict: "SKIPPED",
				Reason: "not run: sweep stopped (" + stopped + ")", Card: "-"})
			continue
		}
		dir := joinPath(base, st.ID)
		runner := newRunner(dir)
		arg := st.Arg
		if arg == "" {
			arg = st.ID
		}
		if _, err := runner.RunTrial(ctx, arg, repeats); err != nil {
			logf("trials sweep: " + st.ID + ": " + err.Error())
			rows = append(rows, SweepRow{Trial: st, Verdict: "INVALID", Reason: trimLen(err.Error(), 200), Card: card})
			var d *Disconnected
			var td *TeardownUnproven
			switch {
			case errors.As(err, &d):
				stopped = "session lost after " + st.ID
			case errors.As(err, &td):
				stopped = "teardown of " + st.ID + " not proven"
			case ctx.Err() != nil:
				stopped = "interrupted"
			}
			continue
		}
		if _, err := ScoreCard(dir); err != nil {
			logf("trials sweep: " + st.ID + ": score: " + err.Error())
			rows = append(rows, SweepRow{Trial: st, Verdict: "INVALID", Reason: trimLen(err.Error(), 200), Card: card})
			continue
		}
		verdict, reason := TrialResult(dir, st.ID)
		rows = append(rows, SweepRow{Trial: st, Verdict: verdict, Reason: reason, Card: card})
	}
	return rows
}

// SweepCounts tallies the rows by verdict.
func SweepCounts(rows []SweepRow) map[string]int {
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Verdict]++
	}
	return counts
}

// SweepExit is the sweep's exit code: 0 only when every planned trial ran and
// PASSed. A FAIL, an INVALID, an INCONCLUSIVE, a SKIPPED or an unrun trial all
// fail the sweep: none of them is a PASS.
func SweepExit(rows []SweepRow, planned int) int {
	if len(rows) != planned {
		return 1
	}
	for _, row := range rows {
		if row.Verdict != "PASS" {
			return 1
		}
	}
	return 0
}

// mdCell keeps a table cell on one line and makes a pipe safe.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "/")
	if s == "" {
		return "-"
	}
	return s
}
