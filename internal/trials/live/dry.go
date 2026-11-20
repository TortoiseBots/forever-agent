package live

import (
	"context"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// DrySetup is the --dry scripted setup: a fixed bot/extra/mob guid table
// and one ok setup row per required check id, so the scorer sees a valid
// repeat. Real setup (bots.go player.go setup.go) arrives in A3.
type DrySetup struct {
	BotName string
	BotGUID string
}

// isDrySetup reports the scripted setup (run.go asks it for dry mode).
func isDrySetup(s Setup) bool {
	_, ok := s.(*DrySetup)
	return ok
}

// dryCheckIDs are the setup rows the dry setup writes ok.
var dryCheckIDs = []string{"bot_present", "grouped", "mobs_ready", "player_visible", "config_read"}

// Run builds the dry setup doc.
func (d *DrySetup) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	bot := d.BotGUID
	if bot == "" {
		bot = "0x0000000000000002"
	}
	name := d.BotName
	if name == "" {
		name = "Atank"
	}
	player := "0x0000000000000001"
	checks := []any{}
	for _, id := range dryCheckIDs {
		ok := true
		val := "dry"
		if id == "config_read" {
			ok = false
			val = "dry: no Forever source"
		}
		checks = append(checks, map[string]any{"id": id, "ok": ok, "value": val,
			"evidence": "dry scripted setup"})
	}
	_ = ctx
	_ = r
	return map[string]any{
		"ok":          true,
		"bot_name":    name,
		"extra_names": []any{},
		"guids": map[string]any{
			"player": player, "bot": bot,
			"extras": []any{}, "mobs": []any{},
		},
		"checks": checks,
		"config": map[string]any{"critical": nil, "low": nil, "medium": nil, "ok": false},
	}, nil
}

// DrySituation is the --dry scripted situation: it waits pulls for the
// collector to tick, marks fight_start/fight_end, and ends "complete".
// Real situations (tank_damage et al) arrive in A4-A5.
type DrySituation struct {
	// FightSecs bounds the collect window; 0 means a short default.
	FightSecs float64
}

// Run marks the fight window and waits for samples. The fight_end row
// carries "ended": "complete", the same key the scorer reads.
func (d *DrySituation) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	secs := d.FightSecs
	if secs <= 0 {
		secs = 3
	}
	start := r.Marks.Add(map[string]any{"kind": "fight_start"})
	deadline := time.Now().Add(time.Duration(secs * float64(time.Second)))
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
		if r.Collect.Lost() != "" {
			return map[string]any{"ended": "disconnected",
				"invalid_reason": r.Collect.Lost()}, nil
		}
	}
	end := r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "complete"})
	_ = start
	_ = end
	return map[string]any{"ended": "complete",
		"t": map[string]any{"start": start["t"], "end": end["t"]}}, nil
}

// ScoreCard scores the whole out dir after all trials ran (ScoreRunE) and
// returns its verdict docs.
func ScoreCard(outDir string) ([]map[string]any, error) {
	return scoreRun(outDir)
}

// scoreRun is ScoreRunE without the import cycle in tests' way.
func scoreRun(outDir string) ([]map[string]any, error) {
	return scoreRunE(outDir)
}

// pyjsonStr keeps pyjson referenced when helpers move.
var _ = pyjson.Str
