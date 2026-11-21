package tbcmd

// The battleground status read: `.tb bg status`, the one module read the
// bg-watch sends during the match (no orders). The module answers with plain
// `bg ...` rows: a `bg: enabled=1 ...` config row, a `bg maps: ...` row, one
// `bg instance=N map=wsg status=... score=A-H alliance=1h+5b horde=0h+6b`
// row per match holding bots, one `bg bot=... role=- objective=- map=...
// instance=N team=... alive=1 move_age=2s` row per bot inside and one
// `bg queued ...` row per bot waiting, then `bg-end verb=status lines=N #tok`.
// Only the end row carries the token and every row arrives behind the
// module's "TortoiseBots: " prefix. The older TBM:BG / TBM:BG_BOT record
// grammar (watcher's team, escorts, deaths) is read too. A reply with no BG
// block, or one that says the module is off, is a bad report, so the watcher
// never reads a refused answer as an empty match.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	rep, err := c.callBare(ctx, ".tb bg status", "bg-end", 0, "bg")
	if err != nil {
		return BGStatusReport{}, err
	}
	return parseBGStatus(rep)
}

// parseBGStatus reads the report. Record keys are read loosely (Get answers
// (string, bool); a missing field is the zero value and an unknown key is
// ignored): this layer refuses only a reply with no BG block at all, one that
// says the module is off, and a malformed value of a key it reads.
func parseBGStatus(rep *command.Reply) (BGStatusReport, error) {
	out := BGStatusReport{Raw: append([]string(nil), rep.Raw...)}
	seen := false
	var mod bgModule
	for _, l := range rep.Lines {
		raw := strings.TrimPrefix(l.Raw, command.ModulePrefix)
		row := command.ParseReplyLine(raw)
		kind := strings.TrimSuffix(row.Kind, ":")
		switch kind {
		case "bg":
			if mod.add(row) {
				seen = true
			}
			continue
		case "TBM:BG", "TBM:BG_BOT":
		default:
			continue
		}
		if row.Bad != "" {
			return BGStatusReport{}, fmt.Errorf("%w: %s", ErrBadReport, row.Bad)
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
			moveAge, err := bgAge(row, "move_age")
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
	if mod.seen {
		if err := mod.fill(&out); err != nil {
			return BGStatusReport{}, err
		}
	}
	if mod.off {
		return BGStatusReport{}, fmt.Errorf("%w: .tb bg status says the module is off (enabled=0)", ErrBadReport)
	}
	if !seen {
		return BGStatusReport{}, fmt.Errorf("%w: .tb bg status answered no BG block", ErrBadReport)
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

// bgAge reads a move age: whole or fractional seconds ("70"), a duration the
// module prints ("2s", "1m30s"), or "-" for a bot that has not moved yet
// (read as 0: not idle). 0 when the line does not carry it.
func bgAge(row command.ReplyLine, key string) (float64, error) {
	s := bgField(row, key)
	if s == "" || s == "-" {
		return 0, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%w: %s=%q is not a duration", ErrBadValue, key, s)
	}
	return d.Seconds(), nil
}

// bgDash reads a module word where "-" means none.
func bgDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// bgSide reads one side's counters, "1h+5b": humans and bots.
func bgSide(key, s string) (humans, bots int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	for _, part := range strings.Split(s, "+") {
		if len(part) < 2 {
			return 0, 0, fmt.Errorf("%w: %s=%q is not <n>h+<n>b", ErrBadValue, key, s)
		}
		n, e := strconv.Atoi(part[:len(part)-1])
		if e != nil || n < 0 {
			return 0, 0, fmt.Errorf("%w: %s=%q is not <n>h+<n>b", ErrBadValue, key, s)
		}
		switch part[len(part)-1] {
		case 'h':
			humans = n
		case 'b':
			bots = n
		default:
			return 0, 0, fmt.Errorf("%w: %s=%q is not <n>h+<n>b", ErrBadValue, key, s)
		}
	}
	return humans, bots, nil
}

// bgScore reads "A-H".
func bgScore(s string) (a, h int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	l, r, ok := strings.Cut(s, "-")
	if ok {
		a, e1 := strconv.Atoi(l)
		h, e2 := strconv.Atoi(r)
		if e1 == nil && e2 == nil && a >= 0 && h >= 0 {
			return a, h, nil
		}
	}
	return 0, 0, fmt.Errorf("%w: score=%q is not <n>-<n>", ErrBadValue, s)
}

// bgInstance is one `bg instance=N ...` row.
type bgInstance struct {
	id              string
	mapName, status string
	scoreA, scoreH  int
	allH, allB      int
	hordeH, hordeB  int
}

// bgBotRow is one `bg bot=... ` row with the instance it names.
type bgBotRow struct {
	instance string
	bot      BGStatusBot
}

// bgModule collects the module's `bg ...` rows of one answer; fill picks the
// watcher's instance and writes the report. Rows it does not know (the maps
// row, queued bots, keys it has never seen) are skipped, so a later module
// round that adds a key does not break the poll.
type bgModule struct {
	seen      bool // an instance, bot or enabled config row came in
	off       bool // the config row says enabled=0
	instances []bgInstance
	bots      []bgBotRow
	err       error
}

// add takes one `bg` row and says whether it is part of a BG block.
func (m *bgModule) add(row command.ReplyLine) bool {
	if row.Kind == "bg:" {
		if v, ok := row.Get("enabled"); ok {
			if v == "0" {
				m.off = true
				return false
			}
			m.seen = true
			return true
		}
		return false
	}
	if len(row.Words) > 0 && (row.Words[0] == "maps:" || row.Words[0] == "queued") {
		return false
	}
	if _, ok := row.Fields["bot"]; ok {
		move, err := bgAge(row, "move_age")
		if err != nil && m.err == nil {
			m.err = err
		}
		alive := bgField(row, "alive")
		m.bots = append(m.bots, bgBotRow{
			instance: bgField(row, "instance"),
			bot: BGStatusBot{
				Role:      bgField(row, "role"),
				Objective: bgDash(bgField(row, "objective")),
				Node:      bgDash(bgField(row, "node")),
				Alive:     alive != "0" && !strings.EqualFold(alive, "no"),
				MoveAgeS:  move,
			},
		})
		m.seen = true
		return true
	}
	if _, ok := row.Fields["instance"]; ok {
		in := bgInstance{id: bgField(row, "instance"), mapName: bgField(row, "map"), status: bgField(row, "status")}
		var err error
		if in.scoreA, in.scoreH, err = bgScore(bgField(row, "score")); err != nil && m.err == nil {
			m.err = err
		}
		if in.allH, in.allB, err = bgSide("alliance", bgField(row, "alliance")); err != nil && m.err == nil {
			m.err = err
		}
		if in.hordeH, in.hordeB, err = bgSide("horde", bgField(row, "horde")); err != nil && m.err == nil {
			m.err = err
		}
		m.instances = append(m.instances, in)
		m.seen = true
		return true
	}
	return false
}

// fill writes the watcher's instance into out: the first instance holding a
// human (the watcher is one), else the first. The watcher's team is the side
// that holds the humans when only one side does. Bot rows of other instances
// are dropped. A module with no instance yet (the human is still queued) is
// an empty, not-in-progress report.
func (m *bgModule) fill(out *BGStatusReport) error {
	if m.err != nil {
		return m.err
	}
	if len(m.instances) == 0 {
		return nil
	}
	pick := m.instances[0]
	for _, in := range m.instances {
		if in.allH+in.hordeH > 0 {
			pick = in
			break
		}
	}
	out.Map, out.Status = pick.mapName, pick.status
	out.AllH, out.AllB, out.HordeH, out.HordeB = pick.allH, pick.allB, pick.hordeH, pick.hordeB
	out.ScoreA, out.ScoreH = pick.scoreA, pick.scoreH
	switch {
	case pick.allH > 0 && pick.hordeH == 0:
		out.Team = "alliance"
	case pick.hordeH > 0 && pick.allH == 0:
		out.Team = "horde"
	}
	for _, b := range m.bots {
		if b.instance == "" || b.instance == pick.id {
			out.Bots = append(out.Bots, b.bot)
		}
	}
	return nil
}
