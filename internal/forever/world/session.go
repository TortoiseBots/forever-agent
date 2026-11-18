package world

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// Options is what the Battle.net login left plus the character to enter with.
type Options struct {
	Address         string
	RealmID         uint32
	RealmAddress    uint32
	RealmJoinTicket []byte
	ClientSecret    []byte
	JoinSecret      []byte
	ClientBuild     uint32
	RealmBuild      uint32
	// BuildAuthKey is auth.build_auth_key for the ticket's variant when the caller has it; empty makes the world
	// socket send the zero digest the live server accepts under Network.SkipBuildAuthKeyCheck.
	BuildAuthKey []byte
	Character    string        // character name (the surname may be omitted)
	Stay         time.Duration // how long to stay in the world after the login burst
	Deadline     time.Duration // whole run; default 2 minutes + Stay
	// AllowInstanceRedirect lets SMSG_CONNECT_TO name a host other than the realm socket's; refused by default.
	AllowInstanceRedirect bool
	Logf                  func(format string, args ...any)
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Report is what the run observed.
type Report struct {
	Characters []Character
	Character  Character
	MapID      int32
	Position   [4]float32
	// InWorld is true once the server sent SMSG_MOVE_SET_ACTIVE_MOVER (the world the client sees) and the
	// completion was answered.
	InWorld         bool
	ActiveMoverGUID wire.GUID
	Stayed          time.Duration
	Counts          map[string]int // server packets by opcode name (unknown numbers as "wire 0xNNNNNN")
	TimeSyncs       int
	Pings           int
	Pongs           int
	Latency         time.Duration
	Logout          LogoutResponse
	LogoutComplete  bool
}

// Count is one line of the packet count.
type Count struct {
	Name  string
	Count int
}

// SortedCounts returns the packet counts by descending count, then name.
func (r *Report) SortedCounts() []Count {
	rows := make([]Count, 0, len(r.Counts))
	for name, n := range r.Counts {
		rows = append(rows, Count{name, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

const (
	sideRealm = iota
	sideInstance
)

type event struct {
	side int
	p    worldconn.Packet
	err  error
}

type session struct {
	opts     Options
	ctx      context.Context
	realm    *worldconn.Realm
	instance *worldconn.Instance
	events   chan event
	report   *Report
	start    time.Time

	// The two socket readers. pumpCancel stops them (it also unblocks a read through the context watcher of the
	// connection) and pumpDone is closed by each when it has returned; Enter waits for both before its
	// connections are closed for good, so a reader never touches a closing socket or inflater.
	pumpCtx    context.Context
	pumpCancel context.CancelFunc
	pumpDone   []chan struct{}

	activeMoverDone bool
	pingSerial      uint32
	pingSentAt      time.Time

	// unknownKeys is how many distinct unknown wire opcodes the report has a key for. A flooding server that walks
	// wire numbers must not grow the report per packet: past maxUnknownReportKeys everything collapses into one key.
	unknownKeys int
}

const (
	// maxUnknownReportKeys is the number of distinct unknown wire opcodes the packet count keeps. Known opcodes are
	// bounded by the table itself, so only the unknown ones need a cap.
	maxUnknownReportKeys = 64
	// otherUnknownKey holds every unknown wire opcode past the cap.
	otherUnknownKey = "other unknown wire opcodes"
)

// Enter runs the world half: authenticate, list characters, log the named character in, answer the login burst
// and keep-alives for Stay, then log out. It returns what was observed even when it fails part-way.
func Enter(ctx context.Context, opts Options) (*Report, error) {
	if opts.Character == "" {
		return nil, errors.New("world: no character name")
	}
	if opts.Deadline <= 0 {
		opts.Deadline = 2*time.Minute + opts.Stay
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Deadline)
	defer cancel()

	report := &Report{Counts: make(map[string]int)}
	s := &session{opts: opts, ctx: ctx, events: make(chan event, 512), report: report, start: time.Now()}
	s.pumpCtx, s.pumpCancel = context.WithCancel(ctx)
	defer s.pumpCancel()

	realm, err := worldconn.Authenticate(ctx, worldconn.Config{
		Address:               opts.Address,
		RealmID:               opts.RealmID,
		RealmAddress:          opts.RealmAddress,
		RealmJoinTicket:       opts.RealmJoinTicket,
		ClientSecret:          opts.ClientSecret,
		JoinSecret:            opts.JoinSecret,
		ClientBuild:           opts.ClientBuild,
		RealmBuild:            opts.RealmBuild,
		BuildAuthKey:          opts.BuildAuthKey,
		AllowInstanceRedirect: opts.AllowInstanceRedirect,
		Deadline:              opts.Deadline,
		Logf:                  opts.Logf,
	})
	if err != nil {
		return report, err
	}
	s.realm = realm
	for _, name := range realm.HandshakeNames() {
		s.report.Counts[name]++
	}
	s.startPump(sideRealm, realm.Next)

	runErr := s.enter(ctx)
	if runErr != nil {
		// the character may be online already; tell the server it is leaving before the sockets drop
		s.abortLogin()
	}
	s.shutdown(ctx)
	return report, runErr
}

// startPump runs one socket reader and records how to wait for it.
func (s *session) startPump(side int, next func(context.Context) (worldconn.Packet, error)) {
	done := make(chan struct{})
	s.pumpDone = append(s.pumpDone, done)
	go func() {
		defer close(done)
		pump(s.pumpCtx, side, next, s.events)
	}()
}

// shutdown stops the readers, closes both sockets in a fixed order whatever happened before, and waits for the
// readers to return before Enter does. The wait is bounded by the run's deadline (or two seconds when that has
// already passed, because Close has then unblocked every read anyway).
func (s *session) shutdown(ctx context.Context) {
	s.pumpCancel()
	if s.instance != nil {
		_ = s.instance.Close()
	}
	if s.realm != nil {
		_ = s.realm.Close()
	}
	waitCtx := ctx
	if waitCtx.Err() != nil {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	for _, done := range s.pumpDone {
		select {
		case <-done:
		case <-waitCtx.Done():
			return
		}
	}
}

// abortLogin is the logout a failed run owes the server: a character whose socket just disappears stays online
// until the server's own timeout. Best effort, bounded by two seconds, and only after the character was logged in
// (the instance socket exists from CMSG_PLAYER_LOGIN on).
func (s *session) abortLogin() {
	if s.instance == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.instance.Send(ctx, wire.CMSGLogoutRequest, BuildLogoutRequest()); err != nil {
		s.opts.logf("world abort: CMSG_LOGOUT_REQUEST: %v", err)
	} else {
		s.opts.logf("world abort: CMSG_LOGOUT_REQUEST sent after the failure")
	}
	if err := s.instance.Send(ctx, wire.CMSGLogDisconnect, BuildLogDisconnect()); err != nil {
		s.opts.logf("world abort: CMSG_LOG_DISCONNECT: %v", err)
	} else {
		s.opts.logf("world abort: CMSG_LOG_DISCONNECT sent")
	}
}

func pump(ctx context.Context, side int, next func(context.Context) (worldconn.Packet, error), out chan<- event) {
	for {
		p, err := next(ctx)
		select {
		case out <- event{side: side, p: p, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *session) enter(ctx context.Context) error {
	// ---- character list
	if err := s.realm.Send(ctx, wire.CMSGEnumCharacters, nil); err != nil {
		return err
	}
	s.opts.logf("world character list: CMSG_ENUM_CHARACTERS sent")
	p, err := s.waitFor("the character list", 30*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGEnumCharactersResult
	})
	if err != nil {
		return err
	}
	list, err := ParseEnumCharactersResult(p.Payload)
	if err != nil {
		return err
	}
	s.report.Characters = list.All()
	s.opts.logf("world character list: %d character(s), max level %d, %d trailing bytes",
		len(s.report.Characters), list.MaxLevel, list.TrailingBytes)
	for _, c := range s.report.Characters {
		s.opts.logf("  character %s: level %d, class %d, race %d, map %d, at %.1f %.1f %.1f", safeText(c.DisplayName()),
			c.Level, c.Class, c.Race, c.MapID, c.X, c.Y, c.Z)
	}
	character, ok := findCharacter(s.report.Characters, s.opts.Character)
	if !ok {
		return fmt.Errorf("world: the account has no character named %q", safeText(s.opts.Character))
	}
	s.report.Character = character

	// ---- CMSG_PLAYER_LOGIN and the instance connection
	if err := s.realm.Send(ctx, wire.CMSGPlayerLogin, BuildPlayerLogin(character.GUID)); err != nil {
		return err
	}
	s.opts.logf("world login: CMSG_PLAYER_LOGIN sent for %s (%s)", safeText(character.DisplayName()), character.GUID)
	p, err = s.waitFor("SMSG_CONNECT_TO", 30*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGConnectTo
	})
	if err != nil {
		return err
	}
	ct, err := worldconn.ParseConnectTo(p.Payload)
	if err != nil {
		return err
	}
	s.opts.logf("world login: SMSG_CONNECT_TO names %s (serial %d)", ct.Address(), ct.Serial)
	inst, err := s.realm.DialInstance(ctx, ct)
	if err != nil {
		return err
	}
	s.instance = inst
	for _, name := range inst.HandshakeNames() {
		s.report.Counts[name]++
	}
	s.startPump(sideInstance, inst.Next)

	// ---- the instance connection starts the login: SMSG_RESUME_COMMS, then the burst
	if _, err := s.waitFor("SMSG_RESUME_COMMS", 30*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGResumeComms
	}); err != nil {
		return err
	}
	now := clientTimeMs()
	if err := s.instance.Send(ctx, wire.CMSGQueuedMessagesEnd, BuildQueuedMessagesEnd(now)); err != nil {
		return err
	}
	s.opts.logf("world login: SMSG_RESUME_COMMS answered with CMSG_QUEUED_MESSAGES_END (%d ms)", now)

	p, err = s.waitFor("SMSG_LOGIN_VERIFY_WORLD", 60*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGLoginVerifyWorld
	})
	if err != nil {
		return err
	}
	verify, err := ParseLoginVerifyWorld(p.Payload)
	if err != nil {
		return err
	}
	s.report.MapID = verify.MapID
	s.report.Position = [4]float32{verify.X, verify.Y, verify.Z, verify.O}
	s.opts.logf("world: in world on map %d at %.2f %.2f %.2f (orientation %.2f), reason %d",
		verify.MapID, verify.X, verify.Y, verify.Z, verify.O, verify.Reason)
	// the real client sends the loading screen notification on the realm socket, with the map it just entered
	if err := s.realm.Send(ctx, wire.CMSGLoadingScreenNotify, BuildLoadingScreenNotify(verify.MapID)); err != nil {
		return err
	}
	s.opts.logf("world: CMSG_LOADING_SCREEN_NOTIFY sent (map %d, showing false)", verify.MapID)

	// ---- stay: answer time syncs, the active-mover exchange and pings
	if err := s.stay(ctx); err != nil {
		return err
	}
	return s.logout(ctx)
}

// stay answers the server until the stay time is over.
func (s *session) stay(ctx context.Context) error {
	end := time.Now().Add(s.opts.Stay)
	s.sendPing()
	s.opts.logf("world: staying %s, answering time syncs and pings", s.opts.Stay)
	pingEvery := 30 * time.Second
	nextPing := time.Now().Add(pingEvery)
	for {
		remaining := time.Until(end)
		if remaining <= 0 {
			break
		}
		wait := remaining
		if untilPing := time.Until(nextPing); untilPing < wait {
			wait = untilPing
		}
		ev, err := s.next("the stay", wait)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				if time.Now().After(nextPing) {
					s.sendPing()
					nextPing = time.Now().Add(pingEvery)
				}
				continue
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
	s.report.Stayed = time.Since(end.Add(-s.opts.Stay))
	s.opts.logf("world: stay over (%s, %d time syncs, %d pings, %d pongs, in world: %v)",
		s.report.Stayed.Round(time.Millisecond), s.report.TimeSyncs, s.report.Pings, s.report.Pongs, s.report.InWorld)
	return nil
}

// logout sends CMSG_LOGOUT_REQUEST and waits for the response and the completion. The server waits 20 s before it
// completes a logout that is not instant, and it closes the instance socket after the completion.
func (s *session) logout(ctx context.Context) error {
	if err := s.instance.Send(ctx, wire.CMSGLogoutRequest, BuildLogoutRequest()); err != nil {
		return err
	}
	s.opts.logf("world logout: CMSG_LOGOUT_REQUEST sent")
	deadline := time.Now().Add(30 * time.Second)
	responded := false
	var completeAt time.Time
	for {
		wait := time.Until(deadline)
		if s.report.LogoutComplete {
			// the response and the completion come on different sockets, so give the response 500 ms to arrive
			// after the completion instead of blocking for the whole deadline
			grace := 500*time.Millisecond - time.Since(completeAt)
			if responded || grace <= 0 {
				break
			}
			if grace < wait {
				wait = grace
			}
		}
		if wait <= 0 {
			return fmt.Errorf("world: no SMSG_LOGOUT_COMPLETE within 30 s")
		}
		ev, err := s.next("the logout", wait)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: no SMSG_LOGOUT_COMPLETE within 30 s")
			}
			return err
		}
		if ev.err != nil {
			// The server sends the completion on the realm socket and closes the instance socket right after, so an
			// instance read error is expected once the response arrived; the completion is still on its way.
			if ev.side == sideInstance && !s.report.LogoutComplete && s.report.Logout.Result == 0 {
				continue
			}
			if s.report.LogoutComplete {
				continue
			}
			return fmt.Errorf("world: the connection %d closed during the logout: %w", ev.side, ev.err)
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
		if ev.p.Core == wire.SMSGLogoutResponse {
			responded = true
			if s.report.Logout.Result != 0 {
				return fmt.Errorf("world: the server refused the logout: result %d", s.report.Logout.Result)
			}
		}
		if s.report.LogoutComplete && completeAt.IsZero() {
			completeAt = time.Now()
		}
	}
	s.opts.logf("world logout: SMSG_LOGOUT_RESPONSE (result %d, instant %v) and SMSG_LOGOUT_COMPLETE",
		s.report.Logout.Result, s.report.Logout.Instant)
	return nil
}

func (s *session) sendPing() {
	target := s.instance
	if target == nil {
		return
	}
	s.pingSerial++
	b := BuildPing(s.pingSerial, uint32(s.report.Latency.Milliseconds()))
	s.pingSentAt = time.Now()
	if err := target.Send(s.ctx, wire.CMSGPing, b); err != nil {
		return
	}
	s.report.Pings++
}

// countPacket adds one packet to the report by name. An unknown wire opcode gets its own "wire 0xNNNNNN" key until
// maxUnknownReportKeys of them exist; beyond that they all count under otherUnknownKey, so a server that walks wire
// numbers cannot grow the map without bound.
func (s *session) countPacket(p worldconn.Packet) {
	if p.Name != "" {
		s.report.Counts[p.Name]++
		return
	}
	name := fmt.Sprintf("wire 0x%06X", p.Wire)
	if _, seen := s.report.Counts[name]; !seen {
		if s.unknownKeys >= maxUnknownReportKeys {
			name = otherUnknownKey
		} else {
			s.unknownKeys++
		}
	}
	s.report.Counts[name]++
}

// handle counts and answers one server packet. Unknown packets are counted and skipped, never an error.
func (s *session) handle(ctx context.Context, ev event) error {
	if ev.err != nil {
		return fmt.Errorf("world: connection %d: %w", ev.side, ev.err)
	}
	p := ev.p
	s.countPacket(p)
	switch p.Core {
	case wire.SMSGTimeSyncRequest:
		seq, err := ParseTimeSyncRequest(p.Payload)
		if err != nil {
			return err
		}
		now := clientTimeMs()
		if err := s.sendOn(ev.side, wire.CMSGTimeSyncResponse, BuildTimeSyncResponse(seq, now)); err != nil {
			return err
		}
		s.report.TimeSyncs++
	case wire.SMSGPong:
		serial, err := ParsePong(p.Payload)
		if err != nil {
			return err
		}
		if serial == s.pingSerial {
			s.report.Latency = time.Since(s.pingSentAt)
		}
		s.report.Pongs++
	case wire.SMSGMoveSetActiveMover:
		guid, err := ParseMoveSetActiveMover(p.Payload)
		if err != nil {
			return err
		}
		s.report.ActiveMoverGUID = guid
		if !s.activeMoverDone {
			if err := s.sendOn(ev.side, wire.CMSGMoveInitActiveMoverDone, BuildMoveInitActiveMoverComplete(clientTimeMs())); err != nil {
				return err
			}
			s.activeMoverDone = true
			s.report.InWorld = true
			s.opts.logf("world: SMSG_MOVE_SET_ACTIVE_MOVER for %s, CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE sent", guid)
		}
	case wire.SMSGCharacterLoginFailed:
		code, err := ParseCharacterLoginFailed(p.Payload)
		if err != nil {
			return err
		}
		return fmt.Errorf("world: the server refused the character login (reason %d)", code)
	case wire.SMSGLogoutResponse:
		r, err := ParseLogoutResponse(p.Payload)
		if err != nil {
			return err
		}
		s.report.Logout = r
	case wire.SMSGLogoutComplete:
		s.report.LogoutComplete = true
	}
	return nil
}

func (s *session) sendOn(side int, core uint32, payload []byte) error {
	switch side {
	case sideRealm:
		return s.realm.Send(s.ctx, core, payload)
	case sideInstance:
		return s.instance.Send(s.ctx, core, payload)
	}
	return fmt.Errorf("world: no connection %d", side)
}

var errWaitTimeout = errors.New("world: wait timed out")

// waitFor reads events until match returns true, answering what the server asks in between (time syncs, active
// mover, logout packets). One absolute timeout bounds the wait.
func (s *session) waitFor(what string, timeout time.Duration, match func(worldconn.Packet) bool) (worldconn.Packet, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return worldconn.Packet{}, fmt.Errorf("world: no %s within %s", what, timeout)
		}
		ev, err := s.next(what, remaining)
		if err != nil {
			return worldconn.Packet{}, err
		}
		if ev.err != nil {
			return worldconn.Packet{}, fmt.Errorf("world: the connection closed while waiting for %s: %w", what, ev.err)
		}
		if err := s.handle(s.ctx, ev); err != nil {
			return worldconn.Packet{}, err
		}
		if match(ev.p) {
			return ev.p, nil
		}
	}
}

func (s *session) next(what string, timeout time.Duration) (event, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ev := <-s.events:
		return ev, nil
	case <-timer.C:
		return event{}, errWaitTimeout
	case <-s.ctx.Done():
		return event{}, fmt.Errorf("world: waiting for %s: %w", what, s.ctx.Err())
	}
}

// findCharacter matches the requested name against the character name and against name + surname, ignoring case.
func findCharacter(list []Character, want string) (Character, bool) {
	for _, c := range list {
		if equalFoldASCII(c.Name, want) || equalFoldASCII(c.DisplayName(), want) || equalFoldASCII(c.Name+" "+c.Surname, want) {
			return c, true
		}
	}
	return Character{}, false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 'a' - 'A'
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// safeText bounds a character name before it goes to the log: printable ASCII only, at most 64 bytes.
func safeText(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			c = '?'
		}
		out = append(out, c)
	}
	return string(out)
}
