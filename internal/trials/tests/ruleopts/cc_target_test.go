package ruleopts

// tests/test_cc_target.py is NOT translated: every case drives the live runner.
//
// cc_target: the player attacks every unmarked summon (the marked one stays untouched) and
// mobs_ready needs every unmarked summon in combat. All five methods build a
// trl.live.situations.Situation with test_live.FakeAgent/FakeMarks/StubCollector, patch
// situations.time and call sit.engage_cc_targets()/sit.run() — there is no scoring-module
// entry point and the live half of the port is out of scope:
//
//	CcTargetEngageTest.test_every_unmarked_summon_is_attacked_and_the_marked_one_is_not
//	CcTargetEngageTest.test_mobs_ready_needs_every_unmarked_summon_in_combat
//	CcTargetEngageTest.test_mobs_ready_fails_when_the_marked_mob_is_idle
//	CcTargetEngageTest.test_mobs_ready_passes_when_the_mark_was_taunted_into_combat
//	CcTargetEngageTest.test_run_calls_it_for_cc_target
