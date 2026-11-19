package world

// Tests for the link's death and for the cast generations: a socket that closes (or goes silent) must end every wait
// at once, and a cast packet that answers a given-up generation must never complete the next cast of that spell.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// deadSession is a fight world with the two socket-death channels the session's waits select on.
func deadSession(t *testing.T, targetDistance float32) (*fightWorld, *session) {
	t.Helper()
	f := newFightWorld(t, targetDistance)
	s := f.session()
	s.deadChs[sideRealm] = make(chan struct{})
	s.deadChs[sideInstance] = make(chan struct{})
	s.events = make(chan event, 32)
	s.opts.FightFor = 60 * time.Second
	s.opts.Stay = time.Second
	return f, s
}

// TestLinkDeathEndsEveryWait: one socket closing ends Attack, Cast, MoveTo, WaitUntil, the rest and the stay loop
// with the same named error, at once, and no further packet is sent.
func TestLinkDeathEndsEveryWait(t *testing.T) {
	f, s := deadSession(t, 3)
	s.setLinkDead(sideInstance, io.EOF)
	if !s.report.LinkLost || s.report.LinkLostSide != "instance" {
		t.Fatalf("the report does not name the lost socket: %+v", s.report)
	}
	sent := len(f.sent)

	if err := s.WaitUntil(context.Background(), time.Now().Add(time.Hour)); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("WaitUntil returned %v", err)
	}
	if err := s.mover.MoveTo(context.Background(), 1010, 2000, 50); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("MoveTo returned %v", err)
	}
	if err := s.mover.ChaseTo(context.Background(), objects.ChaseGoal{
		Dest:    func(time.Time) (float32, float32, float32) { return 1010, 2000, 50 },
		Arrived: func(time.Time) bool { return false },
	}); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("ChaseTo returned %v", err)
	}
	if _, err := s.Cast(context.Background(), 772, f.target, RangeWindow{0, 5}); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("Cast returned %v", err)
	}
	if err := s.Attack(context.Background(), f.target); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("Attack returned %v", err)
	}
	if err := s.stay(context.Background()); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("stay returned %v", err)
	}
	if err := s.Send(wire.CMSGPing, make([]byte, 8)); err == nil || !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Send returned %v", err)
	}
	if len(f.sent) != sent {
		t.Fatalf("a dead link still sent %d packet(s)", len(f.sent)-sent)
	}
}

// TestLinkSilenceIsDeath: a socket that stays open but delivers nothing for linkSilenceLimit is the same loss: the
// live 2026-11-18 run waited out the whole 60 s fight deadline on it.
func TestLinkSilenceIsDeath(t *testing.T) {
	f, s := deadSession(t, 6)
	f.now = f.now.Add(linkSilenceLimit + time.Second)
	s.deadMu.Lock()
	s.lastPacketAt = time.Now().Add(-(linkSilenceLimit + time.Second))
	s.deadMu.Unlock()
	if err := s.WaitUntil(context.Background(), time.Now().Add(time.Hour)); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("WaitUntil returned %v", err)
	}
	if s.report.LinkLostDetail == "" || !strings.Contains(s.report.LinkLostDetail, "no server packet") {
		t.Fatalf("the report's detail is %q", s.report.LinkLostDetail)
	}
}

// TestAbortLogoutReportsLostLink: the failed run still tries the logout when the link is alive, and says
// "logout not possible: connection lost" when it is not.
func TestAbortLogoutReportsLostLink(t *testing.T) {
	f, s := deadSession(t, 6)
	var out []string
	s.opts.Logf = func(format string, args ...any) {
		out = append(out, strings.TrimSpace(strings.Join([]string{format}, " ")))
	}
	s.setLinkDead(sideRealm, io.ErrUnexpectedEOF)
	s.abortLogin()
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "logout not possible: connection lost") {
		t.Fatalf("the abort did not report the lost link: %q", joined)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a dead link still sent %d packet(s)", len(f.sent))
	}
}

// TestStaleCastDoesNotCompleteTheNextCast: Cast A (spell 772) times out; its late START, GO and CAST_FAILED arrive
// while the spell is quarantined, and Cast B of the same spell then starts and must be completed by its own answers.
func TestStaleCastDoesNotCompleteTheNextCast(t *testing.T) {
	f, s := deadSession(t, 3)
	f.castSpell = 772
	ctx := context.Background()

	// A: the server starts the cast and never answers it.
	f.castNoAnswer = true
	if _, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5}); err == nil {
		t.Fatal("cast A did not time out")
	}
	started := false
	for _, e := range f.fight.events {
		if strings.Contains(e.Detail, "started") {
			started = true
		}
	}
	if !started {
		t.Fatalf("A's start was not seen: %+v", f.fight.events)
	}
	aStartID := combat.CastGUID(0, 772, 901)
	// A's three answers, still inside the quarantine window.
	f.server(wire.SMSGSpellStart, buildSpellStart(f.self, f.self, aStartID, 772, 0, f.castTarget()))
	f.server(wire.SMSGSpellGo, buildSpellGo(f.self, f.self, aStartID, 772, 0, f.castTarget()))
	f.server(wire.SMSGCastFailed, buildCastFailed(aStartID, 772, 17))
	if s.fight.staleDrops != 3 {
		t.Fatalf("%d stale cast packet(s) were counted, want 3 (events: %+v)", s.fight.staleDrops, s.fight.events)
	}
	if s.fight.castOpen || s.fight.castStarted {
		t.Fatal("a stale packet reopened a cast")
	}

	// B: the same spell, and its own start and go are the answer.
	f.castNoAnswer = false
	before := s.fight.castSeq
	result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err != nil || result != "success" {
		t.Fatalf("cast B: %q, %v (stale drops %d)", result, err, s.fight.staleDrops)
	}
	if s.fight.castSeq != before+1 {
		t.Fatalf("B's result was not the one that closed the cast (%d -> %d)", before, s.fight.castSeq)
	}
	if f.castCalls != 2 {
		t.Fatalf("%d CMSG_CAST_SPELL for two casts", f.castCalls)
	}
}

// TestDeathDuringSettleEndsAtOnce: a death that arrives while the client settles ends the settle with ErrSelfDead
// and sends nothing after it.
func TestDeathDuringSettleEndsAtOnce(t *testing.T) {
	f, s := deadSession(t, 6)
	o, _ := f.world.Object(f.self)
	dead := patchHealth(t, f.selfCreate, o.Info.Health)
	s.events <- event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGUpdateObject, Payload: dead}}
	done := make(chan error, 1)
	go func() { done <- s.settle(context.Background(), 30*time.Second) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSelfDead) {
			t.Fatalf("settle returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the settle did not end on the death")
	}
	if len(f.sent) != 0 {
		t.Fatalf("a dead character still sent %d packet(s)", len(f.sent))
	}
}

// TestEnterNoticesADeadSocket: the fake server drops a socket at each stage of a run (right after the login burst,
// while the client waits, and when the logout request arrives), as a FIN and as a RST. Enter must end with
// ErrLinkLost at once, name the socket, and print the logout as impossible instead of writing to the corpse.
//
// The instance socket is not dropped at the logout here: its close with the logout is the session ending, not a lost
// link (TestLogoutInstanceCloseIsNotLinkLoss pins that), so only the realm socket's death is the loss at that stage.
func TestEnterNoticesADeadSocket(t *testing.T) {
	for _, sock := range []string{"instance", "realm"} {
		for _, when := range []string{"login", "stay", "logout"} {
			if sock == "instance" && when == "logout" {
				continue
			}
			for _, reset := range []bool{false, true} {
				name := sock + "/" + when
				if reset {
					name += "/reset"
				}
				t.Run(name, func(t *testing.T) {
					f := newFakeServer(t)
					f.closeSocket, f.closeWhen, f.resetLink = sock, when, reset
					defer f.close()
					f.wg.Add(1)
					go f.serveRealm()
					var steps []string
					report, err := Enter(t.Context(), Options{
						Address:         f.realmListener.Addr().String(),
						RealmID:         70,
						RealmAddress:    0x46010046,
						RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
						ClientSecret:    f.keyData[:32],
						JoinSecret:      f.keyData[32:],
						ClientBuild:     70707,
						RealmBuild:      70707,
						Character:       "XXXXXX",
						Stay:            2 * time.Second,
						Deadline:        15 * time.Second,
						BuildAuthKey:    f.buildKey[:],
						Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
					})
					if !errors.Is(err, ErrLinkLost) {
						t.Fatalf("Enter returned %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
					}
					if report == nil || !report.LinkLost || report.LinkLostSide != sock {
						t.Fatalf("the report does not name the %s socket: %+v", sock, report)
					}
					if report.LastPacket == "" {
						t.Fatalf("the report does not name the last packet: %+v", report)
					}
					joined := strings.Join(steps, "\n")
					if !strings.Contains(joined, "logout not possible: connection lost") {
						t.Fatalf("the run did not report the impossible logout:\n%s", joined)
					}
					if strings.Contains(joined, "CMSG_LOGOUT_REQUEST sent after the failure") {
						t.Fatalf("a dead link still tried the abort logout:\n%s", joined)
					}
				})
			}
		}
	}
}

// TestChatAndPartyChecksNoticeADeadSocket: the waits chat-check and party-check own (the chat listen, the wait for an
// invite) end at once with ErrLinkLost when the instance socket drops, instead of waiting out their own deadlines.
// The fake server drops it 150 ms after the login burst, while the client is already in the command's wait.
func TestChatAndPartyChecksNoticeADeadSocket(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeServer, opts *Options)
	}{
		{"chat listen", func(f *fakeServer, opts *Options) {
			f.chat = true
			opts.ChatCheck = true
			opts.Listen = 30 * time.Second
		}},
		{"party invite wait", func(f *fakeServer, opts *Options) {
			// The fake runs the party flow but sends no invite, so the client waits for one the whole 30 s without
			// the drop.
			f.partyInvite = true
			opts.PartyCheck = true
			opts.PartyAcceptFrom = "Inviter"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.closeSocket, f.closeWhen = "instance", "stay"
			defer f.close()
			f.wg.Add(1)
			go f.serveRealm()
			var steps []string
			opts := Options{
				Address:         f.realmListener.Addr().String(),
				RealmID:         70,
				RealmAddress:    0x46010046,
				RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
				ClientSecret:    f.keyData[:32],
				JoinSecret:      f.keyData[32:],
				ClientBuild:     70707,
				RealmBuild:      70707,
				Character:       "XXXXXX",
				Settle:          100 * time.Millisecond, // the drop (150 ms) lands inside the command's own wait
				Deadline:        15 * time.Second,
				BuildAuthKey:    f.buildKey[:],
				Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
			}
			tc.setup(f, &opts)
			started := time.Now()
			report, err := Enter(t.Context(), opts)
			if !errors.Is(err, ErrLinkLost) {
				t.Fatalf("Enter returned %v after %s\nsteps:\n%s", err, time.Since(started), strings.Join(steps, "\n"))
			}
			if took := time.Since(started); took > 5*time.Second {
				t.Fatalf("the dead link took %s to end the wait", took)
			}
			if report == nil || !report.LinkLost || report.LinkLostSide != "instance" {
				t.Fatalf("the report does not name the instance socket: %+v", report)
			}
		})
	}
}

// TestFightCheckStopsAtOnceOnADeadCharacter: a character that is already dead when the command settles is not sent a
// ping, a name query or anything else, and the run returns ErrSelfDead (the command's dead exit code).
func TestFightCheckStopsAtOnceOnADeadCharacter(t *testing.T) {
	f, s := deadSession(t, 6)
	o, _ := f.world.Object(f.self)
	u, err := objects.ParseUpdateObject(patchHealth(t, f.selfCreate, o.Info.Health))
	if err != nil {
		t.Fatalf("the dead self create: %v", err)
	}
	f.world.Apply(u, f.now)
	s.opts.Settle = 100 * time.Millisecond
	if err := s.fightCheck(context.Background()); !errors.Is(err, ErrSelfDead) {
		t.Fatalf("fightCheck returned %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a dead character still sent %d packet(s): %v", len(f.sent), f.sentCores())
	}
}

// linkKillingSender is the session's sender with a socket death injected at one of the walk's waits: the walk must
// notice through the session's own dead channel and end at that instant, not at its next packet or its deadline.
type linkKillingSender struct {
	objects.Sender
	s    *session
	at   int
	seen int
}

func (k *linkKillingSender) WaitUntil(ctx context.Context, until time.Time) error {
	k.seen++
	if k.seen == k.at {
		k.s.setLinkDead(sideInstance, io.EOF)
	}
	return k.Sender.WaitUntil(ctx, until)
}

// TestWalkPathEndsAtOnceOnALostLink: a socket that dies mid-walk ends the walk with ErrLinkLost on the very next
// wait, names the socket, and sends nothing more successfully.
func TestWalkPathEndsAtOnceOnALostLink(t *testing.T) {
	f, s := deadSession(t, 6)
	s.mover.Sender = &linkKillingSender{Sender: s.mover.Sender, s: s, at: 2}
	sent := len(f.sent)
	x, y, z, _ := s.mover.Position()
	err := s.mover.WalkPath(context.Background(), [][3]float32{
		{x + 30, y, z}, {x + 30, y + 30, z},
	})
	if !errors.Is(err, ErrLinkLost) {
		t.Fatalf("a walk whose link died returned %v, want ErrLinkLost", err)
	}
	if !s.report.LinkLost || s.report.LinkLostSide != "instance" {
		t.Fatalf("the report does not name the lost socket: %+v", s.report)
	}
	// Nothing after the loss reached the wire, and the mover's attempt to stop is refused: the session checks the
	// dead link before writing.
	for _, p := range f.sent[sent:] {
		if p.core == wire.CMSGMoveStop {
			t.Fatalf("a stop reached the dead link (packets %d..%d)", sent, len(f.sent))
		}
	}
	if got := len(f.sent) - sent; got != 3 { // facing, start-forward, the one heartbeat before the second wait
		t.Fatalf("%d packets before the loss, want 3", got)
	}
}

// TestLogoutInstanceCloseIsNotLinkLoss: the server closes the instance socket as part of the logout, and the response
// queued on it may be dropped with the close (WorldSession.cpp:766,789). The run must wait for the completion on the
// realm socket and end cleanly: no error, no link-loss report.
func TestLogoutInstanceCloseIsNotLinkLoss(t *testing.T) {
	f := newFakeServer(t)
	defer f.close()
	f.logoutNoResponse = true
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	report, err := Enter(t.Context(), Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Stay:            100 * time.Millisecond,
		Deadline:        20 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !report.LogoutComplete {
		t.Fatalf("the logout did not complete\nsteps:\n%s", joinLines(steps))
	}
	if report.LinkLost {
		t.Fatalf("the instance close with the logout was reported as a lost link (%s: %s)",
			report.LinkLostSide, report.LinkLostDetail)
	}
	if !strings.Contains(joinLines(steps), "no SMSG_LOGOUT_RESPONSE") {
		t.Fatalf("the run did not say the response was absent\nsteps:\n%s", joinLines(steps))
	}
}

// TestLogoutSilentRealmIsALoss: the instance socket closes with the logout and the realm then sends nothing. The
// wait must end at the silence bound with ErrLinkLost naming the realm, not hand the caller a zero event (which the
// run would log as wire 0x000000 and which resets the silence clock).
func TestLogoutSilentRealmIsALoss(t *testing.T) {
	f, s := deadSession(t, 6)
	s.deadMu.Lock()
	s.lastPacketAt = time.Now().Add(-(linkSilenceLimit + time.Second))
	s.deadMu.Unlock()
	done := make(chan error, 1)
	sent := len(f.sent)
	go func() { done <- s.logout(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.deadMu.Lock()
		requested := s.logoutSent
		s.deadMu.Unlock()
		if requested {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the logout request was not sent")
		}
		time.Sleep(time.Millisecond)
	}
	// What the instance socket's reader does when the server closes it with the logout.
	s.setLinkDead(sideInstance, io.EOF)
	select {
	case err := <-done:
		if !errors.Is(err, ErrLinkLost) {
			t.Fatalf("logout returned %v, want ErrLinkLost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the logout did not end on the realm's silence")
	}
	if !s.report.LinkLost || s.report.LinkLostSide != "realm" {
		t.Fatalf("the report does not name the silent realm: %+v", s.report)
	}
	if s.report.LastPacket == "wire 0x000000" {
		t.Fatal("the logout handled a zero packet as traffic")
	}
	if len(f.sent) != sent+1 { // the logout request, nothing after it
		t.Fatalf("the logout sent %d packet(s), want only the request", len(f.sent)-sent)
	}
}

// TestLogoutRefusedByTheServer: a non-zero SMSG_LOGOUT_RESPONSE result means the server would not end the session
// (in combat, dueling, falling). The run must end with that refusal named, not wait for a completion the server will
// not send, and not report the still-open session as a lost link.
func TestLogoutRefusedByTheServer(t *testing.T) {
	f := newFakeServer(t)
	defer f.close()
	f.logoutRefuse = 1
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	report, err := Enter(t.Context(), Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Stay:            100 * time.Millisecond,
		Deadline:        20 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
	})
	if err == nil || !strings.Contains(err.Error(), "the server refused the logout: result 1") {
		t.Fatalf("Enter returned %v, want the refusal named\nsteps:\n%s", err, joinLines(steps))
	}
	if errors.Is(err, ErrLinkLost) {
		t.Fatalf("the refusal was reported as a lost link: %v", err)
	}
	if report == nil || report.LogoutComplete || report.Logout.Result != 1 || report.LinkLost {
		t.Fatalf("the report after the refusal: %+v", report)
	}
	f.wait(t)
}

// TestLogoutRealmLossBeforeCompletionIsAnError: with no completion the session is not ending, and a realm socket
// that closes during the logout is the loss it looks like.
func TestLogoutRealmLossBeforeCompletionIsAnError(t *testing.T) {
	f := newFakeServer(t)
	defer f.close()
	f.closeSocket, f.closeWhen = "realm", "logout"
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	report, err := Enter(t.Context(), Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Stay:            100 * time.Millisecond,
		Deadline:        20 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
	})
	if !errors.Is(err, ErrLinkLost) {
		t.Fatalf("Enter returned %v, want ErrLinkLost\nsteps:\n%s", err, joinLines(steps))
	}
	if report == nil || report.LogoutComplete {
		t.Fatalf("the report claims a completion: %+v", report)
	}
}

// TestLogoutRealmLossAfterTheResponseIsStillALoss: the server answered the logout request, then the realm socket
// closed before SMSG_LOGOUT_COMPLETE. Only the completion ends the session: the answer alone must not turn the lost
// realm socket into a clean end.
func TestLogoutRealmLossAfterTheResponseIsStillALoss(t *testing.T) {
	f := newFakeServer(t)
	defer f.close()
	f.closeSocket, f.closeWhen = "realm", "logout"
	f.logoutRealmLoss = true
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	report, err := Enter(t.Context(), Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Stay:            100 * time.Millisecond,
		Deadline:        20 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
	})
	if !errors.Is(err, ErrLinkLost) {
		t.Fatalf("Enter returned %v, want ErrLinkLost\nsteps:\n%s", err, joinLines(steps))
	}
	if report == nil || report.LogoutComplete || !report.LinkLost || report.LinkLostSide != "realm" {
		t.Fatalf("the report: complete %v, link lost %v on %q", report.LogoutComplete, report.LinkLost, report.LinkLostSide)
	}
}
