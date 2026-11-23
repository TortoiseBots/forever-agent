package tbmproto_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/contract/tbm"
	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
)

func TestCases(t *testing.T) {
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 28 {
		t.Fatalf("want 28 cases, got %d", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

func runCase(t *testing.T, c tbmproto.Case) {
	t.Helper()
	r := tbmproto.NewReader()
	for i := 0; i < len(c.Records); i++ {
		rec := c.Records[i]
		switch rec.Kind {
		case "send":
			if i+1 >= len(c.Records) {
				t.Fatalf("send without frame")
			}
			next := c.Records[i+1]
			transport := tbmproto.Transport(rec.Fields[0])
			frame, err := tbmproto.Encode(transport, rec.Fields[1])
			switch next.Kind {
			case "frame":
				if err != nil {
					t.Fatalf("Encode(%q) error: %v", rec.Fields[1], err)
				}
				want := frameFields(next.Fields)
				if frame.Kind != want.Kind || frame.Channel != want.Channel || frame.Prefix != want.Prefix || frame.Text != want.Text {
					t.Fatalf("frame = %+v, want %+v", frame, want)
				}
			case "encode-error":
				if err == nil {
					t.Fatalf("Encode(%q) = %+v, want error %s", rec.Fields[1], frame, next.Fields[0])
				}
				var pe *tbmproto.Error
				if !errors.As(err, &pe) || pe.Code != next.Fields[0] {
					t.Fatalf("Encode(%q) error = %v, want code %s", rec.Fields[1], err, next.Fields[0])
				}
			default:
				t.Fatalf("send followed by %s", next.Kind)
			}
			i++
		case "recv":
			r.Feed(recvIncoming(rec.Fields))
		case "expect":
			// Checked after Finish.
		case "case", "desc", "module", "frame", "encode-error":
		default:
			t.Fatalf("unknown record %s", rec.Kind)
		}
	}
	r.Finish()
	res := r.Result()
	for _, rec := range c.Records {
		if rec.Kind != "expect" {
			continue
		}
		got, err := resolveExpect(res, rec.Fields[0])
		if err != nil {
			t.Fatalf("expect %s: %v", rec.Fields[0], err)
		}
		if got != rec.Fields[1] {
			t.Fatalf("expect %s = %q, want %q", rec.Fields[0], got, rec.Fields[1])
		}
	}
	ok := strings.HasPrefix(c.Name, "ok-")
	bad := strings.HasPrefix(c.Name, "bad-")
	check := ""
	for _, rec := range c.Records {
		if rec.Kind == "expect" && rec.Fields[0] == "hello.check" {
			check = rec.Fields[1]
		}
	}
	hasEncodeError := false
	for _, rec := range c.Records {
		if rec.Kind == "encode-error" {
			hasEncodeError = true
		}
	}
	if ok && len(res.Problems) != 0 {
		t.Fatalf("ok case has %d problems", len(res.Problems))
	}
	if ok && check != "" && check != "ok" {
		t.Fatalf("ok case check = %q", check)
	}
	if bad && len(res.Problems) == 0 && !hasEncodeError && check != "" && check == "ok" {
		t.Fatalf("bad case passes clean")
	}
}

type wantFrame struct {
	Kind, Channel, Prefix, Text string
}

func frameFields(f []string) wantFrame {
	if f[0] == "addon" {
		return wantFrame{Kind: "addon", Channel: f[1], Prefix: f[2], Text: f[3]}
	}
	return wantFrame{Kind: "chat", Channel: f[1], Text: f[2]}
}

func recvIncoming(f []string) tbmproto.Incoming {
	if f[0] == "addon" {
		return tbmproto.Incoming{Kind: "addon", Channel: f[1], Prefix: f[2], Text: f[3]}
	}
	return tbmproto.Incoming{Kind: "system", Text: f[1]}
}

func resolveExpect(res tbmproto.Result, p string) (string, error) {
	boolStr := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	switch {
	case p == "hello.complete":
		return boolStr(res.Hello.Complete), nil
	case p == "hello.transport":
		return res.Hello.Transport, nil
	case p == "hello.proto":
		return strconv.Itoa(res.Hello.Proto), nil
	case p == "hello.min_addon":
		return strconv.Itoa(res.Hello.MinAddon), nil
	case p == "hello.version":
		if res.Hello.Version == "" {
			return "(empty)", nil
		}
		return res.Hello.Version, nil
	case p == "hello.caps":
		return strings.Join(res.Hello.Caps, ","), nil
	case p == "hello.verbs":
		if len(res.Hello.Verbs) == 0 {
			return "(empty)", nil
		}
		return strings.Join(res.Hello.Verbs, ","), nil
	case p == "hello.has_verbs":
		return boolStr(res.Hello.HasVerbs), nil
	case p == "hello.latest_module":
		return res.Hello.LatestModule, nil
	case p == "hello.latest_addon":
		return res.Hello.LatestAddon, nil
	case p == "hello.check":
		if err := res.Hello.Check(); err == nil {
			return "ok", nil
		} else {
			var pe *tbmproto.Error
			if errors.As(err, &pe) {
				return pe.Code, nil
			}
			return "", err
		}
	case p == "roster.committed":
		return boolStr(res.RosterCommitted), nil
	case p == "roster.count":
		return strconv.Itoa(len(res.Roster)), nil
	case strings.HasPrefix(p, "roster.") && (strings.HasSuffix(p, ".guid") || strings.HasSuffix(p, ".name") ||
		strings.HasSuffix(p, ".class") || strings.HasSuffix(p, ".state") || strings.HasSuffix(p, ".grouped") ||
		strings.HasSuffix(p, ".location") || strings.HasSuffix(p, ".cc_mark") || strings.HasSuffix(p, ".class_name")):
		rest := strings.TrimPrefix(p, "roster.")
		dot := strings.Index(rest, ".")
		i, err := strconv.Atoi(rest[:dot])
		if err != nil || i < 0 || i >= len(res.Roster) {
			return "", fmt.Errorf("bad roster index %s", p)
		}
		row := res.Roster[i]
		empty := func(s string) string {
			if s == "" {
				return "(empty)"
			}
			return s
		}
		switch rest[dot+1:] {
		case "guid":
			return strconv.FormatUint(uint64(row.GUID), 10), nil
		case "name":
			return row.Name, nil
		case "class":
			return strconv.FormatUint(uint64(row.Class), 10), nil
		case "state":
			return row.State, nil
		case "grouped":
			return boolStr(row.Grouped), nil
		case "location":
			return row.Location, nil
		case "cc_mark":
			return empty(row.CCMark), nil
		case "class_name":
			return empty(row.ClassName), nil
		}
	case p == "roster.error":
		if res.RosterError == nil {
			return "(empty)", nil
		}
		return res.RosterError.Code, nil
	case p == "roster.error_message":
		if res.RosterError == nil {
			return "(empty)", nil
		}
		return res.RosterError.Message, nil
	case p == "altbots":
		return strings.Join(res.AltBots, ","), nil
	case p == "botstate.count":
		return strconv.Itoa(len(res.BotState)), nil
	case strings.HasPrefix(p, "botstate."):
		return res.BotState[strings.TrimPrefix(p, "botstate.")], nil
	case p == "ack.count":
		return strconv.Itoa(len(res.Acks)), nil
	case strings.HasPrefix(p, "ack."):
		rest := strings.TrimPrefix(p, "ack.")
		dot := strings.Index(rest, ".")
		i, err := strconv.Atoi(rest[:dot])
		if err != nil || i < 0 || i >= len(res.Acks) {
			return "", fmt.Errorf("bad ack index %s", p)
		}
		switch rest[dot+1:] {
		case "intent":
			return res.Acks[i].Intent, nil
		case "scope":
			return res.Acks[i].Scope, nil
		case "count":
			return strconv.Itoa(res.Acks[i].Count), nil
		case "detail":
			return res.Acks[i].Detail, nil
		}
	case p == "err.count":
		return strconv.Itoa(len(res.Errs)), nil
	case strings.HasPrefix(p, "err."):
		rest := strings.TrimPrefix(p, "err.")
		dot := strings.Index(rest, ".")
		i, err := strconv.Atoi(rest[:dot])
		if err != nil || i < 0 || i >= len(res.Errs) {
			return "", fmt.Errorf("bad err index %s", p)
		}
		switch rest[dot+1:] {
		case "intent":
			return res.Errs[i].Intent, nil
		case "code":
			return res.Errs[i].Code, nil
		case "message":
			return res.Errs[i].Message, nil
		}
	case p == "problems.count":
		return strconv.Itoa(len(res.Problems)), nil
	case strings.HasPrefix(p, "problem."):
		i, err := strconv.Atoi(strings.TrimPrefix(p, "problem."))
		if err != nil || i < 0 || i >= len(res.Problems) {
			return "", fmt.Errorf("bad problem index %s", p)
		}
		return res.Problems[i].Code, nil
	case p == "unknown.count":
		return strconv.Itoa(len(res.Unknown)), nil
	case p == "ignored.count":
		return strconv.Itoa(res.Ignored), nil
	}
	return "", fmt.Errorf("unknown expect path %q", p)
}

func TestManifest(t *testing.T) {
	want := buildManifest(t)
	data, err := fs.ReadFile(tbmcontract.V1, "v1/MANIFEST")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("MANIFEST drift; expected text:\n%s", want)
	}
}

func buildManifest(t *testing.T) string {
	t.Helper()
	var paths []string
	err := fs.WalkDir(tbmcontract.V1, "v1", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || p == "v1/MANIFEST" {
			return nil
		}
		rel := strings.TrimPrefix(p, "v1/")
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString("tbm-contract v1 rev 1 proto 1 min-addon 1\n")
	for _, rel := range paths {
		data, err := fs.ReadFile(tbmcontract.V1, path.Join("v1", rel))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%x  %s\n", sum, rel)
	}
	return b.String()
}

func TestMaxText(t *testing.T) {
	if tbmproto.MaxText != addonmsg.MaxText {
		t.Fatalf("MaxText %d != addonmsg.MaxText %d", tbmproto.MaxText, addonmsg.MaxText)
	}
	if err := addonmsg.ValidatePrefix(tbmproto.Prefix); err != nil {
		t.Fatalf("ValidatePrefix(%q): %v", tbmproto.Prefix, err)
	}
}

func TestModuleCasesCanonical(t *testing.T) {
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if !c.Module {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			for _, rec := range c.Records {
				if rec.Kind != "recv" {
					continue
				}
				texts := recvTexts(rec.Fields)
				for _, text := range texts {
					if !strings.HasPrefix(text, "TBM:") {
						continue
					}
					typ, fields := splitLine(text)
					got, err := tbmproto.EncodeLine(typ, fields...)
					if err != nil {
						t.Fatalf("EncodeLine(%q) error: %v", text, err)
					}
					if got != text {
						t.Fatalf("EncodeLine gave %q, want %q", got, text)
					}
				}
			}
		})
	}
}

func recvTexts(f []string) []string {
	var text string
	if f[0] == "addon" {
		text = f[3]
	} else {
		text = f[1]
	}
	return strings.Split(text, "\n")
}

func splitLine(line string) (string, []string) {
	rest := strings.TrimPrefix(line, "TBM:")
	if i := strings.Index(rest, "|"); i >= 0 {
		return rest[:i], strings.Split(rest[i+1:], "|")
	}
	return rest, nil
}

func TestParseCaseRefuses(t *testing.T) {
	for i, bad := range []string{
		"case x\nbogus y\n",
		"case x\nsend chat version\n",
		"case other\n",
	} {
		if _, err := tbmproto.ParseCase("x", []byte(bad)); err == nil {
			t.Fatalf("bad text %d parsed without error", i)
		}
	}
}
