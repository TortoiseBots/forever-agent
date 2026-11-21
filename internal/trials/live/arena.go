package live

// arena.go: the trial arenas and their exclusive locks.
//
// An arena is one quiet point of a known map from data/arenas.json (plus
// the Forever overlay, whose rows win). The situation takes the arena's
// lock for the whole repeat — through the summon, the fight and the
// teardown — so two runs on one machine never fight in one arena. The lock
// is an flock on `$XDG_STATE_HOME/forever-trials/arena-<server>-<id>.lock`:
// the OS drops it on a crash, so it never goes stale.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/data"
)

// Arena is one arena point: its level window bounds which trials may use it.
type Arena struct {
	ID       string  `json:"id"`
	Map      int     `json:"map"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Z        float64 `json:"z"`
	LevelMin int     `json:"level_min"`
	LevelMax int     `json:"level_max"`
}

// MobEntry is one summonable creature entry and its level range.
type MobEntry struct {
	Entry    int    `json:"entry"`
	Name     string `json:"name"`
	LevelMin int    `json:"level_min"`
	LevelMax int    `json:"level_max"`
	Faction  int    `json:"faction,omitempty"`
}

// MobTable is data/mobs.json: role -> entries.
type MobTable map[string][]MobEntry

// LoadArenas reads the embedded arenas with the Forever overlay applied.
func LoadArenas() []Arena {
	rows, err := ArenasFrom(data.Read("arenas.json"), data.ReadOverlay("arenas.json"))
	if err != nil {
		return nil
	}
	return rows
}

// arenasDoc is the base file's array shape.
func ArenasFrom(base, overlay []byte) ([]Arena, error) {
	var rows []Arena
	if len(base) > 0 {
		if err := json.Unmarshal(base, &rows); err != nil {
			return nil, err
		}
	}
	var extra []Arena
	if len(overlay) > 0 {
		if err := json.Unmarshal(overlay, &extra); err != nil {
			return nil, err
		}
	}
	// The overlay row whose id matches a base row replaces it in place; a
	// new id is appended after the base rows, in overlay order.
	index := map[string]int{}
	for i, a := range rows {
		index[a.ID] = i
	}
	for _, a := range extra {
		if a.ID == "" {
			continue
		}
		if i, ok := index[a.ID]; ok {
			rows[i] = a
			continue
		}
		index[a.ID] = len(rows)
		rows = append(rows, a)
	}
	return rows, nil
}

// LoadMobs reads the embedded mob table with the Forever overlay applied.
func LoadMobs() MobTable {
	rows, err := MobsFrom(data.Read("mobs.json"), data.ReadOverlay("mobs.json"))
	if err != nil {
		return nil
	}
	return rows
}

// MobsFrom merges a mob table: an overlay entry replaces the base entry of
// the same role and entry number, a new one is appended.
func MobsFrom(base, overlay []byte) (MobTable, error) {
	out := MobTable{}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &out); err != nil {
			return nil, err
		}
	}
	var extra MobTable
	if len(overlay) > 0 {
		if err := json.Unmarshal(overlay, &extra); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = MobTable{}
	}
	for role, rows := range extra {
		have := map[int]int{}
		for i, e := range out[role] {
			have[e.Entry] = i
		}
		for _, e := range rows {
			if e.Entry == 0 {
				continue
			}
			if i, ok := have[e.Entry]; ok {
				out[role][i] = e
				continue
			}
			have[e.Entry] = len(out[role])
			out[role] = append(out[role], e)
		}
	}
	return out, nil
}

// pickEntries ranks the role's entries for a level window: entries whose
// whole range sits inside the window first, then the smallest gap, then the
// closest middle (the Python pick_entry).
func pickEntries(mobs MobTable, role string, low, high int) []MobEntry {
	rows := append([]MobEntry(nil), mobs[role]...)
	mid := (float64(low) + float64(high)) / 2
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rows[i], rows[j]
		insideI, insideJ := 0, 0
		if ri.LevelMin < low || ri.LevelMax > high {
			insideI = 1
		}
		if rj.LevelMin < low || rj.LevelMax > high {
			insideJ = 1
		}
		if insideI != insideJ {
			return insideI < insideJ
		}
		gapI := maxInt(0, low-ri.LevelMax, ri.LevelMin-high)
		gapJ := maxInt(0, low-rj.LevelMax, rj.LevelMin-high)
		if gapI != gapJ {
			return gapI < gapJ
		}
		di := absFloat((float64(ri.LevelMin)+float64(ri.LevelMax))/2 - mid)
		dj := absFloat((float64(rj.LevelMin)+float64(rj.LevelMax))/2 - mid)
		return di < dj
	})
	return rows
}

func maxInt(v int, rest ...int) int {
	for _, r := range rest {
		if r > v {
			v = r
		}
	}
	return v
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// arenaLockWord makes a filename-safe word of a server or arena id.
var arenaLockWord = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func lockWord(part string) string {
	return arenaLockWord.ReplaceAllString(part, "_")
}

// ArenaStateDir is where the arena locks live: $XDG_STATE_HOME/forever-trials,
// else ~/.local/state/forever-trials. Outside the repo (a runtime file).
func ArenaStateDir() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return filepath.Join(dir, "forever-trials")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "forever-trials")
	}
	return filepath.Join(home, ".local", "state", "forever-trials")
}

// ArenaLock is one arena's exclusive file lock. A context-free pair of
// Acquire/Release; the OS drops the lock when the process ends, so a crash
// never leaves it held.
type ArenaLock struct {
	path string
	f    *os.File
}

// NewArenaLock returns the lock for (server, arenaID) under baseDir ("" is
// ArenaStateDir).
func NewArenaLock(server, arenaID, baseDir string) *ArenaLock {
	if baseDir == "" {
		baseDir = ArenaStateDir()
	}
	return &ArenaLock{path: filepath.Join(baseDir, "arena-"+lockWord(server)+"-"+lockWord(arenaID)+".lock")}
}

// Path is the lock file (tests assert the shape).
func (l *ArenaLock) Path() string { return l.path }

// Held reports whether this lock is held.
func (l *ArenaLock) Held() bool { return l.f != nil }

// Release gives the lock back; safe to call twice.
func (l *ArenaLock) Release() {
	f := l.f
	l.f = nil
	if f == nil {
		return
	}
	_ = unlockFile(f)
	_ = f.Close()
}

// Acquire takes the arena's lock without blocking; false means another run
// holds it.
func (l *ArenaLock) Acquire() bool {
	if l.f != nil {
		return true
	}
	f, ok := lockFile(l.path)
	if !ok {
		return false
	}
	l.f = f
	return true
}
