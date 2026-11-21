// Package gmwalk records a GM character's walk as world data: the walked path as road polylines (thinned,
// with the map id), points the GM marks, and danger zones as circles. The GM marks by whispering (or saying
// nearby) plain words the recorder watches for, so no server change is needed. The output carries a version
// field, map ids and coordinates in server units (yards, WoW coordinates), and no character names: point and
// zone names are the GM's labels, never who walked or who marked.
package gmwalk

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Version is the only world-data format version this package reads and writes.
const Version = 1

// Bounds. Every input is bounded; a broken record is refused, never panics.
const (
	// MaxNameLen bounds point and zone names.
	MaxNameLen = 64
	// MaxTextLen bounds one mark line before parsing.
	MaxTextLen = 256
	// MinRadiusYd and MaxRadiusYd bound a danger zone's radius in yards.
	MinRadiusYd = 1.0
	MaxRadiusYd = 500.0
	// MaxCoord bounds one coordinate in yards; the world is far smaller.
	MaxCoord = 100000.0
	// MaxSamples bounds the samples one recording holds.
	MaxSamples = 100000
	// MaxRoads bounds the road polylines one recording splits into.
	MaxRoads = 256
	// MaxRoadPoints bounds one thinned road polyline.
	MaxRoadPoints = 4096
	// MaxMarks bounds the points plus zones one recording holds.
	MaxMarks = 1024
	// MaxDataBytes bounds a world-data or dry-recording file on disk.
	MaxDataBytes = 8 << 20
	// SplitJumpYd starts a new road when two consecutive samples stand this far apart: the walk teleported
	// rather than walked.
	SplitJumpYd = 200.0
)

// WorldData is one recorded walk: roads the path thins into, points the GM marked, danger zones as circles.
// Coordinates are server units (yards, WoW coordinates). Names are the GM's labels.
type WorldData struct {
	Version int     `json:"version"`
	Roads   []Road  `json:"roads"`
	Points  []Point `json:"points"`
	Zones   []Zone  `json:"zones"`
}

// Road is one walked stretch on one map: the thinned path polyline.
type Road struct {
	Map    uint32       `json:"map"`
	Points [][3]float32 `json:"points"`
}

// Point is one marked point of interest.
type Point struct {
	Name string  `json:"name"`
	Map  uint32  `json:"map"`
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	Z    float32 `json:"z"`
}

// Zone is one marked danger circle.
type Zone struct {
	Name   string  `json:"name"`
	Map    uint32  `json:"map"`
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	Radius float32 `json:"radius_yd"`
}

// Validate refuses hostile or corrupt data: a wrong version, counts past the bounds, bad names, radii,
// coordinates that are not finite or past the world bound. Unknown JSON fields are refused at decode time.
func Validate(d WorldData) error {
	if d.Version != Version {
		return fmt.Errorf("gmwalk: version %d is not version %d", d.Version, Version)
	}
	if len(d.Roads) > MaxRoads {
		return fmt.Errorf("gmwalk: %d roads pass the %d bound", len(d.Roads), MaxRoads)
	}
	if len(d.Points)+len(d.Zones) > MaxMarks {
		return fmt.Errorf("gmwalk: %d marks pass the %d bound", len(d.Points)+len(d.Zones), MaxMarks)
	}
	for i, r := range d.Roads {
		if len(r.Points) > MaxRoadPoints {
			return fmt.Errorf("gmwalk: road %d has %d points past the %d bound", i, len(r.Points), MaxRoadPoints)
		}
		for j, p := range r.Points {
			if err := checkCoord(p[0], p[1], p[2]); err != nil {
				return fmt.Errorf("gmwalk: road %d point %d: %w", i, j, err)
			}
		}
	}
	for i, p := range d.Points {
		if err := checkName(p.Name); err != nil {
			return fmt.Errorf("gmwalk: point %d: %w", i, err)
		}
		if err := checkCoord(p.X, p.Y, p.Z); err != nil {
			return fmt.Errorf("gmwalk: point %d: %w", i, err)
		}
	}
	for i, z := range d.Zones {
		if err := checkName(z.Name); err != nil {
			return fmt.Errorf("gmwalk: zone %d: %w", i, err)
		}
		if err := checkCoord(z.X, z.Y, z.Z); err != nil {
			return fmt.Errorf("gmwalk: zone %d: %w", i, err)
		}
		if math.IsNaN(float64(z.Radius)) || float64(z.Radius) < MinRadiusYd || float64(z.Radius) > MaxRadiusYd {
			return fmt.Errorf("gmwalk: zone %d: radius %v yd is outside %.0f-%.0f", i, z.Radius, MinRadiusYd, MaxRadiusYd)
		}
	}
	return nil
}

func checkCoord(x, y, z float32) error {
	for _, c := range [3]float32{x, y, z} {
		f := float64(c)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("coordinate is not finite")
		}
		if f > MaxCoord || f < -MaxCoord {
			return fmt.Errorf("coordinate %v is past the %.0f bound", c, MaxCoord)
		}
	}
	return nil
}

// nameChars is what a mark name may hold: one path component, safe for file stems and SQL comments.
const nameChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-"

func checkName(name string) error {
	if name == "" || len(name) > MaxNameLen {
		return fmt.Errorf("name %q is empty or past %d bytes", name, MaxNameLen)
	}
	if strings.Contains(name, "..") || name[0] == '.' || name[0] == '-' {
		return fmt.Errorf("name %q must not start with . or - or hold ..", name)
	}
	if strings.Trim(name, nameChars) != "" {
		return fmt.Errorf("name %q holds characters outside [A-Za-z0-9._-]", name)
	}
	return nil
}

// Mark kinds.
const (
	MarkPoint = "point"
	MarkZone  = "zone"
	MarkSplit = "split"
)

// Mark is one parsed GM word: a point, a zone, or a road split.
type Mark struct {
	Kind   string
	Name   string
	Radius float64
}

var errUnknownMark = errors.New("not a mark (want \"point <name>\", \"zone <name> <radius-yd>\" or \"split\")")

// ParseMark parses one whisper or say line into a mark. The keyword is case-insensitive; the name keeps its case.
// Anything else is errUnknownMark: the caller counts it, never stores it.
func ParseMark(text string) (Mark, error) {
	if len(text) > MaxTextLen {
		return Mark{}, fmt.Errorf("gmwalk: the line is past %d bytes", MaxTextLen)
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return Mark{}, errUnknownMark
	}
	switch strings.ToLower(words[0]) {
	case MarkSplit:
		if len(words) != 1 {
			return Mark{}, fmt.Errorf("gmwalk: %w", errUnknownMark)
		}
		return Mark{Kind: MarkSplit}, nil
	case MarkPoint:
		if len(words) != 2 {
			return Mark{}, fmt.Errorf("gmwalk: %w", errUnknownMark)
		}
		if err := checkName(words[1]); err != nil {
			return Mark{}, err
		}
		return Mark{Kind: MarkPoint, Name: words[1]}, nil
	case MarkZone:
		if len(words) != 3 {
			return Mark{}, fmt.Errorf("gmwalk: %w", errUnknownMark)
		}
		if err := checkName(words[1]); err != nil {
			return Mark{}, err
		}
		r, err := parseRadius(words[2])
		if err != nil {
			return Mark{}, err
		}
		return Mark{Kind: MarkZone, Name: words[1], Radius: r}, nil
	default:
		return Mark{}, errUnknownMark
	}
}

func parseRadius(s string) (float64, error) {
	if s == "" || len(s) > 32 {
		return 0, fmt.Errorf("gmwalk: radius %q is not a number", s)
	}
	r, err := parseFloat(s)
	if err != nil {
		return 0, fmt.Errorf("gmwalk: radius %q is not a number", s)
	}
	if math.IsNaN(r) || math.IsInf(r, 0) || r < MinRadiusYd || r > MaxRadiusYd {
		return 0, fmt.Errorf("gmwalk: radius %q yd is outside %.0f-%.0f", s, MinRadiusYd, MaxRadiusYd)
	}
	return r, nil
}

// parseFloat reads a plain decimal without the strconv dependency surface: digits, one dot, a leading sign.
func parseFloat(s string) (float64, error) {
	neg := false
	rest := s
	if strings.HasPrefix(rest, "+") || strings.HasPrefix(rest, "-") {
		neg = rest[0] == '-'
		rest = rest[1:]
	}
	if rest == "" {
		return 0, errors.New("empty")
	}
	var whole, frac float64
	seenDot, seenDigit := false, false
	div := 1.0
	for i := range rest {
		c := rest[i]
		if c == '.' {
			if seenDot {
				return 0, errors.New("two dots")
			}
			seenDot = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, errors.New("not a digit")
		}
		seenDigit = true
		d := float64(c - '0')
		if !seenDot {
			whole = whole*10 + d
		} else {
			div *= 10
			frac += d / div
		}
	}
	if !seenDigit {
		return 0, errors.New("no digits")
	}
	r := whole + frac
	if neg {
		r = -r
	}
	return r, nil
}
