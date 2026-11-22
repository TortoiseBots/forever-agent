package bnet

// Repository hygiene: the public text of the Forever rounds (docs/forever/*.md and every testdata file of
// internal/forever) is inspected for what a live run leaves behind — the client instance id, an account form, a
// dotted IPv4 address and a millisecond timestamp. The transcripts quote real output, so the values must be
// placeholders and the command must print lengths. The negative subtests prove each pattern bites.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	// ciid is the 16+16 uppercase hex client instance id of a Battle.net ConnectResponse.
	ciidPattern = regexp.MustCompile(`[0-9A-F]{16}-[0-9A-F]{16}`)
	// an account form is the game account the join ticket names, e.g. 2#1; the placeholder form 000#0 is allowed.
	accountPattern = regexp.MustCompile(`\b[0-9]+#[0-9]`)
	// a dotted quad; 127.0.0.1 (loopback) and 0.0.0.0 (wildcard bind) are configuration, not identifiers.
	ipv4Pattern = regexp.MustCompile(`\b[0-9]{1,3}(\.[0-9]{1,3}){3}\b`)
	// a millisecond timestamp is 13 decimal digits; the world clock and the server time are such values.
	millisPattern = regexp.MustCompile(`\b[0-9]{13}\b`)
	// a coordinate triple is three signed decimals with a fractional part, separated by comma or space. Live
	// positions are quotable only as offsets from a named spawn; the synthetic fixture points are allow-listed.
	coordinateTriplePattern = regexp.MustCompile(`-?[0-9]{1,6}\.[0-9]+[ ,]+-?[0-9]{1,6}\.[0-9]+[ ,]+-?[0-9]{1,6}\.[0-9]+`)
)

// syntheticPositions are the coordinate triples the committed fixtures and fake servers use; they are not a run's
// positions. A real position must be reported as an offset from <spawn> instead.
var syntheticPositions = [][3]string{
	{"1.25", "2.5", "3.75"}, // world/testdata login_verify_world
	{"1000", "2000", "50"},  // the rewritten objects.json payloads
	{"1008", "2000", "50"},  // the eastward walk target
	{"1000", "2008", "50"},  // the northward walk target
}

// coordinateFinding reports a coordinate triple that is not one of the synthetic fixture points.
func coordinateFinding(match string) (string, bool) {
	parts := regexp.MustCompile(`[ ,]+`).Split(match, -1)
	if len(parts) != 3 {
		return "", false
	}
	for _, p := range syntheticPositions {
		if parts[0] == p[0] && parts[1] == p[1] && parts[2] == p[2] {
			return "", false
		}
	}
	return "coordinates " + match, true
}

// isHostAddress reports whether a dotted quad names a host, i.e. is an address a run could have used. The two
// loopback/wildcard literals are allowed, and so is a leading zero octet: 0/8 has no host address, and product
// versions read like one (the capture is `repack 0.1.37.4`).
func isHostAddress(s string) bool {
	if s == "127.0.0.1" || s == "0.0.0.0" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n > 255 {
			return false
		}
	}
	return parts[0] != "0"
}

// runIdentifiers returns one finding per run-specific identifier in text, in the form "<kind> <value>".
func runIdentifiers(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, m := range ciidPattern.FindAllString(line, -1) {
			out = append(out, "ciid "+m)
		}
		for _, m := range accountPattern.FindAllString(line, -1) {
			if m != "000#0" {
				out = append(out, "account "+m)
			}
		}
		for _, m := range ipv4Pattern.FindAllString(line, -1) {
			if isHostAddress(m) {
				out = append(out, "ipv4 "+m)
			}
		}
		for _, m := range millisPattern.FindAllString(line, -1) {
			out = append(out, "millisecond time "+m)
		}
		for _, m := range coordinateTriplePattern.FindAllString(line, -1) {
			if finding, ok := coordinateFinding(m); ok {
				out = append(out, finding)
			}
		}
	}
	return out
}

// hygieneFiles is the committed text this round publishes: docs/forever/*.md and every testdata file of
// internal/forever. The paths are relative to this package's directory.
func hygieneFiles(t *testing.T) []string {
	t.Helper()
	docs, err := filepath.Glob("../../../docs/forever/*.md")
	if err != nil {
		t.Fatalf("globbing the round notes: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("no docs/forever/*.md found: the test runs from internal/forever/bnet")
	}
	files := append([]string{}, docs...)
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/testdata/") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/forever: %v", err)
	}
	if len(files) == len(docs) {
		t.Fatal("no testdata file under internal/forever: the walk is looking at the wrong place")
	}
	sort.Strings(files)
	return files
}

// TestCommittedTextCarriesNoRunIdentifiers is the repository hygiene check of both Forever rounds: a ciid, a real
// account form, a host address or a millisecond timestamp in a note or a fixture is a live-run identifier that a
// report must not carry. Its negative subtests feed the same check each pattern in memory, so every pattern is
// proven to bite without depending on the repository text.
func TestCommittedTextCarriesNoRunIdentifiers(t *testing.T) {
	for _, path := range hygieneFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, finding := range runIdentifiers(line) {
				t.Errorf("%s:%d: %s", path, i+1, finding)
			}
		}
	}

	for _, c := range []struct{ name, text, want string }{
		{"ciid", `rpc connect: ciid "0123456789ABCDEF-FEDCBA9876543210", bindless true`, "ciid 0123456789ABCDEF-FEDCBA9876543210"},
		{"account", "realm join: join ticket 76 bytes, gameAccount 1234#1, join secret 32 bytes", "account 1234#1"},
		{"ipv4", "world server 198.51.100.123:8085", "ipv4 198.51.100.123"},
		{"millisecond time", "server time 1234567890123", "millisecond time 1234567890123"},
		{"coordinates", "the server saved the character at -1234.56 -765.43 21.09", "coordinates -1234.56 -765.43 21.09"},
	} {
		t.Run("negative/"+c.name, func(t *testing.T) {
			got := runIdentifiers(c.text)
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("runIdentifiers(%q) = %q, want [%q]", c.text, got, c.want)
			}
		})
	}

	t.Run("every kind at once", func(t *testing.T) {
		text := fmt.Sprintf("ciid %s, account %s, addressed %s, time %s",
			"0123456789ABCDEF-FEDCBA9876543210", "77#3", "203.0.113.7", "1234567890123")
		if got := runIdentifiers(text); len(got) != 4 {
			t.Fatalf("runIdentifiers = %q, want the four identifiers", got)
		}
	})

	t.Run("allowed forms", func(t *testing.T) {
		for _, text := range []string{
			"world server 127.0.0.1:8085, join secret 32 bytes",
			`LoginREST binds "0.0.0.0"`,
			"gameAccount 000#0 (4 bytes, the placeholder form)",
			"the recorder's build (repack 0.1.37.4)",
			"Run on 2026-11-18, account id 2",
			"the SMSG_CONNECT_TO Key became 0x1122334455667788",
			"PyInt(5e18) is the int64 5000000000000000000",
			"the fake server puts the character at 1.25, 2.5, 3.75",
			"the fixture's rewritten position is 1000 2000 50",
		} {
			if got := runIdentifiers(text); len(got) != 0 {
				t.Errorf("runIdentifiers(%q) = %q, want no finding", text, got)
			}
		}
	})
}
