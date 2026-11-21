// Package tables is the client side of CMSG_DB_QUERY_BULK -> SMSG_DB_REPLY
// (HotfixHandler.cpp:31-70, HotfixPackets.h/.cpp): game data the agent asks
// from the server instead of reading client files. The agent needs two
// tables now: SpellName (spell id -> name) and FactionTemplate (friend/enemy
// masks, so "is this mob hostile" becomes a lookup). Every record the Data2
// store writes goes out in DB2LoadInfo field order (DB2Store.cpp
// WriteRecord); the parsers below read exactly that order, cited at each
// table.
package tables

// (no imports: this file is constants only).

// The connection's opcodes (Opcodes.h:315, :1433).
const (
	// CMSGDBQueryBulk asks for records: a table hash, then the record ids.
	CMSGDBQueryBulk uint32 = 0x430010
	// SMSGDBReply answers one record: table hash, record id, timestamp, status, data.
	SMSGDBReply uint32 = 0x490000
)

// Status is DBReply's HotfixRecord::Status (DB2Stores.h:405-414): whether
// the record follows. NotSet, RecordRemoved, Invalid and NotPublic all mean
// no record data; only Valid carries one. A status other than Valid is a
// typed result, never an error.
type Status uint8

const (
	// StatusNotSet is HotfixRecord::NotSet: the server never sends it.
	StatusNotSet Status = 0
	// StatusValid means the data field holds the record.
	StatusValid Status = 1
	// StatusRemoved means the record was removed.
	StatusRemoved Status = 2
	// StatusInvalid is what the handler sends for an entry the store does
	// not hold (HotfixHandler.cpp:54-67), and for every TactKey the server
	// does not publish (:62-67). The brief's "not found" reply.
	StatusInvalid Status = 3
	// StatusNotPublic is the TactKey answer (HotfixHandler.cpp:62-67): the
	// entry exists but the server does not publish it.
	StatusNotPublic Status = 4
)

// Reply is one parsed SMSG_DB_REPLY (HotfixPackets.cpp:57-66): the table
// hash, the record id, the status, and the record bytes when the status is
// Valid. Timestamp is the server's game time, kept for completeness.
type Reply struct {
	TableHash uint32
	RecordID  uint32
	Timestamp uint32
	Status    Status
	Data      []byte
}

// Found reports whether the reply carries a record: a Valid status with a
// non-empty record. An empty record is a typed result, not a record.
func (r Reply) Found() bool {
	return r.Status == StatusValid && len(r.Data) > 0
}

// Table hashes. The hash is the DB2 file header's own TableHash
// (DB2FileLoader.cpp:1740, DB2Stores.cpp:106-649): the handler keys its
// store map on it, so the client must send the same value. SpellName's was
// read from a local SpellName.db2 header; SpellMisc's and FactionTemplate's
// from the hotfix_data rows the core SQL pairs with named table rows
// (sql/custom/hotfixes/2026_10_30_21 column IDs matched to the hashes, and
// the faction_template 3561 row to its hash in 2026_10_01_02).
const (
	// TableSpellName is SpellName.db2 (FileDataId 1990283, LayoutHash 0x782EE721).
	TableSpellName uint32 = 0x46C66698
	// TableSpellMisc is SpellMisc.db2 (FileDataId 1003144, LayoutHash 0x434B3607).
	// It has no decoder: no production path reads school masks (see the
	// notes). The constant stays for the table's wire hash and the tests
	// that need a wrong table.
	TableSpellMisc uint32 = 0xC603EE28
	// TableFactionTemplate is FactionTemplate.db2 (FileDataId 1361579, LayoutHash 0x22B6DC22).
	TableFactionTemplate uint32 = 0x1018ADD5
)

// MaxRecordsPerQuery bounds one CMSG_DB_QUERY_BULK. The handler loops over
// every query in the packet and answers each one inline (HotfixHandler.cpp),
// so the only wire bound is the count field: BitsSize<13>, i.e. 8191. The
// batcher stays far below it; 64 keeps every batch on one small packet while
// the round's own tests pin the split.
const MaxRecordsPerQuery = 64
