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
  disconnected (also every action while offline), `-32003` game refusal (including "not ported"), `-32004` timeout,
  and this server's `-32005` reconnecting: the world socket dropped and the daemon is logging in again, so the call
  did nothing and nothing is queued for after the re-entry, and `-32006` forbidden: the method is off or its target is
  outside the caller's boundary (the GM unit hands). Every method the old daemon served is registered; one that
  this server does not implement answers `-32003` with `"<method>: not ported on this server"`, never an empty
  result.
* Params decode into a struct with unknown fields rejected, as before. GUIDs are `"0x"` hex strings; 128-bit values
  use 32 digits, and a shorter string is read as the low half.

## Method table

122 methods of the old daemon plus one addition (`bot`, below): 34 DONE, 89 NOT-PORTED.

DONE means the ForeverCore code exists in `internal/forever` and the method runs it; NOT-PORTED means there is no
ForeverCore implementation behind the old semantics, so the method answers the typed error.

### Session and observation

| Method | Status | What it does |
|---|---|---|
| `status` | DONE | The session's state, character, guid, map, mover position, target, health/power, dead/ghost and packet counts, and `mono_ms` (the steady clock the combat rows are on). `state` is the link's lifecycle (`online`, `reconnecting`, `offline`); when it is not online, `error` is the reason and `reconnect_attempt`/`reconnect_limit` show the budget. Answers in every state, from the last session. |
| `units` | DONE | The model's units, nearest first: `within`/`near`, `entry`, `name`, `limit`. The `hostile` filter is refused by name (the model has faction templates, not factions). |
| `unit` | DONE | One unit by `guid` or `name`, with distance, health, power, level, target, combat, dead/ghost, cast and `auras` (`spell_id`, `caster`, `stacks`, `remaining_ms`). |
| `item_info` | DONE | The server's own ItemSparse and Item records for `entries` (1-64 positive item ids), one row per entry in the request's order: `found` (the ItemSparse record), name, quality and `quality_name`, class/subclass when the Item store answered, level, required level, prices, stack size, bonding and bag slots. Two record fetches per call, then the session's record cache. Round D1; see [DAEMON-D1-NOTES.md](DAEMON-D1-NOTES.md). No live run yet. |
| `party` | DONE | The party model's roster, leader, health/power and pending invite. Raid groups are not modelled. |
| `chat_log` | DONE | The chat log as the old page (`since`, `limit`, `wait_ms`): seq, chat type name, source, channel, text. It is the chat model, not the 78-kind event log. |
| `logout` | DONE | The full logout (request, response and completion), then the daemon stops. |
| `events` | DONE | The session's own combat log (`since`, `limit` up to 1,000, `wait_ms` up to 2,000) as the trials runner's raw rows, answered as `{events, next, oldest, dropped}`. The ring outlives a reconnect. See round A1's notes; the wait runs off the session goroutine, on the ring's own new-row signal. |
| `map_snapshot` | NOT-PORTED | The old drawing is built from the 1.12 world model and navmesh. |

### Chat, GM and group

| Method | Status | What it does |
|---|---|---|
| `chat` | DONE | `say`, `yell`, `party`, `emote`, `whisper` (with `to`). A channel, guild or raid line is refused by name. |
| `gm` | DONE | One GM line through the command channel; the dot is added when missing. The GM guard (the non-mutating `.gm` probe) is confirmed once per session before the first line. A line the core runs as one of the unit commands (`.die`/`.kill`, `.damage`, `.modify hp/mana/rage/energy`, `.aura`/`.unaura`, `.revive`) goes through the GM hands' boundary below first: same `--allow-gm-units` flag, same `trial_units` allowlist, same "never another player" rule, typed `-32006` and nothing sent on refusal. `.revive <name>` is gated for the player the line names. |
| `gm_revive` | DONE | `.revive <name>` through the same channel and the same unit-line boundary; the name is validated first, and only this session's own character can be revived (any other player is refused). |
| `invite` | DONE | `CMSG_PARTY_INVITE` and the server's command result for that name. |
| `accept_invite` | DONE | Answers the pending invite the model holds and waits under the caller's context for the server's answer: a roster naming the inviter confirms membership; a failed party command result or a roster the model refuses is a `-32003` error, and a silent server is a timeout error. |
| `leave_group` | DONE | Leaves a party the model holds a valid roster for. |
| `gm_kill`, `gm_set_hp`, `gm_aura` | DONE | Select one unit by guid, wait up to 1 s for the server's selection echo in the character's target field, then send the one line: `.die` (a player is refused), `.modify hp <hp>`, `.aura <spell>`/`.unaura <spell>`. Fail-closed: refused with `-32006` unless serve runs with `--allow-gm-units`, the guid is on the allowlist `trial_units` registered, and a player target is this session's own character. |
| `trial_units` | DONE | `{guids: [...]}` replaces the GM hands' allowlist for the current trial and answers `{guids}`; it outlives a reconnect. Refused with `-32006` without `--allow-gm-units`; a zero guid is `-32602`. |
| `gm_evade`, `gm_respawn` | NOT-PORTED | The old helpers select a unit and send a unit-targeted GM line; these two lines are not ported. |
| `promote`, `assistant`, `convert_raid`, `raid_move`, `loot_method`, `disband`, `mark`, `roll` | NOT-PORTED | The party model has no senders or records for these. |
| `join_channel`, `leave_channel` | NOT-PORTED | There is no channel join in the chat round. |

### Movement, combat and map change

| Method | Status | What it does |
|---|---|---|
| `target` | DONE | Selects a unit by guid or name (`CMSG_SET_SELECTION`) and returns its row. |
| `attack` | DONE | The fight loop (select, face, approach, swing) for `guid`/`name` or the selected target, bounded by `for_ms` (default 25 s). Returns `reason: dead`, `stopped` or `map change`. |
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
| `commander` | DONE (round C3) | Remote control of the module's bots over the session's own command channel: `bind`, `unbind`, `status`, `cmd` and `run`, with the binding card, per-bot lines and run records as typed results; a refusal is `-32003` with its code first. `view` is the cut op. See [CMDCTL-C3-NOTES.md](CMDCTL-C3-NOTES.md). |
| `bot_view`, `tbm`, `tbm_handshake`, `tbm_click`, `tbm_buttons`, `tbm_send` | NOT-PORTED | These speak the 1.12 module protocol; the ForeverCore module uses the command channel (`bot`, `commander`). |
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

An unplanned world-socket drop ends the idle wait with `ErrLinkLost`, and the daemon tries to log the character in
again instead of ending. The old session is torn down completely first (both readers cancelled, both sockets closed
and their readers joined) and a fresh session is logged in: a new world socket, a new instance socket and a new
world/chat/party/death model, so nothing of the dropped link survives. The link's lifecycle is explicit in `status`:

* `online` — the session is in the world.
* `reconnecting` — the drop was seen and the daemon is logging in again. Reads answer from the last session;
  actions answer `-32005` and are never queued for after the re-entry. A call posted before the drop is refused by
  its session epoch even if it is still in the queue, so an old action can never run on the replacement session.
* `offline` — the reconnect budget is spent. `status` carries the reason and actions answer `-32002` for
  `--disconnect-grace` (default 30 s), then the process exits with a world error.

The budget is stated in `serve.go`: at most 8 attempts (or `ServeConfig.ReconnectAttempts`; negative turns retries
off), the wait before attempt n is 2 s doubled n-1 times capped at 60 s (2, 4, 8, 16, 32, 60, 60, 60 s, about four
minutes), and one attempt is bounded by 60 s from the world socket to in world. The attempts are one budget across
drops: a link that drops again before it has stayed up for a minute keeps spending the same budget, so a flapping
socket ends offline rather than re-logging in forever; a session that stayed up for a minute starts a fresh budget at
its next drop. The login reuses the realm join ticket the daemon started with (there is no second Battle.net login in
a serving session), and a ticket the server refuses is just a failed attempt.

Read-only methods (`status`, `units`, `chat_log`, `death_status`) still answer while the link is down, from the last
session's frozen model. `chat_log` may end with the link error instead of waiting, because its `wait_ms` drains the
socket. `logout` is an action like any other while reconnecting (`-32005`); a signal still stops the daemon, which
exits with the reconnect error instead of logging out a session that is not in the world.

A clean shutdown (Ctrl-C, or the `logout` method) sends the real logout and waits for `SMSG_LOGOUT_COMPLETE` on its
own bound context. If the server refuses the logout, the daemon exits with the world error.

## Round 2: reconnects and confirmations

Two review findings were fixed here.

1. **A transient world-socket failure ended the daemon for good.** It now reconnects within the stated budget (see
   "The loop and the link"), tears the old session down first, reports `online`/`reconnecting`/`offline` in
   `status`, answers actions with `-32005` while reconnecting and never replays them, and rebuilds every
   session-owned model after re-entry.
2. **`accept_invite` returned success before the server confirmed it.** It now waits under the RPC request's context
   for a roster that names the inviter, a failed party command result, or a timeout, and returns an error unless
   membership is confirmed.

The same "success before the server confirms" check over the DONE methods:

| Class | Methods |
|---|---|
| Waits for the authoritative answer (already did; unchanged) | `invite` (party command result), `leave_group` (the leave result / roster), `cast` (the cast's outcome), `attack` (the fight's outcome: dead, stopped or map change), `accept_resurrect`, `release_spirit`, `corpse_query`, `reclaim_corpse` (the death waits), `logout` (the completion), `gm`, `gm_revive`, `gm_kill`, `gm_set_hp`, `gm_aura`, `bot` (the command's collected reply; `complete` says whether it ended as the command requires; the three unit hands also wait for the server's selection echo first) |
| Client-authoritative in 1.12: the server sends no answer to wait for | `target` (`CMSG_SET_SELECTION`), `face` (`CMSG_MOVE_SET_FACING`), `walk_to`/`walk_path` (the mover's own heartbeats; the server does not acknowledge a walk), `stop_attack` (`CMSG_ATTACK_STOP`), `cancel_cast` (`CMSG_CANCEL_CAST`), `area_trigger` (`CMSG_AREA_TRIGGER`; an effect, if any, arrives as world packets), `chat` and `addon_send` (no acknowledgment; a mute or a language refusal is silent) |
| Read-only | `status`, `units`, `unit`, `party`, `chat_log`, `events`, `cast_state`, `path_to`, `death_status` |
| Fixed | `accept_invite` |

## Round 3: the raw GM boundary and the login burst's rows

Two review findings were fixed here.

1. **The raw `gm` method bypassed the GM unit hands.** It forwarded caller text to the command channel with no
   check, so `target` plus a raw `.die`, `.modify hp`, `.aura` or `.unaura` did what `gm_kill`, `gm_set_hp` and
   `gm_aura` refuse. The raw path now classifies the line against the core's own command table
   (`Chat.cpp:874-887`, `cs_modify.cpp`) — case-insensitive, the core's own prefix matching, one command per
   line — and sends a line that acts on a unit's life, health, power, auras or damage through `serveGMUnit`:
   the flag, the trial allowlist and the player rule, with a typed `-32006` and no line (not even the guard's
   probe) on refusal. Both selections a line could act on are checked (the last one this session sent and the
   server's last echo), because the core applies the two on different threads. `gm_revive` carries `.revive
   <name>` and goes through the same boundary, so reviving another player is refused there too. Ordinary GM
   lines (`.tb ...`, `.gps`, `.server info`, and plain chat) keep working with no flag.
2. **A reconnect could discard rows the login burst produced.** The retained ring was installed after
   `loginWorld` returned, so a combat or aura row the login dispatch wrote in the meantime went into the fresh
   session's own ring and was thrown away. The dropped session's ring is now given to the replacement session
   before its first login packet is dispatched (`loginAgain`), so the seq continues with no hole.

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
* `TestServeWalkAndFace`: `face`, `area_trigger`, `walk_to` (the mover reaches the target), and `path_to` without
  `--navmesh` as the typed error.
* `TestServeCombatAndCommand`: `target`, `cast` (the fake's `SMSGCastFailed` is a typed game error), `cast_state`,
  `cancel_cast` with nothing to cancel, `attack` (the fake sees the swing; the fake's health-zero update ends the
  fight with `reason: dead`), `stop_attack`.
* `TestServeGMAndBot`: the GM probe, `gm`, a `gm_revive` refused without the flag, `.tb` through `bot` and its
  answer, and a non-command line refused.
* `TestServePartyAndAddon`: `party`, `invite` (the fake sees the packet), `accept_invite` and `leave_group` without a
  group refused, `addon_send`.
* `TestServeAcceptInvite`: three subtests against the fake's invite: the roster naming the inviter confirms the
  accept, a failed party command result (group-full) is the typed refusal, and a silent server is the timeout; the
  fake sees CMSG_PARTY_INVITE_RESPONSE in every case.
* `TestServeNotPortedAndUnknown`: `trade`, `teardown` and `map_snapshot` answer `-32003` with the ported-on-this-
  server text; an unknown name answers `-32601`; the five death methods answer their own refusals for a living
  character.
* `TestServeNavmeshPath`: a `navfixture` mesh in a temporary folder; `path_to` is complete and `walk_path` walks it.
* `TestServeOfflineGrace`: with the retries turned off, the fake drops the instance socket; `status` turns offline
  with a reason, an action answers `-32002`, and the daemon exits with a world error after the grace window.
* `TestServeReconnectIdle`: a drop while idle; `status` reports `reconnecting` with its attempt and limit, an action
  answers `-32005`, the daemon logs in again on a second session (the model is rebuilt from its burst) and the refused
  action is not sent later.
* `TestServeReconnectWhileMoving`: a `walk_to` that is running when the socket drops ends with `-32002`, the daemon
  re-enters, and the walk is not resumed (the new session stands at its own create block).
* `TestServeReconnectDuringCombat`: an `attack` that is running (the fake keeps the creature alive) ends with
  `-32002`, then the daemon re-enters with no target selected.
* `TestServeReconnectDuringLogin`: the first re-entry's world socket is refused before the handshake; the attempt
  fails, the next one reaches the world, and the log names the failed attempt.
* `TestServeReconnectExhausted`: every attempt fails; `status` turns offline with `reconnect gave up after 2
  attempt(s)`, an action answers `-32002`, and the daemon exits with that error.
* `TestServeReconnectRepeatedDrops`: every login is dropped after it is in the world; the one budget is spent and the
  daemon goes offline rather than re-logging in forever.
* `TestServeEventsKeepTheLoginBurst`: the fake writes a melee row before `SMSG_LOGIN_VERIFY_WORLD`; the row is in
  `events` after the re-entry, at the next seq, beside the row written after the login.
* `TestServeRawGMRefusesUnitCommands`: with the flag on and nothing registered, `.die`, `.kill`, `.damage`,
  `.modify hp`, `.aura`, `.unaura` and their case/space/abbreviation variants answer `-32006` and the fake sees no
  line; registering the creature lets the same raw lines run.
* `TestServeRawGMPassesOrdinaryCommands`: without the flag `.tb status` and `.gps` still run, and a raw `.die` is
  refused and not sent.
* `TestServeRawGMRevive`: `.revive` through the raw method and `gm_revive` refuses another player even when
  registered; the session's own character passes with the flag and its guid registered. The raw and the typed
  form are refused before any line (and before the guard's probe) when they name anyone else.
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
drops the socket instead, the log prints `rpc: state reconnecting (attempt n of 8)`; `rpc status` shows `state:
reconnecting` (with `reconnect_attempt`/`reconnect_limit`, and `-32005` for actions) until the login lands and it is
`online` again. If the budget is spent, `state: offline` names the reason for the grace window and the process exits
with a world error.

## Open questions

1. **The 1.12-era names that survived: `gm_*`, `tbm*`, `dungeon_*`.** The ForeverCore core's own command table decides
   which `.gm`, `.tb` and `.bot` lines exist; this round only ports the generic command channel.
2. **`bot`'s return shape.** It returns the collector's visible lines, not the 1.12 module's typed `Reply` object. A
   later round can map the typed state/order reports onto methods once the daemon owns a module session.
3. **Navmesh format.** `path_to`/`walk_path` use the ForeverCore v16 folder (`--navmesh`); a v6 folder is refused by
   the planner's header check.
4. **`units` hostility.** The model keeps faction templates, not factions or reputation, so the old `hostile` filter is
   refused rather than guessed.
