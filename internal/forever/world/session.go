package world

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
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
	// WalkCheck runs round 3's walk (forever walk-check): look at the world for Settle, walk Dx/Dy yards from the
	// spawn, wait WalkPause, walk back, report, log out. Stay is then the settle time instead of a plain stay.
	WalkCheck bool
	Dx, Dy    float32
	WalkPause time.Duration
	Settle    time.Duration
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

	// Round 3: what the client understood of the world and where it walked.
	Objects       ObjectSummary
	Units         []UnitRow
	MonsterMoves  int
	MoveRelays    int
	Teleports     int
	MapChanges    int
	NamesAsked    int
	NamesFound    int
	Walked        bool
	WalkFrom      [4]float32
	WalkTo        [4]float32
	BackAt        [4]float32
	WalkClientPos [4]float32
}

// ObjectSummary counts the world model by object type, plus the update statistics.
type ObjectSummary struct {
	Map        uint16
	Objects    int
	Created    int
	Updated    int
	Destroyed  int
	OutOfRange int
	Updates    int
	// Partial counts objects with a block that was not decoded to its declared end; Unparsed counts the blocks
	// that were skipped whole, by object type and reason.
	Partial  int
	Unparsed map[string]int
	ByType   map[string]int
}

// UnitRow is one line of the nearest-units table.
type UnitRow struct {
	Type      string
	Entry     int32
	Name      string
	Level     int32
	Health    int64
	MaxHealth int64
	Distance  float32
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

	// Round 3: the world model, the character's own GUID once the create block names it, and the mover.
	world    *objects.World
	self     wire.GUID
	selfSeen bool
	mover    *objects.Mover
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
	s.world = objects.NewWorld(0)
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
	if s.opts.WalkCheck {
		if err := s.walkCheck(ctx); err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if err := s.stay(ctx); err != nil {
		return err
	}
	return s.logout(ctx)
}

// Send writes one client packet on the instance socket (the socket the real client sends gameplay on).
func (s *session) Send(core uint32, payload []byte) error {
	return s.instance.Send(s.ctx, core, payload)
}

// Now is the client clock the movement time field carries.
func (s *session) Now() time.Time { return time.Now() }

// WaitUntil answers server packets until the given time, so a walk keeps the session healthy (time syncs, pings and
// object updates all arrive while it walks).
func (s *session) WaitUntil(ctx context.Context, until time.Time) error {
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			return nil
		}
		ev, err := s.next("the walk", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return nil
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
}

// collect answers the server for d and applies what it sends.
func (s *session) collect(ctx context.Context, d time.Duration) error {
	end := time.Now().Add(d)
	for {
		remaining := time.Until(end)
		if remaining <= 0 {
			return nil
		}
		ev, err := s.next("the settle", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return nil
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
}

const (
	// nameQueryDeadline bounds one wait for a name answer; the server answers both lookups immediately.
	nameQueryDeadline = 3 * time.Second
	// maxNameQueryRounds bounds how many batches of lookups one run sends.
	maxNameQueryRounds = 4
	// maxPlayerNamesPerQuery is the server's Array<ObjectGuid, 50> (QueryPackets.h:112).
	maxPlayerNamesPerQuery = 50
	// maxCreatureQueries is what the official client batches per query (QueryPackets.cpp:66-67).
	maxCreatureQueries = 11
)

// queryNames asks for the names of the nearest units this client does not know yet, bounded in rounds and with a
// deadline per answer.
func (s *session) queryNames(ctx context.Context) {
	x, y, z := s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2]
	for round := 0; round < maxNameQueryRounds; round++ {
		players, entries := s.world.UnknownUnits(x, y, z, maxPlayerNamesPerQuery)
		if len(entries) > maxCreatureQueries {
			entries = entries[:maxCreatureQueries]
		}
		if len(players) == 0 && len(entries) == 0 {
			return
		}
		if len(players) > 0 {
			if err := s.sendOn(sideInstance, wire.CMSGQueryPlayerNames, objects.BuildQueryPlayerNames(players)); err != nil {
				return
			}
			s.report.NamesAsked += len(players)
			s.opts.logf("world: CMSG_QUERY_PLAYER_NAMES for %d player(s)", len(players))
			if _, err := s.waitFor("SMSG_QUERY_PLAYER_NAMES_RESPONSE", nameQueryDeadline, func(p worldconn.Packet) bool {
				return p.Core == wire.SMSGQueryPlayerNamesResponse
			}); err != nil {
				s.opts.logf("world: the player name answer did not come: %v", err)
				return
			}
		}
		if len(entries) > 0 {
			// The 6-bit count cannot name more than 63 entries; BuildQueryCreature splits a longer list.
			s.opts.logf("world: CMSG_QUERY_CREATURE for %d entr(y/ies)", len(entries))
			for _, packet := range objects.BuildQueryCreature(uint32Entries(entries)) {
				if err := s.sendOn(sideInstance, wire.CMSGQueryCreature, packet); err != nil {
					return
				}
				if _, err := s.waitFor("SMSG_QUERY_CREATURE_RESPONSE", nameQueryDeadline, func(p worldconn.Packet) bool {
					return p.Core == wire.SMSGCreatureQueryResponse
				}); err != nil {
					s.opts.logf("world: the creature name answer did not come: %v", err)
					return
				}
			}
		}
	}
}

// uint32Entries narrows the entries to the query's uint32 wire form.
func uint32Entries(entries []int32) []uint32 {
	out := make([]uint32, len(entries))
	for i, e := range entries {
		out[i] = uint32(e)
	}
	return out
}

// walkCheck is round 3's command: settle, summarise the world, walk dx/dy and back, report, and let Enter log out.
func (s *session) walkCheck(ctx context.Context) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 8 * time.Second
	}
	pause := s.opts.WalkPause
	if pause <= 0 {
		pause = 3 * time.Second
	}
	s.sendPing()
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	x, y, z, o := s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2], s.report.WalkClientPos[3]
	s.queryNames(ctx)
	s.summarise(x, y, z)

	// The walk: from the position the server put the character at (the create block's, not the login's).
	from := [4]float32{x, y, z, o}
	s.report.WalkFrom = from
	target := [4]float32{x + s.opts.Dx, y + s.opts.Dy, z, o}
	s.report.WalkTo = target
	if s.mover == nil {
		s.report.Walked = false
		s.opts.logf("walk-check: no create block of this character arrived, so the walk is skipped")
		return nil
	}
	if err := s.mover.MoveTo(ctx, target[0], target[1], target[2]); err != nil {
		return err
	}
	if err := s.collect(ctx, pause); err != nil {
		return err
	}
	if err := s.mover.MoveTo(ctx, from[0], from[1], from[2]); err != nil {
		return err
	}
	bx, by, bz, bo := s.mover.Position()
	s.report.BackAt = [4]float32{bx, by, bz, bo}
	s.report.WalkClientPos = s.report.BackAt
	s.report.Walked = true
	s.opts.logf("walk-check: back at the spawn, the client believes it is at %.2f %.2f %.2f (orientation %.2f)", bx, by, bz, bo)
	return nil
}

// summarise fills the report's object summary and nearest-units table, and prints both.
func (s *session) summarise(x, y, z float32) {
	c := s.world.Counts()
	summary := ObjectSummary{
		Map:        s.world.MapID(),
		Objects:    c.Objects,
		Created:    c.Created,
		Updated:    c.Updated,
		Destroyed:  c.Destroyed,
		OutOfRange: c.OutOfRange,
		Updates:    c.Applied,
		Partial:    c.Partial,
		Unparsed:   make(map[string]int, len(c.Unparsed)),
		ByType:     make(map[string]int),
	}
	for reason, n := range c.Unparsed {
		summary.Unparsed[reason] = n
	}
	for t, n := range c.ByType {
		summary.ByType[objects.TypeName(t)] = n
	}
	s.report.Objects = summary
	var typeNames []string
	for name := range summary.ByType {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)
	s.opts.logf("world: %d object(s) in the model after %d update(s):", summary.Objects, summary.Updates)
	for _, name := range typeNames {
		s.opts.logf("  %-16s %d", name, summary.ByType[name])
	}
	s.opts.logf("world: %d created, %d updated, %d destroyed, %d out of range", summary.Created, summary.Updated, summary.Destroyed, summary.OutOfRange)
	if c.Partial > 0 {
		s.opts.logf("world: %d object(s) partial: a block naming them was not decoded to its declared end", c.Partial)
	}
	if len(c.Unparsed) > 0 {
		total := 0
		keys := make([]string, 0, len(c.Unparsed))
		for key := range c.Unparsed {
			total += c.Unparsed[key]
			keys = append(keys, key)
		}
		sort.Strings(keys)
		s.opts.logf("world: %d unparsed block(s), skipped by size:", total)
		for _, key := range keys {
			if detail := c.UnparsedDetail[key]; detail != "" {
				s.opts.logf("  %-52s %d  (%s)", key, c.Unparsed[key], detail)
				continue
			}
			s.opts.logf("  %-52s %d", key, c.Unparsed[key])
		}
	}
	if c.UnknownMoves > 0 {
		s.opts.logf("world: %d movement packet(s) for a GUID the model does not hold (not created)", c.UnknownMoves)
	}

	units := s.world.Units(x, y, z)
	limit := 5
	if len(units) < limit {
		limit = len(units)
	}
	s.report.Units = make([]UnitRow, 0, limit)
	for _, u := range units[:limit] {
		row := UnitRow{
			Type:      objects.TypeName(u.Type),
			Entry:     u.Info.Entry,
			Name:      s.world.Name(u),
			Level:     u.Info.Level,
			Health:    u.Info.Health,
			MaxHealth: u.Info.MaxHealth,
			Distance:  objects.Distance(u.X, u.Y, u.Z, x, y, z),
		}
		s.report.Units = append(s.report.Units, row)
		s.opts.logf("  nearest: %-11s entry %-6d level %-3d health %d/%d  %5.1f yd  %s",
			row.Type, row.Entry, row.Level, row.Health, row.MaxHealth, row.Distance, row.Name)
	}
	s.report.NamesFound = 0
	for _, u := range units {
		if s.world.Name(u) != "" {
			s.report.NamesFound++
		}
	}
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
	case wire.SMSGUpdateObject:
		u, err := objects.ParseUpdateObject(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_UPDATE_OBJECT: %w", err)
		}
		s.applyUpdate(u)
		s.world.Apply(u, time.Now())
	case wire.SMSGOnMonsterMove:
		m, err := objects.ParseMonsterMove(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_ON_MONSTER_MOVE: %w", err)
		}
		s.report.MonsterMoves++
		s.world.ApplyMonsterMove(m, time.Now())
	case wire.SMSGMoveUpdate:
		m, err := objects.ParseMoveUpdate(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOVE_UPDATE: %w", err)
		}
		s.report.MoveRelays++
		s.world.ApplyMoveUpdate(m.Info, nil, time.Now())
	case wire.SMSGMoveUpdateTeleport:
		m, err := objects.ParseMoveUpdateTeleport(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOVE_UPDATE_TELEPORT: %w", err)
		}
		s.report.MoveRelays++
		s.world.ApplyMoveUpdate(m.Info, m.Speeds, time.Now())
	case wire.SMSGMoveTeleport:
		t, err := objects.ParseMoveTeleport(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOVE_TELEPORT: %w", err)
		}
		s.report.Teleports++
		s.world.ApplyTeleport(t, time.Now())
		if t.MoverGUID == s.self {
			if t.Transport != nil || t.HasVehicle {
				// An ack would tell the server the client is standing on the transport's seat, which this round
				// cannot model: report it and end the walk instead of sending an ack the client cannot honour.
				s.opts.logf("world: teleport onto a transport: not handled (transport %v, vehicle %v)", t.Transport != nil, t.HasVehicle)
				return fmt.Errorf("world: teleport onto a transport: not handled (transport %v, vehicle %v)", t.Transport != nil, t.HasVehicle)
			}
			// The server waits for CMSG_MOVE_TELEPORT_ACK before it accepts movement from the new position
			// (MovementHandler.cpp:424-480 reads it on the instance socket). Apply the destination, the facing and
			// the sequence to the mover first, so the first packet after the ack starts from there.
			sequence := t.Sequence
			if s.mover != nil {
				s.mover.SetTeleport(t.X, t.Y, t.Z, t.O, t.Sequence)
				sequence = s.mover.Sequence
				s.report.WalkClientPos = [4]float32{t.X, t.Y, t.Z, t.O}
			}
			if err := s.sendOn(sideInstance, wire.CMSGMoveTeleportAck, objects.BuildTeleportAck(t.MoverGUID, sequence, clientTimeMs())); err != nil {
				return err
			}
			s.opts.logf("world: SMSG_MOVE_TELEPORT answered (map %d, client position %.2f %.2f %.2f)", s.world.MapID(), t.X, t.Y, t.Z)
		}
	case wire.SMSGMoveRoot, wire.SMSGMoveUnroot:
		guid, _, err := objects.ParseMoverFlag(p.Payload)
		if err != nil {
			return fmt.Errorf("world: the root/unroot push: %w", err)
		}
		if guid == s.self {
			// No ack: the server only validates the movement info an ack carries (HandleMovementAckMessage,
			// MovementHandler.cpp:777-784) and rounds 1-2 left SMSG_MOVE_ROOT unanswered with a healthy session.
			s.opts.logf("world: %s for the active mover (no ack sent, see OBJECTS-R1-NOTES.md)", wire.ServerName(p.Core))
		}
	case wire.SMSGNewWorld, wire.SMSGTransferPending:
		s.report.MapChanges++
		s.opts.logf("world: %s seen (map change: not handled in this round)", wire.ServerName(p.Core))
	case wire.SMSGQueryPlayerNamesResponse:
		names, err := objects.ParsePlayerNamesResponse(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_QUERY_PLAYER_NAMES_RESPONSE: %w", err)
		}
		for _, n := range names {
			if n.Found && n.Name != "" {
				s.world.SetName(n.GUID, n.Name)
			}
		}
	case wire.SMSGCreatureQueryResponse:
		creatures, err := objects.ParseCreatureQueryResponse(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_QUERY_CREATURE_RESPONSE: %w", err)
		}
		for _, c := range creatures {
			if c.Found && c.Name != "" {
				s.world.SetEntryName(int32(c.Entry), c.Name)
			}
		}
	}
	return nil
}

// applyUpdate records what the client learned about itself from an update: the create block of ThisIsYou carries the
// character's GUID, position and run speed. A later ThisIsYou create (a map change or a fresh burst) puts the
// character somewhere else, so the mover's belief follows it instead of keeping the first create's position.
func (s *session) applyUpdate(u *objects.Update) {
	for _, b := range u.Blocks {
		if b.Movement == nil || !b.Movement.ThisIsYou {
			continue
		}
		if !s.selfSeen {
			s.selfSeen = true
			s.self = b.GUID
			if s.mover == nil {
				s.mover = &objects.Mover{
					GUID:       b.GUID,
					Sender:     s,
					ClientTime: func(time.Time) uint32 { return clientTimeMs() },
					Logf:       s.opts.logf,
					X:          b.Movement.X,
					Y:          b.Movement.Y,
					Z:          b.Movement.Z,
					O:          b.Movement.O,
					RunSpeed:   b.Movement.Run,
					Flags:      b.Movement.Flags,
				}
				s.world.SetActiveMover(b.GUID)
			}
			s.opts.logf("world: own create block: position %.2f %.2f %.2f, run speed %.2f yd/s, walk %.2f yd/s",
				b.Movement.X, b.Movement.Y, b.Movement.Z, b.Movement.Run, b.Movement.Walk)
		} else if s.mover != nil {
			s.mover.SetPosition(b.Movement.X, b.Movement.Y, b.Movement.Z, b.Movement.O)
			s.opts.logf("world: own create again: position reset to %.2f %.2f %.2f",
				b.Movement.X, b.Movement.Y, b.Movement.Z)
		}
		s.report.WalkClientPos = [4]float32{b.Movement.X, b.Movement.Y, b.Movement.Z, b.Movement.O}
	}
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
