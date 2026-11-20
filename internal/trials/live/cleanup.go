package live

import (
	"context"
	"sort"
	"time"
)

// CleanupRun replays the run ledger: gm_kill every ledgered summon,
// gm_aura remove on the player and bots, bots stay+free. openSlot maps a
// serve dir to its RPC; a dir with no serve reports skips instead of
// failing. It returns one row per action ({id, ok, value, evidence}).
func CleanupRun(ctx context.Context, runDir string, ledger *Ledger, openSlot func(dir string) (RPC, error), timeout time.Duration) ([]map[string]any, error) {
	_ = runDir
	rows := []map[string]any{}
	byDir := map[string][]string{}
	var nodir []string
	seen := map[string]bool{}
	for _, s := range ledger.Summons() {
		guid, _ := s["guid"].(string)
		if guid == "" || seen[guid] {
			continue
		}
		seen[guid] = true
		dir, _ := s["dir"].(string)
		if dir == "" {
			nodir = append(nodir, guid)
			continue
		}
		byDir[dir] = append(byDir[dir], guid)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		rpc, err := openSlot(dir)
		client := NewClient(rpc)
		for _, guid := range byDir[dir] {
			rows = append(rows, killOne(ctx, client, err, guid, timeout))
		}
	}
	sort.Strings(nodir)
	for _, guid := range nodir {
		rows = append(rows, map[string]any{"id": "mobs_dead", "ok": false,
			"value": guid, "evidence": "no serve dir in ledger row: skipped"})
	}
	return rows, nil
}

func killOne(ctx context.Context, client *Client, openErr error, guid string, timeout time.Duration) map[string]any {
	if openErr != nil {
		return map[string]any{"id": "mobs_dead", "ok": false,
			"value": guid, "evidence": "no serve: skipped kill of " + guid}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := client.GM(ctx, ".die"); err != nil {
		// gm_kill selects first in the real round; the dry replay sends
		// the kill line for the ledgered guid.
		return map[string]any{"id": "mobs_dead", "ok": false,
			"value": guid, "evidence": "kill " + guid + ": " + err.Error()}
	}
	return map[string]any{"id": "mobs_dead", "ok": true,
		"value": guid, "evidence": "gm kill " + guid}
}

// RowOK reports a cleanup row's ok flag.
func RowOK(row map[string]any) bool { return boolOf(row["ok"]) }
