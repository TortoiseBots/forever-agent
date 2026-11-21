// File I/O and the server-module converter.

package gmwalk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ReadFile reads and validates a world-data file: bounded on disk, strict JSON, validated after decode.
func ReadFile(path string) (WorldData, error) {
	return readFile(path, MaxDataBytes, true)
}

// ReadRecording reads a dry-recording file: same bounds and strictness, no Validate (a recording is inputs,
// not outputs).
func ReadRecording(path string) (Recording, error) {
	raw, err := readBounded(path, MaxDataBytes)
	if err != nil {
		return Recording{}, err
	}
	var rec Recording
	if err := decodeStrict(raw, &rec); err != nil {
		return Recording{}, err
	}
	if err := rec.validate(); err != nil {
		return Recording{}, err
	}
	return rec, nil
}

func readFile(path string, maxBytes int, validate bool) (WorldData, error) {
	raw, err := readBounded(path, maxBytes)
	if err != nil {
		return WorldData{}, err
	}
	var d WorldData
	if err := decodeStrict(raw, &d); err != nil {
		return WorldData{}, err
	}
	if d.Roads == nil {
		d.Roads = []Road{}
	}
	if d.Points == nil {
		d.Points = []Point{}
	}
	if d.Zones == nil {
		d.Zones = []Zone{}
	}
	if validate {
		if err := Validate(d); err != nil {
			return WorldData{}, err
		}
	}
	return d, nil
}

func readBounded(path string, maxBytes int) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("gmwalk: %s is not a plain file", path)
	}
	if fi.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("gmwalk: %s is %d bytes past the %d bound", path, fi.Size(), maxBytes)
	}
	return os.ReadFile(path)
}

// decodeStrict decodes JSON with unknown fields refused and a single value required: a hostile file with an
// extra key or trailing bytes is an error, not a silent pass.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("gmwalk: the file is not world data: %w", err)
	}
	if dec.More() {
		return errors.New("gmwalk: the file holds more than one JSON value")
	}
	return nil
}

// WriteFile validates the data and writes it atomically: a temp file in the folder, then a rename. A broken
// record is never half-written.
func WriteFile(path string, d WorldData) error {
	if err := Validate(d); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxDataBytes {
		return fmt.Errorf("gmwalk: the output is %d bytes past the %d bound", len(raw), MaxDataBytes)
	}
	return writeAtomic(path, raw, 0o644)
}

func writeAtomic(path string, raw []byte, perm os.FileMode) error {
	dir := dirOf(path)
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("gmwalk: the output folder %s is not a directory", dir)
		}
	}
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("gmwalk: %s exists and is not a plain file; nothing was written", path)
		}
	}
	f, err := os.CreateTemp(dirOrDot(dir), ".gm-walk-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return ""
}

func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// ---- the server module's shape ----

// RoadRow is one row of the handcrafted_road table the worldserver loads (HandcraftedRoadStorage.h in
// ForeverCore: id, mapId, fromX, fromY, toX, toY, width, comment, verified). The table carries no Z: ground
// height is resolved from the mesh at load. A road polyline becomes one row per hop.
type RoadRow struct {
	Map      uint32  `json:"mapId"`
	FromX    float32 `json:"fromX"`
	FromY    float32 `json:"fromY"`
	ToX      float32 `json:"toX"`
	ToY      float32 `json:"toY"`
	Width    float32 `json:"width_yd"`
	Comment  string  `json:"comment"`
	Verified bool    `json:"verified"`
}

// ZoneShape is the danger-zone proposal for the module side: no loader exists, so the notes carry the format
// spec (GM-WALK-NOTES.md) and the converter emits this JSON for the module to adopt.
type ZoneShape struct {
	Name     string  `json:"name"`
	Map      uint32  `json:"mapId"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	RadiusYd float32 `json:"radius_yd"`
}

// PointShape is the point proposal for the module side, same status as ZoneShape.
type PointShape struct {
	Name string  `json:"name"`
	Map  uint32  `json:"mapId"`
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	Z    float32 `json:"z"`
}

// ModuleData is the converter's output: road rows for the handcrafted_road table plus the zone/point
// proposals the module has no loader for yet.
type ModuleData struct {
	Version int          `json:"version"`
	Roads   []RoadRow    `json:"roads"`
	Zones   []ZoneShape  `json:"zones"`
	Points  []PointShape `json:"points"`
}

// DefaultRoadWidthYd is the width the converter stamps on road rows: the storage default (8 yards).
const DefaultRoadWidthYd = 8.0

// Convert renders world data in the module's shape. Roads become one straight row per hop (the loader only
// knows from/to segments); zones and points pass through with their names. Verified is always false: a walk
// is raw material, and a human marks it verified on the module side.
func Convert(d WorldData) ModuleData {
	out := ModuleData{Version: d.Version, Roads: []RoadRow{}, Zones: []ZoneShape{}, Points: []PointShape{}}
	for i := range d.Roads {
		r := &d.Roads[i]
		for j := 1; j < len(r.Points); j++ {
			a, b := r.Points[j-1], r.Points[j]
			out.Roads = append(out.Roads, RoadRow{
				Map:      r.Map,
				FromX:    a[0],
				FromY:    a[1],
				ToX:      b[0],
				ToY:      b[1],
				Width:    DefaultRoadWidthYd,
				Comment:  fmt.Sprintf("gm-walk road %d hop %d", i, j),
				Verified: false,
			})
		}
	}
	for i := range d.Zones {
		z := &d.Zones[i]
		out.Zones = append(out.Zones, ZoneShape{Name: z.Name, Map: z.Map, X: z.X, Y: z.Y, Z: z.Z, RadiusYd: z.Radius})
	}
	for i := range d.Points {
		p := &d.Points[i]
		out.Points = append(out.Points, PointShape{Name: p.Name, Map: p.Map, X: p.X, Y: p.Y, Z: p.Z})
	}
	return out
}

// WriteModule validates the data and writes the converter's JSON atomically.
func WriteModule(path string, m ModuleData) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxDataBytes {
		return fmt.Errorf("gmwalk: the output is %d bytes past the %d bound", len(raw), MaxDataBytes)
	}
	return writeAtomic(path, raw, 0o644)
}

// Recording is the --dry input: a hand-made list of samples and mark lines the offline path replays. It is
// test data by design; the live walk never writes one.
type Recording struct {
	Samples []Sample `json:"samples"`
	Marks   []string `json:"marks"`
}

func (r Recording) validate() error {
	if len(r.Samples) > MaxSamples {
		return fmt.Errorf("gmwalk: %d samples pass the %d bound", len(r.Samples), MaxSamples)
	}
	if len(r.Marks) > MaxMarks {
		return fmt.Errorf("gmwalk: %d marks pass the %d bound", len(r.Marks), MaxMarks)
	}
	for i, s := range r.Samples {
		if !finite(s.X) || !finite(s.Y) || !finite(s.Z) {
			return fmt.Errorf("gmwalk: sample %d is not finite", i)
		}
	}
	return nil
}

// Replay runs a dry recording through the recorder: samples in order, each mark line parsed and applied at
// the position the GM stood at. Unknown lines are counted, never stored.
func Replay(rec Recording, toleranceYd float64) (WorldData, int, error) {
	r := &Recorder{}
	for _, s := range rec.Samples {
		r.AddSample(s)
	}
	unknown := 0
	for _, line := range rec.Marks {
		if len(line) > MaxTextLen {
			unknown++
			continue
		}
		m, err := ParseMark(line)
		if err != nil {
			unknown++
			continue
		}
		if err := r.Mark(m); err != nil {
			unknown++
		}
	}
	return r.Build(toleranceYd), unknown, nil
}
