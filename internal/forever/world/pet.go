package world

// The player's pet on ForeverCore (ticket #90, round P2): which unit is the pet, the control bar the server sends, the
// orders the agent sends back, and the pet's own events.
//
// Where the facts come from, the way the core's own client reads them:
//   - which unit is the pet: the character's own UF::UnitData Summon (Unit::SetPet, Object/Pet.cpp:401 at the pinned
//     core); the pet's own row (health, power, target, combat flag) is an ordinary unit row the model already holds.
//   - the control bar: SMSG_PET_SPELLS_MESSAGE when the pet is summoned (Player::PetSpellInitialize) and
//     SMSG_PET_MODE when the bar's command or reaction changes. HandlePetAction sets the state and sends nothing
//     back (PetHandler.cpp:66-120), so an order sent here is applied to the session's copy and the next SMSG_PET_MODE
//     corrects it.
//   - a refusal of a pet spell: SMSG_PET_CAST_FAILED, whose reason is a SpellCastResult (the combat package's
//     names); a refused order: SMSG_PET_ACTION_FEEDBACK (PetDefines.h:102-109).
//
// The bar's `enabled` is the core's own test: the bar is known and the character's UNIT_FIELD_MOUNTDISPLAYID is zero
// (the core sends no disable on mount; HandlePetAction drops the orders while mounted, PetHandler.cpp:66).

import (
	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/pet"
	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// petWatch is what the session keeps of the pet. A reconnect builds a fresh session, so it starts empty: the bar is
// what the last SMSG_PET_SPELLS_MESSAGE said, mode the last SMSG_PET_MODE, refused the pet's last refusal, and
// lastPet/lastDead the pet the character's own Summon named at the last check.
type petWatch struct {
	bar      *pet.Spells
	mode     *pet.Mode
	refused  string
	lastPet  wire.GUID
	lastDead bool
}

// petState is the session's pet watch, made on first use (a session assembled by hand has none).
func (s *session) petState() *petWatch {
	if s.pet == nil {
		s.pet = &petWatch{}
	}
	return s.pet
}

// unitOwner is the owner rule the pet layer runs on: UF::UnitData SummonedBy, else CreatedBy, else CharmedBy, with
// the field's name. A unit with none of them has no owner.
func unitOwner(i objects.Info) (wire.GUID, string) {
	switch {
	case !i.SummonedBy.IsZero():
		return i.SummonedBy, "summoned_by"
	case !i.CreatedBy.IsZero():
		return i.CreatedBy, "created_by"
	case !i.CharmedBy.IsZero():
		return i.CharmedBy, "charmed_by"
	}
	return wire.EmptyGUID, ""
}

// handlePet answers the pet packets. A body this round cannot consume exactly is counted and merges nothing, like the
// vendor and loot handlers: the session's bar keeps what it had and the session never ends on a pet packet.
func (s *session) handlePet(p worldconn.Packet) error {
	w := s.petState()
	switch p.Core {
	case wire.SMSGPetSpellsMessage:
		sp, err := pet.ParseSpells(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if sp.Cleared {
			// The core's "remove the bar" body: SendRemoveControlBar answers an empty pet GUID.
			w.bar = nil
			w.mode = nil
			s.opts.logf("world: the pet's control bar was removed")
			return nil
		}
		// The newest bar is the server's latest word: an older SMSG_PET_MODE is not laid over it.
		bar := sp
		w.bar = &bar
		s.opts.logf("world: the pet's control bar: %d spell button(s), %d action(s), react %s, command %s",
			petSpellButtons(bar), len(bar.Actions), pet.ReactName(bar.React), pet.CommandName(bar.Command))
	case wire.SMSGPetMode:
		m, err := pet.ParseMode(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.mode = &m
		if w.bar != nil && w.bar.Pet == m.Pet {
			w.bar.Command, w.bar.React = m.Command, m.React
		}
	case wire.SMSGPetCastFailed:
		f, err := pet.ParseCastFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		reason := combat.SpellFailureName(f.Reason)
		w.refused = reason
		s.opts.logf("world: the pet's spell %d failed: %s", f.SpellID, reason)
		s.event(Event{
			Kind:       events.KindPetError,
			Source:     s.eventName("", w.lastPet),
			SourceGUID: eventGUID(w.lastPet),
			SpellID:    uint32(f.SpellID),
			Text:       "pet spell failed: " + reason,
			Data:       map[string]any{"reason": reason, "code": f.Reason},
		})
	case wire.SMSGPetActionFeedback:
		f, err := pet.ParseFeedback(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		name := pet.FeedbackName(f.Response)
		s.opts.logf("world: the pet refused an order: %s (spell %d)", name, f.SpellID)
		s.event(Event{
			Kind:       events.KindPetError,
			Source:     s.eventName("", w.lastPet),
			SourceGUID: eventGUID(w.lastPet),
			SpellID:    uint32(f.SpellID),
			Text:       "pet order refused: " + name,
			Data:       map[string]any{"feedback": name},
		})
	}
	return nil
}

// petSpellButtons counts the bar buttons the client shows as spells (the 10 the bar carries, type Enabled,
// Disabled or Passive and a non-zero action).
func petSpellButtons(sp pet.Spells) int {
	n := 0
	for _, btn := range sp.Bar {
		if isPetSpellButton(btn) {
			n++
		}
	}
	return n
}

// isPetSpellButton reports a button the client draws as a spell: a non-zero action and the Enabled (0xC1), Disabled
// (0x81) or Passive (0x01) state (UnitDefines.h:431-438).
func isPetSpellButton(btn uint32) bool {
	typ := pet.ButtonType(btn)
	return pet.ButtonAction(btn) != 0 && (typ == pet.ActEnabled || typ == pet.ActDisabled || typ == pet.ActPassive)
}

// petCheck runs after every applied SMSG_UPDATE_OBJECT (the dispatch calls it beside the other model watchers). It
// compares the character's own Summon with the pet it announced and emits the pet events: a new pet summoned, the
// old one dismissed, the pet dying and coming back.
func (s *session) petCheck() {
	if s.world == nil {
		return
	}
	self := s.selfOrMover()
	if self.IsZero() {
		return
	}
	me, ok := s.world.Object(self)
	if !ok {
		return
	}
	g := me.Info.Summon
	var dead, found bool
	var entry int32
	if !g.IsZero() {
		if po, ok := s.world.Object(g); ok {
			found, dead, entry = true, po.Dead, po.Info.Entry
		}
	}
	w := s.petState()
	prev, prevDead := w.lastPet, w.lastDead
	if g != prev {
		// A new pet starts with no refusal of the old one's.
		w.refused = ""
	}
	w.lastPet, w.lastDead = g, dead
	switch {
	case g != prev:
		if !prev.IsZero() {
			s.event(Event{
				Kind:       events.KindPetDismissed,
				Source:     s.eventName("", prev),
				SourceGUID: eventGUID(prev),
				Target:     s.eventName("", self),
				TargetGUID: eventGUID(self),
				Text:       "pet dismissed",
				Data:       map[string]any{"was_dead": prevDead},
			})
		}
		if !g.IsZero() {
			s.event(Event{
				Kind:       events.KindPetSummoned,
				Source:     s.eventName("", g),
				SourceGUID: eventGUID(g),
				Target:     s.eventName("", self),
				TargetGUID: eventGUID(self),
				Data:       map[string]any{"entry": entry, "dead": dead},
			})
			if dead {
				// The core sends the pet already dead (a hunter's pet that died where it was dismissed): it is
				// announced dead rather than dying after the fact.
				s.event(Event{
					Kind:       events.KindPetDied,
					Source:     s.eventName("", g),
					SourceGUID: eventGUID(g),
					Target:     s.eventName("", self),
					TargetGUID: eventGUID(self),
					Text:       "pet died",
				})
			}
		}
	case !g.IsZero() && found && dead && !prevDead:
		s.event(Event{
			Kind:       events.KindPetDied,
			Source:     s.eventName("", g),
			SourceGUID: eventGUID(g),
			Target:     s.eventName("", self),
			TargetGUID: eventGUID(self),
			Text:       "pet died",
		})
	case !g.IsZero() && found && !dead && prevDead:
		s.event(Event{
			Kind:       events.KindPetRevived,
			Source:     s.eventName("", g),
			SourceGUID: eventGUID(g),
			Target:     s.eventName("", self),
			TargetGUID: eventGUID(self),
			Text:       "pet revived",
		})
	}
}

// petObject is the unit the character's own Summon names, when the model holds it: the pet this session reports and
// orders.
func (s *session) petObject() (*objects.Object, bool) {
	if s.world == nil {
		return nil, false
	}
	self := s.selfOrMover()
	if self.IsZero() {
		return nil, false
	}
	me, ok := s.world.Object(self)
	if !ok || me.Info.Summon.IsZero() {
		return nil, false
	}
	p, ok := s.world.Object(me.Info.Summon)
	if !ok {
		return nil, false
	}
	return p, true
}

// ServePetSpell is one spell of the pet's control bar or its action list: Slot is the bar button (3 to 6 for a
// castable spell) and -1 for a spell the list holds off the bar.
type ServePetSpell struct {
	SpellID  uint32 `json:"spell_id"`
	Name     string `json:"name,omitempty"`
	Slot     int    `json:"slot"`
	Autocast bool   `json:"autocast"`
	Passive  bool   `json:"passive,omitempty"`
}

// ServePet is pet's result: whether the character has a pet, its own unit row, and the bar's state. Without a pet
// only Present (false) and Bar (false) are set.
type ServePet struct {
	Present   bool    `json:"present"`
	GUID      GUID    `json:"guid,omitempty"`
	Entry     int32   `json:"entry,omitempty"`
	Name      string  `json:"name,omitempty"`
	Level     int32   `json:"level,omitempty"`
	Dead      bool    `json:"dead"`
	Health    int64   `json:"health"`
	MaxHealth int64   `json:"max_health"`
	PowerType uint8   `json:"power_type"`
	Power     int32   `json:"power"`
	MaxPower  int32   `json:"max_power"`
	InCombat  bool    `json:"in_combat"`
	Target    GUID    `json:"target,omitempty"`
	Distance  float32 `json:"distance"`
	Moving    bool    `json:"moving"`

	// Bar is true when the session holds the pet's control bar. React and Command are the last state the server or
	// this session's own order set; Enabled is false while the character is mounted.
	Bar     bool            `json:"bar"`
	React   string          `json:"react,omitempty"`
	Command string          `json:"command,omitempty"`
	Enabled bool            `json:"enabled"`
	Spells  []ServePetSpell `json:"spells,omitempty"`
	// Refused is the reason of the pet's last refused cast, cleared when a new pet is summoned.
	Refused string `json:"refused,omitempty"`
}

// servePet is the pet method's body: the pet's row from the model, plus the bar when it names that pet.
func (s *session) servePet() ServePet {
	o, ok := s.petObject()
	if !ok {
		return ServePet{}
	}
	u := s.serveUnit(o, s.Now())
	out := ServePet{
		Present: true, GUID: u.GUID, Entry: u.Entry, Name: u.Name, Level: u.Level, Dead: u.Dead,
		Health: u.Health, MaxHealth: u.MaxHealth, PowerType: u.PowerType, Power: u.Power, MaxPower: u.MaxPower,
		InCombat: u.InCombat, Target: u.Target, Distance: u.Distance, Moving: u.Moving,
	}
	w := s.petState()
	out.Refused = w.refused
	if w.bar != nil && w.bar.Pet == o.GUID {
		out.Bar = true
		out.Enabled = s.mountDisplay() == 0
		out.React, out.Command = pet.ReactName(w.bar.React), pet.CommandName(w.bar.Command)
		out.Spells = s.petSpells(w.bar)
	}
	return out
}

// petSpells lists a bar's spells the way the client's own bar shows them: every spell button of slots 3-6, then every
// action the list holds that no button shows (slot -1). A name is the server's when the spell-name record is already
// cached; pet never fetches, so an uncached spell answers its id alone.
func (s *session) petSpells(sp *pet.Spells) []ServePetSpell {
	var out []ServePetSpell
	seen := make(map[uint32]bool, len(sp.Actions)+pet.SpellEnd-pet.SpellFirst)
	for i := pet.SpellFirst; i < pet.SpellEnd; i++ {
		btn := sp.Bar[i]
		if !isPetSpellButton(btn) {
			continue
		}
		id := pet.ButtonAction(btn)
		seen[id] = true
		out = append(out, s.petSpell(id, i, btn))
	}
	for _, btn := range sp.Actions {
		id := pet.ButtonAction(btn)
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, s.petSpell(id, -1, btn))
	}
	return out
}

// petSpell is one spell row from a packed button.
func (s *session) petSpell(id uint32, slot int, btn uint32) ServePetSpell {
	typ := pet.ButtonType(btn)
	return ServePetSpell{
		SpellID: id, Name: s.cachedSpellName(id), Slot: slot,
		Autocast: typ == pet.ActEnabled, Passive: typ == pet.ActPassive,
	}
}
