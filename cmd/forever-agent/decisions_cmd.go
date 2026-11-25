// decisions_cmd.go: `forever-agent decisions pull` reads a commander's decision captures into <out>/decisions. It has
// two modes: the Turtle dashboard pull (a daemon's JSON API, reused from the walk's pullDecisionsInto so the
// standalone command and the walk pull the same files) and the Forever module read (the bot module's decision rows of
// one run, through the session's command channel, written as <run_id>.decisions.jsonl). A mode is chosen by its
// address flags: --dash-url for the dashboard, --leader or --run for the module. The password never reaches an error
// text or stdout.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/tbdash"
)

const decisionsPullUsage = `usage: forever-agent decisions pull --dash-url URL --dash-user U --dash-pass-file FILE [--dash-insecure-http] --commander C [--since T] [--until T] --out DIR [--max-mib N]
       forever-agent decisions pull (--leader NAME | --run ID) --out DIR [--since-seq N] [--limit N] [--rest host:port --rpc host:port --ca FILE] --account EMAIL --character NAME`

// decisionsPullOptions are the pull's flags: the dashboard mode's own set and the module mode's.
type decisionsPullOptions struct {
	dashURL          string
	dashUser         string
	passFile         string
	dashInsecureHTTP bool
	command          string
	since            string
	until            string
	out              string
	maxMiB           int
	// The module mode: a leader or a run id names the run the bot module reads, sinceSeq starts above an earlier
	// cursor and limit is the page the module is asked for (default and cap 200).
	common     foreverCommon
	leader     string
	runWord    string
	sinceSeq   int
	sinceGiven bool
	limit      int
}

// moduleMode says which address the flags chose: a leader or a run id is the module read, anything else the dashboard.
func (o decisionsPullOptions) moduleMode() bool { return o.leader != "" || o.runWord != "" }

// target is the module's one word: the run id when it was given, else the leader's name.
func (o decisionsPullOptions) target() string {
	if o.runWord != "" {
		return o.runWord
	}
	return o.leader
}

// decisionsCmd is the `forever-agent decisions` entry.
func decisionsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "pull" {
		fmt.Fprintln(stderr, decisionsPullUsage)
		return exitUsage
	}
	o, code := parseDecisionsPullArgs(args[1:], stderr)
	if code != exitOK {
		return code
	}
	if o.moduleMode() {
		return decisionsPullModule(ctx, o, stdout, stderr)
	}
	since, until, ok := parseDecisionsWindow(o, stderr)
	if !ok {
		return exitUsage
	}
	if o.dashInsecureHTTP {
		warnInsecureDash(stderr, "decisions", o.dashURL)
	}
	c, err := openDecisionsClient(o.dashURL, o.dashUser, o.passFile, o.dashInsecureHTTP)
	if err != nil {
		fmt.Fprintf(stderr, "decisions: %v\n", err)
		return exitRaidWalkFailed
	}
	if err := c.Login(ctx); err != nil {
		fmt.Fprintf(stderr, "decisions: %v\n", sanitizeDecisionsErr(err))
		return exitRaidWalkFailed
	}
	maxBytes := int64(o.maxMiB) * 1024 * 1024
	pullCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, notes := pullDecisionsInto(pullCtx, c, o.command, since, until, o.out, maxBytes)
	for _, n := range notes {
		fmt.Fprintf(stderr, "decisions: %s\n", n)
	}
	fmt.Fprintf(stdout, "decisions: pulled %d captures (%d bytes) into %s/decisions\n", len(res.Captures), res.Bytes, o.out)
	// Budget exhaustion has its own message; every other failure shares the notes above.
	if res.BudgetHit {
		fmt.Fprintf(stderr, "decisions: budget stopped the pull at %d MiB\n", o.maxMiB)
		return exitRaidWalkFailed
	}
	if res.ListFailed || res.ManifestFailed || res.CtxExpired || res.Incomplete > 0 || pullCtx.Err() != nil {
		return exitRaidWalkFailed
	}
	return exitOK
}

// enterDecisionsPull is the module mode's session entry: a var so a test reaches the report without a server.
var enterDecisionsPull = func(ctx context.Context, o *decisionsPullOptions, stdout io.Writer) (*world.Report, error) {
	return o.common.enter(ctx, "decisions pull", stdout, world.Options{
		Deadline: 10 * time.Minute,
		Settle:   2 * time.Second,
		DecisionsPull: &world.DecisionsPullOptions{
			Target: o.target(), Since: o.sinceSeq, HasSince: o.sinceGiven, Limit: o.limit, Out: o.out,
		},
	})
}

// decisionsPullModule is the Forever mode: log in, read the module's rows of the run through the command channel, and
// write them. The read's own failures (the command permission, a refusal code, a module build without the command)
// are this command's whole answer, so they exit nonzero with the reason.
func decisionsPullModule(ctx context.Context, o decisionsPullOptions, stdout, stderr io.Writer) int {
	const cmdName = "decisions pull"
	if err := o.common.check(cmdName); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := o.common.passwordCheck(cmdName); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	report, err := enterDecisionsPull(ctx, &o, stdout)
	if code, isLogin := loginExit(err, stderr); isLogin {
		return code
	}
	if report != nil && report.Decisions != nil {
		d := report.Decisions
		if d.Rows == 0 {
			fmt.Fprintln(stdout, "decisions: the module has no decision row for this run (nothing written)")
		} else {
			fmt.Fprintf(stdout, "decisions: pulled %d row(s) in %d page(s) (%d bytes) into %s\n", d.Rows, d.Pages, d.Bytes, report.DecisionsFile)
		}
		if d.PageBound && d.Note != "" {
			fmt.Fprintf(stderr, "decisions: %s\n", d.Note)
		}
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "decisions: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stderr, "decisions: the session ended without a pull")
	return exitWorld
}

func parseDecisionsPullArgs(args []string, stderr io.Writer) (decisionsPullOptions, int) {
	var o decisionsPullOptions
	fs := flag.NewFlagSet("decisions-pull", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, decisionsPullUsage) }
	fs.StringVar(&o.dashURL, "dash-url", "", "the dashboard URL")
	fs.StringVar(&o.dashUser, "dash-user", "TADASH", "the dashboard login")
	fs.StringVar(&o.passFile, "dash-pass-file", "", "the dashboard password file")
	fs.BoolVar(&o.dashInsecureHTTP, "dash-insecure-http", false, "allow a plain-http dashboard that is not loopback: the GM password and the session cookie then travel in clear text")
	fs.StringVar(&o.command, "commander", "", "the commander to pull")
	fs.StringVar(&o.since, "since", "", "RFC3339 start of the window")
	fs.StringVar(&o.until, "until", "", "RFC3339 end of the window")
	fs.StringVar(&o.out, "out", "", "the output dir")
	fs.IntVar(&o.maxMiB, "max-mib", decisionsBudgetDefaultMiB, "the pull budget in MiB")
	o.common.flags(fs)
	fs.StringVar(&o.leader, "leader", "", "the bot that leads the module's run; with --run, the module read instead of the dashboard")
	fs.StringVar(&o.runWord, "run", "", "the module's run id; with --leader, the module read instead of the dashboard")
	fs.IntVar(&o.sinceSeq, "since-seq", 0, "the module read: start above this decision sequence")
	fs.IntVar(&o.limit, "limit", 25, "the module read: rows asked for per page (1-200; a page over the reply bound is halved)")
	if err := fs.Parse(args); err != nil {
		return o, exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "decisions: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprintln(stderr, decisionsPullUsage)
		return o, exitUsage
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "since-seq" {
			o.sinceGiven = true
		}
	})
	if o.moduleMode() {
		return parseDecisionsModuleArgs(o, fs, stderr)
	}
	var missing []string
	if o.dashURL == "" {
		missing = append(missing, "--dash-url")
	}
	if o.passFile == "" {
		missing = append(missing, "--dash-pass-file")
	}
	if o.command == "" {
		missing = append(missing, "--commander")
	}
	if o.out == "" {
		missing = append(missing, "--out")
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "decisions: missing %s\n", joinWords(missing))
		fmt.Fprintln(stderr, decisionsPullUsage)
		return o, exitUsage
	}
	if o.maxMiB <= 0 {
		fmt.Fprintf(stderr, "decisions: --max-mib must be above 0\n")
		return o, exitUsage
	}
	return o, exitOK
}

// parseDecisionsModuleArgs checks the module mode's own flags: one address, an output folder, a page inside the
// command's clamp, and no dashboard flag left over (a half dashboard invocation never becomes a module read).
func parseDecisionsModuleArgs(o decisionsPullOptions, fs *flag.FlagSet, stderr io.Writer) (decisionsPullOptions, int) {
	if o.leader != "" && o.runWord != "" {
		fmt.Fprintln(stderr, "decisions: --leader and --run name one run together; give one")
		return o, exitUsage
	}
	var stray []string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "dash-url", "dash-user", "dash-pass-file", "dash-insecure-http", "commander", "since", "until", "max-mib":
			stray = append(stray, "--"+f.Name)
		}
	})
	if len(stray) > 0 {
		fmt.Fprintf(stderr, "decisions: %s belong to the dashboard mode, not to a module read\n", joinWords(stray))
		return o, exitUsage
	}
	if o.out == "" {
		fmt.Fprintln(stderr, "decisions: the module read needs --out (the folder of the file)")
		fmt.Fprintln(stderr, decisionsPullUsage)
		return o, exitUsage
	}
	if o.sinceSeq < 0 {
		fmt.Fprintln(stderr, "decisions: --since-seq is 0 or more")
		return o, exitUsage
	}
	if o.limit < 1 || o.limit > 200 {
		fmt.Fprintln(stderr, "decisions: --limit is 1-200")
		return o, exitUsage
	}
	return o, exitOK
}

// openDecisionsClient builds the dashboard client; a var so tests hand in a fake.
var openDecisionsClient = func(base, user, passFile string, insecure bool) (decisionsPuller, error) {
	return tbdash.NewWithOptions(base, user, tbdash.PasswordFile(passFile), tbdash.Options{AllowInsecureHTTP: insecure})
}

// sanitizeDecisionsErr drops nothing sensitive: the dashboard client never carries the password
// into an error, so this is the identity. It exists so a future message cannot leak it by accident.
func sanitizeDecisionsErr(err error) error { return err }

func parseDecisionsWindow(o decisionsPullOptions, stderr io.Writer) (time.Time, time.Time, bool) {
	var since, until time.Time
	if o.since != "" {
		t, err := time.Parse(time.RFC3339, o.since)
		if err != nil {
			fmt.Fprintf(stderr, "decisions: --since wants RFC3339, not %q\n", o.since)
			return since, until, false
		}
		since = t
	}
	if o.until != "" {
		t, err := time.Parse(time.RFC3339, o.until)
		if err != nil {
			fmt.Fprintf(stderr, "decisions: --until wants RFC3339, not %q\n", o.until)
			return since, until, false
		}
		until = t
	}
	return since, until, true
}

func joinWords(words []string) string {
	out := ""
	for i, word := range words {
		if i > 0 {
			out += ", "
		}
		out += word
	}
	return out
}
