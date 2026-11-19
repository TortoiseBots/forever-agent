# forever-agent

A headless World of Warcraft client for **WoW Forever (Classic 1.60.1)** servers built on
[ForeverCore](https://github.com/Karrz1/TrinityCore). One process logs one character in, with no game window, and
exposes it over a local JSON-RPC API, so server-side behaviour can be tested from scripts.

**Status: early port; the character can log in and out, but there is no gameplay yet.** This repository starts as a
copy of [turtle-agent](https://github.com/pjerra/turtle-agent) (the same tool for vanilla 1.12 / Turtle WoW servers).
The 1.12 game layer still speaks the 1.12 protocol. Rounds 1 and 2 wrote the Forever login and the world socket up
to "the server treats the player as fully in the world"; movement, objects and gameplay come next.

| Part | State |
|---|---|
| Login through bnetserver | **SRP login, login ticket and the realm join answer work** (`forever-agent forever login-check`, round 1: see [docs/forever/LOGIN-R1-NOTES.md](docs/forever/LOGIN-R1-NOTES.md)) |
| World socket (encrypted, compressed) | **Works** (`forever-agent forever world-check`: banners, world auth, AES-GCM, character list, login on the instance socket, time sync, ping, logout; round 2: see [docs/forever/WORLD-R1-NOTES.md](docs/forever/WORLD-R1-NOTES.md)) |
| Packets and object fields for build 70938 | login and character-select packets done; round 3 reads `SMSG_UPDATE_OBJECT` (creates, values, movement blocks, splines), the movement relays and both name lookups, and walks the character (`forever-agent forever walk-check`; see [docs/forever/OBJECTS-R1-NOTES.md](docs/forever/OBJECTS-R1-NOTES.md)) |
| Map changes (round A7) | **Works** (`forever-agent forever map-check`: follows a far teleport, a teleport to another continent and entering or leaving an instance — suspend/new-world handshake, the world model cleared for the new map, the navmesh planner dropped; see [docs/forever/MAPCHANGE-R1-NOTES.md](docs/forever/MAPCHANGE-R1-NOTES.md)) |
| Chat and party (round 5) | **Works** (`forever-agent forever chat-check` and `forever party-check`: say, yell, whisper, party and emote text out; `SMSG_CHAT` in, the notification/`MOTD` lines, and the invite/accept/update/member-state/leave flow with a bounded party model; see [docs/forever/CHAT-R1-NOTES.md](docs/forever/CHAT-R1-NOTES.md)) |
| Command channel (round A6) | **Works** (`forever-agent forever cmd-check`: one chat-command line at a time, the reply's system lines collected by token or quiet window, the core's refusals as named errors, a GM guard before anything is sent, and the typed state/order/threat/party model of the server module's `.tb` commands; see [docs/forever/COMMAND-R1-NOTES.md](docs/forever/COMMAND-R1-NOTES.md)) |
| Character screen (round 6) | **Works** (`forever-agent forever char-create` and `char-delete`: create, delete and the name-availability check with every result code named; a create with no customization choices is valid; see [docs/forever/CHARCREATE-R1-NOTES.md](docs/forever/CHARCREATE-R1-NOTES.md)) |
| Game layer on the new packets | not started |
| Trials (behaviour test bench, in Go, as `forever-agent trials`) | not started |

`forever-agent forever login-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL` logs in through the
login server (password from `FOREVER_PASSWORD`) and stops at the realm join ticket: one line per step, then the realm
name, id and address. It prints no ticket, key, proof or password, only their lengths; the connect step prints the
ciid, the server id and the server time as counts (`ciid 33 chars`), never their values, so the output is safe to
paste into a report. `--build` picks the client build to claim (default 70938); a realm only admits the builds it
accepts. Each RPC step and the login as a whole are deadline-bounded (30 s per request, 2 min overall), so a silent
or slow-drip server cannot hold the command open.

`forever-agent forever world-check --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME`
does the same login and then enters the world with that character: world auth, character list, `CMSG_PLAYER_LOGIN`,
the instance socket, the login burst (loading screen, time syncs, the active-mover exchange), `--stay` seconds of
keep-alives (default 20 s), then a clean logout. It prints one line per step, a count of server packets by opcode
name, and no key or ticket, only lengths. `--build` has to be the build the realm accepts (its
`auth.realmlist.gamebuild`), because a realm admits only that build unless `Realm.AlsoAcceptBuilds` lists more. By
default the instance socket `SMSG_CONNECT_TO` names must be on the host the realm socket is connected to;
`--allow-instance-redirect` permits a different host for clustered servers.

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
`--build` must be the realm's build, like world-check.
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
not acted on either.
`--capture-dir DIR` works as it does for chat-check.

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

Create the settings file (default `~/.config/turtle-agent/settings.json`, mode **0600**; the agent refuses a file
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
[NOTICE](NOTICE). Dungeon event data derived from [jrad7/mod-dungeon-clear](https://github.com/jrad7/mod-dungeon-clear) (AGPL-3.0-or-later) is marked with its source where it appears. World of Warcraft is a trademark of Blizzard Entertainment; this project is not affiliated with it.
