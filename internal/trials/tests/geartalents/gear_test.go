package geartalents

// Translated scoring tests from the Python bench's test_gear.py: the kit pick by class, role and
// level, the kit table itself, and the validator / shipped-trial checks. The guard, the setup
// checks, the strip and the runner wiring need the live runner and are not translated (see doc).

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/gear"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// gtPlayer is test_gear.py's player(): a GM-levelled character with the tier gear option.
func gtPlayer(klass string, level int64, role string) map[string]any {
	return map[string]any{"class": klass, "level": level, "forced_role": role, "gear": "tier"}
}

// gtSlots is {item["slot"] for item in items}.
func gtSlots(items []gear.Kit) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		out[item.Slot] = true
	}
	return out
}

func TestKitPickTest_TheKitFollowsLevelClassAndRole(t *testing.T) {
	for _, level := range []int64{40, 45, 50} {
		items, kitLevel, why := gear.PickKit(gtPlayer("warrior", level, "tank"))
		if kitLevel != level || why != "" {
			t.Fatalf("level %d: kit level %d, why %q", level, kitLevel, why)
		}
		if len(items) < 10 {
			t.Fatalf("level %d: %d items, want at least 10", level, len(items))
		}
		for _, item := range items {
			if item.RequiredLevel > float64(level) {
				t.Fatalf("level %d: item %d needs level %v", level, item.Entry, item.RequiredLevel)
			}
		}
	}
	tankItems, _, _ := gear.PickKit(gtPlayer("warrior", 40, "tank"))
	dpsItems, _, _ := gear.PickKit(gtPlayer("warrior", 40, "dps"))
	tank := gtSlots(tankItems)
	dps := gtSlots(dpsItems)
	if !tank["offhand"] {
		t.Fatal("the tank kit has no shield (offhand)")
	}
	if dps["offhand"] {
		t.Fatal("the DPS kit has an offhand item: it would read as a tank")
	}
	if !dps["twohand"] {
		t.Fatal("the DPS kit has no two-handed weapon")
	}
}

func TestKitPickTest_ALevelBetweenKitsTakesTheHighestKitBelowItWithinTheGap(t *testing.T) {
	if _, kitLevel, _ := gear.PickKit(gtPlayer("warrior", 47, "tank")); kitLevel != 45 {
		t.Fatalf("level 47: kit level %d, want 45", kitLevel)
	}
	if _, kitLevel, _ := gear.PickKit(gtPlayer("warrior", 49, "tank")); kitLevel != 45 {
		t.Fatalf("level 49: kit level %d, want 45", kitLevel)
	}
}

func TestKitPickTest_NoKitIsAClearReasonNotAnEmptyList(t *testing.T) {
	cases := []struct {
		name   string
		player map[string]any
		text   string
	}{
		{"no mage tank gear kit", gtPlayer("mage", 40, "tank"), "no mage tank gear kit"},
		{"no warrior tank gear kit for level 39", gtPlayer("warrior", 39, "tank"), "no warrior tank gear kit for level 39"},
		{"level 60 beyond the gap", gtPlayer("warrior", 60, "tank"), "within 9 levels of 60"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, _, why := gear.PickKit(tc.player)
			if len(items) != 0 {
				t.Fatalf("want no kit, got %d items", len(items))
			}
			if !strings.Contains(why, tc.text) {
				t.Fatalf("why %q does not contain %q", why, tc.text)
			}
		})
	}
	if items, _, _ := gear.PickKit(gtPlayer("warrior", 40, "tank"), gear.Kits{}); len(items) != 0 {
		t.Fatalf("an empty kit table gave a kit of %d items", len(items))
	}
}

func TestKitPickTest_EveryKitIsOneItemPerSlotWithArmorAndNoDuplicates(t *testing.T) {
	for klass, roles := range gear.LoadKits() {
		for role, levels := range roles {
			for level, items := range levels {
				slots := map[string]bool{}
				entries := map[int64]bool{}
				armor := 0.0
				for _, item := range items {
					if slots[item.Slot] {
						t.Fatalf("%s/%s/%s: duplicate slot %q", klass, role, level, item.Slot)
					}
					slots[item.Slot] = true
					if entries[item.Entry] {
						t.Fatalf("%s/%s/%s: duplicate entry %d", klass, role, level, item.Entry)
					}
					entries[item.Entry] = true
					armor += item.Armor
				}
				if armor <= 2000 {
					t.Fatalf("%s/%s/%s: %v armor, want more than 2000", klass, role, level, armor)
				}
			}
		}
	}
}

// gtGearErrors is test_the_validator's errors(): the gear errors of a copy of the DPS mage trial
// after updating the player with over.
func gtGearErrors(t *testing.T, over map[string]any) []string {
	t.Helper()
	trial := scenario.FindTrialByID("dps-mage-threat-cap")
	if trial == nil {
		t.Fatal("trial dps-mage-threat-cap not found")
	}
	player := pyjson.ObjectOrEmpty(trial["player"])
	for key, value := range over {
		player[key] = value
	}
	trial["player"] = player
	var out []string
	for _, err := range scenario.Validate(trial) {
		if strings.Contains(err, "gear") {
			out = append(out, err)
		}
	}
	return out
}

func TestValidatorAndTrialsTest_TheValidator(t *testing.T) {
	assertGearErrors := func(t *testing.T, over map[string]any, wantErrors bool) {
		t.Helper()
		errs := gtGearErrors(t, over)
		if wantErrors && len(errs) == 0 {
			t.Fatalf("want a gear error, got none")
		}
		if !wantErrors && len(errs) != 0 {
			t.Fatalf("want no gear error, got %v", errs)
		}
	}
	t.Run("tier", func(t *testing.T) { assertGearErrors(t, map[string]any{"gear": "tier"}, false) })
	t.Run("null", func(t *testing.T) { assertGearErrors(t, map[string]any{"gear": nil}, false) })
	t.Run("blue", func(t *testing.T) { assertGearErrors(t, map[string]any{"gear": "blue"}, true) })
	// no mage kit: the trial fails validation
	t.Run("tier mage", func(t *testing.T) {
		assertGearErrors(t, map[string]any{"gear": "tier", "class": "mage"}, true)
	})
	t.Run("tier level 30", func(t *testing.T) {
		assertGearErrors(t, map[string]any{"gear": "tier", "level": int64(30)}, true)
	})
}

func TestValidatorAndTrialsTest_EveryTankAndDpsTrialTurnsItOnWithAKitThatFits(t *testing.T) {
	var files []string
	for _, path := range scenario.TrialFiles() {
		if strings.HasPrefix(path, "dps/") || strings.HasPrefix(path, "tanks/") {
			files = append(files, path)
		}
	}
	if len(files) < 6 {
		t.Fatalf("%d tank/dps trials, want at least 6", len(files))
	}
	for _, path := range files {
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		player := pyjson.ObjectOrEmpty(trial["player"])
		if gearVal, _ := player["gear"].(string); gearVal != "tier" {
			t.Fatalf("%s: player.gear = %v, want tier", path, player["gear"])
		}
		checks := map[string]bool{}
		if items, ok := pyjson.List(trial["setup_checks"]); ok {
			for _, cid := range items {
				checks[pyjson.Str(cid)] = true
			}
		}
		if !checks["player_gear"] {
			t.Fatalf("%s: setup_checks lacks player_gear", path)
		}
		items, _, why := gear.PickKit(player)
		if len(items) == 0 {
			t.Fatalf("%s: no kit: %s", path, why)
		}
		// a DPS-role player (the tank-* trials) gets no shield: it would read as a tank
		if role, _ := player["forced_role"].(string); role != "tank" {
			if gtSlots(items)["offhand"] {
				t.Fatalf("%s: a non-tank player's kit has an offhand item", path)
			}
		}
	}
}

func TestPickKitNoShield(t *testing.T) {
	t.Run("tank kit switches to shield-free two-hand kit", func(t *testing.T) {
		for _, level := range []int64{40, 45, 50} {
			items, kitLevel, why := gear.PickKitNoShield(gtPlayer("warrior", level, "tank"))
			if why != "" || kitLevel != level || len(items) == 0 {
				t.Fatalf("level %d: kit=%d level=%d why=%q", level, len(items), kitLevel, why)
			}
			if gear.HasShield(items) {
				t.Fatalf("level %d: PickKitNoShield returned a shield", level)
			}
			if !gtSlots(items)["twohand"] {
				t.Fatalf("level %d: expected twohand weapon in shield-free kit", level)
			}
		}
	})

	t.Run("one-hand plus non-shield off-hand is kept", func(t *testing.T) {
		c2, s7 := int64(2), int64(7)  // one-hand sword
		c4, s0 := int64(4), int64(0)  // off-hand misc (non-shield)
		c4p, s4 := int64(4), int64(4) // plate
		custom := gear.Kits{
			"paladin": {
				"tank": {
					"40": {
						{Entry: 1001, Slot: "head", ItemClass: &c4p, Subclass: &s4},
						{Entry: 1002, Slot: "mainhand", ItemClass: &c2, Subclass: &s7},
						{Entry: 1003, Slot: "offhand", ItemClass: &c4, Subclass: &s0},
					},
				},
			},
		}
		items, _, why := gear.PickKitNoShield(gtPlayer("paladin", 40, "tank"), custom)
		if why != "" || len(items) != 3 {
			t.Fatalf("got %d items why=%q, want all 3 kept", len(items), why)
		}
	})

	t.Run("strips shield when no shield-free dps kit exists", func(t *testing.T) {
		c2, s4 := int64(2), int64(4) // one-hand mace
		c4, s6 := int64(4), int64(6) // shield
		c4p, s4p := int64(4), int64(4)
		custom := gear.Kits{
			"paladin": {
				"tank": {
					"40": {
						{Entry: 1001, Slot: "head", ItemClass: &c4p, Subclass: &s4p},
						{Entry: 1002, Slot: "mainhand", ItemClass: &c2, Subclass: &s4},
						{Entry: 1003, Slot: "offhand", ItemClass: &c4, Subclass: &s6},
					},
				},
			},
		}
		items, _, why := gear.PickKitNoShield(gtPlayer("paladin", 40, "tank"), custom)
		if why != "" || len(items) != 2 {
			t.Fatalf("got %d items why=%q, want 2 (without shield)", len(items), why)
		}
		if gear.HasShield(items) {
			t.Fatalf("shield item was not stripped: %+v", items)
		}
	})
}
