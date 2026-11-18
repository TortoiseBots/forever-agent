package scenario

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

var globChars = "*?["

// fnmatchCase is Python's fnmatch.fnmatchcase: `*` any run, `?` one character, `[...]` a
// class (with `!` negating and `a-z` ranges).
func fnmatchCase(name, pattern string) bool {
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		case '[':
			end := i + 1
			if end < len(pattern) && (pattern[end] == '!' || pattern[end] == '^') {
				end++
			}
			if end < len(pattern) && pattern[end] == ']' {
				end++
			}
			for end < len(pattern) && pattern[end] != ']' {
				end++
			}
			if end >= len(pattern) {
				re.WriteString(`\[`)
				continue
			}
			class := pattern[i+1 : end]
			re.WriteString("[")
			if strings.HasPrefix(class, "!") {
				re.WriteString("^")
				class = class[1:]
			}
			re.WriteString(strings.ReplaceAll(class, `\`, `\\`))
			re.WriteString("]")
			i = end
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		return name == pattern
	}
	return compiled.MatchString(name)
}

// MatchSelector lists the paths one selector picks from entries ([(trial id, path)]).
//
// An explicit glob (contains * ? or [) may match MANY trials; a bare id must match EXACTLY
// one (ids are never matched as substrings). No match gives none; a bare id that two files
// carry is an error.
func MatchSelector(sel string, entries [][2]string) ([]string, error) {
	if !strings.Contains(sel, VariantSep) {
		// a selector without "@" reaches base trials only
		var kept [][2]string
		for _, entry := range entries {
			if !strings.Contains(entry[0], VariantSep) {
				kept = append(kept, entry)
			}
		}
		entries = kept
	}
	var exact []string
	for _, entry := range entries {
		if entry[0] == sel {
			exact = append(exact, entry[1])
		}
	}
	if len(exact) > 0 {
		if len(exact) > 1 {
			return nil, fmt.Errorf("selector %s matches %d trials; ids must be unique", pythonRepr(sel), len(exact))
		}
		return exact, nil
	}
	if strings.ContainsAny(sel, globChars) {
		var out []string
		for _, entry := range entries {
			if fnmatchCase(entry[0], sel) {
				out = append(out, entry[1])
			}
		}
		return out, nil
	}
	return nil, nil
}

// ResolveSelectors resolves every selector or fails the whole command. `prefix` (the --class
// shorthand) is tried as `<prefix><selector>` when the selector itself matches nothing.
func ResolveSelectors(selectors []string, entries [][2]string, prefix string) ([]string, error) {
	var out []string
	var bad []string
	for _, sel := range selectors {
		hits, err := MatchSelector(sel, entries)
		if err != nil {
			return nil, err
		}
		if len(hits) == 0 && prefix != "" {
			hits, err = MatchSelector(prefix+strings.ToLower(sel), entries)
			if err != nil {
				return nil, err
			}
		}
		if len(hits) == 0 {
			bad = append(bad, sel)
		}
		out = append(out, hits...)
	}
	if len(bad) > 0 {
		quoted := make([]string, 0, len(bad))
		for _, b := range bad {
			quoted = append(quoted, pythonRepr(b))
		}
		plural := "s"
		if len(bad) == 1 {
			plural = ""
		}
		return nil, fmt.Errorf("selector%s matched no trial: %s (use an exact id or a glob)",
			plural, strings.Join(quoted, ", "))
	}
	seen := map[string]bool{}
	var unique []string
	for _, path := range out {
		if path != "" && !seen[path] {
			seen[path] = true
			unique = append(unique, path)
		}
	}
	sort.Strings(unique)
	return unique, nil
}

// AllTrials loads every embedded trial, with each valid variant appended as its own entry;
// paths line up with trials (`path@variant` for a variant).
func AllTrials() ([]Trial, []string) {
	var trials []Trial
	var paths []string
	for _, path := range TrialFiles() {
		trial, err := LoadTrial(path)
		if err != nil {
			continue
		}
		trials = append(trials, trial)
		paths = append(paths, path)
		for _, variant := range ExpandVariants(trial) {
			trials = append(trials, variant.Trial)
			paths = append(paths, path+VariantSep+variant.Name)
		}
	}
	return trials, paths
}

// Entries pairs every trial with its id for the selectors.
func Entries(trials []Trial, paths []string) [][2]string {
	out := make([][2]string, 0, len(trials))
	for i, trial := range trials {
		path := ""
		if i < len(paths) {
			path = paths[i]
		} else {
			path = pyjson.Str(trial["id"])
		}
		out = append(out, [2]string{pyjson.Str(trial["id"]), path})
	}
	return out
}

// FindTrials resolves a CLI selection to trial paths: selectors, else a class/spec filter.
func FindTrials(selectors []string, botClass, spec string) ([]string, error) {
	trials, paths := AllTrials()
	if len(selectors) > 0 {
		return ResolveSelectors(selectors, Entries(trials, paths), "")
	}
	var out []string
	for i, trial := range trials {
		tid := pyjson.Str(trial["id"])
		if strings.Contains(tid, VariantSep) {
			continue // a class or spec selection runs base trials
		}
		if botClass != "" && pyjson.Str(trial["class"]) != botClass {
			continue
		}
		if spec != "" && pyjson.Str(trial["spec"]) != spec {
			continue
		}
		if i < len(paths) && paths[i] != "" {
			out = append(out, paths[i])
		}
	}
	sort.Strings(out)
	return out, nil
}
