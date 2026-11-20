package main

// forever loot-check and forever object-use: this round's two live commands. loot-check settles, finds the nearest
// lootable thing (a dead creature of --entry, or a chest), kills it first when it is still alive, opens the window,
// votes on a group roll when --roll asked for one, takes the money and the items it may take, and releases.
// object-use finds the nearest game object of --entry and reports its state before and after the use.
//
// Neither command prints an identifier: the output carries game values (entries, item ids, counts, slot and state
// names) and the winner as a role ("you", "another player", "nobody").

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverLootCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever loot-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--entry N] [--roll need|greed|pass] [--take N] [--money=false] [--at x,y,z] [--at-range YARDS] [--range YARDS] [--stay DURATION] [--fight-for DURATION]"
	fs := flag.NewFlagSet("forever loot-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 8*time.Second, "how long to settle in the world before choosing the target")
	entry := fs.Int64("entry", 0, "creature or game-object entry to loot (0: the nearest dead creature or chest)")
	roll := fs.String("roll", "", "vote on a group roll: need, greed or pass (empty votes on nothing; need is never sent unless asked)")
	take := fs.Int("take", 0, "how many items to take from the window (0: every item the window allows)")
	money := fs.Bool("money", true, "take the window's money")
	reach := fs.Float64("range", 30, "how far to look for a target, in yards")
	at := fs.String("at", "", "x,y,z of the chest to loot, when several game objects of --entry are in range")
	atRange := fs.Float64("at-range", 2, "how close a game object must be to --at, in yards")
	fightFor := fs.Duration("fight-for", 60*time.Second, "how long a target that is still alive is fought before giving up")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever loot-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever loot-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	if *entry < 0 || *entry > 1<<24 {
		fmt.Fprintln(stderr, "forever loot-check: --entry must be a creature or game-object entry")
		return exitUsage
	}
	if _, _, err := loot.VoteFromWord(*roll); err != nil {
		fmt.Fprintf(stderr, "forever loot-check: %v\n", err)
		return exitUsage
	}
	if *take < 0 || *take > 18 {
		fmt.Fprintln(stderr, "forever loot-check: --take is between 0 (all) and 18")
		return exitUsage
	}
	if *reach < 1 || *reach > 200 {
		fmt.Fprintln(stderr, "forever loot-check: --range must be between 1 and 200 yards")
		return exitUsage
	}
	atX, atY, atZ, atErr := parseAtPoint(*at)
	if atErr != nil {
		fmt.Fprintf(stderr, "forever loot-check: %v\n", atErr)
		return exitUsage
	}
	if *atRange < 0.5 || *atRange > 100 {
		fmt.Fprintln(stderr, "forever loot-check: --at-range must be between 0.5 and 100 yards")
		return exitUsage
	}
	if *fightFor <= 0 || *fightFor > 5*time.Minute {
		fmt.Fprintln(stderr, "forever loot-check: --fight-for must be between 0 and 5m")
		return exitUsage
	}
	if err := common.passwordCheck("forever loot-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever loot-check", stdout, world.Options{
		LootCheck: true,
		Entry:     int32(*entry),
		Roll:      *roll,
		LootMoney: *money,
		TakeItems: *take,
		LootRange: float32(*reach),
		AtSet:     *at != "",
		AtX:       atX,
		AtY:       atY,
		AtZ:       atZ,
		AtRange:   float32(*atRange),
		FightFor:  *fightFor,
		Settle:    *stay,
		Deadline:  *stay + *fightFor + 3*time.Minute,
	})
	if report != nil {
		printLinkLost(stdout, report)
		printLootCheckSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever loot-check: %v\n", err)
		return lootCommandExit(err)
	}
	return exitOK
}

func foreverObjectUseCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever object-use [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME --entry N [--at x,y,z] [--at-range YARDS] [--range YARDS] [--stay DURATION]"
	fs := flag.NewFlagSet("forever object-use", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 8*time.Second, "how long to settle in the world before using the object")
	entry := fs.Int64("entry", 0, "game-object entry to use (required)")
	at := fs.String("at", "", "x,y,z of the object to use, when several objects of --entry are in range")
	atRange := fs.Float64("at-range", 2, "how close an object must be to --at, in yards")
	reach := fs.Float64("range", 40, "how far to look for the object, in yards")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever object-use"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *entry <= 0 || *entry > 1<<24 {
		fmt.Fprintln(stderr, "forever object-use: --entry is required and must be a game-object entry")
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever object-use: --stay must be between 0 and 10m")
		return exitUsage
	}
	if *reach < 1 || *reach > 200 {
		fmt.Fprintln(stderr, "forever object-use: --range must be between 1 and 200 yards")
		return exitUsage
	}
	atX, atY, atZ, atErr := parseAtPoint(*at)
	if atErr != nil {
		fmt.Fprintf(stderr, "forever object-use: %v\n", atErr)
		return exitUsage
	}
	if *atRange < 0.5 || *atRange > 100 {
		fmt.Fprintln(stderr, "forever object-use: --at-range must be between 0.5 and 100 yards")
		return exitUsage
	}
	if err := common.passwordCheck("forever object-use"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	report, err := common.enter(ctx, "forever object-use", stdout, world.Options{
		ObjectUse: true,
		Entry:     int32(*entry),
		AtSet:     *at != "",
		AtX:       atX,
		AtY:       atY,
		AtZ:       atZ,
		AtRange:   float32(*atRange),
		LootRange: float32(*reach),
		Settle:    *stay,
		Deadline:  *stay + 2*time.Minute,
	})
	if report != nil {
		printLinkLost(stdout, report)
		printObjectUseSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever object-use: %v\n", err)
		return lootCommandExit(err)
	}
	return exitOK
}

// lootCommandExit maps a loot command's error to its exit code: the link and death codes, the ambiguity's own code
// (a script must tell a refusal from a use: nothing was sent, and --at picks one), and the world code for anything
// else.
func lootCommandExit(err error) int {
	switch {
	case errors.Is(err, world.ErrLinkLost):
		return exitDisconnected
	case errors.Is(err, world.ErrSelfDead):
		return exitDead
	case errors.Is(err, world.ErrAmbiguousObject):
		return exitAmbiguous
	}
	return exitWorld
}

// parseAtPoint parses --at x,y,z; an empty value is no point at all.
func parseAtPoint(point string) (x, y, z float32, err error) {
	if point == "" {
		return 0, 0, 0, nil
	}
	parts := strings.Split(point, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("--at wants x,y,z, not %q", point)
	}
	var v [3]float64
	for i, p := range parts {
		f, perr := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, 0, 0, fmt.Errorf("--at: %q is not a number", strings.TrimSpace(p))
		}
		v[i] = f
	}
	return float32(v[0]), float32(v[1]), float32(v[2]), nil
}

// printLootCheckSummary prints the window, what was taken and how the rolls ended.
func printLootCheckSummary(w io.Writer, report *world.Report) {
	r := report.Loot
	if r.Ambiguous {
		fmt.Fprintf(w, "loot-check: %d game objects of entry %d are in range; one must be chosen with --at:\n",
			len(r.Candidates), r.Entry)
		for _, c := range r.Candidates {
			fmt.Fprintf(w, "loot-check:   at %.2f, %.2f, %.2f (%.1f yd)\n", c.X, c.Y, c.Z, c.Distance)
		}
		return
	}
	if r.Kind == "" {
		fmt.Fprintf(w, "loot-check: no window was opened (the model held %d objects)\n", report.Objects.Objects)
		return
	}
	fmt.Fprintf(w, "loot-check: target: %s, entry %d, at %.1f yd", r.Kind, r.Entry, r.Distance)
	if r.Name != "" {
		fmt.Fprintf(w, " (%s)", r.Name)
	}
	fmt.Fprintln(w)
	if r.StateKnown {
		before := goStateText(r.StateBefore)
		after := goStateText(r.StateAfter)
		fmt.Fprintf(w, "loot-check: state: %s -> %s\n", before, after)
	}
	if !r.Opened {
		if r.Failure != 0 {
			fmt.Fprintf(w, "loot-check: the window was refused: %s (reason %d)\n", loot.ErrorName(r.Failure), r.Failure)
		} else {
			fmt.Fprintln(w, "loot-check: no loot window was opened")
		}
		return
	}
	fmt.Fprintf(w, "loot-check: window: %s, threshold %d, %d copper, %d item(s), %d currency row(s)\n",
		loot.MethodName(r.Method), r.Threshold, r.Coins, len(r.Items), r.Currencies)
	for _, it := range r.Items {
		roll := ""
		if it.Roll {
			roll = ", needs a roll"
		}
		if it.Trade {
			roll += ", tradable to the tap list"
		}
		fmt.Fprintf(w, "loot-check:   slot %d: item %d x%d, %s%s\n", it.LootListID, it.ItemID, it.Count,
			loot.SlotName(it.Slot), roll)
	}
	for i := range r.Rolls {
		row := &r.Rolls[i]
		fmt.Fprintf(w, "loot-check: roll: slot %d item %d, %d ms, valid votes 0x%02X", row.LootListID, row.ItemID,
			row.TimeLimit, row.ValidRolls)
		if row.OurVote != 0xFF {
			fmt.Fprintf(w, ", our vote %s", loot.RollName(row.OurVote))
		}
		if row.Ended {
			if row.AllPassed {
				fmt.Fprintf(w, ": everybody passed (%d vote(s) seen)", row.VoteSeen)
			} else {
				fmt.Fprintf(w, ": %s won with %d on a %s vote (%d vote(s) seen)", row.Winner, row.Number,
					loot.RollName(row.Vote), row.VoteSeen)
			}
			if row.Autopassed > 0 || row.OffSpec > 0 {
				fmt.Fprintf(w, ", %d autopass(es), %d off-spec", row.Autopassed, row.OffSpec)
			}
		} else {
			fmt.Fprintf(w, ": no result before the client stopped waiting")
		}
		fmt.Fprintln(w)
	}
	if r.MoneyTaken != 0 || r.MoneyMod != 0 {
		fmt.Fprintf(w, "loot-check: money taken: %d copper (modifier %d)\n", r.MoneyTaken, r.MoneyMod)
	}
	if len(r.Removed) > 0 {
		fmt.Fprintf(w, "loot-check: item slots removed by the server: %v\n", r.Removed)
	}
	fmt.Fprintf(w, "loot-check: window released: %v\n", r.Released)
	if r.ReleaseErr != "" {
		fmt.Fprintf(w, "loot-check: the cleanup release failed: %s\n", r.ReleaseErr)
	}
}

// printObjectUseSummary prints the object's state and flags before and after the use.
func printObjectUseSummary(w io.Writer, report *world.Report) {
	r := report.ObjectUse
	if !r.Found {
		if !r.Ambiguous {
			fmt.Fprintln(w, "object-use: no object of that entry was in range")
			return
		}
		fmt.Fprintf(w, "object-use: %d objects of entry %d are in range; one must be chosen with --at:\n",
			len(r.Candidates), r.Entry)
		for _, c := range r.Candidates {
			fmt.Fprintf(w, "object-use:   at %.2f, %.2f, %.2f (%.1f yd)\n", c.X, c.Y, c.Z, c.Distance)
		}
		return
	}
	fmt.Fprintf(w, "object-use: entry %d", r.Entry)
	if r.Name != "" {
		fmt.Fprintf(w, " (%s)", r.Name)
	}
	fmt.Fprintf(w, " at %.1f yd\n", r.Distance)
	if r.StateKnown {
		fmt.Fprintf(w, "object-use: state %s -> %s, flags 0x%08X -> 0x%08X (changed: %v)\n",
			goStateText(r.StateBefore), goStateText(r.StateAfter), r.FlagsBefore, r.FlagsAfter, r.StateChanged)
	} else {
		fmt.Fprintln(w, "object-use: no values update carried the object's state")
	}
	if r.LootOpened {
		fmt.Fprintf(w, "object-use: the use opened a loot window (%d item(s)); released: %v\n", r.LootItems, r.Released)
	}
	if report.Loot.ReleaseErr != "" {
		fmt.Fprintf(w, "object-use: the cleanup release failed: %s\n", report.Loot.ReleaseErr)
	}
}

// goStateText names a GOState the way the report prints it (SharedDefines.h:3278-3285).
func goStateText(state int8) string {
	switch state {
	case 0:
		return "active (open)"
	case 1:
		return "ready (closed)"
	case 2:
		return "destroyed"
	case 24:
		return "transport active"
	case 25:
		return "transport stopped"
	}
	return fmt.Sprintf("state %d", state)
}
