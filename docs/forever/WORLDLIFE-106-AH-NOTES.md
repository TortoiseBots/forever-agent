# Forever world life, issue #106 part 2: the auction house

Scope: the auction house packets and daemon methods, as the second PR of #106 (the first is trainers and mail).
Code: `internal/forever/auction` (the wire layer), `internal/forever/wire` (opcodes), `internal/forever/world`
(`auction.go` the session glue, `serve_auction_methods.go`), `internal/events` (ten kinds).

Everything below was checked in ForeverCore at 6517af05 (`git show 6517af05:<path>`). Fixtures are hand-built from
the core's writers and readers; no capture byte is in the repository. turtle-agent has no auction house, so the method
names and fields are this server's own.

Status: implemented and tested against the fake world. **No live run yet**: the live checks for the lead's queue are
at the end.

## The protocol is not Turtle's

The core speaks the modern bucket protocol, not the old list-by-page one: a browse answers **buckets** (one row per
item and item level with the quantity for sale and the lowest price), a bucket answers its **auctions**, a stackable
item is a **commodity** posted by unit price and bought by **quote then confirm**, and any other item is posted with a
minimum bid and a buyout and bid on by auction id (AuctionHouseHandler.cpp). Classic 1.60's run times are 2, 8 and 24
hours (AuctionHouseMgr.h:53; the post takes minutes, 120/480/1440, AuctionHouseHandler.cpp:641-647 and :859-865).
Which of the two an item is follows its stack size (a stack above one is a commodity, AuctionHouseHandler.cpp:291, :666 and :878); the client has no item template, so a post says `commodity` and the server's `item_not_found`
answers a wrong guess.

The core drops a request silently when the player is throttled (`CheckThrottle`) or the auctioneer cannot be
interacted with, so a read that gets no answer is a result (`ok: false`, an `auction_result` event with reason
`no_answer`), and a command that gets none is an error that says its outcome is unknown.

## The client's packets

| Packet | Core opcode | Body | Core |
|---|---|---|---|
| `CMSG_AUCTION_HELLO_REQUEST` | 0x3E005F | auctioneer (packed GUID) | AuctionHousePackets.cpp:326-329, AuctionHouseHandler.cpp:196-210 |
| `CMSG_AUCTION_BROWSE_QUERY` | 0x3E0064 | auctioneer, offset, min and max level, two unused bytes, filter mask, known-pet count and bytes, pet level, 8/3/2-bit lengths (name, class filters, sorts), name, filters, sorts | AuctionHousePackets.cpp:271-306 |
| `CMSG_AUCTION_LIST_ITEMS_BY_BUCKET_KEY` | 0x3E0065 | auctioneer, bucket key (20-bit item id, pet flag, 11-bit level, suffix flag, a flush, optional uint16s), offset, an unknown byte, 2-bit sort count, sorts | AuctionHousePackets.cpp:366-380 |
| `CMSG_AUCTION_LIST_OWNED_ITEMS` | 0x3E0067 | auctioneer, offset, 2-bit sort count, sorts | AuctionHousePackets.cpp:398-410 |
| `CMSG_AUCTION_SELL_ITEM` | 0x3E0060 | auctioneer, min bid, buyout, run time (minutes), 6-bit count (exactly one, used once), the item's packed GUID and count | AuctionHousePackets.cpp:459-473, AuctionHouseHandler.cpp:816-956 |
| `CMSG_AUCTION_SELL_COMMODITY` | 0x3E006D | auctioneer, unit price, run time, 6-bit count, the stacks | AuctionHousePackets.cpp:444-457, AuctionHouseHandler.cpp:610-814 |
| `CMSG_AUCTION_PLACE_BID` | 0x3E0063 | auctioneer, auction id, amount | AuctionHousePackets.cpp:412-420, AuctionHouseHandler.cpp:356-488 |
| `CMSG_AUCTION_REMOVE_ITEM` | 0x3E0061 | auctioneer, auction id, item id | AuctionHousePackets.cpp:422-430, AuctionHouseHandler.cpp:490-554 |
| `CMSG_AUCTION_GET_COMMODITY_QUOTE` | 0x3E006A | auctioneer, item id, quantity | AuctionHousePackets.cpp:481-489, AuctionHouseHandler.cpp:161-194 |
| `CMSG_AUCTION_CONFIRM_COMMODITIES_PURCHASE` | 0x3E006B | the same four fields | AuctionHousePackets.cpp:316-324, AuctionHouseHandler.cpp:123-159 |

Every packet ends with the tainted-by flag (an optional addon info), which this client never sets. The wire numbers
are the `ClassicClientOpcodes.inc` region-2 rows and are pinned for both supported client builds in
`wire/opcodes_test.go`. The favourite, replicate, bidded-list, bucket-keys and item-id listing packets and the
cancel-commodities-purchase packet are not built: nothing asked for them.

The failing case for the post layout is **`TestSellItemReadsBackThroughTheServersReader`**, which reads the bytes
with a reader written independently from `AuctionSellItem::Read`.

## The server's answers

| Packet | Core opcode | Body | Core |
|---|---|---|---|
| `SMSG_AUCTION_HELLO_RESPONSE` | 0x450199 | auctioneer (packed), purchase delay, cancel delay, house id, open bit | AuctionHousePackets.cpp:547-557 |
| `SMSG_AUCTION_COMMAND_RESULT` | 0x45019B | int32 auction id, command, error, bag result, bidder (packed), min increment, money, delay | AuctionHousePackets.cpp:501-513 |
| `SMSG_AUCTION_LIST_BUCKETS_RESULT` | 0x4502F5 | count, delay, filter mask, total, buckets, browse-mode and has-more bits | AuctionHousePackets.cpp:147-178 (a bucket), :573-588 |
| `SMSG_AUCTION_LIST_ITEMS_RESULT` | 0x4502F6 | count, total, delay, auctions, 2-bit list type, has-more bit, a flush, bucket key, total quantity | AuctionHousePackets.cpp:590-610 |
| `SMSG_AUCTION_LIST_OWNED_ITEMS_RESULT` | 0x4502F8 | int32 count, int32 sold count, delay, auctions, sold auctions, has-more bit | AuctionHousePackets.cpp:612-628 |
| `SMSG_AUCTION_GET_COMMODITY_QUOTE_RESULT` | 0x4502FA | three presence bits (price, quantity, duration), item id, delay, then the present values (the duration is an int64 of milliseconds) | AuctionHousePackets.cpp:515-533 |
| won, outbid, owner-bid, closed notifications | 0x45019C-F | a bidder or owner notification, plus the bid and increment, the bidder, or the proceeds delay and the sold bit | AuctionHousePackets.cpp:491-499, :630-646, :663-668 |

An auction (`AuctionItem`, AuctionHousePackets.cpp:180-259) opens with up to 17 bits: item present, 4-bit enchant count,
2-bit gem count, min bid, min increment, buyout and unit price present, the two censor bits, key and creator present
and, when the bid is not censored, bidder and bid amount present. A byte write flushes the pending bits first
(ByteBuffer.cpp:88-94), so the bit group is followed by the bidder's packed GUID and the bid amount, then the fixed
fields (count, charges, flags, auction id, owner, duration left, delete reason), the optional item, the enchants and
gems, the prices, the server-side info (item GUID, owner account and end time unless censored), the bucket key and
the creator. A listing of someone else's auctions censors the bid; the owned list shows it.

The failing case for the answers is **`TestItemListSecondAuctionAfterAFullFirst`**: a first auction with every
optional present and an uncensored bid, then a second one, whose id must parse after the exact bytes of the first.
`TestItemBitGroupLengths` pins the 15- and 17-bit groups.

## The methods

All answers carry `since` (the event-log cursor before the first packet) and `event` (the row that answers). Prices
are copper. Preconditions (bad parameters, an auctioneer that is not in the world model, out of reach, without the
auctioneer flag) are RPC errors before any packet.

| Method | Parameters | What it does |
|---|---|---|
| `auction_hello` | `guid`, `wait_ms` | opens the house: `house_id`; `ok` is the open-for-business bit |
| `auction_browse` | `guid`, `name`, `min_level`, `max_level`, `offset` | `buckets[]` (`item_id`, `item_level`, `total_quantity`, `required_level`, `min_price`, `contains_owner_item`), `total`, `more` |
| `auction_items` | `guid`, `item_id`, `item_level`, `offset` | the auctions of one bucket: `auctions[]` (`auction_id`, `item_entry`, `count`, `owner`, `min_bid`, `min_increment`, `buyout`, `unit_price`, `bid`, `bidder`, `duration_left_ms`), `commodities` |
| `auction_owned` | `guid`, `offset` | the character's own `auctions[]` and the ones that sold (`sold[]`) |
| `auction_sell` | `guid`, the item (`item_entry`, or `bag` and `slot`), `commodity`, `min_bid` and `buyout` (an item) or `unit_price` and `count` (a commodity), `hours` (2, 8 or 24, default 24), `force` | posts; answers the `auction_id` (0 for a commodity) and the purse before and after (the deposit) |
| `auction_bid` | `guid`, `auction_id`, `amount` | bids, or buys out when the amount is the buyout; answers the result and the purse |
| `auction_cancel` | `guid`, `auction_id`, `item_id` | cancels one of the character's own auctions (5 percent of the bid when someone has bid, AuctionHouseHandler.cpp:515-524) |
| `auction_quote` | `guid`, `item_id`, `quantity` | what a quantity of a commodity costs now: `quote` (`total_price`, `quantity`, `valid_ms`); ok false when the house cannot fill it |
| `auction_buy` | `guid`, `item_id`, `quantity` | buys the quoted quantity; refused before any packet without a quote for this item and quantity, or with one that has run out on the client's clock |

The item to post is one of the character's own bag items: never an equipped item or a worn bag, and an item whose slot
the inventory has not confirmed is refused (the same rules as `mail_send` and `sell`). An item post is one item
(`count` above one is refused) with `min_bid`, `buyout` or both (a minimum bid above the buyout is refused); a commodity
post takes `unit_price` and posts `count` of the stack (the whole stack when omitted). Any command (post, bid, cancel, buy) whose result never came blocks every later command for the rest of the session
unless the caller sets `force: true` on each: the command may still complete, and its late answer cannot be told from
the next command's (a commodity post and a commodity purchase both answer with auction 0). Check `auction_owned` and
the purse before forcing.

## Live checks (for the lead's queue)

1. At an auctioneer with a GM character holding a non-stackable item and a stack of a stackable one:
   `auction_hello`, then `auction_sell` the item (min bid and buyout, 2 hours) and the stack (`commodity`, a unit
   price): both answer ok, the purse drops by the deposit, and `auction_owned` lists both.
2. `auction_browse` by name finds the buckets; `auction_items` lists the auctions of the item's bucket with the
   prices posted.
3. With a second character of the same faction: `auction_bid` on the first's item below the buyout (the first sees an
   `auction_owner_bid` event), then at the buyout (the second gets `auction_won`, the first `auction_closed` sold with
   the proceeds mail delay); `auction_quote` and `auction_buy` for part of the commodity stack.
4. `auction_cancel` of an auction with no bid, and of one with a bid (5 percent of the bid leaves the purse).
5. A refused bid (the character's own auction, `bid_own`; a bid below the minimum, `higher_bid`) answers `ok: false`
   with the reason.
