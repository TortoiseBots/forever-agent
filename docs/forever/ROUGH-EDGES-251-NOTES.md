# Three rough edges from the live re-check (#251)

The live re-check of 25 November passed 8 of 9 rows and left three answer bugs. Each is here with its root cause from
the code and the core, the fix, and the tests.

## 1. A trade-slot cast answered the core's "no message" result as a refusal

**Root cause.** `Spell::prepare` does not cast a spell whose target is the trade item while the trade is open. It
stores the spell in the player's trade data and leaves by the silent path (`Spell.cpp:3826-3841`):

```cpp
if (m_targets.GetTargetMask() & TARGET_FLAG_TRADE_ITEM)
    if (TradeData* my_trade = modOwner->GetTradeData())
        if (!my_trade->IsInAcceptProcess())
        {
            // Spell will be cast after completing the trade. Silently ignore at this place
            my_trade->SetSpell(m_spellInfo->Id, m_CastItem);
            cleanupSpell(SPELL_FAILED_DONT_REPORT);
            return;
        }
```

`SPELL_FAILED_DONT_REPORT` is `32` (`SharedDefines.h:1747`), named "do not report" by the agent's table
(`internal/forever/combat/reasons.go`). The spell is cast from `TradeData::Update` once both sides accept it. The
agent's `cast` method took every result other than `success` as a refusal
(`internal/forever/world/serve_session_methods.go`, the `cast` registration), so the live row's accepted cast answered
`cast: the server refused the spell: do not report` although the spell went off and the lockbox unlocked.

**Fix.** `combat.SpellFailSilent` names the code and `combat.SilentSpellResult` recognises its string. The code alone
does not prove a cast: the core answers it for casts that never happened too (`Spell.cpp:6090`, a spell on its event
cooldown), so the stored spell is the proof:

* The core stores the trade-slot spell in the trade data and sends the update at that moment
  (`TradeData::SetSpell`, `TradeData.cpp:84-101`); the agent's trade model already reads it as `my_spell`. A trade-slot
  cast that answers the silent result waits up to a second (`tradeStoredSpell`) for the trade to name that spell, then
  answers `ServeCast` with `spell_id`, `trade_slot`, `silent: true` and `completes: "trade_accept"`.
* A silent result with nothing stored is no cast: the call fails with `cast: the server answered its no-message result
  for spell N and <why>: nothing was cast` (`no spell-go came`, or `the trade names no such spell`). It never says
  "refused": that wording stays for a real refusal.
* The update must be newer than the cast: the trade data keeps naming the earlier cast's spell, so the check compares
  the number of own-side trade updates from before the call (`trade.mineUpdates`). A second silent result with no new
  update fails as above.
* A plain success keeps the empty object every caller already gets.
* `mount`'s cast check stays as it is on main: its silent-result branch was out of this issue's scope, and
  `waitMounted` already decides from the display.
* The fight log's line for a silent result says `spell N answered the silent result (do not report)` instead of
  calling it a failure. The `spell_fail` row keeps the packet's own result name.

**Tests.** `TestCastOnTradeSlotTakesTheStoredSpellAsSuccess` (the fake stores the spell and answers reason 32, as the
core does), `TestCastTradeSlotSilentWithoutTheStoredSpellIsNoCast`, `TestCastNeverCallsTheSilentAnswerARefusal`,
`TestCastPlainSuccessKeepsTheEmptyAnswer` (the plain success is still `{}` byte for byte) and
`TestSecondCastOfAStoredTradeSpellNeedsItsOwnUpdate`. Reverting the stored-spell check, dropping the update sequence, or
reading every silent result as success fails one of them.

**Review follow-up.** The first version of this fix took every silent result as success. The self-review showed the
core's other `SPELL_FAILED_DONT_REPORT` sites are real refusals said silently (cooldown, CC, a tame that failed), so a
plain cast would have reported a spell that never went off. The stored spell now decides, and a silent result is
never called a refusal either way.

## 2. `walk_to` answered the between-maps error for a walk that ended in a map change

**Root cause.** The portal's trigger made the map change; the walk's own leg met it and the mover ends it with
`objects.ErrMapChange` (`Mover.MoveTo`, the wait's `ErrMapChange`/`betweenMaps` branches), and `walk_to` passed that
error straight out. Nothing waited for the transfer to land, so the answer described the moment between the maps
instead of the map the server moved the character to.

**Fix.** `serveWalkTo` (`internal/forever/world/serve_session.go`) keeps the walk's own answer:

* The leg meeting a transfer, the call waits out the between-maps window (`walkEndedInATransfer`, on the caller's
  context, bounded by the session's `mapChangeLimit`) and answers `{"changed_map":{map,x,y,z}}` with the map and the
  position the server put the character at.
* A walk still between maps when its deadline passes is the one error: `objects: the character is between maps: the
  character is still between maps at the walk's deadline` (a transfer that outlives `mapChangeLimit` says so too).
* A walk started while a transfer is already open never moves: `... the walk did not start`.

`walk_path` keeps its map-change error: the dungeon driver's legs read it (`driver.ErrMapChange`).

**Tests.** `TestWalkToAnswersTheMapChangeItEndedIn` (the pending, the suspend token and the new world land inside one
of the walk's waits), `TestWalkToFailsOnlyWhileTheTransferNeverLands` (the pending alone: the between-maps error at the
caller's deadline) and `TestWalkToAgainFromABuildingTransferIsAnError`. Returning the bare `ErrMapChange` again fails
the first.

## 3. The area-trigger watch was armed only by hand

**Root cause.** `area_trigger_watch` armed a volume (`internal/forever/world/trigger.go`) and the mover's `Cross` hook
sent `CMSG_AREA_TRIGGER` on a crossing, but nothing armed anything for a plain walk: the live row had to call the
method first. The agent keeps no table of triggers, and the only trigger data the daemon has is its plan folder.

**Fix.** The instance-portal volumes are indexed per map (`internal/forever/world/trigger_plan.go`), built once
from `--plans-dir`:

| Volume | Where | Radius |
|---|---|---|
| A plan's `entrance_trigger` | the plan's `portal` point, on the portal's map | the default 4 yd |

That is the only volume a walk arms: it is the one that changes the map. A plan's `area_trigger` step is the step's
own to fire (the group driver's `doAreaTrigger`, the remote driver's `takeTrigger`), which walks to `stand` and sends
once. Indexing the zone as well fired it on that walk in and again from the step, and in a remote run an agent walk
across a `by: "bot"` or `by: "auto"` zone fired it outside the GM/invisible guard the step keeps.

`armWalkTriggers` arms the portal volumes the walk's route crosses (the mover's position and then the destination for
`walk_to`, the polyline for `walk_path` and every walking leg of `dungeon_run`) and returns the disarm that removes
exactly what it added. A walk that begins inside a volume arms it as entered and sends nothing for it: the state a
previous walk leaves behind goes with its volume, so arming again at the portal is not a step in. An id the caller
armed by hand keeps the caller's volume and stays armed — one state per id is what keeps the #241 re-arm idempotent.
The 32-volume bound skips the rest with a log line rather than refusing the walk. `area_trigger_watch` stays for
everything the plans do not name.

**Tests.** `TestPlanIndexNamesOnlyTheInstancePortal`, `TestWalkArmsThePortalTriggerOfItsRoute`,
`TestWalkLeavesAManualVolumeAlone`, `TestWalkToArmsThePortalTriggerAndSendsIt`, `TestServeWalkToArmsThePlansPortalTrigger`
(a plain `walk_to` over the plan's portal point sends one `CMSG_AREA_TRIGGER`),
`TestWalkDoesNotArmThePlansAreaTriggerStep` (the walk in sends nothing; the step's own send is the only one),
`TestRemoteBossZoneIsNotFiredByAWalkCrossing`, `TestWalkStartingInsideDoesNotFireTheVolume` and
`TestCancelledDungeonWalkDisarmsItsVolumes` (the watch is empty after a cancelled walk; removing the disarm fails it).

## Docs

The manual changed with the code (`DOCS-SYNC.md`): [Movement](../manual/daemon/movement.md) (`walk_to`'s
`changed_map`, `walk_path`'s map-change error), [Teleports and travel](../manual/daemon/travel.md) ("When the watch is
armed"), [Spells and combat](../manual/player-actions/spells-and-combat.md) (the silent result and `ServeCast`) and
[Trade](../manual/player-actions/trade.md) (the trade-slot cast's answer).
