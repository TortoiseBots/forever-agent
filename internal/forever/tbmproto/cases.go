package tbmproto

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Record is one parsed case line: its kind and the fields after it. For
// send, frame, recv and expect the last field holds the rest of the line.
type Record struct {
	Kind   string
	Fields []string
	LineNo int
}

// Case is one parsed .case file.
type Case struct {
	Name    string
	Module  bool
	Records []Record
}

// ParseCase parses one case file: the first record names the file stem, and
// every send is followed at once by its frame or encode-error. Escapes (\n,
// \\) and {rep N C} expand in send/frame/recv texts and expect values.
func ParseCase(name string, data []byte) (Case, error) {
	c := Case{Name: name}
	pendingSend := false
	fail := func(lineNo int, format string, args ...any) (Case, error) {
		return Case{}, fmt.Errorf("tbm: case %s line %d: %s", name, lineNo, fmt.Sprintf(format, args...))
	}
	for i, raw := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		line := strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, " ")
		kind, rest := parts[0], parts[1:]
		rec := Record{Kind: kind, LineNo: lineNo}
		switch kind {
		case "case":
			if len(rest) != 1 {
				return fail(lineNo, "case wants 1 field")
			}
			if rest[0] != name {
				return fail(lineNo, "name %q is not the file stem", rest[0])
			}
			if len(c.Records) > 0 {
				return fail(lineNo, "second case record")
			}
			rec.Fields = []string{rest[0]}
		case "desc":
			if len(rest) < 1 {
				return fail(lineNo, "desc wants text")
			}
			rec.Fields = []string{strings.Join(rest, " ")}
		case "module":
			if len(rest) != 1 || rest[0] != "yes" {
				return fail(lineNo, "bad module value")
			}
			c.Module = true
			rec.Fields = []string{"yes"}
		case "encode-error":
			if len(rest) != 1 {
				return fail(lineNo, "encode-error wants 1 field")
			}
			if !pendingSend {
				return fail(lineNo, "encode-error without send")
			}
			pendingSend = false
			rec.Fields = []string{rest[0]}
		case "send":
			if len(rest) < 1 || (rest[0] != "addon" && rest[0] != "addon-self" && rest[0] != "chat") {
				return fail(lineNo, "bad send transport")
			}
			if pendingSend {
				return fail(lineNo, "send without frame")
			}
			pendingSend = true
			cmd, err := expand(strings.Join(rest[1:], " "))
			if err != nil {
				return fail(lineNo, "%v", err)
			}
			rec.Fields = []string{rest[0], cmd}
		case "frame":
			if !pendingSend {
				return fail(lineNo, "frame without send")
			}
			pendingSend = false
			raw := rest
			if raw[0] != "addon" && raw[0] != "chat" {
				return fail(lineNo, "bad frame shape")
			}
			need := 4
			if raw[0] == "chat" {
				need = 3
			}
			if len(raw) < need {
				return fail(lineNo, "bad frame shape")
			}
			head, err := expandFields(raw[:need-1])
			if err != nil {
				return fail(lineNo, "%v", err)
			}
			text, err := expand(strings.Join(raw[need-1:], " "))
			if err != nil {
				return fail(lineNo, "%v", err)
			}
			rec.Fields = append(head, text)
		case "recv":
			raw := rest
			if len(raw) < 1 || (raw[0] != "addon" && raw[0] != "system") {
				return fail(lineNo, "bad recv shape")
			}
			need := 4
			if raw[0] == "system" {
				need = 2
			}
			if len(raw) < need {
				return fail(lineNo, "bad recv shape")
			}
			head, err := expandFields(raw[:need-1])
			if err != nil {
				return fail(lineNo, "%v", err)
			}
			text, err := expand(strings.Join(raw[need-1:], " "))
			if err != nil {
				return fail(lineNo, "%v", err)
			}
			rec.Fields = append(head, text)
		case "expect":
			if len(rest) < 2 {
				return fail(lineNo, "expect wants 2 fields")
			}
			val, err := expand(strings.Join(rest[1:], " "))
			if err != nil {
				return fail(lineNo, "%v", err)
			}
			rec.Fields = []string{rest[0], val}
		default:
			return fail(lineNo, "unknown record %q", kind)
		}
		c.Records = append(c.Records, rec)
	}
	if len(c.Records) == 0 || c.Records[0].Kind != "case" {
		return Case{}, fmt.Errorf("tbm: case %s: first record must be case", name)
	}
	if pendingSend {
		return Case{}, fmt.Errorf("tbm: case %s: send without frame", name)
	}
	return c, nil
}

// expandFields expands escapes in every field.
func expandFields(fields []string) ([]string, error) {
	out := make([]string, len(fields))
	for i, f := range fields {
		var err error
		out[i], err = expand(f)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// expand resolves \n, \\ and {rep N C} in one field.
func expand(s string) (string, error) {
	if !strings.Contains(s, "{rep ") && !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "{rep "):
			end := strings.Index(s[i:], "}")
			if end < 0 {
				return "", fmt.Errorf("bad {rep} in %q", s)
			}
			inner := s[i+5 : i+end]
			parts := strings.SplitN(inner, " ", 2)
			if len(parts) != 2 {
				return "", fmt.Errorf("bad {rep} in %q", s)
			}
			n, err := strconv.Atoi(parts[0])
			if err != nil || n < 1 || n > 1000 {
				return "", fmt.Errorf("bad {rep} in %q", s)
			}
			if len([]rune(parts[1])) != 1 {
				return "", fmt.Errorf("bad {rep} in %q", s)
			}
			b.WriteString(strings.Repeat(parts[1], n))
			i += end + 1
		case s[i] == '\\' && i+1 < len(s) && (s[i+1] == 'n' || s[i+1] == '\\'):
			if s[i+1] == 'n' {
				b.WriteByte('\n')
			} else {
				b.WriteByte('\\')
			}
			i += 2
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), nil
}

// LoadCases reads every .case file under dir, sorted by case name.
func LoadCases(fsys fs.FS, dir string) ([]Case, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".case") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []Case
	for _, n := range names {
		data, err := fs.ReadFile(fsys, path.Join(dir, n))
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(n, ".case")
		c, err := ParseCase(stem, data)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
