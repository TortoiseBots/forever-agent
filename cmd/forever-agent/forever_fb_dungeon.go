package main

// forever fb-dungeon: round A2's live command. It logs in like fb-check, fills the party to five with the module's
// `.bot lfg`, queues a dungeon with `.bot dungeon`, waits until BOTH the module's finder says `inside` AND the
// client's own map change confirmed the instance, hands the lead to the tank bot, watches for the requested time
// reporting the finder's state, the bots' health and deaths, the tank's party lines and the module's event pushes,
// then leads off, leaves the dungeon and dismisses the bots.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/fbcmd"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverFBDungeonCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever fb-dungeon [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--dungeon NAME] [--role tank|healer|dps] [--watch SECONDS] [--leave=false] [--settle DURATION] [--stay DURATION] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever fb-dungeon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	dungeon := fs.String("dungeon", "rfc", "the dungeon word the module's finder knows (a short word like rfc, a name, or any)")
	role := fs.String("role", "", "the player's own role (tank, healer, dps); empty lets the module read it from the class")
	watch := fs.Int("watch", 600, "seconds to watch inside the dungeon, reporting health, deaths, party lines and events")
	leave := fs.Bool("leave", true, "leave the dungeon when the watch ends (--leave=false stays inside)")
	settle := fs.Duration("settle", 4*time.Second, "how long to settle in the world before the first command")
	stay := fs.Duration("stay", 0, "how long to stay in the world after the last step")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat, party and add-on handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever fb-dungeon"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := fbcmd.ValidateDungeonWord(*dungeon); err != nil {
		fmt.Fprintln(stderr, "forever fb-dungeon: --dungeon:", err)
		return exitUsage
	}
	if err := fbcmd.ValidateRole(*role); err != nil {
		fmt.Fprintln(stderr, "forever fb-dungeon: --role:", err)
		return exitUsage
	}
	if *watch < 0 || *watch > 3600 {
		fmt.Fprintln(stderr, "forever fb-dungeon: --watch must be between 0 and 3600 seconds")
		return exitUsage
	}
	if *settle < 0 || *settle > time.Minute || *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever fb-dungeon: --settle is at most 1m and --stay at most 10m")
		return exitUsage
	}
	if err := common.passwordCheck("forever fb-dungeon"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever fb-dungeon", stdout, world.Options{
		Stay:           *stay,
		Settle:         *settle,
		FBDungeon:      true,
		FBDungeonName:  *dungeon,
		FBDungeonRole:  *role,
		FBDungeonWatch: time.Duration(*watch) * time.Second,
		FBDungeonLeave: *leave,
		CaptureDir:     *captureDir,
	})
	if report != nil {
		for _, line := range world.FBDungeonSummary(report.FBDungeon) {
			fmt.Fprintln(stdout, line)
		}
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever fb-dungeon: %v\n", err)
		return exitWorld
	}
	return exitOK
}
