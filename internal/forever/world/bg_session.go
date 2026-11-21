// The battleground side of the session: the decoded BG state the watcher
// reads, the packet dispatch, the status poll seam and the bg-watch runner.
//
// The model is what a client could see: world states (SMSG_INIT_WORLD_STATES
// / SMSG_UPDATE_WORLD_STATE), the status packets, the match packets and the
// spirit-healer time. BG-system chat is not stored: it carries player names,
// and the world states are the authority on the flags. Nothing here needs
// the module: the `.tb bg status` polls go through the command channel like
// every other typed module read, and the queue/port/leave sends are typed
// client packets (the GameObjUse precedent: the core's reader defines the
// bytes).

package world

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// bgState is the session's decoded battleground state for the watcher.
type bgState struct {
	// worlds is the world-state map the watcher reads (id -> value).
	worlds map[int32]int32
	// diedSince is the cursor into the death count the watcher has seen.
	diedSince int
	// deaths counts the watcher's own deaths (model Deaths() drains).
	deaths int
	// invite is the last NEED_CONFIRMATION the session decoded, and
	// inviteExpires when its accept window closes (arrival plus Timeout).
	invite        *bg.ConfirmStatus
	inviteExpires time.Time
	// active is set once SMSG_BATTLEFIELD_STATUS_ACTIVE confirmed the queue
	// entry; the watcher starts observing only then.
	active bool
	// end is the match end the authoritative PVP_MATCH_COMPLETE named.
	end *bg.WatchEnd
	// completeInit is a PVP_MATCH_INITIALIZE in the Complete state (the
	// character entered a match in WAIT_LEAVE): it names the length but no
	// winner, so it is only the fallback when no result packet arrives.
	completeInit   *bg.MatchInit
	completeInitAt time.Time
	// failReason is the last FAILED status reason text.
	failReason string
}

// bgInit returns the state, making it on first use.
func (s *session) bgInit() *bgState {
	if s.bg == nil {
		s.bg = &bgState{worlds: map[int32]int32{}}
	}
	return s.bg
}

// MaxBGWorldStates bounds the session's world-state map: a battleground init
// carries its map's dozen ids, and a hostile or buggy server cannot grow the
// map one update packet at a time past this.
const MaxBGWorldStates = 4096

// handleBG answers the battleground packets the watch reads. A packet this
// round's parser cannot consume exactly is counted (Report.ParseErrors) and
// merges nothing, like every other handler. Unknown BG opcodes the watch
// does not model (LIST, SET_STATE, CAPTURE_POINT_*) are parsed and counted,
// never merged: the fixture rule still proves the layout.
func (s *session) handleBG(p worldconn.Packet) error {
	s.capturePacket(p)
	st := s.bgInit()
	switch p.Core {
	case wire.SMSGBattlefieldStatusQueued:
		q, err := bg.ParseStatusQueued(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.opts.logf("world: %s: avg wait %ds, wait %ds", wire.ServerName(p.Core), q.AvgWait, q.Wait)
	case wire.SMSGBattlefieldStatusNeedConfirmation:
		c, err := bg.ParseStatusNeedConfirmation(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		c.Expires = time.Now().Add(time.Duration(c.Timeout) * time.Millisecond)
		st.invite = &c
		st.inviteExpires = c.Expires
		s.opts.logf("world: %s: map %d, instance %d, timeout %dms", wire.ServerName(p.Core), c.Map, c.Header.InstanceID, c.Timeout)
	case wire.SMSGBattlefieldStatusActive:
		a, err := bg.ParseStatusActive(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// The entry is confirmed: the queue and its invite window are gone.
		st.active = true
		st.invite = nil
		st.inviteExpires = time.Time{}
		s.opts.logf("world: %s: map %d, instance %d, elapsed %dms", wire.ServerName(p.Core), a.Map, a.Header.InstanceID, a.Start)
	case wire.SMSGBattlefieldStatusNone:
		if _, err := bg.ParseStatusNone(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// The status none after an invite means the server removed the
		// entry at the window's end; the invite stays for the watcher to
		// stop as expired (its Expires has passed) rather than waiting out
		// the whole queue budget.
	case wire.SMSGBattlefieldStatusFailed:
		f, err := bg.ParseStatusFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		st.failReason = fmt.Sprintf("the server refused the join (reason %d)", f.Reason)
		s.opts.logf("world: %s: reason %d", wire.ServerName(p.Core), f.Reason)
	case wire.SMSGInitWorldStates:
		w, err := bg.ParseInitWorldStates(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		st.worlds = w.States
	case wire.SMSGUpdateWorldState:
		u, err := bg.ParseUpdateWorldState(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		id := int32(u.ID)
		if _, known := st.worlds[id]; !known && len(st.worlds) >= MaxBGWorldStates {
			s.opts.logf("world: %s: the world-state map is full (%d ids), %d not merged", wire.ServerName(p.Core), MaxBGWorldStates, id)
			return nil
		}
		st.worlds[id] = u.Value
	case wire.SMSGPVPMatchInitialize:
		m, err := bg.ParseMatchInit(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if m.State == bg.MatchComplete && st.end == nil {
			// Entering a match in WAIT_LEAVE: the init names the length but
			// no winner. It is a fallback only, taken when the result packet
			// does not follow (Ended).
			st.completeInit = &m
			st.completeInitAt = time.Now()
		}
		s.opts.logf("world: %s: map %d state %d", wire.ServerName(p.Core), m.Map, uint8(m.State))
	case wire.SMSGPVPMatchComplete:
		m, err := bg.ParseMatchEnd(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if m.TailErr != nil {
			// The head (winner, duration) is valid; a tail layout detail
			// must not cost the match's result.
			s.report.ParseErrors++
			s.opts.logf("world: %s log data was not decoded (%v); the head still ends the watch", wire.ServerName(p.Core), m.TailErr)
		}
		end := &bg.WatchEnd{
			Winner:    bg.WinnerName(m.Winner),
			DurationS: float64(m.Duration),
		}
		if end.Winner != "" {
			end.Text = end.Winner + " wins"
		} else {
			// PVP_TEAM_NEUTRAL: the premature-finish timer's answer when
			// neither side met the minimum, or a draw.
			end.Premature = true
			end.Text = "no winner"
		}
		st.end = end
		st.completeInit = nil
		if m.Stats != nil {
			s.opts.logf("world: %s: %d player(s), %d/%d inside", wire.ServerName(p.Core), len(m.Stats.Players), m.Stats.Alliance, m.Stats.Horde)
		}
		s.opts.logf("world: %s: winner %d, %ds", wire.ServerName(p.Core), m.Winner, m.Duration)
	case wire.SMSGPVPMatchStatistics:
		st2, err := bg.ParseMatchStats(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.opts.logf("world: %s: %d player(s), %d/%d inside", wire.ServerName(p.Core), len(st2.Players), st2.Alliance, st2.Horde)
	case wire.SMSGBattlegroundPlayerPositions:
		if _, err := bg.ParsePlayerPositions(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
	case wire.SMSGBattlegroundPlayerJoined:
		g, err := bg.ParsePlayerJoined(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.opts.logf("world: %s: %s", wire.ServerName(p.Core), g)
	case wire.SMSGBattlegroundPlayerLeft:
		g, err := bg.ParsePlayerLeft(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.opts.logf("world: %s: %s", wire.ServerName(p.Core), g)
	case wire.SMSGAreaSpiritHealerTime:
		tm, err := bg.ParseSpiritTime(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.opts.logf("world: %s: %dms left", wire.ServerName(p.Core), tm.Left)
	case wire.SMSGPVPMatchSetState:
		if len(p.Payload) != 1 {
			s.noteParseError(p, fmt.Errorf("world: SMSG_PVP_MATCH_SET_STATE is %d bytes, want 1", len(p.Payload)))
			return nil
		}
		s.opts.logf("world: %s: state %d", wire.ServerName(p.Core), p.Payload[0])
	case wire.SMSGBattlefieldList:
		s.opts.logf("world: %s (%d byte(s))", wire.ServerName(p.Core), len(p.Payload))
	case wire.SMSGUpdateCapturePoint:
		if _, err := bg.ParseCapturePoint(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
	case wire.SMSGCapturePointRemoved:
		if _, err := bg.ParseCaptureRemoved(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
	}
	return nil
}

// isBGPacket reports the opcodes handleBG owns.
func isBGPacket(core uint32) bool {
	switch core {
	case wire.SMSGBattlefieldStatusNeedConfirmation, wire.SMSGBattlefieldStatusActive,
		wire.SMSGBattlefieldStatusQueued, wire.SMSGBattlefieldStatusNone,
		wire.SMSGBattlefieldStatusFailed, wire.SMSGBattlefieldList,
		wire.SMSGBattlegroundPlayerPositions, wire.SMSGUpdateCapturePoint,
		wire.SMSGCapturePointRemoved, wire.SMSGBattlegroundPlayerJoined,
		wire.SMSGBattlegroundPlayerLeft, wire.SMSGPVPMatchStatistics,
		wire.SMSGPVPMatchSetState, wire.SMSGPVPMatchComplete,
		wire.SMSGPVPMatchInitialize, wire.SMSGAreaSpiritHealerTime,
		wire.SMSGInitWorldStates, wire.SMSGUpdateWorldState:
		return true
	}
	return false
}

// bgDied records the watcher's own death: the world's Deaths() drain fills
// it and the watcher reads it once per poll. A death of any other unit in
// view is not the watcher's.
func (s *session) bgDied() {
	s.bgInit().deaths++
}

// bgPorts adapts the session to the watcher's Ports seam: sends through the
// instance socket, status through the command channel's `.tb bg status`,
// world states and the watcher's own death from the decoded model, and Sleep
// that answers server packets meanwhile (the dungeon clock's precedent).
type bgPorts struct {
	s *session
}

// Send writes one client packet on the instance socket.
func (b bgPorts) Send(core uint32, payload []byte) error {
	return b.s.Send(core, payload)
}

// Status polls `.tb bg status`: the per-side humans/bots and per-bot rows
// the record keeps, plus deaths and deserter sightings. It is the one
// module read the watch sends during the match (no orders); the line is
// built by the typed client like every other module read (tbcmd.BGStatus),
// so the adapter's only path to the command channel stays the typed send.
func (b bgPorts) Status() (bg.StatusReport, error) {
	client := tbcmd.NewClient(command.NewSession(&cmdLink{s: b.s}))
	client.Base = b.s.commandOptions(command.Options{})
	rep, err := client.BGStatus(b.s.ctx)
	if err != nil {
		return bg.StatusReport{}, bgStatusError(err)
	}
	return bgStatusFromTyped(rep), nil
}

// bgStatusError tells a refused status poll (a reply without the module's BG
// block: a server without the module, or with it off) from a broken link.
func bgStatusError(err error) error {
	if errors.Is(err, tbcmd.ErrBadReport) {
		return bg.ErrStatusRefused
	}
	return err
}

// bgStatusFromTyped adapts the typed status report to the watcher's ports.
func bgStatusFromTyped(rep tbcmd.BGStatusReport) bg.StatusReport {
	out := bg.StatusReport{
		Team: rep.Team, Map: rep.Map, Status: rep.Status,
		ScoreA: rep.ScoreA, ScoreH: rep.ScoreH,
		FCEscorts: rep.FCEscorts,
		Deserter:  rep.Deserter, Fallback: rep.Fallback,
		HasFC: rep.HasFC, FCX: float32(rep.FCX), FCY: float32(rep.FCY),
	}
	out.Alliance = bg.Side{Humans: rep.AllH, Bots: rep.AllB, Deaths: rep.DeathsA}
	out.Horde = bg.Side{Humans: rep.HordeH, Bots: rep.HordeB, Deaths: rep.DeathsH}
	for _, row := range rep.Bots {
		out.Bots = append(out.Bots, bg.BotRow{
			Role: row.Role, Objective: row.Objective, Node: row.Node,
			Alive: row.Alive, MoveAgeS: row.MoveAgeS,
		})
	}
	return out
}

// WorldStates returns the world-state map the session decoded so far.
func (b bgPorts) WorldStates() map[int32]int32 {
	st := b.s.bgInit()
	return st.worlds
}

// SelfPos returns the watcher's own position: the mover's, or the last
// client position when there is no mover yet.
func (b bgPorts) SelfPos() (x, y, z float32) {
	if b.s.mover != nil {
		px, py, pz, _ := b.s.mover.Position()
		return px, py, pz
	}
	p := b.s.report.WalkClientPos
	return p[0], p[1], p[2]
}

// Refused is the last join refusal the session decoded (a FAILED status).
func (b bgPorts) Refused() string { return b.s.bgInit().failReason }

// Died says the watcher itself died since the last poll.
func (b bgPorts) Died() bool {
	st := b.s.bgInit()
	if st.deaths > st.diedSince {
		st.diedSince = st.deaths
		return true
	}
	return false
}

// PollStatus sends CMSG_REQUEST_BATTLEFIELD_STATUS (an empty packet: the
// server answers the queued/invited/active status;
// HandleRequestBattlefieldStatusOpcode, BattleGroundHandler.cpp:588-646).
func (b bgPorts) PollStatus() error {
	return b.Send(wire.CMSGRequestBattlefieldStatus, bg.BuildRequestBattlefieldStatus())
}

// Sleep waits between polls, answering server packets meanwhile.
func (b bgPorts) Sleep(d time.Duration) error {
	return b.s.collect(b.s.ctx, d)
}

// Invite hands the watcher its decoded invite (NEED_CONFIRMATION) with the
// accept window still open: Expires is filled from the packet's arrival, so
// a late poll sees the time that is left, not the first packet's window.
func (b bgPorts) Invite(time.Time) (bg.ConfirmStatus, bool) {
	st := b.s.bgInit()
	if st.invite == nil {
		return bg.ConfirmStatus{}, false
	}
	c := *st.invite
	c.Expires = st.inviteExpires
	return c, true
}

// Active says the server confirmed the queue entry (SMSG_BATTLEFIELD_STATUS_
// ACTIVE): the join event and the match observation wait for it.
func (b bgPorts) Active() bool { return b.s.bgInit().active }

// bgCompleteInitGrace is how long a complete-state initialize waits for the
// authoritative PVP_MATCH_COMPLETE before it is used as the fallback: the
// result packet goes to the players inside when the match ends, so a
// character that entered in WAIT_LEAVE never gets one.
const bgCompleteInitGrace = 15 * time.Second

// Ended hands the watcher the match end: the authoritative
// PVP_MATCH_COMPLETE, or, after the grace with no result packet, the length
// a complete-state initialize carried (winner unknown).
func (b bgPorts) Ended() (bg.WatchEnd, bool) {
	st := b.s.bgInit()
	if st.end != nil {
		return *st.end, true
	}
	if st.completeInit != nil && time.Since(st.completeInitAt) >= bgCompleteInitGrace {
		return bg.WatchEnd{
			DurationS: float64(st.completeInit.Duration),
			NoResult:  true,
			Text:      "match complete (initialize state, no result packet)",
		}, true
	}
	return bg.WatchEnd{}, false
}

// bgWatch is the session side of `forever bg-watch`: settle, confirm GM (the
// queue join needs no GM, but the run's own probes do), then the watch
// machine over bgPorts. It sends the queue join and the port accept as typed
// client packets and otherwise reads, except the `.tb bg status` polls.
func (s *session) bgWatch(ctx context.Context, cfg *bg.WatcherConfig) (*bg.Result, error) {
	if cfg == nil {
		return nil, nil
	}
	c := *cfg
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = dungeonSettle
	}
	if err := s.collect(ctx, settle); err != nil {
		return nil, err
	}
	// The watch sends CMSG_BATTLEMASTER_JOIN through the typed queue id,
	// like the dungeon watch's typed sends (dungeon_session.go); the GM
	// probe stays because the run's own `.tb bg status` polls need the
	// command channel's permission story told before anything is sent.
	if err := s.confirmGM(ctx); err != nil {
		return nil, err
	}
	w := bg.NewWatcher(c, bgPorts{s: s}, s.opts.logf)
	deadline := time.Now().Add(time.Duration(c.MaxMin+5) * time.Minute)
	res := w.Run(deadline)
	s.opts.logf("bg-watch: stop %s, %d event(s), winner %s, clear=%v", res.Stop, len(res.Record.Events), res.Record.Winner, res.Clear)
	return &res, nil
}
