# Raid, round R2: `convert_raid` and `raid_move` on the Forever serve (#108)

Scope: `internal/forever/world` (the two calls, their RPC methods, the fake) and `internal/forever/serveclient` (the
two client calls). No command drives a raid yet (R3's `forever raid-watch`).

## The calls (`internal/forever/world/raidctl.go`)

| Call | Packet | Rank the core asks | Refused here before sending | Readback (5 s) |
|---|---|---|---|---|
| `convertRaid(ctx)` | `CMSG_CONVERT_RAID`, `Raid` true | leader, two or more members | not the leader, a group of one | the roster's `FlagRaid` (`party.State.Raid`) |
| `raidMove(ctx, guid, name, subgroup, swapWith)` | `CMSG_CHANGE_SUB_GROUP` (subgroup-1), or `CMSG_SWAP_SUB_GROUPS` | leader or assistant, raid only | wrong rank, not a raid, a subgroup outside 1-8, an unknown member, a full subgroup without `swap_with` | the member's row shows the new subgroup (and the swap target its old one) |

The core drops a raid packet it refuses without an answer (a subgroup outside 0..7, a sender neither leader nor
assistant, a full subgroup, `GroupHandler.cpp:519-526`), so a readback that never comes is the error
`convert_raid: the server did not apply it within 5s (the core drops a refused raid command silently)`. The wait is
the group layer's `waitGroupConfirm` loop, split into `waitGroupReadback` with a per-call refusal phrase.

`raidMove` follows the Turtle daemon's `session.RaidMove` (`internal/session/groupctl.go:193`): resolve the member by
guid or by name (this character counts), refuse a `swap_with` that does not name a member of the target subgroup in
another subgroup, refuse a subgroup of five without `swap_with`, and send the 0-based wire subgroup
(`uint8(subgroup-1)`). `convertRaid` with `Raid` already set is done without a packet.

## The serve methods (`world/serve_session_methods.go`)

| Method | Params | Result |
|---|---|---|
| `convert_raid` | `{}` | `{}` |
| `raid_move` | `guid?` \| `name?`, `subgroup` (1-8), `swap_with?` | `{}` |

Both leave `notPortedMethods`; `serveclient` gains `Client.ConvertRaid(ctx)` and `Client.RaidMove(ctx,
serveclient.RaidMoveParams)`, and the README's method list names both.

## Tests

`internal/forever/world/serve_raid_test.go`, against the fake world over real sockets:

- `TestServeConvertRaidSendsAndReadsBack`: a party of three converts; the fake saw `Raid` true and answers the raid
  roster; `party` reports `raid`.
- `TestServeConvertRaidRefusesAlone`: a one-member roster ("the group has 1 member(s)") and a non-leader are
  refused, neither sending a packet.
- `TestServeRaidMoveFullSubgroupNeedsSwap`: a plain move sends `CMSG_CHANGE_SUB_GROUP` with the 0-based wire
  subgroup (Botsix to subgroup 3 sends 2) and reads back; subgroup 1 of five refuses without a packet; `swap_with`
  sends `CMSG_SWAP_SUB_GROUPS` and both rows trade.
- `TestServeRaidMoveSwapSendsSwap`: the swap packet names the moving member first, then its target; a swap target
  outside the target subgroup, a swap with itself, a party (not a raid) and a member that neither leads nor assists
  are refused with nothing sent.
- `TestServeRaidMoveNoReadbackFails`: a silent server leaves the roster unchanged, so the call fails "the server did
  not apply it" (the packet still went out); `groupConfirmWait` is shortened for the test.

`TestServeGroupMethodTable` flipped: `convert_raid` and `raid_move` are in the ported set, and both refuse outside a
group by name. `TestGroupControlOpcodesTranslate` (`party/control_test.go`) now covers the three raid opcodes'
client wire numbers too.

The fake's side: `parseFakeRaidConvert`/`parseFakeRaidChange`/`parseFakeRaidSwap`, the core's rank and capacity rules
in `fakeChangeSubgroupLocked`/`fakeSwapSubgroupsLocked`, `groupSelfOnly` for the one-member roster, and the seen
slices `groupSeenConvert`, `groupSeenChange`, `groupSeenSwap`.

## Mutations this round would catch

- Success before the roster (no readback): `TestServeRaidMoveNoReadbackFails`.
- The wire subgroup off by one: `TestServeRaidMoveFullSubgroupNeedsSwap` asserts the fake saw 2 for subgroup 3.
