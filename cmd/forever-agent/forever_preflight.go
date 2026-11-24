package main

// forever preflight, and the preflight the three Forever run commands (dungeon-watch, dungeon-run, dungeon-play) make
// before they log in: the serve slot, its lease, the plan and its export, the module answering over the serve link, the
// SOAP console and the bots named on the command line (internal/forever/fpreflight). Read-only: a failed check ends the
// command with exitPreflight before a session is opened; a warning is printed and the run goes on.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/fpreflight"
	"github.com/pjerra/forever-agent/internal/forever/soap"
)

// exitPreflight is a preflight that found a failure: the run did not start.
const exitPreflight = 13

const foreverPreflightUsage = "usage: forever-agent forever preflight --mode dungeon-watch|dungeon-run|dungeon-play --plan FILE --character NAME [--leader NAME | --party A,B,C,D,E | --bots A,B,C,D] [--serve-dir DIR] [--export FILE|DIR] [--soap-host H] [--soap-port N] [--soap-user NAME] [--soap-password-file FILE | --soap-password-env VAR]"

// preflightFlags are the flags the run commands share with `forever preflight`.
type preflightFlags struct {
	skip, only   bool
	serveDir     string
	export       string
	soapHost     string
	soapPort     int
	soapUser     string
	soapPassFile string
	soapPassEnv  string
}

// register adds the shared flags. --no-preflight is only for the run commands; `forever preflight` is a preflight.
func (p *preflightFlags) register(fs *flag.FlagSet, runCommand bool) {
	if runCommand {
		fs.BoolVar(&p.skip, "no-preflight", false, "start without the preflight (serve slot, lease, plan export, module, SOAP and bots)")
		fs.BoolVar(&p.only, "preflight-only", false, "run the preflight and stop: no login, no run")
	}
	fs.StringVar(&p.serveDir, "serve-dir", "", "the forever serve --dir whose slot, lease and module link the preflight checks")
	fs.StringVar(&p.export, "export", "", "the plan's exported migration (plan_<name>.sql) or its folder")
	fs.StringVar(&p.soapHost, "soap-host", envOr("FOREVER_SOAP_HOST", soap.DefaultHost), "SOAP endpoint host (loopback only)")
	fs.IntVar(&p.soapPort, "soap-port", envPort("FOREVER_SOAP_PORT", soap.DefaultPort), "SOAP endpoint port")
	fs.StringVar(&p.soapUser, "soap-user", os.Getenv("FOREVER_SOAP_USER"), "admin account the SOAP endpoint authenticates; empty skips the SOAP checks")
	fs.StringVar(&p.soapPassFile, "soap-password-file", os.Getenv("FOREVER_SOAP_PASSWORD_FILE"), "private file (0600) holding the SOAP password")
	fs.StringVar(&p.soapPassEnv, "soap-password-env", "", "environment variable holding the SOAP password (default "+soap.DefaultPasswordEnv+")")
}

// check refuses a combination that cannot work: a preflight-only on a dry run has nothing to check.
func (p *preflightFlags) check(cmd string, dry bool, stderr io.Writer) bool {
	switch {
	case p.only && p.skip:
		fmt.Fprintf(stderr, "%s: --preflight-only and --no-preflight contradict each other\n", cmd)
	case p.only && dry:
		fmt.Fprintf(stderr, "%s: --preflight-only checks a live run; drop --dry\n", cmd)
	default:
		return true
	}
	return false
}

// console is the SOAP client the flags name, or nil when no user is given.
func (p *preflightFlags) console() (fpreflight.Console, error) {
	if p.soapUser == "" {
		return nil, nil
	}
	cfg := soap.Config{Host: p.soapHost, Port: p.soapPort, User: p.soapUser, PasswordFile: p.soapPassFile, PasswordEnv: p.soapPassEnv}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return soap.New(cfg)
}

// preflightDeps builds the effects of one preflight; a test replaces it.
var preflightDeps = fpreflight.DefaultDeps

// runPreflight does the preflight of a run command. stop is true when the command must end with code: a failed
// check, or --preflight-only. A skipped preflight is not stop.
func runPreflight(ctx context.Context, cmd, kind string, p *preflightFlags, plan func() (string, uint32, error), character string, bots []string, stdout, stderr io.Writer) (stop bool, code int) {
	if p.skip {
		return false, exitOK
	}
	console, err := p.console()
	if err != nil {
		fmt.Fprintf(stderr, "%s: preflight: %v\n", cmd, err)
		return true, exitUsage
	}
	rep := fpreflight.Run(ctx, fpreflight.Options{
		Kind: kind, ServeDir: p.serveDir, Plan: plan, Export: p.export, Character: character, Bots: bots,
	}, preflightDeps(console))
	rep.Print(stdout)
	if rep.Blocked() {
		fmt.Fprintf(stderr, "%s: the preflight failed; nothing was started (--no-preflight skips it)\n", cmd)
		return true, exitPreflight
	}
	return p.only, exitOK
}

func foreverPreflightCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever preflight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var pf preflightFlags
	pf.register(fs, false)
	mode := fs.String("mode", "", "the run to check: dungeon-watch, dungeon-run or dungeon-play")
	plan := fs.String("plan", "", "dungeon plan file")
	character := fs.String("character", "", "the commander or player character")
	leader := fs.String("leader", "", "dungeon-watch: the bot that leads the module's run")
	party := fs.String("party", "", "dungeon-run: the five party members")
	bots := fs.String("bots", "", "dungeon-play: the four bots")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *plan == "" || *character == "" {
		fmt.Fprintln(stderr, foreverPreflightUsage)
		return exitUsage
	}
	var load func(string) (*driver.Route, error)
	var names []string
	switch *mode {
	case fpreflight.KindWatch:
		load = driver.LoadWatchRoute
		if *leader == "" {
			fmt.Fprintln(stderr, "forever preflight: dungeon-watch needs --leader")
			return exitUsage
		}
		names = []string{*leader}
	case fpreflight.KindRun:
		load = driver.LoadRoute
		p, err := splitParty(*party)
		if err != nil {
			fmt.Fprintf(stderr, "forever preflight: %v\n", err)
			return exitUsage
		}
		names = p
	case fpreflight.KindPlay:
		load = driver.LoadPlayRoute
		b, err := splitBots(*bots)
		if err != nil {
			fmt.Fprintf(stderr, "forever preflight: %v\n", err)
			return exitUsage
		}
		names = b[:]
	default:
		fmt.Fprintln(stderr, foreverPreflightUsage)
		return exitUsage
	}
	pf.only = true
	if _, code := runPreflight(ctx, "forever preflight", *mode, &pf, fpreflight.PlanLoader(load, *plan), *character, names, stdout, stderr); code != exitOK {
		return code
	}
	return exitOK
}
