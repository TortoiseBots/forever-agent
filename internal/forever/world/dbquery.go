package world

// The session side of CMSG_DB_QUERY_BULK -> SMSG_DB_REPLY (tables): batched
// record fetches with one deadline per batch, backed by the session's bounded
// record cache, the spell-name lookup the events page reads, and the own
// faction template the hostile filter reads.

import (
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// dbQueryDeadline bounds one DB reply batch; the server answers each record
// inline (HotfixHandler.cpp:31-70). It is one deadline for the batch, not
// per reply, so a silent server ends the batch after it instead of
// stretching the wait per id.
const dbQueryDeadline = 3 * time.Second

// dbCache is the session's record cache, made on first use: one entry per
// (table, id), bounded, so the agent never asks twice for the same record
// within a verdict's own life.
func (s *session) dbCache() *tables.Cache {
	if s.db == nil {
		s.db = tables.NewCache(tables.DefaultCacheSize)
	}
	return s.db
}

// fetchRecords asks the server for table's ids and returns the records it
// holds, keyed by id: one CMSG_DB_QUERY_BULK batch per BuildQuery packet,
// then one SMSG_DB_REPLY per id inside one deadline for the batch. A
// repeated id is asked once and answers every copy. Cached ids are not
// asked again; an id the server does not hold is missing from the result (a
// typed result, not an error) but is still cached for a while, so a missing
// id is asked once per while, not once per fetch. An id left unanswered
// when the batch times out is cached the same way, so a silent server does
// not cost a full deadline per later lookup either; the fetch itself still
// reports the timeout. A reply for another table or id, and a short or
// hostile reply, is an error.
func (s *session) fetchRecords(table uint32, ids []uint32) (map[uint32][]byte, error) {
	out := map[uint32][]byte{}
	var ask []uint32
	seen := map[uint32]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
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
		deadline := time.Now().Add(dbQueryDeadline)
		answered := 0
		for _, id := range batch {
			p, err := s.waitForUntil("SMSG_DB_REPLY", deadline, func(p worldconn.Packet) bool {
				return p.Core == wire.SMSGDBReply
			})
			if err != nil {
				// A batch that timed out is not retried id by id on the
				// next fetch: the unanswered ids are cached as misses for
				// a while. Any other failure (a dead link, a closed
				// session) leaves nothing cached, so the next fetch asks
				// again instead of hiding the outage.
				if errors.Is(err, errWaitTimeout) {
					for _, missed := range batch[answered:] {
						s.dbCache().Put(table, missed, false, nil)
					}
				}
				return nil, err
			}
			answered++
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
// spell id). The second return is false when the server holds no name or
// the fetch fails: the caller shows the id as today.
func (s *session) SpellName(id uint32) (string, bool) {
	names := s.spellNamesFor([]int32{int32(id)})
	name, ok := names[id]
	return name, ok && name != ""
}

// spellNamesFor resolves the server's names for combat-log spell ids in one
// batched fetch: the ids the log rows name, asked once each. An id the
// server does not hold, or a fetch that fails, is absent from the result;
// the caller keeps the id as today. It runs on the session goroutine, like
// every fetch.
func (s *session) spellNamesFor(ids []int32) map[uint32]string {
	var ask []uint32
	seen := map[uint32]bool{}
	for _, raw := range ids {
		if raw <= 0 {
			continue
		}
		id := uint32(raw)
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, found, ok := s.dbCache().Get(tables.TableSpellName, id); ok && found {
			continue
		}
		ask = append(ask, id)
	}
	// fetchRecords skips cached ids itself, but asking only the unknown ones
	// keeps the batch (and its deadline) proportional to what is missing.
	records, err := s.fetchRecords(tables.TableSpellName, ask)
	if err != nil {
		records = map[uint32][]byte{}
	}
	out := map[uint32]string{}
	for id := range seen {
		data, ok := records[id]
		if !ok {
			if data, found, ok := s.dbCache().Get(tables.TableSpellName, id); ok && found {
				records[id] = data
			} else {
				continue
			}
		}
		data = records[id]
		if name, err := tables.ParseSpellName(data, id); err == nil && name.Name != "" {
			out[id] = name.Name
		}
	}
	return out
}

// ownFactionTemplate fetches the character's own faction template once: the
// model's FactionTemplate id for the character's object, parsed from the
// server's record. The cache makes the second call free; without a known
// character object there is no template to fetch.
func (s *session) ownFactionTemplate() (tables.FactionTemplate, error) {
	self := s.selfOrMover()
	o, ok := s.world.Object(self)
	if !ok {
		return tables.FactionTemplate{}, fmt.Errorf("world: no character object for the faction lookup")
	}
	id := o.Info.FactionTemplate
	if id <= 0 {
		return tables.FactionTemplate{}, fmt.Errorf("world: the character names no faction template")
	}
	tmpl, err := s.factionTemplate(uint32(id))
	if err != nil {
		return tables.FactionTemplate{}, err
	}
	return tmpl, nil
}

// factionTemplate parses one cached or fetched FactionTemplate record.
func (s *session) factionTemplate(id uint32) (tables.FactionTemplate, error) {
	records, err := s.fetchRecords(tables.TableFactionTemplate, []uint32{id})
	if err != nil {
		return tables.FactionTemplate{}, err
	}
	data, ok := records[id]
	if !ok {
		return tables.FactionTemplate{}, fmt.Errorf("world: the server holds no faction template %d", id)
	}
	return tables.ParseFactionTemplate(data, id)
}

// hostileToSelf reports whether the mob with the model's faction template
// id is hostile to the character: the mob's template against the
// character's own, the core's own Unit::IsHostileTo direction. A template
// the server does not hold is an error, never a guess.
func (s *session) hostileToSelf(templateID int32) (bool, error) {
	own, err := s.ownFactionTemplate()
	if err != nil {
		return false, err
	}
	mob, err := s.factionTemplate(uint32(max(templateID, 0)))
	if err != nil {
		return false, err
	}
	return mob.IsHostileTo(own), nil
}
