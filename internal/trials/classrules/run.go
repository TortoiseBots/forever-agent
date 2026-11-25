package classrules

// TrialRows judges every repeat of one trial: one row per class rule per repeat, in the report
// card's shape. A trial with no repeat directories has no rows.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var repDirRe = regexp.MustCompile(`^rep(\d+)$`)

// TrialRows judges every repeat of a trial directory.
func TrialRows(trialDir string, trial map[string]any) []any {
	out := []any{}
	for _, rep := range repDirs(trialDir) {
		rec := LoadRep(rep, trial)
		for _, row := range Rows(rec) {
			row.Rep = filepath.Base(rep)
			out = append(out, row.Doc())
		}
	}
	return out
}

// repDirs lists a trial's rep* directories by number (rep10 after rep9), like the scorer does.
func repDirs(trialDir string) []string {
	entries, err := os.ReadDir(trialDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() || !repDirRe.MatchString(entry.Name()) {
			continue
		}
		out = append(out, filepath.Join(trialDir, entry.Name()))
	}
	sort.SliceStable(out, func(i, j int) bool { return repNumber(out[i]) < repNumber(out[j]) })
	return out
}

// repNumber is the numeric index of a repN directory, -1 when the name has none.
func repNumber(path string) int {
	name := strings.TrimSuffix(filepath.Base(path), "/")
	m := repDirRe.FindStringSubmatch(name)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
