package main

// forever scrub-check: a `forever serve --capture-dir` capture must not carry the runner's secrets or the addresses
// it played against before it is shared. The needles are every account of a slot under a server with a `forever`
// block in the slots file, each --account (raw and upper case), the account password (--password-file, else
// FOREVER_PASSWORD), every --session-key-file's key (raw and decoded from hex) and every --auth-token-file's token;
// every file of every run is read in every form capture.Variants lists, and every file is also read for an IPv4
// address. A finding names the run, the file and the class, never the secret.
//
// With --redact DIR the command writes a cleaned copy under DIR instead: every match is masked by one '*' per byte
// (so the manifests keep their lengths), the copy is read back to prove it is clean, and the original is untouched.

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/slots"
)

const foreverScrubUsage = "usage: forever-agent forever scrub-check [--slots FILE] [--account EMAIL]... [--password-file FILE] [--session-key-file FILE]... [--auth-token-file FILE]... [--redact DIR] <dir>..."

// scrubList collects a repeatable string flag.
type scrubList []string

func (s *scrubList) String() string { return strings.Join(*s, ",") }

func (s *scrubList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// foreverScrubCmd checks each capture directory, or with --redact one of them, writing a cleaned copy. Exit 0 is
// clean (or, with --redact, a clean copy), exitScrub (7) a finding, exitUsage (2) a usage, settings, secret-file or
// read error. A run needs at least one needle.
func foreverScrubCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever scrub-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slotsPath := fs.String("slots", "", "slots file whose Forever servers' slot accounts must not appear")
	pwPath := fs.String("password-file", "", "file holding the password (default: FOREVER_PASSWORD)")
	var accounts scrubList
	fs.Var(&accounts, "account", "an account e-mail that must not appear (repeatable)")
	var keyFiles scrubList
	fs.Var(&keyFiles, "session-key-file", "file holding a session key, raw or hex (repeatable)")
	var tokenFiles scrubList
	fs.Var(&tokenFiles, "auth-token-file", "file holding an auth token (repeatable)")
	redact := fs.String("redact", "", "write a cleaned copy of the one capture directory under this new directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, foreverScrubUsage)
		return exitUsage
	}
	if *redact != "" && fs.NArg() != 1 {
		fmt.Fprintln(stderr, "forever scrub-check: --redact takes one capture directory")
		return exitUsage
	}
	// The needles, in order: the slots file's Forever accounts, each --account, the password, the session keys, the
	// tokens. Variants expands each into the forms a server can send it.
	var needles []capturedir.Needle
	add := func(b []byte, kind capturedir.Kind) {
		if len(b) > 0 {
			needles = append(needles, capturedir.Needle{Bytes: b, Kind: kind})
		}
	}
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
				add([]byte(sl.Account), capturedir.KindAccount)
				add([]byte(strings.ToUpper(sl.Account)), capturedir.KindAccount)
			}
		}
	}
	for _, a := range accounts {
		add([]byte(a), capturedir.KindAccount)
		add([]byte(strings.ToUpper(a)), capturedir.KindAccount)
	}
	password, err := servePassword(*pwPath)
	if err != nil {
		fmt.Fprintln(stderr, "forever scrub-check:", err)
		return exitUsage
	}
	add([]byte(password), capturedir.KindPassword)
	for _, path := range keyFiles {
		key, err := readSecretFile(path)
		if err != nil {
			fmt.Fprintln(stderr, "forever scrub-check:", err)
			return exitUsage
		}
		add(key, capturedir.KindSessionKey)
		// A key written as hex text is the same secret as the bytes it names: check for both.
		if raw, ok := decodeHexSecret(key); ok {
			add(raw, capturedir.KindSessionKey)
		}
	}
	for _, path := range tokenFiles {
		raw, err := readSecretFile(path)
		if err != nil {
			fmt.Fprintln(stderr, "forever scrub-check:", err)
			return exitUsage
		}
		// An rpc token file holds the token alone; a dashboard token file holds "<label> <token>".
		fields := strings.Fields(string(raw))
		if len(fields) == 0 {
			fmt.Fprintf(stderr, "forever scrub-check: %s: no token\n", path)
			return exitUsage
		}
		add([]byte(fields[len(fields)-1]), capturedir.KindAuthToken)
	}
	if len(needles) == 0 {
		fmt.Fprintln(stderr, "forever scrub-check: nothing to check against")
		return exitUsage
	}
	if *redact != "" {
		return foreverScrubRedact(fs.Arg(0), *redact, needles, stdout, stderr)
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
			fmt.Fprintf(stdout, "%s: %s/%s contains %s\n", dir, f.Run, f.File, f.Kind)
		}
		code = exitScrub
	}
	return code
}

// foreverScrubRedact writes the cleaned copy of dir and proves it clean: findings of the original are reported as
// redacted, a finding that survives in the copy is an exitScrub, and a clean copy exits 0.
func foreverScrubRedact(dir, out string, needles []capturedir.Needle, stdout, stderr io.Writer) int {
	found, _, err := capturedir.Check(dir, needles)
	if err != nil {
		fmt.Fprintln(stderr, "forever scrub-check:", err)
		return exitUsage
	}
	for _, f := range found {
		fmt.Fprintf(stdout, "%s: %s/%s contains %s (redacted in the copy)\n", dir, f.Run, f.File, f.Kind)
	}
	rep, err := capturedir.Redact(dir, out, needles)
	if err != nil {
		fmt.Fprintln(stderr, "forever scrub-check:", err)
		return exitUsage
	}
	left, _, err := capturedir.Check(out, needles)
	if err != nil {
		fmt.Fprintln(stderr, "forever scrub-check:", err)
		return exitUsage
	}
	if len(left) > 0 {
		for _, f := range left {
			fmt.Fprintf(stdout, "%s: %s/%s still contains %s\n", out, f.Run, f.File, f.Kind)
		}
		return exitScrub
	}
	fmt.Fprintf(stdout, "%s: redacted copy in %s, %d run(s), %d file(s), %d match(es) masked\n",
		dir, out, rep.Runs, rep.Files, rep.Masked)
	return exitOK
}

// readSecretFile reads one small secret file: a regular file only its owner can read, with its trailing newline
// removed. The path may appear in an error; the content never does.
func readSecretFile(path string) ([]byte, error) {
	raw, err := privfile.Read(path, 64*1024)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	raw = bytes.TrimRight(raw, "\r\n")
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return raw, nil
}

// decodeHexSecret decodes a session key written as hex text (an optional 0x prefix and space allowed), and reports
// whether the text was hex at all.
func decodeHexSecret(raw []byte) ([]byte, bool) {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"))
	text = strings.Join(strings.Fields(text), "")
	if len(text) < 6 || len(text)%2 != 0 {
		return nil, false
	}
	decoded, err := hex.DecodeString(text)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// foreverCaptureHint answers the message that sends a Turtle capture command to the Forever tools when path is a
// Forever capture directory (a run or a parent of runs), and "" otherwise.
func foreverCaptureHint(path, cmd string) string {
	if !capturedir.IsCaptureDir(path) {
		return ""
	}
	return cmd + ": this is a Forever capture directory: use forever-agent forever scrub-check|capture-events|capture-stats"
}
