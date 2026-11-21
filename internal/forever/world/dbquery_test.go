package world

// The session side of CMSG_DB_QUERY_BULK -> SMSG_DB_REPLY, against the
// handler-boundary fake: the query builds the core's own packet, the fake
// answers with the core's own reply, a missing id is a cached miss, and a
// hostile reply (another table, a short packet) is an error.

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// dbFake is the fake DB server at the sender boundary: it records every
// CMSG_DB_QUERY_BULK; the replies arrive on the session's event queue like
// a live run's (dbAnswer), so the wait path is the real one.
type dbFake struct {
	t     *testing.T
	asked [][]uint32
}

func (f *dbFake) Send(core uint32, payload []byte) error {
	if core != wire.CMSGDBQueryBulk {
		return nil
	}
	b := wire.NewReader(payload)
	table := b.ReadU32()
	n := int(b.ReadBits(13))
	b.FlushBits()
	var ids []uint32
	for range n {
		ids = append(ids, b.ReadU32())
	}
	if err := b.Err(); err != nil {
		f.t.Fatalf("the fake query: %v", err)
	}
	f.asked = append(f.asked, ids)
	_ = table
	return nil
}

func (f *dbFake) WaitUntil(ctx context.Context, until time.Time) error { return nil }
func (f *dbFake) Now() time.Time                                       { return time.Now() }

// dbSession builds a session whose sender is the fake: sends are recorded,
// waits read from the event queue like a live run's.
func dbSession(t *testing.T, fake *dbFake) *session {
	t.Helper()
	s := &session{
		opts:        Options{Logf: func(string, ...any) {}},
		ctx:         context.Background(),
		world:       nil,
		report:      &Report{Counts: map[string]int{}},
		fightSender: fake,
	}
	s.events = make(chan event, 64)
	s.deadChs[sideRealm] = make(chan struct{})
	s.deadChs[sideInstance] = make(chan struct{})
	return s
}

// answer queues one DB reply on the session's event queue.
func dbAnswer(t *testing.T, s *session, table, id uint32, status tables.Status, data []byte) {
	t.Helper()
	b := wire.NewWriter()
	b.WriteU32(table)
	b.WriteU32(id)
	b.WriteU32(7)
	b.WriteBits(uint64(status), 3)
	b.FlushBits()
	b.WriteU32(uint32(len(data)))
	b.WriteBytes(data)
	s.events <- event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGDBReply, Payload: b.Bytes()}}
}

// dbRecord writes one SpellName record the way the server does for IndexField
// -1 tables (DB2Store.cpp:47-48): no id on the wire, just the name.
func dbRecord(t *testing.T, name string) []byte {
	t.Helper()
	b := wire.NewWriter()
	b.WriteBytes([]byte(name))
	b.WriteU8(0)
	return b.Bytes()
}

// TestFetchRecordsAsksOnceAndCaches pins the round's core promise: the first
// fetch asks, the second serves from the cache without sending.
func TestFetchRecordsAsksOnceAndCaches(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	want := dbRecord(t, "Rend")
	go dbAnswer(t, s, tables.TableSpellName, 774, tables.StatusValid, want)
	got, err := s.fetchRecords(tables.TableSpellName, []uint32{774})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got[774]) == 0 {
		t.Fatal("no record for 774")
	}
	name, err := tables.ParseSpellName(got[774], 774)
	if err != nil || name.Name != "Rend" {
		t.Fatalf("record: %+v %v", name, err)
	}
	if len(fake.asked) != 1 {
		t.Fatalf("the first fetch sent %d queries", len(fake.asked))
	}
	// The second fetch asks nothing: the cache serves it.
	got, err = s.fetchRecords(tables.TableSpellName, []uint32{774})
	if err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if len(got[774]) == 0 {
		t.Fatal("the cache lost 774")
	}
	if len(fake.asked) != 1 {
		t.Fatalf("the cached fetch sent %d more queries", len(fake.asked)-1)
	}
	// SpellName resolves through the same cache.
	if n, ok := s.SpellName(774); !ok || n != "Rend" {
		t.Fatalf("SpellName: %q %v", n, ok)
	}
	if len(fake.asked) != 1 {
		t.Fatal("SpellName sent a query for a cached id")
	}
}

// TestFetchRecordsBatchesAndMisses: many ids go out in bounded batches, and
// a not-found id is a cached miss, not an error.
func TestFetchRecordsBatchesAndMisses(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	var ids []uint32
	for i := uint32(1); i <= 2*tables.MaxRecordsPerQuery+3; i++ {
		ids = append(ids, i)
	}
	go func() {
		for _, id := range ids {
			status := tables.StatusValid
			var data []byte
			if id%7 == 0 {
				status = tables.StatusInvalid
			} else {
				data = dbRecord(t, "n")
			}
			dbAnswer(t, s, tables.TableSpellName, id, status, data)
		}
	}()
	got, err := s.fetchRecords(tables.TableSpellName, ids)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fake.asked) != 3 {
		t.Fatalf("%d ids went out in %d batches, want 3", len(ids), len(fake.asked))
	}
	for _, batch := range fake.asked {
		if len(batch) > tables.MaxRecordsPerQuery {
			t.Fatalf("a batch carries %d ids past the bound", len(batch))
		}
	}
	for _, id := range ids {
		_, want := got[id]
		if (id%7 != 0) != want {
			t.Fatalf("id %d present=%v", id, want)
		}
	}
	// The miss is cached: asking again sends nothing.
	go dbAnswer(t, s, tables.TableSpellName, 7, tables.StatusInvalid, nil)
	if _, err := s.fetchRecords(tables.TableSpellName, []uint32{7}); err != nil {
		t.Fatalf("missing fetch: %v", err)
	}
	before := len(fake.asked)
	if _, err := s.fetchRecords(tables.TableSpellName, []uint32{7}); err != nil {
		t.Fatalf("remiss fetch: %v", err)
	}
	if len(fake.asked) != before {
		t.Fatal("a cached miss was asked again")
	}
	if _, ok := s.SpellName(7); ok {
		t.Fatal("SpellName resolved a missing id")
	}
}

// TestFetchRecordsRefusesHostileReplies: a reply for another table or
// record, and a short reply, are errors, never silent merges.
func TestFetchRecordsRefusesHostileReplies(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	go dbAnswer(t, s, tables.TableSpellMisc, 774, tables.StatusValid, dbRecord(t, "x"))
	if _, err := s.fetchRecords(tables.TableSpellName, []uint32{774}); err == nil {
		t.Fatal("a reply for another table was accepted")
	}
	s2 := dbSession(t, fake)
	go func() {
		s2.events <- event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGDBReply, Payload: []byte{1, 2, 3}}}
	}()
	if _, err := s2.fetchRecords(tables.TableSpellName, []uint32{774}); err == nil {
		t.Fatal("a short reply was accepted")
	}
}
