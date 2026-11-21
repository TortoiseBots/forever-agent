package world

// The act runner: `forever act` drives one scriptable step per world action, with the same report lines for every
// later feature round. Each step is one line (`step N verb ok|fail reason`); a later round adds its own verbs here.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/follow"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	actVerbWait  = "wait"
	actVerbGM    = "gm"
	actVerbGoto  = "goto"
	actVerbNPC   = "npc"
	actVerbOpen  = "open"
	actVerbSelec = "select"
	actVerbClose = "close"
	actVerbInv   = "inv"

	// The W4 verbs (mount.go, follow_session.go): the mount, the dismount and the follow loop.
	actVerbMount    = "mount"
	actVerbDismount = "dismount"
	actVerbFollow   = "follow"

	// Round R2's item actions (inventory_act.go). `open` keeps its R0 meaning (the NPC menu) and opens an item when
	// the step names one.
	actVerbEquip   = "equip"
	actVerbStore   = "store"
	actVerbSwap    = "swap"
	actVerbSplit   = "split"
	actVerbDestroy = "destroy"
	actVerbUse     = "use"

	// Round R3's vendor steps (vendor_act.go).
	actVerbVendor   = "vendor"
	actVerbBuy      = "buy"
	actVerbSell     = "sell"
	actVerbSellJunk = "sell-junk"
	actVerbBuyback  = "buyback"
	actVerbRepair   = "repair"

	// Round R5's quest steps (quest_act.go).
	actVerbQuestStatus   = "quest-status"
	actVerbQuests        = "quests"
	actVerbQuestAccept   = "quest-accept"
	actVerbQuestProgress = "quest-progress"
	actVerbQuestComplete = "quest-complete"
	actVerbQuestReward   = "quest-reward"
	actVerbQuestAbandon  = "quest-abandon"
)

// Act bounds: how far a step walks to talk, and how long it waits for a menu.
const (
	// actInteractReach is the NPC interaction distance, INTERACTION_DISTANCE minus a step (ObjectDefines.h:24 is
	// 5.0; GetNPCIfCanInteractWith checks it, Player.cpp:1936-1990). The runner stops at three quarters of it so
	// a discrete step never ends outside the range.
	actInteractReach = 4.0
	// actMenuWait bounds one menu answer: the server answers a talk at once, so a silent server is a refusal, not
	// a longer wait.
	actMenuWait = 5 * time.Second
	// actSubmenuWait bounds the wait for a submenu after a select: most options open nothing, so it is short.
	actSubmenuWait = time.Second
)

// ActStep is one parsed `--step 'verb k=v'` line.
type ActStep struct {
	Verb string
	Args map[string]string
	Raw  string
}

// ParseActStep parses one `--step 'verb k=v'` line: the verb, then k=v pairs. A value is one word, or a double-quoted
// string (`note="a b c"`, with \" and \\ escapes) when it holds spaces; `line=` (the gm step) takes the whole rest of
// the step, so `gm line=.additem 6948 1` sends the command with its arguments (a rest that is one quoted string is
// unquoted). Durations (`wait dur=`) parse with time.ParseDuration; positions and entries parse as numbers at use.
func ParseActStep(line string) (ActStep, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return ActStep{}, fmt.Errorf("world: an act step needs a verb (wait, gm, goto, npc, open, select, close)")
	}
	verb, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimLeft(rest, " \t")
	step := ActStep{Verb: verb, Args: map[string]string{}, Raw: line}
	for rest != "" {
		k, after, ok := strings.Cut(rest, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t\"") {
			word, _, _ := strings.Cut(rest, " ")
			return ActStep{}, fmt.Errorf("world: step %q: %q is not k=v", line, word)
		}
		if _, dup := step.Args[k]; dup {
			return ActStep{}, fmt.Errorf("world: step %q: duplicate key %q", line, k)
		}
		var v string
		switch {
		case k == "line":
			v = strings.TrimSpace(after)
			if u, err := strconv.Unquote(v); err == nil && strings.HasPrefix(v, `"`) {
				v = u
			}
			rest = ""
		case strings.HasPrefix(after, `"`):
			val, tail, err := scanQuoted(after)
			if err != nil {
				return ActStep{}, fmt.Errorf("world: step %q: %s=: %w", line, k, err)
			}
			v, rest = val, strings.TrimLeft(tail, " \t")
		default:
			word, tail, _ := strings.Cut(after, " ")
			v, rest = word, strings.TrimLeft(tail, " \t")
		}
		step.Args[k] = v
	}
	switch verb {
	case actVerbWait, actVerbGM, actVerbGoto, actVerbNPC, actVerbOpen, actVerbSelec, actVerbClose, actVerbInv,
		actVerbMount, actVerbDismount, actVerbFollow,
		actVerbEquip, actVerbStore, actVerbSwap, actVerbSplit, actVerbDestroy, actVerbUse,
		actVerbVendor, actVerbBuy, actVerbSell, actVerbSellJunk, actVerbBuyback, actVerbRepair,
		actVerbQuestStatus, actVerbQuests, actVerbQuestAccept, actVerbQuestProgress, actVerbQuestComplete,
		actVerbQuestReward, actVerbQuestAbandon:
	default:
		return ActStep{}, fmt.Errorf("world: unknown act verb %q (wait, gm, goto, npc, open, select, close, inv, mount, dismount, follow, equip, store, swap, split, destroy, use, vendor, buy, sell, sell-junk, buyback, repair, quest-status, quests, quest-accept, quest-progress, quest-complete, quest-reward, quest-abandon)", verb)
	}
	return step, nil
}

// scanQuoted reads one double-quoted value from the start of s (backslash escapes a quote or a backslash) and
// returns it with the text after the closing quote, which must be empty or start with a space.
func scanQuoted(s string) (value, tail string, err error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			i++
			if i >= len(s) {
				return "", "", fmt.Errorf("unterminated quote")
			}
			b.WriteByte(s[i])
		case '"':
			tail = s[i+1:]
			if tail != "" && tail[0] != ' ' && tail[0] != '\t' {
				return "", "", fmt.Errorf("text after the closing quote")
			}
			return b.String(), tail, nil
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("unterminated quote")
}

// requireKeys refuses a step whose args miss a required key.
func (s ActStep) requireKeys(keys ...string) error {
	for _, k := range keys {
		if _, ok := s.Args[k]; !ok {
			return fmt.Errorf("world: step %q needs %s=", s.Raw, k)
		}
	}
	return nil
}

// actState is the runner's own state: the current NPC and its last menu.
type actState struct {
	npc      *objects.Object
	menu     npc.GossipMessage
	hasMenu  bool
	menuNPC  bool // the menu came from TALK_TO_GOSSIP (false: a QUEST_GIVER_HELLO answers a quest list)
	quests   *quest.QuestList
	npcEntry int32
	hasNPC   bool
	steps    int
	// vendor is the list the last `vendor` step read, and hasVendor that a vendor is open: the purchase and sale
	// steps need it, so they cannot run against a vendor nobody opened.
	vendor    *items.VendorInventory
	hasVendor bool
	// lines are the step and menu lines this run printed, in order, bounded by actLinesMax (lines past it are
	// counted in dropped). Only the run's own lines land here: the log sink also carries the chat, party and world
	// handler lines of everything else the session does, and those never belong in an act answer.
	lines   []string
	dropped int
}

// The bounds of one act answer: how many lines it keeps and how long one line may be (a menu option's text is the
// server's).
const (
	actLinesMax   = 500
	actLineMaxLen = 512
)

// actLine prints one of the run's own lines: it goes to the caller's log sink as before and, bounded, into the
// answer.
func (s *session) actLine(a *actState, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	s.opts.logf("%s", line)
	if len(a.lines) >= actLinesMax {
		a.dropped++
		return
	}
	if len(line) > actLineMaxLen {
		line = line[:actLineMaxLen]
	}
	a.lines = append(a.lines, line)
}

// actStep runs one step of the act script and prints its line.
func (s *session) actStep(ctx context.Context, a *actState, step ActStep) error {
	a.steps++
	switch step.Verb {
	case actVerbWait:
		return s.actWait(ctx, a, step)
	case actVerbGM:
		return s.actGM(ctx, a, step)
	case actVerbGoto:
		return s.actGoto(ctx, a, step)
	case actVerbNPC:
		return s.actNPC(ctx, a, step)
	case actVerbOpen:
		return s.actOpenStep(ctx, a, step)
	case actVerbSelec:
		return s.actSelect(ctx, a, step)
	case actVerbClose:
		return s.actClose(ctx, a, step)
	case actVerbMount:
		return s.actMount(ctx, a, step)
	case actVerbDismount:
		return s.actDismount(ctx, a, step)
	case actVerbFollow:
		return s.actFollow(ctx, a, step)
	case actVerbInv:
		return s.actInvStep(ctx, a, step)
	case actVerbEquip:
		return s.actEquip(ctx, a, step)
	case actVerbStore:
		return s.actStore(ctx, a, step)
	case actVerbSwap:
		return s.actSwap(ctx, a, step)
	case actVerbSplit:
		return s.actSplit(ctx, a, step)
	case actVerbDestroy:
		return s.actDestroy(ctx, a, step)
	case actVerbUse:
		return s.actUse(ctx, a, step)
	case actVerbVendor:
		return s.actVendor(ctx, a, step)
	case actVerbBuy:
		return s.actBuy(ctx, a, step)
	case actVerbSell:
		return s.actSell(ctx, a, step)
	case actVerbSellJunk:
		return s.actSellJunk(ctx, a, step)
	case actVerbBuyback:
		return s.actBuyback(ctx, a, step)
	case actVerbRepair:
		return s.actRepair(ctx, a, step)
	case actVerbQuestStatus:
		return s.actQuestStatus(ctx, a, step)
	case actVerbQuests:
		return s.actQuests(ctx, a, step)
	case actVerbQuestAccept:
		return s.actQuestAccept(ctx, a, step)
	case actVerbQuestProgress:
		return s.actQuestProgress(ctx, a, step)
	case actVerbQuestComplete:
		return s.actQuestComplete(ctx, a, step)
	case actVerbQuestReward:
		return s.actQuestReward(ctx, a, step)
	case actVerbQuestAbandon:
		return s.actQuestAbandon(ctx, a, step)
	default:
		return s.actFail(a, step, fmt.Errorf("unknown verb %q", step.Verb))
	}
}

// actFail prints the step's failure line and returns the error.
func (s *session) actFail(a *actState, step ActStep, err error) error {
	s.actLine(a, "step %d %s fail %v", a.steps, step.Verb, err)
	return err
}

// actOK prints the step's success line.
func (s *session) actOK(a *actState, step ActStep, detail string) {
	if detail != "" {
		s.actLine(a, "step %d %s ok %s", a.steps, step.Verb, detail)
	} else {
		s.actLine(a, "step %d %s ok", a.steps, step.Verb)
	}
}

// actWait answers the server for dur= (default one second).
func (s *session) actWait(ctx context.Context, a *actState, step ActStep) error {
	dur := time.Second
	if raw, ok := step.Args["dur"]; ok {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 || d > 10*time.Minute {
			return s.actFail(a, step, fmt.Errorf("dur=%q is not a duration from 0 to 10m", raw))
		}
		dur = d
	}
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); dur > left {
			return s.actFail(a, step, fmt.Errorf("dur=%s is longer than the %s left of the run deadline", dur, left.Round(time.Second)))
		}
	}
	if err := s.collect(ctx, dur); err != nil {
		return s.actFail(a, step, err)
	}
	s.actOK(a, step, fmt.Sprintf("waited %s", dur))
	return nil
}

// actGM sends one command line through the existing command channel and its GM guard. Only a clear success passes:
// a refused, unknown-command, syntax, no-reply or incomplete result fails the step (the command session itself logs
// those and carries on, which is right for cmd-check but not for a script whose next step relies on this one).
func (s *session) actGM(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("line"); err != nil {
		return s.actFail(a, step, err)
	}
	line := step.Args["line"]
	if line == "" {
		return s.actFail(a, step, fmt.Errorf("gm needs line=TEXT"))
	}
	// A serving daemon holds every GM line to the boundary the raw gm method uses: a line that acts on a unit is
	// refused unless the daemon allows it, and nothing (not even the GM probe) is sent for a refused line. The
	// one-shot `forever act` has no daemon and keeps the command guard alone.
	if s.serveRT != nil {
		if err := s.serveGMUnitLine("act", line); err != nil {
			return s.actFail(a, step, err)
		}
	}
	s.initChat()
	q := command.NewSession(&cmdLink{s: s})
	if err := s.confirmGM(ctx); err != nil {
		return s.actFail(a, step, err)
	}
	res, err := s.runOneResult(ctx, q, line)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if err := gmOutcome(res.Rep, res.RunErr); err != nil {
		return s.actFail(a, step, fmt.Errorf("%q: %w", line, err))
	}
	s.actOK(a, step, line)
	return nil
}

// gmOutcome decides whether one command's result is a clear success: the run raised no error and no named refusal,
// the reply ended the way its mode requires, and it was not echoed back as ordinary say chat.
func gmOutcome(rep *command.Reply, runErr error) error {
	switch {
	case runErr != nil:
		return runErr
	case rep == nil:
		return command.ErrNoReply
	case rep.Refusal != nil:
		return rep.Refusal
	case rep.Echoed:
		return command.ErrNoPermission
	case !rep.Complete:
		return command.ErrIncomplete
	}
	return nil
}

// actGoto walks to x= y= z= on the navmesh when one is configured, in a straight line otherwise.
func (s *session) actGoto(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("x", "y", "z"); err != nil {
		return s.actFail(a, step, err)
	}
	x, err := parseActFloat(step.Args["x"])
	if err != nil {
		return s.actFail(a, step, fmt.Errorf("x=%q is not a number", step.Args["x"]))
	}
	y, err := parseActFloat(step.Args["y"])
	if err != nil {
		return s.actFail(a, step, fmt.Errorf("y=%q is not a number", step.Args["y"]))
	}
	z, err := parseActFloat(step.Args["z"])
	if err != nil {
		return s.actFail(a, step, fmt.Errorf("z=%q is not a number", step.Args["z"]))
	}
	if s.mover == nil {
		return s.actFail(a, step, ErrNotReady)
	}
	if err := s.requireWalkMode(step); err != nil {
		return s.actFail(a, step, err)
	}
	if err := s.walkToPoint(ctx, float32(x), float32(y), float32(z), "act goto"); err != nil {
		return s.actFail(a, step, err)
	}
	s.actOK(a, step, fmt.Sprintf("at %.1f %.1f %.1f", x, y, z))
	return nil
}

// requireWalkMode refuses a walk without --navmesh unless the step says straight=1: a straight line at the given
// height is a walk at an invented height, which the repo's notes rule out, so it is an explicit choice.
func (s *session) requireWalkMode(step ActStep) error {
	if s.opts.NavmeshDir != "" || step.Args["straight"] == "1" {
		return nil
	}
	return fmt.Errorf("no --navmesh: a walk needs straight=1 (a straight line at an invented height) or a navmesh")
}

// parseActFloat parses one step coordinate.
func parseActFloat(raw string) (float64, error) {
	var v float64
	_, err := fmt.Sscanf(raw, "%g", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// actNPC finds the nearest unit with the given entry, walks within interaction range and stores it as the current
// NPC.
func (s *session) actNPC(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("entry"); err != nil {
		return s.actFail(a, step, err)
	}
	var entry int32
	if _, err := fmt.Sscanf(step.Args["entry"], "%d", &entry); err != nil || entry <= 0 {
		return s.actFail(a, step, fmt.Errorf("entry=%q is not a creature entry", step.Args["entry"]))
	}
	if s.mover == nil {
		return s.actFail(a, step, ErrNotReady)
	}
	target := s.nearestEntry(entry)
	if target == nil {
		return s.actFail(a, step, fmt.Errorf("no unit of entry %d in the world model", entry))
	}
	now := s.transport().Now()
	tx, ty, tz := target.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz); dist > actInteractReach {
		if err := s.requireWalkMode(step); err != nil {
			return s.actFail(a, step, err)
		}
		px, py, pz := approachPoint(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz, actInteractReach*0.75, 1)
		if err := s.walkToPoint(ctx, px, py, pz, fmt.Sprintf("entry %d", entry)); err != nil {
			return s.actFail(a, step, err)
		}
		now = s.transport().Now()
		tx, ty, tz = target.Position(now)
		dist = objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
		if dist > actInteractReach {
			return s.actFail(a, step, fmt.Errorf("entry %d is %.1f yd away after the walk", entry, dist))
		}
	}
	a.npc = target
	a.npcEntry = entry
	a.hasNPC = true
	a.hasMenu = false
	// A vendor list belongs to the vendor it was read from: a new current NPC starts without one.
	a.vendor, a.hasVendor = nil, false
	s.actMenu, s.actQuest = nil, nil
	s.actOK(a, step, fmt.Sprintf("entry %d within reach", entry))
	return nil
}

// nearestEntry returns the nearest unit of the given entry in the world model.
func (s *session) nearestEntry(entry int32) *objects.Object {
	mx, my, mz := s.MoverX(), s.MoverY(), s.MoverZ()
	var best *objects.Object
	bestDist := float32(0)
	for _, u := range s.world.Units(mx, my, mz) {
		if !u.Info.HasUnit || u.Info.Entry != entry {
			continue
		}
		x, y, z := u.Position(s.transport().Now())
		if d := objects.Distance(mx, my, mz, x, y, z); best == nil || d < bestDist {
			best, bestDist = u, d
		}
	}
	return best
}

// npcFlags returns what opening packet an NPC needs: quest hello when it has the quest-giver flag but no gossip
// flag, talk otherwise.
func npcNeedsQuestHello(flags uint32) bool {
	return flags&npc.NPCFlagQuestGiver != 0 && flags&npc.NPCFlagGossip == 0
}

// actOpenStep routes `open`: a step that names an item opens the item, one that names nothing opens the current
// NPC's menu.
func (s *session) actOpenStep(ctx context.Context, a *actState, step ActStep) error {
	if _, ok := step.Args["entry"]; ok {
		return s.actOpenItem(ctx, a, step)
	}
	if _, ok := step.Args["slot"]; ok {
		return s.actOpenItem(ctx, a, step)
	}
	return s.actOpen(ctx, a, step)
}

// actOpen sends TALK_TO_GOSSIP when NpcFlags has GOSSIP 0x1, otherwise QUEST_GIVER_HELLO, and prints the menu. The
// current NPC comes from the last `npc` step; the packet is always select-then-talk, like the spirit healer flow.
func (s *session) actOpen(ctx context.Context, a *actState, step ActStep) error {
	if !a.hasNPC || a.npc == nil {
		return s.actFail(a, step, fmt.Errorf("no current NPC (an npc step comes first)"))
	}
	now := s.transport().Now()
	nx, ny, nz := a.npc.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return s.actFail(a, step, fmt.Errorf("the NPC is %.1f yd away (walk closer first)", dist))
	}
	guid := a.npc.GUID
	if err := s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(guid)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_SET_SELECTION: %w", err))
	}
	a.hasMenu, a.quests = false, nil
	// The stored answer follows this open: a menu left from an earlier exchange must not satisfy the wait.
	s.actMenu, s.actQuest = nil, nil
	useQuestHello := npcNeedsQuestHello(a.npc.Info.NpcFlags)
	if useQuestHello {
		if err := s.transport().Send(wire.CMSGQuestGiverHello, npc.BuildQuestGiverHello(guid)); err != nil {
			return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_HELLO: %w", err))
		}
	} else {
		if err := s.transport().Send(wire.CMSGTalkToGossip, npc.BuildTalkToGossip(guid)); err != nil {
			return s.actFail(a, step, fmt.Errorf("CMSG_TALK_TO_GOSSIP: %w", err))
		}
	}
	menu, quests, found, err := s.waitActAnswer(ctx, guid, actMenuWait, useQuestHello)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !found {
		if useQuestHello {
			return s.actFail(a, step, fmt.Errorf("no quest answer within %s", actMenuWait))
		}
		return s.actFail(a, step, fmt.Errorf("no gossip menu within %s", actMenuWait))
	}
	a.menuNPC = !useQuestHello
	if quests != nil {
		// A quest-only NPC answers the hello with its quest list; there is no gossip menu to choose from.
		a.menu = npc.GossipMessage{Unit: guid}
		a.hasMenu = true
		a.quests = quests
		s.actOK(a, ActStep{Verb: actVerbOpen}, fmt.Sprintf("quest list with %d quest(s)", len(quests.Quests)))
		return nil
	}
	a.menu = *menu
	a.hasMenu = true
	s.printActMenu(a, *menu)
	return nil
}

// waitActAnswer waits up to wait for the answer of the NPC guid: its gossip menu, or (questList) its quest list. The
// answers land on the session (handleNPC stores them); only one from this NPC counts. found is false when none came.
func (s *session) waitActAnswer(ctx context.Context, guid wire.GUID, wait time.Duration, questList bool) (*npc.GossipMessage, *quest.QuestList, bool, error) {
	end := s.transport().Now().Add(wait)
	for {
		if m := s.actMenu; m != nil && m.Unit == guid {
			s.actMenu = nil
			return m, nil, true, nil
		}
		if q := s.actQuest; questList && q != nil && q.Giver == guid {
			s.actQuest = nil
			return nil, q, true, nil
		}
		if err := s.linkDead(); err != nil {
			return nil, nil, false, err
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return nil, nil, false, nil
		}
		step := 100 * time.Millisecond
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return nil, nil, false, err
		}
	}
}

// printActMenu prints the menu one option per line, then the step's ok line.
func (s *session) printActMenu(a *actState, m npc.GossipMessage) {
	for _, o := range m.Options {
		s.actLine(a, "menu option %d npc %d %q", o.ID, o.NPC, o.Text)
	}
	s.actOK(a, ActStep{Verb: actVerbOpen}, fmt.Sprintf("menu %d with %d option(s)", m.MenuID, len(m.Options)))
}

// actSelect sends CMSG_GOSSIP_SELECT_OPTION for option= on the current NPC's menu, then adopts a submenu the NPC
// answers with, so the next select checks the options of the menu now open. With no submenu the menu is spent.
func (s *session) actSelect(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("option"); err != nil {
		return s.actFail(a, step, err)
	}
	if !a.hasMenu || a.npc == nil {
		return s.actFail(a, step, fmt.Errorf("no open menu (an open step comes first)"))
	}
	var option int32
	if _, err := fmt.Sscanf(step.Args["option"], "%d", &option); err != nil {
		return s.actFail(a, step, fmt.Errorf("option=%q is not a number", step.Args["option"]))
	}
	found := false
	for _, o := range a.menu.Options {
		if o.ID == option {
			found = true
			break
		}
	}
	if !found {
		return s.actFail(a, step, fmt.Errorf("the menu has no option %d", option))
	}
	s.actMenu, s.actQuest = nil, nil
	if err := s.transport().Send(wire.CMSGGossipSelectOption, npc.BuildGossipSelectOption(a.npc.GUID, a.menu.MenuID, option, "")); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_GOSSIP_SELECT_OPTION: %w", err))
	}
	a.hasMenu, a.quests = false, nil
	sub, _, got, err := s.waitActAnswer(ctx, a.npc.GUID, actSubmenuWait, false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if got {
		a.menu, a.hasMenu = *sub, true
		s.actOK(a, step, fmt.Sprintf("option %d, submenu %d with %d option(s)", option, sub.MenuID, len(sub.Options)))
		return nil
	}
	s.actOK(a, step, fmt.Sprintf("option %d", option))
	return nil
}

// actClose sends CMSG_CLOSE_INTERACTION for the current NPC and forgets the menu.
func (s *session) actClose(ctx context.Context, a *actState, step ActStep) error {
	_ = ctx
	if !a.hasNPC || a.npc == nil {
		return s.actFail(a, step, fmt.Errorf("no current NPC (an npc step comes first)"))
	}
	if err := s.transport().Send(wire.CMSGCloseInteraction, npc.BuildCloseInteraction(a.npc.GUID)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_CLOSE_INTERACTION: %w", err))
	}
	a.hasMenu, a.quests = false, nil
	s.actMenu, s.actQuest = nil, nil
	s.actOK(a, step, "closed")
	return nil
}

// actMount mounts the character: `mount spell=N` runs the method's cast loop, and `mount entry=N` answers the same
// needs-item-use refusal the method does.
func (s *session) actMount(ctx context.Context, a *actState, step ActStep) error {
	spellText, haveSpell := step.Args["spell"]
	entryText, haveEntry := step.Args["entry"]
	if haveSpell == haveEntry {
		return s.actFail(a, step, fmt.Errorf("mount needs exactly one of spell= or entry="))
	}
	var p serveMountParams
	if haveSpell {
		var spell int32
		if _, err := fmt.Sscanf(spellText, "%d", &spell); err != nil || spell <= 0 {
			return s.actFail(a, step, fmt.Errorf("spell=%q is not a spell id", spellText))
		}
		p.Spell = spell
	} else {
		var entry int32
		if _, err := fmt.Sscanf(entryText, "%d", &entry); err != nil || entry <= 0 {
			return s.actFail(a, step, fmt.Errorf("entry=%q is not an item entry", entryText))
		}
		p.Entry = entry
	}
	out, err := s.serveMount(ctx, p)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if p.Entry > 0 {
		s.actOK(a, step, fmt.Sprintf("entry %d, display %d", p.Entry, out.DisplayID))
		return nil
	}
	s.actOK(a, step, fmt.Sprintf("spell %d, display %d, %d cast(s)", p.Spell, out.DisplayID, out.Attempts))
	return nil
}

// actDismount cancels the mount aura and waits for the display field to reach zero.
func (s *session) actDismount(ctx context.Context, a *actState, step ActStep) error {
	out, err := s.serveDismount(ctx)
	if err != nil {
		return s.actFail(a, step, err)
	}
	detail := "on foot"
	if out.Mounted {
		detail = fmt.Sprintf("mounted, display %d", out.DisplayID)
	}
	s.actOK(a, step, detail)
	return nil
}

// actFollow runs one follow loop: `follow lead=NAME [near= far= lost_s= tick_ms= dur=]`. It returns when the leader
// is lost or the step's own dur= (or the run's deadline) ends; a lost or failed loop fails the step. The leader is
// followed with movement only: no chat, no GM line.
func (s *session) actFollow(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("lead"); err != nil {
		return s.actFail(a, step, err)
	}
	if follow.SameName(step.Args["lead"], s.report.Character.Name) {
		return s.actFail(a, step, fmt.Errorf("lead=%s is this character: a character cannot follow itself", step.Args["lead"]))
	}
	if s.opts.NavmeshDir == "" {
		return s.actFail(a, step, errors.New("no --navmesh: following walks on the navmesh"))
	}
	cfg := follow.Config{Lead: step.Args["lead"], Self: s.report.Character.Name}
	if raw, ok := step.Args["near"]; ok {
		v, err := parseActFloat(raw)
		if err != nil || v <= 0 {
			return s.actFail(a, step, fmt.Errorf("near=%q is not a distance", raw))
		}
		cfg.Near = v
	}
	if raw, ok := step.Args["far"]; ok {
		v, err := parseActFloat(raw)
		if err != nil || v <= 0 {
			return s.actFail(a, step, fmt.Errorf("far=%q is not a distance", raw))
		}
		cfg.Far = v
	}
	if raw, ok := step.Args["lost_s"]; ok {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < 1 || n > serveFollowLostMax {
			return s.actFail(a, step, fmt.Errorf("lost_s=%q is not 1 to %d", raw, serveFollowLostMax))
		}
		cfg.Lost = time.Duration(n) * time.Second
	}
	if raw, ok := step.Args["tick_ms"]; ok {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < serveFollowTickMin || n > serveFollowTickMax {
			return s.actFail(a, step, fmt.Errorf("tick_ms=%q is not %d to %d", raw, serveFollowTickMin, serveFollowTickMax))
		}
		cfg.Tick = time.Duration(n) * time.Millisecond
	}
	// A follow in a script is bounded: the run holds the session goroutine, so every other call waits behind it.
	raw, ok := step.Args["dur"]
	if !ok {
		return s.actFail(a, step, errors.New("follow needs dur= (a script's follow is bounded: the run holds the session)"))
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 || d > 10*time.Minute {
		return s.actFail(a, step, fmt.Errorf("dur=%q is not a duration above 0 up to 10m", raw))
	}
	actx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	res := follow.Run(actx, followDirectGame{s: s, ctx: actx}, cfg)
	if ctx.Err() != nil {
		// The run's own deadline (or the caller) ended the step, not its dur=: that is the run's budget running out.
		return s.actFail(a, step, ctx.Err())
	}
	if res.Reason == follow.ReasonStopped {
		s.actOK(a, step, fmt.Sprintf("%s, %d walk(s), %d stop(s)", res.Reason, res.Walks, res.Stops))
		return nil
	}
	return s.actFail(a, step, fmt.Errorf("%s: %s", res.Reason, res.Detail))
}

// act runs the whole script: settle, then one line per step. A failed step ends the run with its error; the
// caller still logs out.
func (s *session) act(ctx context.Context, steps []ActStep) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 3 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	_, err := s.RunActSteps(ctx, steps)
	return err
}

// RunActSteps runs a parsed script on the caller's goroutine (which is the session goroutine for serve) and returns
// the lines `forever act` prints: the run's own step lines and menu lines, in order, bounded by actLinesMax. It
// never returns another handler's line (chat, party, world) or a third party's text. A failed step prints its fail
// line and ends the run with its error; the caller still logs out. The caller's own log sink keeps receiving the
// lines.
func (s *session) RunActSteps(ctx context.Context, steps []ActStep) (lines []string, err error) {
	a := &actState{}
	defer func() {
		lines = a.lines
		if a.dropped > 0 {
			lines = append(lines, fmt.Sprintf("act: %d more line(s) were not kept (the answer keeps %d)", a.dropped, actLinesMax))
		}
	}()
	for _, step := range steps {
		if err := s.actStep(ctx, a, step); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
