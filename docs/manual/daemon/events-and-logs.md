---
type: Method Reference
title: Events and logs
description: Read the combat log, the session's ordered event stream and the chat log, with cursors and long-poll waits.
tags: [daemon, events, logs, combat, chat]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:07Z }
sources:
  - id: combatlog
    resource: internal/forever/world/combatlog.go
  - id: eventlog
    resource: internal/forever/world/eventlog.go
  - id: event-kinds
    resource: internal/events/log.go
  - id: serve-events
    resource: internal/forever/world/serve_session_methods.go
  - id: chat-log
    resource: internal/forever/chat/chat.go
---

# What this is

The session records what happened three ways:

| Method | What it reads | Ring size | Page max |
|---|---|---|---|
| `events` | The combat log: one row per combat packet effect | 4,096 rows | 1,000 |
| `event_log` | One ordered stream of every event: chat, system, party, combat, loot, quests, ... | 20,000 events | 1,000 |
| `chat_log` | The chat lines the session saw | The session's chat log | 5,000 |

All three are cursored by `since`: pass the `next` of the previous page and you never read a row twice.
Both rings survive a reconnect: a dropped session's rings are handed to the session that replaces it, so a
cursor never goes back and history before the drop stays readable.

Rows carry two clocks. `time` is the wall clock. `mono_ms` is the process's steady clock, the axis a client
cannot step; `status.mono_ms`, `status.base_time` and `status.wall_offset_ms` let you convert between them.

# events

The combat log: one row per packet effect the handlers decode, never per packet. It is the same ring
`dungeon-watch` and the trials runner read.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `since` | int | `0` | Return rows with a `seq` above this. |
| `limit` | int | `1000` | Page size; `0` or above 1,000 means 1,000. Negative is refused. |
| `wait_ms` | int | `0` | Long-poll: wait up to this long for a row when the page would be empty. `0` to `2000`. |

The result:

| Field | Meaning |
|---|---|
| `events` | The rows, oldest first. |
| `next` | The seq to pass as `since` next time. |
| `oldest` | The lowest seq the ring still holds (`0` when empty). |
| `dropped` | Rows that fell out of the ring above `since`; above zero means this page has a hole. |

A row:

| Field | Meaning |
|---|---|
| `seq`, `kind`, `time`, `mono_ms` | Identity, kind, wall clock, steady clock. |
| `source`, `target` | `{guid, name}` for each side; the name is empty when the client does not know it. |
| `spell_id`, `amount` | Spell and amount. |
| `reason` | Why the row did not land as usual: a spell failure, a victim state, the environmental damage type. |
| `spell_name` | The server's own name for `spell_id`, when it answered the lookup. |
| `interrupted_spell_id` | On an interrupt row: the cast that was stopped. |
| `absorbed_spell_id` | On an absorb row: the incoming spell the shield stopped. |
| `dispelled` | On a dispel row: `[{spell_id, harmful}]`, the auras removed. |
| `remaining_ms` | On an `aura_applied` row: how long the aura had left. |
| `sample` | The advanced combat log's unit block: `{health, power, power_type}`. |
| `pos`, `self`, `horz_speed`, `vert_speed`, `dir_x`, `dir_y`, `air_ms`, `drop`, `simulated` | Knockback rows: takeoff or landing, whether it was the mover's own packet, the speeds and direction, and the landing's air time and fall. `simulated` marks a client-computed landing the server never stated. |

Kinds:

| Kind | Written when |
|---|---|
| `spell_start`, `spell_go`, `spell_fail` | A cast starts, goes off, or fails (`reason` names the failure). |
| `melee`, `spell_damage`, `periodic`, `heal` | Damage and healing. |
| `aura_applied`, `aura_removed` | An aura appears or expires. |
| `death` | A unit's health reached zero. |
| `miss`, `resist`, `immune`, `absorb`, `interrupt`, `dispel` | Outcomes that are not ordinary damage. |
| `instakill`, `environmental` | A kill or an environmental damage row. |
| `knockback`, `knockback_land` | A knockback takeoff and its landing. |
| `hostile`, `alert` | The core's AI reaction: a unit turns hostile, or alerts. |

The 1,000-row cap is per call; keep paging while `next` moves. `wait_ms` is spent outside the session call
queue, on the ring's own new-row signal, so an empty long-poll never blocks `status`, a chat or a logout, and
several long-polls can wait at once. `events` still answers while the link is reconnecting.

```json
{"jsonrpc":"2.0","id":5,"method":"events","params":{"since":0,"limit":2}}
```

# event_log

The session's one ordered stream: chat, whispers, system and GM output, party, guild, addon messages, combat
rows (copied), loot, quests, vendors, trainers, taxi, mail, trades, death and the link's own state. Use it
when you want one seq axis instead of joining `chat_log` with `events`.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `since` | uint | `0` | Return events with a `seq` above this. |
| `limit` | int | `1000` | Page size; `0` or above 1,000 means 1,000. Negative is refused. |
| `wait_ms` | int | `0` | Long-poll for the next event. `0` to `2000`. |

The result:

| Field | Meaning |
|---|---|
| `events` | The events, oldest first. |
| `next` | The seq to pass as `since` next time. |
| `more` | Another page follows. |
| `dropped` | Events that fell out of the ring above `since`. |
| `epoch` | The log's life: a daemon restart is a new log with a new epoch. |
| `reset` | The cursor was above the log's seq (a restart): the page starts at the beginning of the new log. |

An event row: `seq`, `time`, `mono_ms`, `kind`, `source`, `source_guid`, `target`, `target_guid`, `spell_id`,
`amount`, `crit`, `party`, `text`, `data`. A GUID is `0x` plus 32 hex digits; a zero one is absent.

`kind` values, by family:

| Kind | Meaning |
|---|---|
| `session` | The link and the character: `data.state` is `in_world`, `map_change`, `falling`, `fall_land`, `fall_cancelled`, `disconnected` or `reconnected`. |
| `chat`, `whisper` | Chat lines and whispers, in and out. |
| `system` | System messages, GM command output and notifications. |
| `party`, `guild`, `raid_mark` | Party packets and guild invites and results; `raid_mark` is a raid icon set or cleared (`data.state` is `single` or `list`). A `guild` row's `text` is `invite`, `joined`, `result`, `declined`, `expired`, `signed` or `sign_refused`; the offer answers carry `data.state` too, and a signature result carries `data.item` and `data.error`. This core sends no `declined` or `expired` invite packet; those two rows appear only if a server sends one. |
| `addon` | An add-on message: prefix, text and sender. |
| `spell_start`, `spell_go`, `spell_fail`, `melee`, `spell_damage`, `periodic`, `heal`, `aura_applied`, `aura_removed`, `death`, `miss`, `resist`, `immune`, `absorb`, `interrupt`, `dispel`, `instakill`, `environmental`, `knockback`, `knockback_land`, `hostile`, `alert` | The combat ring's kinds, copied with the same sides, spell and amount. `data.reason` carries the failure or victim state; the two knockback rows carry the position, speeds and air time. (`hostile` and `alert` come from SMSG_AI_REACTION, which writes no combat row.) |
| `summon_request` | A portal offers to summon the player: `data.summoner`, `area` and `reason`. |
| `loot_roll_start`, `loot_roll`, `loot_roll_won`, `loot_all_passed`, `loot_open`, `loot_error`, `loot_taken`, `loot_closed` | Group rolls and the loot window. |
| `channel_notify` | Channel join, leave, kick and password answers. |
| `gossip`, `gossip_complete` | An NPC's menu, and its close. |
| `gameobject` | A game object changed: `data.change` is `state`, `flags`, `dyn_flags`, `animation`, `reset`, `spawn` or `destroyed`. |
| `vendor_list`, `vendor_buy`, `vendor_buy_failed`, `vendor_sell`, `vendor_sell_failed` | Vendor lists and purchases. |
| `trainer_list`, `trainer_trained`, `trainer_failed`, `spell_learned`, `spell_removed`, `spell_superseded` | Trainer and spellbook changes. |
| `cooldown_start`, `cooldown_end`, `cooldown_clear`, `combo_points` | The agent's own spell cooldowns and combo points. |
| `quest_list`, `quest_details`, `quest_request_items`, `quest_offer_reward`, `quest_accepted`, `quest_rewarded`, `quest_abandoned`, `quest_error`, `quest_shared`, `quest_share_declined`, `quest_push_result` | Quest panels, shares, push results and outcomes. |
| `taxi_reply`, `taxi_node`, `taxi_flight` | Flight-master answers, new nodes, takeoff and landing (`data.state`). |
| `trade_status` | A step of a trade window (`data.status`). |
| `pet_summoned`, `pet_died`, `pet_revived`, `pet_dismissed`, `pet_error` | The player's pet. |
| `mail_list`, `mail_result`, `mail_new` | Mailbox list, command results, and the new-mail notice. |
| `auction_hello`, `auction_buckets`, `auction_items`, `auction_owned`, `auction_quote`, `auction_result`, `auction_won`, `auction_outbid`, `auction_owner_bid`, `auction_closed` | Auction house answers and notifications. |
| `bank` | A bank method's outcome: `data.command` is `list`, `deposit` or `withdraw`; `ok` and `reason` say whether it stood. `bank_buy_slot` writes none (it refuses before sending). |

The ring outlives a reconnect, and a read is answered under the ring's own lock, so `event_log` still works
while reconnecting, while an `act`, a walk or a follow step holds the session goroutine.

Cursor across a restart: keep reading with your last `next`. When the daemon restarted, the new log's seq
begins again at 1, so your cursor is above it; the answer then has `reset: true` and starts at the beginning
instead of staying empty forever.

# chat_log

The chat lines the session saw (say, party, yell, whisper, emote, channel, monster speech, addon).

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `since` | uint | `0` | Return lines with a `seq` above this. |
| `limit` | int | `500` | Page size, at most `5000`. |
| `wait_ms` | int | `0` | Long-poll for a line. `0` to `25000`. |

Result: `events`, `next`, `oldest`, `dropped`, `more`.

A row: `seq`, `kind` (`chat`), `type` (a `CHAT_MSG_*` name such as `CHAT_MSG_SAY`, `CHAT_MSG_PARTY`,
`CHAT_MSG_WHISPER`, `CHAT_MSG_EMOTE`, `CHAT_MSG_CHANNEL`, `CHAT_MSG_ADDON`), `source` (sender name or GUID
text), `channel`, `text`.

`chat_log`'s wait polls the session's own packet loop, so a long wait holds other calls. Keep `wait_ms`
short — for long waits use `event_log`, whose `chat` and `whisper` rows carry the same lines without holding
the queue.

Sending chat is on [chat](../player-actions/chat.md).

# gm_combatlog

Not a log reader: it starts or stops the **core's** own GM combat-log watch
(`.gm combatlog [group|off]`), for a unit or the group.

| Parameter | Type | Meaning |
|---|---|---|
| `guid`, `name` | GUID / string | The unit to watch. Required for `unit` and `group`. |
| `mode` | string | `unit`, `group` or `off`. |

Result: `mode`, `watching` (only when the core answered `Combat log watch on ...`), `text` (the core's line).
A core older than 0.1.38 answers "no such command"; that is error `-32003` with `data.code`
`unsupported-core`. It runs a GM line, so the session's GM guard applies, but it does not act on an
allowlisted unit: `--allow-gm-units` is not needed.

# Examples

```sh
# page the combat log from the beginning
forever-agent forever rpc ./slot events '{"since":0,"limit":100}'

# wait up to two seconds for the next event, then continue from next
forever-agent forever rpc ./slot event_log '{"since":1234,"wait_ms":2000}'

# the last 50 chat lines
forever-agent forever rpc ./slot chat_log '{"since":0,"limit":50}'
```

A polling loop in shell, using `next`:

```sh
since=0
while sleep 1; do
  page=$(forever-agent forever rpc --raw ./slot event_log "{\"since\":$since,\"wait_ms\":1000}")
  since=$(printf '%s' "$page" | sed -n 's/.*"next":\([0-9]*\).*/\1/p')
  printf '%s\n' "$page"
done
```

# Common problems

| Symptom | Cause and fix |
|---|---|
| `events: limit must not be negative` / `wait_ms must be 0 to 2000` | Fix the parameter; `event_log` has the same bounds. |
| `dropped` is above zero | Your cursor fell out of the ring. Continue from `oldest` (or with the returned `next`) and accept the hole. |
| `event_log` answers `reset: true` | The daemon restarted since your cursor. Continue from the page's `next`. |
| `chat_log: wait_ms must be 0 to 25000` | Bound the wait; prefer `event_log` for long waits. |
| `events` is empty in a fight | The character's packets are not combat rows yet, or `since` is already past them. Read `oldest`. |
| `gm_combatlog` answers `unsupported-core` | The core has no `.gm combatlog`; it needs 0.1.38 or later. |

# Related

- [RPC basics](rpc-basics.md), [Method index](method-index.md)
- [Status and world reads](status-and-world-reads.md): the clock fields
- [Chat](../player-actions/chat.md)
- [Traces and records](../testing/traces-and-records.md): what a run writes to disk
- [Stop reasons](../dungeons/stop-reasons.md): what a run's stop reasons mean
