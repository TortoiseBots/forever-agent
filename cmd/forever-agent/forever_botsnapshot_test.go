package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/trials/realism"
)

// botSnapshotTestServer starts a real rpc server with bot-snapshot's methods: a druid character at
// 60 wearing three items, a grouped priest, a member with no unit row, and an empty neck slot.
func botSnapshotTestServer(t *testing.T, token string) int {
	t.Helper()
	srv := rpc.NewServer()
	srv.SetToken(token)
	type statusAnswer struct {
		Character string `json:"character"`
		GUID      string `json:"guid"`
		Level     int32  `json:"level"`
	}
	srv.Register("status", rpc.Typed(func(context.Context, struct{}) (statusAnswer, error) {
		return statusAnswer{Character: "Hiddenhero", GUID: "0x01", Level: 60}, nil
	}))
	srv.Register("unit", rpc.Typed(func(_ context.Context, p struct {
		GUID string `json:"guid"`
	}) (map[string]any, error) {
		return map[string]any{"guid": p.GUID, "class": 11, "level": 60}, nil
	}))
	srv.Register("equipment", rpc.Typed(func(context.Context, struct {
		Names bool `json:"names"`
	}) ([]map[string]any, error) {
		return []map[string]any{
			{"slot": 0, "slot_name": "head", "item_entry": 22267, "name": "Spellweaver's Turban", "quality": 3},
			{"slot": 15, "slot_name": "main_hand", "item_entry": 13964, "name": "Witchblade", "quality": 4},
			{"slot": 2, "slot_name": "shoulders", "item_entry": 15067, "name": "Ironfeather Shoulders"},
			{"slot": 1, "slot_name": "neck", "item_entry": 0, "name": ""},
		}, nil
	}))
	srv.Register("item_info", rpc.Typed(func(_ context.Context, p struct {
		Entries []int32 `json:"entries"`
	}) (map[string]any, error) {
		rows := make([]map[string]any, 0, len(p.Entries))
		for _, entry := range p.Entries {
			rows = append(rows, map[string]any{"item_entry": entry, "found": true,
				"name": "item", "quality": 3, "item_level": 62, "required_level": 58})
		}
		return map[string]any{"items": rows}, nil
	}))
	srv.Register("bot_view", rpc.Typed(func(context.Context, struct {
		Bots   []string `json:"bots,omitempty"`
		WaitMS int      `json:"wait_ms,omitempty"`
	}) (map[string]any, error) {
		return map[string]any{"party": true, "self": "Hiddenhero", "bots": []map[string]any{
			{"name": "Hiddenpriest", "guid": "0x02", "in_party": true, "in_view": true,
				"unit": map[string]any{"guid": "0x02", "name": "Hiddenpriest", "class": 5, "level": 60}},
			{"name": "Anothermate", "guid": "0x03", "in_party": true, "in_view": false},
		}}, nil
	}))
	return rpcTestServerPort(t, srv)
}

// TestBotSnapshotReadsTheServe is the end-to-end read: a serve dir in, a snapshot file out, no
// character name anywhere, and the file passes the snapshot loader.
func TestBotSnapshotReadsTheServe(t *testing.T) {
	token := rpcCLITestToken(t)
	port := botSnapshotTestServer(t, token)
	dir := rpcCLITestDir(t, port, token)
	out := filepath.Join(t.TempDir(), "snap.json")
	code, stdout, stderr := runCLI("forever", "bot-snapshot", "--dir", dir, "--out", out)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "wrote ") {
		t.Fatalf("the command must name the file it wrote: %q", stdout)
	}
	if !strings.Contains(stderr, "Anothermate") || !strings.Contains(stderr, "skipped") {
		t.Fatalf("a member with no unit row must be reported on stderr: %q", stderr)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Hiddenhero", "Hiddenpriest", "Anothermate"} {
		if strings.Contains(string(raw), name) {
			t.Fatalf("the snapshot carries the character name %q:\n%s", name, raw)
		}
	}
	snap, err := realism.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the snapshot the command wrote does not load: %v\n%s", err, raw)
	}
	if len(snap.Bots) != 2 || snap.Bots[0].ID != "bot1" || snap.Bots[0].Class != "druid" || snap.Bots[0].Level != 60 {
		t.Fatalf("self row: %+v", snap.Bots[0])
	}
	head := snap.Bots[0].Items[0]
	if head.Slot != "head" || head.ItemID != 22267 || head.ItemLevel != 62 || *head.Quality != 3 {
		t.Fatalf("the head row must carry slot, id, item level and quality: %+v", head)
	}
	if len(snap.Bots[0].Items) != 3 {
		t.Fatalf("the empty neck slot and the id-0 row must be left out: %+v", snap.Bots[0].Items)
	}
	for _, item := range snap.Bots[0].Items {
		if item.EnchantID != nil {
			t.Fatalf("the serve reads no enchant, so no item may carry an enchant id: %+v", item)
		}
	}
	if snap.Bots[1].ID != "bot2" || snap.Bots[1].Class != "priest" || snap.Bots[1].ItemsKnown {
		t.Fatalf("the grouped row must carry class and level only: %+v", snap.Bots[1])
	}
	if len(snap.Bots[0].Notes) == 0 || len(snap.Bots[1].Notes) == 0 {
		t.Fatalf("every bot must state the fields the snapshot could not fill")
	}
}

// TestBotSnapshotStdoutAndJSONShape pins the default (stdout) form and the format word.
func TestBotSnapshotStdoutAndJSONShape(t *testing.T) {
	token := rpcCLITestToken(t)
	port := botSnapshotTestServer(t, token)
	dir := rpcCLITestDir(t, port, token)
	code, stdout, stderr := runCLI("forever", "bot-snapshot", "--dir", dir)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if doc["format"] != realism.Format {
		t.Fatalf("format %v, want %s", doc["format"], realism.Format)
	}
	if doc["source"] != "forever bot-snapshot" {
		t.Fatalf("source %v", doc["source"])
	}
}

// TestBotSnapshotWaitAndBotsFlags pins the flags the serve's own parameters need.
func TestBotSnapshotWaitAndBotsFlags(t *testing.T) {
	if code, _, errOut := runCLI("forever", "bot-snapshot"); code != exitUsage || !strings.Contains(errOut, "usage:") {
		t.Fatalf("no --dir must be a usage error: code %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("forever", "bot-snapshot", "--dir", t.TempDir(), "--wait", "5000"); code != exitUsage ||
		!strings.Contains(errOut, "0-2000") {
		t.Fatalf("--wait over the serve's bound must be refused: code %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("forever", "bot-snapshot", "--dir", t.TempDir()); code != exitUsage {
		t.Fatalf("a dir with no slot.json must be refused: code %d %q", code, errOut)
	}
}

// TestTakeBotSnapshotPropagatesAServeError pins that a failed read fails the command instead of
// writing a half snapshot.
func TestTakeBotSnapshotPropagatesAServeError(t *testing.T) {
	token := rpcCLITestToken(t)
	srv := rpc.NewServer()
	srv.SetToken(token)
	srv.Register("status", rpc.Typed(func(context.Context, struct{}) (string, error) {
		return "", &rpc.Error{Code: -32000, Message: "no session"}
	}))
	port := rpcTestServerPort(t, srv)
	dir := rpcCLITestDir(t, port, token)
	code, stdout, stderr := runCLI("forever", "bot-snapshot", "--dir", dir)
	if code != exitSlotRefused {
		t.Fatalf("code %d, want %d (stderr %q)", code, exitSlotRefused, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("a failed read must write nothing: %q", stdout)
	}
	if !strings.Contains(stderr, "status:") {
		t.Fatalf("the error must name the call: %q", stderr)
	}
}

// TestBotSnapshotValidatesBeforeWriting pins the low finding: a serve whose character is not in the
// world (level 0) would write a file the loader refuses, so the command must refuse before writing.
func TestBotSnapshotValidatesBeforeWriting(t *testing.T) {
	token := rpcCLITestToken(t)
	srv := rpc.NewServer()
	srv.SetToken(token)
	srv.Register("status", rpc.Typed(func(context.Context, struct{}) (map[string]any, error) {
		return map[string]any{"guid": "0x01", "level": 0}, nil
	}))
	srv.Register("unit", rpc.Typed(func(context.Context, struct {
		GUID string `json:"guid"`
	}) (map[string]any, error) {
		return map[string]any{"guid": "0x01", "class": 11, "level": 0}, nil
	}))
	srv.Register("equipment", rpc.Typed(func(context.Context, struct {
		Names bool `json:"names"`
	}) ([]map[string]any, error) {
		return nil, nil
	}))
	srv.Register("bot_view", rpc.Typed(func(context.Context, struct {
		Bots   []string `json:"bots,omitempty"`
		WaitMS int      `json:"wait_ms,omitempty"`
	}) (map[string]any, error) {
		return map[string]any{"bots": []map[string]any{}}, nil
	}))
	port := rpcTestServerPort(t, srv)
	dir := rpcCLITestDir(t, port, token)
	out := filepath.Join(t.TempDir(), "snap.json")
	code, stdout, stderr := runCLI("forever", "bot-snapshot", "--dir", dir, "--out", out)
	if code == exitOK {
		t.Fatalf("a level 0 character must not produce a successful snapshot (stdout %q)", stdout)
	}
	if !strings.Contains(stderr, "loadable snapshot") || !strings.Contains(stderr, "level") {
		t.Fatalf("the error must say the snapshot does not load and why: %q", stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("the command wrote a file the loader refuses: %v", err)
	}
}

// rpcTestServerPort starts a real rpc server for a test and returns its port.
func rpcTestServerPort(t *testing.T, srv *rpc.Server) int {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return rpcCLITestPort(t, ts)
}
