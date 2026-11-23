// Package auction is the auction house wire layer of the Forever (WoW Classic 1.60.1) client: the packets a player
// sends at an auctioneer (hello, browse, list a bucket, list the own auctions, post, bid, cancel, quote and buy a
// commodity) and the answers and notifications that carry the results. Every layout is the server's own reader or
// writer in ForeverCore at the pinned commit (AuctionHousePackets.cpp, AuctionHouseHandler.cpp,
// AuctionHouseMgr.cpp); the file:line is cited at each field group. Nothing here touches the world model.
//
// The protocol is the modern bucket one, not Turtle's: a browse answers buckets (one row per item and level), a
// bucket answers its auctions, a stackable item is a commodity bought by quote and confirm, and any other item is
// posted with a minimum bid and a buyout. Bits and bytes interleave the way ByteBuffer writes them: a byte write
// flushes the pending bits first (ByteBuffer.cpp:88-94), so every group of bits ends at the next byte field.
package auction

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds on what an answer may name; the lists are the server's data and every count is bounded before an
// allocation follows it.
const (
	// MaxItemsPerList is AuctionHouseResultLimits::Items (AuctionHouseMgr.h:94) with the browse limit as the bucket
	// bound (:93); a list that names more is malformed.
	MaxItemsPerList   = 50
	MaxBucketsPerList = 500
	// maxOwnedItems bounds the owned list, which is not limited by the result limits the same way.
	maxOwnedItems = 1000
	maxAppearance = 4096
	maxEnchants   = 15
	maxGems       = 3
)

// Run times a post may name, in minutes: Classic 1.60 runs 2, 8 and 24 hours (AuctionHouseMgr.h:53,
// AuctionHouseHandler.cpp:641-647 and :859-865: MIN_AUCTION_TIME is two hours and the cases are one, four and twelve
// of it).
const (
	RunTime2h  uint32 = 120
	RunTime8h  uint32 = 480
	RunTime24h uint32 = 1440
)

// ValidRunTime reports whether the core accepts a post of that many minutes.
func ValidRunTime(minutes uint32) bool {
	return minutes == RunTime2h || minutes == RunTime8h || minutes == RunTime24h
}

// AuctionResult is the error code of a command result (AuctionHouseMgr.h:55-71).
const (
	ResultOK                      int32 = 0
	ResultInventory               int32 = 1
	ResultDatabaseError           int32 = 2
	ResultNotEnoughMoney          int32 = 3
	ResultItemNotFound            int32 = 4
	ResultHigherBid               int32 = 5
	ResultBidIncrement            int32 = 7
	ResultBidOwn                  int32 = 10
	ResultRestrictedAccountTrial  int32 = 13
	ResultHasRestriction          int32 = 17
	ResultAuctionHouseBusy        int32 = 18
	ResultAuctionHouseUnavailable int32 = 19
	ResultCommodityPurchaseFailed int32 = 21
	ResultItemHasQuote            int32 = 23
)

// ResultName names a result code; an unknown value is reported as itself.
func ResultName(r int32) string {
	switch r {
	case ResultOK:
		return "ok"
	case ResultInventory:
		return "inventory"
	case ResultDatabaseError:
		return "database_error"
	case ResultNotEnoughMoney:
		return "not_enough_money"
	case ResultItemNotFound:
		return "item_not_found"
	case ResultHigherBid:
		return "higher_bid"
	case ResultBidIncrement:
		return "bid_increment"
	case ResultBidOwn:
		return "bid_own"
	case ResultRestrictedAccountTrial:
		return "restricted_account_trial"
	case ResultHasRestriction:
		return "has_restriction"
	case ResultAuctionHouseBusy:
		return "auction_house_busy"
	case ResultAuctionHouseUnavailable:
		return "auction_house_unavailable"
	case ResultCommodityPurchaseFailed:
		return "commodity_purchase_failed"
	case ResultItemHasQuote:
		return "item_has_quote"
	}
	return fmt.Sprintf("unknown(%d)", r)
}

// AuctionCommand is the command a result answers (AuctionHouseMgr.h:73-78).
const (
	CommandSell   int32 = 0
	CommandCancel int32 = 1
	CommandBid    int32 = 2
)

// CommandName names a command; an unknown value is reported as itself.
func CommandName(c int32) string {
	switch c {
	case CommandSell:
		return "sell"
	case CommandCancel:
		return "cancel"
	case CommandBid:
		return "bid"
	}
	return fmt.Sprintf("unknown(%d)", c)
}

var errShort = errors.New("auction: packet is shorter than its layout")

// exactly reports a short packet or bytes left after the layout.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("auction: %w", errShort)
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("auction: %d byte(s) left after the layout", n)
	}
	return nil
}

// ---- shared structures ----

// BucketKey is AuctionBucketKey (AuctionHousePackets.cpp:40-72): 20 bits of item id, an optional pet species flag, 11
// bits of item level and an optional suffix flag, a flush, then the two optional uint16s.
type BucketKey struct {
	ItemID             uint32
	ItemLevel          uint16
	BattlePetSpeciesID *uint16
	ItemSuffix         *uint16
}

func writeBucketKey(b *wire.Buffer, k BucketKey) {
	b.WriteBits(uint64(k.ItemID), 20)
	b.WriteBit(k.BattlePetSpeciesID != nil)
	b.WriteBits(uint64(k.ItemLevel), 11)
	b.WriteBit(k.ItemSuffix != nil)
	b.FlushBits()
	if k.BattlePetSpeciesID != nil {
		b.WriteU16(*k.BattlePetSpeciesID)
	}
	if k.ItemSuffix != nil {
		b.WriteU16(*k.ItemSuffix)
	}
}

func readBucketKey(b *wire.Buffer) (BucketKey, error) {
	var k BucketKey
	b.FlushBits() // ResetBitPos: the key starts on a byte (AuctionHousePackets.cpp:40-44)
	k.ItemID = b.ReadBits(20)
	hasPet := b.ReadBit()
	k.ItemLevel = uint16(b.ReadBits(11))
	hasSuffix := b.ReadBit()
	b.FlushBits()
	if hasPet {
		v := b.ReadU16()
		k.BattlePetSpeciesID = &v
	}
	if hasSuffix {
		v := b.ReadU16()
		k.ItemSuffix = &v
	}
	if b.Err() != nil {
		return BucketKey{}, errShort
	}
	return k, nil
}

// Enchant is one ItemEnchantData of an auction (ItemPacketsCommon.cpp:214-221).
type Enchant struct {
	ID         int32
	Expiration uint32
	Charges    int32
	Slot       uint8
}

// Gem is one ItemGemData of an auction (ItemPacketsCommon.cpp:223-228).
type Gem struct {
	Slot uint8
	Item items.ItemInstance
}

// Item is one AuctionItem (AuctionHousePackets.cpp:180-259). An optional field is a pointer; the money fields are
// copper. Bidder and BidAmount are set together by the core (AuctionHouseMgr.cpp:219-223).
type Item struct {
	Item                 *items.ItemInstance
	Count                int32
	Charges              int32
	Flags                int32
	AuctionID            int32
	Owner                wire.GUID
	MinBid               *uint64
	MinIncrement         *uint64
	BuyoutPrice          *uint64
	UnitPrice            *uint64
	DurationLeft         int32
	DeleteReason         uint8
	CensorServerSideInfo bool
	CensorBidInfo        bool
	ItemGUID             wire.GUID
	OwnerAccount         wire.GUID
	EndTime              uint32
	Bidder               *wire.GUID
	BidAmount            *uint64
	Enchants             []Enchant
	Gems                 []Gem
	BucketKey            *BucketKey
	Creator              *wire.GUID
}

// readItem reads one AuctionItem: the bit group (item, enchant and gem counts, the four prices, the two censor bits,
// the key, the creator and, when the bid info is not censored, the bidder and the amount), then the fixed fields and
// the optional values in the writer's order.
func readItem(b *wire.Buffer) (Item, error) {
	var it Item
	hasItem := b.ReadBit()
	enchants := int(b.ReadBits(4))
	gems := int(b.ReadBits(2))
	hasMinBid, hasMinInc, hasBuyout, hasUnit := b.ReadBit(), b.ReadBit(), b.ReadBit(), b.ReadBit()
	it.CensorServerSideInfo = b.ReadBit()
	it.CensorBidInfo = b.ReadBit()
	hasKey := b.ReadBit()
	hasCreator := b.ReadBit()
	var hasBidder, hasBidAmount bool
	if !it.CensorBidInfo {
		hasBidder = b.ReadBit()
		hasBidAmount = b.ReadBit()
	}
	if b.Err() != nil {
		return Item{}, errShort
	}
	if enchants > maxEnchants || gems > maxGems {
		return Item{}, fmt.Errorf("the auction names %d enchants and %d gems", enchants, gems)
	}
	if hasBidder {
		g := wire.ReadPackedGUID(b)
		it.Bidder = &g
	}
	if hasBidAmount {
		v := b.ReadU64()
		it.BidAmount = &v
	}
	b.FlushBits()
	it.Count, it.Charges, it.Flags, it.AuctionID = b.ReadI32(), b.ReadI32(), b.ReadI32(), b.ReadI32()
	it.Owner = wire.ReadPackedGUID(b)
	it.DurationLeft = b.ReadI32()
	it.DeleteReason = b.ReadU8()
	b.ReadU32() // Unused1110
	if b.Err() != nil {
		return Item{}, errShort
	}
	if hasItem {
		inst, err := items.ReadItemInstance(b)
		if err != nil {
			return Item{}, err
		}
		it.Item = &inst
	}
	for range enchants {
		it.Enchants = append(it.Enchants, Enchant{ID: b.ReadI32(), Expiration: b.ReadU32(), Charges: b.ReadI32(), Slot: b.ReadU8()})
	}
	for range gems {
		g := Gem{Slot: b.ReadU8()}
		if b.Err() != nil {
			return Item{}, errShort
		}
		inst, err := items.ReadItemInstance(b)
		if err != nil {
			return Item{}, err
		}
		g.Item = inst
		it.Gems = append(it.Gems, g)
	}
	readOpt := func(has bool) *uint64 {
		if !has {
			return nil
		}
		v := b.ReadU64()
		return &v
	}
	it.MinBid, it.MinIncrement, it.BuyoutPrice, it.UnitPrice = readOpt(hasMinBid), readOpt(hasMinInc), readOpt(hasBuyout), readOpt(hasUnit)
	if !it.CensorServerSideInfo {
		it.ItemGUID = wire.ReadPackedGUID(b)
		it.OwnerAccount = wire.ReadPackedGUID(b)
		it.EndTime = uint32(b.ReadI32())
	}
	if hasKey {
		k, err := readBucketKey(b)
		if err != nil {
			return Item{}, err
		}
		it.BucketKey = &k
	}
	if hasCreator {
		g := wire.ReadPackedGUID(b)
		it.Creator = &g
	}
	if b.Err() != nil {
		return Item{}, errShort
	}
	return it, nil
}

func readItems(b *wire.Buffer, n, max int, what string) ([]Item, error) {
	if n < 0 || n > max {
		return nil, fmt.Errorf("auction: the %s names %d auctions, at most %d", what, n, max)
	}
	out := make([]Item, 0, n)
	for i := range n {
		it, err := readItem(b)
		if err != nil {
			return nil, fmt.Errorf("auction: %s auction %d: %w", what, i, err)
		}
		out = append(out, it)
	}
	return out, nil
}

// ---- the client's packets ----

// BuildHello is CMSG_AUCTION_HELLO_REQUEST (AuctionHelloRequest::Read, AuctionHousePackets.cpp:326-329): the
// auctioneer's packed GUID.
func BuildHello(auctioneer wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	return b.Bytes()
}

// Sort is one AuctionSortDef (AuctionHousePackets.cpp:92-99): the sort order byte and the reverse bit. At most two
// sorts a packet.
type Sort struct {
	Order   uint8
	Reverse bool
}

// writeSorts writes the sort entries (each starts on a byte: ResetBitPos, then the order byte and the reverse bit).
func writeSorts(b *wire.Buffer, sorts []Sort) {
	for _, s := range sorts {
		b.FlushBits()
		b.WriteU8(s.Order)
		b.WriteBit(s.Reverse)
		b.FlushBits()
	}
}

// BuildBrowseQuery is CMSG_AUCTION_BROWSE_QUERY (AuctionBrowseQuery::Read, AuctionHousePackets.cpp:271-306): the
// auctioneer, offset, level range, two unused bytes, the filter mask, the known-pet bytes and the pet level, the
// tainted-by flag, the 8-bit name length, the 3-bit class-filter count and the 2-bit sort count, the name, then the
// class filters and the sorts. This client names no class filter and no known pets.
type BrowseQuery struct {
	Auctioneer wire.GUID
	Offset     uint32
	MinLevel   uint8
	MaxLevel   uint8
	Filters    uint32
	Name       string
	Sorts      []Sort
}

// BuildBrowseQuery writes a BrowseQuery; a name past the 8-bit length field or more than two sorts is an error.
func BuildBrowseQuery(q BrowseQuery) ([]byte, error) {
	if len(q.Name) > 255 {
		return nil, fmt.Errorf("auction: the search name is %d bytes, the field holds 255", len(q.Name))
	}
	if len(q.Sorts) > 2 {
		return nil, fmt.Errorf("auction: %d sorts, a packet holds 2", len(q.Sorts))
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, q.Auctioneer)
	b.WriteU32(q.Offset)
	b.WriteU8(q.MinLevel)
	b.WriteU8(q.MaxLevel)
	b.WriteU8(0) // Unused1007_1
	b.WriteU8(0) // Unused1007_2
	b.WriteU32(q.Filters)
	b.WriteU32(0)     // known pets size
	b.WriteU8(0)      // MaxPetLevel
	b.WriteU32(0)     // Unused1026
	b.WriteBit(false) // TaintedBy
	b.WriteBits(uint64(len(q.Name)), 8)
	b.WriteBits(0, 3) // item class filters
	b.WriteBits(uint64(len(q.Sorts)), 2)
	b.FlushBits()
	b.WriteString(q.Name)
	writeSorts(b, q.Sorts)
	return b.Bytes(), nil
}

// BuildListItemsByBucketKey is CMSG_AUCTION_LIST_ITEMS_BY_BUCKET_KEY (AuctionListItemsByBucketKey::Read,
// AuctionHousePackets.cpp:366-380): the auctioneer, the bucket key, the offset, an unknown byte, the tainted-by flag,
// the 2-bit sort count and the sorts.
func BuildListItemsByBucketKey(auctioneer wire.GUID, key BucketKey, offset uint32, sorts []Sort) ([]byte, error) {
	if len(sorts) > 2 {
		return nil, fmt.Errorf("auction: %d sorts, a packet holds 2", len(sorts))
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	writeBucketKey(b, key)
	b.WriteU32(offset)
	b.WriteU8(0) // Unknown830
	b.WriteBit(false)
	b.WriteBits(uint64(len(sorts)), 2)
	b.FlushBits()
	writeSorts(b, sorts)
	return b.Bytes(), nil
}

// BuildListOwnedItems is CMSG_AUCTION_LIST_OWNED_ITEMS (AuctionListOwnedItems::Read, AuctionHousePackets.cpp:398-410):
// the auctioneer, the offset, the tainted-by flag, the 2-bit sort count and the sorts.
func BuildListOwnedItems(auctioneer wire.GUID, offset uint32, sorts []Sort) ([]byte, error) {
	if len(sorts) > 2 {
		return nil, fmt.Errorf("auction: %d sorts, a packet holds 2", len(sorts))
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	b.WriteU32(offset)
	b.WriteBit(false)
	b.WriteBits(uint64(len(sorts)), 2)
	b.FlushBits()
	writeSorts(b, sorts)
	return b.Bytes(), nil
}

// ForSale is one AuctionItemForSale (AuctionHousePackets.cpp:101-107): the item's packed GUID and the count to use.
type ForSale struct {
	Item  wire.GUID
	Count uint32
}

func writeForSale(b *wire.Buffer, items []ForSale) {
	for _, it := range items {
		wire.WritePackedGUID(b, it.Item)
		b.WriteU32(it.Count)
	}
}

// BuildSellItem is CMSG_AUCTION_SELL_ITEM (AuctionSellItem::Read, AuctionHousePackets.cpp:459-473): the auctioneer, the
// minimum bid, the buyout, the run time in minutes, the tainted-by flag, the 6-bit count and the item (exactly one,
// used once: AuctionHouseHandler.cpp:818-822).
func BuildSellItem(auctioneer, item wire.GUID, minBid, buyout uint64, runTime uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	b.WriteU64(minBid)
	b.WriteU64(buyout)
	b.WriteU32(runTime)
	b.WriteBit(false)
	b.WriteBits(1, 6)
	b.FlushBits()
	writeForSale(b, []ForSale{{Item: item, Count: 1}})
	return b.Bytes()
}

// MaxCommodityStacks is the 6-bit count's limit for one commodity post (Array<AuctionItemForSale, 64>).
const MaxCommodityStacks = 64

// BuildSellCommodity is CMSG_AUCTION_SELL_COMMODITY (AuctionSellCommodity::Read, AuctionHousePackets.cpp:444-457): the
// auctioneer, the price of one unit, the run time, the tainted-by flag, the 6-bit count and the stacks.
func BuildSellCommodity(auctioneer wire.GUID, unitPrice uint64, runTime uint32, stacks []ForSale) ([]byte, error) {
	if len(stacks) == 0 || len(stacks) >= MaxCommodityStacks {
		return nil, fmt.Errorf("auction: %d stacks, a commodity post holds 1 to %d", len(stacks), MaxCommodityStacks-1)
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	b.WriteU64(unitPrice)
	b.WriteU32(runTime)
	b.WriteBit(false)
	b.WriteBits(uint64(len(stacks)), 6)
	b.FlushBits()
	writeForSale(b, stacks)
	return b.Bytes(), nil
}

// BuildPlaceBid is CMSG_AUCTION_PLACE_BID (AuctionPlaceBid::Read, AuctionHousePackets.cpp:412-420): the auctioneer, the
// auction id, the amount and the tainted-by flag.
func BuildPlaceBid(auctioneer wire.GUID, auctionID int32, amount uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	b.WriteI32(auctionID)
	b.WriteU64(amount)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// BuildRemoveItem is CMSG_AUCTION_REMOVE_ITEM (AuctionRemoveItem::Read, AuctionHousePackets.cpp:422-430): the
// auctioneer, the auction id, the item id and the tainted-by flag.
func BuildRemoveItem(auctioneer wire.GUID, auctionID, itemID int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	b.WriteI32(auctionID)
	b.WriteI32(itemID)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// BuildGetCommodityQuote is CMSG_AUCTION_GET_COMMODITY_QUOTE (AuctionGetCommodityQuote::Read,
// AuctionHousePackets.cpp:481-489): the auctioneer, the item id, the quantity and the tainted-by flag.
func BuildGetCommodityQuote(auctioneer wire.GUID, itemID int32, quantity uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, auctioneer)
	b.WriteI32(itemID)
	b.WriteU32(quantity)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// BuildConfirmCommoditiesPurchase is CMSG_AUCTION_CONFIRM_COMMODITIES_PURCHASE (AuctionConfirmCommoditiesPurchase::Read,
// AuctionHousePackets.cpp:316-324): the same four fields as the quote.
func BuildConfirmCommoditiesPurchase(auctioneer wire.GUID, itemID int32, quantity uint32) []byte {
	return BuildGetCommodityQuote(auctioneer, itemID, quantity)
}

// ---- the server's answers ----

// Hello is SMSG_AUCTION_HELLO_RESPONSE (AuctionHelloResponse::Write, AuctionHousePackets.cpp:547-557): the
// auctioneer, the two delivery delays, the auction house id and the open-for-business bit.
type Hello struct {
	Auctioneer            wire.GUID
	PurchaseDeliveryDelay uint32
	CancelDeliveryDelay   uint32
	AuctionHouseID        int32
	OpenForBusiness       bool
}

// ParseHello reads SMSG_AUCTION_HELLO_RESPONSE to its last byte.
func ParseHello(p []byte) (Hello, error) {
	b := wire.NewReader(p)
	h := Hello{Auctioneer: wire.ReadPackedGUID(b), PurchaseDeliveryDelay: b.ReadU32(), CancelDeliveryDelay: b.ReadU32(), AuctionHouseID: b.ReadI32()}
	h.OpenForBusiness = b.ReadBit()
	b.FlushBits()
	if err := exactly(b); err != nil {
		return Hello{}, err
	}
	return h, nil
}

// CommandResult is SMSG_AUCTION_COMMAND_RESULT (AuctionCommandResult::Write, AuctionHousePackets.cpp:501-513; sent by
// WorldSession::SendAuctionCommandResult, AuctionHouseHandler.cpp:1008-1017): the auction, the command, the result,
// the bag result of an inventory error, the bidder, the minimum increment, the money and the delay to the next action.
type CommandResult struct {
	AuctionID    int32
	Command      int32
	ErrorCode    int32
	BagResult    int32
	Bidder       wire.GUID
	MinIncrement uint64
	Money        uint64
	DesiredDelay uint32
}

// OK reports whether the command went through.
func (r CommandResult) OK() bool { return r.ErrorCode == ResultOK }

// Reason names the result: the code's name, and for an inventory error the failure it carries.
func (r CommandResult) Reason() string {
	if r.ErrorCode == ResultInventory {
		return "inventory: " + items.InventoryResultName(r.BagResult)
	}
	return ResultName(r.ErrorCode)
}

// ParseCommandResult reads SMSG_AUCTION_COMMAND_RESULT to its last byte.
func ParseCommandResult(p []byte) (CommandResult, error) {
	b := wire.NewReader(p)
	r := CommandResult{AuctionID: b.ReadI32(), Command: b.ReadI32(), ErrorCode: b.ReadI32(), BagResult: b.ReadI32(),
		Bidder: wire.ReadPackedGUID(b), MinIncrement: b.ReadU64(), Money: b.ReadU64(), DesiredDelay: b.ReadU32()}
	if err := exactly(b); err != nil {
		return CommandResult{}, err
	}
	return r, nil
}

// Bucket is one BucketInfo (AuctionHousePackets.cpp:147-178): the key, the total quantity, the required level, the
// lowest price, the appearance ids, the four optional pet fields (bits), two flags, a flush and the optional values.
type Bucket struct {
	Key                   BucketKey
	TotalQuantity         int32
	RequiredLevel         int32
	MinPrice              uint64
	Appearances           []int32
	MaxBattlePetQuality   *uint8
	MaxBattlePetLevel     *uint8
	BattlePetBreedID      *uint8
	BattlePetLevelMask    *uint32
	ContainsOwnerItem     bool
	ContainsOnlyCollected bool
}

func readBucket(b *wire.Buffer) (Bucket, error) {
	var k Bucket
	var err error
	if k.Key, err = readBucketKey(b); err != nil {
		return Bucket{}, err
	}
	k.TotalQuantity, k.RequiredLevel, k.MinPrice = b.ReadI32(), b.ReadI32(), b.ReadU64()
	n := int(b.ReadU32())
	if b.Err() != nil {
		return Bucket{}, errShort
	}
	if n < 0 || n > maxAppearance {
		return Bucket{}, fmt.Errorf("the bucket names %d appearance ids", n)
	}
	for range n {
		k.Appearances = append(k.Appearances, b.ReadI32())
	}
	hasQuality, hasLevel, hasBreed, hasMask := b.ReadBit(), b.ReadBit(), b.ReadBit(), b.ReadBit()
	k.ContainsOwnerItem, k.ContainsOnlyCollected = b.ReadBit(), b.ReadBit()
	b.FlushBits()
	if hasQuality {
		v := b.ReadU8()
		k.MaxBattlePetQuality = &v
	}
	if hasLevel {
		v := b.ReadU8()
		k.MaxBattlePetLevel = &v
	}
	if hasBreed {
		v := b.ReadU8()
		k.BattlePetBreedID = &v
	}
	if hasMask {
		v := b.ReadU32()
		k.BattlePetLevelMask = &v
	}
	if b.Err() != nil {
		return Bucket{}, errShort
	}
	return k, nil
}

// BucketList is SMSG_AUCTION_LIST_BUCKETS_RESULT (AuctionListBucketsResult::Write, AuctionHousePackets.cpp:573-588).
type BucketList struct {
	Buckets      []Bucket
	DesiredDelay uint32
	Filters      int32
	TotalCount   int32
	BrowseMode   bool
	HasMore      bool
}

// ParseBucketList reads SMSG_AUCTION_LIST_BUCKETS_RESULT to its last byte: a count, the delay, the filter mask and the
// total, the buckets, then the browse-mode and has-more bits.
func ParseBucketList(p []byte) (BucketList, error) {
	b := wire.NewReader(p)
	n := int(b.ReadU32())
	l := BucketList{DesiredDelay: b.ReadU32(), Filters: b.ReadI32(), TotalCount: b.ReadI32()}
	if b.Err() != nil {
		return BucketList{}, errShort
	}
	if n < 0 || n > MaxBucketsPerList {
		return BucketList{}, fmt.Errorf("auction: the bucket list names %d buckets, at most %d", n, MaxBucketsPerList)
	}
	for i := range n {
		k, err := readBucket(b)
		if err != nil {
			return BucketList{}, fmt.Errorf("auction: bucket %d: %w", i, err)
		}
		l.Buckets = append(l.Buckets, k)
	}
	l.BrowseMode, l.HasMore = b.ReadBit(), b.ReadBit()
	b.FlushBits()
	if err := exactly(b); err != nil {
		return BucketList{}, err
	}
	return l, nil
}

// ItemList is SMSG_AUCTION_LIST_ITEMS_RESULT (AuctionListItemsResult::Write, AuctionHousePackets.cpp:590-610).
// ListType is 0 for items and 1 for commodities (AuctionHouseListType).
type ItemList struct {
	Items         []Item
	TotalCount    uint32
	DesiredDelay  uint32
	ListType      uint8
	HasMore       bool
	BucketKey     BucketKey
	TotalQuantity uint32
}

// ParseItemList reads SMSG_AUCTION_LIST_ITEMS_RESULT to its last byte: a count, the total and the delay, the
// auctions, the 2-bit list type and the has-more bit, a flush, the bucket key and the total quantity.
func ParseItemList(p []byte) (ItemList, error) {
	b := wire.NewReader(p)
	n := int(b.ReadU32())
	l := ItemList{TotalCount: b.ReadU32(), DesiredDelay: b.ReadU32()}
	if b.Err() != nil {
		return ItemList{}, errShort
	}
	var err error
	if l.Items, err = readItems(b, n, MaxItemsPerList, "item list"); err != nil {
		return ItemList{}, err
	}
	l.ListType = uint8(b.ReadBits(2))
	l.HasMore = b.ReadBit()
	b.FlushBits()
	if l.BucketKey, err = readBucketKey(b); err != nil {
		return ItemList{}, err
	}
	l.TotalQuantity = b.ReadU32()
	if err := exactly(b); err != nil {
		return ItemList{}, err
	}
	return l, nil
}

// OwnedList is SMSG_AUCTION_LIST_OWNED_ITEMS_RESULT (AuctionListOwnedItemsResult::Write, AuctionHousePackets.cpp:612-628).
type OwnedList struct {
	Items        []Item
	Sold         []Item
	DesiredDelay uint32
	HasMore      bool
}

// ParseOwnedList reads SMSG_AUCTION_LIST_OWNED_ITEMS_RESULT to its last byte: the two counts and the delay, the
// auctions, the sold auctions and the has-more bit.
func ParseOwnedList(p []byte) (OwnedList, error) {
	b := wire.NewReader(p)
	n, sold := int(b.ReadI32()), int(b.ReadI32())
	l := OwnedList{DesiredDelay: b.ReadU32()}
	if b.Err() != nil {
		return OwnedList{}, errShort
	}
	var err error
	if l.Items, err = readItems(b, n, maxOwnedItems, "owned list"); err != nil {
		return OwnedList{}, err
	}
	if l.Sold, err = readItems(b, sold, maxOwnedItems, "sold list"); err != nil {
		return OwnedList{}, err
	}
	l.HasMore = b.ReadBit()
	b.FlushBits()
	if err := exactly(b); err != nil {
		return OwnedList{}, err
	}
	return l, nil
}

// CommodityQuote is SMSG_AUCTION_GET_COMMODITY_QUOTE_RESULT (AuctionGetCommodityQuoteResult::Write,
// AuctionHousePackets.cpp:515-533). The optional fields are all absent when the house cannot fill the quantity.
type CommodityQuote struct {
	TotalPrice    *uint64
	Quantity      *uint32
	QuoteDuration *int64 // milliseconds
	ItemID        int32
	DesiredDelay  uint32
}

// ParseCommodityQuote reads SMSG_AUCTION_GET_COMMODITY_QUOTE_RESULT to its last byte: three presence bits (flushed by
// the item id that follows), the item id and the delay, then the present values (the duration is an int64).
func ParseCommodityQuote(p []byte) (CommodityQuote, error) {
	b := wire.NewReader(p)
	hasPrice, hasQty, hasDur := b.ReadBit(), b.ReadBit(), b.ReadBit()
	q := CommodityQuote{ItemID: b.ReadI32(), DesiredDelay: b.ReadU32()}
	if hasPrice {
		v := b.ReadU64()
		q.TotalPrice = &v
	}
	if hasQty {
		v := b.ReadU32()
		q.Quantity = &v
	}
	if hasDur {
		v := int64(b.ReadU64())
		q.QuoteDuration = &v
	}
	if err := exactly(b); err != nil {
		return CommodityQuote{}, err
	}
	return q, nil
}

// BidderNotification is AuctionBidderNotification (AuctionHousePackets.cpp:261-269): the house, the auction, the bidder
// and the item.
type BidderNotification struct {
	AuctionHouseID int32
	AuctionID      int32
	Bidder         wire.GUID
	Item           items.ItemInstance
}

func readBidderNotification(b *wire.Buffer) (BidderNotification, error) {
	n := BidderNotification{AuctionHouseID: b.ReadI32(), AuctionID: b.ReadI32(), Bidder: wire.ReadPackedGUID(b)}
	if b.Err() != nil {
		return BidderNotification{}, errShort
	}
	var err error
	n.Item, err = items.ReadItemInstance(b)
	return n, err
}

// OwnerNotification is AuctionOwnerNotification (AuctionHousePackets.cpp:138-145): the auction, the bid and the item.
type OwnerNotification struct {
	AuctionID int32
	BidAmount uint64
	Item      items.ItemInstance
}

func readOwnerNotification(b *wire.Buffer) (OwnerNotification, error) {
	n := OwnerNotification{AuctionID: b.ReadI32(), BidAmount: b.ReadU64()}
	if b.Err() != nil {
		return OwnerNotification{}, errShort
	}
	var err error
	n.Item, err = items.ReadItemInstance(b)
	return n, err
}

// ParseWon reads SMSG_AUCTION_WON_NOTIFICATION (AuctionWonNotification::Write, AuctionHousePackets.cpp:663-668).
func ParseWon(p []byte) (BidderNotification, error) {
	b := wire.NewReader(p)
	n, err := readBidderNotification(b)
	if err != nil {
		return BidderNotification{}, err
	}
	return n, exactly(b)
}

// Outbid is SMSG_AUCTION_OUTBID_NOTIFICATION (AuctionOutbidNotification::Write, AuctionHousePackets.cpp:630-637): the
// bidder notification, the bid and the minimum increment.
type Outbid struct {
	Info         BidderNotification
	BidAmount    uint64
	MinIncrement uint64
}

// ParseOutbid reads SMSG_AUCTION_OUTBID_NOTIFICATION to its last byte.
func ParseOutbid(p []byte) (Outbid, error) {
	b := wire.NewReader(p)
	n, err := readBidderNotification(b)
	if err != nil {
		return Outbid{}, err
	}
	o := Outbid{Info: n, BidAmount: b.ReadU64(), MinIncrement: b.ReadU64()}
	return o, exactly(b)
}

// OwnerBid is SMSG_AUCTION_OWNER_BID_NOTIFICATION (AuctionOwnerBidNotification::Write, AuctionHousePackets.cpp:639-646):
// the owner notification, the minimum increment and the bidder.
type OwnerBid struct {
	Info         OwnerNotification
	MinIncrement uint64
	Bidder       wire.GUID
}

// ParseOwnerBid reads SMSG_AUCTION_OWNER_BID_NOTIFICATION to its last byte.
func ParseOwnerBid(p []byte) (OwnerBid, error) {
	b := wire.NewReader(p)
	n, err := readOwnerNotification(b)
	if err != nil {
		return OwnerBid{}, err
	}
	o := OwnerBid{Info: n, MinIncrement: b.ReadU64(), Bidder: wire.ReadPackedGUID(b)}
	return o, exactly(b)
}

// Closed is SMSG_AUCTION_CLOSED_NOTIFICATION (AuctionClosedNotification::Write, AuctionHousePackets.cpp:491-499): the
// owner notification, the proceeds mail delay and the sold bit.
type Closed struct {
	Info              OwnerNotification
	ProceedsMailDelay float32
	Sold              bool
}

// ParseClosed reads SMSG_AUCTION_CLOSED_NOTIFICATION to its last byte.
func ParseClosed(p []byte) (Closed, error) {
	b := wire.NewReader(p)
	n, err := readOwnerNotification(b)
	if err != nil {
		return Closed{}, err
	}
	c := Closed{Info: n, ProceedsMailDelay: b.ReadFloat32()}
	c.Sold = b.ReadBit()
	b.FlushBits()
	return c, exactly(b)
}
