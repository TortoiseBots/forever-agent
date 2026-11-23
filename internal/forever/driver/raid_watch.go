package driver

// The raid watch (`forever raid-watch`, SPEC #108 R3): the dungeon watch with a raid plan. Everything the watch does
// is the same machine — PREPARE, ENTER, the module's own run, the safety budgets, the practice rescue — and this file
// is the one part that knows the group is a raid:
//
//   - before PREPARE the module's commander card must show Raid and at least the plan's raid size (10, 20 or 40)
//     members. When it does not and this character leads the group (the card's playerled flag), the watch sends one
//     convert_raid and re-reads the card; a card that still is not a raid, or is short of the plan's size, ends the
//     watch before anything else is sent (the spec's "refuses short roster").
//   - the --layout file's bot name -> subgroup entries are then applied with raid_move, each one read back by the
//     group layer (a move the core drops is an error there, never a silent no-op); a member already in its subgroup
//     gets no packet.
//   - at the end the watch writes summary.json in the tb-raid-walk v1 shape raid-autopilot and the Turtle report
//     tools read (internal/raidwalk.Summary), beside the run's own <run_id>.forever-run.json.
//
// The two sends are the group layer's client packets, not `.tb` action lines: they are the watcher's own, they run
// before the module's run starts (the watch sends no order after its start), and every one of them is gated by the
// group layer's rank rule and answered by the roster readback (`internal/forever/world/raidctl.go`, R2).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
)

// RaidSlot is one line of a raid watch's --layout file: a bot's name and the subgroup (1-8, as the raid frame numbers
// them) it is moved to before PREPARE.
type RaidSlot struct {
	Name     string `json:"name"`
	Subgroup int    `json:"subgroup"`
}

// maxRaidSize is a raid's largest group (the core's MAX_RAID_SIZE, 40): the layout's bound and the party the watch
// models. The plan's own raid block says which of 10, 20 or 40 it is (Route.Raid).
const maxRaidSize = party.MaxRaidMembers

// partySize is the group's size: five for a dungeon party, the plan's raid size for a raid watch.
func (r *Run) partySize() int {
	if r.cfg.Raid && r.route != nil && r.route.Raid != nil && r.route.Raid.Size > 0 {
		return r.route.Raid.Size
	}
	return 5
}

// LoadRaidLayout reads a raid watch's --layout file. One entry per line:
//
//	# a comment
//	Tankone 1
//	Healertwo = 2
//
// The name and the subgroup are separated by whitespace or '='. Blank lines and lines whose first non-blank character
// is '#' are skipped. A line that is not a name and a subgroup 1-8, or a name listed twice, is an error naming the
// line: a layout that silently drops an entry would put the raid where its author did not.
func LoadRaidLayout(path string) ([]RaidSlot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseRaidLayout(b)
}

// ParseRaidLayout parses the layout file's bytes (LoadRaidLayout's rules).
func ParseRaidLayout(b []byte) ([]RaidSlot, error) {
	var out []RaidSlot
	seen := map[string]int{}
	for n, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		fields := strings.FieldsFunc(s, func(c rune) bool { return c == '=' || c == ' ' || c == '\t' })
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: %q: use a bot's name and its subgroup 1-%d", n+1, strings.TrimSpace(line), party.MaxSubgroups)
		}
		name := fields[0]
		sub, err := strconv.Atoi(fields[1])
		if err != nil || sub < 1 || sub > party.MaxSubgroups {
			return nil, fmt.Errorf("line %d: %q: the subgroup is 1-%d, not %q", n+1, strings.TrimSpace(line), party.MaxSubgroups, fields[1])
		}
		key := strings.ToLower(name)
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("line %d: %q is listed twice (line %d)", n+1, name, prev)
		}
		seen[key] = n + 1
		out = append(out, RaidSlot{Name: name, Subgroup: sub})
	}
	if len(out) > maxRaidSize {
		return nil, fmt.Errorf("%d entries: a raid has at most %d members", len(out), maxRaidSize)
	}
	return out, nil
}

// raidSetup makes the module's group the plan's raid and applies the layout, before PREPARE. It returns false with
// stopReason set when the watch cannot go on; the module's own refusal text is logged, never swallowed.
func (r *Run) raidSetup(ctx context.Context) bool {
	size := r.partySize()
	card, ok := r.raidCard(ctx)
	if r.lost || r.stopReason != "" {
		return false
	}
	if !ok {
		r.logf("raid: no commander card: no group to convert or lay out")
		r.stop("raid:card")
		return false
	}
	if (!card.Raid || len(card.Members) < size) && card.PlayerLed && len(card.Members) >= size {
		r.logf("raid: the card shows %d members in no raid; this character leads, so convert_raid goes out", len(card.Members))
		if err := r.cmd.ConvertRaid(ctx); err != nil {
			if errors.Is(err, ErrLinkLost) {
				r.linkLost()
				return false
			}
			r.logf("raid: convert_raid: %v", err)
			r.stop("raid:convert")
			return false
		}
		// The convert is done when the group layer read its roster back; the card read now is what the watch acts on.
		if card, ok = r.raidCard(ctx); !ok {
			if r.lost || r.stopReason != "" {
				return false
			}
			r.logf("raid: the commander card is gone after convert_raid")
			r.stop("raid:card")
			return false
		}
	}
	if len(card.Members) < size {
		r.logf("raid: the card shows %d members, the plan needs %d", len(card.Members), size)
		r.stop("raid:roster")
		return false
	}
	if !card.Raid {
		r.logf("raid: the group is not a raid: the card shows no raid flag")
		r.stop("raid:not-raid")
		return false
	}
	return r.applyRaidLayout(ctx, card)
}

// raidCard reads the commander's binding card: the card of the module's own commander (r.commander), else the card
// that leads the watched tank. A failed read is one stop; a link loss sets the watch's own lost flag.
func (r *Run) raidCard(ctx context.Context) (tbcmd.Card, bool) {
	rep, err := r.cmd.CommanderStatus(ctx, r.commander)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return tbcmd.Card{}, false
	}
	if err != nil {
		r.logf("raid: the commander card read failed: %v", err)
		return tbcmd.Card{}, false
	}
	tank := r.cfg.Tank()
	for i := range rep.Cards {
		c := &rep.Cards[i]
		if r.commander != "" && strings.EqualFold(c.Cmd, r.commander) {
			return *c, true
		}
	}
	for i := range rep.Cards {
		c := &rep.Cards[i]
		if tank != "" && (strings.EqualFold(c.Lead, tank) || cardHolds(c, tank)) {
			return *c, true
		}
	}
	if len(rep.Cards) == 1 {
		return rep.Cards[0], true
	}
	return tbcmd.Card{}, false
}

// cardHolds reports whether the card lists a member by that name.
func cardHolds(c *tbcmd.Card, name string) bool {
	for _, m := range c.Members {
		if strings.EqualFold(m.Bot, name) {
			return true
		}
	}
	return false
}

// applyRaidLayout moves every layout entry to its subgroup with raid_move, in file order. A member the card does not
// hold is a layout that names someone else's raid (stopped, not sent), and one already in its subgroup gets no packet.
// The group layer reads every move back; a move it cannot confirm ends the watch with its own text.
func (r *Run) applyRaidLayout(ctx context.Context, card tbcmd.Card) bool {
	if len(r.cfg.Layout) == 0 {
		return true
	}
	subs := make(map[string]int, len(card.Members))
	for _, m := range card.Members {
		if m.HasSub {
			subs[strings.ToLower(m.Bot)] = m.Sub
		}
	}
	in := make(map[string]bool, len(card.Members))
	for _, m := range card.Members {
		in[strings.ToLower(m.Bot)] = true
	}
	for _, slot := range r.cfg.Layout {
		key := strings.ToLower(slot.Name)
		if !in[key] {
			r.logf("raid: layout: %q is not on the commander's card", slot.Name)
			r.stop("raid:layout")
			return false
		}
		if cur, ok := subs[key]; ok && cur == slot.Subgroup {
			continue
		}
		if err := r.cmd.RaidMove(ctx, slot.Name, slot.Subgroup); err != nil {
			if errors.Is(err, ErrLinkLost) {
				r.linkLost()
				return false
			}
			r.logf("raid: layout: %s to subgroup %d: %v", slot.Name, slot.Subgroup, err)
			r.stop("raid:layout")
			return false
		}
		r.logf("raid: layout: %s to subgroup %d", slot.Name, slot.Subgroup)
		subs[key] = slot.Subgroup
	}
	return true
}

// The raid watch's summary file. It is internal/raidwalk.Summary's tb-raid-walk v1 shape, so raid-autopilot and the
// Turtle report tools read a Forever raid walk's summary the way they read a Turtle walk's. The type is redeclared
// here because raidwalk imports the world session, which imports this package: naming the subset this watch fills
// keeps the file readable by raidwalk.Summary (an absent field reads as its zero value) without the cycle.
const (
	raidWalkFormat  = "tb-raid-walk"
	raidWalkVersion = 1
	// RaidSummaryFile is the name of the raid summary inside the watch's --out folder.
	RaidSummaryFile = "summary.json"
)

// raidWalkSummary is the subset of raidwalk.Summary a Forever raid watch fills.
type raidWalkSummary struct {
	Format       string         `json:"format"`
	Version      int            `json:"version"`
	Plan         string         `json:"plan"`
	Map          uint32         `json:"map"`
	Server       string         `json:"server"`
	Slot         int            `json:"slot"`
	Character    string         `json:"character"`
	Started      time.Time      `json:"started"`
	Ended        time.Time      `json:"ended"`
	EndReason    string         `json:"end_reason"`
	StepsReached int            `json:"steps_reached"`
	StepsTotal   int            `json:"steps_total"`
	Roster       raidWalkRoster `json:"roster"`
	Layout       raidWalkLayout `json:"layout"`
	BossKills    []raidWalkBoss `json:"boss_kills"`
	Deaths       raidWalkDeaths `json:"deaths"`
	ExitCode     int            `json:"exit_code"`
	Notes        []string       `json:"notes"`
}

// raidWalkRoster is raidwalk.RosterSummary's formation part: the main tank, the off-tanks and everyone else.
type raidWalkRoster struct {
	MT   string   `json:"mt"`
	OT   []string `json:"ot"`
	Rest []string `json:"rest"`
}

// raidWalkLayout is raidwalk.LayoutSummary: the main tank and the tanks the layout puts the raid with.
type raidWalkLayout struct {
	MT       string   `json:"mt"`
	Tanks    []string `json:"tanks"`
	Warnings []string `json:"warnings"`
}

// raidWalkBoss is one raidwalk.BossKillSummary: one boss the module's instance report knew, with its state as the
// kill evidence (the module's report is all a watch has; it credits no kill of its own).
type raidWalkBoss struct {
	Entry    uint32 `json:"entry"`
	Step     int    `json:"step"`
	Killed   bool   `json:"killed"`
	Evidence string `json:"evidence"`
}

// raidWalkDeaths is raidwalk.DeathSummary's total (the module's own counter).
type raidWalkDeaths struct {
	Total int `json:"total"`
}

// raidExit is the summary's exit code: the link's own code for a lost link, 0 for a clear and 1 for any other end.
func raidExit(res *Result) int {
	switch {
	case res.Exit != 0:
		return res.Exit
	case res.Clear:
		return 0
	}
	return 1
}

// writeRaidSummary writes <out>/summary.json for a raid watch: the run's own counters in the tb-raid-walk shape. It
// never fails the watch: a write error is one log line. The file carries the raid's bot names (the Turtle tools match
// their rows by them); no host, account or path ever reaches it.
func (r *Run) writeRaidSummary(w *watchRun, res *Result) {
	if !r.cfg.Raid || r.cfg.Out == "" || r.route == nil {
		return
	}
	s := raidWalkSummary{
		Format: raidWalkFormat, Version: raidWalkVersion,
		Plan: r.route.Name, Map: r.route.Map, Character: r.cfg.Commander,
		Started: r.start, Ended: r.now(), EndReason: res.Stop, ExitCode: raidExit(res),
		Roster:    raidWalkRoster{OT: []string{}, Rest: []string{}},
		Layout:    raidWalkLayout{Tanks: []string{}, Warnings: []string{}},
		BossKills: []raidWalkBoss{}, Notes: []string{"forever raid-watch (the module's own run)"},
	}
	if w != nil {
		s.StepsReached, s.StepsTotal = w.record.Step, w.record.Steps
		s.Deaths.Total = w.record.Deaths
		entries := make([]uint32, 0, len(w.bosses))
		for e := range w.bosses {
			entries = append(entries, e)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i] < entries[j] })
		for _, e := range entries {
			state := w.bosses[e]
			s.BossKills = append(s.BossKills, raidWalkBoss{Entry: e, Killed: state == "dead",
				Evidence: "the module's instance report says " + state})
		}
	}
	// The formation: the roster's own roles, in the party order PREPARE adopted (the tank first, the healer next).
	for _, name := range r.cfg.Party {
		switch strings.ToLower(r.partyRoles[strings.ToLower(name)]) {
		case "tank":
			if s.Roster.MT == "" {
				s.Roster.MT = name
			} else {
				s.Roster.OT = append(s.Roster.OT, name)
			}
			s.Layout.Tanks = append(s.Layout.Tanks, name)
		case "healer", "dps":
			s.Roster.Rest = append(s.Roster.Rest, name)
		}
	}
	s.Layout.MT = s.Roster.MT
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		r.logf("raid: summary: %v", err)
		return
	}
	at := filepath.Join(r.cfg.Out, RaidSummaryFile)
	tmp := at + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		r.logf("raid: summary: %v", err)
		return
	}
	if err := os.Rename(tmp, at); err != nil {
		r.logf("raid: summary: %v", err)
		return
	}
	res.RaidSummaryFile = at
	r.logf("raid: summary %s", at)
}
