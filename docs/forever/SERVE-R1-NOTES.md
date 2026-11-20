# Forever serve, round SERVE-1 notes

Scope: `forever serve`, the long-running session daemon for the ForeverCore client. It logs in like `world-check`,
enters the named character, answers time syncs and pings, and serves the same local JSON-RPC protocol the old daemon
serves (`POST /rpc`, one JSON-RPC 2.0 object, bearer token, loopback only). Code: `internal/forever/world/serve.go`,
`serve_session.go`, `serve_session_methods.go`; command: `cmd/forever-agent/forever_serve.go`.

The rules from the one-shot commands hold: the password comes from `--password-file` or `FOREVER_PASSWORD`, is never
printed, and the session uses the same login, the same two sockets, the same movement and the same logout as
`world-check`.

## The socket

* The listener is bound before the login, so a bad or busy address costs no login attempt. The command prints the
  address and the token-file path, never the token.
* Authentication is the old daemon's: `Authorization: Bearer <token>`. `--no-auth` turns it off; the loopback, Host
  and Origin checks still apply.
* `--dir DIR` writes a slot-style `slot.json`, `agent.pid` and `rpc-<port>.token` into DIR, so the existing `rpc`
  client (`rpc DIR METHOD [params]`) opens it unchanged. Without `--dir`, the token file is
  `rpc-<port>.token` in the working directory (or `--token-file`).
* Error codes are the old ones: `-32602` invalid params, `-32601` unknown method, `-32001` not in world, `-32002`
  disconnected, `-32003` game refusal (including "not ported"), `-32004` timeout. Every method the old daemon served
  is registered; one that this server does not implement answers `-32003` with
  `"<method>: not ported on this server"`, never an empty result.
* Params decode into a struct with unknown fields rejected, as before. GUIDs are `"0x"` hex strings; 128-bit values
  use 32 digits, and a shorter string is read as the low half.

## Method table

122 methods of the old daemon plus one addition (`bot`, below): 30 DONE, 93 NOT-PORTED.

DONE means the ForeverCore code exists in `internal/forever` and the method runs it; NOT-PORTED means there is no
ForeverCore implementation behind the old semantics, so the method answers the typed error.

### Session and observation

| Method | Status | What it does |
|---|---|---|
| `status` | DONE | The session's state, character, guid, map, mover position, target, health/power, dead/ghost and packet counts. Answers while the link is dead, with `state: "disconnected"` and the reason. |
| `units` | DONE | The model's units, nearest first: `within`/`near`, `entry`, `name`, `limit`. The `hostile` filter is refused by name (the model has faction templates, not factions). |
| `unit` | DONE | One unit by `guid` or `name`, with distance, health, power, level, target, combat, dead/ghost and cast. |
| `party` | DONE | The party model's roster, leader, health/power and pending invite. Raid groups are not modelled. |
| `chat_log` | DONE | The chat log as the old page (`since`, `limit`, `wait_ms`): seq, chat type name, source, channel, text. It is the chat model, not the 78-kind event log. |
| `logout` | DONE | The full logout (request, response and completion), then the daemon stops. |
| `events` | NOT-PORTED | There is no ForeverCore event log with the old kinds; the chat log is `chat_log`. |
| `map_snapshot` | NOT-PORTED | The old drawing is built from the 1.12 world model and navmesh. |

### Chat, GM and group

| Method | Status | What it does |
|---|---|---|
| `chat` | DONE | `say`, `yell`, `party`, `emote`, `whisper` (with `to`). A channel, guild or raid line is refused by name. |
| `gm` | DONE | One GM line through the command channel; the dot is added when missing. The GM guard (the non-mutating `.gm` probe) is confirmed once per session before the first line. |
| `gm_revive` | DONE | `.revive <name>` through the same channel; the name is validated first. |
| `invite` | DONE | `CMSG_PARTY_INVITE` and the server's command result for that name. |
| `accept_invite` | DONE | Answers the pending invite the model holds. |
| `leave_group` | DONE | Leaves a party the model holds a valid roster for. |
| `gm_kill`, `gm_evade`, `gm_set_hp`, `gm_respawn`, `gm_aura` | NOT-PORTED | The old helpers select a unit and send a unit-targeted GM line; that select-then-line pair is not in `internal/forever`. |
| `promote`, `assistant`, `convert_raid`, `raid_move`, `loot_method`, `disband`, `mark`, `roll` | NOT-PORTED | The party model has no senders or records for these. |
| `join_channel`, `leave_channel` | NOT-PORTED | There is no channel join in the chat round. |

### Movement, combat and map change

| Method | Status | What it does |
|---|---|---|
| `target` | DONE | Selects a unit by guid or name (`CMSG_SET_SELECTION`) and returns its row. |
| `attack` | DONE | The fight loop (select, face, approach, swing) for `guid`/`name` or the selected target, bounded by `for_ms` (default 60 s). Returns `reason: dead`, `stopped` or `map change`. |
| `stop_attack` | DONE | Asks a running `attack` to stop; the fight's own cleanup sends `CMSG_ATTACK_STOP`. |
| `cast` | DONE | One spell at a target or at the selected target (empty: self), with the session's cast/answer loop. A server refusal is a `-32003` error naming the reason. |
| `cancel_cast` | DONE | Cancels the cast the model holds (`CMSG_CANCEL_CAST`). |
| `cast_state` | DONE | The current cast, its remaining time, and the unit's health/power/combat/death from the combat snapshot. |
| `face` | DONE | Turns toward a unit (`CMSG_MOVE_SET_FACING`). |
| `walk_to` | DONE | A straight walk with the mover's own heartbeats and stop. |
| `path_to` | DONE | A navmesh route on the current map. Needs `--navmesh`; without it the method answers `-32003` naming the missing folder. |
| `walk_path` | DONE | The same route, walked as one continuous movement. Same `--navmesh` rule. |
| `area_trigger` | DONE | `CMSG_AREA_TRIGGER` for one trigger id. |
| `teleport` | NOT-PORTED | The old client-side GM teleport and its landing wait are not in `internal/forever`. |
| `spells` | NOT-PORTED | The login burst's known-spell list is not a spellbook model. |

### Death

| Method | Status | What it does |
|---|---|---|
| `death_status` | DONE | Phase, released, dead/ghost, corpse position and the reclaim countdown. |
| `accept_resurrect` | DONE | Accepts the pending resurrection offer. |
| `release_spirit` | DONE | Releases to the ghost and follows the graveyard teleport. |
| `corpse_query` | DONE | Asks the server for the corpse and returns its map and position. |
| `reclaim_corpse` | DONE | The corpse run and reclaim, with the server's delay and radius rules. |

### Command layer, add-ons and the bot module

| Method | Status | What it does |
|---|---|---|
| `bot` | DONE (addition) | One `.bot`/`.tb` line through the same command channel and GM guard; returns the sent line and the visible answer. |
| `addon_send` | DONE | Registers the prefix and sends one add-on message on `party`, `raid`, `instance`, `guild`, `officer` or `whisper:NAME`. |
| `commander`, `bot_view`, `tbm`, `tbm_handshake`, `tbm_click`, `tbm_buttons`, `tbm_send` | NOT-PORTED | These speak the 1.12 module protocol; the ForeverCore module uses the command channel (`bot`). |
| `addon_start`, `addon_stop`, `addon_state`, `addon_tick`, `addon_click` | NOT-PORTED | No headless add-on host in the ForeverCore client. |

### The rest of the old method set

| Methods | Status | Why |
|---|---|---|
| `inventory`, `equipment`, `equip`, `use_item`, `loot`, `loot_take`, `loot_close`, `open_object`, `use_object`, `gameobjects` | NOT-PORTED | No item, loot or object-use model in `internal/forever`. |
| `vendor_list`, `buy`, `sell`, `trainer_list`, `train`, `gossip_hello`, `gossip_select`, `taxi`, `quest_hello`, `quest_query`, `quest_accept`, `quest_complete`, `quest_reward`, `quest_abandon`, `quest_log` | NOT-PORTED | No NPC, vendor, trainer, taxi or quest model. |
| `trade`, `trade_open`, `trade_expect`, `trade_set_item`, `trade_accept`, `trade_cancel`, `trade_status` | NOT-PORTED | No trade model. |
| `pet`, `pet_action`, `mount`, `dismount` | NOT-PORTED | No pet or mount model. |
| `brain_start`, `brain_stop`, `brain_status`, `follow_start`, `follow_stop`, `follow_status` | NOT-PORTED | The old brain and follow loops are 1.12 code. |
| `dungeon_run`, `dungeon_status`, `dungeon_stop`, `dungeon_pause`, `dungeon_resume`, `dungeon_skip`, `dungeon_stage`, `plan_check`, `tune_set`, `tune_list` | NOT-PORTED | A ForeverCore run is a command (`dungeon-run`, `dungeon-watch`), not a daemon service. |
| `lfg_join`, `lfg_leave`, `lfg_status`, `lfg_auto`, `lfg_listings`, `lfg_listing_role`, `lfg_proposal_answer`, `lfg_role_check_answer`, `tt` | NOT-PORTED | The 1.12 group finder and the server-side script toolkit have no ForeverCore client code. |
| `teardown` | NOT-PORTED | The old teardown drives the 1.12 module, brain and follow loops. The process-level steps (pid file, lease, token removal) are outside the RPC. |

## The loop and the link

The session goroutine owns the socket and the model. HTTP calls post one function to that goroutine and wait; a call
whose caller went away is dropped before it runs. An idle 50 ms wait answers the server's time syncs and pings
between calls.

An unplanned world-socket drop ends the idle wait with `ErrLinkLost`. The RPC stays up for `--disconnect-grace`
(default 30 s, the old daemon's own window), answering `status` with `state: "disconnected"` and the reason, and
refusing every action with `-32002`. The daemon then exits with a world error. There is no automatic reconnect (the
one-shot commands do not reconnect either).

A clean shutdown (Ctrl-C, or the `logout` method) sends the real logout and waits for `SMSG_LOGOUT_COMPLETE` on its
own bound context. If the server refuses the logout, the daemon exits with the world error.

## Slot and teardown

* **`slot setup` is not ported.** It writes the old client's `settings.json` (realmd/world addresses, mmaps, RPC
  address, addon dir) and prints the old `login --config` start line. The ForeverCore login needs the login REST
  address, the Battle.net RPC address, a CA file and a build, and its agent has no settings file. `forever serve
  --dir` therefore writes only the no-secret manifest the `rpc` client needs (`slot.json`, token file, pid file); it
  is not a lease holder and creates no settings.
* **`teardown` is not ported.** Its game half is the old `teardown` RPC, which drives the 1.12 module, brain and
  follow loops; its process half needs a lease and a slot setup. A ForeverCore daemon is stopped with a signal (it
  logs out) and its `--dir` files are the token, pid and manifest above.

## Tests

`internal/forever/world/serve_fake_test.go`, against the fake world server over real sockets:

* `TestServeStatusChatAndLogout`: login, `status`, `units`, `unit`, `chat` (the server sees the text), `chat_log`
  (system line and the say echo), `logout` (the server sees the request and the completion reaches the report).
* `TestServeWalkAndFace`: `face`, `walk_to` (the mover reaches the target), and `path_to` without `--navmesh` as the
  typed error.
* `TestServeCombatAndCommand`: `target`, `cast` (the fake's `SMSGCastFailed` is a typed game error), `attack` (the
  fake sees the swing; the fake's health-zero update ends the fight with `reason: dead`), `stop_attack`.
* `TestServeGMAndBot`: the GM probe, `gm` lines, `.tb` through `bot` and its answer, and a non-command line refused.
* `TestServePartyAndAddon`: `party`, `invite` (the fake sees the packet), `accept_invite` without an invite refused,
  `addon_send`.
* `TestServeNotPortedAndUnknown`: `trade`, `teardown` and `map_snapshot` answer `-32003` with the ported-on-this-
  server text; an unknown name answers `-32601`.
* `TestServeLinkDropGrace`: the fake drops the instance socket; `status` turns disconnected with a reason, an action
  answers `-32002`, and the daemon exits with a world error after the grace window.
* `TestServeCleanShutdown`: cancelling the parent context logs out and returns cleanly.

`cmd/forever-agent/forever_serve_test.go` pins the password file and that the `--dir` manifest opens through the
same `openSlotClient` the `rpc` command uses, with the derived token file.

## Live steps

No live run was made in this round. The steps are:

```
# password: a file the account owner creates (never printed)
forever-agent forever serve \
  --rest <login-rest-host:port> --rpc <bnet-rpc-host:port> --ca <ca.pem> \
  --account <account> --character <character> --password-file <file> \
  --listen 127.0.0.1:8765 --dir <dir>

rpc <dir> status
rpc <dir> units '{"within": 30}'
rpc <dir> chat '{"type":"say","text":"serve rpc"}'
```

A successful run shows `rpc: listening on ...`, three results, and `serve: logged out` after Ctrl-C. If the server
drops a socket instead, `rpc status` shows `state: disconnected` for the grace window and the process exits with a
world error.

## Open questions

1. **The 1.12-era names that survived: `gm_*`, `tbm*`, `dungeon_*`.** The ForeverCore core's own command table decides
   which `.gm`, `.tb` and `.bot` lines exist; this round only ports the generic command channel.
2. **`bot`'s return shape.** It returns the collector's visible lines, not the 1.12 module's typed `Reply` object. A
   later round can map the typed state/order reports onto methods once the daemon owns a module session.
3. **Navmesh format.** `path_to`/`walk_path` use the ForeverCore v16 folder (`--navmesh`); a v6 folder is refused by
   the planner's header check.
4. **`units` hostility.** The model keeps faction templates, not factions or reputation, so the old `hostile` filter is
   refused rather than guessed.
