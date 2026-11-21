package main

// The dry half of `forever bg-watch`: the watch machine over a scripted WSG
// match (queue, invite, one capture, alliance win) and a scripted AB match
// (claims, assaults, one occupation). The script lives in the bg package's
// own test fake pattern, written here as a command-local fake so the dry
// path exercises the same machine the live session binds.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
)

// bgDryFake is the command's scripted ports: world states, status polls and
// the match end advance one poll per Sleep over a fixed script.
type bgDryFake struct {
	now    time.Time
	sent   int
	states []map[int32]int32
	cursor int
	polls  int
	max    int
	mapID  bg.Map
}

func (f *bgDryFake) Send(uint32, []byte) error { f.sent++; return nil }

func (f *bgDryFake) Status() (bg.StatusReport, error) {
	return bg.StatusReport{
		Team:      "alliance",
		Status:    "in-progress",
		Alliance:  bg.Side{Humans: 1, Bots: 4},
		Horde:     bg.Side{Bots: 5},
		FCEscorts: 2,
		Bots:      []bg.BotRow{{Role: "attacker", Objective: "fight", Node: "stables", Alive: true, MoveAgeS: 3}},
	}, nil
}

func (f *bgDryFake) WorldStates() map[int32]int32 {
	m := f.states[f.cursor]
	if f.cursor < len(f.states)-1 {
		f.cursor++
	}
	return m
}

func (f *bgDryFake) SelfPos() (float32, float32, float32) { return 1540, 1481, 352 }

func (f *bgDryFake) Died() bool { return false }

func (f *bgDryFake) PollStatus() error { return nil }

// Active serves the queue-entry confirmation: the script is one accepted
// session, so the entry is active from the script's start.
func (f *bgDryFake) Active() bool { return true }

func (f *bgDryFake) Refused() string { return "" }

func (f *bgDryFake) Sleep(d time.Duration) error {
	f.now = f.now.Add(d)
	f.polls++
	return nil
}

func (f *bgDryFake) Invite(time.Time) (bg.ConfirmStatus, bool) {
	return bg.ConfirmStatus{Header: bg.StatusHeader{InstanceID: 1}, Map: uint32(f.mapID)}, true
}

func (f *bgDryFake) Ended() (bg.WatchEnd, bool) {
	if f.polls >= f.max {
		return bg.WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins (dry)"}, true
	}
	return bg.WatchEnd{}, false
}

// bgWatchDry plays the watch over the scripted world and prints the end
// line, the record file and the card.
func bgWatchDry(ctx context.Context, cfg bg.WatcherConfig, stdout, stderr io.Writer) int {
	_ = ctx
	_ = stderr
	var states []map[int32]int32
	if cfg.Map == bg.Map(bg.MapAB) {
		states = []map[int32]int32{
			{1776: 0, 1777: 0},
			{1769: 1, 1784: 1, 1774: 1, 1794: 1, 1789: 1, 1776: 0, 1777: 0},                     // claimed by the Alliance
			{1767: 1, 1782: 1, 1772: 1, 1792: 1, 1787: 1, 1776: 30, 1777: 0, 1779: 5, 1778: 0},  // occupied
			{1770: 1, 1782: 1, 1772: 1, 1792: 1, 1787: 1, 1776: 60, 1777: 10, 1779: 4, 1778: 0}, // the Horde assaults the stables
			{1767: 1, 1782: 1, 1772: 1, 1792: 1, 1787: 1, 1776: 90, 1777: 10, 1779: 5, 1778: 0}, // defended back
		}
	} else {
		states = []map[int32]int32{
			{bg.WSFlagAlliance: 0, bg.WSFlagHorde: 0, bg.WSCapAlliance: 0, bg.WSCapHorde: 0},
			{bg.WSFlagAlliance: 0, bg.WSFlagHorde: 1, bg.WSCapAlliance: 0, bg.WSCapHorde: 0},
			{bg.WSFlagAlliance: 0, bg.WSFlagHorde: 0, bg.WSCapAlliance: 1, bg.WSCapHorde: 0},
		}
	}
	if cfg.RunID == "" {
		cfg.RunID = "bg-dry-1"
	}
	f := &bgDryFake{states: states, max: len(states), mapID: cfg.Map}
	w := bg.NewWatcher(cfg, f, func(format string, args ...any) {
		fmt.Fprintf(stdout, format+"\n", args...)
	})
	base := time.Now()
	res := w.Run(base.Add(time.Duration(cfg.MaxMin+5) * time.Minute))
	fmt.Fprintf(stdout, "forever bg-watch: map %s role %s stop %s winner %s events %d (dry)\n",
		cfg.Map, roleWord(cfg.Role), res.Stop, res.Record.Winner, len(res.Record.Events))
	fmt.Fprintf(stdout, "card: %s\n", res.Card.VerdictText)
	if cfg.Out != "" {
		sumFile, cardTxt, cardJSON, err := writeBGFiles(cfg.Out, &res)
		if err != nil {
			fmt.Fprintf(stdout, "card: %s (not written: %v)\n", res.Card.VerdictText, err)
			fmt.Fprint(stdout, bgCardText(res.Card))
			return exitWorld
		}
		fmt.Fprintf(stdout, "record: %s\n", sumFile)
		fmt.Fprintf(stdout, "card files: %s %s\n", cardTxt, cardJSON)
	}
	if res.Clear {
		return exitOK
	}
	return exitWorld
}

// bgCardText is the card as a person reads it: the verdict, the thresholds,
// the match numbers and the flags with their evidence.
func bgCardText(c bg.Card) string {
	out := "verdict: " + c.VerdictText + "\n"
	out += fmt.Sprintf("map: %s winner: %s duration: %.0fs\n", c.Map, c.Winner, c.DurationS)
	out += fmt.Sprintf("thresholds: quiet_min=%.0f idle_s=%.0f escort_share=%.2f touch_s=%.0f\n",
		c.Thresholds.QuietMin, c.Thresholds.IdleS, c.Thresholds.EscortShare, c.Thresholds.TouchS)
	for _, f := range c.Flags {
		out += "flag " + f.ID + " " + f.Key + ": " + f.Text + "\n"
		for _, e := range f.Evidence {
			out += "  " + e + "\n"
		}
	}
	for _, n := range c.Notes {
		out += "note: " + n + "\n"
	}
	return out
}
