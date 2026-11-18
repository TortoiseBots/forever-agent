package edge

// Translation of tests/test_drink_settle.py.
//
// Every test in this file drives trl.live.situations.Situation.run_ooc — the live runner's
// out-of-combat window and its bench-1113 meal handling. The Go port has no live runner half
// (no internal/trials/live/situations, no Situation, no MEAL_WAIT_S or DRINK_AURA_IDS), so the
// tests are not translated. Listed here so the coverage gap is visible:
//
//   - test_no_meal_clears_both_and_opens_at_once
//   - test_a_meal_is_waited_out_and_the_hire_is_never_cleared
//   - test_a_refused_mana_reset_after_a_meal_makes_the_repeat_invalid
//   - test_an_unreadable_max_mana_after_a_meal_makes_the_repeat_invalid
//   - test_a_meal_that_outlasts_the_wait_invalidates_the_repeat
//   - test_an_aura_list_that_never_reads_makes_the_repeat_invalid
//   - test_reads_that_fail_and_then_show_no_meal_open_the_window
//   - test_a_refused_cleanup_line_does_not_stop_the_window
//   - test_the_drink_ids_match_the_trials_uptime_list (pins the trial's aura list against the
//     live module's DRINK_AURA_IDS; that constant has no Go home either)
