package gmwalk

// Output format and hostile input: the JSON round-trips through strict decode, broken files are refused, and the
// converter renders the module's shape.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleData() WorldData {
	return WorldData{
		Version: Version,
		Roads: []Road{
			{Map: 0, Points: [][3]float32{{0, 0, 0}, {10, 0, 0}, {20, 5, 0}}},
			{Map: 1, Points: [][3]float32{{100, 200, 30}, {110, 200, 30}}},
		},
		Points: []Point{{Name: "gate", Map: 0, X: 10, Y: 0, Z: 0}},
		Zones:  []Zone{{Name: "lava", Map: 0, X: 20, Y: 5, Z: 0, Radius: 25}},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "walk.json")
	if err := WriteFile(path, sampleData()); err != nil {
		t.Fatal(err)
	}
	d, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != Version || len(d.Roads) != 2 || len(d.Points) != 1 || len(d.Zones) != 1 {
		t.Fatalf("round trip lost data: %+v", d)
	}
	if d.Roads[0].Points[2] != [3]float32{20, 5, 0} {
		t.Fatalf("corner: %v", d.Roads[0].Points[2])
	}
	if d.Zones[0].Radius != 25 {
		t.Fatalf("radius: %v", d.Zones[0].Radius)
	}
}

func hostileFiles() map[string]string {
	return map[string]string{
		"empty":               ``,
		"not json":            `hello`,
		"two values":          `{"version":1,"roads":[],"points":[],"zones":[]} {}`,
		"wrong version":       `{"version":2,"roads":[],"points":[],"zones":[]}`,
		"no version":          `{"roads":[],"points":[],"zones":[]}`,
		"unknown field":       `{"version":1,"roads":[],"points":[],"zones":[],"author":"me"}`,
		"unknown nested":      `{"version":1,"roads":[{"map":0,"points":[],"gm":"x"}],"points":[],"zones":[]}`,
		"infinite coordinate": `{"version":1,"roads":[],"points":[{"name":"a","map":0,"x":1e999,"y":0,"z":0}],"zones":[]}`,
		"bad name":            `{"version":1,"roads":[],"points":[{"name":"a/b","map":0,"x":0,"y":0,"z":0}],"zones":[]}`,
		"empty name":          `{"version":1,"roads":[],"points":[{"name":"","map":0,"x":0,"y":0,"z":0}],"zones":[]}`,
		"NaN coordinate":      `{"version":1,"roads":[],"points":[{"name":"a","map":0,"x":0,"y":0,"z":"NaN"}],"zones":[]}`,
		"huge coordinate":     `{"version":1,"roads":[{"map":0,"points":[[0,0,200000]]}],"points":[],"zones":[]}`,
		"bad radius":          `{"version":1,"roads":[],"points":[],"zones":[{"name":"z","map":0,"x":0,"y":0,"z":0,"radius_yd":0}]}`,
		"huge radius":         `{"version":1,"roads":[],"points":[],"zones":[{"name":"z","map":0,"x":0,"y":0,"z":0,"radius_yd":9999}]}`,
		"too many roads":      `{"version":1,"roads":[` + strings.Repeat(`{"map":0,"points":[]},`, MaxRoads+1) + `],"points":[],"zones":[]}`,
	}
}

func TestReadFileRefusesHostileInput(t *testing.T) {
	dir := t.TempDir()
	for name, body := range hostileFiles() {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFile(path); err == nil {
			t.Errorf("%s: read, want refusal", name)
		}
	}
}

func TestReadFileRefusesOversizeAndNonRegular(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxDataBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadFile(big); err == nil {
		t.Error("oversize file: read, want refusal")
	}
	if _, err := ReadFile(dir); err == nil {
		t.Error("directory: read, want refusal")
	}
	if _, err := ReadFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: read, want refusal")
	}
}

func TestWriteFileRefusesToHalfWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "walk.json")
	bad := sampleData()
	bad.Version = 99
	if err := WriteFile(path, bad); err == nil {
		t.Fatal("invalid data: wrote, want refusal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid data left a file behind")
	}
}

func TestWriteFileRefusesNonRegularTarget(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink("elsewhere", link); err != nil {
		t.Skip("no symlinks here")
	}
	if err := WriteFile(link, sampleData()); err == nil {
		t.Fatal("symlink target: wrote, want refusal")
	}
}

func TestConvertRendersModuleShape(t *testing.T) {
	m := Convert(sampleData())
	// Map 0's road has 2 hops, map 1's has 1.
	if len(m.Roads) != 3 {
		t.Fatalf("%d road rows, want 3", len(m.Roads))
	}
	r := m.Roads[0]
	if r.Map != 0 || r.FromX != 0 || r.ToX != 10 || r.Width != DefaultRoadWidthYd {
		t.Fatalf("row: %+v", r)
	}
	if r.Verified {
		t.Fatal("converter output must never claim verified")
	}
	if len(m.Zones) != 1 || m.Zones[0].RadiusYd != 25 || m.Zones[0].Name != "lava" {
		t.Fatalf("zones: %+v", m.Zones)
	}
	if len(m.Points) != 1 || m.Points[0].Name != "gate" {
		t.Fatalf("points: %+v", m.Points)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back ModuleData
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if len(back.Roads) != 3 {
		t.Fatalf("module JSON round trip: %+v", back)
	}
}

func TestConvertEmpty(t *testing.T) {
	m := Convert(WorldData{Version: Version})
	if m.Roads == nil || m.Zones == nil || m.Points == nil {
		t.Fatal("converter slices must be non-nil")
	}
	path := filepath.Join(t.TempDir(), "module.json")
	if err := WriteModule(path, m); err != nil {
		t.Fatal(err)
	}
}

func TestConvertDegenerateRoad(t *testing.T) {
	d := WorldData{Version: Version, Roads: []Road{{Map: 0, Points: [][3]float32{{1, 2, 3}}}}}
	if got := Convert(d); len(got.Roads) != 0 {
		t.Fatalf("single-point road made %d rows", len(got.Roads))
	}
}
