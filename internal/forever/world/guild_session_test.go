package world

// The guild invite flow (#113): the three hands over a hand-built session — a fake transport records what went out,
// the test feeds the answers the core would send through the session's own dispatch. The join event and the
// character's UNIT_FIELD_GUILD_GUID are the two readbacks; status.guild comes from the unit field, never from the
// held invite.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/guild"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

type guildSent struct {
	core    uint32
	payload []byte
}

// guildFake is the session's transport: it records each packet and lets the test answer it. The hands run on their
// own goroutine, so the record is behind a mutex.
type guildFake struct {
	s      *session
	mu     sync.Mutex
	sent   []guildSent
	onSend func(core uint32, payload []byte)
}

func (f *guildFake) Send(core uint32, payload []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, guildSent{core: core, payload: append([]byte(nil), payload...)})
	f.mu.Unlock()
	if f.onSend != nil {
		f.onSend(core, payload)
	}
	return nil
}

// count is how many packets the transport has written.
func (f *guildFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// sentAt returns one recorded packet.
func (f *guildFake) sentAt(i int) guildSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[i]
}

func (f *guildFake) WaitUntil(ctx context.Context, until time.Time) error { return nil }

func (f *guildFake) Now() time.Time { return time.Now() }

// push hands one server packet to the session the way a socket reader would.
func (f *guildFake) push(core uint32, payload []byte) {
	f.s.events <- event{p: worldconn.Packet{Core: core, Payload: payload}}
}

// newGuildWorld builds the hand-made session: one character of its own in the world model, no guild in its fields
// yet, the fake transport and a packet channel for the answers.
func newGuildWorld(t *testing.T) (*session, *guildFake, wire.GUID) {
	t.Helper()
	self := wire.GUID{Low: 3, High: 0x0800000000000000}
	w := objects.NewWorld(0)
	payload := fakeUnitCreateBlock(fakePackedGUID(self), 0, 1000, 2000, 50, 7, 1, 100, 100, 60, 0)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the self create: %v", err)
	}
	w.Apply(u, time.Now())
	s := &session{
		ctx:      context.Background(),
		world:    w,
		report:   &Report{Counts: map[string]int{}, Character: Character{Name: "Pppppppp"}},
		opts:     Options{Logf: func(string, ...any) {}},
		self:     self,
		selfSeen: true,
	}
	s.events = make(chan event, 8)
	f := &guildFake{s: s}
	s.fightSender = f
	return s, f, self
}

// setSelfGuild writes the character's own UNIT_FIELD_GUILD_GUID (retail bit 126) through the world model.
func setSelfGuild(t *testing.T, s *session, self, guildGUID wire.GUID) {
	t.Helper()
	payload := buildUnitValuesUpdate(self, unitValuesBody(126, func(v *wire.Buffer) {
		wire.WritePackedGUID(v, guildGUID)
	}))
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the guild value update: %v", err)
	}
	s.world.Apply(u, time.Now())
}

// buildGuildInvitePacket is SMSG_GUILD_INVITE for the session tests: the golden fixture's layout with the names the
// test passes.
func buildGuildInvitePacket(guildGUID wire.GUID, guildName, inviter string) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(inviter)), 6)
	b.WriteBits(0, 9) // surname
	b.WriteBits(uint64(len(guildName)), 7)
	b.WriteBits(0, 7) // old guild
	b.FlushBits()
	b.WriteU32(4242) // inviter realm
	b.WriteU32(4242) // guild realm
	wire.WritePackedGUID(b, guildGUID)
	b.WriteU32(0)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	for e := uint32(1); e <= guild.EmblemFields; e++ {
		b.WriteU32(e)
	}
	b.WriteI32(0)
	b.WriteString(inviter)
	b.WriteString(guildName)
	return b.Bytes()
}

// buildGuildJoinedPacket is SMSG_GUILD_EVENT_PLAYER_JOINED for the session tests.
func buildGuildJoinedPacket(g wire.GUID, name string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteU32(4242)
	b.WriteBits(uint64(len(name)), 6)
	b.WriteBits(0, 9)
	b.FlushBits()
	b.WriteString(name)
	return b.Bytes()
}

// buildGuildResultPacket is SMSG_GUILD_COMMAND_RESULT for the session tests.
func buildGuildResultPacket(result, command uint8, name string) []byte {
	b := wire.NewWriter()
	b.WriteU8(result)
	b.WriteU8(command)
	b.WriteBits(uint64(len(name)), 8)
	b.FlushBits()
	b.WriteString(name)
	return b.Bytes()
}

// waitForSend waits until the fake has recorded n sends: the hands run on the test's goroutine only through this
// helper, so nothing sleeps for a fixed time.
func waitForSend(t *testing.T, f *guildFake, n int, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for f.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d send(s) after 2s", f.count())
		}
		select {
		case err := <-done:
			t.Fatalf("the call ended before its %d send(s): %v", n, err)
		case <-time.After(time.Millisecond):
		}
	}
}

// TestStatusGuildField pins status.guild to the character's own unit field and status.guild_name to the accepted
// invite: a status that reported the invite's guild as the character's guild fails here.
func TestStatusGuildField(t *testing.T) {
	s, _, self := newGuildWorld(t)
	// No guild yet: the field is omitted from the answer.
	raw, err := json.Marshal(s.serveStatus(&serveState{}))
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st["guild"]; ok {
		t.Fatalf("a guildless status carries guild: %s", raw)
	}
	if _, ok := st["guild_name"]; ok {
		t.Fatalf("a guildless status carries guild_name: %s", raw)
	}

	// The unit field names one guild and the accepted invite another: the answer must name the field's guild.
	fieldGuild := wire.GUID{Low: 2}
	inviteGuild := wire.GUID{Low: 0xAB}
	setSelfGuild(t, s, self, fieldGuild)
	s.guildInit().accepted = &guild.Invite{Guild: inviteGuild, GuildName: "GGGGGGGGG"}

	status := s.serveStatus(&serveState{})
	if wire.GUID(status.Guild) != fieldGuild {
		t.Fatalf("status.guild %s, want the unit field's %s (not the invite's %s)", wire.GUID(status.Guild), fieldGuild, inviteGuild)
	}
	if status.GuildName != "GGGGGGGGG" {
		t.Fatalf("status.guild_name %q, want the accepted invite's guild name", status.GuildName)
	}
	raw, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"guild":"`) || !strings.Contains(string(raw), `"guild_name":"GGGGGGGGG"`) {
		t.Fatalf("the status answer is %s", raw)
	}
}

// TestServeAcceptGuildInviteNoInvite: an accept with no held invite is refused by name and nothing goes out.
func TestServeAcceptGuildInviteNoInvite(t *testing.T) {
	s, f, _ := newGuildWorld(t)
	err := s.serveAcceptGuildInvite(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no pending guild invite") {
		t.Fatalf("accept with no invite: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("a refused accept sent %d packet(s)", n)
	}
}

// TestServeAcceptGuildInviteWaitsForJoin: the accept carries the invite's guild GUID, reports nothing before the
// join arrives, then succeeds; the invite is consumed by the accept, so a second accept has nothing to accept.
func TestServeAcceptGuildInviteWaitsForJoin(t *testing.T) {
	s, f, self := newGuildWorld(t)
	guildGUID := wire.GUID{Low: 2}
	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGGuildInvite, Payload: buildGuildInvitePacket(guildGUID, "GGGGGGGGG", "Pppppppp")}); err != nil {
		t.Fatalf("the invite: %v", err)
	}
	rows := s.EventLog().Read(0, 0).Rows
	if len(rows) != 1 || rows[0].Kind != events.KindGuild || rows[0].Text != "invite" || rows[0].Data["guild_name"] != "GGGGGGGGG" {
		t.Fatalf("the invite row is %+v", rows)
	}

	done := make(chan error, 1)
	go func() { done <- s.serveAcceptGuildInvite(context.Background()) }()
	waitForSend(t, f, 1, done)
	accept := f.sentAt(0)
	if accept.core != wire.CMSGGuildAcceptInvite {
		t.Fatalf("the accept sent %s", wire.ClientName(accept.core))
	}
	r := wire.NewReader(accept.payload)
	if got := wire.ReadPackedGUID(r); got != guildGUID || r.Remaining() != 0 {
		t.Fatalf("the accept names guild %s (%d byte(s) left)", got, r.Remaining())
	}
	select {
	case err := <-done:
		t.Fatalf("the accept answered %v before the join", err)
	case <-time.After(20 * time.Millisecond):
	}

	f.push(wire.SMSGGuildEventPlayerJoined, buildGuildJoinedPacket(self, "Pppppppp"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the accept: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the accept did not answer after the join")
	}
	if s.guild.accepted == nil || s.guild.accepted.GuildName != "GGGGGGGGG" {
		t.Fatalf("the accepted invite is %+v", s.guild.accepted)
	}
	if err := s.serveAcceptGuildInvite(context.Background()); err == nil || !strings.Contains(err.Error(), "no pending guild invite") {
		t.Fatalf("a second accept: %v (the invite was not consumed)", err)
	}
}

// TestServeAcceptGuildInviteTimesOut: without a join the accept fails, naming the last guild command result the way
// the core answers a refused guild command.
func TestServeAcceptGuildInviteTimesOut(t *testing.T) {
	s, f, _ := newGuildWorld(t)
	old := guildConfirmWait
	guildConfirmWait = 60 * time.Millisecond
	t.Cleanup(func() { guildConfirmWait = old })
	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGGuildInvite, Payload: buildGuildInvitePacket(wire.GUID{Low: 2}, "GGGGGGGGG", "Pppppppp")}); err != nil {
		t.Fatalf("the invite: %v", err)
	}
	// The server's refusal arrives with the accept: it changes nothing, it is only named.
	f.onSend = func(core uint32, _ []byte) {
		if core != wire.CMSGGuildAcceptInvite {
			return
		}
		f.onSend = nil
		f.push(wire.SMSGGuildCommandResult, buildGuildResultPacket(3, 2, "Pppppppp"))
	}
	err := s.serveAcceptGuildInvite(context.Background())
	if err == nil || !strings.Contains(err.Error(), "result 3 for command 2") {
		t.Fatalf("the accept without a join: %v", err)
	}
}

// TestServeGuildLeaveNotInGuild: a leave with no guild is refused by name and nothing goes out; with a guild it
// sends the empty packet and answers once the guild field turns zero.
func TestServeGuildLeaveNotInGuild(t *testing.T) {
	s, f, self := newGuildWorld(t)
	err := s.serveGuildLeave(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not in a guild") {
		t.Fatalf("leave with no guild: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("a refused leave sent %d packet(s)", n)
	}

	setSelfGuild(t, s, self, wire.GUID{Low: 2})
	s.guildInit().accepted = &guild.Invite{GuildName: "GGGGGGGGG"}
	done := make(chan error, 1)
	go func() { done <- s.serveGuildLeave(context.Background()) }()
	waitForSend(t, f, 1, done)
	leave := f.sentAt(0)
	if leave.core != wire.CMSGGuildLeave || len(leave.payload) != 0 {
		t.Fatalf("the leave sent %s with %d byte(s)", wire.ClientName(leave.core), len(leave.payload))
	}
	f.push(wire.SMSGUpdateObject, buildUnitValuesUpdate(self, unitValuesBody(126, func(v *wire.Buffer) {
		wire.WritePackedGUID(v, wire.EmptyGUID)
	})))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the leave: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the leave did not answer after the guild field cleared")
	}
	if s.guild.accepted != nil {
		t.Fatalf("the accepted invite survived the leave: %+v", s.guild.accepted)
	}
}
