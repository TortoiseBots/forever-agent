# The serve method table

`forever serve` answers one JSON-RPC method per line of the block below. The block is generated from the daemon's
own registration (`world.ServeRPCMethods`, `world.ServeRefusal`) by
`go test ./internal/forever/world -run TestServeMethodTableDoc -update-method-table`, and the same test fails when
the two drift, so this page cannot go stale. `forever rpc DIR list` prints the same table off the running socket:
an implemented method bare, a refused one as `name (refused: <reason>)`.

There is no "not ported" row left. Every name the old (Turtle) daemon served is either implemented here or refused
with the reason it has no Forever meaning and, where one exists, the command or method that does the job. A refusal
is the typed `-32003` error `<name>: <reason>` (never an empty success, never `-32601`), and its params are not
decoded, so a caller written for the old daemon gets the reason whatever JSON it sends.

## The decisions

The lead's decisions for the old names, one per group:

**The client brain is retired (a).** `brain_start`, `brain_status`, `brain_stop` and the brain's tuning
(`tune_list`, `tune_set`) answer a refusal: on a ForeverCore server the module's bots fight server-side, so there is
no character for a client brain to drive. The CLI's brain surfaces (`rotation`, `pulltest`, `tune-diff`) stay on the
Turtle stack and say so (`forever-agent help` marks them `[Turtle only; Forever: none: Turtle brain]`); the player
character in a `forever dungeon-play` run is driven by the module's own brain.

**The client dungeon finder is not built (b).** `lfg_auto`, `lfg_join`, `lfg_leave`, `lfg_listing_role`,
`lfg_listings`, `lfg_proposal_answer`, `lfg_role_check_answer` and `lfg_status` answer a refusal: the module's own
finder fills the party and queues the dungeon (`forever fb-dungeon`, the module's `.bot lfg` and `.bot dungeon`), so
a client-side LFG protocol is not planned.

**The old Turtle dungeon driver modes stay `[Turtle only]` (c).** The Turtle daemon's `dungeon_run` (its leader,
commander and observe modes with the 1.12 brain and TBM) is Turtle code and is not offered against a Forever
server; the Forever replacements are the commands `forever dungeon-run` (agent-led), `forever dungeon-watch`
(module-led) and `forever dungeon-play`.

**The run verbs and the plan check are implemented (d).** `dungeon_run`, `dungeon_status`, `dungeon_stop`,
`dungeon_pause` and `dungeon_resume` are the module's own run control under the old names, and `plan_check` runs the
same plan check `plan-check --forever` runs. Each reuses the existing code and adds no behaviour:

| Method | The line it sends (through the command channel) |
|---|---|
| `dungeon_run {plan, lead_bot?}` | `.tb run start <lead> <plan>` |
| `dungeon_status {lead_bot?}` | `.tb run status <lead|all>` (no lead: every run the module holds) |
| `dungeon_stop {lead_bot?, reason?}` | `.tb run stop <lead> [reason]` |
| `dungeon_pause {lead_bot?}` | `.tb run pause <lead>` |
| `dungeon_resume {lead_bot?}` | `.tb run resume <lead>` |

A run's party must already be bound to the character (`commander bind`, or the watch's own bring-in): a mutation
names a lead of one of this character's bindings, or the character's only one, and the module's `not-owned` refusal
is passed through. The answer is `{sent, runs}` with the module's run records typed as the `commander` method types
them. `dungeon_skip` and `dungeon_stage` are refused: a module-led run has no client-side step control, and an
agent-led run's start step is `forever dungeon-run --from-leg/--legs`.

`dungeon_run` is the one mutation that starts something, so it makes the CLI's start preflight first, through the
same functions (`internal/forever/fpreflight`) as the module-led command whose `.tb run start` it sends
(`forever dungeon-watch`): the plan `--plans-dir` holds, loaded by `driver.LoadWatchRoute`; the serve slot
`forever serve --dir` wrote, its lease and its port; the module answering `.tb state party` over this session's
command channel; and the called lead, read with `.pinfo` through the serve character's own GM console (the same
command and parser the CLI's SOAP console runs; the daemon is given no SOAP endpoint). The plan export is checked
with no path, so it reports the CLI's own "no --export" warning. A blocked check refuses the call with the failed
lines in the `-32003` message and sends nothing. A daemon started without `--dir` cannot check the slot, the lease
or the module link, so it refuses too, naming `forever dungeon-watch`. The preflight runs off the session queue:
the GM guard, each send and each 50 ms packet slice is its own short queued call, so `status` and the other methods
answer while a slow console or module is waited for. Each read holds the command channel's one-run lock
(`command.Session.Exclusive`) for its whole reply, so no other command runs inside a tokenless `.pinfo` answer's
quiet window: the console's text carries no marker that would tell it from a dashboard `bot` call's reply, and a
stray "Player not found!" would refuse the run. A reply already on the wire from a command that ran before the read
can still fall inside that window (a tokenless run's tail is waited out only for a tokened one), which the code
notes. A `.pinfo` reply that stops before its Account line is reported as cut off, not as a character that does not
exist; a console answer of nothing refuses with the read's bound.

`plan_check {name, from_leg?, from_pull_group?}` walks the plan from the serve's plans folder (`--plans-dir`, default
`dungeons`) on a scratch Forever planner over the serve's `--navmesh` (its own tile cache, so a walk on the session
keeps its tiles). It answers the check's findings (`checked`, `skipped`, `plan`, `map`, `points`, `issues`,
`warnings`, `exempt`, `run_leader`), and leaves the plan unchecked with the reason in `skipped` when the serve has
no `--navmesh` or the map's tiles, exactly as the old daemon did. The world export lints (`--spawns`) are not part
of the daemon's check: no serve flag names a spawns folder, and `plan-check --forever --spawns` runs them.

**The TBM methods are implemented, the add-on host is not (e).** `tbm_handshake`, `tbm_send` and `tbm` are the TBM
client `forever tbm-check` already runs (`internal/forever/tbmclient` over this session's add-on layer): the hello
(roster probe, then the trailer and the transport commands switch to), and one command with its answer. The wait
runs off the session queue: the tap install, every send and each 50 ms packet slice is its own short queued call,
one exchange runs at a time, and `wait_ms` (5 s by default) is capped at 15 s, so `status` and the dungeon verbs
answer while a silent module is waited for. `tbm` is
`tbm_send` under the old name (its old transport parameter `via` is accepted too). `addon_start`, `addon_stop`,
`addon_state`, `addon_tick`, `addon_click`, `tbm_buttons` and `tbm_click` are refused: they need an add-on HOST
(a real add-on running headless), which is a separate, later ticket; the ported half of the add-on layer is
`addon_send`.

**`bot_view` is implemented (f).** The old `bot_view` read the 1.12 module's perception frames. The data already
exists in this daemon: the party roster joined to the world model. `bot_view {bots?, wait_ms?}` answers one snapshot
per call — a row per bot with `in_party`, `online`, `dead`, `in_view` and, when the model holds the unit, the same
unit row `unit` returns (position, health, power, target, cast, auras, combat state). With no `bots` it lists every
party member but this character; `wait_ms` drives the packet loop first (at most 2 s) so a unit the server has just
sent is in the model.

**`tt` is refused.** The Turtle test toolkit drives 1.12 test servers; a Forever test setup uses SOAP
(`forever soap`).

## The table

<!-- method-table:begin (generated: run go test ./internal/forever/world -run TestServeMethodTableDoc -update-method-table) -->
| Method | Status |
|---|---|
| `accept_guild_invite` | implemented |
| `accept_invite` | implemented |
| `accept_resurrect` | implemented |
| `accept_summon` | implemented |
| `act` | implemented |
| `addon_click` | refused: no add-on host on this server: running a real add-on headless is a later ticket, so there is nothing to start, tick, click or read (addon_send is the ported half, the add-on message layer) |
| `addon_send` | implemented |
| `addon_start` | refused: no add-on host on this server: running a real add-on headless is a later ticket, so there is nothing to start, tick, click or read (addon_send is the ported half, the add-on message layer) |
| `addon_state` | refused: no add-on host on this server: running a real add-on headless is a later ticket, so there is nothing to start, tick, click or read (addon_send is the ported half, the add-on message layer) |
| `addon_stop` | refused: no add-on host on this server: running a real add-on headless is a later ticket, so there is nothing to start, tick, click or read (addon_send is the ported half, the add-on message layer) |
| `addon_tick` | refused: no add-on host on this server: running a real add-on headless is a later ticket, so there is nothing to start, tick, click or read (addon_send is the ported half, the add-on message layer) |
| `area_trigger` | implemented |
| `assistant` | implemented |
| `attack` | implemented |
| `auction_bid` | implemented |
| `auction_browse` | implemented |
| `auction_buy` | implemented |
| `auction_cancel` | implemented |
| `auction_hello` | implemented |
| `auction_items` | implemented |
| `auction_owned` | implemented |
| `auction_quote` | implemented |
| `auction_sell` | implemented |
| `bank_buy_slot` | implemented |
| `bank_deposit` | implemented |
| `bank_list` | implemented |
| `bank_withdraw` | implemented |
| `bot` | implemented |
| `bot_view` | implemented |
| `brain_start` | refused: the client brain is retired on Forever: the module's bots fight on the server, so a client brain has nothing to drive |
| `brain_status` | refused: the client brain is retired on Forever: the module's bots fight on the server, so a client brain has nothing to drive |
| `brain_stop` | refused: the client brain is retired on Forever: the module's bots fight on the server, so a client brain has nothing to drive |
| `buy` | implemented |
| `cancel_cast` | implemented |
| `cast` | implemented |
| `cast_state` | implemented |
| `channel_list` | implemented |
| `chat` | implemented |
| `chat_log` | implemented |
| `combat_near` | implemented |
| `commander` | implemented |
| `convert_raid` | implemented |
| `corpse_query` | implemented |
| `death_status` | implemented |
| `decline_guild_invite` | implemented |
| `disband` | implemented |
| `dismount` | implemented |
| `dungeon_pause` | implemented |
| `dungeon_resume` | implemented |
| `dungeon_run` | implemented |
| `dungeon_skip` | refused: a module-led run has no client-side step control: the module owns its run (dungeon_pause, dungeon_resume and dungeon_stop are its verbs), and an agent-led run's start step is forever dungeon-run --from-leg/--legs |
| `dungeon_stage` | refused: a module-led run has no client-side step control: the module owns its run (dungeon_pause, dungeon_resume and dungeon_stop are its verbs), and an agent-led run's start step is forever dungeon-run --from-leg/--legs |
| `dungeon_status` | implemented |
| `dungeon_stop` | implemented |
| `dynamic_objects` | implemented |
| `equip` | implemented |
| `equipment` | implemented |
| `event_log` | implemented |
| `events` | implemented |
| `face` | implemented |
| `follow_start` | implemented |
| `follow_status` | implemented |
| `follow_stop` | implemented |
| `gameobjects` | implemented |
| `gm` | implemented |
| `gm_aura` | implemented |
| `gm_combatlog` | implemented |
| `gm_evade` | implemented |
| `gm_kill` | implemented |
| `gm_respawn` | implemented |
| `gm_revive` | implemented |
| `gm_set_hp` | implemented |
| `gossip_hello` | implemented |
| `gossip_select` | implemented |
| `guild_leave` | implemented |
| `inv` | implemented |
| `inventory` | implemented |
| `invite` | implemented |
| `item_info` | implemented |
| `join_channel` | implemented |
| `leave_channel` | implemented |
| `leave_group` | implemented |
| `lfg_auto` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_join` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_leave` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_listing_role` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_listings` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_proposal_answer` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_role_check_answer` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `lfg_status` | refused: the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon) |
| `logout` | implemented |
| `loot` | implemented |
| `loot_close` | implemented |
| `loot_method` | implemented |
| `loot_take` | implemented |
| `mail_delete` | implemented |
| `mail_list` | implemented |
| `mail_read` | implemented |
| `mail_return` | implemented |
| `mail_send` | implemented |
| `mail_status` | implemented |
| `mail_take_item` | implemented |
| `mail_take_money` | implemented |
| `map_snapshot` | implemented |
| `mark` | implemented |
| `marks` | implemented |
| `mount` | implemented |
| `open_object` | implemented |
| `party` | implemented |
| `path_to` | implemented |
| `pet` | implemented |
| `pet_action` | implemented |
| `plan_check` | implemented |
| `promote` | implemented |
| `quest_abandon` | implemented |
| `quest_accept` | implemented |
| `quest_complete` | implemented |
| `quest_hello` | implemented |
| `quest_log` | implemented |
| `quest_query` | implemented |
| `quest_reward` | implemented |
| `raid_move` | implemented |
| `reclaim_corpse` | implemented |
| `release_spirit` | implemented |
| `roll` | implemented |
| `sell` | implemented |
| `spells` | implemented |
| `status` | implemented |
| `stop_attack` | implemented |
| `target` | implemented |
| `taxi` | implemented |
| `tbm` | implemented |
| `tbm_buttons` | refused: the TBM buttons are the add-on's own UI table, so they need the add-on host (a later ticket); tbm_handshake and tbm_send speak the protocol without one |
| `tbm_click` | refused: the TBM buttons are the add-on's own UI table, so they need the add-on host (a later ticket); tbm_handshake and tbm_send speak the protocol without one |
| `tbm_handshake` | implemented |
| `tbm_send` | implemented |
| `teardown` | implemented |
| `teleport` | implemented |
| `trade` | implemented |
| `trade_accept` | implemented |
| `trade_cancel` | implemented |
| `trade_expect` | implemented |
| `trade_open` | implemented |
| `trade_set_gold` | implemented |
| `trade_set_item` | implemented |
| `trade_status` | implemented |
| `train` | implemented |
| `trainer_list` | implemented |
| `trial_units` | implemented |
| `tt` | refused: the Turtle test toolkit drives 1.12 test servers; a Forever test setup uses SOAP (forever soap) |
| `tune_list` | refused: brain tuning: the client brain is retired on Forever (the module's bots fight on the server), so there is nothing to tune |
| `tune_set` | refused: brain tuning: the client brain is retired on Forever (the module's bots fight on the server), so there is nothing to tune |
| `unit` | implemented |
| `units` | implemented |
| `use_item` | implemented |
| `use_object` | implemented |
| `vendor_list` | implemented |
| `walk_path` | implemented |
| `walk_ray` | implemented |
| `walk_to` | implemented |

<!-- method-table:end -->

## See also

* `forever rpc DIR list` — the same table off the running daemon.
* [../REPORTCARD-R1-NOTES.md](REPORTCARD-R1-NOTES.md), [../WATCH-R1-NOTES.md](WATCH-R1-NOTES.md) — the run records
  the run verbs return.
* [../CMDCTL-C3-NOTES.md](CMDCTL-C3-NOTES.md) — the `commander` method the run verbs share their controller with.
