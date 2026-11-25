package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The player run's record (round P5b, #88): the counters the milestone's verdict reads and the evidence the spell
// judge reads. Both are reads of what the machine already does: a frame from the roster, the session's combat log, the
// lines the send gate admitted.

// WhoRows is what an Owner may add to answer `.bot who` with its rows: the live adapter and the golden fake do. The
// run reads each bot's class and level from them, once, after FORM.
type WhoRows interface {
	Rows() []botcmd.WhoRow
}

// playMember is what `.bot who` said about one bot.
type playMember struct {
	class string
	level int
}

// playSlot is a bot's name in the trace: its role, never the character's name.
func playSlot(i int) string {
	switch i {
	case 0:
		return "tank"
	case 1:
		return "heal"
	case 2:
		return "dps1"
	}
	return "dps2"
}

// slot is the trace's name for a character of the run: a bot's slot (tank, heal, dps1, dps2), or "agent" for anything
// else (the player's own character). The record never carries a character's name.
func (p *playRun) slot(name string) string {
	for i, b := range p.cfg.Bots {
		if strings.EqualFold(b, name) {
			return playSlot(i)
		}
	}
	return "agent"
}

// slots is slot over a list.
func (p *playRun) slots(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, p.slot(n))
	}
	return out
}

// playLineCounts counts the lines the send gate admitted by their first words and how many were GM lines (a `.` line
// that is not a `.bot` line). A whisper word is counted as `whisper <word>`.
func playLineCounts(sent []string) (map[string]int, int) {
	by := map[string]int{}
	gm := 0
	for _, line := range sent {
		switch line {
		case botcmd.WhisperRelease, botcmd.WhisperCorpseRun:
			by["whisper "+line]++
			continue
		}
		words := strings.Fields(line)
		key := line
		if len(words) >= 2 {
			key = words[0] + " " + words[1]
		}
		if strings.HasPrefix(line, ".") && !strings.HasPrefix(line, ".bot ") {
			gm++
		}
		by[key]++
	}
	return by, gm
}

// learnParty reads `.bot who` once and keeps each bot's class and level for the trace's party records. A refusal or
// a reply with no rows only leaves the class unknown (the judge then says so); a lost link or a missing module ends
// the run. It reports whether the run may go on.
func (p *playRun) learnParty(ctx context.Context) bool {
	if err := p.sendBot(botcmd.VerbWho, ""); err != nil {
		p.stop("send")
		return false
	}
	_, err := p.ports.Owner.Who(ctx)
	if err != nil && !errors.Is(err, botcmd.ErrRefused) {
		if p.fatal(err) {
			return false
		}
		p.stop("form")
		return false
	}
	p.who = map[string]playMember{}
	if rows, ok := p.ports.Owner.(WhoRows); ok {
		for _, r := range rows.Rows() {
			p.who[strings.ToLower(r.Bot)] = playMember{class: r.Class, level: r.Level}
		}
	}
	return true
}

// sample writes one frame of the party and the hostile units (when the caller has them), and drains the session's
// combat log into the run's file. The trace's writer samples, so a call per poll is cheap; frames before the party is
// formed are skipped.
//
// A bot's combat flag is the run's fight or a living hostile in the view with that bot as its target: a pull's gather,
// a walk and a wait are not a fight of the run's, and a frame that said "not in combat" while the bots fought on it
// would leave a party left behind unreadable (#256).
func (p *playRun) sample(units []Unit) {
	if p.rec == nil || p.who == nil {
		return
	}
	attacked := map[wire.GUID]bool{}
	for _, u := range units {
		if u.Dead || u.Friendly || u.Target.IsZero() {
			continue
		}
		attacked[u.Target] = true
	}
	p.refreshSelf()
	var bots []PlayFrameBot
	for _, m := range p.roster() {
		for i, name := range p.cfg.Bots {
			if !strings.EqualFold(m.Name, name) {
				continue
			}
			w := p.who[strings.ToLower(name)]
			bots = append(bots, PlayFrameBot{Member: m, Name: playSlot(i), Role: traceRole(i), Class: w.class, Level: w.level, Combat: p.inFight || attacked[m.GUID]})
		}
	}
	self := PlaySelf{GUID: p.ports.Group.Leader().String(), Pos: p.self, Map: p.selfMap, Alive: p.ports.Player.Phase() == PhaseAlive}
	if p.rec.PlayFrame(p.now(), self, bots, units) {
		p.frames++
	}
	p.drainCombat()
}

// drainCombat appends the session's new combat-log rows to the run's file, a page at a time until caught up. A page
// the file refuses stays where it is (the cursor does not pass it), a hole the ring reports is counted, and the file's
// size bound ends the log for good: the summary says so.
func (p *playRun) drainCombat() {
	if p.ports.Combat == nil || p.rec == nil || p.combatFull {
		return
	}
	for {
		rows, next, _, dropped := p.ports.Combat.CombatRows(p.combatSeq, combatPageMax)
		if lost := p.combatSeq + dropped; lost > p.combatLostThrough {
			p.combatLost += lost - max(p.combatLostThrough, p.combatSeq)
			p.combatLostThrough = lost
		}
		if len(rows) > 0 {
			rows = p.redactRows(rows)
			n, err := p.rec.Combat(rows)
			if err != nil {
				if errors.Is(err, errCombatFull) {
					p.combatFull = true
				} else {
					// The page stays where it is and the next drain retries it; if no later drain gets it through, the
					// record says its log is incomplete.
					p.combatErr = combatErrText(err)
				}
				return
			}
			p.combatErr = ""
			p.combatRows += n
		}
		if next <= p.combatSeq {
			return
		}
		p.combatSeq = next
		if len(rows) < combatPageMax {
			return
		}
	}
}

// redactRows replaces each party member's name in the combat rows with its slot (tank, heal, dps1, dps2, agent), so the
// combat log names nobody either. The GUIDs stay: the judge joins the rows to the party records on them. A row is
// copied only when it carries a name.
func (p *playRun) redactRows(rows []json.RawMessage) []json.RawMessage {
	names := map[string]string{}
	for i, b := range p.cfg.Bots {
		names[b] = playSlot(i)
	}
	for _, m := range p.roster() {
		if p.slot(m.Name) == "agent" {
			names[m.Name] = "agent"
		}
	}
	out := make([]json.RawMessage, len(rows))
	for i, row := range rows {
		out[i] = row
		for name, slot := range names {
			if name == "" {
				continue
			}
			quoted := []byte(strconv.Quote(name))
			if bytes.Contains(out[i], quoted) {
				out[i] = bytes.ReplaceAll(out[i], quoted, []byte(strconv.Quote(slot)))
			}
		}
	}
	return out
}

// playRecord is the summary's play record at the run's end.
func (p *playRun) playRecord(end time.Time) *PlayRecord {
	by, gm := playLineCounts(p.sent)
	budget := p.cfg.MaxMinutes
	if budget == 0 {
		budget = p.route.MaxMinutes
	}
	return &PlayRecord{
		Minutes: end.Sub(p.start).Seconds() / 60, MaxMinutes: budget, MaxWipes: p.maxWipes,
		GMLines: gm, LinesByPrefix: by, Frames: p.frames, CombatRows: p.combatRows,
		CombatDropped: p.combatLost, CombatTruncated: p.combatFull, CombatError: p.combatErr,
	}
}
