package fixesb

// Translated from tests/test_sweep_fixes.py. Every test in that file drives the live runner,
// which is not part of the Go scoring port, so none of them is translated:
//   - LevelCarryOverTest.* (gm_guard.GmGuard + setup.check_level, .levelup over the agent rpc)
//   - TalentLearnRetryTest.* (setup.check_talents + the flaky FakeAgent rpc)
//   - ArenaShortageTest.* (situations.load_arenas / eligible_arenas)
//   - PlayerVisibleExtrasTest.* (setup.check_player_visible)
//   - CcMarkAckTest.* (situations.Situation.mark_and_cc)
//   - NoFightReasonTest.* (trl.live run.Run.run_repeat)
//   - UnclaimedSummonTest.* (situations.summon_mob + gm_guard)
