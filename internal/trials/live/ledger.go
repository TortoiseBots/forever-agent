package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Ledger is ledger.jsonl: every summon guid, logged-in bot, aura and GM
// line, appended as we go so a killed runner still leaves the cleanup
// trail. `trials cleanup` replays it.
type Ledger struct {
	path   string
	mu     sync.Mutex
	fileMu sync.Mutex
	rows   []map[string]any
}

// NewLedger opens path, loading the rows already there; a missing file
// starts empty. A bad line is skipped, never fatal.
func NewLedger(path string) *Ledger {
	l := &Ledger{path: path}
	if path == "" {
		return l
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	for _, line := range splitLines(string(raw)) {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		l.rows = append(l.rows, row)
	}
	return l
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// Add appends row (stamping "at" when absent) to memory and to the file.
func (l *Ledger) Add(row map[string]any) {
	next := map[string]any{}
	for k, v := range row {
		next[k] = v
	}
	if _, ok := next["at"]; !ok {
		next["at"] = nowISO()
	}
	line, err := json.Marshal(next)
	if err != nil {
		l.mu.Lock()
		l.rows = append(l.rows, next)
		l.mu.Unlock()
		return
	}
	l.mu.Lock()
	l.rows = append(l.rows, next)
	path := l.path
	l.mu.Unlock()
	if path == "" {
		return
	}
	// Serialized like Marks.Add: the file order matches memory order.
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// Rows returns a copy of the rows in file order.
func (l *Ledger) Rows() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]any(nil), l.rows...)
}

// OfKind returns the rows of one kind.
func (l *Ledger) OfKind(kind string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, r := range l.rows {
		if k, _ := r["kind"].(string); k == kind {
			out = append(out, r)
		}
	}
	return out
}

// Summons returns the summon rows.
func (l *Ledger) Summons() []map[string]any { return l.OfKind("summon") }

// SummonGUIDs returns the summon guids in ledger order.
func (l *Ledger) SummonGUIDs() []string {
	var out []string
	for _, r := range l.Summons() {
		if g, _ := r["guid"].(string); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// Slots returns the lease rows for cleanup.
func (l *Ledger) Slots() []map[string]any { return l.OfKind("lease") }
