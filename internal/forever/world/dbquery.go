package world

// The session side of CMSG_DB_QUERY_BULK -> SMSG_DB_REPLY (tables): batched
// record fetches with a deadline per batch, backed by the session's bounded
// record cache, and the spell-name lookup the dashboard card reads.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// dbQueryDeadline bounds one wait for a DB reply batch; the server answers
// each record inline (HotfixHandler.cpp:31-70).
const dbQueryDeadline = 3 * time.Second

// dbCache is the session's record cache, made on first use: one entry per
// (table, id), bounded, so the agent never asks twice for the same record.
func (s *session) dbCache() *tables.Cache {
	if s.db == nil {
		s.db = tables.NewCache(tables.DefaultCacheSize)
	}
	return s.db
}

// fetchRecords asks the server for table's ids and returns the records it
// holds, keyed by id: one CMSG_DB_QUERY_BULK batch per BuildQuery packet,
// then one SMSG_DB_REPLY per id, each with its own deadline. Cached ids are
// not asked again; an id the server does not hold is missing from the
// result (a typed result, not an error) but is still cached, so a missing
// id is asked once, not once per fetch. A batch whose reply never comes is
// an error, as is a reply for another table or id.
func (s *session) fetchRecords(table uint32, ids []uint32) (map[uint32][]byte, error) {
	out := map[uint32][]byte{}
	var ask []uint32
	for _, id := range ids {
		if data, found, ok := s.dbCache().Get(table, id); ok {
			if found {
				out[id] = data
			}
			continue
		}
		ask = append(ask, id)
	}
	for _, packet := range tables.BuildQuery(table, ask) {
		if err := s.sendOn(sideInstance, tables.CMSGDBQueryBulk, packet); err != nil {
			return nil, err
		}
		batch := ask
		if len(batch) > tables.MaxRecordsPerQuery {
			batch = batch[:tables.MaxRecordsPerQuery]
		}
		ask = ask[len(batch):]
		for _, id := range batch {
			p, err := s.waitFor("SMSG_DB_REPLY", dbQueryDeadline, func(p worldconn.Packet) bool {
				return p.Core == wire.SMSGDBReply
			})
			if err != nil {
				return nil, err
			}
			r, err := tables.ParseReply(p.Payload)
			if err != nil {
				return nil, err
			}
			if r.TableHash != table || r.RecordID != id {
				return nil, tables.ErrUnexpectedReply(table, id, r.TableHash, r.RecordID)
			}
			s.dbCache().Put(table, id, r.Found(), r.Data)
			if r.Found() {
				out[id] = r.Data
			}
		}
	}
	return out, nil
}

// SpellName asks the server for the spell's name (SpellName records by
// spell id). The second return is false when the server holds no name: the
// caller shows the id as today. An asker without a link (a log built
// outside a session) always reports none.
func (s *session) SpellName(id uint32) (string, bool) {
	if s == nil {
		return "", false
	}
	records, err := s.fetchRecords(tables.TableSpellName, []uint32{id})
	if err != nil {
		return "", false
	}
	data, ok := records[id]
	if !ok {
		return "", false
	}
	name, err := tables.ParseSpellName(data)
	if err != nil || name.Name == "" {
		return "", false
	}
	return name.Name, true
}
