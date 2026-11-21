package world

// The core's GM commands that print nothing when they work, and the teleports among them.
//
// Most GM commands answer a success with a PSendSysMessage, so a reply with no line at all used to be the error "the
// server sent no reply". A good few do not (checked against the core's own handlers, cs_go.cpp, cs_tele.cpp,
// cs_misc.cpp, cs_character.cpp, cs_learn.cpp and cs_npc.cpp):
//
//	.go xyz / creature / zonexy / ...   DoTeleport and the handlers around it print only a refusal
//	.tele <place>                       HandleTeleCommand prints only a refusal (`.tele name`/`group` do print)
//	.aura, .unaura (and their self forms), .revive, .respawn, .die, .damage, .cast
//	.levelup (self), .learn, .npc add temp, .npc evade
//
// (`.modify hp|mana|rage|energy` is not on the list: it prints.) For such a line the refusal is the only text there
// can be, so silence is accepted only when nothing can still arrive: the run lasts to its deadline with no line
// (command.Options.Silent), or the effect is seen first (command.Options.Confirm). Any line the server does send is
// an error carrying it. A teleport's effect is the character's own position or the new-world packet, never a chat
// reply (teleportArrived).

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

type gmLineClass int

const (
	gmReplies  gmLineClass = iota // prints a line on success: the reply is the proof
	gmSilent                      // prints nothing on success
	gmTeleport                    // prints nothing on success and moves the character
)

// teleportGoal is where a teleport line says the character will stand, when the line says it. hasMap and hasZ say
// whether the line named a map and a height: map 0 is a real map, so no value stands for "not given".
type teleportGoal struct {
	known  bool
	hasMap bool
	hasZ   bool
	mapID  uint32
	x, y   float32
	z      float32
}

// classifyGMLine names how a GM line answers success. An unknown line is gmReplies, the strict default.
func classifyGMLine(line string) (gmLineClass, teleportGoal) {
	line = strings.TrimSpace(line)
	if line == "" || (line[0] != '.' && line[0] != '!') {
		return gmReplies, teleportGoal{}
	}
	f := strings.Fields(strings.ToLower(line[1:]))
	if len(f) == 0 {
		return gmReplies, teleportGoal{}
	}
	switch f[0] {
	case "go":
		if len(f) >= 4 && f[1] == "xyz" {
			if g, ok := parseGoXYZ(f[2:]); ok {
				return gmTeleport, g
			}
			return gmReplies, teleportGoal{} // a malformed line is answered by the core's own syntax text
		}
		if len(f) >= 2 && f[1] == "ticket" {
			return gmReplies, teleportGoal{}
		}
		return gmTeleport, teleportGoal{}
	case "tele":
		if len(f) >= 2 && (f[1] == "name" || f[1] == "group") {
			return gmReplies, teleportGoal{}
		}
		if len(f) >= 2 {
			return gmTeleport, teleportGoal{}
		}
	case "aura", "unaura", "revive", "respawn", "die", "damage", "levelup", "learn", "cast":
		return gmSilent, teleportGoal{}
	case "npc":
		if len(f) >= 3 && f[1] == "add" && f[2] == "temp" || len(f) >= 2 && f[1] == "evade" {
			return gmSilent, teleportGoal{}
		}
	}
	return gmReplies, teleportGoal{}
}

// parseGoXYZ reads `x y [z [map [o]]]` of `.go xyz` (the core takes z as optional, cs_go.cpp HandleGoXYZCommand: with
// no z it uses the ground height there, so the goal is then the point in two dimensions). A line it cannot read is
// left to the core's own syntax answer.
func parseGoXYZ(a []string) (teleportGoal, bool) {
	if len(a) < 2 {
		return teleportGoal{}, false
	}
	x, err1 := strconv.ParseFloat(a[0], 32)
	y, err2 := strconv.ParseFloat(a[1], 32)
	if err1 != nil || err2 != nil {
		return teleportGoal{}, false
	}
	g := teleportGoal{known: true, x: float32(x), y: float32(y)}
	if len(a) >= 3 {
		z, err := strconv.ParseFloat(a[2], 32)
		if err != nil {
			return teleportGoal{}, false
		}
		g.hasZ, g.z = true, float32(z)
	}
	if len(a) >= 4 {
		m, err := strconv.ParseUint(a[3], 10, 32)
		if err != nil {
			return teleportGoal{}, false
		}
		g.hasMap, g.mapID = true, uint32(m)
	}
	return g, true
}

// teleportConfirmWait bounds the wait for the character to arrive. A variable so the tests shorten it.
var teleportConfirmWait = 8 * time.Second

// silentLineWait is how long a silent line (.aura, .revive, .cast, ...) waits for a refusal that may still come.
// A variable so the tests shorten it.
var silentLineWait = 3 * time.Second

// teleportArriveYd is how close to the named point the character must stand to have arrived.
const teleportArriveYd = 2.0

// teleportMark is what the session believed before a teleport line went out.
type teleportMark struct {
	selfTeleports int
	epoch         uint64
	mapID         uint32
}

func (s *session) teleportMark() teleportMark {
	return teleportMark{selfTeleports: s.selfTeleports, epoch: s.mapEpoch(), mapID: s.world.MapID()}
}

// teleportArrived is the proof of a teleport line: the character stands within teleportArriveYd of the point the
// line named, on the map it named (the current one when it named none), or, for a line that names no point, the
// server moved this character (its own SMSG_MOVE_TELEPORT) or sent a new world. Another unit's teleport and a walk
// of a yard are not proof.
func (s *session) teleportArrived(before teleportMark, g teleportGoal) bool {
	if s.betweenMaps() {
		return false
	}
	if g.known {
		want := before.mapID
		if g.hasMap {
			want = g.mapID
		}
		if s.world.MapID() != want {
			return false
		}
		dx, dy := float64(s.MoverX()-g.x), float64(s.MoverY()-g.y)
		d := math.Hypot(dx, dy)
		if g.hasZ {
			d = math.Hypot(d, float64(s.MoverZ()-g.z))
		}
		return d <= teleportArriveYd
	}
	return s.selfTeleports > before.selfTeleports || s.mapEpoch() != before.epoch
}

// gmLinePrep checks the cast pace and fills the collector's options for one GM line: a silent-class line is run
// with command.Options.Silent, and a teleport also with a Confirm of the character's position. The returned func
// finishes the line once it ran.
func (s *session) gmLinePrep(line string, o command.Options) (command.Options, func(*command.Reply, error) error, error) {
	if err := s.checkGMCastPace(line); err != nil {
		return o, nil, err
	}
	class, goal := classifyGMLine(line)
	done := func(rep *command.Reply, err error) error {
		if rep != nil {
			s.noteGMCastSent(line)
		}
		if err != nil || class == gmReplies || rep == nil {
			return err
		}
		if class == gmTeleport {
			if rep.End == "confirmed" {
				return nil
			}
			return fmt.Errorf("%q: the teleport is not confirmed: the character did not arrive within %s (at %.1f %.1f on map %d)%s",
				line, teleportConfirmWait, s.MoverX(), s.MoverY(), s.world.MapID(), answeredText(rep))
		}
		if len(rep.Raw) != 0 {
			return fmt.Errorf("%q: the server answered a command that prints nothing when it works: %s", line, strings.Join(rep.Raw, " | "))
		}
		return nil
	}
	if class == gmReplies {
		return o, done, nil
	}
	o.Silent = true
	if class == gmSilent && (o.Deadline <= 0 || o.Deadline > silentLineWait) {
		// Silence can only be trusted once a refusal had its time: long enough for a loaded core, far shorter than
		// the deadline of a line that has to reply.
		o.Deadline = silentLineWait
	}
	if class == gmTeleport {
		mark := s.teleportMark()
		o.Confirm = func() bool { return s.teleportArrived(mark, goal) }
		if o.Deadline <= 0 {
			o.Deadline = teleportConfirmWait
		}
	}
	return o, done, nil
}

// answeredText is the server's own words of a reply, for an error that has to carry them.
func answeredText(rep *command.Reply) string {
	if rep == nil || len(rep.Raw) == 0 {
		return ""
	}
	return ": " + strings.Join(rep.Raw, " | ")
}

// GM casts are paced. The live core-gains run lost the character's world connection (the server closed the socket:
// `reading a frame header: EOF`, and every reconnect was answered `unknown account`) after about fourteen
// `.cast <spell> triggered` lines at one selected mob in a short time, with the server itself still up. The core's own
// per-opcode packet limit does not name chat (WorldSession::DosProtection leaves CMSG_CHAT_MESSAGE_SAY unlimited), and
// the agent had not closed the socket, so the cause is on the server side and the evidence cannot tell more. A burst
// of triggered casts is the one pattern that run shows, so the lines go out at a measured pace: at most gmCastBurst
// `.cast` lines in any gmCastWindow. A line over the pace is refused at once with the time to wait, never held, so
// the serving loop and every call behind it keep running.
var (
	gmCastBurst  = 4
	gmCastWindow = 8 * time.Second
)

// isGMCast reports whether a GM line is the core's `.cast` command, in the abbreviations the core accepts (a prefix
// of "cast" of three letters or more).
func isGMCast(line string) bool {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(line)))
	if len(f) == 0 || len(f[0]) < 4 || (f[0][0] != '.' && f[0][0] != '!') {
		return false
	}
	w := f[0][1:]
	return len(w) >= 3 && strings.HasPrefix("cast", w)
}

// checkGMCastPace refuses a `.cast` line sent over the pace, naming how long to wait.
func (s *session) checkGMCastPace(line string) error {
	if !isGMCast(line) || gmCastBurst <= 0 {
		return nil
	}
	now := time.Now()
	keep := s.gmCastAt[:0]
	for _, at := range s.gmCastAt {
		if now.Sub(at) < gmCastWindow {
			keep = append(keep, at)
		}
	}
	s.gmCastAt = keep
	if len(s.gmCastAt) < gmCastBurst {
		return nil
	}
	wait := gmCastWindow - now.Sub(s.gmCastAt[0])
	return fmt.Errorf("gm: %d .cast lines went out in the last %s; the next is allowed in %s", len(s.gmCastAt), gmCastWindow, wait.Round(100*time.Millisecond))
}

// noteGMCastSent records a `.cast` line that was sent.
func (s *session) noteGMCastSent(line string) {
	if isGMCast(line) {
		s.gmCastAt = append(s.gmCastAt, time.Now())
	}
}
