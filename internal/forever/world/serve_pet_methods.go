package world

// pet's call table (ticket #90, round P2): the read `pet` and the order `pet_action`. The state both answer is the
// session's petWatch (pet.go); the packets are the pet package's builders.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/pet"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// registerPetMethods wires pet and pet_action into the method table.
func registerPetMethods(srv *rpc.Server, st *serveState) {
	srv.Register("pet", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServePet, error) {
		return s.servePet(), nil
	}))
	srv.Register("pet_action", serveCallTyped(st, func(s *session, ctx context.Context, p servePetActionParams) (ServePet, error) {
		return s.servePetAction(ctx, p)
	}))
}

// servePetActionParams is pet_action's call: TA's own argument names.
type servePetActionParams struct {
	Action  string `json:"action"`
	Target  GUID   `json:"target,omitempty"`
	Slot    *int   `json:"slot,omitempty"`
	SpellID int32  `json:"spell_id,omitempty"`
	Name    string `json:"name,omitempty"`
}

// servePetAction sends one order to the pet and answers the pet's state after it. Every refusal that needs no
// packet (no pet, a dead pet, a mounted character, an unknown action, an order the bar does not name, the bar
// unknown where the order needs it) is an error before anything is sent; a sent order answers a result.
func (s *session) servePetAction(_ context.Context, p servePetActionParams) (ServePet, error) {
	info := s.servePet()
	if !info.Present {
		return ServePet{}, errors.New("pet_action: the character has no pet")
	}
	if info.Dead {
		return ServePet{}, errors.New("pet_action: the pet is dead")
	}
	if s.mountDisplay() != 0 {
		return ServePet{}, errors.New("pet_action: the server drops pet orders while mounted")
	}
	if !info.Bar {
		return ServePet{}, errors.New("pet_action: the pet's bar has not arrived")
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	petGUID := wire.GUID(info.GUID)
	w := s.petState()
	send := func(core uint32, payload []byte) error { return s.transport().Send(core, payload) }
	setBar := func(fn func(*pet.Spells)) {
		if w.bar != nil && w.bar.Pet == petGUID {
			fn(w.bar)
		}
	}
	switch action {
	case "attack":
		target := wire.GUID(p.Target)
		if target.IsZero() && s.serveRT != nil {
			target = s.serveRT.target
		}
		if target.IsZero() {
			return ServePet{}, errors.New("pet_action attack: no target (give target or select one)")
		}
		if err := send(wire.CMSGPetAction, pet.BuildAction(petGUID, pet.Button(pet.CmdAttack, pet.ActCommand), target)); err != nil {
			return ServePet{}, err
		}
	case "follow", "stay":
		cmd, state := pet.CmdFollow, uint8(1)
		if action == "stay" {
			cmd, state = pet.CmdStay, 0
		}
		if err := send(wire.CMSGPetAction, pet.BuildAction(petGUID, pet.Button(cmd, pet.ActCommand), wire.EmptyGUID)); err != nil {
			return ServePet{}, err
		}
		setBar(func(b *pet.Spells) { b.Command = state })
	case "passive", "defensive", "aggressive":
		react := map[string]uint32{"passive": uint32(pet.ReactPassive), "defensive": uint32(pet.ReactDefensive), "aggressive": uint32(pet.ReactAggressive)}[action]
		if err := send(wire.CMSGPetAction, pet.BuildAction(petGUID, pet.Button(react, pet.ActReaction), wire.EmptyGUID)); err != nil {
			return ServePet{}, err
		}
		setBar(func(b *pet.Spells) { b.React = uint8(react) })
	case "cast":
		sp, btn, err := s.pickPetSpell(info, p)
		if err != nil {
			return ServePet{}, err
		}
		if sp.Slot < pet.SpellFirst || sp.Slot >= pet.SpellEnd || sp.Passive {
			return ServePet{}, fmt.Errorf("pet_action cast: %s is not a castable button of the pet's bar", petSpellLabel(sp))
		}
		target := wire.GUID(p.Target)
		if target.IsZero() && s.serveRT != nil {
			// TA's cast sends the player's selection when the order names none; the server drops a target the spell
			// does not need.
			target = s.serveRT.target
		}
		if err := send(wire.CMSGPetAction, pet.BuildAction(petGUID, btn, target)); err != nil {
			return ServePet{}, err
		}
	case "autocast_on", "autocast_off":
		sp, _, err := s.pickPetSpell(info, p)
		if err != nil {
			return ServePet{}, err
		}
		if sp.Passive {
			return ServePet{}, fmt.Errorf("pet_action %s: %s is passive", action, petSpellLabel(sp))
		}
		on := action == "autocast_on"
		if err := send(wire.CMSGPetSpellAutocast, pet.BuildSpellAutocast(petGUID, sp.SpellID, on)); err != nil {
			return ServePet{}, err
		}
		setBar(func(b *pet.Spells) {
			for i, button := range b.Bar {
				if pet.ButtonAction(button) != sp.SpellID || pet.ButtonType(button) == pet.ActPassive {
					continue
				}
				typ := pet.ActDisabled
				if on {
					typ = pet.ActEnabled
				}
				b.Bar[i] = pet.Button(sp.SpellID, typ)
			}
			// A spell the list holds off the bar (slot -1) keeps its state in Actions.
			for i, button := range b.Actions {
				if pet.ButtonAction(button) != sp.SpellID || pet.ButtonType(button) == pet.ActPassive {
					continue
				}
				typ := pet.ActDisabled
				if on {
					typ = pet.ActEnabled
				}
				b.Actions[i] = pet.Button(sp.SpellID, typ)
			}
		})
	default:
		return ServePet{}, fmt.Errorf("pet_action: unknown action %q (attack, follow, stay, passive, defensive, aggressive, cast, autocast_on, autocast_off)", p.Action)
	}
	return s.servePet(), nil
}

// pickPetSpell finds the spell an order names on the pet's bar: by slot, spell id or name (the highest rank when
// several share the name). It answers the packed button too, so cast sends the bar's own button back.
func (s *session) pickPetSpell(info ServePet, p servePetActionParams) (ServePetSpell, uint32, error) {
	if !info.Bar {
		return ServePetSpell{}, 0, errors.New("pet_action: the pet's bar has not arrived")
	}
	w := s.petState()
	buttonOf := func(sp ServePetSpell) uint32 {
		if w.bar == nil {
			return 0
		}
		if sp.Slot >= 0 && sp.Slot < pet.BarSlots {
			return w.bar.Bar[sp.Slot]
		}
		for _, btn := range w.bar.Actions {
			if pet.ButtonAction(btn) == sp.SpellID {
				return btn
			}
		}
		return 0
	}
	var found *ServePetSpell
	for i := range info.Spells {
		sp := &info.Spells[i]
		switch {
		case p.Slot != nil && sp.Slot == *p.Slot:
		case p.Slot == nil && p.SpellID != 0 && sp.SpellID == uint32(p.SpellID):
		case p.Slot == nil && p.SpellID == 0 && p.Name != "" && sp.Name != "" && strings.EqualFold(sp.Name, p.Name):
		default:
			continue
		}
		if found == nil || sp.SpellID > found.SpellID {
			found = sp
		}
	}
	if found == nil {
		return ServePetSpell{}, 0, fmt.Errorf("pet_action: the pet has no such spell (slot, spell_id or name) on its bar; it has %s", petSpellList(info.Spells))
	}
	return *found, buttonOf(*found), nil
}

// petSpellLabel prints a spell the way a refusal names it.
func petSpellLabel(sp ServePetSpell) string {
	if sp.Name != "" {
		return fmt.Sprintf("%s (%d)", sp.Name, sp.SpellID)
	}
	return fmt.Sprint(sp.SpellID)
}

// petSpellList prints the bar's spells for a refusal that named none of them.
func petSpellList(sps []ServePetSpell) string {
	if len(sps) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(sps))
	for _, sp := range sps {
		parts = append(parts, fmt.Sprintf("%s@%d", petSpellLabel(sp), sp.Slot))
	}
	return strings.Join(parts, ", ")
}
