package world_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/world"
)

// oldCastFailureNames are the texts game.CastFailName printed, in code order,
// before step 5 of docs/forever/SEAM-AUDIT.md (SpellDefines.h:284-433).
var oldCastFailureNames = []string{
	"affecting_combat",
	"already_at_full_health",
	"already_at_full_power",
	"already_being_tamed",
	"already_have_charm",
	"already_have_summon",
	"already_open",
	"aura_bounced",
	"autotrack_interrupted",
	"bad_implicit_targets",
	"bad_targets",
	"cant_be_charmed",
	"cant_be_disenchanted",
	"cant_be_prospected",
	"cant_cast_on_tapped",
	"cant_duel_while_invisible",
	"cant_duel_while_stealthed",
	"cant_stealth",
	"caster_aurastate",
	"caster_dead",
	"charmed",
	"chest_in_use",
	"confused",
	"dont_report",
	"equipped_item",
	"equipped_item_class",
	"equipped_item_class_mainhand",
	"equipped_item_class_offhand",
	"error",
	"fizzle",
	"fleeing",
	"food_lowlevel",
	"highlevel",
	"hunger_satiated",
	"immune",
	"interrupted",
	"interrupted_combat",
	"item_already_enchanted",
	"item_gone",
	"item_not_found",
	"item_not_ready",
	"level_requirement",
	"line_of_sight",
	"lowlevel",
	"low_castlevel",
	"mainhand_empty",
	"moving",
	"need_ammo",
	"need_ammo_pouch",
	"need_exotic_ammo",
	"nopath",
	"not_behind",
	"not_fishable",
	"not_here",
	"not_infront",
	"not_in_control",
	"not_known",
	"not_mounted",
	"not_on_taxi",
	"not_on_transport",
	"not_ready",
	"not_shapeshift",
	"not_standing",
	"not_tradeable",
	"not_trading",
	"not_unsheathed",
	"not_while_ghost",
	"no_ammo",
	"no_charges_remain",
	"no_champion",
	"no_combo_points",
	"no_dueling",
	"no_endurance",
	"no_fish",
	"no_items_while_shapeshifted",
	"no_mounts_allowed",
	"no_pet",
	"no_power",
	"nothing_to_dispel",
	"nothing_to_steal",
	"only_abovewater",
	"only_daytime",
	"only_indoors",
	"only_mounted",
	"only_nighttime",
	"only_outdoors",
	"only_shapeshift",
	"only_stealthed",
	"only_underwater",
	"out_of_range",
	"pacified",
	"possessed",
	"reagents",
	"requires_area",
	"requires_spell_focus",
	"rooted",
	"silenced",
	"spell_in_progress",
	"spell_learned",
	"spell_unavailable",
	"stunned",
	"targets_dead",
	"target_affecting_combat",
	"target_aurastate",
	"target_dueling",
	"target_enemy",
	"target_enraged",
	"target_friendly",
	"target_in_combat",
	"target_is_player",
	"target_not_dead",
	"target_not_in_party",
	"target_not_looted",
	"target_not_player",
	"target_no_pockets",
	"target_no_weapons",
	"target_unskinnable",
	"thirst_satiated",
	"too_close",
	"too_many_of_item",
	"totems",
	"training_points",
	"try_again",
	"unit_not_behind",
	"unit_not_infront",
	"wrong_pet_food",
	"not_while_fatigued",
	"target_not_in_instance",
	"not_while_trading",
	"target_not_in_raid",
	"disenchant_while_looting",
	"prospect_while_looting",
	"prospect_need_more",
	"target_freeforall",
	"no_edible_corpses",
	"only_battlegrounds",
	"target_not_ghost",
	"too_many_skills",
	"transform_unusable",
	"wrong_weather",
	"damage_immune",
	"prevented_by_mechanic",
	"play_time",
	"reputation",
	"min_skill",
	"unknown",
}

// TestCastFailureText pins every reason text to the output of the old 1.12 code
// table, so logs, events and RPC payloads keep their words.
func TestCastFailureText(t *testing.T) {
	for code := 0; code < 256; code++ {
		want := fmt.Sprintf("failed_%d", code)
		if code < len(oldCastFailureNames) {
			want = oldCastFailureNames[code]
		}
		if got := world.CastFailure(code).String(); got != want {
			t.Errorf("code %d: %q, want %q", code, got, want)
		}
	}
}

// TestCastFailurePayload pins the number a reason carries into JSON: cast_state's
// last_failure.reason and spell_fail's reason are 1.12 SpellCastResult codes.
func TestCastFailurePayload(t *testing.T) {
	for _, code := range []uint8{0, 11, 35, 60, 89, 97, 148, 149, 255} {
		b, err := json.Marshal(world.CastFailure(code))
		if err != nil || string(b) != fmt.Sprint(code) {
			t.Errorf("code %d: %q, %v", code, b, err)
		}
	}
}

// TestCastFailureReasons pins the reasons the spell state acts on.
func TestCastFailureReasons(t *testing.T) {
	if got := world.CastFailureInterrupted.String(); got != "interrupted" {
		t.Errorf("interrupted reads %q", got)
	}
	if got := world.CastFailureNotReady.String(); got != "not_ready" {
		t.Errorf("not ready reads %q", got)
	}
}
