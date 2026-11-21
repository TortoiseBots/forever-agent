// Package dummybench scores the training-dummy spell/damage/reaction bench
// (module feature issue #18): per-spell damage and reaction tallies from the
// combat log rows the agent already parses, checked against a small
// per-class table of expected numbers.
//
// A dummy repeat is scored by trial rule, not by verdicts.json alone: each
// rule names one spell and one stat ({metric: dummy, spell, stat}), and the
// bench reads ScoreRep's per-spell tallies for the rule's spell. Bench rule
// (binding): a setup row is ok only after a server read-back; unknown stays
// unknown; INVALID or SKIPPED never become PASS. A number the table does
// not know scores UNKNOWN, never FAIL; a stranger (a row with a spell the
// table does not name for this spec) ends the rep INVALID.
//
// Table format (documented): one JSON file per class/spec, for example
// testdata/dummy_mage_fire.json:
//
//	{"class": "mage", "spec": "fire",
//	 "rotation": ["Fireball", "Fireball", "Fireblast", "Fireball"],
//	 "expects": [{"spell": "Fireball", "min_damage": null,
//	              "max_reaction_s": null}, ...]}
//
// rotation is the order the fight drives (the class bot's rotation for the
// fixed time; repeats read the same list). expects holds one row per spell
// the rotation may cast; min_damage is the least mean damage per cast the
// bench accepts and max_reaction_s the slowest mean reaction (able-to-act
// to cast start) it accepts. A bound the server has not measured is JSON
// null ("to be measured"): it scores UNKNOWN, never FAIL.
package dummybench
