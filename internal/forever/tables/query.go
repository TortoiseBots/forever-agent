package tables

// The query and the reply wire form. The query's reader is the core's own
// (HotfixPackets.cpp:66-74): the table hash, then a BitsSize<13> record
// count (flushed), then the uint32 ids. The reply's writer is the core's own
// (:57-66): table hash, record id, timestamp, a 3-bit status, a uint32 data
// size, then the record bytes WriteRecord (DB2Store.cpp:42-97) wrote in
// DB2LoadInfo field order: fixed-size fields first in declaration order,
// then each localized string as a C string in the session locale (enUS
// here), then the optional-data pairs the hotfix rows add
// (HotfixHandler.cpp:52-58).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// maxRecordBytes bounds one reply's record: a count is server data, so the
// parser refuses the packet when it exceeds the bound instead of allocating
// from it. The three tables this round reads are tens of bytes; 1 MiB is far
// above that and still a hard stop.
const maxRecordBytes = 1 << 20

// (BuildQuery documents the 13-bit query count at its own site.)

// BuildQuery is CMSG_DB_QUERY_BULK for one batch: the table hash, a 13-bit
// record count (flushed), then the uint32 ids (HotfixPackets.cpp:66-74). A
// longer list is split into several packets of at most MaxRecordsPerQuery
// entries.
func BuildQuery(table uint32, ids []uint32) [][]byte {
	var out [][]byte
	for len(ids) > 0 {
		n := len(ids)
		if n > MaxRecordsPerQuery {
			n = MaxRecordsPerQuery
		}
		b := wire.NewWriter()
		b.WriteU32(table)
		b.WriteBits(uint64(n), 13)
		b.FlushBits()
		for _, id := range ids[:n] {
			b.WriteU32(id)
		}
		out = append(out, b.Bytes())
		ids = ids[n:]
	}
	return out
}

// ParseReply parses one SMSG_DB_REPLY payload (HotfixPackets.cpp:57-66). A
// short or hostile packet is an error; a reply whose status is not Valid, or
// whose record is empty, is a typed result with Found() false, never an
// error.
func ParseReply(payload []byte) (Reply, error) {
	b := wire.NewReader(payload)
	var r Reply
	r.TableHash = b.ReadU32()
	r.RecordID = b.ReadU32()
	r.Timestamp = b.ReadU32()
	r.Status = Status(b.ReadBits(3))
	b.FlushBits()
	size := int(b.ReadU32())
	if size < 0 || size > maxRecordBytes {
		return Reply{}, fmt.Errorf("tables: reply carries %d record bytes", size)
	}
	r.Data = b.ReadBytes(size)
	if err := b.Err(); err != nil {
		return Reply{}, err
	}
	if b.Remaining() != 0 {
		return Reply{}, fmt.Errorf("tables: reply has %d bytes left", b.Remaining())
	}
	if r.Status > StatusNotPublic {
		return Reply{}, fmt.Errorf("tables: reply status %d is not a HotfixRecord status", uint8(r.Status))
	}
	return r, nil
}
