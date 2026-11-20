package main

// forever fight-check: round 4's live command. It logs in like walk-check, settles in the world, chooses a target
// (the nearest living attackable creature of level 1-3 within 40 yd, or the one with --entry), attacks it for
// --fight-for, casts the spells the character really knows (see internal/forever/world/fightcheck.go), prints the
// fight log and logs out. It carries no identifiers: positions are offsets, units are named by role, entries,
// levels, spell ids and damage are game values.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverFightCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever fight-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--entry N] [--level-min N] [--level-max N] [--fight-for DURATION] [--capture-dir DIR] [--provoke]"
	fs := flag.NewFlagSet("forever fight-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 8*time.Second, "how long to settle in the world before choosing the target")
	entry := fs.Int64("entry", 0, "creature template entry to fight (0: the nearest living creature of the level band within 40 yd)")
	levelMin := fs.Int64("level-min", 0, "lowest target level (0: three below the character's own level)")
	levelMax := fs.Int64("level-max", 0, "highest target level (0: two above the character's own level)")
	fightFor := fs.Duration("fight-for", 60*time.Second, "how long to fight before giving up")
	captureDir := fs.String("capture-dir", "", "debug: write every packet the combat, chat and party handlers see into a new run directory under this path (a scratch folder OUTSIDE the repository)")
	provoke := fs.Bool("provoke", false, "debug: before the fight, provoke the refusals a fight log otherwise has no real packet for (out-of-range swing, cast at no rage)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever fight-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever fight-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	if *fightFor <= 0 || *fightFor > 5*time.Minute {
		fmt.Fprintln(stderr, "forever fight-check: --fight-for must be between 0 and 5m")
		return exitUsage
	}
	if *entry < 0 || *entry > 1<<24 {
		fmt.Fprintln(stderr, "forever fight-check: --entry must be a creature template entry")
		return exitUsage
	}
	for _, l := range []struct {
		name string
		v    int64
	}{{"--level-min", *levelMin}, {"--level-max", *levelMax}} {
		if l.v < 0 || l.v > 60 {
			fmt.Fprintf(stderr, "forever fight-check: %s must be between 0 (the character's own level) and 60\n", l.name)
			return exitUsage
		}
	}
	if *levelMin != 0 && *levelMax != 0 && *levelMin > *levelMax {
		fmt.Fprintln(stderr, "forever fight-check: --level-min is above --level-max")
		return exitUsage
	}
	if err := common.passwordCheck("forever fight-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever fight-check", stdout, world.Options{
		FightCheck: true,
		Entry:      int32(*entry),
		LevelMin:   int32(*levelMin),
		LevelMax:   int32(*levelMax),
		FightFor:   *fightFor,
		CaptureDir: *captureDir,
		Provoke:    *provoke,
		Settle:     *stay,
		Deadline:   *fightFor + 4*time.Minute,
	})
	if report != nil {
		printLinkLost(stdout, report)
		printFightCheckSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever fight-check: %v\n", err)
		if errors.Is(err, world.ErrLinkLost) {
			// The connection is gone: its own code, and no reconnect (the brief forbids papering over a disconnect).
			return exitDisconnected
		}
		if errors.Is(err, world.ErrSelfDead) {
			// The character died: its own code, so a script can tell a death from a refused session.
			return exitDead
		}
		return exitWorld
	}
	return exitOK
}

// printLinkLost prints which socket was lost and the last packet it delivered: the two facts a script needs to tell
// a server that closed the connection from one that stopped answering.
func printLinkLost(w io.Writer, report *world.Report) {
	if report == nil || !report.LinkLost {
		return
	}
	last := report.LastPacket
	if last == "" {
		last = "none"
	}
	fmt.Fprintf(w, "fight-check: connection lost on the %s socket (%s); the last server packet was %s, %s before the end\n",
		report.LinkLostSide, report.LinkLostDetail, last, report.LastPacketAgo.Round(time.Millisecond))
}

// printFightCheckSummary prints the final state of the fight: counts, damage, the aura transitions, the character's
// health and power, and the experience the server granted.
func printFightCheckSummary(w io.Writer, report *world.Report) {
	f := report.Fight
	if f.Target.IsZero() {
		fmt.Fprintf(w, "fight-check: no target was fought (the model held %d objects)\n", report.Objects.Objects)
		return
	}
	verdict := "the target survived"
	switch {
	case f.Killed:
		verdict = "the target died"
	case f.Evaded:
		verdict = "the target got away (the server stopped the attack with it alive)"
	}
	fmt.Fprintf(w, "fight-check: %s: entry %d level %d, %d/%d health left\n",
		verdict, f.TargetEntry, f.TargetLevel, f.TargetHealth, f.TargetMax)
	fmt.Fprintf(w, "fight-check: %d swing(s) hit, %d miss(es), %d cast(s), %d aura event(s)\n",
		f.Swings, f.Misses, f.Casts, f.Auras)
	fmt.Fprintf(w, "fight-check: damage dealt %d, damage taken %d\n", f.DamageDealt, f.DamageTaken)
	fmt.Fprintf(w, "fight-check: you are at %d/%d health, %s %s\n",
		f.SelfHealth, f.SelfMax, combat.PowerText(int8(f.SelfPowerType), f.SelfPower, f.SelfMaxPower),
		combat.PowerName(int8(f.SelfPowerType)))
	if f.XPGain != 0 {
		fmt.Fprintf(w, "fight-check: experience gained %d\n", f.XPGain)
	}
	if len(f.CastRows) > 0 {
		fmt.Fprintf(w, "fight-check: casts:")
		for _, c := range f.CastRows {
			fmt.Fprintf(w, " %d:%s", c.SpellID, c.Result)
		}
		fmt.Fprintln(w)
	}
	if len(f.AuraRows) > 0 {
		var gained, lost int
		for _, a := range f.AuraRows {
			if a.Gained {
				gained++
			} else {
				lost++
			}
		}
		fmt.Fprintf(w, "fight-check: auras seen: %d gained, %d lost\n", gained, lost)
	}
	if len(report.KnownSpells) > 0 {
		fmt.Fprintf(w, "fight-check: the character knows %d spell(s)\n", len(report.KnownSpells))
	}
	if report.Captured > 0 {
		fmt.Fprintf(w, "fight-check: %d packet(s) written to the debug capture\n", report.Captured)
	}
}
