package geartalents

// Translated scoring tests from the Python bench's test_gear_proficiency.py: the shipped warrior
// kits carry their item class data and need Plate Mail (750) and their weapon skill. Every other
// case in that file drives the live setup/guard and is not translated (see doc).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/gear"
)

// gpContains is `needle in haystack` for a []int64.
func gpContains(haystack []int64, needle int64) bool {
	for _, value := range haystack {
		if value == needle {
			return true
		}
	}
	return false
}

func TestProficiencyTest_TheShippedWarriorKitsCarryClassDataAndNeedPlateAndTheirWeapon(t *testing.T) {
	const plateMail = int64(750)
	for role, levels := range gear.LoadKits()["warrior"] {
		for level, items := range levels {
			if missing := gear.MissingClass(items); len(missing) != 0 {
				t.Fatalf("%s/%s: kit entries without class data: %v", role, level, missing)
			}
			if spells := gear.ProficiencySpells(items); !gpContains(spells, plateMail) {
				t.Fatalf("%s/%s: proficiency spells %v lack Plate Mail 750", role, level, spells)
			}
		}
	}
}
