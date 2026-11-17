# Seam audit: where the upper layers touch the 1.12 protocol

Branch `main` at `d1788e71`. Read-only audit. Every count below comes from `git grep` over
non-test `.go` files unless it says "tests". The Go toolchain was not available, so nothing
here is type-checked: counts of identifiers like `packet.GUID` are exact text matches; counts of
struct field names (section 3) are text matches on `.Name` and are upper bounds.

Protocol packages: `session`, `auth`, `worldconn`, `packet`, `opcode`, `objects`, `game`.
"Upstream" means every other package.

## Summary

- The seam is **not** `internal/game`. Upper layers use only 17 identifiers from `game`
  (6 files). `game` is a pure codec package: 34 `Build*` functions return `[]byte`, 74 `Parse*`
  functions take `body []byte`. Only `internal/session` calls them.
- The real seam is **`*session.Session`**: 116 exported methods
  (`git grep -E '^func \([a-z]+ \*?Session\) [A-Z]' internal/session`), of which 108 are
  called from `cmd/forever-agent` or `internal/mapview`. `cmd` names `session.*` 209 times in
  35 files.
- The upper packages already talk through small interfaces of their own (`brain.Game`
  internal/brain/brain.go:30, `dungeon.Game` internal/dungeon/driver.go:20, and about 60
  more). Four adapter types in `cmd` implement them over the session:
  `sessionGame` (cmd/forever-agent/dungeon_methods.go:921), `sessionBrainGame`
  (cmd/forever-agent/brain_methods.go:170), `remoteGame` (cmd/forever-agent/commander_methods.go:209),
  `sessionTeardownGame` (cmd/forever-agent/teardown_methods.go:74).
- What leaks through those interfaces is **value types**, not behaviour: `packet.GUID`
  (530 mentions in 95 upstream files) and the `objects` snapshot structs (285 uses in 64 files).
- No 1.12 update-field index is used upstream. The only `objects` function used upstream is
  `objects.GOTypeByName` (cmd/forever-agent/gameobject_methods.go:29).
- Opcode constants upstream: 23 uses in 3 files, all in capture/replay tooling
  (`internal/capture`, `internal/mapview`, `cmd/forever-agent/capturecmd.go`).
- GUID width: `packet.GUID` is `uint64` (internal/packet/guid.go:13). At least 28 upstream
  lines build or take apart the 1.12 GUID bit layout, and 213 `map[packet.GUID]` declarations
  in 47 files depend on it being a comparable value (fine for a struct, not for a slice).
- Plans store DB spawn ids (`Spawn uint32`, internal/dungeon/plan.go:235), not GUIDs, and the
  code derives spawn ids from the low 24 bits of a 1.12 GUID in 9 places.
- Server-specific text protocols are a bigger surface than the wire seam: `tbm` (the bot
  module's addon/command protocol) has 694 use sites in 61 files; `.bot ...` literals appear on
  82 code lines; GM dot-command literals on 143 code lines.
- The event log is a second seam: protocol packages emit 78 distinct `events.Kind*` constants;
  37 of them are read upstream (177 `events.Kind*` references upstream in total).

## 1. Upper-layer imports of protocol packages

Grep per cell: files that import the package and contain `\b<pkg>\.[A-Z]`, then count of
`\b<pkg>\.[A-Z][A-Za-z_]*` matches. Kinds: (a) neutral value type, (b) wire-specific,
(c) behaviour.

| Package | Protocol pkg | Files | Uses | What | Kind |
|---|---|---|---|---|---|
| cmd/forever-agent | packet | 27 | 114 | `GUID` 104, `ParseGUID` 8, `HighUnit` 2 | a (GUID), b (HighUnit) |
| cmd/forever-agent | objects | 22 | 72 | `Unit` 39, `Item` 9, `Filter` 9, `GameObject` 6, `GOFilter` 3, `QuestSlot` 2, `Aura` 2, `DynamicObject` 1, `GOTypeByName` 1 | a |
| cmd/forever-agent | session | 35 | 209 | `Session` 71, `StatusInfo` 20, `StatusInWorld` 15, 53 other names | c |
| cmd/forever-agent | worldconn | 3 | 14 | `Dial`, `Conn`, `Tap`, `Addons`, `ErrWrite` | b, c |
| cmd/forever-agent | auth | 2 | 10 | `Login`, `Ticket`, `AuthError`, `ResultDBBusy` | b, c |
| cmd/forever-agent | game | 1 | 2 | `ResurrectRequest`, `Corpse` | a |
| cmd/forever-agent | opcode | 1 | 1 | `opcode.Name` | b |
| internal/brain | packet | 21 | 91 | `GUID` only | a |
| internal/brain | objects | 21 | 108 | `Unit` 101, `DynamicObject` 2, `Cast` 2, `Aura` 2, `Item` 1 | a |
| internal/brain | game | 3 | 5 | `game.Facing` (pure maths) | a |
| internal/dungeon | packet | 35 | 244 | `GUID` 242, `HighPet` 1, `HighGameObject` 1 | a, b |
| internal/dungeon | objects | 17 | 87 | `Unit` 69, `GameObject` 10, `Aura` 7, `Item` 1 | a |
| internal/raidwalk | packet | 8 | 62 | `GUID` 60, `ParseGUID` 1, `HighUnit` 1 | a, b |
| internal/raidwalk | objects | 2 | 13 | `Unit` | a |
| internal/tbm | packet | 3 | 23 | `GUID` | a |
| internal/spellstate | game | 1 | 9 | `CastFailName`, `CastFailedNotReady`, `CastFailedInterrupted`, `InitialSpells`, `InfiniteCooldown` | b |
| internal/spellstate | packet | 1 | 3 | `GUID` | a |
| internal/mapview | opcode | 1 | 17 | 17 `MSGMove*` constants | b |
| internal/mapview | session | 1 | 5 | `Session`, `StatusInWorld`, `ReplayWith`, `Options` | c |
| internal/mapview | packet, game, objects, worldconn | 1 | 5 | `NewReader`, `ReadMoveInfo`, `Filter`, `GOFilter`, `ClientToServer` | b |
| internal/capture | opcode, worldconn, packet | 1 | 13 | `CMSGAuthSession`, `SMSGWardenData`, `CMSGWardenData`, `NumMsgTypes`, `Name`, `Direction`, `NewReader/NewWriter` | b |
| internal/planscout | objects | 1 | 3 | `Unit` | a |
| internal/patrols | packet | 1 | 4 | `GUID` | a |
| internal/events | packet | 1 | 2 | `GUID` | a |
| internal/config | worldconn | 1 | 2 | `ValidateAddonNames` | b |
| internal/worldspawn | packet | 1 | 2 | `GUID`, `HighUnit` | b |

`internal/spells`, `internal/dashboard`, `internal/scenario` and `internal/rpc` import no
protocol package.

Examples:
- (a) internal/brain/brain.go:32 `Self() (objects.Unit, bool)`; internal/dungeon/buffs.go:536
  `func (d *Driver) rebuff(ctx context.Context, g packet.GUID) error`.
- (b) internal/spellstate/spellstate.go:541 `CastRefused(spell uint32, code uint8)` takes a raw
  1.12 cast-result code; internal/raidwalk/quiet.go:241 `if u.GUID.High() != packet.HighUnit`.
- (b) internal/mapview/source.go:62-67 a set of 1.12 movement opcodes; internal/capture/capture.go:133
  writes a 1.12 capture record with `packet.NewWriter()`.
- (c) cmd/forever-agent/login.go:123 `dialWorld(... *auth.Ticket, tap worldconn.Tap ...)`;
  cmd/forever-agent/login.go:480 `logoutAndWait(s *session.Session, conn *worldconn.Conn, ...)`.

Tests (upstream `_test.go`, same greps): `packet` 114 files / 765 uses, `objects` 86 / 1,361,
`opcode` 29 / 434, `session` 28 / 134, `game` 20 / 116, `worldconn` 4 / 34. The `opcode` and
`game` test uses are fake 1.12 servers in `cmd/forever-agent` tests; they belong to the 1.12
implementation and keep proving it.

## 2. internal/game exported API

`internal/game` has 28 non-test files (4,588 lines). Every area is a codec: `Build<X>(...) []byte`
and `Parse<X>(body []byte) (<X>, error)`. Signatures are wire-specific by construction. The value
types they return mostly carry `packet.GUID`.

| Area | Files | Example exports | Signature leaks |
|---|---|---|---|
| Movement | actions.go, movement.go, fall.go | `BuildMovement`, `ReadMoveInfo(*packet.Reader)`, `ParseKnockBack`, `Facing`, `FallTimeTo` | `MoveInfo` (1.12 movement flags), `packet.Reader`; `Facing`/`Fall*` are neutral maths |
| Chat, channels | chat.go, channel.go | `BuildMessageChat`, `ParseMessageChat`, `ParseChannelNotify` | 1.12 `ChatType` numbers |
| Addon messages | addon.go | `BuildAddonMessage`, `SplitAddon` | 1.12 addon-in-chat framing |
| Combat log | combat.go | 16 types (`MeleeHit`, `SpellGo`, `Heal`, `Kill`...) | `packet.GUID`, 1.12 hit/victim codes |
| Casting, spellbook | actions.go, spellbook.go | `BuildCastSpell(spell, packet.GUID)`, `InitialSpells`, `CastFailName` | 1.12 cast-result codes (spellbook.go:323-324) |
| Group, raid | group.go, groupctl.go, raid.go | `ParseGroupList`, `BuildLootMethod`, `BuildRaidTargetUpdate` | `packet.GUID`, 1.12 group flags |
| Loot | loot.go, lootwindow.go | `ParseLootResponse`, `BuildLootRoll` | 1.12 loot codes |
| Gossip, NPC | gossip.go, vendor.go, trainer.go, taxi.go, gameobject.go | `GossipMenu`, `BuildBuyItem`, `TrainerList`, `BuildActivateTaxi` | `packet.GUID`, 1.12 error codes |
| Quests | quest.go | 7 builders, `QuestDetails`, `QuestUpdateKill` | `packet.GUID` |
| Items | items.go | `BuildUseItem`, `InventoryChangeFailure` | 1.12 inventory codes |
| Trade | trade.go | `TradeStatus`, `ParseTradeStatusExtended` | 1.12 trade codes |
| Pets | pet.go | `BuildPetAction`, `PetSpells` | `packet.GUID`, 1.12 action-bar packing |
| Queries | query.go | `NameInfo`, `CreatureInfo`, `ItemInfo` | `ItemInfo` is neutral |
| Death | death.go | `ResurrectRequest`, `Corpse`, `BuildAreaTrigger` | `ResurrectRequest.Caster` is `packet.GUID` (death.go:26) |

Three `game` types escape through `Session` signatures: `game.ResurrectRequest`
(internal/session/death.go:121), `game.Corpse` (death.go:170), `game.ItemInfo`
(iteminfo.go:55). `internal/game/gametest` (11 files) builds 1.12 server packets for tests.

## 3. What the upper layers read from internal/objects

No update-field index, no `Store`, no `Field()` call is used upstream
(grep `objects\.(UnitField|PlayerField|ItemField|ObjectField|UnitBytes|UnitMount|GOFlag|GOType)|\.Store\(\)|\.Field\(`).
`Session.Store()` (internal/session/world.go:43) has no upstream caller. The upper layers read
only the snapshot structs the session returns.

Field reads, grep `\.<Field>\b` over non-test files in cmd, brain, dungeon, raidwalk, planscout,
patrols, mapview, tbm, events, spellstate. Upper bounds; generic names also match plan and
navmesh structs.

| Struct | Distinctive fields (uses / files) | Generic names (not attributable) |
|---|---|---|
| `Unit` (internal/objects/store.go:47) | `InCombat` 95/39, `Dead` 222/74, `Class` 124/33, `Level` 95/26, `MaxHealth` 51/24, `Health` 43/25, `Hostile` 37/25, `Moving` 36/18, `PowerType` 31/16, `Ghost` 28/11, `Casting` 27/8, `Power` 26/11, `MaxPower` 24/12, `Distance2D` 14/8, `Auras` 14/12, `CrowdControlled()` 9/8, `PosAgeMS` 7/5, `Mana` 5/3, `StandState` 3/2, `ComboPoints` 3/2, `ComboTarget` 3/2, `GCDMS` 3/1, `BaseMana` 3/1, `MaxMana` 2/1, `BaseHealth` 2/1, `InParty` 2/1, `Faction` 1/1, `NPCFlags` 0 | `GUID`, `Entry`, `Name`, `X/Y/Z`, `O` 13/7, `Distance`, `Target`, `Flags`, `Type` |
| `Aura` (store.go:102) | `SpellID` 62/25, `Harmful` 8/7, `RemainingMS` 8/3, `Stacks` 5/2 | `Slot`, `Name` |
| `Cast` (store.go:115) | `EndsInMS` 8/3, `Channel` 7/4 | `SpellID`, `Target` |
| `Item` (store.go:662) | `TempEnchant` 4/3, `Bag` 9/5 | `Slot`, `Count`, `Entry`, `GUID` |
| `GameObject` (gameobject.go:58) | `TypeName` 1/1, `Locked` 1/1, `Usable` 1/1, `DynFlags` 0, `DisplayID` 0, `CreatedBy` 0 | `State`, `Type`, `Flags` |
| `DynamicObject` (dynobj.go:15) | `Caster` 5/3, `Radius` (also navmesh) | `Spell` |
| `QuestSlot` (quests.go:26) | `Counts` 34/15, `Complete` 25/14, `Failed` 15/9 | `ID`, `Slot` |

This is the "what the agent needs from update fields" list. `CrowdControlled()`
(internal/objects/cc.go:23) reads aura names and mechanics, not unit flags. Fields that score 0
are still serialised to RPC JSON, so dashboards may read them; that is not measured here.

## 4. Opcode constants used outside the protocol layer

| Constant | Uses | Where |
|---|---|---|
| `opcode.Name` | 2 | cmd/forever-agent/capturecmd.go:74; internal/capture/capture.go:288 |
| `CMSGAuthSession`, `SMSGWardenData`, `CMSGWardenData` | 1 each | internal/capture/capture.go:31-33 (redaction list) |
| `NumMsgTypes` | 1 | internal/capture/capture.go:223 |
| `MSGMoveStartForward`, `...StartBackward`, `...Stop`, `...StartStrafeLeft`, `...StartStrafeRight`, `...StopStrafe`, `...Jump`, `...StartTurnLeft`, `...StartTurnRight`, `...StopTurn`, `...SetRunMode`, `...SetWalkMode`, `...FallLand`, `...StartSwim`, `...StopSwim`, `...SetFacing`, `...Heartbeat` | 1 each (17) | internal/mapview/source.go:62-67 |

All of it is capture and replay of 1.12 packet logs. No live-play code upstream names an opcode.

## 5. GUID width

`type GUID uint64` (internal/packet/guid.go:13) with 1.12 layout helpers: `High()` (:23), `Entry()`
from bits 24-47 (:32), `HighUnit/HighPet/...` (:15-21). JSON is already a hex string
(`MarshalJSON`, :41), so RPC payloads and event logs carry `"0xF130..."` strings, not numbers.

Upstream assumptions (grep scope: all non-protocol packages, non-test):

| Assumption | Count | Example |
|---|---|---|
| `packet.GUID` mentioned | 530 in 95 files | internal/dungeon/buffs.go:536 |
| `map[packet.GUID]` | 213 in 47 files | (works with any comparable type) |
| `packet.GUID(...)` conversions | 12 | internal/dungeon/prepare.go:557 `packet.GUID(r.GUID)` |
| `uint64(...)` casts on a guid line | 43 | cmd/forever-agent/brain_methods.go:53 |
| uint64/[]uint64 vars or fields named guid | 15 | internal/mapview/build.go:19 `GUID uint64` |
| 24-bit counter mask (`&0xffffff`) | 9 | internal/raidwalk/gmclear.go:377 `SpawnGUID(g) = uint64(g) & 0x00FFFFFF` |
| `<<48`, `>>48`, `<<24` layout shifts | 7 | internal/dungeon/stepper.go:458 builds a game-object GUID from entry and spawn |
| `packet.High*` constants | 6 | cmd/forever-agent/plan_scout_live.go:953 |
| `.High()` / `.Entry()` / `.IsCreature()` calls | 2 / 3 / 1 | internal/dungeon/remote_cc.go:230 |
| decimal `FormatUint/ParseUint` of a guid | 11 | cmd/forever-agent/addon_host.go:475 |
| hex printf of a guid (`%016X`, `0x%X`) | 7 | internal/tbm/commander.go:290 `GUIDArg` (26 callers in 10 files) |
| `packet.ParseGUID` | 9 | cmd/forever-agent (8), internal/raidwalk (1) |

Plan files: `dungeons/*.json` hold `"spawn"` ids (42 keys in 3 files) typed `uint32`
(internal/dungeon/plan.go:235, :688, :941, :1099); no plan file stores a GUID. The `"guid"` keys
found are in 5 test fixtures only. No SQL export of GUIDs was found by these greps.

`tbm.GUIDArg` sends a GUID as text inside `.bot` commands to the server bot module; its format
there is a contract with that module, not with the client.

## 6. 1.12 / Turtle-specific code outside the protocol layer

| Item | Callers | Sites |
|---|---|---|
| Turtle LFT (`internal/lft`, addon text `TW_LFG`) | cmd/forever-agent/lfg_methods.go:12 only | 59 `lft.` uses in 1 file |
| Eluna `.tt` toolkit (`internal/eluna`) | cmd/forever-agent/eluna_methods.go:11 only | 3 uses (:49, :64, :69) |
| MPQ (`internal/mpq`) | internal/minimap/extract.go:19, wmo.go:21 | 7 uses |
| Minimap (`internal/minimap`) | cmd/forever-agent/minimap_cmd.go:10, plan_diff_cmd.go:15, internal/dashboard/minimap.go:14, internal/plandiff/svg.go:11 | 30 uses |
| realmd hour-start login hold | cmd/forever-agent/login.go:294, reconnect.go:168 and :178, fleet.go:249 and :394 (via `bookFleetLogin`) | 5 call sites, all in cmd; off switch `TA_LOGIN_HOUR_HOLD` (login_hold.go:13) |
| Bot-module protocol (`internal/tbm`) | 21 files in cmd (256 uses), 33 in dungeon (409), 5 in raidwalk (23), 1 in dashboard (5), 1 in tools (1) | 694 uses in 61 files |
| Headless addon host (`internal/addonhost`) | cmd/forever-agent/addon_host.go:15 only | 8 uses |
| `.bot ...` text commands | cmd 9 files, dungeon 14, raidwalk 3, scenario 1, tbm 6 (unfiltered file counts) | 82 code lines in 27 files (comment lines dropped) |
| GM dot commands (`.gm`, `.go`, `.tele`, `.revive`, `.respawn`, `.kill`, `.aura`, ...) | cmd, dungeon, raidwalk, session, eluna | 143 code lines in 38 files; 27 upstream calls of `Session.GM*` methods |

`Session.Teleport` is itself a GM line (`.go xyz`, internal/session/actions.go:616-625).

## 7. Event log seam

Session emits through `s.emit` (106 calls, internal/session/session.go:538). The protocol
packages reference 78 distinct `events.Kind*` constants. Upstream reads 37 of them, most often
`KindSystem` 27, `KindAddon` 18, `KindChat` 7, `KindWhisper` 7, `KindQuestError` 5,
`KindQuestOfferReward` 5 (example: cmd/forever-agent/commander_methods.go:64). The keys inside
`Event.Data` are an untyped contract; see Unknowns. `events.Event` itself carries
`SourceGUID`/`TargetGUID packet.GUID` (internal/events/log.go:180).

## Proposed game.Client interface

The interface is the method set of `*session.Session` that the upper layers call today. It sits in
a new neutral package (called `world` here). All file:line refs are in `internal/session/`.
Every `packet.GUID` becomes `world.GUID`, every `objects.X` becomes `world.X`.

```go
package world

// GUID is opaque: comparable (map key), hex in JSON, width hidden. 1.12: Lo only.
type GUID struct{ Hi, Lo uint64 }
func (g GUID) Kind() Kind     // player, creature, pet, gameobject, dynobj, item, corpse
func (g GUID) Entry() uint32
func (g GUID) IsZero() bool

type Client interface {
	// Lifecycle (8)
	Run(ctx context.Context) error                       // session.go:446
	Ready() <-chan struct{}                             // session.go:392
	Done() <-chan struct{}                              // session.go:395
	Log() *events.Log                                   // session.go:397
	Redact(text string) string                          // session.go:400
	State() Status                                      // session.go:402
	Status() StatusInfo                                 // session.go:414; drop Unknown (opcode names) or rename to UnknownMessages
	Logout(ctx context.Context) (LogoutOutcome, error)  // session.go:825

	// World view (6)
	Units(f Filter) []Unit                              // world.go:68
	Unit(g GUID, name string) (Unit, error)             // world.go:99
	GameObjects(f GOFilter) []GameObject                // gameobject.go:29
	DynamicObjects() []DynamicObject                    // sight.go:66
	Team() string                                       // openobject.go:223
	TeamOf(g GUID) string                               // replaces RaceOf openobject.go:207 (raw race byte) + session.Team

	// Movement (7)
	WalkTo(ctx context.Context, x, y, z float32) error              // actions.go:422
	WalkPath(ctx context.Context, x, y, z float32) (WalkReport, error) // navwalk.go:100
	PathTo(ctx context.Context, x, y, z float32) (PathInfo, error)  // navwalk.go:79
	Face(ctx context.Context, g GUID) error                         // actions.go:389
	Teleport(ctx context.Context, x, y, z float32, mapID int) error // actions.go:618 (GM text)
	LineClear(a, b [3]float32) (clear, known bool)                  // sight.go:34 (navmesh, not wire)
	SnapToMesh(p [3]float32) ([3]float32, bool)                     // sight.go:52 (navmesh, not wire)

	// Combat and casting (14)
	Target(ctx context.Context, g GUID, name string) (Unit, error)  // actions.go:240
	SelectConfirmed(ctx context.Context, g GUID, wait time.Duration) error // selection.go:267
	Attack(ctx context.Context) error                               // actions.go:290
	StopAttack(ctx context.Context) error                           // actions.go:312
	Stand(ctx context.Context) error                                // actions.go:308
	CancelAutoRepeat(ctx context.Context) error                     // actions.go:319
	Cast(ctx context.Context, spell uint32, target GUID) error      // actions.go:325
	CancelCast(ctx context.Context, spell uint32) error             // actions.go:384
	CancelAura(ctx context.Context, spell uint32) error             // actions.go:347
	CastOnMainHand(ctx context.Context, spell uint32) error         // actions.go:365
	CastOnTradeSlot(ctx context.Context, spell uint32, slot uint8) error // tradestep.go:535
	SpellState() *spellstate.State                                  // spells.go:32; CastRefused must take a neutral reason, not a 1.12 code
	CastState() (CastStateInfo, error)                              // spells.go:194
	Spells(f SpellsFilter) ([]SpellRow, error)                      // spells.go:273

	// Chat (3) and addon messages (1)
	Chat(ctx context.Context, typ, text, to string) error           // chat.go:88
	JoinChannel(ctx context.Context, name, password string) error   // social.go:26
	LeaveChannel(ctx context.Context, name string) error            // social.go:35
	AddonSend(ctx context.Context, channel, prefix, text, to string) error // chat.go:103

	// GM text commands (9): wire-neutral, server-specific
	GM(ctx context.Context, text string) error                      // chat.go:120
	GMOn(ctx context.Context, g GUID, text string) error            // chat.go:155
	GMOnConfirmed(ctx context.Context, g GUID, text string) error   // chat.go:226
	GMKill(ctx context.Context, g GUID) error                       // chat.go:184
	GMAura(ctx context.Context, g GUID, spell uint32) error         // chat.go:203
	GMEvade(ctx context.Context, g GUID) error                      // practice.go:85
	GMSetHP(ctx context.Context, g GUID) error                      // practice.go:102
	GMRespawn(ctx context.Context, g GUID) error                    // practice.go:128
	GMRevive(ctx context.Context, name string) error                // practice.go:150

	// Group and raid (11)
	Party() PartyInfo                                               // party.go:71
	Invite(ctx context.Context, name string) error                  // party.go:154
	AcceptInvite(ctx context.Context) error                         // party.go:161
	LeaveGroup(ctx context.Context) error                           // party.go:188
	Promote(ctx context.Context, g GUID, name string) error         // groupctl.go:129
	Assistant(ctx context.Context, g GUID, name string, clear bool) error // groupctl.go:151
	ConvertRaid(ctx context.Context) error                          // groupctl.go:176
	RaidMove(ctx context.Context, g GUID, name string, subgroup int, swapWith string) error // groupctl.go:193
	Disband(ctx context.Context) error                              // groupctl.go:253
	SetLootMethod(ctx context.Context, method string, master GUID, masterName string, threshold uint8) error // groupctl.go:274
	Mark(ctx context.Context, g GUID, name, icon string) (MarkResult, error) // raid.go:39

	// Loot (8)
	Roll(ctx context.Context, ref RollRef, choice string) (RollInfo, error) // loot.go:60
	LootWindow(ctx context.Context) (*LootWindow, error)            // lootwindow.go:103
	OpenLoot(ctx context.Context, g GUID, name string, wait time.Duration) (*LootWindow, error) // lootwindow.go:120
	TakeLoot(ctx context.Context, take LootTake, wait time.Duration) (LootTaken, error) // lootwindow.go:253
	CloseLoot(ctx context.Context, wait time.Duration) error        // lootwindow.go:374
	OpenObject(ctx context.Context, g GUID, spell uint32, wait time.Duration) (*LootWindow, error) // openobject.go:57
	LootObject(ctx context.Context, g GUID, spell uint32, openWait, takeWait time.Duration) (LootedObject, error) // openobject.go:153
	LootCorpse(ctx context.Context, g GUID, wait time.Duration) (LootedObject, error) // openobject.go:162

	// Gossip and NPCs (9)
	UseObject(ctx context.Context, g GUID) (GameObject, error)      // gameobject.go:55
	GossipHello(ctx context.Context, npc GUID) error                // social.go:70
	GossipSelect(ctx context.Context, npc GUID, option uint32, code string) error // social.go:89
	ActivateTaxi(ctx context.Context, npc GUID, nodes []uint32) error // social.go:155
	VendorList(ctx context.Context, vendor GUID) error              // vendor.go:54
	Buy(ctx context.Context, vendor GUID, ref BuyRef) error         // vendor.go:73
	Sell(ctx context.Context, vendor GUID, ref EquipRef, count uint32) (Item, error) // vendor.go:138
	TrainerList(ctx context.Context, trainer GUID) error            // vendor.go:301
	Train(ctx context.Context, trainer GUID, spell uint32) error    // vendor.go:310

	// Quests (7)
	QuestLog() []QuestSlot                                          // quests.go:58
	QuestHello(ctx context.Context, npc GUID) error                 // quests.go:74
	QuestQuery(ctx context.Context, npc GUID, quest uint32) error   // quests.go:90
	QuestAccept(ctx context.Context, npc GUID, quest uint32) error  // quests.go:101
	QuestComplete(ctx context.Context, npc GUID, quest uint32) error // quests.go:123
	QuestReward(ctx context.Context, npc GUID, quest uint32, choice *uint32) error // quests.go:141
	QuestAbandon(ctx context.Context, quest uint32) error           // quests.go:174

	// Items (8) and queries (1)
	Items() []Item                                                  // items.go:62
	Inventory() []Item                                              // items.go:21
	Equipment() []Item                                              // items.go:26
	MainHandItem() (Item, bool)                                     // actions.go:358
	Equip(ctx context.Context, ref EquipRef) (Item, error)          // items.go:41
	UseItem(ctx context.Context, ref EquipRef, target GUID) (Item, error) // useitem.go:47
	Mount(ctx context.Context, ref MountRef, wait time.Duration) (MountInfo, error) // useitem.go:97
	Dismount(ctx context.Context, spell uint32, wait time.Duration) (MountInfo, error) // useitem.go:241
	ItemInfos(ctx context.Context, entries []uint32, wait time.Duration) (map[uint32]ItemInfo, error) // iteminfo.go:55; was game.ItemInfo

	// Trade (7)
	Trade(ctx context.Context, req TradeRequest) (TradeResult, error) // trade.go:414
	TradeOpen(ctx context.Context, to GUID, name string, openWait time.Duration) (TradeStepState, error) // tradestep.go:203
	TradeExpect(ctx context.Context, from GUID, name string, wait time.Duration) (TradeStepState, error) // tradestep.go:249
	TradeSetItem(ctx context.Context, tradeSlot uint8, ref EquipRef) (TradeSetResult, error) // tradestep.go:321
	TradeAccept(ctx context.Context, wait time.Duration) (TradeResult, error) // tradestep.go:406
	TradeCancel(ctx context.Context) (string, error)                // tradestep.go:482
	TradeStatus() TradeStepState                                    // tradestep.go:501

	// Pets (3)
	Pet() PetInfo                                                   // pet.go:231
	PetAction(ctx context.Context, o PetOrder) error                // pet.go:286
	PetOfPlayer(owner GUID) (Unit, bool)                            // pet.go:432

	// Death (6)
	Death() DeathInfo                                               // death.go:58
	AcceptResurrect(ctx context.Context) (ResurrectRequest, error)  // death.go:121; was game.ResurrectRequest
	ReleaseSpirit(ctx context.Context) error                        // death.go:152
	CorpseQuery(ctx context.Context) (Corpse, error)                // death.go:170; was game.Corpse
	ReclaimCorpse(ctx context.Context) error                        // death.go:203
	AreaTrigger(ctx context.Context, id uint32) error               // death.go:228
}
```

108 methods: lifecycle 8, world view 6, movement 7, combat/casting 14, chat 3, addon 1, GM 9,
group/raid 11, loot 8, gossip/NPC 9, quests 7, items 8, queries 1, trade 7, pets 3, death 6.
Left out because nothing upstream calls them: `ClearTarget`, `Race`, `SelectionConfirmed`,
`SelectionAmbiguous`, `ComboPoints`, `Mounted`, `ServerSelection`, `Store`.

Leaks named in the sketch: `packet.GUID` -> `world.GUID` (all areas); `objects.*` ->
`world.*`; `game.ResurrectRequest/Corpse/ItemInfo` -> `world.*`; `RaceOf` returns a raw
`UNIT_FIELD_BYTES_0` byte -> `TeamOf`; `StatusInfo.Unknown` is keyed by 1.12 opcode names;
`spellstate.CastRefused(code uint8)` takes a 1.12 cast-result code. Splitting the 108 methods
into per-area interfaces (as `brain.Game` already does) is optional; one struct implements all.

## Refactor order

Every step keeps `*session.Session` as the only implementation, so the 1.12 tests keep proving
behaviour. Steps 1-4 use Go type aliases (`type X = Y`), which change no behaviour.

Status: steps 1-4 done on `refactor/world-types` (VM suite green after each).

1. **Neutral package with GUID alias.** Add `internal/world` with `type GUID = packet.GUID`.
   Rewrite upstream imports of `packet.GUID`/`ParseGUID`: 539 uses in 95 non-test files, plus
   114 test files (765 `packet.` uses). Mechanical (gofmt/sed).
2. **GUID layout helpers.** Replace the upstream bit tricks with `world` functions
   (`SpawnLow(g)`, `CreatureGUID(entry, spawn)`, `GameObjectGUID(...)`, `IsPet(g)`), implemented
   with today's 1.12 maths: at most 28 lines (9 masks, 7 shifts, 6 `High*`, 6 method calls;
   the rows may share lines).
3. **Move snapshot types.** Move `Unit`, `Aura`, `Cast`, `Item`, `GameObject`, `DynamicObject`,
   `QuestSlot`, `Filter`, `GOFilter`, `GOTypeByName` to `world`; leave aliases in `objects`.
   Upstream: 285 uses in 64 files (tests: 1,361 uses in 86 files; aliases mean tests can move later).
4. **Move session and game result types.** `StatusInfo`, `PartyInfo`, `LootWindow`, trade and
   pet types (56 distinct `session.` names used upstream) and `game.ResurrectRequest/Corpse/
   ItemInfo/Facing/InitialSpells` to `world`, aliases left behind. Upstream: `session.` 214 uses
   in 36 files, `game.` 17 uses in 6 files (section 1 greps).
5. **Cast-failure reason.** Give `spellstate.CastRefused` a neutral reason enum; session maps
   1.12 codes. 9 uses in 1 file plus its session caller(s).
6. **Define `world.Client`** and assert `var _ world.Client = (*session.Session)(nil)`. Change the
   four cmd adapters and `mapview.SessionFrame` (internal/mapview/source.go:28) to hold a
   `world.Client`: 68 `*session.Session` references in 22 cmd files.
7. **Login behind a factory.** `login.go`, `reconnect.go`, `fleet.go` get a
   `Connect(ctx, settings) (world.Client, error)`; `auth`/`worldconn` uses (24 in cmd) and
   the hour-start hold move behind it.
8. **Mark 1.12-only tools.** `capture`, the replay path of `mapview`, `capturecmd.go`, `lft`,
   `eluna`, `mpq/minimap` stay 1.12-only; they are not on the interface.
9. **Only then** change `world.GUID` from an alias to the struct, and fix the uint64 sites from
   section 5 (43 casts, 15 uint64 fields, 11 decimal formats, 7 hex formats).

## Unknowns

- **Exact field-access counts.** Section 3 counts names, not types. A `go/types` pass
  (`gopls references` on each field, or a small `golang.org/x/tools/go/packages` program) gives
  exact numbers.
- **Event `Data` keys.** Upstream reads `e.Data["..."]` from 37 kinds; the key set was not
  enumerated. A grep of `Data\["` per kind on both sides would list the contract.
- **Spawn id vs GUID on the new server.** The 9 mask sites assume the GUID low part equals the
  DB spawn id. Whether ForeverCore sends the spawn id in the creature GUID must be checked on a
  live object or in its `ObjectGuid` creation code.
- **Server-side features.** Whether the new server runs the bot module (`.bot`, `tbm`), an Eluna
  `.tt` toolkit, Turtle LFT, and the same GM command syntax (`.go xyz`, `.tele name`,
  `.gm visible`, `.revive`) decides whether 143 GM lines, 82 `.bot` lines and 694 `tbm` uses
  survive. Needs the server's command table.
- **Game data ids.** `internal/spells`, `internal/kbspells`, plan entries, map ids and
  area-trigger ids are 1.12 data; whether 1.60.1 keeps the same ids was not checked.
- **Enum values in snapshots.** `Unit.Class`, `PowerType`, `StandState`, game-object `Type` and
  `State` are 1.12 values; whether the new update fields use the same numbers was not checked.
- **Navmesh.** `LineClear`/`SnapToMesh`/`PathTo` use `internal/navmesh` data built for 1.12 maps;
  reuse for the new client's maps was not checked.
- **Hidden callers.** Interfaces in `dungeon` and `brain` may be satisfied by types outside
  `cmd`; only the four adapters above were traced.
