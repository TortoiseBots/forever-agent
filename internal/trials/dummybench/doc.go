// Package dummybench scores the training-dummy spell/damage/reaction bench
// (module feature issue #18): per-spell damage and reaction tallies from the
// combat log rows the agent already parses, checked against a small
// per-class table of expected numbers.
//
// A dummy repeat is scored by trial rule, not by verdicts.json alone: each
// rule names one spell and one stat ({metric: dummy, spell, stat}), and the
// bench reads TallyRep's per-spell tallies for the rule's spell. Bench rule
// (binding): a setup row is ok only after a server read-back; unknown stays
// unknown; INVALID or SKIPPED never become PASS. The EXPECTED TABLE decides:
// a mean damage below min_damage is FAIL, a mean reaction above
// max_reaction_s is FAIL, both met is PASS; a missing bound, or a known
// reaction bound with no measured reaction, is UNKNOWN. A stranger (a cast
// spell the table does not name for this spec) makes the whole repeat
// INVALID: no spell in it scores PASS or FAIL.
//
// Table format (documented): one JSON file per class/spec, for example
// testdata/dummy_mage_fire.json:
//
//	{"class": "mage", "spec": "fire",
//	 "expects": [{"spell": "Fireball", "min_damage": null,
//	              "max_reaction_s": null}, ...]}
//
// expects holds one row per spell the fight may cast (the bot's own module
// rotation decides the order; the table does not). min_damage is the least
// mean damage per cast the bench accepts and max_reaction_s the slowest
// mean reaction (able-to-act to cast start) it accepts. A bound the server
// has not measured is JSON null ("to be measured"): it scores UNKNOWN,
// never FAIL. A bound must be a finite JSON number >= 0, spell names are
// trimmed and folded for matching, and a spell named twice is refused with
// a *TableError.
//
// The scored window is the repeat's fight_start..fight_end from marks, read
// in the events' time base; a cast inside it counts with all its damage
// rows, up to DamageGraceS after its go (the last cast's projectile travel).
// Reaction runs from the moment the bot could act (the engage order, the
// previous cast's GCD/cast time, and the spell's own and category
// cooldowns), never from the previous cast's go.
package dummybench
