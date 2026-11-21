# L4: maps and minimaps from the 1.60 client data

Round L4 of the FOREVER-AGENT-REST spec: a read-only DB2 reader and the Forever minimap extractor. Both are
library code; the `minimap-extract` flags that use them are K5's.

## What was added

| File | What |
|---|---|
| `internal/db2/wdc.go` | `db2.Open`, the WDC4/WDC5 structure (header, sections, field structures, column storage, pallet and common data, string block, ID list, copy table), `File.Maps` |
| `internal/db2/wdc_test.go` | hand-built WDC4 and WDC5 files, every compression, hostile-input tests, the fuzz target |
| `internal/minimap/forever.go` | `RunForever`, the loose-tile stitch, the navmesh fallback, `Options.DB2Dir` |
| `internal/minimap/forever_test.go` | tile stitch, navmesh fallback, prefer validation |

No user-facing flag changed. K5 adds `minimap-extract --forever --client <extract> --db2 <dbc dir>` and the manual
entry; until then the README has no command to describe, which is why this round's notes are the only doc.

## The DB2 layout

The 1.60 client writes WDC5 (`schema` `WOWSTATIC_1_60_1_*`). This round's reader follows the server's loader and the
wowdev format description. The per-file facts below were checked by hand against a real build 70959 `Map.db2` (a manual
step: no real client file is committed, the test files are built by the tests themselves, so they prove the reader
against the layout the tests assume; the real-file counts are under "Real-file check"):

* **Header.** WDC5 adds a version and a 128-byte schema string to the WDC4 header; after them both have record/field
  counts, record size, string table size, table hash, layout hash, min/max id, locale, flags, id index, total field
  count, packed data offset, parent lookups, column meta size, common and pallet data sizes, section count
  (`DB2FileLoader.cpp:1750-1800`). The reader takes WDC4 as the same header without the first 132 bytes and rejects
  the offset-map flag (0x1): its records are variable length, and `Map.db2` does not use it.
* **Sections.** 40 bytes each: tact id, file offset, record count, string table size, offset records end, ID list
  size, relationship data size, offset map id count, copy table count (`DB2FileLoader.h:56-66`). The section's
  records, string table, ID list and copy table are laid out in that order from `file_offset`
  (`DB2FileLoader.cpp:353-376`, `:1955-2010`).
* **Column storage.** Field structures are `(int16 unused bits, uint16 position)`; the column meta (24 bytes per
  column) then carries bit offset, bit size, additional data size, compression and the compression data. The five
  compressions of the server are implemented: none, immediate, common data, pallet, pallet array, signed immediate
  (`DB2FileLoader.cpp:32-40`, `:1808-1851`). The pallet and common blocks must add up to the header's
  `palletDataSize` and `commonDataSize` exactly, or the file is refused as misread. Bit-packed values are read from `packed data offset + the field's
  packed bit offset` with the server's shift-and-mask (`:883-887`).
* **Strings.** From WDC2 on, a string field holds the distance from itself to the string in one blob of every
  section's records followed by every section's strings. The client moves the string tables to the end of that blob
  when loading, so the same value resolves differently in different sections. The reader builds that blob and
  resolves as the client does (`DB2FileLoader.cpp:798-803`; wowdev DB2, WDC2 section).
* **Encryption.** A section with a tact key lists its encrypted ids between the header blocks and the section data;
  the list exists for every keyed section, the server's known `TRINITY` key included (`:1900-1916`). A section the
  key does not open is skipped: its records, strings and ids stay zero, and `File.Notes` says so.
* **Map.db2.** The 70959 layout has 26 columns (`DB2Metadata.h:14538`, layout hash `0xD43AFAC3`): Directory is 0
  (`FT_STRING_NOT_LOCALIZED`), MapName is 1, InstanceType is 8 (`DB2LoadInfo.h:3934-3970`). `Maps` refuses any
  other field count or layout hash instead of reading the wrong columns. The real file has 81 records in 4 sections: two are
  encrypted (2 and 1 records) and one is the known key (5 records). The reader returns 78 rows and notes both skipped
  sections; directories and names spot-check against the client's own data (`Azeroth`/`Eastern Kingdoms`,
  `Shadowfang`/`Shadowfang Keep`, `Blackfathom`/`Blackfathom Deeps`, `Uldaman`, `TanarisInstance`/`Zul'Farrak`,
  `BlackrockDepths`, `Stratholme Raid`/`Naxxramas`). The ID list supplies the ids (flag 0x4); a file that sets the
  flag must give every readable section its list (an `idListSize` of 0 with records is refused, not read past the
  file), and an inline id column is read through the same column storage when a file does not set it (an id column
  that is common data is refused: it is keyed by the id). Copy table rows repeat their source row's values under the
  new id, resolved by the source's id only: a source in a skipped encrypted section, or not an id of the file, is
  skipped and counted in the notes `Maps` returns (never matched by record index, which would copy another map's
  name). Duplicate ids, a string offset that leaves the string block or has no end, and a negative instance type
  are errors.

## Hostile input

Every file is treated as hostile. Before any allocation or read `parse` checks, in uint64: the record size (4 to
65536 bytes), the field count (at most 4096), the section count (at most 256, and that many 40 byte headers must
fit), `records * recordSize`, the pallet and common totals and every block's own size against the file's length;
each section starts after the header blocks, ends inside the file, and all sections together hold no more bytes than
the file has after the header, so the record, string and id blobs are never larger than the file. `Open` refuses a
file over 256 MiB. Every column's whole bit range (plain, packed value, pallet index) must fit the record, so a field
never reads into its neighbour. A value of up to 64 bits at a bit shift reads across nine bytes. A pallet array
column reads every element (`values`); `value` refuses an array. A pallet index past the pallet is an error.

## Minimaps

The extractor is given an outside CASC extractor's listfile-named folder. The tiles are loose BLP2 files under
`world/minimaps/<Directory>/map<Col>_<Row>.blp`, the same `map<A>_<B>` convention `ParseTranslate` reads (A along
world y, B along world x). Every folder and file name is matched case-insensitively; the directory comes from
`Map.db2`. The tiles are decoded with `internal/blp` and laid on one image with `Bounds` (256 px per 533.33-yard
tile in practice, the decoded tile size in general), one decoded tile at a time so a 64 x 64 map does not hold every
tile plus the picture. Mixed tile sizes are refused: they have no single `PxPerYard`.

Sources are the tile picture (`minimap`, `from: "terrain"`) and the navmesh render. An indoor map has no terrain
tiles, so the navmesh render is the fallback; `--prefer` accepts only those two sources and refuses `worldmap`, which
this client draws from UiMapArt (decision 3 keeps that for later). The output is exactly `Run`'s: `<id>.png`,
`<id>.json`, `<id>.<kind>.png/.json` and `<id>.status.json`, so the dashboard and plan-diff read a Forever folder
unchanged. The status file always gives a reason for the unused world-map slot, with the Forever wording, and for a
missing minimap or navmesh.

## Real-file check

A one-off, local run of the strict reader on a real build 70959 `Map.db2` from a client extract (nothing from it is
committed): 81 records in 4 sections, version 5, layout hash `0xD43AFAC3`, 26 of 26 fields; `Maps` read 78 rows with
no error, 3 ids are listed by the 2 skipped encrypted sections, no copy rows. The same counts came from the extracts
of the two other 1.60 builds on hand, whose `Map.db2` is the same. The strict checks (whole bit ranges, pallet and
common totals equal to the header, sections after the header blocks) accepted the real file.

## Tests

* `TestWDCStringFieldOffsetIsRelative` (the failing case): a two-record file whose second record's Directory reads
  `Bmap` (and MapName `B Map`). Both records store the distance from their own field; a reader adding the value to
  the string block or to the file reads past the block and gets nothing.
* `TestMapDB2CopyTable`: a copy table entry (`999` from `209`) duplicates the source's Directory, name and instance
  type under the new id.
* `TestWDC4Header`: the same file with the WDC4 header, opened through `db2.Open`.
* `TestEncryptedSectionSkipped`: the keyed section's record is absent from `Maps` and `Notes` names the section.
* `TestMapDB2RefusesOtherLayout`: a 20-field file, and a 26-field file with another layout hash, are refused.
* `TestCopySourceInSkippedSectionIsSkipped`, `TestDuplicateIDsRefused`, `TestHostileStringsAndIDs`,
  `TestInlineIDs`, `TestInlineIDColumnThatIsCommonData`: copy rows, ids and strings as above.
* `TestColumnCompressions`: plain, unsigned immediate, signed immediate (a negative instance type is refused), pallet
  (an index past the pallet is refused), pallet array (all elements) and common data, hand-built;
  `TestReadBitsCrossesNineBytes` a 60-bit value at bit 5.
* `TestHostileFiles` (one corrupted number per case: huge counts and sizes, sections and strings past the file, an
  id list of size 0 or the wrong size, pallet data of 0xFFFFFFFC bytes, pallet or common totals that disagree with the
  column meta, fields past the record), `TestHostileEncryptedIDCount`, `TestIDListMissingAtFileEnd`,
  `TestOpenRefusesOversizeFile`: each returns an error, none panics or allocates by the declared size.
  `TestEveryTruncationAndByteFlip` parses every prefix of three files and every byte of them set to 0x00, 0x7f, 0x80
  and 0xff. Run against the code before this round, `TestHostileFiles` dies with a fatal out-of-memory error
  (an 8 GiB make) and `TestIDListMissingAtFileEnd` panics with an index out of range.
* `FuzzParse`: `go test -fuzz=FuzzParse -fuzztime=60s ./internal/db2/`; the seeds are hand-built files, never client
  data.
* `TestMapInSkippedSectionSaysSo`: asking for a map listed by an encrypted section names the skipped section; an
  unknown id keeps the plain message. `TestRepeatedMapIDIsOneMap`: a repeated id is one map.
* `TestForeverTileNamesAreStrict`, `TestForeverDuplicateTilesFailTheMap`: only `map<Col>_<Row>.blp` is a tile (no
  trailing junk, nothing past 63); `map29_27.blp` and `map029_27.blp` together fail the map instead of one winning.
* `TestStitchRefusesAnImageOverTheCap`: see "Image cap" below.
* `TestForeverTilesStitch`: a mixed-case `World/MiniMaps/ZulFarrak` folder with `MAP29_27.blp` and `map30_28.BLP`
  stitches north-west tile first, writes the same files, and marks the world map unavailable with a reason.
* `TestNoTilesFallsBackToNavmesh`: an indoor map with an empty folder and a one-polygon mmtile takes the navmesh
  render as `<id>.png` and records the minimap reason.

## Gates

Run on code commit `fa2b2ed5` (clean committed tree; the notes commit after it changes only this file):

* `vm-test ta <local worktree>`: `RESULT: PASS  kind=ta sha=fa2b2ed5 dirty=0 files=2311`, `rc=0`. (An earlier run
  alongside two other jobs on the same VM timed out in `internal/forever/world`, a package this round does not touch,
  after 10 minutes of load; it passed alone.)
* `vm-test ta <local worktree> -- 'go test -race -count=1 ./internal/db2/... ./internal/forever/...'`:
  `RESULT: PASS  kind=ta sha=fa2b2ed5 dirty=0 files=2311`, `rc=0`.
* `go test -fuzz=FuzzParse -fuzztime=60s ./internal/db2/` on the VM: 415,695 executions, no panic, `PASS`.
* `make fmt-check`, `make cross`, `staticcheck ./...`: clean.

## Known limits

* The three encrypted Map.db2 records stay unread (no key), so three maps are absent from `Maps`; `Notes` names the
  sections, and asking `RunForever` for one of them says it is in a skipped section (the section's id list names the
  ids) instead of "not in Map.db2".
* The tile naming convention (`map<Col>_<Row>`, first number the column, west to east) is the 1.12 one, carried over.
  No 1.60 tile of a known place has been checked against it (no extracted minimap folder was at hand), so it is
  unverified for build 70959; the test encodes the code's own reading. The first real extract should be checked by
  stitching a known dungeon entrance.
* Image cap: a stitched picture may be at most 16384 px a side and 8192 x 8192 = 67,108,864 px in all (256 MiB of
  NRGBA, held whole, then PNG-encoded); a larger map (a full 64 x 64 continent) is refused with that number. Every
  dungeon and raid is far below it.
* World-map art (UiMapArt) and WMO indoor minimap composition for this client are later (decision 3); indoor maps get
  the navmesh render.
* The extractor expects the listfile layout (`world/minimaps/...`). A CASC tool that writes hashed names instead
  needs its listfile mode; that is the operator's tool, not this code.
