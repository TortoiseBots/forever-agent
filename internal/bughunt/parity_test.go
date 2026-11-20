package bughunt

// Optional parity with the Python checker, gated by BUGHUNT_PY_CHECKER (the path to checker.py). The Forever rules
// read the combat log and the frames; the Python reads a trials repeat's events and samples. This test builds one
// repeat whose two recordings describe the same fight, so rules 4 and 8 must find the same (rule, key) pairs in
// both. Rule 15 needs a dispeller's class, which a trials repeat does not carry for its extras, so both engines are
// silent there; the fixture still lists the debuff, and the test pins that silence.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeParityRepeat writes a trials repeat whose events, samples and raw rows describe one AoE and one buff
// flip-flop.
func writeParityRepeat(t *testing.T, root string) string {
	t.Helper()
	rep := filepath.Join(root, "trials", "healer-priest-heal-latency", "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	const start = "2023-11-14T22:13:20+00:00"
	base := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	at := func(t float64) string {
		return base.Add(time.Duration(math.Round(t*1000)) * time.Millisecond).Format(combatTimeLayout)
	}
	guids := map[string]string{"Atank": gTank, "Bheal": gHeal, "Cmage": gDps, "Dcmd": gDps3}
	var events, raw, samples []string
	on := func(dst string, sid int, t float64) {
		events = append(events, fmt.Sprintf(`{"kind":"aura_on","dst":"%s","dst_guid":"%s","spell_id":%d,"t":%v,"wall":"%s"}`,
			dst, guids[dst], sid, t, start))
		raw = append(raw, fmt.Sprintf(`{"kind":"aura_applied","time":"%s","seq":%d,"source":{"guid":"%s","name":"Bheal"},`+
			`"target":{"guid":"%s","name":"%s"},"spell_id":%d,"amount":0}`, at(t), len(raw)+1, gHeal, guids[dst], dst, sid))
	}
	off := func(dst string, sid int, t float64) {
		events = append(events, fmt.Sprintf(`{"kind":"aura_off","dst":"%s","dst_guid":"%s","spell_id":%d,"t":%v,"wall":"%s"}`,
			dst, guids[dst], sid, t, start))
		raw = append(raw, fmt.Sprintf(`{"kind":"aura_removed","time":"%s","seq":%d,"source":{"guid":"%s","name":"Bheal"},`+
			`"target":{"guid":"%s","name":"%s"},"spell_id":%d,"amount":0}`, at(t), len(raw)+1, gHeal, guids[dst], dst, sid))
	}
	const fort, aoe = 21562, 12345
	for i, t := range []float64{0, 10, 20, 30} {
		events = append(events,
			fmt.Sprintf(`{"kind":"cast_start","src":"Bheal","src_guid":"%s","dst":"Atank","dst_guid":"%s","spell":"Power Word: Fortitude","spell_id":%d,"t":%v}`,
				gHeal, gTank, fort, t-0.2),
			fmt.Sprintf(`{"kind":"cast","src":"Bheal","src_guid":"%s","dst":"Atank","dst_guid":"%s","spell":"Power Word: Fortitude","spell_id":%d,"t":%v}`,
				gHeal, gTank, fort, t))
		raw = append(raw,
			fmt.Sprintf(`{"kind":"spell_start","time":"%s","seq":%d,"source":{"guid":"%s","name":"Bheal"},`+
				`"target":{"guid":"%s","name":"Atank"},"spell_id":%d,"amount":0}`, at(t-0.2), len(raw)+1, gHeal, gTank, fort),
			fmt.Sprintf(`{"kind":"spell_go","time":"%s","seq":%d,"source":{"guid":"%s","name":"Bheal"},`+
				`"target":{"guid":"%s","name":"Atank"},"spell_id":%d,"amount":0}`, at(t), len(raw)+1, gHeal, gTank, fort))
		on("Atank", fort, t-0.1)
		off("Atank", fort, 40+float64(i)*10)
	}
	for i := 0; i <= 4; i++ {
		events = append(events, fmt.Sprintf(`{"kind":"damage","src":"Mob","src_guid":"0x0000000000000B01","dst":"Cmage","dst_guid":"%s",`+
			`"spell":"Rain of Fire","spell_id":%d,"amount":20,"t":%d}`, gDps, aoe, i))
		raw = append(raw, fmt.Sprintf(`{"kind":"spell_damage","time":"%s","seq":%d,"source":{"guid":"0x0000000000000B01","name":"Mob"},`+
			`"target":{"guid":"%s","name":"Cmage"},"spell_id":%d,"amount":20}`, at(float64(i)), len(raw)+1, gDps, aoe))
	}
	for t := 0; t <= 34; t++ {
		for _, m := range []struct {
			unit, name string
			x          float64
		}{{"bot", "Bheal", 0}, {"extra1", "Atank", 2}, {"extra2", "Cmage", 4}, {"extra3", "Dcmd", 6}} {
			auras := "[]"
			if m.name == "Bheal" && t <= 14 {
				auras = "[17172]"
			}
			samples = append(samples, fmt.Sprintf(`{"t":%d,"unit":"%s","guid":"%s","hp":100,"max_hp":100,"power":50,"max_power":100,`+
				`"x":%v,"y":0,"z":0,"in_combat":true,"auras":%s,"wall":"%s"}`, t, m.unit, guids[m.name], m.x, auras, start))
		}
	}
	writeLines := func(name string, rows []string) {
		if err := os.WriteFile(filepath.Join(rep, name), []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLines("events.jsonl", events)
	writeLines("raw-ta.jsonl", raw)
	writeLines("samples.jsonl", samples)
	setup := map[string]any{"bot_name": "Bheal", "extra_names": []string{"Atank", "Cmage", "Dcmd"}, "checks": []any{}}
	b, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rep, "setup.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return rep
}

// keySet is the hits' (rule|key) strings, sorted.
func keySet(hits []Hit) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hits {
		k := fmt.Sprintf("%d|%s", h.Rule, strings.Join(h.Key, "|"))
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestParityWithPythonChecker compares rules 4 and 8 with the Python checker on the same repeat. It is skipped
// unless BUGHUNT_PY_CHECKER names checker.py.
func TestParityWithPythonChecker(t *testing.T) {
	checker := os.Getenv("BUGHUNT_PY_CHECKER")
	if checker == "" {
		t.Skip("BUGHUNT_PY_CHECKER is not set")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not found: %v", err)
	}
	root := t.TempDir()
	rep := writeParityRepeat(t, root)
	r, err := LoadTrials(rep, "trials")
	if err != nil {
		t.Fatal(err)
	}
	r.Names = map[int]string{21562: "Power Word: Fortitude", 12345: "Rain of Fire"}
	r.Dispel = map[int]string{17172: "magic"}
	if hits := Rule15(r); len(hits) != 0 {
		t.Fatalf("rule 15 judged a repeat whose extras have no class: %+v", hits)
	}
	ours := keySet(append(Rule4(r), Rule8(r)...))
	outDir := filepath.Join(root, "py")
	cmd := exec.Command(python, "-I", checker, "--rules", "4,8", "--out", outDir, root)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checker: %v\n%s", err, b)
	}
	f, err := os.Open(filepath.Join(outDir, "hits.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var theirs []Hit
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var h Hit
		if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
			t.Fatalf("hit: %v", err)
		}
		theirs = append(theirs, h)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for i := range theirs {
		// The Python's rule 8 key carries the aura-life word; ours leaves it out (derived text, not identity).
		if theirs[i].Rule == 8 && len(theirs[i].Key) > 2 {
			theirs[i].Key = theirs[i].Key[:2]
		}
	}
	if got, want := strings.Join(ours, " "), strings.Join(keySet(theirs), " "); got != want {
		t.Fatalf("keys differ:\n ours:   %s\n python: %s", got, want)
	}
	if len(ours) != 2 {
		t.Fatalf("the fixture should hold two tickets, got %v", ours)
	}
}
