package tbcmd

// The battleground status read: `.tb bg status`, the one module read the
// bg-watch sends during the match (no orders). The reply is one TBM:BG
// record with the watcher's team, the instance, map, status, scores and the
// escort count, then one TBM:BG_BOT record per bot with its role, objective,
// node, alive state and move age. A reply with no BG block is a bad report,
// so the watcher never reads a refused answer as an empty match.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// BGStatusReport is one `.tb bg status` answer: the per-side humans/bots and
// the per-bot rows. Names never leave the session: a bot row carries its
// role word, not a character name.
type BGStatusReport struct {
	// Team is the watcher's side ("alliance" or "horde").
	Team    string
	Map     string
	Status  string
	AllH    int
	AllB    int
	HordeH  int
	HordeB  int
	ScoreA  int
	ScoreH  int
	DeathsA int
	DeathsH int
	// FCEscorts counts the watcher's team within 20 yd of the flag carrier,
	// the carrier itself excluded.
	FCEscorts int
	Deserter  bool
	Fallback  bool
	HasFC     bool
	FCX       float64
	FCY       float64
	Bots      []BGStatusBot
	Raw       []string
}

// BGStatusBot is one bot of the status answer.
type BGStatusBot struct {
	Role      string
	Objective string
	// Node is the AB node the bot is assigned to, "" elsewhere.
	Node     string
	Alive    bool
	MoveAgeS float64
}

// BGStatus reads the battleground status. The report is only returned when
// it carries the BG block; unparseable lines are skipped, a reply with no BG
// block is ErrBadReport.
func (c *Client) BGStatus(ctx context.Context) (BGStatusReport, error) {
	rep, err := c.call(ctx, ".tb bg status", "bg-end")
	if err != nil {
		return BGStatusReport{}, err
	}
	return parseBGStatus(rep)
}

// bgStatusKinds are the record kinds this layer reads. The module names the
// record; these two tell a well-formed report from another command's reply.
var bgStatusKinds = map[string]bool{"TBM:BG": true, "TBM:BG_BOT": true}

// parseBGStatus reads the report. Record keys are read loosely (Get answers
// (string, bool); a missing field is the zero value): the module's future
// grammar owns the exact keys, and this layer refuses only a reply with no
// BG block at all.
func parseBGStatus(rep *command.Reply) (BGStatusReport, error) {
	out := BGStatusReport{Raw: append([]string(nil), rep.Raw...)}
	seen := false
	for _, l := range rep.Lines {
		row := command.ParseReplyLine(l.Raw)
		if row.Bad != "" {
			return BGStatusReport{}, fmt.Errorf("%w: %s", ErrBadReport, row.Bad)
		}
		if !bgStatusKinds[l.Kind] && !bgStatusKinds[row.Kind] {
			continue
		}
		kind := l.Kind
		if kind == "" {
			kind = row.Kind
		}
		switch kind {
		case "TBM:BG":
			seen = true
			out.Team = bgField(row, "team")
			out.Map = bgField(row, "map")
			out.Status = bgField(row, "status")
			var err error
			if out.AllH, err = bgInt(row, "alliance_humans"); err != nil {
				return BGStatusReport{}, err
			}
			if out.AllB, err = bgInt(row, "alliance_bots"); err != nil {
				return BGStatusReport{}, err
			}
			if out.HordeH, err = bgInt(row, "horde_humans"); err != nil {
				return BGStatusReport{}, err
			}
			if out.HordeB, err = bgInt(row, "horde_bots"); err != nil {
				return BGStatusReport{}, err
			}
			if out.ScoreA, err = bgInt(row, "alliance_score"); err != nil {
				return BGStatusReport{}, err
			}
			if out.ScoreH, err = bgInt(row, "horde_score"); err != nil {
				return BGStatusReport{}, err
			}
			if out.DeathsA, err = bgInt(row, "alliance_deaths"); err != nil {
				return BGStatusReport{}, err
			}
			if out.DeathsH, err = bgInt(row, "horde_deaths"); err != nil {
				return BGStatusReport{}, err
			}
			if out.FCEscorts, err = bgInt(row, "fc_escorts"); err != nil {
				return BGStatusReport{}, err
			}
			out.Deserter = bgYes(bgField(row, "deserter"))
			out.Fallback = bgYes(bgField(row, "fallback"))
			out.HasFC = bgYes(bgField(row, "fc"))
			if out.FCX, err = bgFloat(row, "fc_x"); err != nil {
				return BGStatusReport{}, err
			}
			if out.FCY, err = bgFloat(row, "fc_y"); err != nil {
				return BGStatusReport{}, err
			}
		case "TBM:BG_BOT":
			seen = true
			moveAge, err := bgFloat(row, "move_age")
			if err != nil {
				return BGStatusReport{}, err
			}
			alive := bgField(row, "alive")
			out.Bots = append(out.Bots, BGStatusBot{
				Role:      bgField(row, "role"),
				Objective: bgField(row, "objective"),
				Node:      bgField(row, "node"),
				Alive:     alive != "0" && !strings.EqualFold(alive, "no"),
				MoveAgeS:  moveAge,
			})
		}
	}
	if !seen {
		return BGStatusReport{}, fmt.Errorf("%w: .tb bg status answered no TBM:BG block", ErrBadReport)
	}
	return out, nil
}

// bgField reads one reply field, "" when the line does not carry it.
func bgField(row command.ReplyLine, key string) string {
	v, _ := row.Get(key)
	return v
}

// bgInt reads an int field, 0 when the line does not carry it, an error
// when the value is not a number (a malformed value is not a zero).
func bgInt(row command.ReplyLine, key string) (int, error) {
	s := bgField(row, key)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q is not a number", ErrBadValue, key, s)
	}
	return n, nil
}

// bgFloat reads a float field, 0 when the line does not carry it, an error
// when the value is not a number.
func bgFloat(row command.ReplyLine, key string) (float64, error) {
	s := bgField(row, key)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q is not a number", ErrBadValue, key, s)
	}
	return f, nil
}

// bgYes reads a flag field ("1"/"yes", any case).
func bgYes(s string) bool { return s == "1" || strings.EqualFold(s, "yes") }
