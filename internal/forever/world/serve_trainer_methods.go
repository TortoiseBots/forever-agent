package world

// The trainer daemon methods (issue #106): trainer_list and train, the names and JSON of turtle-agent's, over the R4
// act steps (trainer_act.go) and the shared method frame. A call waits for the server's answer and returns it: the
// list the server sent, or the learned-spell answer (and the fare that left the purse) or the refusal. Preconditions
// (bad parameters, a unit that is not in the world model, out of reach, a creature that cannot train, a spell the
// cached list does not hold) are RPC errors raised before any packet; once a packet went out a refusal is a result
// with ok false and a trainer_failed event, and only a stop stays an error.

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveTrainerParams is the parameter object of the trainer methods.
type serveTrainerParams struct {
	GUID    GUID   `json:"guid"`
	SpellID *int32 `json:"spell_id,omitempty"`
	WaitMS  int    `json:"wait_ms,omitempty"`
}

// ServeTrainerSpell is one row of a trainer's list. State is what this client can tell about the row: "known" (the
// spellbook holds it), "unavailable" (the character's level or a required ability is missing), "learnable", or
// "unknown_level" while the model has not read the character's level.
type ServeTrainerSpell struct {
	SpellID      int32   `json:"spell_id"`
	State        string  `json:"state"`
	Cost         uint32  `json:"cost"`
	Level        uint8   `json:"level"`
	ReqSkill     uint32  `json:"req_skill,omitempty"`
	ReqSkillRank uint32  `json:"req_skill_rank,omitempty"`
	ReqAbility   []int32 `json:"req_ability,omitempty"`
}

// ServeTrainer is the list the trainer sent: its type (0 class, 1 mounts, 2 tradeskills, 3 pets), the id a purchase
// names and its greeting.
type ServeTrainer struct {
	TrainerType int8                `json:"trainer_type"`
	TrainerID   uint32              `json:"trainer_id"`
	Greeting    string              `json:"greeting"`
	Spells      []ServeTrainerSpell `json:"spells"`
}

// ServeTrainerResult is both trainer methods' answer. Since is the event-log cursor before the call's first packet;
// Event is the row that answers the call (trainer_list, trainer_trained or trainer_failed).
type ServeTrainerResult struct {
	Since   uint64        `json:"since"`
	OK      bool          `json:"ok"`
	Event   *Event        `json:"event"`
	Trainer *ServeTrainer `json:"trainer,omitempty"`
	Lines   []string      `json:"lines"`
}

// registerTrainerMethods wires trainer_list and train into the method table.
func registerTrainerMethods(srv *rpc.Server, st *serveState) {
	srv.Register("trainer_list", serveCallTyped(st, func(s *session, ctx context.Context, p serveTrainerParams) (ServeTrainerResult, error) {
		return s.serveTrainerList(ctx, p)
	}))
	srv.Register("train", serveCallTyped(st, func(s *session, ctx context.Context, p serveTrainerParams) (ServeTrainerResult, error) {
		return s.serveTrain(ctx, p)
	}))
}

// requireTrainer is the trainer methods' flag check, raised before any packet: a creature with the trainer bit, or a
// gossip NPC whose menu may carry the trainer option (the same test the act steps make).
func requireTrainer(entry int32, flags uint32) error {
	if !npc.IsTrainer(flags) && flags&npc.NPCFlagGossip == 0 {
		return fmt.Errorf("entry %d is not a trainer (NpcFlags %#x has neither the trainer bit nor a gossip menu)", entry, flags)
	}
	return nil
}

// trainerRows turns the list the trainer step read into the method's answer, with each row's state.
func (s *session) trainerRows(l *npc.TrainerList) *ServeTrainer {
	out := &ServeTrainer{TrainerType: l.Type, TrainerID: l.TrainerID, Greeting: l.Greeting, Spells: make([]ServeTrainerSpell, 0, len(l.Spells))}
	level := s.selfLevel()
	for _, sp := range l.Spells {
		row := ServeTrainerSpell{SpellID: sp.SpellID, Cost: sp.MoneyCost, Level: sp.ReqLevel, ReqSkill: sp.ReqSkillLine, ReqSkillRank: sp.ReqSkillRank}
		for _, a := range sp.ReqAbility {
			if a != 0 {
				row.ReqAbility = append(row.ReqAbility, a)
			}
		}
		switch {
		case s.knowsSpell(sp.SpellID):
			row.State = "known"
		case level <= 0:
			row.State = "unknown_level"
		case level < int32(sp.ReqLevel) || (l.Type != trainerTypePet && s.missingAbility(sp)):
			row.State = "unavailable"
		default:
			row.State = "learnable"
		}
		out.Spells = append(out.Spells, row)
	}
	return out
}

// missingAbility reports a row whose required ability the spellbook does not hold.
func (s *session) missingAbility(sp npc.TrainerSpell) bool {
	for _, req := range sp.ReqAbility {
		if req != 0 && !s.knowsSpell(req) {
			return true
		}
	}
	return false
}

// serveTrainerList opens the trainer and answers its list, read afresh from the server.
func (s *session) serveTrainerList(parent context.Context, p serveTrainerParams) (ServeTrainerResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeTrainerResult{}, err
	}
	defer cancel()
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeTrainerResult{}, err
	}
	if err := requireTrainer(unit.Info.Entry, unit.Info.NpcFlags); err != nil {
		return ServeTrainerResult{}, err
	}
	res := ServeTrainerResult{Since: s.EventLog().Last(), Lines: []string{}}
	a := s.methodState()
	lines, refusal, err := s.methodStep(parent, ctx, a, actVerbTrainer, nil)
	res.Lines = lines
	if err != nil {
		return res, err
	}
	if refusal != "" {
		res.Event = s.methodEvent(events.KindTrainerFailed, unit.GUID, map[string]any{"entry": unit.Info.Entry}, refusal)
		return res, nil
	}
	res.OK = true
	res.Trainer = s.trainerRows(a.trainer)
	res.Event = s.firstEventSince(res.Since, events.KindTrainerList)
	return res, nil
}

// serveTrain buys one spell from the trainer: the list is read first when this NPC's is not cached, and the answer is
// the server's own (the learned-spell answer with the fare read back from the purse, or the refusal).
func (s *session) serveTrain(parent context.Context, p serveTrainerParams) (ServeTrainerResult, error) {
	if p.SpellID == nil || *p.SpellID <= 0 {
		return ServeTrainerResult{}, errors.New("train needs spell_id, a positive spell id from the trainer's list")
	}
	spell := *p.SpellID
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeTrainerResult{}, err
	}
	defer cancel()
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeTrainerResult{}, err
	}
	if err := requireTrainer(unit.Info.Entry, unit.Info.NpcFlags); err != nil {
		return ServeTrainerResult{}, err
	}
	a := s.methodState()
	cached := a.hasTrainer && a.trainer != nil && a.trainer.Trainer == unit.GUID
	if cached && !listHasSpell(a.trainer, spell) {
		// A row the cached list does not have is a precondition: no packet has gone out yet.
		return ServeTrainerResult{}, fmt.Errorf("the trainer's list has no spell %d", spell)
	}
	res := ServeTrainerResult{Since: s.EventLog().Last(), Lines: []string{}}
	failed := func(text string) (ServeTrainerResult, error) {
		// A server refusal already wrote its own trainer_failed row; a refusal the step made before sending did not.
		if e := s.firstEventSince(res.Since, events.KindTrainerFailed); e != nil {
			res.Event = e
			return res, nil
		}
		res.Event = ptrEvent(s.event(Event{
			Kind:       events.KindTrainerFailed,
			Source:     s.eventName("", unit.GUID),
			SourceGUID: eventGUID(unit.GUID),
			SpellID:    uint32(spell),
			Text:       text,
			Data:       map[string]any{"entry": unit.Info.Entry},
		}))
		return res, nil
	}
	if !cached {
		lines, refusal, err := s.methodStep(parent, ctx, a, actVerbTrainer, nil)
		res.Lines = append(res.Lines, lines...)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = fmt.Errorf("%w while reading the trainer list; no purchase was sent", err)
			}
			return res, err
		}
		if refusal != "" {
			return failed(refusal)
		}
	}
	var cost uint32
	for _, sp := range a.trainer.Spells {
		if sp.SpellID == spell {
			cost = sp.MoneyCost
		}
	}
	before := s.inventoryNow()
	lines, refusal, err := s.methodStep(parent, ctx, a, actVerbLearn, map[string]string{"spell": fmt.Sprint(spell)})
	res.Lines = append(res.Lines, lines...)
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if refusal != "" {
		return failed(refusal)
	}
	after := s.inventoryNow()
	res.OK = true
	res.Trainer = s.trainerRows(a.trainer)
	res.Event = ptrEvent(s.event(Event{
		Kind:       events.KindTrainerTrained,
		Source:     s.eventName("", unit.GUID),
		SourceGUID: eventGUID(unit.GUID),
		SpellID:    uint32(spell),
		Data:       map[string]any{"entry": unit.Info.Entry, "cost": cost, "copper_before": before.Coinage, "copper_after": after.Coinage},
	}))
	return res, nil
}

// listHasSpell reports whether the trainer's list holds the spell.
func listHasSpell(l *npc.TrainerList, spell int32) bool {
	for _, sp := range l.Spells {
		if sp.SpellID == spell {
			return true
		}
	}
	return false
}

// ptrEvent returns a pointer to a copy of one stored event row.
func ptrEvent(e Event) *Event { return &e }
