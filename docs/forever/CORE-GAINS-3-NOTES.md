# Forever core gains 3 notes: game data asked from the server

Scope: item 6 of FOREVER-AGENT-CORE-GAINS (game data from the server):
a small client for `CMSG_DB_QUERY_BULK` -> `SMSG_DB_REPLY`, record decoders
for SpellName / SpellMisc / FactionTemplate, a bounded per-session cache,
batched requests, and the spell-name lookup wired into the place the agent
shows spell names today (the dashboard card) with an id fallback. Checked in
ForeverCore at d02645dd21. No captures read (the brief forbids packet
captures); every byte below is the server's own reader/writer.

## The packets

`CMSG_DB_QUERY_BULK` (Opcodes.h:315, `0x430010`) -> `HandleDBQueryBulk`
(HotfixHandler.cpp:31-70): the client sends a table hash and a list of
record ids; the server answers with one `SMSG_DB_REPLY` (`0x490000`,
Opcodes.h:1433) per id, inline, in the same order.

The query's reader (HotfixPackets.cpp:66-74):

| field | type |
|---|---|
| table hash | `uint32` |
| record count | `BitsSize<13>` (flushed) |
| record ids | `uint32` each |

The reply's writer (:57-66):

| field | type |
|---|---|
| table hash | `uint32` |
| record id | `uint32` |
| timestamp | `uint32` (GameTime) |
| status | `Bits<3>` (`HotfixRecord::Status`), flushed |
| data size | `uint32` |
| data | bytes: `DB2StorageBase::WriteRecord` in DB2LoadInfo field order |

`DB2StorageBase::WriteRecord` (DB2Store.cpp:42-97) writes every meta field
in order (`FT_INT`/`FT_FLOAT`/`FT_BYTE`/`FT_SHORT`/`FT_LONG` raw, `FT_STRING`
as a C string in the session locale with enUS fallback, `:76-88`), then the
hotfix optional-data pairs the handler appends (HotfixHandler.cpp:52-58).
The parsers read exactly that order.

Status (`HotfixRecord::Status`, DB2Stores.h:405-414): `Valid` (1) carries
the record; `Invalid` (3, the "not found" answer for an entry the store
does not hold), `RecordRemoved` (2) and `NotPublic` (4, the TactKey answer,
Handler :62-67) carry none. A non-Valid status and an empty record are
typed results (`Reply.Found()` is false), never errors. A status above
`NotPublic` is a hostile packet and an error.

Batching: the handler loops over every query in the packet, so the only
wire bound on one query is the 13-bit count. The client batches at
`MaxRecordsPerQuery = 64` anyway: small packets, and the round's tests pin
the split.

## The tables

The hash is the DB2 file header's own `TableHash` (DB2FileLoader.cpp:1740,
stored keyed in `DB2Stores.cpp:649`): the handler keys its store map on it,
so the client must send the same value. None is a constant in the core
source; each was verified two ways:

| table | hash | verified |
|---|---|---|
| SpellName.db2 (FileDataId 1990283, LayoutHash `0x782EE721`) | `0x46C66698` (1187407512) | read from a local SpellName.db2 header (WDC5), and the `hotfix_data`/`hotfix_blob` rows the core SQL pairs with `spell_name` ids (2026_10_30_21) |
| SpellMisc.db2 (FileDataId 1003144, LayoutHash `0x434B3607`) | `0xC603EE28` (3322146344) | the `hotfix_data` rows the core SQL pairs with `spell_misc` ids (2026_10_30_21) |
| FactionTemplate.db2 (FileDataId 1361579, LayoutHash `0x22B6DC22`) | `0x1018ADD5` (270052821) | the `hotfix_data` row for faction_template id 3561 (2026_10_01_02 sniff, line 16928) |

Record layouts (all `WriteRecord` Meta order, i.e. the Meta fields for the LoadInfo):

The id travels in the reply header, not the record: every Meta here has
IndexField -1, so WriteRecord skips the leading LoadInfo ID on the wire
(DB2Store.cpp:47-48, DB2Meta.cpp:22-25) and the record is the Meta fields
alone. The decoders take the reply's RecordID so the result still names its
record.

* SpellName (SpellNameMeta, 1 field): C-string name (LoadInfo ID, Name).
* SpellMisc (SpellMiscMeta, 17 fields): 17 int32 attributes, int16
  difficulty, three uint16 indexes, range index, uint8 school mask, three
  floats, seven int32 (icons, tuning, visuals), parent SpellID
  (ParentIndexField 16).
* FactionTemplate (FactionTemplateMeta, 7 fields): uint16 faction, int32
  flags, the three group bytes, then the two 8-entry uint16 lists
  (`MAX_FACTION_RELATIONS` 8, DB2Structure.h:1710). `IsFriendlyTo` /
  `IsHostileTo` / `IsHostileToPlayers` are the core's own helpers
  (DB2Structure.h:1726-1756) with `FACTION_MASK_PLAYER = 1`
  (DBCEnums.h:1012-1014).

## The code

* `internal/forever/tables`: `BuildQuery` (batches), `ParseReply`
  (short/hostile replies are errors, statuses are typed results; the
  record's own trailing bytes are the hotfix optional-data pairs, not a
  framing error), the SpellName and FactionTemplate decoders (`ParseSpellName`
  reads the name's own terminator and ignores the tail; the fixed-size
  FactionTemplate reads its seven Meta fields and ignores the tail, both per
  HotfixHandler.cpp:52-58), `Cache` (bounded LRU over (table, id) behind its
  own mutex; a miss is cached for `DefaultNegativeTTL` (5 min), a found
  record never lapses), `ErrUnexpectedReply` (a reply for another table/id is
  an error: the server answers inline, so it is hostile, not late).
  `ParseSpellMisc` is removed: no production path reads school masks, so the
  decoder had no caller.
* `internal/forever/wire/opcodes.go`: `CMSGDBQueryBulk` and `SMSGDBReply`
  plus their names (`SMSG_DB_REPLY` was already in servernames_gen).
* `internal/forever/world/dbquery.go`: `fetchRecords` (deduped ids, cache
  first, one batch per `BuildQuery` packet, one `SMSG_DB_REPLY` per id inside
  one `dbQueryDeadline` (3 s) for the batch via `waitForUntil`; a batch whose
  reply never comes is an error, and the unanswered ids are cached as misses
  for a while, so a silent server costs one deadline per while, not one per
  lookup. A short/hostile reply, or a wrong-table/id reply, is an error and
  caches nothing), `SpellName` (name or false; false means show the id),
  `spellNamesFor` (one batched fetch for the ids a page names),
  `ownFactionTemplate`/`factionTemplate`/`hostileToSelf` (the character's
  own template, fetched once and cached, against the mob's: the core's own
  `Unit::IsHostileTo` direction). The cache lives on the session (`s.db`),
  made on first use.
* `internal/forever/world/combatlog.go`: `CombatRow.spell_name` (the
  server's own name for the row's `spell_id`, filled when the session pages
  the log; empty means the server holds no name or the fetch failed, and the
  row's `spell_id` is the fallback).
* `internal/forever/world/serve_session_methods.go`: `serveEventsPage`
  fills `spell_name` from `spellNamesFor` (one batched, cached fetch per
  page; the fetch runs on the session goroutine like every wait), and the
  `units` hostile filter is ported: the mob's template against the
  character's own, fetched once per unknown template and cached. The pull
  gate (`friendlyUnit`) is unchanged: it still reads NPC flags, and template
  wiring there waits for a live session.
* `internal/dashboard/card.go`: `SetServerSpellNames` plus the
  `spellText` fallback chain: server name, then the packed table
  (`spells.Name`), then `"spell <id>"` as today, `""` for none. The map sits
  behind a mutex (the page reads it while a session-side fetch may replace
  it). It still has no production writer: the dashboard is a separate
  process and `bot_view` carries spell ids, not names, so live server names
  reach the page through the session's `events` rows (`spell_name`), not the
  card. Wiring the card waits for a poller-side name channel.

Out of scope, noted: the cache has an entry bound (256) but no total byte
budget (a 1 MiB hostile record retained per entry); the query/reply fixtures
are hand-made from the core's reader/writer, not core-produced bytes (the
round forbids captures, and there was no live run). Both wait for a live
server check.

## Tests

`internal/forever/tables/tables_test.go`: query/reply round trips with
hand-made fixtures, short/hostile replies never panic (every truncation,
an oversize size, status 7, short records), not-found statuses and empty
records as typed results, the cache bound (eviction order, misses cached
with a TTL, use-refresh, concurrent read/write under `-race`), the batch
split, the hotfix optional-data tail (a tailed SpellName and FactionTemplate
still parse, per HotfixHandler.cpp:52-58), and faction hostility (Stormwind
vs Horde/Alliance-player templates with the real player bit, list vs mask
order, `IsHostileToPlayers` only with the player bit in the enemy group).
`internal/forever/world/dbquery_test.go`: the fetch against the
handler-boundary fake (asks once, caches, batches of 64, misses cached
with expiry, wrong-table and short replies refused, duplicate ids asked
once, one deadline per batch for silent and answered batches, timed-out
ids negatively cached), the spell names on `serveEventsPage` (two named
ids and one unnamed in one batch, real session path), and the `units`
hostile filter (Horde mob kept, Alliance mob and self kept by the friendly
filter, one batched template fetch). `internal/dashboard`:
`TestServerSpellNamesOverride` (server wins, packed table behind it for an
id the server did not name — the assert names `Rejuvenation`, so it can
fail — clear restores today, unknown stays `"spell <id>"`) and the
concurrent names test under `-race`.

## Gates

`make fmt-check`, `make cross`, `staticcheck`, `vm-test ta ~/wt-fa-gains3`
RESULT: PASS  kind=ta wt=wt-fa-gains3 sha=ea696546 dirty=0 files=2332.

## Live check (no live run this round)

On a test server with a character in the world, ask for three known spell
ids and one faction template and read back the names/masks:

1. SpellName 133 (Fireball), 774 (Rend), 133 (again: the second fetch
   must not send).
2. FactionTemplate 12 (Stormwind: faction 72, Alliance group, enemy
   Horde) and 29 (Orc): the pair must read hostile both ways.
3. SpellName 1 (no such spell): the reply status must be Invalid and the
   card must show `spell 1`.
