---
type: Reference
title: Method index
description: Every method a forever serve daemon registers, with its parameters, what it does and the page that explains it.
tags: [daemon, methods, reference, rpc]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:07Z }
sources:
  - id: method-table
    resource: internal/forever/world/serve_session_methods.go
  - id: method-table-rpc
    resource: internal/forever/world/serve_rpc_methods.go
  - id: method-groups
    resource: internal/forever/world/serve_item_methods.go
  - id: method-groups-npc
    resource: internal/forever/world/serve_npc_methods.go
  - id: method-groups-quest
    resource: internal/forever/world/serve_quest_methods.go
  - id: method-groups-mail
    resource: internal/forever/world/serve_mail_methods.go
  - id: method-groups-auction
    resource: internal/forever/world/serve_auction_methods.go
  - id: method-groups-bank
    resource: internal/forever/world/serve_bank_methods.go
  - id: method-groups-dungeon
    resource: internal/forever/world/serve_dungeon_methods.go
  - id: method-plan-check
    resource: internal/forever/world/serve_plan_check.go
  - id: method-bot-view
    resource: internal/forever/world/serve_bot_view.go
  - id: method-tbm
    resource: internal/forever/world/serve_tbm_methods.go
---

# What this is

One table row per method the daemon registers, grouped the way the command-line tools use them. `Params`
lists the JSON fields the method reads; a method with `{}` reads none (any field is refused with `-32602`).
`forever rpc DIR list` prints the same names from the running server.

The older daemon's names are registered too: an implemented one is a row here, a refused one answers
`-32003 <method>: <reason>` and is listed on [methods the daemon refuses](not-ported-methods.md). An old caller
never gets an empty success.

Call shape, error codes and the `rpc` command are on [RPC basics](rpc-basics.md).

# Link and session

| Method | Params | What it does | Page |
|---|---|---|---|
| `status` | `{}` | Where the character is, its state, and the link's lifecycle | [Status](status-and-world-reads.md#status) |
| `logout` | `{}` | Full protocol logout, then the daemon stops | [forever serve](serve.md#stopping-it) |
| `teardown` | `gm`, `dry_run` | Step-by-step end of the session (GM line, logout) | [forever serve](serve.md#stopping-it) |
| `act` | `steps`, `timeout_ms` | Run a scripted `act` step list | [Act steps](../player-actions/act-steps.md) |

# World reads

| Method | Params | What it does | Page |
|---|---|---|---|
| `units` | `within`, `near`, `entry`, `name`, `hostile`, `owner`, `limit` | Units in view, nearest first | [Status](status-and-world-reads.md#units) |
| `unit` | `guid`, `name` | One unit row | [Status](status-and-world-reads.md#unit-and-target) |
| `target` | `guid`, `name` | Select a unit on the server | [Status](status-and-world-reads.md#unit-and-target) |
| `gameobjects` | `within`, `radius`, `entry`, `type`, `name`, `limit` | Game objects in view | [Status](status-and-world-reads.md#gameobjects) |
| `dynamic_objects` | `{}` | Ground spells in view | [Status](status-and-world-reads.md#dynamic_objects) |
| `item_info` | `entry` or `entries`, `wait_ms` | Item data for up to 64 entries | [Status](status-and-world-reads.md#item_info) |
| `map_snapshot` | `floor`, `plan`, `floors`, `background`, `html`, `pad` | The run so far as an SVG | [Status](status-and-world-reads.md#map_snapshot) |
| `combat_near` | `x`, `y`, `z`, `radius`, `window_s`, `who` | Who hit mobs near a point recently | [Status](status-and-world-reads.md#combat_near) |
| `death_status` | `{}` | Death phase and corpse | [Status](status-and-world-reads.md#death_status) |
| `inv` | `{}` | The character's inventory and money | [Items](../player-actions/items-and-inventory.md) |
| `inventory` | `bags`, `names` | Inventory rows, optionally bag contents and names | [Items](../player-actions/items-and-inventory.md) |
| `equipment` | `names` | Equipped items | [Items](../player-actions/items-and-inventory.md) |
| `spells` | `spell_id`, `name`, `highest`, `passive` | The spellbook with cooldowns | [Spells](../player-actions/spells-and-combat.md) |
| `cast_state` | `{}` | Current cast, GCD, last cast, combo points | [Spells](../player-actions/spells-and-combat.md) |
| `party` | `{}` | Party or raid roster, loot settings, marks | [Groups](../player-actions/groups-raids-and-guild.md) |
| `marks` | `{}` | Raid target icons in force | [Groups](../player-actions/groups-raids-and-guild.md) |
| `follow_status` | `{}` | The follow loop's state | [Movement](movement.md#follow_start-follow_stop-follow_status) |

# Movement and travel

| Method | Params | What it does | Page |
|---|---|---|---|
| `walk_to` | `x`, `y`, `z` | One straight run | [Movement](movement.md#walk_to) |
| `path_to` | `x`, `y`, `z` | Plan a navmesh route, move nothing | [Movement](movement.md#path_to) |
| `walk_path` | `x`, `y`, `z` | Plan and walk a navmesh route | [Movement](movement.md#walk_path) |
| `walk_ray` | `x`, `y`, `z`, `from` | Navmesh walkability of a straight line | [Movement](movement.md#walk_ray) |
| `face` | `guid`, `name` | Turn toward a unit | [Movement](movement.md#face) |
| `follow_start` | `lead`, `near`, `far`, `lost_s`, `tick_ms` | Start a follow loop | [Movement](movement.md#follow_start-follow_stop-follow_status) |
| `follow_stop` | `{}` | Stop the follow loop and answer its result | [Movement](movement.md#follow_start-follow_stop-follow_status) |
| `teleport` | `x`, `y`, `z`, `map`, `require_ground` | `.go xyz` and the fall after it | [Travel](travel.md#teleport) |
| `area_trigger` | `id` | Enter an area trigger | [Travel](travel.md#area_trigger) |
| `area_trigger_watch` | `id`, `map`, `x`, `y`, `z`, `radius`, `clear` | Send the trigger on the walk's own crossing | [Travel](travel.md#area_trigger_watch) |
| `mount` | `spell`, `entry` | Mount by spell or mount item | [Travel](travel.md#mount-and-dismount) |
| `dismount` | `{}` | Cancel the mount aura | [Travel](travel.md#mount-and-dismount) |
| `taxi` | `guid`, `node`, `nodes`, `wait_landing`, `wait_ms` | Flight window, or one flight | [Travel](travel.md#taxi) |

# Combat and spells

| Method | Params | What it does | Page |
|---|---|---|---|
| `attack` | `guid`, `name`, `for_ms` | Select and fight a target | [Spells](../player-actions/spells-and-combat.md) |
| `stop_attack` | `{}` | Stop the running fight | [Spells](../player-actions/spells-and-combat.md) |
| `cast` | `spell_id`, `target`, `trade_slot` | Cast a spell, or a trade-slot enchant | [Spells](../player-actions/spells-and-combat.md) |
| `cancel_cast` | `spell_id` | Cancel the current cast | [Spells](../player-actions/spells-and-combat.md) |

# Items

| Method | Params | What it does | Page |
|---|---|---|---|
| `equip` | `item_entry`, `bag`, `slot` | Equip or swap an item | [Items](../player-actions/items-and-inventory.md) |
| `use_item` | `item_entry`, `bag`, `slot`, `target`, `spell_id` | Use an item | [Items](../player-actions/items-and-inventory.md) |

# The bank

| Method | Params | What it does | Page |
|---|---|---|---|
| `bank_list` | `guid`, `wait_ms` | Open the banker and read the tab bags (player slots 63-71) and their contents | [The bank](../player-actions/items-and-inventory.md#the-bank) |
| `bank_deposit` | `guid`, `item_entry` or `bag`+`slot`, `wait_ms` | Move one carried item into the bank | [The bank](../player-actions/items-and-inventory.md#the-bank) |
| `bank_withdraw` | `guid`, `item_entry` or `bag`+`slot` (bank positions), `wait_ms` | Move one item from the bank into the bags | [The bank](../player-actions/items-and-inventory.md#the-bank) |
| `bank_buy_slot` | `guid` | Always refused before any packet: the client cannot read the tab count the price comes from | [The bank](../player-actions/items-and-inventory.md#the-bank) |

# Loot and game objects

| Method | Params | What it does | Page |
|---|---|---|---|
| `loot` | `guid`, `name`, `wait_ms` | Open a corpse's loot window | [Quests and loot](../player-actions/quests-and-loot.md) |
| `loot_take` | `slot`, `all`, `money`, `wait_ms` | Take slots and money | [Quests and loot](../player-actions/quests-and-loot.md) |
| `loot_close` | `wait_ms` | Release the loot window | [Quests and loot](../player-actions/quests-and-loot.md) |
| `open_object` | `guid`, `spell_id`, `take`, `wait_ms` | Open a chest or door, optionally take all | [Quests and loot](../player-actions/quests-and-loot.md) |
| `use_object` | `guid`, `wait_ms` | Use a game object | [Quests and loot](../player-actions/quests-and-loot.md) |
| `roll` | `looted`, `item_slot`, `roll_id`, `choice` (or its alias `vote`) | Need, greed or pass a group roll | [Quests and loot](../player-actions/quests-and-loot.md#roll) |

# NPCs, vendors and gossip

| Method | Params | What it does | Page |
|---|---|---|---|
| `vendor_list` | `guid`, `wait_ms` | A vendor's list | [Vendors](../player-actions/vendors-and-gossip.md) |
| `buy` | `guid`, `item_entry`, `muid`, `count`, `wait_ms` | Buy from a vendor | [Vendors](../player-actions/vendors-and-gossip.md) |
| `sell` | `guid`, `item_entry`, `bag`, `slot`, `count`, `force`, `wait_ms` | Sell to a vendor | [Vendors](../player-actions/vendors-and-gossip.md) |
| `gossip_hello` | `guid`, `wait_ms` | Open an NPC's gossip menu | [Vendors](../player-actions/vendors-and-gossip.md) |
| `gossip_select` | `guid`, `option`, `code`, `wait_ms` | Pick a gossip option | [Vendors](../player-actions/vendors-and-gossip.md) |

# Quests

| Method | Params | What it does | Page |
|---|---|---|---|
| `quest_hello` | `guid`, `wait_ms` | Open a quest giver's menu | [Quests and loot](../player-actions/quests-and-loot.md) |
| `quest_query` | `guid`, `quest_id`, `wait_ms` | Read one quest's details | [Quests and loot](../player-actions/quests-and-loot.md) |
| `quest_accept` | `guid`, `quest_id`, `wait_ms` | Accept a quest | [Quests and loot](../player-actions/quests-and-loot.md) |
| `quest_complete` | `guid`, `quest_id`, `wait_ms` | Hand a quest in | [Quests and loot](../player-actions/quests-and-loot.md) |
| `quest_reward` | `guid`, `quest_id`, `choice`, `wait_ms` | Pick the reward | [Quests and loot](../player-actions/quests-and-loot.md) |
| `quest_abandon` | `quest_id`, `wait_ms` | Abandon a quest | [Quests and loot](../player-actions/quests-and-loot.md) |
| `quest_log` | `{}` | The character's quest log | [Quests and loot](../player-actions/quests-and-loot.md) |

# Death and the corpse run

| Method | Params | What it does | Page |
|---|---|---|---|
| `accept_resurrect` | `{}` | Accept a pending resurrection offer | [Status](status-and-world-reads.md#death_status) |
| `accept_summon` | `accept` | Answer a summon offer | [Status](status-and-world-reads.md#death_status) |
| `release_spirit` | `{}` | Release to the ghost | [Status](status-and-world-reads.md#death_status) |
| `corpse_query` | `{}` | Where the corpse is | [Status](status-and-world-reads.md#death_status) |
| `reclaim_corpse` | `{}` | Reclaim the corpse | [Status](status-and-world-reads.md#death_status) |

# Group, raid and guild

| Method | Params | What it does | Page |
|---|---|---|---|
| `invite` | `name` | Invite a player | [Groups](../player-actions/groups-raids-and-guild.md) |
| `accept_invite` | `{}` | Accept the pending invite | [Groups](../player-actions/groups-raids-and-guild.md) |
| `leave_group` | `{}` | Leave the party | [Groups](../player-actions/groups-raids-and-guild.md) |
| `disband` | `{}` | Disband the group (leader) | [Groups](../player-actions/groups-raids-and-guild.md) |
| `promote` | `name` | Promote a member to leader | [Groups](../player-actions/groups-raids-and-guild.md) |
| `assistant` | `name`, `clear` | Set or clear a raid assistant (a raid only) | [Groups](../player-actions/groups-raids-and-guild.md#party-leader-controls) |
| `convert_raid` | `{}` | Convert the party to a raid | [Groups](../player-actions/groups-raids-and-guild.md) |
| `raid_move` | `guid`, `name`, `subgroup`, `swap_with` | Move a member to a subgroup | [Groups](../player-actions/groups-raids-and-guild.md) |
| `loot_method` | `method`, `master`, `threshold` | Set the group's loot rule | [Groups](../player-actions/groups-raids-and-guild.md) |
| `mark` | `guid`, `name`, `icon` | Set a raid target icon | [Groups](../player-actions/groups-raids-and-guild.md) |
| `accept_guild_invite` | `{}` | Accept a guild invite | [Groups](../player-actions/groups-raids-and-guild.md) |
| `decline_guild_invite` | `{}` | Decline a guild invite | [Groups](../player-actions/groups-raids-and-guild.md) |
| `guild_leave` | `{}` | Leave the guild | [Groups](../player-actions/groups-raids-and-guild.md) |

# Dungeon runs

| Method | Params | What it does | Page |
|---|---|---|---|
| `dungeon_run` | `plan`, `lead_bot` | Start a module-led run (`.tb run start <lead> <plan>`); the CLI's start preflight runs first (plan, slot and lease, module link, the lead via `.pinfo`) and a failed check refuses with its lines, sending nothing | [dungeon-run](../dungeons/dungeon-run.md) |
| `dungeon_status` | `lead_bot` | The module's run records; no lead reads every run | [dungeon-watch](../dungeons/dungeon-watch.md) |
| `dungeon_stop` | `lead_bot`, `reason` | Stop a run (`.tb run stop`) | [dungeon-watch](../dungeons/dungeon-watch.md) |
| `dungeon_pause` | `lead_bot` | Pause a run (`.tb run pause`) | [dungeon-watch](../dungeons/dungeon-watch.md) |
| `dungeon_resume` | `lead_bot` | Resume a run (`.tb run resume`) | [dungeon-watch](../dungeons/dungeon-watch.md) |
| `plan_check` | `name`, `from_leg`, `from_pull_group` | Check a plan from the serve's `--plans-dir` over its `--navmesh`; without one the findings say `skipped` with the reason | [Plan tools](../dungeons/plan-tools.md) |

# Trade

| Method | Params | What it does | Page |
|---|---|---|---|
| `trade` | `to`, `items`, `gold`, `wait_ms`, `open_wait_ms` | Open a trade and fill it | [Trade](../player-actions/trade.md) |
| `trade_open` | `to`, `open_wait_ms` | Open a trade window | [Trade](../player-actions/trade.md) |
| `trade_expect` | `from`, `wait_ms` | Wait for the other side's window | [Trade](../player-actions/trade.md) |
| `trade_set_item` | `trade_slot`, `item_entry`, `bag`, `slot`, `clear` | Put an item in a trade slot | [Trade](../player-actions/trade.md) |
| `trade_set_gold` | `gold` | Put gold in the window | [Trade](../player-actions/trade.md) |
| `trade_accept` | `wait_ms` | Accept the trade | [Trade](../player-actions/trade.md) |
| `trade_cancel` | `{}` | Cancel the trade | [Trade](../player-actions/trade.md) |
| `trade_status` | `{}` | The trade window's state | [Trade](../player-actions/trade.md) |

# Mail and the auction house

| Method | Params | What it does | Page |
|---|---|---|---|
| `mail_list` | `guid`, `wait_ms` | A mailbox's mails | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_read` | `guid`, `mail_id`, `wait_ms` | Read one mail | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_take_item` | `guid`, `mail_id`, `index`, `force`, `wait_ms` | Take an attachment | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_take_money` | `guid`, `mail_id`, `wait_ms` | Take the money the mail holds | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_delete` | `guid`, `mail_id`, `force`, `wait_ms` | Delete a mail | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_return` | `guid`, `mail_id`, `wait_ms` | Return a mail | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_send` | `guid`, `to`, `subject`, `body`, `money`, `cod`, `attachments`, `force`, `wait_ms` | Send a mail | [Mail and auction](../player-actions/mail-and-auction.md) |
| `mail_status` | `wait_ms` | Next-mail timer and senders of unread mail | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_hello` | `guid`, `wait_ms` | Open an auctioneer | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_browse` | `guid`, `name`, `min_level`, `max_level`, `offset`, `wait_ms` | Browse auctions | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_items` | `guid`, `item_id`, `item_level`, `offset`, `wait_ms` | List one item's auctions | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_owned` | `guid`, `offset`, `wait_ms` | The character's auctions | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_sell` | `guid`, `item_entry`, `bag`, `slot`, `count`, `commodity`, `min_bid`, `buyout`, `unit_price`, `hours`, `force`, `wait_ms` | Post an auction | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_bid` | `guid`, `auction_id`, `amount`, `force`, `wait_ms` | Bid on an auction | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_cancel` | `guid`, `auction_id`, `item_id`, `force`, `wait_ms` | Cancel an own auction | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_quote` | `guid`, `item_id`, `quantity`, `wait_ms` | Commodity quote | [Mail and auction](../player-actions/mail-and-auction.md) |
| `auction_buy` | `guid`, `item_id`, `quantity`, `force`, `wait_ms` | Buy the quantity the last commodity quote named | [Mail and auction](../player-actions/mail-and-auction.md) |

# Trainers, flight paths and pets

| Method | Params | What it does | Page |
|---|---|---|---|
| `trainer_list` | `guid`, `wait_ms` | A trainer's list | [Trainers](../player-actions/trainers-and-flight-paths.md) |
| `train` | `guid`, `spell_id`, `wait_ms` | Train one spell | [Trainers](../player-actions/trainers-and-flight-paths.md) |
| `pet` | `{}` | The player's pet and its action bar | [Pets](../player-actions/pets.md) |
| `pet_action` | `action`, `target`, `slot`, `spell_id`, `name` | One pet action | [Pets](../player-actions/pets.md) |

# Chat and channels

| Method | Params | What it does | Page |
|---|---|---|---|
| `chat` | `type`, `text`, `to` | Say, party, yell, whisper, emote, channel | [Chat](../player-actions/chat.md) |
| `join_channel` | `name`, `password` | Join a chat channel | [Chat](../player-actions/chat.md) |
| `leave_channel` | `name` | Leave a chat channel | [Chat](../player-actions/chat.md) |
| `channel_list` | `name` | List a channel's members | [Chat](../player-actions/chat.md) |
| `addon_send` | `channel`, `prefix`, `text`, `to` | Send an add-on message | [Add-on protocol](../bots-and-raids/addon-protocol.md) |
| `chat_log` | `since`, `limit`, `wait_ms` | Chat lines | [Events and logs](events-and-logs.md#chat_log) |

# The add-on host

| Method | Params | What it does | Page |
|---|---|---|---|
| `addon_start` | `dir`, `clock`, `prefixes` | Load a real add-on headless in a `lua5.1` sidecar, register its prefixes and feed it the session | [Run an add-on headless](../bots-and-raids/addon-host.md) |
| `addon_state` | `{}` | What the add-on sent, received, raised and printed, and its `throttled` flag | [Run an add-on headless](../bots-and-raids/addon-host.md) |
| `addon_tick` | `ms` | Advance a `manual` clock by `ms` (100 when omitted) and run `OnUpdate` once | [Run an add-on headless](../bots-and-raids/addon-host.md) |
| `addon_stop` | `{}` | Close the sidecar and unregister only the add-on's prefixes | [Run an add-on headless](../bots-and-raids/addon-host.md) |

# Events

| Method | Params | What it does | Page |
|---|---|---|---|
| `events` | `since`, `limit`, `wait_ms` | The combat log | [Events and logs](events-and-logs.md#events) |
| `event_log` | `since`, `limit`, `wait_ms` | The session's ordered event stream | [Events and logs](events-and-logs.md#event_log) |
| `gm_combatlog` | `guid`, `name`, `mode` | The core's GM combat-log watch | [Events and logs](events-and-logs.md#gm_combatlog) |

# GM and test bench

| Method | Params | What it does | Page |
|---|---|---|---|
| `gm` | `text` | One raw GM command line | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `gm_revive` | `name` | `.revive <name>` | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `gm_kill` | `guid`, `name` | `.die` on an allowlisted creature | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `gm_set_hp` | `guid`, `hp`, `current_only` | `.modify hp` or `.modify currenthp` | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `gm_aura` | `guid`, `spell`, `remove` | `.aura` / `.unaura` | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `gm_evade` | `guid`, `name` | `.npc evade` on an allowlisted unit | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `gm_respawn` | `guid`, `name` | `.respawn` on an allowlisted unit | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |
| `trial_units` | `guids` | Replace the GM hands' allowlist | [Safety and GM guards](../getting-started/safety-and-gm-guards.md) |

# Bot module

| Method | Params | What it does | Page |
|---|---|---|---|
| `bot` | `line`, `wait_ms` | One `.bot` or `.tb` command line | [Bot control](../bots-and-raids/bot-control.md) |
| `commander` | `op`, `lead_bot`, `bots`, `verb`, `args`, `wait_ms`, `pause_run`, `leave_party`, `on` | Bind, unbind, order or run the bot module's leader | [Bot control](../bots-and-raids/bot-control.md) |
| `bot_view` | `bots`, `wait_ms` | The party roster joined to the world model, one row per bot (`in_party`, `online`, `dead`, `in_view`, and the unit row while in view); `wait_ms` (at most 2 s) lets a just-sent unit land first | [Bot control](../bots-and-raids/bot-control.md) |
| `tbm_handshake` | `transport`, `wait_ms` | The TBM hello: the roster probe, then the trailer and the transport it switches to | [Add-on protocol](../bots-and-raids/addon-protocol.md) |
| `tbm_send` | `command`, `transport`, `wait_ms` | One TBM command and its answer, off the session queue (`wait_ms` 5 s by default, capped at 15 s); one exchange runs at a time | [Add-on protocol](../bots-and-raids/addon-protocol.md) |
| `tbm` | `command`, `via`, `wait_ms` | `tbm_send` under the old name (`via` is accepted as the transport) | [Add-on protocol](../bots-and-raids/addon-protocol.md) |

# Refused

No method is "not ported" any more: an old name that has no Forever meaning answers
`-32003 <method>: <reason>`, and the reason names what to use. The refused names and their reasons are on
[methods the daemon refuses](not-ported-methods.md).

# Related

- [RPC basics](rpc-basics.md), [forever serve](serve.md)
- [Status and world reads](status-and-world-reads.md), [Movement](movement.md),
  [Teleports and travel](travel.md), [Events and logs](events-and-logs.md)
- [Methods the daemon refuses](not-ported-methods.md)
