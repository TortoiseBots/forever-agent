package world

// The session side of CMSG_DB_QUERY_BULK -> SMSG_DB_REPLY, against the
// handler-boundary fake: the query builds the core's own packet, the fake
// answers with the core's own reply, a missing id is a cached miss, and a
// hostile reply (another table, a short packet) is an error.

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
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

// TestFetchRecordsBatchDeadline is one deadline for the batch, not per
// reply: five ids queue five waits, but the one deadline bounds them all.
// (The old code armed a fresh 3 s wait per id, so a silent server held one
// batch for ~15 s; here a silent batch still reports after ~3 s.)
func TestFetchRecordsBatchDeadline(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	start := time.Now()
	if _, err := s.fetchRecords(tables.TableSpellName, []uint32{1, 2, 3, 4, 5}); err == nil {
		t.Fatal("a silent batch answered")
	}
	if took := time.Since(start); took > 2*dbQueryDeadline {
		t.Fatalf("a silent batch took %s, past twice the one deadline", took)
	}
	// An answered batch costs no deadline at all.
	s2 := dbSession(t, fake)
	go func() {
		for i := uint32(1); i <= 5; i++ {
			dbAnswer(t, s2, tables.TableSpellName, i, tables.StatusValid, dbRecord(t, "n"))
		}
	}()
	got, err := s2.fetchRecords(tables.TableSpellName, []uint32{1, 2, 3, 4, 5})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d records, want 5", len(got))
	}
}

// TestFetchRecordsDedupesIds asks a repeated id once: the reply answers
// both copies, and a second fetch sends nothing.
func TestFetchRecordsDedupesIds(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	go dbAnswer(t, s, tables.TableSpellName, 774, tables.StatusValid, dbRecord(t, "Rend"))
	got, err := s.fetchRecords(tables.TableSpellName, []uint32{774, 774, 774})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got[774]) == 0 {
		t.Fatal("no record for 774")
	}
	if len(fake.asked) != 1 || len(fake.asked[0]) != 1 {
		t.Fatalf("asked %v, want one batch with one id", fake.asked)
	}
}

// TestServeEventsPageFillsSpellNames is decision 1 through the real session
// path: combat-log rows go in unnamed, the DB answers name two of the ids,
// and the page carries the server's names (the zero id stays unnamed; its
// spell_id is the fallback). The repeated id costs one batched query.
func TestServeEventsPageFillsSpellNames(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	s.world = objects.NewWorld(0)
	for _, id := range []int32{774, 133, 774, 0} {
		s.CombatLog().Append(CombatRow{Kind: "spell_go", SpellID: id})
	}
	go func() {
		dbAnswer(t, s, tables.TableSpellName, 774, tables.StatusValid, dbRecord(t, "Rend"))
		dbAnswer(t, s, tables.TableSpellName, 133, tables.StatusValid, dbRecord(t, "Fireball"))
	}()
	page, _ := s.serveEventsPage(0, CombatPageMax)
	if len(page.Events) != 4 {
		t.Fatalf("page holds %d rows, want 4", len(page.Events))
	}
	if page.Events[0].SpellName != "Rend" || page.Events[1].SpellName != "Fireball" || page.Events[2].SpellName != "Rend" {
		t.Fatalf("spell names %+q", []string{page.Events[0].SpellName, page.Events[1].SpellName, page.Events[2].SpellName})
	}
	if page.Events[3].SpellName != "" {
		t.Fatalf("zero spell id named %q", page.Events[3].SpellName)
	}
	if len(fake.asked) != 1 {
		t.Fatalf("the page sent %d queries, want one batch", len(fake.asked))
	}
}

// TestServeUnitsHostileFilter is decisions 6 and 8 through the real session
// path: the character carries the Alliance-player template, one mob the
// Horde template and one the Alliance template; the hostile filter keeps
// only the Horde mob, and asking friendly keeps only the Alliance one. The
// three templates cost one batched fetch.
func TestServeUnitsHostileFilter(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	s.world = objects.NewWorld(0)
	self := wire.GUID{Low: 0x1000}
	s.self, s.selfSeen = self, true
	putUnit := func(g wire.GUID, templateID int32) {
		t.Helper()
		info := objects.Info{Type: objects.TypeUnit, HasUnit: true, FactionTemplate: templateID}
		info.Complete()
		s.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{GUID: g, ObjectType: objects.TypeUnit, Info: info, ValuesComplete: true}}}, time.Now())
	}
	putUnit(self, 3)
	mobHostile := wire.GUID{Low: 0x2000}
	mobFriendly := wire.GUID{Low: 0x3000}
	putUnit(mobHostile, 29)
	putUnit(mobFriendly, 12)
	go func() {
		dbAnswer(t, s, tables.TableFactionTemplate, 3, tables.StatusValid, hostileRecord(1|2, 2, 0))
		dbAnswer(t, s, tables.TableFactionTemplate, 29, tables.StatusValid, hostileRecord(1|4, 0, 2))
		dbAnswer(t, s, tables.TableFactionTemplate, 12, tables.StatusValid, hostileRecord(2, 0, 4))
	}()
	hostile := true
	units, err := s.serveUnits(serveUnitsParams{Hostile: &hostile})
	if err != nil {
		t.Fatalf("hostile units: %v", err)
	}
	if len(units) != 1 || wire.GUID(units[0].GUID) != mobHostile {
		t.Fatalf("hostile units: %+v", units)
	}
	friendly := false
	units, err = s.serveUnits(serveUnitsParams{Hostile: &friendly})
	if err != nil {
		t.Fatalf("friendly units: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("friendly units: %+v", units)
	}
	byGUID := map[wire.GUID]bool{}
	for _, u := range units {
		byGUID[wire.GUID(u.GUID)] = true
	}
	if !byGUID[self] || !byGUID[mobFriendly] {
		t.Fatalf("friendly units: %+v", units)
	}
}

// hostileRecord writes one FactionTemplate record the way the server does:
// its group bytes with placeholder faction and lists.
func hostileRecord(factionGroup, friendGroup, enemyGroup uint8) []byte {
	tmpl := tables.FactionTemplate{Faction: 1, FactionGroup: factionGroup, FriendGroup: friendGroup, EnemyGroup: enemyGroup}
	b := wire.NewWriter()
	b.WriteU16(tmpl.Faction)
	b.WriteI32(0)
	b.WriteU8(tmpl.FactionGroup)
	b.WriteU8(tmpl.FriendGroup)
	b.WriteU8(tmpl.EnemyGroup)
	for range 8 {
		b.WriteU16(0)
	}
	for range 8 {
		b.WriteU16(0)
	}
	return b.Bytes()
}
