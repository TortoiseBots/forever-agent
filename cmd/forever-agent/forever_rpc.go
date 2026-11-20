// forever rpc is the CLI client of the forever serve session daemon. It
// talks to the serve socket like the old (Turtle) rpc command talks to its
// daemon: the same slot-dir checks, the same argument parsing, the same
// output (the pretty or --raw JSON result, or the typed `error: <code>
// <message>` with a nonzero exit).
//
// `forever rpc DIR list` contacts the selected daemon's status method and
// then prints the methods serve answers, with the ones the server does not
// port marked; anything else calls one method with JSON params. The list is
// the server's own table (world.ServeRPCMethods), never a second copy.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

const foreverRPCUsage = "usage: forever-agent forever rpc [--timeout DUR] [--raw] <dir> <method|list> [<params-json>|-]"

// exitRPCCancel is a call aborted by Ctrl-C/SIGTERM or --timeout before the
// daemon answered. The connection is closed and no reply was printed, so a
// caller must not read the failure as "the mutation did not happen" and
// blindly retry an irreversible one.
const exitRPCCancel = 1

// foreverRPCCmd calls one serve method with JSON params and prints the
// result, or the typed error. Params come from rpcReadParams ("-" reads
// rpcStdin, as the old command does).
func foreverRPCCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("forever rpc", stderr)
	}
	o, code := parseForeverRPCArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// The dir is a serve --dir, which writes the same manifest the old slot
	// setup writes: the same checks, the derived token file, never the
	// strings (openSlotClient, as the old rpc does).
	c, err := openSlotClient(o.dir)
	if err != nil {
		var opened *slotOpenError
		if errors.As(err, &opened) {
			fmt.Fprintln(stderr, strings.Replace(opened.msg, "rpc:", "forever rpc:", 1))
			return opened.code
		}
		fmt.Fprintf(stderr, "forever rpc: %v\n", err)
		return exitUsage
	}
	if o.method == "list" {
		return foreverRPCList(ctx, c, o.timeout, stdout, stderr)
	}
	params, err := rpcReadParams(o.params, o.paramsFromStdin)
	if err != nil {
		fmt.Fprintf(stderr, "forever rpc: %v\n", err)
		return exitUsage
	}
	var result json.RawMessage
	if err := c.Call(ctx, o.method, params, &result, o.timeout); err != nil {
		return foreverRPCCallError(c, err, stderr)
	}
	var out []byte
	if o.raw {
		out, err = json.Marshal(result)
	} else {
		out, err = json.MarshalIndent(result, "", "  ")
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever rpc: %v\n", rpcScrub(err.Error(), c.token))
		return exitUsage
	}
	out = []byte(rpcScrub(string(out), c.token))
	fmt.Fprintln(stdout, string(out))
	return exitOK
}

// foreverRPCOptions is the parsed `forever rpc` command line.
type foreverRPCOptions struct {
	dir             string
	method          string
	params          string
	paramsFromStdin bool
	timeout         time.Duration
	raw             bool
}

// parseForeverRPCArgs parses forever rpc flags in any position (before,
// between or after the positionals), like the old rpc command. Failures are
// reported to stderr; the result is exitOK with the options, or exitUsage.
func parseForeverRPCArgs(args []string, stderr io.Writer) (foreverRPCOptions, int) {
	var o foreverRPCOptions
	var timeoutStr string
	fs := flag.NewFlagSet("forever rpc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&timeoutStr, "timeout", "30s", "per-call timeout (Go duration)")
	fs.BoolVar(&o.raw, "raw", false, "print the result compactly")
	pos, rest := splitPositionals(fs, args, 3)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) < 2 || len(pos) > 3 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, foreverRPCUsage)
		return o, exitUsage
	}
	o.dir, o.method = pos[0], pos[1]
	if len(pos) == 3 {
		if pos[2] == "-" {
			o.paramsFromStdin = true
		} else {
			o.params = pos[2]
		}
	}
	if o.method == "" {
		fmt.Fprintln(stderr, foreverRPCUsage)
		return o, exitUsage
	}
	if o.method == "list" && (o.params != "" || o.paramsFromStdin) {
		fmt.Fprintln(stderr, foreverRPCUsage)
		return o, exitUsage
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil || timeout <= 0 {
		fmt.Fprintf(stderr, "forever rpc: bad --timeout %q: want a positive Go duration like 5s\n", timeoutStr)
		return o, exitUsage
	}
	o.timeout = timeout
	return o, exitOK
}

// foreverRPCCallError prints one failed serve call the way `forever rpc`
// reports it and returns the exit code: the typed error line (16), a bad
// reply or an unreachable daemon (15), a cancelled or timed-out call (1).
func foreverRPCCallError(c *slotClient, err error, stderr io.Writer) int {
	var aborted *slotCallAbortedError
	if errors.As(err, &aborted) {
		fmt.Fprintf(stderr, "forever rpc: %s\n", rpcScrub(err.Error(), c.token))
		return exitRPCCancel
	}
	var rpcErr *slotRPCError
	if errors.As(err, &rpcErr) {
		fmt.Fprintf(stderr, "error: %d %s\n", rpcErr.Code, rpcErr.Message)
		return exitRPCError
	}
	// A bad envelope keeps its own "bad reply" line; only transport
	// failures (daemon not running, connection refused) name the address,
	// as the old rpc does.
	var bad *rpcBadReplyError
	if errors.As(err, &bad) {
		fmt.Fprintf(stderr, "forever rpc: %s\n", rpcScrub(err.Error(), c.token))
		return exitSlotRefused
	}
	fmt.Fprintf(stderr, "forever rpc: cannot reach the agent at 127.0.0.1:%d: %s\n", c.manifest.Port, rpcScrub(err.Error(), c.token))
	return exitSlotRefused
}

// foreverRPCList contacts the selected daemon's status method, then prints
// every method serve answers, one per line, with the not-ported ones marked.
// The contact is the point: a stopped daemon (or a --dir not pointing at the
// running one) fails here with the call's exit code instead of printing a
// table that nothing answers. The table is the server's own: DONE names bare,
// NOT-PORTED names with the mark, both sorted.
func foreverRPCList(ctx context.Context, c *slotClient, timeout time.Duration, stdout, stderr io.Writer) int {
	var status struct {
		State string `json:"state"`
	}
	if err := c.Call(ctx, "status", nil, &status, timeout); err != nil {
		return foreverRPCCallError(c, err, stderr)
	}
	ported, notPorted := world.ServeRPCMethods()
	names := make([]string, 0, len(ported)+len(notPorted))
	names = append(names, ported...)
	for _, n := range notPorted {
		names = append(names, n+" (not ported)")
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteString("\n")
	}
	fmt.Fprint(stdout, rpcScrub(b.String(), c.token))
	return exitOK
}
