package botcmd

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// goldenDir is the contract's golden files: contracts/bot-commands/v1/golden, four levels above this package.
const goldenDir = "../../../../contracts/bot-commands/v1/golden"

type goldenBlock struct {
	name    string
	request string
	lines   []string
	expect  string
}

// readGoldenBlocks reads every block of every golden file.
func readGoldenBlocks(t *testing.T) []goldenBlock {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(goldenDir, "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("botcmd: no golden files under %s (err %v)", goldenDir, err)
	}
	var blocks []goldenBlock
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("botcmd: read %s: %v", file, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "# case: "):
				blocks = append(blocks, goldenBlock{name: strings.TrimPrefix(line, "# case: ")})
			case strings.HasPrefix(line, "#"), strings.TrimSpace(line) == "":
			case len(blocks) == 0:
				t.Fatalf("%s: %q comes before the first case", file, line)
			case strings.HasPrefix(line, "> "):
				blocks[len(blocks)-1].request = line[2:]
			case strings.HasPrefix(line, "< "):
				i := len(blocks) - 1
				blocks[i].lines = append(blocks[i].lines, line[2:])
			case strings.HasPrefix(line, "= "):
				blocks[len(blocks)-1].expect = line[2:]
			default:
				t.Fatalf("%s: %q is outside the block format", file, line)
			}
		}
	}
	return blocks
}

// TestGoldenCases proves every golden block parses to its `=` line: the request is what Line builds, and the reply
// lines parse (or refuse to parse, for the core's own line) exactly as the block says.
func TestGoldenCases(t *testing.T) {
	blocks := readGoldenBlocks(t)
	if len(blocks) < 20 {
		t.Fatalf("botcmd: %d golden cases, want at least 20", len(blocks))
	}
	seen := make(map[string]bool, len(blocks))
	for _, b := range blocks {
		if seen[b.name] {
			t.Fatalf("botcmd: case %q appears twice", b.name)
		}
		seen[b.name] = true
		t.Run(b.name, func(t *testing.T) { checkGolden(t, b) })
	}
}

func checkGolden(t *testing.T, b goldenBlock) {
	t.Helper()
	words := strings.Fields(b.request)
	if len(words) < 2 || words[0] != ".bot" {
		t.Fatalf("request %q is not a .bot line", b.request)
	}
	v, ok := ParseVerb(words[1])
	if !ok {
		t.Fatalf("request verb %q is not a v1 verb", words[1])
	}
	bot := ""
	if len(words) > 2 {
		bot = words[2]
	}
	if built, err := Line(v, bot, ""); err != nil || built != b.request {
		t.Fatalf("Line(%q, %q) = %q, %v; the golden request is %q", v, bot, built, err, b.request)
	}

	switch {
	case b.expect == "no-module":
		for _, line := range b.lines {
			if r, ok := ParseReply(line); ok {
				t.Fatalf("%q parsed as %+v; the core's absent line is not a reply", line, r)
			}
		}
		rep := &command.Reply{Raw: b.lines, RawText: b.lines}
		if _, err := Classify(rep, v, bot); !errors.Is(err, ErrNoModule) {
			t.Fatalf("Classify on the absent lines = %v, want ErrNoModule", err)
		}
	case strings.HasPrefix(b.expect, "who "):
		want := parseWhoExpect(t, b.expect)
		got, err := ParseWho(b.lines)
		if err != nil {
			t.Fatalf("ParseWho: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("ParseWho returned %d rows, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	default:
		if len(b.lines) != 1 {
			t.Fatalf("%d lines for one reply", len(b.lines))
		}
		want := parseReplyExpect(t, b.expect)
		got, ok := ParseReply(b.lines[0])
		if !ok || got != want {
			t.Fatalf("%q parsed %+v (%v), want %+v", b.lines[0], got, ok, want)
		}
		r, err := Classify(&command.Reply{Raw: b.lines, RawText: b.lines}, v, bot)
		if err != nil || r != want {
			t.Fatalf("Classify = %+v, %v; want %+v", r, err, want)
		}
		if want.OK != (want.Err() == nil) {
			t.Fatalf("Err() = %v on %+v; ok and no error must agree", want.Err(), want)
		}
	}
}

func parseReplyExpect(t *testing.T, line string) Reply {
	t.Helper()
	rest := line
	take := func(key string) string {
		if !strings.HasPrefix(rest, key) {
			t.Fatalf("expect %q has no %s", line, key)
		}
		rest = rest[len(key):]
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			t.Fatalf("expect %q ends inside %s", line, key)
		}
		word := rest[:i]
		rest = rest[i+1:]
		return word
	}
	r := Reply{Verb: take("verb="), Bot: take("bot=")}
	switch take("ok=") {
	case "true":
		r.OK = true
	case "false":
	default:
		t.Fatalf("expect %q: ok= is not true or false", line)
	}
	if !strings.HasPrefix(rest, "code=") {
		t.Fatalf("expect %q has no code=", line)
	}
	rest = rest[len("code="):]
	if strings.HasPrefix(rest, " ") {
		rest = rest[1:] // an empty code is written "code= text="
	} else {
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			t.Fatalf("expect %q ends inside code=", line)
		}
		r.Code, rest = rest[:i], rest[i+1:]
	}
	if !strings.HasPrefix(rest, "text=") {
		t.Fatalf("expect %q has no text=", line)
	}
	r.Text = rest[len("text="):]
	return r
}

func parseWhoExpect(t *testing.T, line string) []WhoRow {
	t.Helper()
	words := strings.Fields(line)
	if len(words) < 2 || words[0] != "who" || !strings.HasPrefix(words[1], "n=") {
		t.Fatalf("expect %q is not a who line", line)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(words[1], "n="))
	if err != nil {
		t.Fatalf("expect %q: %v", line, err)
	}
	rows := make([]WhoRow, 0, len(words)-2)
	for _, w := range words[2:] {
		parts := strings.Split(strings.TrimPrefix(w, "row="), ",")
		if len(parts) != 6 || !strings.HasPrefix(w, "row=") {
			t.Fatalf("expect %q: %q is not a row", line, w)
		}
		level, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("expect %q: %v", line, err)
		}
		id, err := strconv.ParseUint(parts[3], 10, 32)
		if err != nil {
			t.Fatalf("expect %q: %v", line, err)
		}
		rows = append(rows, WhoRow{Bot: parts[0], Level: level, Class: parts[2], Map: uint32(id), Move: parts[4], Mode: parts[5]})
	}
	if n != len(rows) {
		t.Fatalf("expect %q says %d rows and lists %d", line, n, len(rows))
	}
	return rows
}
