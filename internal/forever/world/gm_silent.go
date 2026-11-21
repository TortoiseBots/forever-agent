package world

// The core's GM commands that print nothing when they work, and the teleports among them.
//
// A tokenless command ends when its reply goes quiet, and a reply with no line at all used to be the error "the
// server sent no reply". Most GM commands answer a success with a PSendSysMessage, so that was a fair rule; but a
// good few do not (checked against the core's own handlers, ForeverCore cs_go.cpp, cs_tele.cpp, cs_misc.cpp,
// cs_character.cpp, cs_learn.cpp, cs_modify.cpp and cs_npc.cpp):
//
//	.go xyz / creature / zonexy / ...   DoTeleport and the handlers around it print only a refusal
//	.tele <place>                       HandleTeleCommand prints only a refusal (`.tele name`/`group` do print)
//	.aura, .unaura (and their self forms), .revive, .respawn, .die, .damage, .cast
//	.levelup, .learn, .modify hp|mana|rage|energy, .npc add temp, .npc evade
//
// Those lines ask the collector for AllowEmpty: a quiet window with no line is the success. A refusal, a say echo
// (no permission), an unknown command and a syntax answer still fail as before.
//
// A teleport is also never confirmed by chat. The proof is the character's own position: the move or new-world
// packet the server sends, read back from the mover within a deadline (confirmTeleport).

import (
	"context"
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

// teleportGoal is where a teleport line says the character will stand, when the line says it.
type teleportGoal struct {
	known bool
	mapID uint32 // 0 with known true means "the map the character is on"
	x, y  float32
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
			return gmTeleport, teleportGoal{}
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
	case "modify":
		if len(f) >= 2 {
			switch f[1] {
			case "hp", "mana", "rage", "energy":
				return gmSilent, teleportGoal{}
			}
		}
	case "npc":
		if len(f) >= 3 && f[1] == "add" && f[2] == "temp" || len(f) >= 2 && f[1] == "evade" {
			return gmSilent, teleportGoal{}
		}
	}
	return gmReplies, teleportGoal{}
}

// parseGoXYZ reads `x y [z [map [o]]]` of `.go xyz`.
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
	if len(a) >= 4 {
		m, err := strconv.ParseUint(a[3], 10, 32)
		if err != nil {
			return teleportGoal{}, false
		}
		g.mapID = uint32(m)
	}
	return g, true
}

// gmLineOptions is the collector's options for one GM line: a silent-success command may end quietly with no line.
func gmLineOptions(line string, o command.Options) command.Options {
	if class, _ := classifyGMLine(line); class != gmReplies && o.Token == "" {
		o.AllowEmpty = true
	}
	return o
}

// teleportConfirmWait bounds the wait for the character to arrive. A variable so the tests shorten it.
var teleportConfirmWait = 8 * time.Second

const (
	// teleportArriveYd is how close to a named point the character must stand to have arrived; the core may nudge a
	// point onto the ground.
	teleportArriveYd = 3.0
	// teleportMovedYd is how far a teleport with no named point must have moved the character.
	teleportMovedYd = 1.0
)

// teleportMark is what the session believed before a teleport line went out.
type teleportMark struct {
	teleports int
	epoch     uint64
	mapID     uint32
	x, y      float32
}

func (s *session) teleportMark() teleportMark {
	return teleportMark{teleports: s.report.Teleports, epoch: s.mapEpoch(), mapID: s.world.MapID(), x: s.MoverX(), y: s.MoverY()}
}

// confirmTeleport waits for the proof of a teleport line that printed nothing: the server's own move or new-world
// packet, or the position read back, within teleportConfirmWait. Chat is never the proof.
func (s *session) confirmTeleport(ctx context.Context, line string, before teleportMark, goal teleportGoal) error {
	deadline := time.Now().Add(teleportConfirmWait)
	for {
		if !s.betweenMaps() {
			x, y := s.MoverX(), s.MoverY()
			if goal.known {
				want := goal.mapID
				if want == 0 {
					want = before.mapID
				}
				if s.world.MapID() == want && math.Hypot(float64(x-goal.x), float64(y-goal.y)) <= teleportArriveYd {
					return nil
				}
			} else if s.report.Teleports > before.teleports || s.mapEpoch() != before.epoch ||
				math.Hypot(float64(x-before.x), float64(y-before.y)) > teleportMovedYd {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%q: the teleport is not confirmed: the character did not arrive within %s (at %.1f %.1f on map %d)",
				line, teleportConfirmWait, s.MoverX(), s.MoverY(), s.world.MapID())
		}
		if err := s.collect(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}
