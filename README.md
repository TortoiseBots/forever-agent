# forever-agent

A headless **WoW Forever** client for [ForeverCore](https://github.com/Karrz1/TrinityCore) servers. One process logs
one character in, with no game window, and exposes it over a local JSON-RPC API, so server-side behaviour can be
tested from scripts.

**Status: early port; the character can log in and out, but there is no gameplay yet.** This repository starts as a
copy of [turtle-agent](https://github.com/pjerra/turtle-agent) (the same tool for vanilla 1.12 / Turtle WoW servers).
The 1.12 game layer still speaks the 1.12 protocol. Rounds 1 and 2 wrote the Forever login and the world socket up
to "the server treats the player as fully in the world"; movement, objects and gameplay come next.

| Part | State |
|---|---|
| Login through bnetserver | **SRP login, login ticket and the realm join answer work** (`forever-agent forever login-check`, round 1: see [docs/forever/LOGIN-R1-NOTES.md](docs/forever/LOGIN-R1-NOTES.md)) |
| World socket (encrypted, compressed) | **Works** (`forever-agent forever world-check`: banners, world auth, AES-GCM, character list, login on the instance socket, time sync, ping, logout; round 2: see [docs/forever/WORLD-R1-NOTES.md](docs/forever/WORLD-R1-NOTES.md)) |
| Packets and object fields | login and character-select packets done; round 3 reads `SMSG_UPDATE_OBJECT` (creates, values, movement blocks, splines), the movement relays and both name lookups, and walks the character (`forever-agent forever walk-check`; see [docs/forever/OBJECTS-R1-NOTES.md](docs/forever/OBJECTS-R1-NOTES.md)) |
| Self inventory (round R1) | **Works** (the character's own inventory, read from the login create block: equipment 0-18, the four bags and their contents, the backpack 35-62, money, the quest log and the 19 visible items; kept current by the item create and update blocks; read with `forever rpc DIR inv` from a `forever serve` daemon, see the `inv` method below) |
| Map changes (round A7) | **Works** (`forever-agent forever map-check`: follows a far teleport, a teleport to another continent and entering or leaving an instance — suspend/new-world handshake, the world model cleared for the new map, the navmesh planner dropped; see [docs/forever/MAPCHANGE-R1-NOTES.md](docs/forever/MAPCHANGE-R1-NOTES.md)) |
| Chat and party (round 5) | **Works** (`forever-agent forever chat-check` and `forever party-check`: say, yell, whisper, party and emote text out; `SMSG_CHAT` in, the notification/`MOTD` lines, and the invite/accept/update/member-state/leave flow with a bounded party model; see [docs/forever/CHAT-R1-NOTES.md](docs/forever/CHAT-R1-NOTES.md)) |
| Add-on messages (round A0) | **Works** (`forever-agent forever addon-check`: prefix registration, one add-on message on party, raid, instance, guild, officer or whisper, the receive filter and bounded inbox; a channel message is received and classified but sending on a channel is not supported yet; see [docs/forever/ADDON-R1-NOTES.md](docs/forever/ADDON-R1-NOTES.md)) |
| Death, release and resurrection (round 6) | **Works** (`forever-agent forever death-check`: the character dies, releases its spirit, learns where its corpse is, runs back and reclaims it, or answers another unit's resurrection offer or uses the spirit healer; the world model also tracks every other unit's dead/ghost/resurrected state; see [docs/forever/DEATH-R1-NOTES.md](docs/forever/DEATH-R1-NOTES.md)) |
| Act runner and NPC talk (forever-world-r0) | **Works against the fake world; a live run is the next step** (`forever-agent forever act`: one scriptable step per world action — wait, gm (through the command channel and its GM guard), goto (navmesh or straight line), npc (find by entry, walk within reach), open (talk or quest hello by NpcFlags, prints the menu), select, close — with one `step N verb ok|fail reason` line each; see [docs/forever/ACT-R0-NOTES.md](docs/forever/ACT-R0-NOTES.md)) |
| Item actions (round R2) | **Works** (`forever act` steps equip, store, swap, split, destroy, use, open an item and `inv`; the inventory follows every action without a relog: gains come from the push result, backpack moves from the player's own InvSlots update, moves the server has not confirmed shown as the client's guess and then unknown, refusals from `SMSG_INVENTORY_CHANGE_FAILURE` with the result's name and tail; `equip` names its destination and `use` its spell, and `--capture-dir` writes every packet the run's handlers see, see [docs/forever/WORLD-R2-NOTES.md](docs/forever/WORLD-R2-NOTES.md)) |
| Loot and game objects (round LOOT-1) | **Works against the fake world; a live run is the next step** (`forever-agent forever loot-check` and `forever object-use`: open a corpse's or chest's window, read the items, money, slots and roll-required flags, vote on a group roll (need, greed or pass; need only when asked), take money and items, release the window; use a door, lever, chest or quest object and read its open/closed state from the object updates; see [docs/forever/LOOT-R1-NOTES.md](docs/forever/LOOT-R1-NOTES.md)) |
| ForeverCore world export (round L3) | **Works offline** (`tools/dungeongen/export-forever.sh` writes the TSVs `worldspawn.Load` reads from a ForeverCore 25fc7ca world database — creatures, formations, waypoint paths, game objects, entrances — and `worldspawn.LoadForeverDB` reads them: levels in the order the core applies them (classic spawn row, classic entry row, content tuning; 60 when unknown), normal difficulty only, and the core groupAI bits mapped to the reader's flags (the leader's own formation row counts); see [docs/forever/L3-NOTES.md](docs/forever/L3-NOTES.md)) |
| Plan scout on Forever (round K4) | **Offline draft works; the live pass is wired, not yet run** (`forever-agent plan-scout --forever --spawns DIR --mmaps DIR [--parent MAP=PARENT]` drafts from `worldspawn.LoadForeverDB` on the v16 planner and its output passes `plan-check --forever`; `--live --slot DIR` drives a running `forever serve` with `.gm on`, `.instance unbind all`, a `.go xyz` and a `units {within:40}` read per stop — levels the core has no row for take the live unit's — a `path_to` per hop and `walk_path` of the risky hops in the walk budget; allow-list checked before every line; see [docs/forever/CLI-K2-K6-NOTES.md](docs/forever/CLI-K2-K6-NOTES.md)) |
| Command channel (round A6) | **Works** (`forever-agent forever cmd-check`: one chat-command line at a time, the reply's system lines collected by token or quiet window, the core's refusals as named errors, a GM guard before anything is sent, and the typed state/order/threat/party model of the server module's `.tb` commands; see [docs/forever/COMMAND-R1-NOTES.md](docs/forever/COMMAND-R1-NOTES.md)) |
| WoW Forever Bots module (round A1) | **Works** (`forever-agent forever fb-check`: the module's own `.bot` commands through the command channel, orders by whisper (the bot answers) or by a party word, and state read through the module's add-on protocol — PING, the roster, one bot's detail — with chunked answers, a shape-based drift rule and a soft fallback to the chat commands; see [docs/forever/WFB-BOTS-R1-NOTES.md](docs/forever/WFB-BOTS-R1-NOTES.md)) |
| WoW Forever Bots module, dungeon (round A2) | **Works** (`forever-agent forever fb-dungeon`: `.bot lfg` fills the party to five, `.bot dungeon` queues, entry is confirmed only by the module's `inside` **and** the client's own map change, the tank bot leads, and the run watches health, deaths, party lines and the module's events; see [docs/forever/WFB-BOTS-R2-NOTES.md](docs/forever/WFB-BOTS-R2-NOTES.md)) |
| Roster dashboard (rounds DASH-1/2) | **Client and watch; no live run yet** (`forever-agent forever dash-bots`: the typed login, the logged-in bots and the heartbeat, and a watch that reports a death, a stuck bot, a lone fight and a bot far from its group; plain http only on loopback unless the explicit flag allows it; the dungeon watch can take the same client and rules; see [docs/forever/DASH-R1-NOTES.md](docs/forever/DASH-R1-NOTES.md) and [docs/forever/DASH-R2-NOTES.md](docs/forever/DASH-R2-NOTES.md)) |
| Dungeon driver (round D1) | **Works against the scripted world** (`forever-agent forever dungeon-run --dry`: the run loop as a state machine over a plan — party checks, entry, legs, pulls, fights, rest, deaths, wipes, bosses, the safety rules and the run's trace and summary; no session adapter yet, see [docs/forever/DRIVER-R1-NOTES.md](docs/forever/DRIVER-R1-NOTES.md)) |
| Dungeon watcher (round S7) | **Works against the scripted world; the live mode is wired, not yet run** (`forever-agent forever dungeon-watch`: PREPARE and ENTER as a run, then one typed `.tb run start`, after which the client never orders a bot — it polls the module's run status and event ring at a stated interval and prints progress, with a watchdog, a pause budget, a time budget, a wipe budget and the practice rescue, and a summary extended with the module's counters; round B1 adds the trace frames (`--frame-every`) and the session's combat log written beside the trace — see [docs/forever/WATCH-R1-NOTES.md](docs/forever/WATCH-R1-NOTES.md) and [docs/forever/HUNT-B1-NOTES.md](docs/forever/HUNT-B1-NOTES.md)) |
| Bug hunter (round B3) | **Offline, on a watch or a trials repeat** (`forever-agent bughunt`: the rules over the watch's frames (F) and combat log (C), the tickets the board carries with stable ids and a baseline's new/known/gone; see [docs/forever/HUNT-B3-NOTES.md](docs/forever/HUNT-B3-NOTES.md)) |
| Run report card (round S7-B) | **Works against the scripted world** (`forever-agent forever dungeon-watch` writes a card after every run and `forever run-report RECORD` builds it offline from the saved record: per-leg time, pulls and re-pulls, deaths with the member's slot and step, wipes, pauses with reasons, flags with their evidence lines, a one-line verdict, and `--compare A B` for what got better or worse — see [docs/forever/REPORTCARD-R1-NOTES.md](docs/forever/REPORTCARD-R1-NOTES.md)) |
| Dungeon driver (rounds D1-D2) | **Works against the scripted world; the live mode is wired, not yet run** (`forever-agent forever dungeon-run`: the run loop as a state machine over a plan — party checks, entry, legs, pulls, fights, rest, deaths, wipes, bosses, the safety rules and the run's trace and summary; `--dry` runs it over the scripted world, the live mode binds the ports to a world session and the command channel — see [docs/forever/DRIVER-R1-NOTES.md](docs/forever/DRIVER-R1-NOTES.md) and [docs/forever/DRIVER-R2-NOTES.md](docs/forever/DRIVER-R2-NOTES.md)) |
| Character screen (round 6) | **Works** (`forever-agent forever char-create` and `char-delete`: create, delete and the name-availability check with every result code named; a create with no customization choices is valid; see [docs/forever/CHARCREATE-R1-NOTES.md](docs/forever/CHARCREATE-R1-NOTES.md)) |
| Game layer on the new packets | not started |
| Trials (behaviour test bench, in Go, as `forever-agent trials`) | **Setup toolkit and provisioning (round A3): the `.tb` test-kit client, hire-pool and player provisioning, the 22-row setup round; `trials run` provisions live without `--dry`; see [docs/forever/TRIALS-A3-NOTES.md](docs/forever/TRIALS-A3-NOTES.md). Dummy spell/damage bench (issue #18): `dummy` situation kind, per-spell damage + reaction tallies against a per-class table and a trial file beside it (`internal/trials/dummybench/testdata`), report card per spell; unmeasured bounds stay UNKNOWN, strangers INVALID; see [docs/forever/DUMMY-BENCH-NOTES.md](docs/forever/DUMMY-BENCH-NOTES.md)** |

`forever-agent forever login-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL` logs in through the
login server (password from `FOREVER_PASSWORD`) and stops at the realm join ticket: one line per step, then the realm
name, id and address. It prints no ticket, key, proof or password, only their lengths; the connect step prints the
ciid, the server id and the server time as counts (`ciid 33 chars`), never their values, so the output is safe to
paste into a report. `--build` pins the client build to claim; without it the command claims the newest build this
client knows that the realm accepts (a newer build is tried first and the realm list's build is only a hint). A realm only admits the builds it accepts, and a realm that accepts none of these is refused by
name. Each RPC step and the login as a whole are deadline-bounded (30 s per request, 2 min overall), so a silent or
slow-drip server cannot hold the command open.

`forever-agent forever world-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
does the same login and then enters the world with that character: world auth, character list, `CMSG_PLAYER_LOGIN`,
the instance socket, the login burst (loading screen, time syncs, the active-mover exchange), `--stay` seconds of
keep-alives (default 20 s), then a clean logout. It prints one line per step, a count of server packets by opcode
name, and no key or ticket, only lengths. Without `--build` the command claims the newest build it knows that the realm
accepts and the world server authenticates: the realm list is not the world's build list, so a build the world server
refuses with `ERROR_BAD_VERSION` is dropped and the next known build is tried, newest first. That code is retried on
only under the setting this client's setup requires (`Network.SkipBuildAuthKeyCheck = 1` in
[docs/forever/PROTOCOL-LOGIN.md](docs/forever/PROTOCOL-LOGIN.md)): the server also answers `ERROR_BAD_VERSION` for a
join-ticket variant with no `build_auth_key` row while it checks the digest, and this client's zero digest can never
pass that check. There the login stops at the refusal and names both causes instead of calling the build unsupported;
`--world-checks-build-auth-key` states that the server checks the key (`Network.SkipBuildAuthKeyCheck = 0`). When no
build works the error names each one and which side refused it. A pinned `--build` is claimed as given, and
the world server's refusal is the command's error. By default the
instance socket `SMSG_CONNECT_TO` names must be on the host the realm socket is connected to;
`--allow-instance-redirect` permits a different host for clustered servers.

`forever-agent forever serve --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME
[--password-file FILE] [--listen HOST:PORT] [--dir DIR] [--token-file FILE] [--no-auth] [--navmesh DIR]
[--parent MAP=PARENT] [--disconnect-grace DURATION] [--allow-gm-units]` keeps that character in the world
and serves the local JSON-RPC socket the old daemon serves: `POST /rpc`, one JSON-RPC 2.0 object, a bearer
token, loopback only, the same error codes. The password comes from `--password-file` or
`FOREVER_PASSWORD` and is never printed. The listener is bound before the login; `--listen` defaults to
`127.0.0.1:8765`. `--dir DIR` writes `slot.json`, `agent.pid` and `rpc-<port>.token`, so `rpc DIR status`
works unchanged; without it the token file is `rpc-<port>.token` in the working directory. `status`,
`inv`, `units`, `unit`, `party`, `chat_log`, `events`, `chat`, `gm`, `gm_revive`, `gm_kill`, `gm_set_hp`,
`gm_aura`, `trial_units`, `bot`, `commander`, `invite`, `accept_invite`, `leave_group`, `target`, `attack`,
`stop_attack`, `cast`, `cancel_cast`, `cast_state`, `face`, `walk_to`, `path_to`, `walk_path`,
`area_trigger`, the five death methods, `addon_send` and `logout` are ported; `--navmesh` enables the two
path methods. Every other old method answers a typed `not ported on this server` error. A `unit` row
carries the auras this client's model holds (`spell_id`, `caster`, `stacks`, `remaining_ms`), and `status`
carries `mono_ms`, the steady clock every combat row is on. `events` (`since`, `limit` up to 1,000,
`wait_ms` up to 2,000) reads the session's own 4,096-row combat log as the trials runner's raw rows and
answers `{events, next, oldest, dropped}`, also while reconnecting: the ring is handed to the session that
replaces a dropped one before its login burst is dispatched, so a seq never goes back and a row the re-entry's
own login writes is kept. Each row also carries `spell_name` when the server named the spell: one batched,
cached `CMSG_DB_QUERY_BULK` per page for the ids the page names, with the row's `spell_id` as the fallback.
Its `wait_ms` is spent off the session goroutine, on the
ring's own new-row signal, so an empty long-poll never holds `status`, a chat, a GM hand or a logout;
concurrent long-polls all return at their own cap. `units` takes an optional `hostile` filter: the mob's
faction template against the character's own (fetched once from the server and cached), so `true` keeps only
what is hostile to the character and `false` only what is not.

The character's own inventory is `inv`: `{known, coinage, items[], quest_log[], free_slot, stale?, stale_reason?}`. Each item row is
`{guid, container, slot, entry, count, durability?, max_durability?, placed, unconfirmed?}`, where `container` is 255 for the
player's own slots (equipment 0-18, bag slots 30-33, backpack 35-62) or the bag slot the item is inside, and `slot`
is the index in that container. `inv` is read from the character's own create block — PlayerData (the 175-entry
quest log, the 19 visible items) and the ActivePlayerData prefix (the 145 InvSlots and Coinage) — and from every
item's own create or values update, and it sends nothing (`known` is false until that create arrives). It is kept
current without a relog (round R2): the server's own words are the one truth for a slot, its update fields (the
player's own InvSlots with the Coinage that travels with them, and a bag's own slots) and its item push results (an
item gain is placed by its push result; `placed` is false only for a gain that named no slot, a merged stack). A move
the client sent itself only bridges the gap: the client's guess shows (`unconfirmed`) until the server's slots confirm
it or two seconds pass, and then the item is `placed: false` rather than silently at the guess; an equipment change's
answer is not decoded, so an equipped item stays unknown until the next login. An update the client could not
decode that named slots makes `stale` true until the next login or map change. A refusal is printed as its name and tail (`cant_equip_level_i (needs level 12)`). The token is
`inv`; the old daemon's
`inventory` method keeps its "not ported" answer, because its result is a plain row array and a caller written for
it must not be handed another shape. The act runner's `inv` step (round R2) prints the same rows, one line per item
(`entry 118 count 5 slot 35`, `slot unknown (arrived after the snapshot)`, or `(unconfirmed)` while a move of the item waits for the server), a `STALE` line when slots were lost,, then the first free backpack slot and
the quest log's occupied slots.

`gm_kill`, `gm_set_hp` and `gm_aura` are a test bench's hands and are off unless serve was started with
`--allow-gm-units`. Even then they act only on a guid the runner registered through `trial_units`
(`{"guids": [...]}`, which replaces the list and answers `{guids}`), and a player target must be this
session's own character: another player is refused even when registered. Every refusal is the typed
`-32006` error and sends nothing. The allowlist outlives a reconnect. A permitted hand selects the unit,
waits for the server's selection echo and sends the one line (`.die`, `.modify hp N`, `.aura N`/`.unaura
N`). The raw `gm` method and `gm_revive` are held to the same boundary: a line the core runs as one of the
unit commands (`.die`/`.kill`, `.damage`, `.modify hp/mana/rage/energy`, `.aura`/`.unaura`, `.revive`) is
refused with `-32006` unless the flag is on and every unit it could act on is a registered trial unit, and
`.revive` only ever names this session's own character. Ordinary GM lines (`.tb ...`, `.gps`, `.server info`)
and chat are unaffected.

Remote control on ForeverCore is `commander`: the module's TortoiseBots party, commanded by the session's own
character over the session's `.tb` command channel (no add-on bridge, no other account).
`forever rpc DIR commander '{"op":"bind","lead_bot":"<tank>","bots":["<healer>","<dps>"]}'` forms the lead's party,
binds it to the character and answers the binding card. Ops are `bind` (`lead_bot`, `bots`), `unbind` (`lead_bot`),
`status`, `cmd` (`verb`, `args`, `wait_ms`, `pause_run`; `lead_bot` scopes the call to that lead's party, and a
name the character does not command is refused before any line goes out) and `run` (`verb` start, stop, pause,
resume, status or events; `lead_bot`; `args` carries the plan, the reason words or the event sequence; a `start`
without `lead_bot` uses the only bound lead, and is refused when there is not exactly one). The answer carries
`sent` (the command lines that went out), `bots` (the module's per-bot lines), `card` (the binding card the call
read, or `cards` for a `status`), `final` (each waited bot's `ok` or `fail:<code>`, `{}` when no wait ran),
`party_error` (why a `status` read the cards but not the party), and a run verb's `runs` and `events`. Every
mutation reads `.tb commander status <self>` before the line and, for a bind, order, role or run mutation, after
it: a target outside the character's own bindings is refused `not-owned` before anything is sent, and a line
whose target changed in between says the line already went through. Every refusal also carries the call's partial
result in the error's `data` (the lines that went out, the bots, `card` and `final` it had read), so a caller that
sees an order in `sent` knows it went out and never retries it blindly. An order while a run is running or paused
is refused `run-active` unless `pause_run` is true (the module pauses the run itself). A verb of a module package
that is not installed answers `not-available` after one cached probe per session, and the probe cache is dropped
when the world link is re-entered; a verb whose typed path has not landed (round C5) answers
`not-available: <verb>: needs-typed-path (round C5)` with no probe line at all. `view` is the port's cut verb:
`commander view: not ported on this server`. Every refusal is `-32003` with its code first in the message
(`not-available`, `run-active`, `not-owned`, `refused`, `bind-refused`, or the module's own error code); a lost or
re-entering link keeps `-32002`/`-32005`. `wait_ms` (clamped to 0..30000 before the conversion) polls `.tb state`
every 500 ms for each queued order's per-bot line behind one overall deadline (the wait plus a 5 s read budget,
capped at 15 s; the poll runs on the session goroutine, so a long wait holds the loop and every other call behind
it for that bounded time), and is ignored by the other ops; an order is sent once and never resent.

Ctrl-C logs out. An unplanned world-socket drop is not fatal: the daemon tears the old session down, logs in again
with the same account and character (up to 8 attempts, 2 s doubling to 60 s apart, one attempt bounded by 60 s; a
session that stayed up for a minute starts a fresh budget), and serves reads throughout. `status` reports the link
state (`online`, `reconnecting`, `offline`); while `reconnecting` every action answers `-32005` and nothing is queued
for after the re-entry. When the budget is spent the daemon is `offline` with the reason for `--disconnect-grace`
(default 30 s) and then exits with a world error. `docs/forever/SERVE-R1-NOTES.md` has the method table.

`forever-agent forever rpc [--timeout DUR] [--raw] <dir> <method|list> [<params-json>|-]` is the CLI client of
that socket: one method call with a JSON object or array (or `-` for stdin, or nothing), printing the pretty JSON
result (compact with `--raw`) or the typed `error: <code> <message>` with exit 16. `list` first calls the selected
daemon's `status`, then prints every method the socket answers, with the ones this server does not port marked
`(not ported)`; a stopped daemon fails `list` like any other call, so the table is never printed for a socket nobody
answers. The dir is a serve `--dir`, opened with the same checks as the old `rpc` (slot.json asserted, the derived
token file, nothing but 127.0.0.1). A call the server refuses answers its typed error; a daemon that is not running
is `forever rpc: cannot reach` with exit 15. Ctrl-C or an expired `--timeout` aborts the HTTP request (the connection
is closed) and the command waits for the call to stop before exiting 1 with `cancelled:` or `timed out after ...`; no
result is printed, so a caller must not retry an irreversible method as if the first call had been refused.
`internal/forever/serveclient` is the typed Go client of the same socket for commands that orchestrate a live slot
(see [docs/forever/REST-L1-NOTES.md](docs/forever/REST-L1-NOTES.md)): `Open(dir)`/`New(addr, tokenFile)` with typed
`Status`, `Units`, `Unit`, `Party`, `GM`, `Bot`, `Events` and `Logout` (what the daemon serves today; `EventLog`, `Act`,
`Commander` and `Teardown` need W3/W2), loopback addresses only, guids as text with `Key()` delegating to the run-map
key, `-32005` retried for 60 s and `-32002` ending the call at once.

`forever-agent fleet --forever --config FILE --slots FILE [--server NAME] [--stagger D] [--wait D]
[--stop-on-failure]` runs one `forever serve` per fleet-file member (`{account, character, dir, listen,
password_file}`) instead of one old `login` child. The serve's connection flags come from the slots file's
`forever` block (`rest`, `rpc`, `ca`, `build`; `--server` picks it when the file names more than one). Each
child is `forever serve --dir <dir> --listen <listen> --account <account> --character <character>
--password-file <file>` plus those flags; the fleet file carries no secret, only paths and names. `online`
(the serve link lifecycle) counts as in world, on this path only; starts are paced one login per 5 s under the
per-IP limit. Before any login every member is checked against the slots file: its account must be a slot
account, its character allowed, and no live lease of another holder may hold that account; accounts are
unique in a fleet file. `--slots` and `--server` are refused without `--forever`.
`forever-agent slot setup --forever <server> <slot> --holder NAME ...` leases the slot and builds the dir
as today (plans included) but writes no `settings.json` and prints the `forever serve --dir` line the fleet
child runs; `slot.json` carries the real server, slot, holder and lease id, so `teardown` and `rpc` work on
the dir. `forever serve --dir` keeps a matching `slot.json` and refuses a foreign non-empty dir.
`--mmaps-dir` is refused with `--forever` (a serve takes `--navmesh` itself).
`forever-agent forever char-create --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --name NAME --race R --class C [--sex S] (--choice OPTION:CHOICE ... | --no-appearance)`
logs in like world-check and stops at the character screen: it lists the account's characters, sends
`CMSG_CREATE_CHARACTER` and lists again. The appearance is explicit: each `--choice OPTION:CHOICE` pair is sent as
given (only the two numbers are checked; a pair the server rejects is its `CHAR_CREATE_ERROR`), and `--no-appearance`
is the documented test-character form, because no real client has been seen creating a character with no
customization choices (the server accepts one, at create and at login). It prints the result code name
(`CHAR_CREATE_SUCCESS`, or the refusal's name) and both lists, and exits non-zero on a refusal. A successful create
whose follow-up list fails still exits zero and says so: `done; the list could not be refreshed`.
`forever-agent forever char-delete ... (--name NAME [--surname S] | --guid GUID) --yes NAME`
resolves the name on the same character-screen list (every match, so an ambiguous first name is refused with its
candidates) and sends nothing until `--yes` repeats the resolved character's full name exactly; without it the command
prints `would delete NAME (level L, GUID); pass --yes NAME` and exits with the usage code. With it, it sends
`CMSG_CHAR_DELETE` with the resolved GUID and lists again; the client's own confirmation prompt is not on the wire.
`--build` follows world-check's rule, `--world-checks-build-auth-key` included: without `--build` the login claims
the newest build it knows that the realm accepts and the world server authenticates (a build the world refuses with
`ERROR_BAD_VERSION` is skipped, newest first, when the server skips the build auth key check); a pinned build is
claimed as given.
Both commands refuse to send when the run's remaining time is less than the request's bound plus a 10 s margin
(for a delete, the server's 30 s pending-delete bound, so 40 s must be left after the list; for a create, its 30 s
answer bound): nothing is sent, and the error names the least `Deadline` that would have worked. If the request was
sent and its answer did not come before the run ended, the command prints exactly `create sent, no answer: the
character may or may not have been created: list again` or `delete sent, no answer: the character may or may not be
deleted: list again` and exits 32: list again, never retry blind. A link that dies after the send is the same kind of
answer with its own words (`create sent, then the link was lost: ...` or `delete sent, then the link was lost: ...`)
and the same exit 32.

`forever-agent forever walk-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
runs the same login and stays in the world for `--stay` seconds (default 8 s), then prints what the client understood
of the world around the character: the object count by type, the update statistics and the five nearest units with
entry, name, level, health and distance. It then walks `--dx` and `--dy` yards from the spawn point (default 8 and 0,
`CMSG_MOVE_SET_FACING`, `CMSG_MOVE_START_FORWARD`, `CMSG_MOVE_HEARTBEAT` every 500 ms, `CMSG_MOVE_STOP` inside
0.5 yards), stands `--pause` seconds (default 3 s), walks back, prints where the client believes the character is and
logs out. Without `--navmesh` the walk is straight-line only: no pathfinding, and Z is interpolated between the start
and the target, so use it on flat ground. With `--navmesh DIR --to-x X --to-y Y --to-z Z` the command plans a route on
a Forever mmaps folder (`internal/navmesh`, the v16 format) to that absolute target on the current map, prints the
route kind, corners, length and nav flags, and walks the polyline as one continuous movement: one start forward, a
facing packet where the route really turns (the 4-yard floor samples between corners shape Z and get no packet of
their own), heartbeats every 500 ms, one stop. `--parent MAP=PARENT[,..]` applies the server's instance rule (for
example `--parent 389=1`), so a walk inside an instance uses the parent terrain map's tile files. Z comes from the mesh along the route, not from a
straight line between two points. Creature and player names come from `CMSG_QUERY_CREATURE` and
`CMSG_QUERY_PLAYER_NAMES`, cached per entry and GUID, at most four batches of each per run with a 3 s deadline per
answer.

`forever-agent forever map-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
enters the world, settles `--settle` seconds (default 3 s), prints the map, the difficulty and the position, sends
one `CMSG_AREA_TRIGGER` when `--area-trigger ID` is given, then stays `--stay` seconds (default 60 s) and follows
every map change the server makes: each one prints the from and to map, the reason, the difficulty, the client's own
position and the objects the new map has five seconds later. A transfer the server aborts is printed with the
reason's name, the argument and the map, and the character stays where it was. The command is how a far teleport, a
teleport to another continent and an instance entry are checked live; the layouts and the sequences are in
[docs/forever/MAPCHANGE-R1-NOTES.md](docs/forever/MAPCHANGE-R1-NOTES.md).

`forever-agent forever path-check --navmesh DIR --map N --from X,Y,Z --to X,Y,Z` is offline (no login): it prints the
same route's kind, every point with Z, the length and the nav flags the corridor crosses. `--server-filter` uses the
server's own player filter (ground, steep, water, lava and road, nothing excluded) instead of the safe walk filter
(ground, steep, water and road; lava and slime excluded), and `--parent MAP=PARENT[,..]` applies the instance map's
parent-tile rule (`--parent 389=1`); a bare number is refused, because the pair names which map it is for. The real-data checks live in `internal/navmesh` and run when
`FOREVER_MMAPS_DIR` names a Forever mmaps folder; see [docs/forever/NAVMESH-R1-NOTES.md](docs/forever/NAVMESH-R1-NOTES.md).

`forever-agent forever route-walk --plan FILE --navmesh DIR --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
walks a whole dungeon plan live and checks every planned point against a confirmed position. It enters the plan's
instance (`--enter tele` teleports to the plan's `entrance_tele`; `--enter none` expects the character placed),
optionally turns GM mode on (`--gm`, which also skips the instance's access rules), and then walks each selected leg
(`--legs 1-3,tunnel`) as the plan defines it: every `via` waypoint in order, then the leg's `to`, planned from where
the previous hop actually ended with the same Forever navmesh `path-check` uses, and walked with `walk-check`'s real
movement (`CMSG_MOVE_START_FORWARD`, a heartbeat every 500 ms, `CMSG_MOVE_STOP`). The plan's avoid boxes are given to
the planner. Every sample (default one stop per 3 s of running) records the planned point, the confirmed position,
their distance, the Z difference and the time; a leg is `PASS`, `DEVIATED` (a waypoint or a mesh snap over
`--max-deviation`, default 2 yd, from the plan's own point), `UNCONFIRMED` (the server did not confirm the stop: no
echo of the request's marker, no complete `.gps` block, two reads that disagree, or an answer that cannot be tied to
the stop), `STUCK` (the confirmed position moved under `--stuck-step` for `--stuck-after`, default 6 s at 0.5 yd),
`FELL` (the confirmed Z is
`--fall-drop`, default 2 yd, below the plan), `LIQUID` (a magma/slime navmesh floor within
`--liquid-above`/`--liquid-below` of the path), `NOPATH` (no complete route, or a `gm_cross` hop), `INCOMPLETE` (the
deadline or a cancel ended the leg or the run) or `FAIL`. `--leave tele` (the default) teleports the character back out to the plan's `outside_tele` after the last leg,
before GM mode goes off: a level-1 character left standing among the packs aggroes the moment the GM flag clears,
and the server then refuses the logout. The
position is the server's own when the account can run the core's `.gps` (the default, `--confirm gps`: the command
selects the character, marks each request with a self-whisper it waits to see echoed, sends `.gps` and reads the
`X: Y: Z:` line, and confirms a stop only from two such reads that agree); `--confirm client` records the last movement
packet instead, still treats a server correction (a teleport) as a deviation, and cannot see a silent stall. The
command prints a per-leg table, writes `route-walk.json` beside it with `--out DIR` (or prints JSON with `--json`),
and exits 12 when a leg did not pass. The thresholds are flags; the full contract and the live run are in
[docs/forever/ROUTEWALK-NOTES.md](docs/forever/ROUTEWALK-NOTES.md).

`forever-agent forever chat-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
settles for `--stay` seconds, optionally says `--say TEXT`, whispers `--whisper NAME:TEXT`, sends a text emote
`--emote TEXT`, then prints every chat line it receives for `--listen` seconds (default 10 s) and logs out. The
message checks the server makes (non-empty, at most 511 bytes, valid UTF-8, no control characters or newlines, no
`|` hyperlink escape, and a leading `/`, `.` or `!` is a command, not a line) run before anything is sent and fail
with a named error. Every packet goes out in the character's race language (Common for a human, Orcish for an orc,
and so on): the server refuses a language the character has not learned. `--watch NAME` prints that player's position
from the world model once a second for `--listen` instead of the chat log (the name lookup is the server's
`CMSG_QUERY_PLAYER_NAMES`), and `--capture-dir DIR` writes every packet the combat, chat and party handlers see
into a NEW run directory under DIR (`<UTC start>-<4 random bytes>`, exclusive files, at most 8192 packets and 32 MiB
per run): keep it outside the repository, the payloads are raw server bytes. Text from the server is sanitised: no control
bytes survive, output is valid UTF-8 and never longer than the display bound, and a `|H…|h` hyperlink prints its
visible label.

`forever-agent forever party-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
either invites `--invite NAME`, or waits for an invite from `--accept-from NAME` and accepts it or from
`--decline-from NAME` and declines it; `--wait-join DURATION` (with `--invite`) waits that long for the invitee to
join before the member states and the party line; `--uninvite NAME` (with `--invite`) waits for the group to form,
says the party line and uninvites that member (this core disbands a two-member group instead of sending
`SMSG_GROUP_UNINVITE`, so with two members the group simply ends). The party model as it changes (members, leader, loot method) is printed, the
members' full state is asked for, a line is said in party chat, and the character leaves and logs out (`--stay` is
how long it stays in the group). The command fails with a named error when the outcome is not the one asked for: an
invite the server refused, no invite, an invite that says it cannot be accepted, or no party update after the
accept. A roster over five members is validated by the same rules, then parsed and bounded and reported as
`raid: not handled in this round`; a roster that is not a normal party type, has an empty party GUID, lists an empty
or duplicate GUID, does not list this character exactly once at its own MyIndex, or names a leader it does not list
is printed as `invalid roster: <reason>`, ends the party in the model, and nothing is sent for it (no party line, no
member stats, no uninvite, no leave). A roster that does not belong to the transition that produced it (the accepted
invite's leader, or the name of an invite the server confirmed) is printed as `unexpected roster: <reason>` and is
not acted on either. A roster that arrives before the client knows its own GUID (a character that logs in already
grouped) is held, not judged, and applied when the GUID arrives; a map change drops a held roster, because it belongs
to the map the character left and the server sends the party update again for the new one.
`--capture-dir DIR` works as it does for chat-check.

`forever-agent forever death-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
settles for `--stay` seconds (default 3 s) and then waits up to `--death-wait` (default 5 min) for the character to
die: the command never kills it, a server-console command or a mob does. Once dead it prints every step with its time
and does what a player does: `CMSG_REPOP_REQUEST`, the ghost aura and the graveyard teleport (a graveyard on another
map is the map-change round's transfer: the client follows it and arrives as a ghost there with its death state
intact; a dungeon's entrance graveyard revives the character at the teleport, which the server does), the corpse
location query, a run back to the corpse (straight line, or a navmesh route with `--navmesh DIR` and the instance rule
`--parent MAP=PARENT[,..]`) at the ghost's own run speed, the announced reclaim delay (a reclaim before it is
ignored by the server, so the client waits it out), `CMSG_RECLAIM_CORPSE`, and the resurrection. A corpse on another
map is reported as exactly that ("walk to its entrance"), because this client cannot walk between maps; a corpse the
server places on a transport is reported as on a transport and is neither run to nor reclaimed, because the answer's
coordinates are the transport's offsets. The ghost's
movement changes are acknowledged the way a real client does (`CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK`,
`..._SWIM_...`, `CMSG_MOVE_WATER_WALK_ACK`), because the server counts them; a resurrection that lands during the
corpse run stops the walk and ends the flow as a success with no reclaim.
`--accept-resurrect` waits up to `--resurrect-wait` (default 60 s) for a `SMSG_RESURRECT_REQUEST` and accepts it
(with no offer it falls back to the release and the corpse run); `--accept-resurrect-from NAME` makes that stricter:
only an offer from that unit is answered (the default accepts any GUID, because the server decides who may offer).
An accepted offer follows the server's teleport to the offerer, including a map change. `--spirit-healer` uses the
spirit healer at the graveyard instead (the minimal gossip interaction the capture shows: talk, the SpiritHealer
option, select, activate, close), and follows the server's teleport to the corpse's graveyard afterwards if it is on
another map. A server that revives the character directly is detected from `SMSG_DEATH_RELEASE_LOC`'s map id -1 and
reported as `revived by the server`. The summary prints the final phase, the corpse, the announced reclaim delay, how
the character came back, and how many other units the model saw resurrect. `--capture-dir DIR` works as it does for
chat-check.

`forever-agent forever loot-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME [--entry N] [--roll need|greed|pass] [--take N] [--money=false] [--at x,y,z] [--at-range YARDS] [--range YARDS] [--stay DURATION] [--fight-for DURATION]`
settles for `--stay` seconds, finds the nearest lootable target within `--range` (default 30 yd): a dead creature of
`--entry`, or a chest when no entry is given. A chest is only opened when the choice is unambiguous: more than one
game object of its entry in range and no `--at` is a refusal (exit 12, nothing opened, each one listed by position
and distance), and `--at x,y,z` picks the one nearest that point within `--at-range` (default 2 yd). A living
creature of `--entry` is killed first (the fight round's own flow), then looted. The window is opened with `CMSG_LOOT_UNIT` for a creature and with `CMSG_GAME_OBJ_USE` for a
game object, followed by `CMSG_GAME_OBJ_REPORT_USE` as a real client does; the command prints the method, the
threshold, the copper, one row per item (slot, id, count, slot type, whether it needs a roll) and every group roll
(it sends `CMSG_LOOT_ROLL` only when `--roll` names a vote and the roll's own vote mask allows it, so need is never
sent unasked). It then takes the money (`CMSG_LOOT_MONEY` unless `--money=false`), takes the items the window allows
(up to `--take`, 0 meaning all), releases with `CMSG_LOOT_RELEASE` and prints what the server removed and whether
the release was confirmed. A run that ends after a window was accepted but before its own release (its deadline, a
parse error, a failed send) still gives the window back: one release is sent on a cleanup context of its own (10 s),
and a failure of that cleanup is printed separately from the run's own error. A refused window is a named error
(`there is no loot`, `you are too far away`, ...); a creature with nothing to loot is answered with silence by this
core, which the command reports as "no loot window". A roll's result is waited for as long as the start allows plus
3 s, capped at 90 s: the start's own time is the server's choice, and the core's own roll timeout is one minute.

`forever-agent forever object-use --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME --entry N [--at x,y,z] [--at-range YARDS] [--range YARDS] [--stay DURATION]`
settles, looks for the game objects of that entry within `--range` (default 40 yd), records the object's state and
flags, sends `CMSG_GAME_OBJ_USE` (and the report-use packet a real client follows with) and prints the state and
flags after the object's own values update — a door or lever reads ready (closed) before and active (open) after; a
chest also opens a loot window, which is read and released without taking anything. When more than one object of the
entry is in range the command refuses to guess: it lists them (position and distance only, no identifiers), sends
no use and exits 12, so a script does not walk up to somebody else's door. `--at x,y,z` chooses the in-range object
nearest that point within `--at-range` (default 2 yd); a miss is a named error. One object in range keeps working
without `--at`.

`forever-agent forever cmd-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
sends chat-command lines on purpose: each `--line TEXT` (repeatable, sent in order, e.g. `--line ".server info"`)
goes out as a say line that starts with `.`, and the system lines the server answers with are printed as `cmd-r:`
lines. The account must hold the command permission, so the run first sends one harmless probe (`.gm` with no
argument: the server's non-mutating gm form) and refuses to send anything else unless the probe's specific
acknowledgement arrives (the `gm on`/`gm off` entry of the command's own help block, which the server lists only to
an account holding the permission) and the probe is not said back as the character's own say chat anywhere in the
run. A notification, a MOTD, an announcement or "no such command" is not an acknowledgement; a say echo at any
point, even after other lines, resets the guard and stops the run before another line is sent. The probe changes
nothing, so no GM mode is ever switched on or off. A reply is matched to its command by a fresh token
(`#...`) on every line a `.tb` command prints, so interleaved system lines are ignored and stay in the chat log; a
core command has no token, so its reply ends at a quiet window (1.5 s) or a line cap, and its run is serialised with
every other command. An untagged refusal is taken for a tokened run only before its first tokened line and only
after the quiet window of an earlier tokenless run has passed. The core's refusals are named: `Command '…' does not exist`, `Incorrect syntax.`, an unknown
subcommand, an ambiguous command, a typed argument the server refused, or the say echo of a missing permission.
`--tb-state TARGET` reads the typed state table of a target (`party`, `party <leader>` or a bot's name) after the
lines and prints one row per bot (role, class, level, health, power, state, movement, target entry, last order and
the rest gate); `--quiet` and `--timeout` override the collector's defaults. The typed order/role/master/party/threat
calls and `WaitRested` live in `internal/forever/command/tbcmd`.

`forever-agent forever addon-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME
--prefix PREFIX` is the add-on message transport (`internal/forever/addonmsg`): it replaces this client's registered
prefix list (the unregister-all packet, then the full-list registration), optionally sends one `--send TEXT` on
`--channel` (`party` — the default —, `raid`, `instance`, `guild`, `officer` or `whisper:NAME`), then prints every
add-on message that arrives for the registered prefix during `--listen` (default 10 s) and logs out. Sending on a
chat channel is not supported yet: the core looks a channel up by the GUID in the packet, and this client joins no
channel, so it has none; `channel:NAME` is refused with that message. The server's limits are checked before
anything is sent: a prefix is 1..16 bytes of valid UTF-8, a text at most 255 bytes (it is not required to be UTF-8:
the core reads it as bytes), one registration packet holds at most 64 prefixes, and a whisper target is a character
name. An incoming add-on message is a `SMSG_CHAT` with language `LANG_ADDON`/`LANG_ADDON_LOGGED` and a non-empty
prefix; only prefixes this client registered reach the caller, the queue is 256 messages (oldest dropped, counted),
and arrival order per (prefix, sender) is kept. The delivered text is the server's exact bytes; every printed line
is a bounded display form instead (valid UTF-8 only, no control, line-separator or bidi characters, a tab shown as
`\t`), at most a fixed number of receive lines are printed and the totals follow at the end.
`--invite NAME` or `--accept-from NAME` form the two-client party with party-check's invite/accept flow and keep it
for the whole run (the add-on party proof), leaving it before the logout. Every packet is also written to
`--capture-dir` when given, as for chat-check.
`forever-agent forever fb-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
drives the bots of a server running the WoW Forever Bots module. It probes with `.bot about` (that command needs no
game-master permission, and a say echo proves the module is absent), fixes the reply language, adds the bots named by
`--add CLASS[:ROLE]` (default `warrior:dps`) with the module's chat command and waits for the server to group them,
then reads the roster and one bot's detail through the module's add-on protocol: the `PBC` prefix on the party
channel, a 6-field envelope, 200-byte client parts, 64 parts and 15,360 bytes per body (the module's own bound: its
64-part reassembly cap times its 240-byte parts), answers matched by a random-start sequence number. Only the
module's own answers are read: a frame under `PBC` must arrive as a **whisper** add-on message sent by this
character's own GUID, because that is how the module sends one; anything else is dropped and counted, never parsed.
Nothing on the wire carries a version, so the shapes are versioned: a roster record with fewer than 19 values, a
count that disagrees, a detail answer for another id, a malformed or oversized frame or an answer of the wrong type
fails **that request** with a named error and the protocol stays on; no answer in 6 s turns the protocol off for
the session and the run says `pbc: unavailable (<reason>)`, printing the roster from `.bot list` instead. Every
`.bot` command needs this session's successful `.bot about` probe first, whichever path sends it; on a server
without the module that probe is said once as ordinary chat and nothing else is sent. `--order WORD` is whispered
to every added bot and only a whisper from that bot, in the party (or on the protocol's authenticated roster) and
matching that order's own confirmation phrase is taken as its answer; a refusal is reported as one.
`--order party:WORD` is said in party chat (confirmed by rereading the bot's last rule), default `follow` then
`stay`. Real players in the client's party receive a party-word order as ordinary chat; the module can also answer
in system chat after a `sys` request, which this client never sends. `--dismiss NAME|all` (default `all`) logs the
bots out and waits for the party to drop them.

`forever-agent forever fb-dungeon --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME
[--dungeon NAME] [--role tank|healer|dps] [--watch SECONDS] [--leave=false]` runs a dungeon with the module's bots.
It probes and fixes the language as fb-check does, says `.bot lfg [ROLE]` and waits until the **party model** lists
five members (the module's own one-line answer is parsed and printed), queues `--dungeon` (a short word like the
default `rfc`, a name, or `any`) with `.bot dungeon NAME [ROLE]`, and then polls the module's finder through the
`LFG_REQ`/`LFG_RESP` protocol pair every 2 s. `LFG_RESP` is `phase(|name|seconds waited|form wait|members|party
size|can leave)` with phase `idle|forming|entering|leaving|inside`; the run counts the entry as **inside** only when
the module says `inside` **and** the client's own map change put the character on the instance map (tied to the
dungeon's map id for the default dungeon table; a map change that never comes is bounded, and neither signal alone is
taken as proof). It then says `.bot lead on` and prints the module's lead answer: the module expects the player to
follow the leading tank, so the tank holds when the player falls behind — this client does not walk, so it stays at
the entrance and reports the tank's own party lines (`waiting for mana`, `waiting for you`). During `--watch`
(default 600 s) it reports the finder's phase, every bot's hp/mana from the protocol's roster, deaths (a grouped bot
at 0 % hp), the module's `EVENT_PUSH` events and party chat. It ends with `.bot lead off`, `.bot dungeon leave`
(unless `--leave=false`), waits for the client's map change out (bounded), and dismisses every bot. An answer whose
shape this client does not read fails softly, naming the unreadable answer; no state is invented from it.
`forever-agent forever dungeon-run --plan PLAN.json --party TANK,HEALER,DPS,DPS,DPS --character NAME --dry`
runs one dungeon's machine with no network: the five-bot party is checked, brought to the portal and into the
instance, walked along the plan's legs, its packs pulled one at a time and rested between, and the bosses killed; the
output is the order list and the state sequence, and `--out DIR` also writes the run's bt-trace and its
`<run_id>.forever-run.json` summary (named by the plan's dungeon id, never by a path or a character name).
`--from-leg`, `--legs a,b`, `--enter tele|portal|none`, `--reset`, `--max-minutes`, `--max-wipes` and `--practice`
narrow a run; `--max-wipes N` is the wipe budget (the Nth wipe ends the run, `0` allows no wipe at all, `-1` is no
budget). Every plan field the driver does not run is refused before anything is sent, with the field's path, and
the command exits 2; the loader test records, per file under `dungeons/`, whether it loads or which fields refuse it,
and no shipped plan loads today. The dry walker is a straight line and the fights are scripted: it proves the
machine's order list and a plan's shape, not the game.

Without `--dry` the same command runs live: the connection flags are world-check's (`--rest`, `--rpc`, `--ca`,
`--server-name`, `--build`, `--account`), `--navmesh DIR` gives the walker its route, and the command waits for the GM
probe before it sends anything. The plan and the leg selection are checked before the login (a refused plan is exit 2
and no session). `--enter none` runs the party where it stands, the open-world mode;
`testdata/open_world_northshire.json` is a three-pack map-0 test layout for it, built from the public starting-area
points this repository already uses. Exit codes: 0 clear, 2 usage or a refused plan, 7 link lost, 5 any other stop.
The live mode has not been run against a server yet; what the first attempt must check is the last section of the
round's notes.
`forever-agent forever dash-bots --url URL --user NAME [--pass-file FILE] [--dash-insecure-http] [--watch [--interval 2s]]`
reads the roster dashboard of a server: the typed login, every logged-in bot (class, level, health, power, map, zone,
instance, position, target, group, kind and state) and the heartbeat. Without `--watch` it prints that roster once.
With `--watch` the first read is the baseline and every later read prints only the changes: a bot that died, a bot
whose state says moving but whose position has not changed for `--stuck-after` (default 20 s), a bot in combat with
no bot of its group in combat for `--lone-combat-after` (default 5 s), and a bot more than `--group-distance`
(default 60 yd) from the centre of its group. Each condition is reported once and fires again only after it cleared.
`--record FILE` (with `--watch`) appends one JSON line per good read — the read's time and the roster exactly as it
was decoded — so a watch's whole history can be replayed offline; a line's names are the dashboard's own.
The password comes from the private `--pass-file` or `FOREVER_DASH_PASSWORD`, never a flag; `--timeout` (default
15 s) bounds every request, and a 401 costs one re-login.
The dashboard's URL must be **https**, or **http to a loopback IP literal** (127.0.0.0/8 or ::1) or the exact
name `localhost`, which the client dials as 127.0.0.1 itself: the login sends a GM password and the session cookie,
and a hostname must not be able to move that traffic somewhere else between the check and the login. Every other
plain-http name needs the explicit `--dash-insecure-http`, which prints a one-line warning and
makes the command, the decision pull and raid-walk accept it. A roster record that omits or zeroes a field the watch
rules read (hp, max_hp, map, instance, x, y, z, state) is skipped and reported on stderr, never read as a death, a
stuck flag or a distance; one answer is capped at 8 MiB. Names and the dashboard's strings are sanitised before
they are printed. Exit codes: 0 printed or watched, 1 a login or read failure (five failed reads in a row end a
watch), 2 usage. The rules, the API shape the client accepts and the live steps are in
[docs/forever/DASH-R1-NOTES.md](docs/forever/DASH-R1-NOTES.md), and the round-2 fixes (the http boundary, the
required fields, the one login for concurrent 401s and the size cap) in
[docs/forever/DASH-R2-NOTES.md](docs/forever/DASH-R2-NOTES.md).

`forever-agent forever dungeon-watch --leader NAME --plan PLAN.json --character NAME`
watches a run a server module that runs dungeon plans leads by itself. The party is the module's own: its bot
commander stands outside it and the tank leads, while this character is only an outside GM observer and never the
party's commander or a member. The client does what a run does up to the entry (PREPARE checks the roster — the tank
is the party's leader or the bot the run names — then the portal or the teleport, with the same gates), starts the
module's run with one typed command, and from then on sends no order — an order would pause the run. A setup
answer is read whole or not at all: the `<verb>-begin` marker, a `status=` line per addressed character, and an end
line with `records=`, the party it resolved (`target=party:commander:<name>`, `target=party:<tank>`) and its own
counts are all required, every record's `status=` must be one of the verb's own outcomes (`started`/`refused`,
`revived`/`refused`, `ok`/`failed`) with the end's counts as their tally, and the target must name what the command
addressed. An end-only acknowledgement, or a report missing any of those parts (or carrying a status or count the
verb never writes), is a report that proves nothing and is refused — never a success — and an entry
answered `no-trigger` (a party outside the entrance's trigger box) is retried once as `enter party map <plan map>`
instead of failing. The watch brings the party's own commander — the bot its roster names, never this character — to
the tank's side before PREPARE, and again after an entry lands the party: the module marks a bot whose master stands
on another map or instance `stuck=blocked:bot in an instance` (the tank's `dm=othermap`), and PREPARE and the start's
readiness rule both refuse that block. The move is the module's own `tele <commander> to <tank>` and nothing else:
the watching character is an observer and is never teleported in the commander's place, so a roster that names no
commander ends the watch (`commander:unnamed`) instead of falling back to a move of this character. The watch waits up
to 30 s for the module's report to show the commander with the tank,
logging where it stood and, if the module refuses the line, that the watch goes on without it. The same-map check
compares the party's map with the plan's, not the commander's, and the start's gate waits out the party's own
teleport: while a member is not on the plan's map — a member on the old map with its engine off is a teleport in
flight, not an engine-off refusal — it holds for up to the entry budget (90 s) and then reports its existing error
(`start:party-map`). A plan that carries the format's in-fight run fields
(`kill_first`, `cc_priority`, `cc_always`, `tank_min_hp`, a boss's `tactics`) loads unchanged: those belong to the
module's run. Every `--poll` seconds
(default 2, at most 30) it reads the module's run record and its event ring, prints the progress (state, step,
leg, pulls, deaths, wipes, each pause with its reason) and the bosses the instance report gives, and keeps five
safety budgets: a watchdog (`--watchdog-minutes`, default 10) that pauses a run whose event ring stopped
growing and ends the watch; a pause budget (`--pause-minutes`, default 5) that reports a pause the module never
cleared and ends the watch as `PAUSED-STUCK` (the watch sends no resume on its own, and the watchdog covers a
paused run too); a time budget (`--max-minutes`); a wipe budget (`--max-wipes N`: the (N+1)th wipe
ends the watch, `0` allows no wipe at all and `-1` is no budget); and the practice rescue, which, when a run
has been paused on a wipe for 300 s, sends one practice `revive party` and one checked `resume`, at most eight times. The rescue fires only for the
wipe's own pause: the pause event right after the wipe whose reason names the wipe or the dead party, never a later
pause for another reason. A pause the rescue still owns is nobody else's: the watchdog and the pause budget both
stand aside until the rescue's 300 s threshold, and for 600 s more after a revive while its `resume` may be gated
(a `--watchdog-minutes` shorter than the rescue can no longer end a run that is about to be cleared); a `resume`
that lands restarts the watchdog's event clock. A death event is attributed only from its `why=`: a name that is no
current party member is recorded as it arrived, never counted for a member and never grouped by the fight's unit
id. A status read and an event read each keep their own blind rule: three failed reads of the
same kind and 30 s without a good one end the watch with `blind`, whichever read it is, and every end logs its
reason — a blind one names the last read error it saw. At the end it sends a stop
(a run that ended by itself needs none), waits (bounded) for the module's state to reach stopped, and writes the
run's usual trace and `<run_id>.forever-run.json` summary with a `watch` block holding the module's own counters,
the per-leg and per-step evidence, the deaths, the pauses and the instance's own encounter packets
(`encounters`: `start`, `end` with its success, duration and group, and `boss_kill`), and the trace carries
`boss_pull`, `boss_kill` and `boss_wipe` at those packets' own times. While encounter packets flow the watch does not
read `.tb instance`; the report is the fallback for an instance whose script sends nothing (the classic Ragefire
Chasm bosses today), and the forced read at the end still happens. The stop and that wait run on a context detached from
the caller's: an interrupt or the caller's deadline still lets the module receive the stop, and the phase is bounded
by 600 s on the run's clock and 600 s of real time when the watch ended on its own, but by 60 s in total after an
interrupt or the caller's deadline (each call by 10 s); when the run has not reached stopped by then the result and
the summary say the stop was not confirmed (`stop_unconfirmed`). The same block is what the
report card reads: after every run the watch writes `<run_id>.report-card.txt` and its JSON beside the summary and
prints the verdict, so a weak spot is found without a person reading the log. A lost link ends the watch at once
(exit 7); `--dry` runs the whole watch over the scripted module's run. The live mode has not been run against a
server yet; the last section of the round's notes is the order of the first checks.

The watch also records the fight itself. Every `--frame-every` seconds (default 2, at most 60, `0` turns frames off)
it reads `.tb state party` and `.tb threat party` — two reads, never an order — and writes one trace frame: each
party member's role, movement order, stuck state, meal and rest, life state and distance to the leader, its auras
from this client's own model, and each mob's health, position, threat victim and the top of its threat list. The
frame's first sight of a bot writes its party record (guid, role, class) and of a mob its registry record (entry and
name), so one record stands alone. A guid is one text everywhere in the record, frames, party rows, threat entries and
combat rows alike: `0x` and 32 upper-case hex digits. A party member this client's model has not resolved yet is a
frame row with no guid (its role still names the slot), never its character name, so a member's identity does not
change under a frame once the model knows it. The threat a frame carries comes first from the client's own pushed
table (`SMSG_THREAT_UPDATE` and its three kin, `Combat/ThreatManager.cpp:859-893`), and the module's `.tb threat`
read is the fallback: no fresh pushed list in the last 5 s means a read, and while pushes flow that read is at most
one per 10 s instead of one per 2 s tick. Every tick (frames on or off) it drains the session's own combat log into
`<run_id>.combatlog.jsonl`, one row per line in the trials runner's raw row shape (`seq kind time mono_ms
source{guid,name} target{guid,name} spell_id amount`, plus `reason` on a spell_fail), so the normaliser, the trials
runner and the bug hunter read one file. The core-gains round's kinds join the same file: `interrupt` (spell id is
the interrupting spell and `interrupted_spell_id` the stopped cast), `dispel` (`dispelled` lists the auras),
`absorb` (`absorbed_spell_id` is the incoming spell), `miss`, `resist`, `immune`, `instakill` and `environmental`.
A row whose packet carried the advanced log's unit block also has `sample` (health, power and power type), and the
client asks for that form once after world entry (`CMSG_SET_ADVANCED_COMBAT_LOGGING`). The watch's summary counts both (`frames`, `combat_rows`,
`combat_dropped`); a frame read that fails is one line and the next tick retries, and the watch still ends on the
same reasons as before. A combat-log page is all or nothing: a write that fails leaves the page in the cursor and the
next tick retries it, so no failure can skip rows the summary would call complete (`combat_error` names the last one).
`--combat-max-mb` bounds the file (default 64): a page that would cross it is not written and the summary says
`combat_truncated` with `combat_bytes`, the size the file stopped at. The trace's events carry the watch's own
anchors: `run_start` at the run-start action's reply time (the module's clock zero, which a map reader uses to place
the module's `at_s` on the trace's clock) and one `wipe` at the module's own second for every wipe it reports.

`forever-agent forever run-report RECORD [--json] [--out DIR]`
builds the card of a saved `<run_id>.forever-run.json` offline and prints it; `--json` prints the machine-readable
card (stable field names, the thresholds it was built with, one entry per leg, the deaths and pauses, and the flags
with their evidence), `--out DIR` writes the `.report-card.txt` and `.json` there, and `--repulls`, `--member-deaths`
and `--slow-factor` change the three thresholds (defaults: more than 2 re-pulls at one step, one member with at least
3 deaths and 2 more than the next, and a leg over 2× the plan's recorded time). A death whose event named no party
member is flagged apart as `unattributed-deaths` with its evidence. `forever-agent forever run-report
--compare A B` takes two cards or two records and prints what got better or worse per leg (seconds, pulls,
re-pulls, deaths, wipes) and per flag. The plan's recorded leg time is read from the record when it carries one;
today's plan format records none, so the slow-leg flag stays quiet until a record has it.

`forever-agent forever run-map DIR [--plan FILE] [--navmesh DIR [--parent MAP=PARENT]] [--out NAME.svg] [--html] [--names]
[--floors auto|off|z1,z2] [--width PX] [--from-s N] [--to-s N] [--force]` draws a saved watch (the folder `dungeon-watch
--out` wrote, or one of its trace files) as a top-down map: one SVG per floor (`<run_id>.svg`, `-f2` ... when the map has
several) and, with `--html`, one self-contained HTML replay with a time slider (no script, font or image from outside the
file). The routes are labelled by party slot (`--names` uses the characters' names), with the plan's route and pull groups,
the creatures seen, boss kills, legs, pulls, deaths, wipes, pauses and practice rescues. `--navmesh` gives the
floor background; without it, or in a build without cgo, the map says so. Output names are one file name written into DIR
(no path, lowercase `.svg`), and nothing is overwritten without `--force`. `dungeon-watch --map --out DIR` writes the same
two files after the summary; a failure there is one log line and never changes the watch's exit. See
[docs/forever/MAP-M2-NOTES.md](docs/forever/MAP-M2-NOTES.md).

`forever-agent forever gm-walk --follow NAME --character AGENT [--marks-from gm|any] [--out FILE] [--module-out FILE] [--force]
[--dry FILE]` records a GM character's walk as world data: the agent follows the GM, samples the GM's own object
position, and turns the GM's whisper (or nearby say) mark words (`point <name>`, `zone <name> <radius-yd>`, `split`) into roads (thinned
polylines with the map id), points and danger circles in a versioned JSON file with no character names (defaults use the map id and a UTC timestamp). `--module-out`
(and `forever gm-convert --in DATA --out COPY --module-out MODULE`, no login) renders the module's shape: one row per
road hop for the `handcrafted_road` table plus the zone/point proposals. `--dry FILE` replays a hand-made recording
through the same thin/mark/format path with no network. See [docs/forever/GM-WALK-NOTES.md](docs/forever/GM-WALK-NOTES.md).

`forever-agent bughunt [--rules LIST] [--names F] [--dispel F] [--baseline DIR] --out DIR INPUT...`
hunts bot misbehaviour in what a watch or a trials repeat recorded. An input is a watch folder (its trace, party
records, `<run_id>.combatlog.jsonl` and `<run_id>.forever-run.json`), a watch trace file, or a trials repeat
(`samples.jsonl`, `events.jsonl`, and the recorder's `raw-ta.jsonl` when it has one); folders are searched
recursively, and a watch folder or a repeat is one run. It reads the watch's frames (F) and its combat log (C), and
a repeat's raw rows are the same C shape on the repeat's clock. `--rules` picks the rules (default: every rule this
build has), `--names` and `--dispel` name the spells and give the dispel types — both are live-checked data files,
never guessed: a names file is a JSON object of id to name (or a spells.json's `by_id`, whose values may be
`{"name": ...}`), and a dispel file is a JSON object of id to `magic`, `curse`, `disease`, `poison` or `never` (or
the core's `spell_template.dispel` numbers 0-4); only debuffs count, so an entry is kept when the names file marks its id
`"harmful": true` or the entry does. An id the names file does not name is `spell <id>` and never
matches a buff, AoE or heal name; an id the dispel file does not list is not a known debuff. A rule whose input a
run does not carry is a skipped check (as is a stretch the combat log does not provably cover, and a malformed combat-log
line is an error naming the file and line) with its reason in `counts.json`, never a hit from a guess: no combat log
skips rules 1, 2, 4, 8 and 11; no names skips 2, 4 and 8; no dispel types or no known classes skip 15; no meal report
skips 13. The command writes `hits.jsonl` (one hit per line, the Python checker's own keys), `counts.json` (every
selected rule's hits, runs with hits, skipped checks and the reasons), `tickets.json` and `tickets.md`. A ticket is
one (rule, key): its id is `BH-<rule>-<8 hex of sha1(rule|key)>`, so the same finding keeps its id across nights;
its row is `{id, title, rule, key, count, runs, first_seen, deaths_near, evidence (at most 5), labels [bughunt,
rule-N, role], status new|known|reopened|gone, cost}`. `--baseline DIR` reads that directory's `tickets.json`: a ticket it
holds is `known` (and keeps its first night; `reopened` when it was `gone`), one it lacks is `new`, and a baseline
ticket the new run no longer sees is `gone` only when its rule ran with all its inputs (a skipped or unselected rule
leaves its tickets as they were). `tickets.md` ranks the tickets by summed cost, worst first. The rules are the Python bug hunt's
own (1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 13, 14 and 15; 7 waits for pet data and 12 for raids), with the Forever inputs
the plan's table gives. `BUGHUNT_PY_CHECKER=checker.py` runs the optional parity test against the Python checker on
one repeat (rules 4 and 8 must agree on every key).

For Turtle WoW servers use turtle-agent. The text below is turtle-agent's README with the name changed and still
describes the 1.12 client.

> Use it only on servers you run or are allowed to test on. It has no Warden emulation and does no rendering.

## Quick start

Requirements: Go 1.27.1 or newer (see `go.mod`), a C++17 compiler for cgo (g++ or clang), and a server whose realmd
and world ports you can reach. `CGO_ENABLED=0` still builds; `path_to` and `walk_path` then answer that the binary
has no navmesh engine.

```sh
make build                      # bin/turtle-agent
make test                       # gofmt check, go vet, builds, go test -race ./...
```

Create the settings file (default `<user config dir>/turtle-agent/settings.json`, mode **0600**; the agent refuses a file
other users can read) with your server and account in it — placeholders below, never real values:

```json
{
  "realmd": "<server>",
  "accounts": { "<account>": "<password>" },
  "rpc": "127.0.0.1:8765"
}
```

Only `realmd` and `accounts` are required. `rpc` must be a loopback address. See
[the settings reference](docs/okf/getting-started/settings-reference.md) for every key.

Every realmd login waits out the first minutes of each UTC hour: the core's login throttle
(`AuthSocket.cpp:439-457`) wrongly counts every account seen from the IP between hh:00:00 and hh:03:00, so a login that
starts from hh:59:57 (3 s early, for clock skew) to hh:03:05 is held until hh:03:05, plus a random 0-20 s so held logins
do not all fire at once, and logs `login: waiting <s> s for the realmd hour-start window`. The callers that start a login
child (raid-walk, plan-scout, fleet) allow for the hold in their own waits. The window assumes the database's `NOW()` uses a
whole-hour time zone. Set `TA_LOGIN_HOUR_HOLD=0` to turn the hold off.

Raid-walk rosters (`tb-raid-roster` v2) may name a `spec` per batch; for a hunter it also sets the talent build and reads it back
(see [the roster format](docs/okf/raids/roster-format.md)). Known limit: the read-back checks that the spec's tree has the most
points, not the premade's exact point spread, which depends on the hire's level; refusals in TB's answer are caught and a failed
spec is listed in the walk's `spec_failed`.

Raid-walk's `--prepull-aura role=<role>:<ids>` (`tank`, `melee`, `ranged`, `caster`, `healer` or `all`) puts a flask,
elixir, food and world-buff list on the raid as auras at the step before each boss row — whatever that step's phase,
or where the paused run stands when it passed that step too fast to pause (the boss row itself or beyond it) — and on
the boss row's own pause, all only while the run is still before the pull and on the same three cases: a boss alive
and out of combat gets the fire; a boss already dead — his corpse in the fresh arena read, or the walk's own record of
the kill — gets none, one `run.log` line `boss already dead, no pre-pull fire`, and the run carries on; a boss the
read cannot prove (engaged, or missing from the read) fails closed with no aura — and
`prepull-auras.json` records what each bot carries. A value that is not a `role=` list is a `tb-prepull-auras` file, so
`--prepull-aura dungeons/prepull/zg-preraid.json` names the six lists at once (`dungeons/prepull/onyxia-preraid.json` is the same six for Onyxia's Lair, with Greater Fire Protection Potion's aura 17543 on the `all` list). The targets are the walk's own hires,
matched by name in the commander's raid read from its view (the walk's player is not in the hired bots' raid); a frame
member the walk never hired gets no aura but is still watched for combat as one of the raid, and a hire whose role the
walk cannot tell fails the fire. The role comes PER BOT from the module's own state rows: the walk reads `.bot roster`
and takes each bot's `TBM:BOTSTATE|name|...specs=<strategy>+<strategy>` field, the spec kit the module itself installed
on that bot (holy/restoration/discipline healer, protection/tank feral/tank shaman tank, retribution/feral/enhancement
melee, hunter ranged, shadow/balance/elemental/mage/warlock caster). Neither the class alone nor the roster batch is the
source: a mixed batch like walk 101b's `5h 13d` names no role for a priest, paladin, druid or shaman, while the module's
row names the bot's own spec. The walk's mt and ot are tanks whatever their kit. A bot the snapshot leaves out, an
incomplete snapshot, and a kit naming no spec or specs of more than one role all fail closed before any aura;
`prepull-auras.json` records the source as `role_source` and each bot's `specs`. The batch is fail-closed: the commander and every living raid member, list or no list, are re-read for
combat before each `.aura` (an in-combat unit or an unreadable `unit` row stops the batch), a boss row's arena is read
fresh from the server (`q units` around the row: a boss already engaged, a read that cannot be made or that lists no
unit of the boss, or a unit row without its combat state, stops the walk), and every bot the walk hired must receive a
list — a fire that reaches fewer of them, or none at all, fails with the bots and reasons named. A boss row lands in
`boss_rows` only after its auras verified — a failed fire is an `attempts{row, reason}` entry instead.
By default every requested aura must verify; a miss fails the walk,
and `--prepull-aura-tolerance N` is the explicit opt-in that accepts up to N missing ids per bot
(see [the raid-walk reference](docs/okf/raids/raid-walk-reference.md)).
Its `gm_aura` RPC is the select-then-`.aura <id>` hand behind it (see [the RPC reference](docs/okf/agent/rpc-reference.md)).
`--prepull-aura-when start` fires the same lists ONCE instead, after the hire, the gear and the placement and before
the run's first move (the run is not running, so nothing is paused), records the verified fire as `start_row`, and at
each boss pull only reads and logs which auras every hire still carries — walk 101 lost Hakkar's row 689 four times
out of four when a bot entered combat during the boss-row fire's ~120 GM sends. The start fire goes out only on a
positively confirmed idle run: `run status` must parse a `TBM:RUN` line in state `none` or `done`; a refused answer, an
answer without that line, and any running state (whatever plan it names) send nothing and fail the walk. It waits first
(up to 60 s, polling every 2 s) for every hire to be in the commander's view and alive and in the agent's unit store,
because the fire goes out inside the placement's teleport wave (walk 101: the ZG Jeklik run at plan start row 1 stopped
0.16 s after the last `.tele` with "the unit read of <MT> failed twice"); a hire still missing after the wait fails the
walk, and `run.log` names how long the wait took. The coverage
read is taken at the pull itself — the boss row current in the plan's `pull` or `task` (fight start) phase — never at
the step before it; a row is read at the pull and refreshed once at the fight start, and a phase is recorded only after
a successful read, so a failed read is retried at the next qualifying status.

`--prepull-aura-topup` (off by default, needs `--prepull-aura`) closes the gap a death or an expiry leaves between boss
rows: before EVERY pull row, trash or boss, out of combat and never inside the pull, the walk reads each living hire's
auras and puts back only the ids its role's list is missing — a death strips every aura, and food (~15 min), the elixirs,
the flasks and the world buffs expire — then reads them back. A boss row is the boss-row fire's own business in
`--prepull-aura-when boss` (the default), so the top-up stays out of it and never sends a list the fire is sending;
under `--prepull-aura-when start`, where no boss row is fired, the top-up is what reads and tops up the hires at the
boss pull. It uses the fire's path (the commander join when the unit store cannot see the raid, the per-bot role, the
combat recheck before each send), pauses the run only while there is something to send (at most 15 s, resumed right
after or at the cap), never targets a dead bot, and never fails the walk: combat during the top-up stops the batch
quietly and the pull goes on. One `run.log` line names the pull and each topped bot with the ids put back and the
cause (`death`, `expired`, or `never had`). Completion is tracked per hire: each hire is read once per pull step, and
one a status could not read (a failed unit read, a body, a hire out of the commander's view) is read again at the next
pre-pull status of the same pull. A hire that still lacks an id after 3 sends is done for that pull step too, with one
line naming the ids (`... still lacks <ids> after 3 tries; giving up for this step`): an aura the server will not
stick is not resent on every status before the same pull. Nothing is carried to the next pull step: an aura that was
there at one pull can expire seconds later.

A `--place` walk revives the hires its own frames do not show alive before it moves the raid: the ZG Mandokir runs of
walk 101b all wiped at the zone-in while the commander ran the corridor clear, and 19 corpses stayed dead through every
placement try because `.summon` never moves a corpse. Each of those hires gets one `.revive <name>` (100 ms apart, a
failed line retried once); a hire no frame read at the check shows at all is unknown, and unknown is revived too (a
`.revive` on a living bot is harmless). The frames are then read every 2 s for up to 10 s, and the run is then
**ASSISTED** (a revive is a GM assist, `place_revived` in `summary.json`, `ASSISTED: revived N hires before the
placement` in `summary.txt`). A hire counts as revived only when a frame read after the revive shows it alive: a failed
read, an empty frame or a stale observation leaves it unknown, which is not revived. Half or more of the hires still dead
or unknown after that ends the walk at once with exit 29 (`place: party dead ... fresh instance needed`) instead of
spending its tries; `--no-place-revive` turns the revive off and keeps the fast fail. See
[the raid-walk reference](docs/okf/raids/raid-walk-reference.md).

Log a character in and keep it online with the RPC server running:

```sh
bin/turtle-agent login --rpc 127.0.0.1:8765 <account> <character>
```

```sh
bin/turtle-agent rpc <slot-dir> status
```

It reads the slot's token file itself and prints only the call's `result`. See
[Drive the agent with RPC](docs/okf/getting-started/drive-rpc.md).

## What you can do with it

- Drive one character over RPC: [the agent and its RPC](docs/okf/agent/index.md).
- Run a bot party through a dungeon: [dungeon runs](docs/okf/dungeons/index.md).
- Walk raids (boss-mechanic practice runs included), supervise many, regression-check plans: [raids](docs/okf/raids/index.md).
- Write, check and ship run plans: [plans](docs/okf/plans/index.md).
- Walk the navmesh and draw the run as a map: [navigation](docs/okf/navigation/index.md).
- Fight with data: rotations, tactics, the brain: [combat](docs/okf/combat/index.md).
- Watch it live, record and score it: [observability](docs/okf/observability/index.md).

## Documentation

The manual lives in [docs/okf](docs/okf/index.md): one topic per page, each starting with a header that says what
kind of page it is. The format is described in
[How this documentation is written](docs/okf/about/conventions.md), and
[the README section map](docs/okf/about/readme-map.md) shows where every section of the old long README went.

## Development

`make test` must stay green (gofmt, `go vet`, a no-cgo build, `go test -race ./...`, a cgo build, `staticcheck ./...`).
See [docs/CI.md](docs/CI.md) for the jobs. Open PRs against **`dev`**; `main` only takes `dev` after the owner's
review. Contributions that change how the bot plays need the maintainer's decision first — bug fixes go in, behaviour
changes are discussed.

## Licence

GNU Affero General Public License v3.0 or later, see [LICENSE](LICENSE). Versions up to the commit that changed the licence were released under the MIT License (kept in [LICENSES/MIT-turtle-agent-before-agpl.txt](LICENSES/MIT-turtle-agent-before-agpl.txt)); copies you already received under MIT stay under MIT. Parts are ported from [agent-wow](https://github.com/agent-wow/agent-wow) (MIT; the files keep their own MIT headers), and
`internal/navmesh/detour` is Detour from recastnavigation (zlib, `internal/navmesh/detour/License.txt`); see
[NOTICE](NOTICE). Dungeon event data derived from [jrad7/mod-dungeon-clear](https://github.com/jrad7/mod-dungeon-clear) (AGPL-3.0-or-later) is marked with its source where it appears. This is an unofficial community project made for educational purposes. World of Warcraft is a trademark of Blizzard Entertainment; this project is not affiliated with it.
