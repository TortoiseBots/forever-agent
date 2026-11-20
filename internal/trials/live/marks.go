package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// Marks is the repeat's phase rows and the ONE clock of its files.
//
// The host wall clock can step back seconds at a time, so rows stamped with
// time.Now would run backwards in blocks. nowISO is the wall clock read ONCE
// at construction plus the monotonic time since: it never steps, and the
// sampler and the situation stamp with it.
type Marks struct {
	path string
	mu   sync.Mutex
	rows []map[string]any
	base string

	anchorWall float64
	anchorMono time.Time
}

// NewMarks opens path, loading the rows already there.
func NewMarks(path string) *Marks {
	m := &Marks{path: path, anchorWall: float64(time.Now().UnixNano()) / 1e9, anchorMono: time.Now()}
	if path == "" {
		return m
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range splitLines(string(raw)) {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		m.rows = append(m.rows, row)
	}
	return m
}

// nowISO is the anchor wall plus the monotonic time since.
func (m *Marks) nowISO(offsetSecs float64) string {
	secs := m.anchorWall + time.Since(m.anchorMono).Seconds() + offsetSecs
	return time.Unix(int64(secs), int64((secs-float64(int64(secs)))*1e9)).UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
}

// nowISO exports the clock for files the collector writes directly.
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
}

// NowISO is the monotonic-backed wall clock.
func (m *Marks) NowISO() string { return m.nowISO(0) }

// Start sets the time base: wall or now.
func (m *Marks) Start(wall string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if wall == "" {
		wall = m.nowISO(0)
	}
	m.base = wall
}

// Rel is seconds from the base wall to wall (0 with no base), rounded to 2
// decimals like the Python.
func (m *Marks) Rel(wall string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.relLocked(wall)
}

func (m *Marks) relLocked(wall string) float64 {
	if m.base == "" {
		return 0
	}
	if wall == "" {
		wall = m.nowISO(0)
	}
	a, errA := time.Parse(time.RFC3339Nano, wall)
	b, errB := time.Parse(time.RFC3339Nano, m.base)
	if errA != nil || errB != nil {
		return 0
	}
	return pyjson.Round(a.Sub(b).Seconds(), 2)
}

func round2(f float64) float64 { return pyjson.Round(f, 2) }

// Add appends row (stamping wall/t when absent) to memory and the file.
func (m *Marks) Add(row map[string]any) map[string]any {
	next := map[string]any{}
	for k, v := range row {
		next[k] = v
	}
	m.mu.Lock()
	if _, ok := next["wall"]; !ok {
		next["wall"] = m.nowISO(0)
	}
	wall, _ := next["wall"].(string)
	if _, ok := next["t"]; !ok {
		next["t"] = m.relLocked(wall)
	}
	m.rows = append(m.rows, next)
	path := m.path
	m.mu.Unlock()
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				line, jerr := json.Marshal(next)
				if jerr == nil {
					_, _ = f.Write(append(line, '\n'))
				}
				_ = f.Close()
			}
		}
	}
	return next
}

// Excluded adds an excluded span row.
func (m *Marks) Excluded(from, to float64, why string) map[string]any {
	return m.Add(map[string]any{"kind": "excluded", "from": from, "to": to, "why": why})
}

// Kinds returns the rows of one kind.
func (m *Marks) Kinds(kind string) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for _, r := range m.rows {
		if k, _ := r["kind"].(string); k == kind {
			out = append(out, r)
		}
	}
	return out
}

// Rows returns a copy of the rows in file order.
func (m *Marks) Rows() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.rows...)
}
