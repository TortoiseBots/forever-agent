package world

// What a serving session does between calls: it keeps the realm socket alive and learns the names of the players
// that come into view. Both are things a real client does without being asked, and a daemon that sits idle for
// minutes needs both.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// serveRealmPingEvery is how often a serving session pings the realm socket. The server sends its time syncs on the
// instance socket only, so once the login burst is over the realm socket carries nothing unless the client pings:
// the socket's 60 s read deadline then ended an idle session ("reading a frame header: i/o timeout"). The real client
// pings every 30 s, and the core drops a socket that pings faster than every 27 s (WorldSocket::HandlePing), so this
// is the real schedule and no faster. It is a variable so a test can shorten it.
var serveRealmPingEvery = 30 * time.Second

// serveRealmPing sends the realm socket's ping when its turn has come. The pong reads as traffic on that socket. A
// failed send is not handled here: a dead link is seen by the loop on its next pass.
func (s *session) serveRealmPing() {
	now := time.Now()
	if now.Before(s.realmPingAt) {
		return
	}
	s.realmPingAt = now.Add(serveRealmPingEvery)
	if s.realm == nil && s.fightSender == nil {
		return
	}
	s.pingSerial++
	s.pingSentAt = now
	if err := s.sendOn(sideRealm, wire.CMSGPing, BuildPing(s.pingSerial, uint32(s.report.Latency.Milliseconds()))); err != nil {
		return
	}
	s.report.Pings++
}

// serveAskNames asks the server for the names of the players in view that this client does not have, once per
// backoff and never waiting for the answer (the packet handler applies it). The login asks once for everyone then in
// view; without this a player who arrives later keeps an empty name, so a follow cannot find its leader.
func (s *session) serveAskNames() {
	s.askVisibleNames(driver.Vec3{s.MoverX(), s.MoverY(), s.MoverZ()})
}

// serveIdleTick is the serve loop's work between calls.
func (s *session) serveIdleTick() {
	s.serveRealmPing()
	s.serveAskNames()
}
