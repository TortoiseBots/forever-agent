package main

// forever gm-walk and gm-convert: the dry replay over a hand-made recording, the convert round trip, and the
// argument checks before any network or file.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/gmwalk"
)

func writeRecording(t *testing.T) string {
	t.Helper()
	rec := gmwalk.Recording{
		Samples: []gmwalk.Sample{
			{Map: 0, X: 0, Y: 0, Z: 0},
			{Map: 0, X: 10, Y: 0, Z: 0},
			{Map: 0, X: 20, Y: 0, Z: 0},
			{Map: 0, X: 30, Y: 5, Z: 0},
		},
		Marks: []gmwalk.MarkedLine{
			{At: 1, Line: "point gate"},
			{At: 2, Line: "zone lava 25"},
			{At: 2, Line: "hello there"},
			{At: 2, Line: "split"},
		},
	}
	path := filepath.Join(t.TempDir(), "walk.json")
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestForeverGMWalkDry(t *testing.T) {
	out := filepath.Join(t.TempDir(), "walk-out.json")
	mod := filepath.Join(t.TempDir(), "module.json")
	code, stdout, stderr := runCLI("forever", "gm-walk", "--dry", writeRecording(t),
		"--tolerance", "0.5", "--out", out, "--module-out", mod)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"dry replay of 4 samples", "world data written", `point "gate"`, `zone "lava"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output has no %q:\n%s", want, stdout)
		}
	}
	data, err := gmwalk.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Points) != 1 || data.Points[0].Name != "gate" {
		t.Fatalf("points: %+v", data.Points)
	}
	if len(data.Zones) != 1 || data.Zones[0].Radius != 25 {
		t.Fatalf("zones: %+v", data.Zones)
	}
	if len(data.Roads) != 2 {
		t.Fatalf("split must cut the road: %+v", data.Roads)
	}
	mraw, err := os.ReadFile(mod)
	if err != nil {
		t.Fatal(err)
	}
	var m gmwalk.ModuleData
	if err := json.Unmarshal(mraw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Roads) == 0 || len(m.Zones) != 1 {
		t.Fatalf("module data: %+v", m)
	}
}

func TestForeverGMWalkDryDefaults(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	code, stdout, _ := runCLI("forever", "gm-walk", "--dry", writeRecording(t))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "gm-walk-dry.json")); err != nil {
		t.Fatalf("default output not written: %v", err)
	}
}

func TestForeverGMWalkDryRefusals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"nope": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI("forever", "gm-walk", "--dry", path); code != exitUsage {
		t.Fatalf("bad recording: exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI("forever", "gm-walk", "--dry", t.TempDir()+"/missing.json"); code != exitUsage {
		t.Fatalf("missing recording: exit %d, want %d", code, exitUsage)
	}
}

func TestForeverGMWalkArguments(t *testing.T) {
	rec := writeRecording(t)
	cases := [][]string{
		{}, // no follow, no dry
		{"--dry", rec, "--marks-from", "everyone"},
		{"--dry", rec, "--sample", "0"},
		{"--dry", rec, "--tolerance", "-1"},
		{"--dry", rec, "--follow-yd", "0"},
		{"--dry", rec, "--follow-yd", "101"},
		{"--dry", rec, "--settle", "3m"},
		{"--dry", rec, "--timeout", "30s"},
		{"--dry", rec, "extra"},
	}
	for _, args := range cases {
		if code, _, _ := runCLI(append([]string{"forever", "gm-walk"}, args...)...); code != exitUsage {
			t.Fatalf("args %v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestForeverGMWalkNeedsPassword(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	code, _, errOut := runCLI("forever", "gm-walk", "--follow", "Gm", "--rest", "h:1", "--rpc", "h:2",
		"--ca", "ca.pem", "--account", "a", "--character", "C")
	if code != exitUsage || !strings.Contains(errOut, "FOREVER_PASSWORD") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestForeverGMWalkUsageNamesCommand(t *testing.T) {
	_, _, errOut := runCLI("forever")
	if !strings.Contains(errOut, "gm-walk") {
		t.Fatalf("forever usage names no gm-walk: %q", errOut)
	}
}

func TestForeverGMConvert(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	data := gmwalk.WorldData{
		Version: 1,
		Roads:   []gmwalk.Road{{Map: 0, Points: [][3]float32{{0, 0, 0}, {10, 0, 0}}}},
		Points:  []gmwalk.Point{{Name: "gate", Map: 0}},
		Zones:   []gmwalk.Zone{{Name: "lava", Map: 0, Radius: 25}},
	}
	if err := gmwalk.WriteFile(in, data); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.json")
	mod := filepath.Join(dir, "module.json")
	code, stdout, stderr := runCLI("forever", "gm-convert", "--in", in, "--out", out, "--module-out", mod)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "1 road(s), 1 point(s), 1 zone(s)") {
		t.Fatalf("summary line: %s", stdout)
	}
	back, err := gmwalk.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Roads) != 1 || len(back.Points) != 1 {
		t.Fatalf("convert lost data: %+v", back)
	}
	mraw, err := os.ReadFile(mod)
	if err != nil {
		t.Fatal(err)
	}
	var m gmwalk.ModuleData
	if err := json.Unmarshal(mraw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Roads) != 1 || m.Roads[0].Width != gmwalk.DefaultRoadWidthYd || m.Roads[0].Verified {
		t.Fatalf("road row: %+v", m.Roads)
	}
}

func TestForeverGMConvertRefusals(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	if err := gmwalk.WriteFile(in, gmwalk.WorldData{Version: 1, Roads: []gmwalk.Road{}, Points: []gmwalk.Point{}, Zones: []gmwalk.Zone{}}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLI("forever", "gm-convert"); code != exitUsage {
		t.Fatalf("no flags: exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI("forever", "gm-convert", "--in", in); code != exitUsage {
		t.Fatalf("no --out: exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI("forever", "gm-convert", "--in", dir+"/missing.json", "--out", dir+"/o.json"); code != exitUsage {
		t.Fatalf("missing input: exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI("forever", "gm-convert", "--in", in, "--out", dir+"/o.json", "extra"); code != exitUsage {
		t.Fatalf("extra arg: exit %d, want %d", code, exitUsage)
	}
}
