// Command refactor-world-types-1 rewrites the step-1 files of the
// FOREVER-AGENT-WORLD-TYPES brief: packet.GUID -> world.GUID and
// packet.ParseGUID -> world.ParseGUID in every package outside the protocol
// layer, adding the world import and dropping the packet import only when no
// other packet.* use remains. Be consistent per package: all files in the
// list, tests included.
//
// Usage: go run ./tools/refactor/world-types-1 <file-list>
// The file list holds one repo-relative path per line.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	guidRe  = regexp.MustCompile(`\bpacket\.GUID\b`)
	parseRe = regexp.MustCompile(`\bpacket\.ParseGUID\b`)
	pktRe   = regexp.MustCompile(`\bpacket\.[A-Za-z][A-Za-z0-9_]*\b`)
	pktImp  = regexp.MustCompile(`(?m)^\t"github\.com/pjerra/forever-agent/internal/packet"\n`)
	worldIm = "github.com/pjerra/forever-agent/internal/world"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: world-types-1 <file-list>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fails := 0
	for _, f := range strings.Fields(string(raw)) {
		if err := rewrite(f); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			fails++
		}
	}
	if fails > 0 {
		os.Exit(1)
	}
}

func rewrite(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(b)
	if !guidRe.MatchString(src) && !parseRe.MatchString(src) {
		return fmt.Errorf("no packet.GUID or packet.ParseGUID found")
	}
	out := guidRe.ReplaceAllString(src, "world.GUID")
	out = parseRe.ReplaceAllString(out, "world.ParseGUID")

	// Add the world import after the packet import line when missing.
	if !strings.Contains(out, "\t\""+worldIm+"\"\n") {
		m := pktImp.FindStringIndex(out)
		if m == nil {
			return fmt.Errorf("no packet import to anchor the world import")
		}
		out = out[:m[1]] + "\t\"" + worldIm + "\"\n" + out[m[1]:]
	}

	// Drop the packet import when nothing names packet.* anymore.
	if !pktRe.MatchString(out) {
		n := pktImp.FindStringIndex(out)
		if n == nil {
			return fmt.Errorf("packet import already gone but packet.* still used?")
		}
		out = out[:n[0]] + out[n[1]:]
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
