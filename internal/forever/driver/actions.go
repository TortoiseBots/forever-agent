package driver

// The typed setup actions. Everything the driver does to the party outside the orders — bring it into the instance,
// teleport it, raise it, leave or reset the instance, and the commander's own GM lines — is one Action value with a
// kind from this closed vocabulary. There is no call that takes a command line, so no state can send a verb the
// driver did not classify.
//
// act is the one way into this vocabulary, as send is the one for the orders: it classifies the action and applies
// the gate the class needs, and then sends through sendLine, the one function every non-read-only line leaves the run
// through. The gates:
//
//   - moving (a teleport, the entry, the leave: anything that crosses ground or changes map) needs a complete state
//     report taken immediately before the send and newer than the last order, no stop pending, and nobody in combat
//     or dead — unless the action is the wipe's recovery, which exists to raise and move a party that was dead;
//   - state-changing in place (a revive, the GM lines) goes out after the same stop check; a revive is allowed only
//     under the run's practice budget and is counted as PRACTICE, and an instance reset only when the run has
//     reached its end and a fresh report shows the whole party outside the instance (the module refuses otherwise,
//     so the driver must not ask);
//   - read-only (the instance report) needs no gate.
//
// The port's answer is typed: only a parsed success reaches the driver. A module refusal comes back as its named
// error and the action is recorded failed; the run never assumes a refused teleport moved anyone.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// ActionKind is one setup action of the driver's vocabulary. The zero value is not an action and is refused.
type ActionKind int

const (
	// ActionEnter brings the party into the instance (the module's enter).
	ActionEnter ActionKind = iota + 1
	// ActionTeleCommander teleports one character, or the commander, to a map position.
	ActionTeleCommander
	// ActionTeleParty teleports the whole party to a map position.
	ActionTeleParty
	// ActionTeleTo teleports one character to another character.
	ActionTeleTo
	// ActionTeleOutside is the commander's own GM teleport to the plan's named outside place.
	ActionTeleOutside
	// ActionAppear brings the commander to a bot's side (the GM's appear).
	ActionAppear
	// ActionRevive raises one character or the party.
	ActionRevive
	// ActionInstanceLeave takes the party out of the instance.
	ActionInstanceLeave
	// ActionInstanceReset resets the instance. Only after the run's end, and only from outside.
	ActionInstanceReset
	// ActionInstanceInfo reads the instance's boss state.
	ActionInstanceInfo
	// ActionGMOn and ActionGMVisible switch the commander's GM mode on.
	ActionGMOn
	ActionGMVisible
	// ActionRunStart starts the module's own run of the plan for the leader's party. It is fight-starting: from
	// then on the module pulls, so it needs the fight gate.
	ActionRunStart
	// ActionRunStop and ActionRunPause are the stop path's own run commands: passive, and allowed while a stop is
	// pending (they are how a watcher ends a run it does not lead).
	ActionRunStop
	ActionRunPause
	// ActionRunResume restarts a paused run; like the start it is fight-starting.
	ActionRunResume
)

// Action is one typed setup action. The fields a kind does not use are ignored; the port checks them like any other
// argument and refuses an action it cannot perform.
type Action struct {
	Kind ActionKind
	// Who is the target: a character name (a teleport of one bot, a revive, a bot the commander appears at), "party"
	// for the entry and a party revive, or the instance's leader for the instance actions.
	Who string
	// Other is ActionTeleTo's destination character.
	Other string
	// Map and Pos are ActionTeleCommander's and ActionTeleParty's destination.
	Map uint32
	Pos Vec3
	// Dest is ActionTeleOutside's named place, from the plan's outside_tele.
	Dest string
	// Recovery marks the wipe's recovery teleports: the one moving action that does not wait for a party with nobody
	// dead, because raising and gathering the party is what it does. It still needs a fresh report.
	Recovery bool
	// Practice marks one practice revive of the run's budget: the budget gates it and it is counted as PRACTICE.
	Practice bool
	// Plan is ActionRunStart's plan name: the loaded plan's own name.
	Plan string
	// Reason is ActionRunStop's and ActionRunPause's reason, in words the module records with the run.
	Reason string
}

// actionClass says what one action would do to the party and so which gate it needs at the send.
type actionClass int

const (
	// actionRead changes nothing: a report read through the vocabulary.
	actionRead actionClass = iota
	// actionMove crosses ground or changes map. Needs the moving gate.
	actionMove
	// actionState changes the party's state in place (a revive, a GM mode, an instance reset).
	actionState
	// actionFight starts a fight: the module's run, which pulls from now on, and its resume. Needs the fight gate.
	actionFight
	// actionPassive changes nothing on the map and may go out while a stop is pending: the stop path's own run
	// commands, which is how a watcher ends a run it does not lead.
	actionPassive
)

// actionClasses is the classification of every action the vocabulary holds. No setup action starts a fight; the
// orders own that, and their own gate is the pull permit.
var actionClasses = map[ActionKind]actionClass{
	ActionEnter:         actionMove,
	ActionTeleCommander: actionMove,
	ActionTeleParty:     actionMove,
	ActionTeleTo:        actionMove,
	ActionTeleOutside:   actionMove,
	ActionAppear:        actionMove,
	ActionInstanceLeave: actionMove,
	ActionRevive:        actionState,
	ActionInstanceReset: actionState,
	ActionGMOn:          actionState,
	ActionGMVisible:     actionState,
	ActionInstanceInfo:  actionRead,
	ActionRunStart:      actionFight,
	ActionRunStop:       actionPassive,
	ActionRunPause:      actionPassive,
	ActionRunResume:     actionFight,
}

// classifyAction returns an action's class; ok false is a kind the driver refuses to send.
func classifyAction(k ActionKind) (actionClass, bool) {
	c, ok := actionClasses[k]
	return c, ok
}

// ActionReport is the module's typed answer to one action: it was parsed whole, so the driver may act on it.
type ActionReport struct {
	// Verb is the command word the module answered: tele, enter, revive, instance, or the core line's own word.
	Verb string
	// Count is the end line's count (0 for every setup command today).
	Count int
	// Confirmed is false for a core command that answers nothing (the GM teleport and appear are silent): the line
	// was sent without a refusal, and the world (position, map) is what confirms its effect.
	Confirmed bool
	// Info is ActionInstanceInfo's answer.
	Info *tbcmd.InstanceInfoReport
	// Run is the run record ActionRunStart, ActionRunStop, ActionRunPause and ActionRunResume answer with.
	Run *tbcmd.RunState
	// Targets are the `target=` labels a setup answer named: the party (its leader or the commander it was bound
	// to) a party command resolved to, or the one character. Empty for an answer that only acknowledges.
	Targets []tbcmd.ActionTarget
	// Code is the module's refusal code when the action was refused ("refused" for a bad reply or a timeout).
	Code string
}

// String is the action in the words of the run's order log: one stable, short form per kind.
func (a Action) String() string {
	switch a.Kind {
	case ActionEnter:
		return "enter party"
	case ActionTeleCommander:
		return fmt.Sprintf("tele %s %d %s", a.Who, a.Map, fmtVec(a.Pos))
	case ActionTeleParty:
		return fmt.Sprintf("tele party %d %s", a.Map, fmtVec(a.Pos))
	case ActionTeleTo:
		return "tele " + a.Who + " to " + a.Other
	case ActionTeleOutside:
		return "tele-outside " + a.Dest
	case ActionAppear:
		return "appear " + a.Who
	case ActionRevive:
		return "revive " + a.Who
	case ActionInstanceLeave:
		return "instance " + a.Who + " leave"
	case ActionInstanceReset:
		return "instance " + a.Who + " reset"
	case ActionInstanceInfo:
		return "instance " + a.Who + " info"
	case ActionGMOn:
		return "gm on"
	case ActionGMVisible:
		return "gm visible on"
	case ActionRunStart:
		return "run start " + a.Who + " " + a.Plan
	case ActionRunStop:
		if a.Reason != "" {
			return "run stop " + a.Who + " " + a.Reason
		}
		return "run stop " + a.Who
	case ActionRunPause:
		if a.Reason != "" {
			return "run pause " + a.Who + " " + a.Reason
		}
		return "run pause " + a.Who
	case ActionRunResume:
		return "run resume " + a.Who
	}
	return "action"
}

// who is the log's actor for the order record.
func (a Action) who() string {
	switch a.Kind {
	case ActionEnter, ActionTeleParty:
		return "party"
	case ActionTeleOutside, ActionAppear, ActionGMOn, ActionGMVisible:
		return "commander"
	}
	if a.Who == "" {
		return "commander"
	}
	return a.Who
}

// isRunAction reports whether the action is one of the module's run commands.
func (a Action) isRunAction() bool {
	switch a.Kind {
	case ActionRunStart, ActionRunStop, ActionRunPause, ActionRunResume:
		return true
	}
	return false
}

// validDest reports whether a plan's teleport destination is one place name the core's tele command takes: one word
// of letters, digits and ., _ : -. The plan loader does not check it; this is the gate that does.
func validDest(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == ':' || r == '-':
		default:
			return false
		}
	}
	return true
}

// act is the one way into the setup vocabulary, as send is the one for the orders. It classifies the action, applies
// the gate of its class, and sends through sendLine — the one function every non-read-only line leaves the run
// through, orders and actions alike. It returns the parsed answer and why the action was not sent or not performed;
// a lost link ends the run here.
func (r *Run) act(ctx context.Context, a Action) (ActionReport, string) {
	class, ok := classifyAction(a.Kind)
	if !ok {
		r.event("action_refused", map[string]any{"action": a.String(), "why": "kind"})
		return ActionReport{}, "kind"
	}
	if a.Kind == ActionTeleOutside && !validDest(a.Dest) {
		r.event("action_refused", map[string]any{"action": a.String(), "why": "dest"})
		return ActionReport{}, "dest"
	}
	if a.Kind == ActionRunStart && a.Plan == "" {
		r.event("action_refused", map[string]any{"action": a.String(), "why": "plan"})
		return ActionReport{}, "plan"
	}
	if r.lost {
		return ActionReport{}, "link"
	}
	// A pending stop refuses every action except the stop path's own run commands: sending a pause or a stop after
	// the run ended is the watcher's ordinary end, and nothing else may be sent once the run is stopping.
	if r.stopReason != "" && class != actionPassive && class != actionRead {
		return ActionReport{}, "stop"
	}
	var why string
	switch class {
	case actionMove:
		why = r.actionMoveGate(ctx, a)
	case actionState:
		why = r.actionStateGate(ctx, a)
	case actionFight:
		why = r.actionFightGate(ctx, a)
	}
	if why != "" {
		r.event("action_refused", map[string]any{"action": a.String(), "why": why})
		return ActionReport{}, why
	}
	out, why := r.sendLine(ctx, a.who(), "action", a.String(), func() (sent, error) {
		rep, err := r.cmd.Act(ctx, a)
		return sent{action: rep}, err
	})
	if why == "error" {
		// The module's refusal is a named error (its own code); the action is recorded failed and the run reads it
		// as not performed.
		out.action.Code = actionCode(out.err)
		r.event("action_error", map[string]any{"action": a.String(), "code": out.action.Code})
		return out.action, why
	}
	if why != "" {
		return out.action, why
	}
	if a.Practice {
		r.practice++
		r.event("practice_revive", map[string]any{"bot": r.roleName(a.Who), "leg": r.lastLegName()})
		if r.rec != nil {
			r.rec.UpdatePull(func(p *PullRecord) { p.PracticeRevives++ })
		}
	}
	return out.action, ""
}

// actionCode is the module's refusal code, or "refused" for another error (a bad reply, a timeout).
func actionCode(err error) string {
	var ce *tbcmd.CommandError
	if errors.As(err, &ce) && ce.Code != "" {
		return ce.Code
	}
	return "refused"
}

// actionMoveGate is the gate every moving action passes in the same step as its send: a complete state report taken
// now and newer than the last order, no stop pending, and — unless this is the wipe's recovery — nobody in combat
// and nobody dead. A teleport of a party that is still fighting or still down would move a body the module has not
// given back.
func (r *Run) actionMoveGate(ctx context.Context, a Action) string {
	if !r.refresh(ctx) {
		if r.stopReason != "" || r.lost {
			return "stop"
		}
		return "report"
	}
	if !r.reportComplete() {
		return "report"
	}
	// The driver is strictly sequential: a report requested after the last send returned was requested after that
	// send. Equal clocks are that case; an older one was taken before the last order and is stale.
	if r.repAt.Before(r.lastOrderAt) {
		return "stale"
	}
	if a.Recovery {
		return ""
	}
	if r.busy() {
		return "combat"
	}
	for _, name := range r.cfg.Party {
		if b := r.bot(name); b != nil && isDead(*b) {
			return "dead:" + r.roleName(name)
		}
	}
	return ""
}

// actionStateGate is the gate of the state-changing actions. The GM lines change a mode and need only the stop
// check act already made. A revive is the practice path only, and the budget refuses it before the send. The
// instance reset waits for the run's end and for a fresh report of the whole party outside: the module refuses a
// reset from inside, so the driver must not even ask.
func (r *Run) actionStateGate(ctx context.Context, a Action) string {
	switch a.Kind {
	case ActionRevive:
		if !r.practiceOn() {
			return "practice"
		}
		if a.Practice && r.practice >= r.lim.MaxPractice {
			return "budget"
		}
		if !r.refresh(ctx) {
			if r.stopReason != "" || r.lost {
				return "stop"
			}
			return "report"
		}
		if !r.reportComplete() {
			return "report"
		}
		if r.repAt.Before(r.lastOrderAt) {
			return "stale"
		}
		if a.Who != "party" {
			if b := r.bot(a.Who); b == nil {
				return "report"
			} else if !isDead(*b) {
				return "alive"
			}
		}
		return ""
	case ActionInstanceReset:
		if !r.ended {
			return "running"
		}
		if !r.refresh(ctx) {
			if r.stopReason != "" || r.lost {
				return "stop"
			}
			return "report"
		}
		if !r.reportComplete() {
			return "report"
		}
		if r.repAt.Before(r.lastOrderAt) {
			return "stale"
		}
		if !r.allOutside() {
			return "inside"
		}
		return ""
	}
	return ""
}

// actionFightGate is the gate of the actions that make a bot start fighting: the module's run start and its resume.
// The party is about to pull, so the action needs a complete state report taken now and newer than the last order,
// everyone ready (alive, not resting, engine on, in range, on our map and instance) and budget left (the run's time
// and wipe budgets). It returns the reason it is shut, "" when the action may go.
func (r *Run) actionFightGate(ctx context.Context, a Action) string {
	if !r.refresh(ctx) {
		if r.stopReason != "" || r.lost {
			return "stop"
		}
		return "report"
	}
	if !r.reportComplete() {
		return "report"
	}
	if !r.repAt.After(r.lastOrderAt) {
		return "stale"
	}
	// A watch's run starts on the plan's map, in the instance the party entered. ready() compares the party with its
	// own map (the observer may be elsewhere), so without this a party that zoned out together between the placement
	// check and a retried start would pass as ready and the module would be asked to start a run off the plan's map.
	if r.cfg.Watch && !r.partyInInstance() {
		return "party-map"
	}
	if ok, why := r.ready(); !ok {
		return why
	}
	if r.lim.MaxMinutes > 0 && r.sinceStart() >= time.Duration(r.lim.MaxMinutes)*time.Minute {
		return "budget"
	}
	if r.lim.MaxWipes >= 0 && r.wipes > r.lim.MaxWipes {
		return "wipes"
	}
	return ""
}

// actionHold reports whether a shut gate is worth another attempt: the report is not in yet, the party's own state
// blocks the action for now. Everything else — a lost link, a pending stop, a module refusal, the practice switch,
// the spent budget, an unknown kind or destination — is not, and the caller ends the run with its own reason.
func actionHold(why string) bool {
	switch reasonCode(why) {
	case "report", "stale", "combat", "dead", "alive", "inside":
		return true
	}
	return false
}

// actStep is one action on a state's retry path: it maps the shut gate onto "wait and try again" and every other
// reason onto the run's stop. prefix names the calling state in the stop reason; a spent practice budget keeps the
// driver's own reason.
func (r *Run) actStep(ctx context.Context, a Action, prefix string) (done bool, retry bool) {
	_, why := r.act(ctx, a)
	switch why {
	case "":
		return true, false
	case "link", "stop":
		return false, false
	case "budget":
		r.stop("practice-budget")
		return false, false
	case "practice":
		r.stop(prefix)
		return false, false
	}
	if actionHold(why) {
		return false, true
	}
	r.stop(prefix + ":" + reasonCode(why))
	return false, false
}
