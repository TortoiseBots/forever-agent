# Forever core gains 2 notes (items 7, 9, 10, 11, 13)

Scope: five small agent slices from FOREVER-AGENT-CORE-GAINS.md against core rev d02645dd21. No live run:
the brief asks for none, and the live checks below are written into this file instead. Each slice keeps its
own commit; every test quotes the core format string from the source cited beside it.

Code:

* `internal/forever/command/tbcmd/bossstate.go` — item 7: `.instance setbossstate/getbossstate` read-back.
* `internal/forever/command/tbcmd/respawns.go` — item 9: `.list respawns` and `.debug instancespawn`.
* `internal/forever/command/tbcmd/debug.go` — item 10: `.debug boundary`, `.npc evade`, `.debug combat`, `.debug threat`.
* `internal/forever/combat/aireaction.go` + the `SMSGAIReaction` case in `internal/forever/world/session.go` —
  item 11: `SMSG_AI_REACTION` as a "mob turned hostile" event with the source guid in the watch record.
* `internal/forever/objects/gameobject_query.go`, the `goNames` cache in `internal/forever/objects/world.go`,
  the `SMSGGameObjectQueryResponse` case and `queryGameObjects` in `internal/forever/world/session.go` —
  item 13: the game-object query response parsed into names and types, cached per entry.

## Item 7: boss state set and read

The core's commands (cs_instance.cpp:160-255, Console::Yes): `.instance setbossstate <id> <state> [player]`
writes the EncounterState and answers with trinity string 5057 ('Boss id %u state is now set to %i (%s).');
`.instance getbossstate <id> [player]` answers with 5058 ('Boss id %u state is %i (%s).'). Both print the
number and its ToConstant word (NOT_STARTED 0, IN_PROGRESS 1, FAIL 2, DONE 3, SPECIAL 4, TO_BE_DECIDED 5;
InstanceScript.h:68-76, enuminfo_InstanceScript.cpp:30-41). The 2017 seed rows
(2015_11_06_35_world_2015_10_10_05.sql, 2017_99_99_18863_world.sql) fix those texts; the handler call sites
(cs_instance.cpp:204, :251) fix the shape the tests quote in bossstate.go.

The client sends the core commands with no token (the core has no begin/end markers) and parses the single
server line: the id and the word must agree, a get line never parses as a set and vice versa, and any second
line, refusal or syntax text is refused. The set is always read back with a getbossstate and the real
stored state is returned: the core prints the requested state even when InstanceScript::SetBossState refuses
it, so a mismatch is a typed refusal error naming both. Out-of-range states (6+) are refused before anything
is sent. This replaces the old `--gm-instance-data` hack (the raw `.debug instancedata` lines of internal/raidwalk/instdata.go):
test setup opens a later boss directly and reads the state back instead of poking instance data by hand.

No behaviour change (a bug fix in test tooling, not in how a bot plays): bots never send these lines on their
own; the practice reset and the watch keep their own paths.

## Item 9: respawns and instance spawns

`.list respawns [range]` (cs_list.cpp:650-700) prints one section per SpawnObjectType (titles from
enuminfo_SpawnData.cpp: Creature, Gameobject, AreaTrigger), a header line, the column header, then one row per
pending respawn: `<spawnId> | <entry> | [<gridX>,<gridY>] | <zone> (<id>) | <time>[ (inactive)]`
(cs_list.cpp:690; UI64FMTD row, grid as %02u, the (inactive) suffix when the spawn group is not active). The
head-line shapes are LANG_LIST_RESPAWNS_RANGE 5077 ('Listing %s respawns within %uyd'), ZONE 5078 ('Listing %s
respawns for %s (zone %u)') and the column header 5079 ('SpawnID | Entry | GridXY| Zone | Respawn time (Full)';
2020_08_22_00_world_2017_07_31_00_world.sql:200) with 'overdue' the LANG 5080 word. The row zone may itself hold
parentheses, so the parser takes the LAST pair as the id (see the Deadmines gate test).

Proof that a pack stays dead is read as the absence of its spawn ids from every section; the timers are the
ShortText waits ('45s', '1h2m3s'; Util.cpp:116-165) or the overdue word. A section that prints its type header
without the column header is a cut reply, never an empty world: the handler always prints one section per type.

`.debug instancespawn [groupID|explain]` (cs_debug.cpp:1431-1565) prints 'Spawn groups managed by %s (%u):'
(only without a group id: `if (!groupID)`) then, compact, " - '<name>' (<id>) is [not ]active", or with explain
one " |-- '<name>' (<id>)" group header plus the rule lines (' | |-- ...' and the verdict ' | |=> ...'). The
typed read keeps the compact facts and the raw rule text for bug reports; it does not re-decide the rules (the
verdict line's active/not-active is kept from the last '|=>' line).

No behaviour change: read-only setup/test evidence.

## Item 10: boundary, evade, combat, threat

`.debug boundary [fill] [duration]` (cs_debug.cpp:1318; CreatureAI::VisualizeBoundary, CreatureAI.cpp:352-430)
prints nothing on success — the boundary markers summoned in the world ARE the answer — and a LANG failure id
otherwise. The typed answer is boundedness plus the failure kind: no-interior (11011), unbounded (11012) or
failsafe (11013). The 2016 seed texts ('VisualizeBoundary warning: No interior point ...',
'VisualizeBoundary error: Creature movement is unbounded',
'VisualizeBoundary warning: Reached fail-safe flood boundary ...' in
2016_03_22_09_world_2016_01_13_00_world.sql) are quoted in debug.go beside the matcher.

`.npc evade [why] [force]` (cs_npc.cpp HandleNpcEvadeCommand) likewise prints nothing on success; its two
refusals (LANG_SELECT_CREATURE, LANG_CREATURE_NOT_AI_ENABLED) surface as command errors through the collector,
so any collected line is another command's tail and is refused. Both run through the collector's AllowEmpty
mode (a quiet window with no line is the complete success answer) and nil-check before reading; an empty
boundary reply that did not end quietly is UNKNOWN, never bounded.

`.debug combat` (cs_debug.cpp HandleDebugCombatListCommand) prints 'Combat refs: (Combat state: %d | Manager
state: %d)' then '[PvP]/[PvE] %s (SpawnID %u)' rows; `.debug threat` (HandleDebugThreatListCommand :877-968
and HandleDebugThreatInfoCommand) prints the threatened-by-me list, the threat list with threat floats and the
[TAUNT]/[DETAUNT]/[FIXATE]/[SUPPRESSED]/[OFFLINE] flags, and the not-engaged/cannot-have/dead status lines. Every
line the parser accepts is quoted in debug.go's matchers; the raw text of the whole reply is always kept for the
bug report (store-the-raw-text rule).

No behaviour change: bug-report evidence only. The practice `.npc evade` reset keeps its own path.

## Item 11: SMSG_AI_REACTION

The packet is the packed GUID then the uint32 reaction (CombatPackets.h:118-133, CombatPackets.cpp:81-87):
AI_REACTION_ALERT 0 (pre-aggro, CreatureAI.cpp:156), FRIENDLY 1, HOSTILE 2 (every attack, Unit.cpp:6242),
AFRAID 3, DESTROY 4 (SharedDefines.h:6636-6642). The parser names all five and refuses short/trailing packets.

Fix round (the cold review and the adversarial review): only HOSTILE becomes kind "hostile" (a pull
moment); ALERT is its own "alert" kind (stealth notice, not aggro, so a watch counting hostile rows no longer
flags false accidental pulls); FRIENDLY, AFRAID, DESTROY and unknown values write no row. The reaction joins
describesMapContent (mapchange.go), so an old-map copy held between the suspend and the resume is dropped at
the resume instead of landing in the fresh map's log. This is gain, not guess: the driver's "lost or dead"
and "fight end" inference still uses its own polls; the reaction is an extra event, never a replacement.

## Item 13: game-object query

`SMSG_QUERY_GAME_OBJECT_RESPONSE` 0x490007 (not parsed before; only creature and name queries were). The answer
is the uint32 entry, the GUID, a one-bit Allow, a uint32 stats size, then the stats block when allowed: int32
type, int32 display, four C-string names, icon/cast-bar/unknown strings, 35 int32 data words
(MAX_GAMEOBJECT_DATA), float size, uint8 quest-item count with int32 ids, int32 content tuning and int32
required level (QueryPackets.cpp:350-388). The client asks with CMSG_QUERY_GAME_OBJECT 0x3D0142: the uint32
entry then the packed GUID (QueryPackets.cpp:344-348).

The session's queryNames round now asks one query per unknown game-object entry after the player/creature rounds
(same deadline, skipped when the answer does not come), caches the answer per entry beside the creature name
cache (Allow=false answers as negative entries under the same bound), and World.Name serves it: doors, chests
and gossip objects show their names where the loot and route checks list them. Malformed answers are logged and
dropped, never session errors. That unblocks loot/route callers that refuse when two entries are in range: the
entries are now told apart by name as well as by position.

## Fix round: what the reviews changed (core rev d02645dd21 throughout)

* `.list respawns` column header is the real trinity_string 5079 text 'SpawnID | Entry | GridXY| Zone | Respawn
  time (Full)' (2020_08_22_00_world_2017_07_31_00_world.sql:200); the old guess without ' (Full)' failed every
  real reply with "is not the respawn column header". Tests quote the exact string.
* `.debug instancespawn <id>` never prints the managed-by line (cs_debug.cpp `if (!groupID)`): the parser takes
  hasGroup and makes the line optional on that path. The core's four refusal lines ("There is no spawn group
  with ID %u.", "%s has no instance script.", "%s's instance script does not manage any spawn groups.", "%s's
  instance script does not manage group '%s'.") arrive as command errors, so anything collected that is not a
  group line is refused.
* `SetBossState` reads back with `getbossstate` and returns the real state: the set command prints the
  requested state unconditionally while InstanceScript::SetBossState can refuse (loading, same state,
  DONE-is-terminal), so a refused transition is now a typed error naming the requested and the real state.
* Silent-success commands (`.debug boundary`, `.npc evade`) run through the collector's AllowEmpty mode: a
  quiet window with no line is a complete answer (the failure ids / refusals still come back as errors), and
  the parsers nil-check first. An empty boundary reply that did NOT end quietly is UNKNOWN, never bounded.
* Name/query waits are correlated: the player wait needs every asked GUID named, the creature wait every asked
  entry, the gameobject wait the echoed entry — a delayed answer for another entry no longer completes them.
* Malformed name/query answers are logged and dropped (parse error), never session errors; Allow=false
  gameobject answers are kept as negative cache entries under the same bound, so UnknownGameObjects stops
  re-asking them.

## Live checks (not run; the brief asks for none)

* `.instance setbossstate 0 3` then `.instance getbossstate 0` on a test server: the get answers
  'Boss id 0 state is 3 (DONE).'; a refused transition answers DONE on the set but the get still reads the
  old state, and the client reports the refusal. Out-of-range states print the core's LANG_BAD_VALUE refusal.
* `.list respawns 100` in a cleared wing prints the type sections with the ' (Full)' header, no rows for the
  dead packs and ShortText waits for the live ones; `.debug instancespawn 12` prints only the compact line;
  `.debug instancespawn explain` names the blocking boss states.
* `.debug threat` on an engaged boss lists the tank first with its threat float; `.debug combat` names the
  combat refs; `.debug boundary` on a room boss prints nothing and summons the markers.
* Pull a mob: the combat log gains a "hostile" row naming the reacting unit before the first swing; a
  stealthed player noticed gains an "alert" row instead.
* Walk to a door and a chest: `units` shows their names after the query round; a missing template stays
  unnamed (negative cache) without stalling later entries; two same-entry objects still refuse without `--at`.

## Gates

`make fmt-check`, `make cross`, staticcheck, `vm-test ta` on a local worktree on a clean committed tree:

RESULT: PASS kind=ta sha=5608b87c dirty=0
