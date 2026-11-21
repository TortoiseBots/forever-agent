// plan_scout_allow.go: the strict allow-list of the scout's live pass (spec 5.6). Every GM line and every toolkit line
// is checked here before it is sent: the scout moves its own GM, teleports nowhere else, hires nothing, orders no bot.
// The GM lines are parsed into tokens with a strict grammar and re-rendered canonically, so the line the server reads is
// the line the allow-list judged: no tabs or doubled spaces, no exponent, hex, inf/nan or leading-zero numbers, no extra
// field, and a map of 0 only when the plan itself is on map 0.
package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/raidwalk"
)

var (
	// scoutIDRe is the module's request id: #<1-9 digits>.
	scoutIDRe = regexp.MustCompile(`^#[1-9][0-9]{0,8}$`)
	// scoutGUIDRe is a guid as the toolkit's findByHex takes it: 0x and 1-16 hex digits.
	scoutGUIDRe = regexp.MustCompile(`^0[xX][0-9A-Fa-f]{1,16}$`)
	// scoutNumberRe is the strict form of a coordinate or radius: an optional minus, decimal digits with no leading
	// zero, and an optional fraction. That refuses what strconv.ParseFloat would also take: 1e3, 0x1p4, +5, 1_0,
	// .5, 5., inf and nan.
	scoutNumberRe = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
	// scoutMapRe is a map id: decimal digits with no leading zero, so two spellings of one map cannot both pass.
	scoutMapRe = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,9})$`)
)

// scoutNumberMax bounds a coordinate, a radius and a map id the scout may name: WoW's coordinates are within ±17 000
// and its map ids below 2 000, so a larger number is a mistake, never a place.
const scoutNumberMax = 1e7

// scoutNum parses one number with the strict grammar and returns it only when it is finite and in range.
func scoutNum(s string) (float64, bool) {
	if !scoutNumberRe.MatchString(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > scoutNumberMax {
		return 0, false
	}
	if v == 0 {
		return 0, true // -0 and 0 are one number; the canonical form is 0
	}
	return v, true
}

// scoutFormatNum renders a number in the one canonical form the scout sends: decimals only, no trailing zeros, no
// exponent, and 0 for both zeros.
func scoutFormatNum(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// scoutMapOK is the map rule of a `.go xyz` and a `.instance unbind`: the plan's map or the portal's, and map 0 only
// when the plan itself is on map 0 (0 is an unset map everywhere else, never Eastern Kingdoms by accident).
func scoutMapOK(m, planMap, portalMap uint32) bool {
	if m == 0 && planMap != 0 {
		return false
	}
	return m == planMap || (portalMap != 0 && m == portalMap)
}

// scoutMapID parses a map id with the strict grammar.
func scoutMapID(s string) (uint32, bool) {
	if !scoutMapRe.MatchString(s) {
		return 0, false
	}
	m, err := strconv.ParseUint(s, 10, 32)
	if err != nil || m > uint64(scoutNumberMax) {
		return 0, false
	}
	return uint32(m), true
}

// scoutCanonicalGM parses line with the allow-list's strict grammar and returns the canonical text to send. ok is false
// for every line outside the allow-list, including one whose words would parse but whose spacing is not canonical.
func scoutCanonicalGM(line string, planMap, portalMap uint32) (string, bool) {
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", false
	}
	f := strings.Fields(line)
	if len(f) == 0 || strings.Join(f, " ") != line {
		return "", false
	}
	switch line {
	case ".gm on", raidwalk.GMVisibleOff, ".instance unbind all":
		return line, true
	}
	switch f[0] {
	case ".go":
		// .go xyz <x> <y> <z> <map>
		if len(f) != 6 || f[1] != "xyz" {
			return "", false
		}
		nums, ok := scoutCanonNums(f[2:5])
		if !ok {
			return "", false
		}
		m, ok := scoutMapID(f[5])
		if !ok || !scoutMapOK(m, planMap, portalMap) {
			return "", false
		}
		return fmt.Sprintf(".go xyz %s %s %s %d", nums[0], nums[1], nums[2], m), true
	case ".instance":
		// .instance unbind <plan map>
		if len(f) != 3 || f[1] != "unbind" {
			return "", false
		}
		m, ok := scoutMapID(f[2])
		if !ok || m != planMap || !scoutMapOK(m, planMap, portalMap) {
			return "", false
		}
		return fmt.Sprintf(".instance unbind %d", m), true
	case ".bot":
		return scoutCanonBot(f[1:])
	}
	return "", false
}

// scoutCanonBot parses `.bot scout <form>` and returns the canonical tail.
func scoutCanonBot(f []string) (string, bool) {
	if len(f) < 3 || f[0] != "scout" {
		return "", false
	}
	switch f[1] {
	case "cfg", "where":
		if len(f) != 3 || !scoutIDRe.MatchString(f[2]) {
			return "", false
		}
		return ".bot scout " + f[1] + " " + f[2], true
	case "walk":
		if len(f) != 6 || !scoutIDRe.MatchString(f[2]) {
			return "", false
		}
		nums, ok := scoutCanonNums(f[3:6])
		if !ok {
			return "", false
		}
		return fmt.Sprintf(".bot scout walk %s %s %s %s", f[2], nums[0], nums[1], nums[2]), true
	case "path":
		if len(f) != 6 && len(f) != 10 {
			return "", false
		}
		if !scoutIDRe.MatchString(f[2]) {
			return "", false
		}
		nums, ok := scoutCanonNums(f[3:6])
		if !ok {
			return "", false
		}
		line := fmt.Sprintf(".bot scout path %s %s %s %s", f[2], nums[0], nums[1], nums[2])
		if len(f) == 6 {
			return line, true
		}
		if f[6] != "from" {
			return "", false
		}
		from, ok := scoutCanonNums(f[7:10])
		if !ok {
			return "", false
		}
		return line + fmt.Sprintf(" from %s %s %s", from[0], from[1], from[2]), true
	}
	return "", false
}

// scoutCanonNums parses numbers and renders them canonically, or refuses the whole group.
func scoutCanonNums(ss []string) ([]string, bool) {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		v, ok := scoutNum(s)
		if !ok {
			return nil, false
		}
		out = append(out, scoutFormatNum(v))
	}
	return out, true
}

// scoutAllowedLine says whether the scout may send line through the gm RPC: `.gm on`, `.gm visible off`, a `.go xyz` on
// the plan's map or the portal's map, `.instance unbind <plan map>` and `.bot scout cfg|path|walk|where` with an id and
// finite numbers. Everything else (`.die`, `.tele`, `.bot run`, `.bot cmd`, `.appear`, `.summon`, a hire) is refused.
func scoutAllowedLine(line string, planMap, portalMap uint32) bool {
	_, ok := scoutCanonicalGM(line, planMap, portalMap)
	return ok
}

// scoutAllowedTT says whether the scout may send cmd to the Eluna test toolkit: `q inst`, a `q units` read of at most 60
// yd around a point, or `q los` with 1-8 hex guids. One line, no `#` (the agent adds the id), at most the raid-walk cap.
func scoutAllowedTT(cmd string, planMap uint32) bool {
	if cmd == "" || len(cmd) > raidwalk.AllowedTTMax || strings.ContainsAny(cmd, "\r\n#") {
		return false
	}
	f := strings.Fields(cmd)
	if len(f) == 0 || strings.Join(f, " ") != cmd {
		return false
	}
	switch f[0] {
	case "q":
		if len(f) < 2 {
			return false
		}
		switch f[1] {
		case "inst":
			return len(f) == 2
		case "units":
			// q units <r<=60> <x> <y> <z> all
			if len(f) != 7 || f[6] != "all" || !scoutNumbers(f[3:6]) {
				return false
			}
			r, ok := scoutNum(f[2])
			return ok && r >= 1 && r <= 60
		case "los":
			// q los <x> <y> <z> <0xGUID>... (1-8 guids)
			if len(f) < 6 || len(f) > 13 || !scoutNumbers(f[2:5]) {
				return false
			}
			for _, g := range f[5:] {
				if !scoutGUIDRe.MatchString(g) {
					return false
				}
			}
			return true
		}
	}
	_ = planMap
	return false
}

// scoutNumbers says every word is a number the strict grammar takes.
func scoutNumbers(ss []string) bool {
	for _, s := range ss {
		if _, ok := scoutNum(s); !ok {
			return false
		}
	}
	return true
}
