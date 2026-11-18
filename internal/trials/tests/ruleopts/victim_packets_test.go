package ruleopts

// Translated from the Python bench's tests/test_victim_packets.py:
// the scorer reads a mob's victim from its attack packets, not stale 1 Hz samples.

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

// scoreRep is the Python bench's score.score_rep(rep, trial) with no earlier replies.
func scoreRep(t *testing.T, rep string, trial scenario.Trial) map[string]any {
	t.Helper()
	return score.ScoreRep(rep, trial, nil)
}

const (
	vpPlayer = "0x0000000000000001"
	vpBot    = "0x0000000000000002"
	vpHealer = "0x0000000000000003"
	vpMob    = "0xf13000000000abcd"
	vpTaunt  = int64(355)
)

func vpGuids() map[string]any {
	return map[string]any{"player": vpPlayer, "bot": vpBot, "extras": []any{vpHealer}, "mobs": []any{vpMob}}
}

func vpSample(t float64, guid, target string) map[string]any {
	return map[string]any{"t": t, "wall": nil, "unit": "mob", "guid": guid, "hp": int64(600),
		"max_hp": int64(1000), "power": int64(0), "max_power": int64(100), "auras": []any{},
		"casting": nil, "in_combat": true, "target": target,
		"x": 0.0, "y": 0.0, "z": 0.0}
}

func vpCast(t float64, seq any) map[string]any {
	return map[string]any{"t": t, "wall": nil, "kind": "cast", "src": "bot", "src_guid": vpBot,
		"dst": "mob", "dst_guid": vpMob, "src_unit": "$bot", "dst_unit": "mob:" + vpMob,
		"spell_id": vpTaunt, "spell": "Taunt", "amount": nil, "dst_hp_pct": 96.0,
		"src_role": "bot", "dst_role": "mob", "origin": "ta", "seq": seq}
}

// vpRaw is raw(kind, seq, src, dst, t, base); an ISO base formats int(t) into its %02d.
func vpRaw(kind string, seq int64, src, dst string, t float64, base string) map[string]any {
	var wall any
	if base != "" {
		wall = fmt.Sprintf(base, int(t))
	} else {
		wall = t
	}
	return map[string]any{"seq": seq, "time": wall, "kind": kind,
		"source": map[string]any{"name": "m", "guid": src},
		"target": map[string]any{"name": "u", "guid": dst}}
}

func vpCtx() *rules.Ctx {
	return &rules.Ctx{Guids: vpGuids(), Thresholds: map[string]any{}, Trial: map[string]any{},
		Rows: []rules.Row{}, SamplesAll: []rules.Row{}, Marks: []rules.Row{}}
}

func vpWhere(dstTarget string) map[string]any {
	return map[string]any{"kind": "cast", "src": "$bot", "dst_target_of": dstTarget}
}

// vpVictim is one Python (seq, t, victim) tuple; a nil seq means HasSeq false.
func vpVictim(seq any, t float64, victim any) events.AttackVictim {
	out := events.AttackVictim{T: t}
	if seq != nil {
		out.Seq = seq.(int64)
		out.HasSeq = true
	}
	if victim != nil {
		out.Victim = victim.(string)
	}
	return out
}

func vpVictims(rows ...events.AttackVictim) map[string][]events.AttackVictim {
	return map[string][]events.AttackVictim{vpMob: rows}
}

// warR3 is the AFTER2 war-r3 shape: cast seq 18982, packet 18981 (mob->healer) after the sample.
func vpWarR3() (rows []rules.Row, samples []rules.Row, victims map[string][]events.AttackVictim) {
	samples = []rules.Row{vpSample(152.0, vpMob, vpPlayer), vpSample(153.0, vpMob, vpPlayer)}
	victims = vpVictims(vpVictim(int64(18981), 153.1, vpHealer))
	rows = []rules.Row{vpCast(153.38, int64(18982))}
	return rows, samples, victims
}

func TestPacketVictimTest_WarR3PacketOverrulesStaleSample(t *testing.T) {
	// the mob's victim by packet is the healer: the cast is NOT off the player.
	rows, samples, victims := vpWarR3()
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	// quoting the failing line on the old code: the sample still says target=player.
	if samples[len(samples)-1]["target"] != vpPlayer {
		t.Fatalf("sample target = %v; want %s", samples[len(samples)-1]["target"], vpPlayer)
	}
	if rules.Matches(rows[0], vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $player; want no match")
	}
	if !rules.Matches(rows[0], vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $extra1")
	}
}

func TestPacketVictimTest_MobOnPlayerByPacketWithStaleHealerSample(t *testing.T) {
	samples := []rules.Row{vpSample(12.0, vpMob, vpHealer), vpSample(13.0, vpMob, vpHealer)}
	victims := vpVictims(vpVictim(int64(41), 13.1, vpPlayer))
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
	if rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $extra1; want no match")
	}
}

func TestPacketVictimTest_NoPacketFallsBackToSample(t *testing.T) {
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.0, vpMob, vpPlayer)}
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	if !rules.Matches(row, vpWhere("$player"), byGUID, vpCtx(), map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
	if rules.Matches(row, vpWhere("$extra1"), byGUID, vpCtx(), map[string]any{}, nil) {
		t.Fatal("cast matched $extra1; want no match")
	}
}

func TestPacketVictimTest_NewerSampleOverrulesOlderPacket(t *testing.T) {
	// HIGH round-1 finding: an old healer packet with a newer player sample under
	// the cast must NOT decide by packet: the cast off the player still counts.
	samples := []rules.Row{vpSample(12.0, vpMob, vpHealer), vpSample(13.2, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(int64(41), 13.0, vpHealer))
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
	if rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $extra1; want no match")
	}
}

func TestPacketVictimTest_SameTimePacketIsIgnoredTimePath(t *testing.T) {
	// CRITICAL round-1 finding: the taunt's attack_stop/start land on the same
	// mono_ms as the cast's spell_go, so the normaliser gives them the same t.
	// The time path must use only strictly earlier packets (vt < t): a cast whose
	// only same-t packet is the switch to the bot still sees the old player victim
	// and counts as a taunt off the player.
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.0, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(nil, 12.5, vpPlayer), vpVictim(nil, 13.38, vpBot))
	row := vpCast(13.38, nil)
	delete(row, "seq")
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
}

func TestPacketVictimTest_PacketOlderThan5sFallsBackToSample(t *testing.T) {
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.0, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(int64(40), 7.0, vpHealer))
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
}

func TestPacketVictimTest_AttackStopNewerThanTheSampleDecidesNotAttackingSeqPath(t *testing.T) {
	// Round-1 MEDIUM: the newest event wins. A stop 0.2 s after the last
	// sample (13.0, on the player) says the mob dropped its target, so the
	// older sample must not be read as "on the player" at the cast.
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.0, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(int64(40), 13.2, nil))
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if victim, decided := rules.PacketVictim(victims, vpMob, 13.38, int64(42), byGUID); victim != "" || !decided {
		t.Fatalf("packet_victim = (%q, %v); want (\"\", true)", victim, decided)
	}
	if rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $player; want no match")
	}
	if rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $extra1; want no match")
	}
}

func TestPacketVictimTest_AttackStopNewerThanTheSampleDecidesNotAttackingTimePath(t *testing.T) {
	// Same case on the time path (no seq anywhere).
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.0, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(nil, 13.2, nil))
	row := vpCast(13.38, nil)
	delete(row, "seq")
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if victim, decided := rules.PacketVictim(victims, vpMob, 13.38, nil, byGUID); victim != "" || !decided {
		t.Fatalf("packet_victim = (%q, %v); want (\"\", true)", victim, decided)
	}
	if rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $player; want no match")
	}
	if rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $extra1; want no match")
	}
}

func TestPacketVictimTest_AStaleAttackStopLosesToANewerSample(t *testing.T) {
	// The other side of newest-wins: a stop from before the last sample is
	// the older observation; the sample's target still decides.
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.2, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(int64(40), 12.5, nil))
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if victim, decided := rules.PacketVictim(victims, vpMob, 13.38, int64(42), byGUID); victim != "" || decided {
		t.Fatalf("packet_victim = (%q, %v); want (\"\", false)", victim, decided)
	}
	if !rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
	if rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $extra1; want no match")
	}
}

func TestPacketVictimTest_SameTPacketOrdersByFileOrder(t *testing.T) {
	// no seqs: the later file row at the same t wins.
	victims := vpVictims(vpVictim(nil, 13.0, vpPlayer), vpVictim(nil, 13.0, vpHealer))
	row := vpCast(13.38, nil)
	delete(row, "seq")
	byGUID := rules.SampleRows([]rules.Row{vpSample(13.0, vpMob, vpPlayer)})
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $extra1")
	}
}

func TestPacketVictimTest_SwitchAboveCastSeqIsIgnored(t *testing.T) {
	// seq-path mirror of the same-t case: the switch packets sit above the cast's
	// seq, so they decide nothing; the old player victim stands and the cast counts.
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.0, vpMob, vpPlayer)}
	victims := vpVictims(vpVictim(int64(40), 12.5, vpPlayer), vpVictim(int64(43), 13.38, vpBot))
	row := vpCast(13.38, int64(42))
	byGUID := rules.SampleRows(samples)
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $player")
	}
	if rules.Matches(row, vpWhere("$bot"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $bot; want no match")
	}
}

func TestPacketVictimTest_SeqOrdersPacketsNotFileOrTime(t *testing.T) {
	// the highest seq below the cast wins even when filed first with an earlier t.
	victims := vpVictims(vpVictim(int64(43), 13.4, vpHealer), vpVictim(int64(42), 13.5, vpPlayer))
	row := vpCast(13.6, int64(44))
	byGUID := rules.SampleRows([]rules.Row{vpSample(13.0, vpMob, vpHealer)})
	c := vpCtx()
	c.VictimsByGUID = victims
	if !rules.Matches(row, vpWhere("$extra1"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast did not match $extra1")
	}
	if rules.Matches(row, vpWhere("$player"), byGUID, c, map[string]any{}, nil) {
		t.Fatal("cast matched $player; want no match")
	}
}

func TestPacketVictimTest_RawTimeFallsBackToRowTWhenBaseWallZero(t *testing.T) {
	// LOW round-1 finding: the _raw_time refactor changed the normaliser. When
	// the base wall is 0, an ISO `time` re-bases to its raw epoch and must fall
	// back to the row's own t, as the old code did.
	row := map[string]any{"t": 12.5, "time": "2026-01-01T00:00:11+00:00"}
	if got := events.RawTime(row, 0.0, 0, false); got != 12.5 {
		t.Fatalf("raw_time = %v; want 12.5", got)
	}
}

func TestPacketVictimTest_RawTimeRebasesNumericTime(t *testing.T) {
	// The old code re-based a numeric `time` too; the refactor must keep that.
	if got := events.RawTime(map[string]any{"t": 0.0, "time": 100.0}, 90.0, 0, false); got != 10.0 {
		t.Fatalf("raw_time = %v; want 10.0", got)
	}
}

func TestPacketVictimTest_EventsCarrySeqAndVictimTableRoundsLikeEvents(t *testing.T) {
	tmp := t.TempDir()
	writeJSON(t, filepath.Join(tmp, "setup.json"), map[string]any{"ok": true, "guids": vpGuids()})
	writeJSONL(t, filepath.Join(tmp, "marks.jsonl"), []any{
		map[string]any{"kind": "setup_ok", "wall": "2026-01-01T00:00:00+00:00"}})
	writeJSONL(t, filepath.Join(tmp, "samples.jsonl"), []any{vpSample(10.0, vpMob, vpPlayer)})
	writeJSONL(t, filepath.Join(tmp, "raw-ta.jsonl"), []any{
		vpRaw("attack_start", 41, vpMob, vpHealer, 10, "2026-01-01T00:00:%02d+00:00"),
		map[string]any{"seq": int64(42), "time": "2026-01-01T00:00:11+00:00", "kind": "spell_go",
			"source": map[string]any{"name": "b", "guid": vpBot},
			"target": map[string]any{"name": "m", "guid": vpMob}, "spell_id": vpTaunt},
	})
	rows, err := events.Normalise(tmp, map[string]any{"id": "t"})
	if err != nil {
		t.Fatal(err)
	}
	var casts []rules.Row
	for _, r := range rows {
		if r["kind"] == "cast" {
			casts = append(casts, r)
		}
	}
	if len(casts) != 1 {
		t.Fatalf("casts = %d; want 1", len(casts))
	}
	if casts[0]["seq"] != int64(42) {
		t.Fatalf("cast seq = %v; want 42", casts[0]["seq"])
	}
	raws := readJSONL(t, filepath.Join(tmp, "raw-ta.jsonl"))
	marks := readJSONL(t, filepath.Join(tmp, "marks.jsonl"))
	samples := readJSONL(t, filepath.Join(tmp, "samples.jsonl"))
	table, err := events.AttackVictims(raws, marks, samples)
	if err != nil {
		t.Fatal(err)
	}
	got := table[vpMob]
	if len(got) != 1 {
		t.Fatalf("victim table = %v; want one entry", got)
	}
	want := events.AttackVictim{Seq: int64(41), HasSeq: true, T: 10.0, Victim: vpHealer}
	if got[0] != want {
		t.Fatalf("victim table[%s] = %+v; want %+v", vpMob, got[0], want)
	}
}

func vpRuleByID(out map[string]any, id string) map[string]any {
	entries, _ := out["rules"].([]any)
	for _, entry := range entries {
		row, ok := entry.(map[string]any)
		if ok && row["id"] == id {
			return row
		}
	}
	return nil
}

func TestPacketVictimTest_ScoreRepBuildsVictimsFromRaw(t *testing.T) {
	// end to end through score_rep on the real no-taunt rule: the war-r3 shape scores 0,
	// and without raw-ta.jsonl the same rep falls back to the sample and scores 1.
	trial := scenario.FindTrialByID("tank-warrior-no-taunt-tank-player")
	if trial == nil {
		t.Fatal("trial tank-warrior-no-taunt-tank-player not found")
	}
	rows, samples, _ := vpWarR3()
	// the packets and the cast share one ISO clock: marks carry walls, and _time_base
	// reads fight_start from them, so t=153.1 lands 0.1 s after the 153.0 sample.
	marks := []any{
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:00+00:00"},
		map[string]any{"kind": "fight_end", "wall": "2026-01-01T00:03:20+00:00", "ended": "duration"},
	}
	for _, withRaw := range []bool{true, false} {
		tmp := t.TempDir()
		rep := filepath.Join(tmp, "run", "tank-warrior-no-taunt-tank-player", "rep1")
		writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{"ok": true, "guids": vpGuids(),
			"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)}})
		var sampleRows, eventRows []any
		for _, row := range samples {
			sampleRows = append(sampleRows, row)
		}
		for _, row := range rows {
			eventRows = append(eventRows, row)
		}
		writeJSONL(t, filepath.Join(rep, "samples.jsonl"), sampleRows)
		writeJSONL(t, filepath.Join(rep, "events.jsonl"), eventRows)
		writeJSONL(t, filepath.Join(rep, "marks.jsonl"), marks)
		if withRaw {
			base := "2026-01-01T00:02:%02d+00:00"
			writeJSONL(t, filepath.Join(rep, "raw-ta.jsonl"), []any{
				vpRaw("attack_start", 18980, vpMob, vpPlayer, 33, base),
				vpRaw("attack_start", 18981, vpMob, vpHealer, 33, base),
			})
		}
		out := scoreRep(t, rep, trial)
		got := vpRuleByID(out, "TNK-WAR-NO-TAUNT")
		if got == nil {
			t.Fatal("rule TNK-WAR-NO-TAUNT missing")
		}
		wantValue := int64(1)
		wantVerdict := "FAIL"
		if withRaw {
			wantValue, wantVerdict = 0, "PASS"
		}
		if !jsonEqual(wantValue, got["value"]) {
			t.Fatalf("withRaw=%v: value = %v; want %v", withRaw, got["value"], wantValue)
		}
		if got["verdict"] != wantVerdict {
			t.Fatalf("withRaw=%v: verdict = %v; want %s", withRaw, got["verdict"], wantVerdict)
		}
	}
}

func TestPacketVictimTest_ScoreRepRenormalisesSeqFreeRowsFromRaw(t *testing.T) {
	// CRITICAL round-1 finding (a): existing runs' events.jsonl has no seq, so
	// the old code never took the seq path. When the loaded rows lack seq but
	// raw-ta.jsonl exists, score must re-normalise from raw so old runs take it.
	// The raw here mirrors nightly paladin rep2 t=34.71: the cast (seq 42) goes
	// off while the mob is on the player; the stop/start switch to the bot sits
	// above the cast's seq, so the re-normalised rep counts the cast.
	trial := scenario.FindTrialByID("tank-paladin-no-taunt-tank-player")
	if trial == nil {
		t.Fatal("trial tank-paladin-no-taunt-tank-player not found")
	}
	samples := []rules.Row{vpSample(12.0, vpMob, vpPlayer), vpSample(13.2, vpMob, vpPlayer)}
	row := vpCast(13.38, nil)
	row["spell"] = "Hand of Reckoning"
	row["spell_id"] = int64(51302)
	delete(row, "seq")
	rows := []rules.Row{row}
	marks := []any{
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:00+00:00"},
		map[string]any{"kind": "fight_end", "wall": "2026-01-01T00:03:20+00:00", "ended": "duration"},
	}
	tmp := t.TempDir()
	rep := filepath.Join(tmp, "run", "tank-paladin-no-taunt-tank-player", "rep1")
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{"ok": true, "guids": vpGuids(),
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)}})
	var sampleRows, eventRows []any
	for _, r := range samples {
		sampleRows = append(sampleRows, r)
	}
	for _, r := range rows {
		eventRows = append(eventRows, r)
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), sampleRows)
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), eventRows)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), marks)
	writeJSONL(t, filepath.Join(rep, "raw-ta.jsonl"), []any{
		vpRaw("attack_start", 40, vpMob, vpPlayer, 0, "2026-01-01T00:00:%02d+00:00"),
		map[string]any{"seq": int64(42), "time": "2026-01-01T00:00:13+00:00", "kind": "spell_go",
			"source": map[string]any{"name": "b", "guid": vpBot},
			"target": map[string]any{"name": "m", "guid": vpMob}, "spell_id": int64(51302)},
		vpRaw("attack_stop", 43, vpMob, vpPlayer, 13, "2026-01-01T00:00:%02d+00:00"),
		vpRaw("attack_start", 44, vpMob, vpBot, 13, "2026-01-01T00:00:%02d+00:00"),
	})
	out := scoreRep(t, rep, trial)
	scored := readJSONL(t, filepath.Join(rep, "events.jsonl"))
	haveSeq := false
	for _, r := range scored {
		if r["seq"] != nil {
			haveSeq = true
		}
	}
	if !haveSeq {
		t.Fatal("scored events carry no seq; want the re-normalised rows")
	}
	got := vpRuleByID(out, "TNK-PAL-NO-TAUNT")
	if got == nil {
		t.Fatal("rule TNK-PAL-NO-TAUNT missing")
	}
	if !jsonEqual(int64(1), got["value"]) {
		t.Fatalf("value = %v; want 1", got["value"])
	}
	if got["verdict"] != "FAIL" {
		t.Fatalf("verdict = %v; want FAIL", got["verdict"])
	}
}
