package live

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
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
		guard := NewGuard(client, GuardOptions{Ledger: ledger})
		for _, guid := range byDir[dir] {
			rows = append(rows, killOne(ctx, guard, err, guid, timeout))
		}
	}
	sort.Strings(nodir)
	for _, guid := range nodir {
		rows = append(rows, map[string]any{"id": "mobs_dead", "ok": false,
			"value": guid, "evidence": "no serve dir in ledger row: skipped"})
	}
	return rows, nil
}

func killOne(ctx context.Context, guard *Guard, openErr error, guid string, timeout time.Duration) map[string]any {
	if openErr != nil {
		return map[string]any{"id": "mobs_dead", "ok": false,
			"value": guid, "evidence": "no serve: skipped kill of " + guid}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The kill goes through the guard's closed set at the summon target:
	// raw client.GM can no longer bypass it from cleanup.
	if err := guard.Send(ctx, "die", TargetSummon, guid); err != nil {
		return map[string]any{"id": "mobs_dead", "ok": false,
			"value": guid, "evidence": "kill " + guid + ": " + err.Error()}
	}
	return map[string]any{"id": "mobs_dead", "ok": true,
		"value": guid, "evidence": "gm kill " + guid}
}

// RowOK reports a cleanup row's ok flag.
func RowOK(row map[string]any) bool { return boolOf(row["ok"]) }

// CleanupRowKind names the row for the CLI's leftover summary.
func CleanupRowKind(row map[string]any) string { return pyjson.Str(row["id"]) }

// ReleaseLedgerLeases releases every lease row the ledger holds. slotsPath
// is the same file the run leased from; when it is empty or unreadable no
// store exists to release against and every lease row reports the skip, so
// `trials cleanup` can never claim a release it did not make. A lease the
// store no longer knows (already released or taken over) counts as gone.
func ReleaseLedgerLeases(slotsPath string, ledger *Ledger) []map[string]any {
	rows := ledger.Slots()
	if len(rows) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(rows))
	if strings.TrimSpace(slotsPath) == "" {
		for _, r := range rows {
			out = append(out, map[string]any{"id": "lease_released", "ok": false,
				"value":    leaseRowValue(r),
				"evidence": "no slots file: lease not released, expires on its own"})
		}
		return out
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		for _, r := range rows {
			out = append(out, map[string]any{"id": "lease_released", "ok": false,
				"value": leaseRowValue(r), "evidence": "slots file unreadable: " + err.Error()})
		}
		return out
	}
	store := slots.NewStore(cfg, slotsPath)
	for _, r := range rows {
		server, _ := r["server"].(string)
		slot, _ := r["slot"].(string)
		holder, _ := r["holder"].(string)
		leaseID, _ := r["lease_id"].(string)
		if server == "" || slot == "" || holder == "" || leaseID == "" {
			out = append(out, map[string]any{"id": "lease_released", "ok": false,
				"value": leaseRowValue(r), "evidence": "ledger lease row incomplete: skipped"})
			continue
		}
		if _, rerr := store.Release(server, slot, holder, leaseID, false); rerr != nil {
			if cur, gerr := store.Get(server, slot); gerr == nil && cur == nil {
				out = append(out, map[string]any{"id": "lease_released", "ok": true,
					"value": leaseRowValue(r), "evidence": "already released"})
				continue
			}
			out = append(out, map[string]any{"id": "lease_released", "ok": false,
				"value": leaseRowValue(r), "evidence": "release: " + rerr.Error()})
			continue
		}
		out = append(out, map[string]any{"id": "lease_released", "ok": true,
			"value": leaseRowValue(r), "evidence": "lease released"})
	}
	return out
}

func leaseRowValue(r map[string]any) string {
	server, _ := r["server"].(string)
	slot, _ := r["slot"].(string)
	holder, _ := r["holder"].(string)
	if server == "" && slot == "" {
		return pyjson.Str(r["ledger"])
	}
	return server + "/" + slot + " (" + holder + ")"
}
