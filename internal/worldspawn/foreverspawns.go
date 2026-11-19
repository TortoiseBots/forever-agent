package worldspawn

// A Forever server's spawn list for plan-check is a TSV with one row per creature spawn: guid, entry, x, y, z, movement type.
// It carries no creature template, group, link or waypoint data: the aggro and patrol lints then see each spawn alone, which
// the notes say.

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// LoadForeverSpawns reads the TSV a `plan <name> spawns` command prints (guid entry x y z movetype), one map's spawns.
// Level 60 and detection 20 yd are the lints' defaults for a file that carries neither.
func LoadForeverSpawns(path string, mapID uint32) (*World, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var spawns []Spawn
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "--") {
			continue
		}
		var f6 []string
		if strings.Contains(t, "\t") {
			f6 = strings.Split(t, "\t")
		} else {
			f6 = strings.Fields(t)
		}
		if len(f6) < 6 {
			return nil, nil, fmt.Errorf("%s:%d: want guid, entry, x, y, z, movement type", path, line)
		}
		if strings.EqualFold(f6[0], "guid") {
			continue // a header line
		}
		guid, err := strconv.ParseUint(f6[0], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: guid: %w", path, line, err)
		}
		entry, err := strconv.ParseUint(f6[1], 10, 32)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: entry: %w", path, line, err)
		}
		var p dungeon.Point
		for i, dst := range []*float32{&p.X, &p.Y, &p.Z} {
			v, err := strconv.ParseFloat(strings.TrimSpace(f6[2+i]), 32)
			if err != nil {
				return nil, nil, fmt.Errorf("%s:%d: %s: %w", path, line, []string{"x", "y", "z"}[i], err)
			}
			*dst = float32(v)
		}
		movement, err := strconv.Atoi(strings.TrimSpace(f6[5]))
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: movement type: %w", path, line, err)
		}
		spawns = append(spawns, Spawn{
			GUID: uint64(guid), Entry: uint32(entry), Pos: p,
			LevelMax: 60, Faction: 1, DetectionYd: 20, MovementType: movement, SpeedWalk: 1,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if len(spawns) == 0 {
		return nil, nil, fmt.Errorf("%s: no spawn rows", path)
	}
	sort.Slice(spawns, func(i, j int) bool { return spawns[i].GUID < spawns[j].GUID })
	notes := []string{fmt.Sprintf("the spawn list carries no groups, links or waypoints: the aggro and patrol lints see each of the %d spawns alone", len(spawns))}
	return &World{Map: mapID, Spawns: spawns}, notes, nil
}
