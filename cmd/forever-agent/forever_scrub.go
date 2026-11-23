package main

// forever scrub-check: a `forever serve --capture-dir` capture must not carry the runner's secrets before it is
// shared. The needles are every account of a slot under a server with a `forever` block in the slots file, each
// --account (raw and upper case), and the account password (--password-file, else FOREVER_PASSWORD); every file of
// every run is read in every form capture.Variants lists. A finding names the run and the file, never the secret.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/slots"
)

const foreverScrubUsage = "usage: forever-agent forever scrub-check [--slots FILE] [--account EMAIL]... [--password-file FILE] <dir>..."

// scrubAccounts collects the repeatable --account flag.
type scrubAccounts []string

func (s *scrubAccounts) String() string { return strings.Join(*s, ",") }

func (s *scrubAccounts) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// foreverScrubCmd checks each capture directory. Exit 0 is clean, exitScrub (7) a finding, exitUsage (2) a usage,
// settings, password or read error. A run needs at least one needle.
func foreverScrubCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever scrub-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slotsPath := fs.String("slots", "", "slots file whose Forever servers' slot accounts must not appear")
	pwPath := fs.String("password-file", "", "file holding the password (default: FOREVER_PASSWORD)")
	var accounts scrubAccounts
	fs.Var(&accounts, "account", "an account e-mail that must not appear (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, foreverScrubUsage)
		return exitUsage
	}
	// The needles, in order: the slots file's Forever accounts, each --account, then the password. A finding with a
	// needle index below accountNeedles is an account name; the password is the last needle.
	var needles [][]byte
	if *slotsPath != "" {
		cfg, err := slots.Load(*slotsPath)
		if err != nil {
			fmt.Fprintln(stderr, "forever scrub-check:", err)
			return exitUsage
		}
		for _, srv := range cfg.Servers {
			if _, ok := cfg.Forever[srv.Name]; !ok {
				continue // a Turtle server's accounts are not Forever secrets
			}
			for _, sl := range srv.Slots {
				if sl.Account == "" {
					continue
				}
				needles = append(needles, []byte(sl.Account), []byte(strings.ToUpper(sl.Account)))
			}
		}
	}
	for _, a := range accounts {
		needles = append(needles, []byte(a), []byte(strings.ToUpper(a)))
	}
	accountNeedles := len(needles)
	password, err := servePassword(*pwPath)
	if err != nil {
		fmt.Fprintln(stderr, "forever scrub-check:", err)
		return exitUsage
	}
	if password != "" {
		needles = append(needles, []byte(password))
	}
	if len(needles) == 0 {
		fmt.Fprintln(stderr, "forever scrub-check: nothing to check against")
		return exitUsage
	}
	code := exitOK
	for _, dir := range fs.Args() {
		found, files, err := capturedir.Check(dir, needles)
		if err != nil {
			// The error names a file or a needle index, never a secret.
			fmt.Fprintln(stderr, "forever scrub-check:", err)
			return exitUsage
		}
		if len(found) == 0 {
			fmt.Fprintf(stdout, "%s: clean, %d files\n", dir, files)
			continue
		}
		for _, f := range found {
			what := "a password"
			if f.Needle < accountNeedles {
				what = "an account name"
			}
			fmt.Fprintf(stdout, "%s: %s/%s contains %s\n", dir, f.Run, f.File, what)
		}
		code = exitScrub
	}
	return code
}

// foreverCaptureHint answers the message that sends a Turtle capture command to the Forever tools when path is a
// Forever capture directory (a run or a parent of runs), and "" otherwise.
func foreverCaptureHint(path, cmd string) string {
	if !capturedir.IsCaptureDir(path) {
		return ""
	}
	return cmd + ": this is a Forever capture directory: use forever-agent forever scrub-check|capture-events|capture-stats"
}
