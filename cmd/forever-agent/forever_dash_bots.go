package main

// forever dash-bots: the roster of a ForeverCore server's dashboard, and a watch over it that
// reports the changes worth a line: a death, a bot that says moving but stands still, a bot in
// combat with no group mate in it, and a bot far from its group. The password comes from a
// private file or FOREVER_DASH_PASSWORD, never a flag, and is never printed; names and the
// dashboard's strings are sanitised before they reach the terminal.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/roster"
	"github.com/pjerra/forever-agent/internal/tbdash"
)

// dashBotsPasswordEnv is the only environment variable the command reads for the password.
const dashBotsPasswordEnv = "FOREVER_DASH_PASSWORD"

// exitDashBotsFailed is a login failure, a read failure outside a watch, or a watch that gave up.
const exitDashBotsFailed = 1

// dashWatchGiveUp is how many consecutive failed reads end a watch.
const dashWatchGiveUp = 5

const dashBotsUsage = "usage: forever-agent forever dash-bots --url URL --user NAME [--pass-file FILE] [--dash-insecure-http] [--watch [--interval 2s]] [--stuck-after 20s] [--lone-combat-after 5s] [--group-distance 60] [--timeout 15s]\n" +
	"  the password is read from --pass-file (mode 0600) or " + dashBotsPasswordEnv + ", never a flag\n" +
	"  a plain-http dashboard is allowed only on this machine unless --dash-insecure-http is given"

func foreverDashBotsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever dash-bots", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, dashBotsUsage) }
	dashURL := fs.String("url", "", "the dashboard's root URL")
	user := fs.String("user", "", "the GM game account the dashboard logs in as")
	passFile := fs.String("pass-file", "", "the password file (mode 0600); without it the password comes from "+dashBotsPasswordEnv)
	insecureHTTP := fs.Bool("dash-insecure-http", false, "allow a plain-http dashboard that is not loopback: the GM password and the session cookie then travel in clear text")
	watch := fs.Bool("watch", false, "poll and report changes instead of printing the roster once")
	interval := fs.Duration("interval", 2*time.Second, "with --watch: how often the roster is read")
	stuckAfter := fs.Duration("stuck-after", roster.DefaultStuckAfter, "with --watch: how long a moving bot's position may stay put before it is reported stuck")
	loneCombatAfter := fs.Duration("lone-combat-after", roster.DefaultLoneCombatAfter, "with --watch: the grace a bot in combat gets for a group mate to join before it is reported fighting alone")
	groupDistance := fs.Float64("group-distance", roster.DefaultGroupDistance, "with --watch: how many yards from its group's centre a bot may be before it is reported")
	timeout := fs.Duration("timeout", roster.DefaultTimeout, "the bound on one dashboard request, the login included")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "forever dash-bots: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprintln(stderr, dashBotsUsage)
		return exitUsage
	}
	if *dashURL == "" || *user == "" {
		fmt.Fprintln(stderr, "forever dash-bots: --url and --user are required")
		fmt.Fprintln(stderr, dashBotsUsage)
		return exitUsage
	}
	var src tbdash.PasswordSource
	switch {
	case *passFile != "":
		src = tbdash.PasswordFile(*passFile)
	case os.Getenv(dashBotsPasswordEnv) != "":
		src = tbdash.PasswordEnv(dashBotsPasswordEnv)
	default:
		fmt.Fprintf(stderr, "forever dash-bots: no password: --pass-file or %s is required\n", dashBotsPasswordEnv)
		fmt.Fprintln(stderr, dashBotsUsage)
		return exitUsage
	}
	if *interval < 100*time.Millisecond || *interval > 5*time.Minute {
		fmt.Fprintln(stderr, "forever dash-bots: --interval must be between 100ms and 5m")
		return exitUsage
	}
	if *stuckAfter <= 0 || *stuckAfter > time.Hour {
		fmt.Fprintln(stderr, "forever dash-bots: --stuck-after must be above 0 and at most 1h")
		return exitUsage
	}
	if *loneCombatAfter <= 0 || *loneCombatAfter > time.Hour {
		fmt.Fprintln(stderr, "forever dash-bots: --lone-combat-after must be above 0 and at most 1h")
		return exitUsage
	}
	if *groupDistance <= 0 || *groupDistance > 10000 {
		fmt.Fprintln(stderr, "forever dash-bots: --group-distance must be above 0 and at most 10000 yards")
		return exitUsage
	}
	if *timeout < time.Second || *timeout > 2*time.Minute {
		fmt.Fprintln(stderr, "forever dash-bots: --timeout must be between 1s and 2m")
		return exitUsage
	}

	c, err := roster.New(*dashURL, *user, src, roster.Options{
		Timeout:           *timeout,
		AllowInsecureHTTP: *insecureHTTP,
	})
	if err != nil {
		fmt.Fprintf(stderr, "forever dash-bots: %v\n", err)
		return exitUsage
	}
	if *insecureHTTP {
		warnInsecureDash(stderr, "forever dash-bots", *dashURL)
	}
	if err := c.Login(ctx); err != nil {
		if ctx.Err() != nil {
			return exitOK
		}
		fmt.Fprintf(stderr, "forever dash-bots: %v\n", err)
		return exitDashBotsFailed
	}

	if !*watch {
		r, err := c.Roster(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return exitOK
			}
			fmt.Fprintf(stderr, "forever dash-bots: %v\n", err)
			return exitDashBotsFailed
		}
		printDashRejections(stderr, r.Rejected)
		printDashRoster(stdout, r)
		return exitOK
	}

	cfg := roster.Config{
		StuckAfter:      *stuckAfter,
		LoneCombatAfter: *loneCombatAfter,
		GroupDistance:   *groupDistance,
	}
	tracker := roster.NewTracker(cfg)
	primed := false
	failed := 0
	lastRejected := ""
	for {
		at := time.Now()
		r, err := c.Roster(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			return exitOK
		case err != nil:
			failed++
			fmt.Fprintf(stderr, "forever dash-bots: %v\n", err)
			if failed >= dashWatchGiveUp {
				fmt.Fprintf(stderr, "forever dash-bots: giving up after %d failed reads\n", failed)
				return exitDashBotsFailed
			}
		default:
			failed = 0
			if sig := dashRejectionSignature(r.Rejected); sig != lastRejected {
				lastRejected = sig
				printDashRejections(stderr, r.Rejected)
			}
			events := tracker.Observe(at, r)
			if !primed {
				// The first sample is the baseline: its states and positions are in the roster
				// the watch prints, so they are not changes.
				printDashRoster(stdout, r)
				primed = true
			} else {
				for _, e := range events {
					printDashEvent(stdout, e)
				}
			}
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return exitOK
		case <-timer.C:
		}
	}
}

// printDashRoster prints the heartbeat when the dashboard sends one, then one line per bot.
func printDashRoster(w io.Writer, r *roster.Roster) {
	if hb := r.Heartbeat; hb != nil {
		fmt.Fprintf(w, "roster: %d bot(s), %d human(s)", hb.Bots, hb.Humans)
		if len(hb.States) > 0 {
			names := make([]string, 0, len(hb.States))
			for name := range hb.States {
				names = append(names, name)
			}
			sort.Strings(names)
			parts := make([]string, 0, len(names))
			for _, name := range names {
				parts = append(parts, fmt.Sprintf("%s %g", safeName(name), hb.States[name]))
			}
			fmt.Fprintf(w, "; states %s", strings.Join(parts, ", "))
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintf(w, "roster: %d bot(s)\n", len(r.Bots))
	}
	bots := append([]roster.Bot(nil), r.Bots...)
	sort.Slice(bots, func(i, j int) bool {
		if bots[i].Name != bots[j].Name {
			return bots[i].Name < bots[j].Name
		}
		return bots[i].GUID < bots[j].GUID
	})
	for _, b := range bots {
		fmt.Fprintf(w, "  %s  %s %d  %s  %s  hp %d/%d  %s %d/%d  map %d %s%s  (%.1f, %.1f, %.1f)  target %s  group %s\n",
			safeName(b.Name),
			dashText(string(b.Class)), b.Level,
			dashText(string(b.Kind)), dashText(string(b.State)),
			b.HP, b.MaxHP,
			dashText(string(b.PowerType)), b.Power, b.MaxPower,
			b.Map, dashText(string(b.Zone)), dashInstance(b.Instance),
			b.X, b.Y, b.Z,
			dashText(string(b.Target)), dashText(string(b.Group)))
	}
}

// printDashEvent prints one watch event with the server's clock time.
func printDashEvent(w io.Writer, e roster.Event) {
	where := fmt.Sprintf("map %d", e.Map)
	if e.Zone != "" {
		where += " " + safeName(string(e.Zone))
	}
	if e.Instance != "" {
		where += " instance " + safeName(string(e.Instance))
	}
	switch e.Kind {
	case roster.EventDeath:
		fmt.Fprintf(w, "%s  death  %s died  (%s)\n", e.At.Format("15:04:05"), safeName(e.Bot), where)
	case roster.EventStuck:
		fmt.Fprintf(w, "%s  stuck  %s  no movement for %s at %.1f, %.1f, %.1f  (%s)\n",
			e.At.Format("15:04:05"), safeName(e.Bot), e.For.Round(time.Second), e.X, e.Y, e.Z, where)
	case roster.EventLoneCombat:
		fmt.Fprintf(w, "%s  lone-combat  %s  in combat for %s with no group mate in combat  (%s)\n",
			e.At.Format("15:04:05"), safeName(e.Bot), e.For.Round(time.Second), where)
	case roster.EventFarFromGroup:
		fmt.Fprintf(w, "%s  far-from-group  %s  %.1f yd from its group  (%s)\n",
			e.At.Format("15:04:05"), safeName(e.Bot), e.Distance, where)
	default:
		fmt.Fprintf(w, "%s  %s  %s  (%s)\n", e.At.Format("15:04:05"), dashText(string(e.Kind)), safeName(e.Bot), where)
	}
}

// dashText sanitises a dashboard string and shows a dash for an empty one.
func dashText(s string) string {
	if s == "" {
		return "-"
	}
	return safeName(s)
}

func dashInstance(s roster.Text) string {
	if s == "" {
		return ""
	}
	return " instance " + safeName(string(s))
}

// dashRejectionSignature identifies one sample's rejected records, so a watch prints a
// standing rejection once instead of every interval.
func dashRejectionSignature(rejected []roster.Rejection) string {
	if len(rejected) == 0 {
		return ""
	}
	var b strings.Builder
	for _, rej := range rejected {
		fmt.Fprintf(&b, "%d:%s;", rej.Index, rej.Reason)
	}
	return b.String()
}

// printDashRejections reports the records DecodeRoster left out. A rejected record is never a
// death, a stuck flag or a distance: it is not in the roster the rules see.
func printDashRejections(w io.Writer, rejected []roster.Rejection) {
	for _, rej := range rejected {
		name := ""
		if rej.Name != "" {
			name = " " + safeName(rej.Name)
		}
		fmt.Fprintf(w, "forever dash-bots: bot record %d%s skipped: %s\n", rej.Index, name, cleanDashReason(rej.Reason))
	}
}

// cleanDashReason makes a decoder reason safe for a terminal without cutting it to uselessness.
func cleanDashReason(s string) string {
	const max = 200
	truncated := len(s) > max
	if truncated {
		s = s[:max]
	}
	out := []byte(s)
	for i := range out {
		if out[i] < 0x20 || out[i] > 0x7e {
			out[i] = '?'
		}
	}
	if truncated {
		out = append(out, '.', '.', '.')
	}
	return string(out)
}
