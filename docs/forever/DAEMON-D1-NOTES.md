# TBF #170 round D1: item info

What this round adds: the ItemSparse and Item DB2 record decoders in
`internal/forever/tables`, `session.itemInfos` (the two stores joined per entry, through
the session's record cache), and the `item_info` serve method. No live run yet: everything
here is proven against the fake world only.

## The two tables

| Table | TableHash | Layout | Record |
|---|---|---|---|
| ItemSparse | `0x919BE54E` | FileDataId 1572924, LayoutHash `0x6FCC3191` (`ItemSparseMeta`, DB2Metadata.h:12843) | 5 C strings (Description, Display3, Display2, Display1, Display), then 302 bytes: 9 int/float, three 10-entry arrays, 23 int/float, 18 uint16/int16, 18 byte fields |
| Item | `0x50238EC2` | FileDataId 841626, LayoutHash `0x9A2A4834` (`ItemMeta`, DB2Metadata.h:11318) | 43 bytes: ClassID i32, SubclassID ui8, ... |

Both Metas have `IndexField -1`, so the record id never travels in the record: the
reply's own id is passed to the decoder, as with SpellName. The server writes every
record in `DB2LoadInfo` declaration order (`DB2Store.cpp` `WriteRecord`) and appends the
hotfix optional-data pairs after it (`HotfixHandler.cpp`), so the decoders read their own
fields and ignore the tail; a record that ends early is an error.

The layouts are identical in ForeverCore 6517af05 (the core the decoders were first read against)
and 0.1.39 (`d3358c21dc`), so there is no per-core switch here; a layout change arrives
with a new layout hash.

Only the fields the method reports are kept: Name (= Display), Description, Stackable,
SellPrice, BuyPrice, ItemLevel, RequiredLevel, InventoryType, OverallQualityID
(`Quality`), Bonding, ContainerSlots, AllowableClass from ItemSparse; Class, Subclass,
InventoryType from Item.

## The method

`item_info {entry | entries: [1..64 positive ids], wait_ms?}` answers (one entry may be named as `entry`; giving both,
or neither, is refused)
`{items: [{item_entry, found, name?, quality?, quality_name?, class?, subclass?,
inventory_type?, item_level?, required_level?, sell_price?, buy_price?, stackable?,
bonding?, container_slots?}]}` in the request's order, a repeated entry repeated.

- `found` is the ItemSparse record: an entry whose Item record came back without its
  ItemSparse row is not an item the client can show. `class`/`subclass` stay absent when
  the Item store did not answer for a found entry.
- `quality_name` is the client's ladder, 0 poor through 7 heirloom; an id past it is
  omitted, never guessed.
- One `fetchRecords` per table (ItemSparse, then Item), deduplicated: two quest-yield
  queries for a call, and a second call for the same entries sends nothing (the record
  cache also keeps a not-found verdict for a while).
- Bad parameters are errors before any packet: 0 or more than 64 entries, or a non-positive
  id. A failing fetch is an error naming how many of the requested ids were answered.
- `wait_ms` is reserved for the method frame round D2 brings; a non-zero value is an error now, because
  D1's fetch is bounded only by the session's own DB deadline.

## What is not proven

No live run: the record layouts are cited from the core and pinned by hand-made fixtures,
not by a real reply. The DB store's own answer for a real item id is what round L1 sees.
D2 (`inventory`, `equipment`, `equip`, `use_item`) builds the shared method frame on top
of this round.
