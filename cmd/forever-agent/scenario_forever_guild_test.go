package main

// TestScenarioBotGuildInviteCheck (#113): the shipped scenarios/forever-bot-guild-invite.json runs through
// `scenario run --slot A=DIR` against a fake serve slot that plays the bot module's side. A starts guildless
// (status carries no guild key), whispers the bot, the bot's invite lands as a guild event, the accept joins and
// status names the guild, and the finally step's leave clears it. A run that never whispers, never accepts or
// leaves the guild set fails here.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGuildServe is one serve slot whose guild state the test reads back.
type fakeGuildServe struct {
	tok    string
	char   string
	bot    string
	mu     sync.Mutex
	seq    uint64
	guild  string // the guild's guid text while A is in the guild
	name   string // the guild's name while A is in the guild
	events []map[string]any

	whispers int
	accepts  int
	leaves   int
}

func (f *fakeGuildServe) add(kind, source, text string, data map[string]any) {
	f.seq++
	f.events = append(f.events, map[string]any{
		"seq": f.seq, "time": time.Now().UTC().Format(time.RFC3339Nano),
		"kind": kind, "source": source, "text": text, "data": data,
	})
}

func (f *fakeGuildServe) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		enc := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}) }
		fail := func(msg string) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32003, "message": msg}})
		}
		switch req.Method {
		case "status":
			f.mu.Lock()
			out := map[string]any{"character": f.char, "state": "online",
				"guid": "0x" + strings.Repeat("0", 16) + fmt.Sprintf("%016X", uint64(len(f.char)))}
			if f.guild != "" {
				out["guild"] = f.guild
				out["guild_name"] = f.name
			}
			f.mu.Unlock()
			enc(out)
		case "event_log":
			var p struct {
				Since uint64 `json:"since"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			var rows []map[string]any
			for _, e := range f.events {
				if e["seq"].(uint64) > p.Since {
					rows = append(rows, e)
				}
			}
			next := f.seq
			f.mu.Unlock()
			if rows == nil {
				rows = []map[string]any{}
			}
			enc(map[string]any{"events": rows, "next": next, "oldest": uint64(0), "more": false,
				"dropped": 0, "epoch": "test", "last": next})
		case "chat":
			var p struct {
				Type string `json:"type"`
				To   string `json:"to"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Type != "whisper" || !strings.EqualFold(p.To, f.bot) {
				fail("the fake only answers a whisper to the bot")
				return
			}
			f.mu.Lock()
			f.whispers++
			// The module's answer: a guild invite from the bot's guild, whispered as the invite packet's event.
			f.add("guild", f.bot, "invite", map[string]any{"guild_name": "Guild of Bots", "inviter": f.bot})
			f.mu.Unlock()
			enc(map[string]any{})
		case "accept_guild_invite":
			f.mu.Lock()
			hasInvite := false
			for _, e := range f.events {
				if e["kind"] == "guild" && e["text"] == "invite" {
					hasInvite = true
				}
			}
			if !hasInvite {
				f.mu.Unlock()
				fail("no pending guild invite")
				return
			}
			f.accepts++
			f.guild = "0x" + strings.Repeat("0", 30) + "02"
			f.name = "Guild of Bots"
			f.add("guild", f.char, "joined", map[string]any{"guild_name": f.name})
			f.mu.Unlock()
			enc(map[string]any{})
		case "guild_leave":
			f.mu.Lock()
			if f.guild == "" {
				f.mu.Unlock()
				fail("not in a guild")
				return
			}
			f.leaves++
			f.guild, f.name = "", ""
			f.mu.Unlock()
			enc(map[string]any{})
		default:
			enc(map[string]any{})
		}
	})
}

func TestScenarioBotGuildInviteCheck(t *testing.T) {
	f := &fakeGuildServe{
		tok:  "tok-" + strings.Repeat("c", 40),
		char: "Atank",
		bot:  "Guildbot",
	}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	dir := scenarioServeSlot(t, srv.Listener.Addr().(*net.TCPAddr).Port, f.tok)

	file, err := filepath.Abs(filepath.Join("..", "..", "scenarios", "forever-bot-guild-invite.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := scenarioCmd(context.Background(), []string{"run", "--out", out, "--slot", "A=" + dir, file}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("scenario run: exit %d stderr %q stdout %q", code, stderr.String(), stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Exit   int `json:"exit"`
		Passed int `json:"passed"`
		Failed int `json:"failed"`
		Errors int `json:"errors"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Exit != 0 || res.Failed != 0 || res.Errors != 0 || res.Passed == 0 {
		t.Fatalf("result %+v (%s)", res, raw)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.whispers != 1 || f.accepts != 1 || f.leaves != 1 {
		t.Fatalf("the fake saw %d whisper(s), %d accept(s), %d leave(s), want one each", f.whispers, f.accepts, f.leaves)
	}
	if f.guild != "" || f.name != "" {
		t.Fatalf("the guild survived the finally step's leave: %q %q", f.guild, f.name)
	}
}
