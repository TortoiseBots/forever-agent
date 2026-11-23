// Package reportcard turns a watched run's record into a report card: what happened, per leg and per step, and
// the flags that find a weak spot without a human reading the log. The card is built from the same
// <run_id>.forever-run.json a watch writes (its `watch` block carries the per-leg evidence), so a saved record can
// be read offline and two records can be compared.
//
// The card's rules are its thresholds, all flags with defaults (DefaultThresholds): a step re-pulled more than
// Repulls times, one member with at least MemberDeaths deaths and two above the next, and a leg over SlowFactor times
// the plan's recorded time (only when the record carries one). A death the module's event did not name as a party
// member is flagged apart and counted for no member.
package reportcard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/driver"
)

// Schema and Version name the card's JSON, which later runs read and compare.
const (
	Schema  = "forever-report-card"
	Version = 1
)

// Thresholds are the card's rules. Every field is a flag with the default below; the JSON records the values the
// card was built with, so a comparison knows what each side measured.
type Thresholds struct {
	Repulls      int     `json:"repulls"`
	MemberDeaths int     `json:"member_deaths"`
	SlowFactor   float64 `json:"slow_factor"`
}

// DefaultThresholds is the card's table of defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{Repulls: 2, MemberDeaths: 3, SlowFactor: 2}
}

// Card is one run's report card: the run's verdict, its per-leg numbers, the deaths and pauses with their evidence,
// and the flags.
type Card struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Plan    string `json:"plan"`
	Mode    string `json:"mode"`
	// Verdict is one word (CLEARED, STOPPED or FAILED); VerdictText is the one-line sentence the brief asks for.
	Verdict     string     `json:"verdict"`
	VerdictText string     `json:"verdict_text"`
	Clear       bool       `json:"clear"`
	State       string     `json:"state"`
	StopReason  string     `json:"stop_reason"`
	Why         string     `json:"why,omitempty"`
	Leg         int        `json:"leg"`
	LegName     string     `json:"leg_name,omitempty"`
	// LastLeg is the leg the run ended on (the record's own field): a run cut with --last-leg ends here.
	LastLeg     string     `json:"last_leg,omitempty"`
	Step        int        `json:"step"`
	Steps       int        `json:"steps"`
	ElapsedS    float64    `json:"elapsed_s"`
	Pull        int        `json:"pull"`
	Pulls       int        `json:"pulls"`
	Deaths      int        `json:"deaths"`
	Wipes       int        `json:"wipes"`
	Pauses      int        `json:"pauses"`
	Rescues     int        `json:"rescues"`
	Thresholds  Thresholds `json:"thresholds"`
	Legs        []Leg      `json:"legs"`
	DeathLog    []Death    `json:"death_log,omitempty"`
	PauseLog    []Pause    `json:"pause_log,omitempty"`
	Flags       []Flag     `json:"flags"`
}

// Leg is one leg's numbers: its time, its pulls and re-pulls, its steps, and its deaths, wipes and pauses.
type Leg struct {
	Leg     int    `json:"leg"`
	Name    string `json:"name,omitempty"`
	Seconds int    `json:"seconds"`
	PlanS   int    `json:"plan_s,omitempty"`
	Slow    bool   `json:"slow,omitempty"`
	Pulls   int    `json:"pulls"`
	Repulls int    `json:"repulls"`
	Steps   int    `json:"steps"`
	Deaths  int    `json:"deaths"`
	Wipes   int    `json:"wipes"`
	Pauses  int    `json:"pauses"`
}

// Death is one death event the module sent. Who is the member's party slot; Why is the event's why= as it arrived,
// which for a death no member matches is the only name it carries. Recap is the parsed `.deathrecap` timeline the
// watch attached once when the death arrived (the core's own header, frame and cast lines); empty when the watch
// never asked.
type Death struct {
	AtS   int      `json:"at_s"`
	Leg   int      `json:"leg"`
	Step  int      `json:"step"`
	Unit  uint32   `json:"unit,omitempty"`
	Who   string   `json:"who,omitempty"`
	Why   string   `json:"why,omitempty"`
	Recap []string `json:"recap,omitempty"`
}

// Pause is one pause event with the module's reason and whether a resume cleared it.
type Pause struct {
	AtS     float64 `json:"at_s"`
	Leg     int     `json:"leg,omitempty"`
	Step    int     `json:"step,omitempty"`
	Why     string  `json:"why"`
	Cleared bool    `json:"cleared"`
}

// Flag is one weak spot. ID is stable across runs; Key names the step, member or leg it is about; Evidence holds the
// record lines that show it.
type Flag struct {
	ID       string   `json:"id"`
	Key      string   `json:"key"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

// Build makes the card from a run's summary. A summary without a watch block still gets a card: the verdict and the
// stop reason are the record's, and the per-leg sections stay empty.
func Build(sum driver.RunSummary, th Thresholds) Card {
	th = normalize(th)
	c := Card{
		Schema: Schema, Version: Version, RunID: sum.RunID, Plan: sum.Plan, Mode: sum.Mode,
		Clear: sum.Clear, StopReason: sum.StopReason, ElapsedS: sum.DurationS, Thresholds: th,
		LastLeg: sum.LastLeg, Legs: []Leg{}, Flags: []Flag{},
	}
	w := sum.Watch
	if w == nil {
		c.Deaths, c.Wipes, c.Pulls = sum.Deaths, sum.Wipes, len(sum.Pulls)
		c.Rescues = sum.Practice
		c.Flags = append(c.Flags, stopFlagFromRecord(c.StopReason, "", ""))
		c.Verdict, c.VerdictText = verdict(c)
		return c
	}
	c.State = w.State
	c.Why = w.Why
	c.ElapsedS = w.ElapsedS
	c.Leg, c.LegName, c.Step, c.Steps = w.Leg, w.LegName, w.Step, w.Steps
	c.Pull, c.Pulls = w.Pull, w.Pulls
	c.Deaths, c.Wipes, c.Pauses, c.Rescues = w.Deaths, w.Wipes, w.Pauses, w.Rescues
	c.DeathLog = make([]Death, 0, len(w.DeathLog))
	for _, d := range w.DeathLog {
		c.DeathLog = append(c.DeathLog, Death{AtS: d.AtS, Leg: d.Leg, Step: d.Step, Unit: d.Unit, Who: d.Who, Why: d.Why, Recap: append([]string(nil), d.Recap...)})
	}
	c.PauseLog = make([]Pause, 0, len(w.PauseLog))
	for _, p := range w.PauseLog {
		c.PauseLog = append(c.PauseLog, Pause{AtS: p.AtS, Leg: p.Leg, Step: p.Step, Why: p.Why, Cleared: p.Cleared})
	}
	c.Legs = buildLegs(w, th)
	c.Verdict, c.VerdictText = verdict(c)
	c.Flags = buildFlags(w, c.Legs, th, c.StopReason)
	return c
}

// normalize fills a zero threshold from the defaults: a card is always built with all three stated.
func normalize(th Thresholds) Thresholds {
	d := DefaultThresholds()
	if th.Repulls < 0 {
		th.Repulls = d.Repulls
	}
	if th.MemberDeaths <= 0 {
		th.MemberDeaths = d.MemberDeaths
	}
	if th.SlowFactor <= 0 {
		th.SlowFactor = d.SlowFactor
	}
	return th
}

// buildLegs copies the record's legs and adds the two numbers the card derives: the re-pulls of the leg's steps and
// whether the leg was slow against the plan's recorded time.
func buildLegs(w *driver.RunWatch, th Thresholds) []Leg {
	repulls := map[int]int{}
	for _, s := range w.StepLog {
		if s.Pulls > 1 {
			repulls[s.Leg] += s.Pulls - 1
		}
	}
	out := make([]Leg, 0, len(w.Legs))
	for _, l := range w.Legs {
		leg := Leg{
			Leg: l.Leg, Name: l.Name, Seconds: l.Seconds, PlanS: l.PlanS, Pulls: l.Pulls, Repulls: repulls[l.Leg],
			Steps: l.Steps, Deaths: l.Deaths, Wipes: l.Wipes, Pauses: l.Pauses,
		}
		leg.Slow = l.PlanS > 0 && float64(l.Seconds) > float64(l.PlanS)*th.SlowFactor
		out = append(out, leg)
	}
	return out
}

// failedReason says whether a stop reason is the run failing rather than stopping where it was. A budget, a wipe
// limit, the watchdog, a module stop and a spent rescue budget stop the run where it stands; a lost link, a blind
// watch, a refusal or an unfinished start fail it.
func failedReason(r string) bool {
	switch r {
	case "link", "blind", "config", "legs", "enter", "stuck", "state":
		return true
	}
	for _, p := range []string{"start:", "revive:", "resume:", "status:", "prepare:", "state:"} {
		if strings.HasPrefix(r, p) {
			return true
		}
	}
	return false
}

// verdict is the one-line verdict: CLEARED, PAUSED-STUCK AT <leg/step> (<pause reason>), STOPPED AT
// <leg/step> (<reason>) or FAILED (<reason>). A paused run the watch gave up on is not a run that stopped where it
// stood: its pause is what ended it, so it gets its own word.
func verdict(c Card) (string, string) {
	if c.Clear {
		return "CLEARED", "CLEARED"
	}
	if c.StopReason == driver.StopPausedStuck {
		// The record ends with the stop the watcher sent, whose reason is this stop reason; the pause that was not
		// cleared is in the pause log, and its reason is what a reader needs.
		why := c.Why
		if why == "" || why == c.StopReason {
			for i := len(c.PauseLog) - 1; i >= 0; i-- {
				if p := c.PauseLog[i]; !p.Cleared && p.Why != "" {
					why = p.Why
					break
				}
			}
		}
		text := "PAUSED-STUCK AT " + position(c.Leg, c.LegName, c.Step)
		if why != "" && why != c.StopReason {
			text += " (" + why + ")"
		}
		return "PAUSED-STUCK", text
	}
	reason := c.StopReason
	if reason == "" {
		reason = "no stop reason"
	}
	if failedReason(c.StopReason) {
		return "FAILED", "FAILED (" + reason + ")"
	}
	if c.State == "" && c.Steps == 0 {
		return "FAILED", "FAILED (" + reason + ")"
	}
	pos := position(c.Leg, c.LegName, c.Step)
	if c.Why != "" && c.Why != c.StopReason {
		reason += ": " + c.Why
	}
	return "STOPPED", "STOPPED AT " + pos + " (" + reason + ")"
}

// position is where the run was: `leg 2 "hall"/step 41`.
func position(leg int, name string, step int) string {
	if name != "" {
		return fmt.Sprintf("leg %d %q/step %d", leg, name, step)
	}
	return fmt.Sprintf("leg %d/step %d", leg, step)
}

// buildFlags applies every rule to the record. A flag's evidence is the record's own event lines, so a reader can
// check the rule without opening the log.
func buildFlags(w *driver.RunWatch, legs []Leg, th Thresholds, stopReason string) []Flag {
	var flags []Flag
	slow := map[int]bool{}
	for _, l := range legs {
		if l.Slow {
			slow[l.Leg] = true
		}
	}

	// A step re-pulled more than Repulls times.
	for _, s := range w.StepLog {
		repulls := s.Pulls - 1
		if repulls <= th.Repulls {
			continue
		}
		f := Flag{
			ID: "repulls", Key: fmt.Sprintf("leg:%d/step:%d", s.Leg, s.Step),
			Text: fmt.Sprintf("leg %d/step %d re-pulled %d times (limit %d)", s.Leg, s.Step, repulls, th.Repulls),
		}
		for _, p := range s.PullEvents {
			f.Evidence = append(f.Evidence,
				fmt.Sprintf("pull seq=%d t=%ds leg=%d step=%d", p.Seq, p.AtS, s.Leg, s.Step))
		}
		flags = append(flags, f)
	}

	// A pack that killed a member more than once: the same member, twice at one step.
	type packKey struct {
		leg, step int
		who       string
	}
	packs := map[packKey][]Death{}
	var unattributed []Death
	for _, d := range w.DeathLog {
		if d.Who == "" {
			// A death whose event named no current party member says nothing about a member: it is reported on its
			// own and counted nowhere, and the fight's unit id (the same for every death of one pull) never groups it.
			unattributed = append(unattributed, Death{AtS: d.AtS, Leg: d.Leg, Step: d.Step, Unit: d.Unit, Who: d.Who, Why: d.Why})
			continue
		}
		k := packKey{d.Leg, d.Step, d.Who}
		packs[k] = append(packs[k], Death{AtS: d.AtS, Leg: d.Leg, Step: d.Step, Unit: d.Unit, Who: d.Who, Why: d.Why})
	}
	for k, ds := range packs {
		if len(ds) < 2 {
			continue
		}
		f := Flag{
			ID: "pack-deaths", Key: fmt.Sprintf("leg:%d/step:%d/%s", k.leg, k.step, k.who),
			Text: fmt.Sprintf("leg %d/step %d killed %s %d times", k.leg, k.step, k.who, len(ds)),
		}
		for _, d := range ds {
			f.Evidence = append(f.Evidence, deathLine(d))
		}
		flags = append(flags, f)
	}

	// A member who died far more than the others.
	byWho := map[string][]Death{}
	for _, d := range w.DeathLog {
		if d.Who == "" {
			continue
		}
		byWho[d.Who] = append(byWho[d.Who], Death{AtS: d.AtS, Leg: d.Leg, Step: d.Step, Unit: d.Unit, Who: d.Who, Why: d.Why})
	}
	for who, ds := range byWho {
		next := 0
		for other, ods := range byWho {
			if other != who && len(ods) > next {
				next = len(ods)
			}
		}
		if len(ds) < th.MemberDeaths || len(ds)-next < 2 {
			continue
		}
		f := Flag{
			ID: "member-deaths", Key: who,
			Text: fmt.Sprintf("%s died %d times (next most %d)", who, len(ds), next),
		}
		for _, d := range ds {
			f.Evidence = append(f.Evidence, deathLine(d))
		}
		flags = append(flags, f)
	}

	// Deaths no party member matches: bad telemetry, reported as such so it is not read as a member's deaths.
	if len(unattributed) > 0 {
		f := Flag{
			ID: "unattributed-deaths", Key: "count",
			Text: fmt.Sprintf("%d death(s) name no current party member", len(unattributed)),
		}
		for _, d := range unattributed {
			f.Evidence = append(f.Evidence, deathLine(d))
		}
		flags = append(flags, f)
	}

	// A leg much slower than the plan's recorded time, when the record carries one.
	for _, l := range legs {
		if !slow[l.Leg] {
			continue
		}
		flags = append(flags, Flag{
			ID: "slow-leg", Key: fmt.Sprintf("leg:%d", l.Leg),
			Text: fmt.Sprintf("leg %d %q took %ds, %.0f%% of its plan's %ds",
				l.Leg, l.Name, l.Seconds, 100*float64(l.Seconds)/float64(l.PlanS), l.PlanS),
			Evidence: []string{fmt.Sprintf("leg %d %q: seconds=%d plan_s=%d factor=%.1f",
				l.Leg, l.Name, l.Seconds, l.PlanS, th.SlowFactor)},
		})
	}

	// Pauses: the watchdog's, and the ones no resume cleared.
	for _, p := range w.PauseLog {
		if strings.Contains(strings.ToLower(p.Why), "watchdog") {
			flags = append(flags, Flag{
				ID: "watchdog-pause", Key: fmt.Sprintf("at:%.0f", p.AtS),
				Text:     fmt.Sprintf("watchdog paused the run at %s: %s", position(p.Leg, "", p.Step), p.Why),
				Evidence: []string{fmt.Sprintf("pause t=%.0fs leg=%d step=%d why=%q", p.AtS, p.Leg, p.Step, p.Why)},
			})
		}
		if !p.Cleared {
			flags = append(flags, Flag{
				ID: "uncleared-pause", Key: fmt.Sprintf("at:%.0f", p.AtS),
				Text: fmt.Sprintf("pause at %s never cleared: %s", position(p.Leg, "", p.Step), p.Why),
				Evidence: []string{
					fmt.Sprintf("pause t=%.0fs leg=%d step=%d why=%q", p.AtS, p.Leg, p.Step, p.Why),
					fmt.Sprintf("record state=%s why=%q", w.State, w.Why),
				},
			})
		}
	}

	// A rescue used.
	if len(w.RescueLog) > 0 {
		for _, r := range w.RescueLog {
			f := Flag{
				ID: "rescue", Key: fmt.Sprintf("at:%d", r.AtS),
				Text: fmt.Sprintf("practice rescue at %s%s", position(r.Leg, "", r.Step), whySuffix(r.Why)),
				Evidence: []string{
					fmt.Sprintf("rescue t=%ds leg=%d step=%d why=%q", r.AtS, r.Leg, r.Step, r.Why),
				},
			}
			flags = append(flags, f)
		}
	} else if w.Rescues > 0 {
		flags = append(flags, Flag{
			ID: "rescue", Key: "count",
			Text:     fmt.Sprintf("%d practice rescue(s) used", w.Rescues),
			Evidence: []string{fmt.Sprintf("record rescues=%d", w.Rescues)},
		})
	}

	flags = append(flags, stopFlagFromWatch(w, stopReason))
	sortFlags(flags)
	return flags
}

// whySuffix adds the pause's reason to a flag's text.
func whySuffix(why string) string {
	if why == "" {
		return ""
	}
	return " (" + why + ")"
}

// deathWho is the member's word. A death the event did not name as a party member is unattributed: it keeps the name
// it carried, never the fight's unit (every death of one pull shares it).
func deathWho(d driver.DeathRecord) string {
	if d.Who != "" {
		return d.Who
	}
	if d.Why != "" {
		return "unattributed (" + d.Why + ")"
	}
	return "unattributed"
}

// deathLine is one death's evidence.
func deathLine(d Death) string {
	who := d.Who
	if who == "" {
		who = "unattributed"
	}
	line := fmt.Sprintf("death t=%ds leg=%d step=%d who=%s", d.AtS, d.Leg, d.Step, who)
	if d.Who == "" && d.Why != "" {
		line += " why=" + d.Why
	}
	return line + fmt.Sprintf(" unit=%d", d.Unit)
}

// stopFlagFromRecord is the run's own stop reason.
func stopFlagFromRecord(reason, state, why string) Flag {
	text := "stop reason " + reason
	if reason == "" {
		text = "stop reason none recorded"
	}
	ev := fmt.Sprintf("record stop_reason=%s state=%s", reason, state)
	if why != "" {
		ev += fmt.Sprintf(" why=%q", why)
	}
	return Flag{ID: "stop-reason", Key: "reason", Text: text, Evidence: []string{ev}}
}

// stopFlagFromWatch is the run's own stop reason, at the position the record ended.
func stopFlagFromWatch(w *driver.RunWatch, reason string) Flag {
	f := stopFlagFromRecord(reason, w.State, w.Why)
	f.Evidence = append(f.Evidence, fmt.Sprintf("record leg=%d step=%d elapsed=%ds", w.Leg, w.Step, int(w.ElapsedS)))
	return f
}

// flagOrder is the table's order: the weak spots first, the stop reason last.
var flagOrder = map[string]int{
	"repulls": 0, "pack-deaths": 1, "member-deaths": 2, "slow-leg": 3,
	"watchdog-pause": 4, "uncleared-pause": 5, "rescue": 6, "stop-reason": 7,
}

// sortFlags keeps a card's flags in a stable order.
func sortFlags(flags []Flag) {
	sort.SliceStable(flags, func(i, j int) bool {
		if flagOrder[flags[i].ID] != flagOrder[flags[j].ID] {
			return flagOrder[flags[i].ID] < flagOrder[flags[j].ID]
		}
		return flags[i].Key < flags[j].Key
	})
}

// Text is the card as a person reads it: the verdict, the thresholds, the leg table, the deaths, the pauses and the
// flags with their evidence.
func (c Card) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "report card: run %s plan %s mode %s\n", orNone(c.RunID), orNone(c.Plan), orNone(c.Mode))
	fmt.Fprintf(&b, "verdict: %s\n", c.VerdictText)
	fmt.Fprintf(&b, "thresholds: re-pulls >%d per step; one member >=%d deaths and 2 above the next; a leg over %.2gx its plan time\n",
		c.Thresholds.Repulls, c.Thresholds.MemberDeaths, c.Thresholds.SlowFactor)
	fmt.Fprintf(&b, "run: state %s, leg %d, step %d/%d, pulls %d/%d, deaths %d, wipes %d, pauses %d, rescues %d, %.0fs\n",
		orNone(c.State), c.Leg, c.Step, c.Steps, c.Pull, c.Pulls, c.Deaths, c.Wipes, c.Pauses, c.Rescues, c.ElapsedS)
	if len(c.Legs) == 0 {
		b.WriteString("legs: none recorded\n")
	} else {
		b.WriteString("legs:\n")
		for _, l := range c.Legs {
			fmt.Fprintf(&b, "  leg %d %q: %ds, pulls %d, re-pulls %d, steps %d, deaths %d, wipes %d, pauses %d",
				l.Leg, l.Name, l.Seconds, l.Pulls, l.Repulls, l.Steps, l.Deaths, l.Wipes, l.Pauses)
			if l.PlanS > 0 {
				fmt.Fprintf(&b, ", plan %ds", l.PlanS)
			}
			b.WriteString("\n")
		}
	}
	if len(c.DeathLog) == 0 {
		b.WriteString("deaths: none\n")
	} else {
		b.WriteString("deaths:\n")
		for _, d := range c.DeathLog {
			fmt.Fprintf(&b, "  leg %d/step %d t=%ds %s\n", d.Leg, d.Step, d.AtS, deathWho(driver.DeathRecord(d)))
			for _, r := range d.Recap {
				fmt.Fprintf(&b, "    deathrecap: %s\n", r)
			}
		}
	}
	if len(c.PauseLog) == 0 {
		b.WriteString("pauses: none\n")
	} else {
		b.WriteString("pauses:\n")
		for _, p := range c.PauseLog {
			cleared := "not cleared"
			if p.Cleared {
				cleared = "cleared"
			}
			fmt.Fprintf(&b, "  leg %d/step %d t=%.0fs %s: %q\n", p.Leg, p.Step, p.AtS, cleared, p.Why)
		}
	}
	if len(c.Flags) == 0 {
		b.WriteString("flags: none\n")
	} else {
		b.WriteString("flags:\n")
		for _, f := range c.Flags {
			fmt.Fprintf(&b, "  [%s] %s\n", f.ID, f.Text)
			for _, e := range f.Evidence {
				fmt.Fprintf(&b, "    %s\n", e)
			}
		}
	}
	return b.String()
}

// orNone prints a dash for an empty word.
func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Write writes the card's text and JSON into dir as <run_id>.report-card.txt and .json. It returns both paths.
func Write(dir string, c Card) (string, string, error) {
	if dir == "" {
		return "", "", fmt.Errorf("report card: no folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	name := c.RunID
	if name == "" {
		name = "run"
	}
	txtAt := filepath.Join(dir, name+".report-card.txt")
	jsonAt := filepath.Join(dir, name+".report-card.json")
	if err := os.WriteFile(txtAt, []byte(c.Text()), 0o644); err != nil {
		return "", "", err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(jsonAt, append(b, '\n'), 0o644); err != nil {
		return "", "", err
	}
	return txtAt, jsonAt, nil
}

// Load reads a card or a run record from path. A record is built into a card with th; a card is returned as saved
// (its own thresholds are the ones it was built with).
func Load(path string, th Thresholds) (Card, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Card{}, err
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return Card{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	switch head.Schema {
	case Schema:
		var c Card
		if err := json.Unmarshal(b, &c); err != nil {
			return Card{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if c.Version != Version {
			return Card{}, fmt.Errorf("%s: card version %d, want %d", filepath.Base(path), c.Version, Version)
		}
		return c, nil
	case driver.SummarySchema:
		var s driver.RunSummary
		if err := json.Unmarshal(b, &s); err != nil {
			return Card{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if s.Version != driver.SummaryVersion {
			return Card{}, fmt.Errorf("%s: record version %d, want %d", filepath.Base(path), s.Version, driver.SummaryVersion)
		}
		return Build(s, th), nil
	default:
		return Card{}, fmt.Errorf("%s: not a report card or a run record", filepath.Base(path))
	}
}
