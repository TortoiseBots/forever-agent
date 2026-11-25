package world

// The typed client (internal/forever/serveclient) against the REAL serve
// daemon: the fake world server behind the real rpc.Server (rpc.Typed with
// DisallowUnknownFields, the 64 KB body cap, the registered method table),
// called over a loopback port. A renamed field, a changed param or a new
// method table fails here, not in a hand-written fake that mirrors the
// client.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
	taworld "github.com/pjerra/forever-agent/internal/world"
)

// startClientDaemon starts the fake-backed daemon with a real generated
// token (auth on) or none (--no-auth) and writes its serve --dir the way
// forever serve does.
func startClientDaemon(t *testing.T, auth bool) (*serveAgent, string, func()) {
	t.Helper()
	f := newFakeServer(t)
	f.serveMode = true
	steps := &stepLog{}
	token := ""
	if auth {
		var err error
		if token, err = rpc.NewToken(); err != nil {
			t.Fatal(err)
		}
	}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.Token = token })
	dir := t.TempDir()
	port := portOf(t, a.addr)
	m := slotdir.Manifest{
		Server: "forever", Slot: "1", Holder: "forever", Character: "Testchar", Port: port, Dir: dir,
		Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: slotdir.TokenPath(dir, port),
		NoAuth: !auth,
	}
	if auth {
		if err := os.WriteFile(m.TokenFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return a, dir, func() { a.stop(t); f.close() }
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(addr[strings.LastIndex(addr, ":")+1:], "%d", &port); err != nil {
		t.Fatal(err)
	}
	return port
}

// strict decodes raw into out refusing any field out does not have.
func strict(t *testing.T, what string, raw []byte, out any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: the daemon's reply does not fit the client's type: %v\n%s", what, err, raw)
	}
}

// TestServeClientAgainstTheRealDaemon calls every method the daemon on main
// serves through the client, with the same replies strictly decoded into the
// client's types (a daemon field the client lacks fails).
func TestServeClientAgainstTheRealDaemon(t *testing.T) {
	a, dir, stop := startClientDaemon(t, true)
	defer stop()
	waitServeUnits(t, a)
	ctx := context.Background()
	c, err := serveclient.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	st, err := c.Status(ctx)
	if err != nil || st.State != "online" || st.Character == "" || st.GUID == "" {
		t.Fatalf("Status %+v, %v", st, err)
	}
	strict(t, "status", a.mustCall(t, "status", nil), new(serveclient.Status))

	units, err := c.Units(ctx, serveclient.UnitsParams{Within: 200, Limit: 5})
	if err != nil || len(units) == 0 || units[0].Entry == 0 || units[0].Distance <= 0 {
		t.Fatalf("Units %+v, %v", units, err)
	}
	strict(t, "units", a.mustCall(t, "units", map[string]any{"within": 200, "limit": 5}), new([]serveclient.Unit))
	one, err := c.Unit(ctx, string(units[0].GUID), "")
	if err != nil || one.GUID != units[0].GUID || one.Entry != units[0].Entry {
		t.Fatalf("Unit by guid %+v, %v", one, err)
	}
	strict(t, "unit", a.mustCall(t, "unit", map[string]any{"guid": units[0].GUID}), new(serveclient.Unit))

	party, err := c.Party(ctx)
	if err != nil || party.InGroup {
		t.Fatalf("Party %+v, %v", party, err)
	}
	strict(t, "party", a.mustCall(t, "party", nil), new(serveclient.Party))

	gm, err := c.GM(ctx, ".gm")
	if err != nil || len(gm.Lines) == 0 || gm.Sent != ".gm" {
		t.Fatalf("GM %+v, %v", gm, err)
	}
	strict(t, "gm", a.mustCall(t, "gm", map[string]any{"text": ".gm"}), new(serveclient.Command))

	bot, err := c.Bot(ctx, ".tb state", 0)
	if err != nil || !strings.Contains(strings.Join(bot.Lines, ""), "SERVE-BOT-ANSWER") {
		t.Fatalf("Bot %+v, %v", bot, err)
	}

	page, err := c.Events(ctx, 0, 0, 0)
	if err != nil || len(page.Events) != 0 || page.Next != 0 {
		t.Fatalf("Events %+v, %v", page, err)
	}
	strict(t, "events", a.mustCall(t, "events", map[string]any{}), new(serveclient.EventsPage))

	// A real refusal keeps its code through the client.
	_, err = c.Unit(ctx, "", "No Such Unit Anywhere")
	if code, ok := serveclient.CodeOf(err); !ok || code != rpc.CodeGame {
		t.Fatalf("Unit of nobody: %v, want a -32003 *Error", err)
	}

	out, err := c.Logout(ctx)
	if err != nil || out.Outcome != "complete" {
		t.Fatalf("Logout %+v, %v", out, err)
	}
}

// TestServeClientNoAuthDaemon: a daemon started --no-auth writes slot.json
// (naming a token file) but no token file; Open still reaches it.
func TestServeClientNoAuthDaemon(t *testing.T) {
	a, dir, stop := startClientDaemon(t, false)
	defer stop()
	_ = a
	c, err := serveclient.Open(dir)
	if err != nil {
		t.Fatalf("Open of a --no-auth dir: %v", err)
	}
	if st, err := c.Status(context.Background()); err != nil || st.State != "online" {
		t.Fatalf("Status %+v, %v", st, err)
	}
	// An auth daemon whose token file was deleted is refused at Open: no_auth
	// is not in its slot.json, so the client never goes out without a header.
	a2, dir2, stop2 := startClientDaemon(t, true)
	defer stop2()
	if err := os.Remove(filepath.Join(dir2, fmt.Sprintf("rpc-%d.token", portOf(t, a2.addr)))); err != nil {
		t.Fatal(err)
	}
	if _, err := serveclient.Open(dir2); err == nil {
		t.Fatal("Open of an auth dir without its token file succeeded")
	}
	// A dir that claims no_auth against a daemon that wants a token: typed 401.
	a3, dir3, stop3 := startClientDaemon(t, true)
	defer stop3()
	port3 := portOf(t, a3.addr)
	if err := os.Remove(filepath.Join(dir3, fmt.Sprintf("rpc-%d.token", port3))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir3, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir3, "slot.json"), []byte(strings.Replace(string(raw), "{", `{"no_auth":true,`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	c3, err := serveclient.Open(dir3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c3.Status(context.Background()); !errors.Is(err, serveclient.ErrUnauthorized) {
		t.Fatalf("Status without a token: %v, want ErrUnauthorized", err)
	}
}

// jsonKeys lists the JSON object keys of struct type t (embedded structs
// flattened, "-" skipped), sorted.
func jsonKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return keys
}

// TestServeClientTypesMatchTheDaemon: every client type has exactly the JSON
// keys of the daemon's type, both ways, so a field added, dropped or renamed
// on either side fails here.
func TestServeClientTypesMatchTheDaemon(t *testing.T) {
	for _, p := range []struct {
		name           string
		daemon, client reflect.Type
	}{
		{"status", reflect.TypeFor[ServeStatus](), reflect.TypeFor[serveclient.Status]()},
		{"unit", reflect.TypeFor[ServeUnit](), reflect.TypeFor[serveclient.Unit]()},
		{"aura", reflect.TypeFor[ServeAura](), reflect.TypeFor[serveclient.Aura]()},
		{"party", reflect.TypeFor[ServePartyInfo](), reflect.TypeFor[serveclient.Party]()},
		{"party member", reflect.TypeFor[ServePartyMember](), reflect.TypeFor[serveclient.PartyMember]()},
		{"party mark", reflect.TypeFor[ServeMark](), reflect.TypeFor[serveclient.PartyMark]()},
		{"command", reflect.TypeFor[ServeCommand](), reflect.TypeFor[serveclient.Command]()},
		{"logout", reflect.TypeFor[serveLogoutResult](), reflect.TypeFor[serveclient.Logout]()},
		{"events page", reflect.TypeFor[ServeEventsPage](), reflect.TypeFor[serveclient.EventsPage]()},
		{"event_log page", reflect.TypeFor[ServeEventPage](), reflect.TypeFor[serveclient.EventLogPage]()},
		{"event_log row", reflect.TypeFor[Event](), reflect.TypeFor[serveclient.Event]()},
		{"act result", reflect.TypeFor[ServeActResult](), reflect.TypeFor[serveclient.ActResult]()},
		{"combat row", reflect.TypeFor[CombatRow](), reflect.TypeFor[serveclient.CombatRow]()},
		{"combat actor", reflect.TypeFor[CombatActor](), reflect.TypeFor[serveclient.CombatActor]()},
		{"combat point", reflect.TypeFor[CombatPoint](), reflect.TypeFor[serveclient.CombatPoint]()},
	} {
		if d, c := jsonKeys(p.daemon), jsonKeys(p.client); !slices.Equal(d, c) {
			t.Errorf("%s: daemon keys %v, client keys %v", p.name, d, c)
		}
		// Same leaf kinds per key, so int32 vs bool drift is caught too.
		dk, ck := leafKinds(p.daemon), leafKinds(p.client)
		for k, kind := range dk {
			if ck[k] != kind {
				t.Errorf("%s.%s: daemon %s, client %s", p.name, k, kind, ck[k])
			}
		}
	}
	// A fully populated daemon unit and events page survive the client type
	// unchanged.
	g := GUID(wire.GUID{Low: 0x2, High: 0x1})
	unit := ServeUnit{GUID: g, Type: "creature", Entry: 7, Name: "Mob", X: 1, Y: 2, Z: 3, O: 4, Distance: 5, Distance2D: 6,
		Health: 7, MaxHealth: 8, Power: 9, MaxPower: 10, PowerType: 1, Level: 60, Class: 2, Target: g, InCombat: true,
		Dead: true, Ghost: true, Moving: true, Casting: true, SpellID: 11, MountDisplayID: 14338, CC: true,
		Auras: []ServeAura{{SpellID: 12, Caster: g, Stacks: 2, RemainingMS: 3000}}}
	roundTrip(t, "unit", unit, new(serveclient.Unit))
	page := ServeEventsPage{Events: []CombatRow{{Seq: 1, Kind: "melee", Time: "2026-01-02T03:04:05.006+00:00", MonoMS: 9,
		Source: CombatActor{GUID: g.String(), Name: "A"}, Target: CombatActor{GUID: g.String(), Name: "B"},
		SpellID: 5, Amount: 6, Reason: "x"}}, Next: 1, Oldest: 1, Dropped: 2}
	roundTrip(t, "events", page, new(serveclient.EventsPage))
	log := ServeEventPage{Events: []Event{{Seq: 3, Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), MonoMS: 9, Kind: "chat",
		Source: "A", SourceGUID: g.String(), Text: "hi", Party: true}}, Next: 3, More: true, Dropped: 2, Epoch: "abc", Reset: true}
	roundTrip(t, "event_log", log, new(serveclient.EventLogPage))
	roundTrip(t, "act", ServeActResult{Lines: []string{"step 1 wait ok"}, OK: true}, new(serveclient.ActResult))
}

// TestStatusKeysPinContract pins ServeStatus's JSON keys and verifies that every TA StatusInfo JSON key except the
// TA-stack counters unknown_opcodes and last_seq exists on ServeStatus.
func TestStatusKeysPinContract(t *testing.T) {
	pinned := []string{
		"addon", "base_time", "character", "combo_points", "combo_target", "dead", "error", "fall_land_z", "fall_ms",
		"falling",
		"gcd_ms", "ghost", "guid", "guild", "guild_name", "health", "in_combat", "latency_ms", "level", "locale", "map",
		"max_health", "max_power", "mono_ms", "o", "on_taxi", "parse_errors", "power", "power_type",
		"reconnect_attempt", "reconnect_limit", "server_packets", "state", "target", "team",
		"teleport_seq", "wall_offset_ms", "x", "y", "z", "z_unknown",
	}
	got := jsonKeys(reflect.TypeFor[ServeStatus]())
	if !slices.Equal(got, pinned) {
		t.Fatalf("ServeStatus JSON keys = %v, want pinned %v", got, pinned)
	}
	have := make(map[string]bool, len(got))
	for _, k := range got {
		have[k] = true
	}
	for _, k := range jsonKeys(reflect.TypeFor[taworld.StatusInfo]()) {
		if k == "unknown_opcodes" || k == "last_seq" {
			continue
		}
		if !have[k] {
			t.Errorf("TA StatusInfo JSON key %q missing from ServeStatus", k)
		}
	}
}

// leafKinds maps each JSON key of a struct type to its field's kind.
func leafKinds(t reflect.Type) map[string]string {
	out := map[string]string{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		k := f.Type.Kind()
		switch k {
		case reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			out[name] = "number"
		case reflect.String:
			out[name] = "string"
		case reflect.Struct:
			// A world GUID (struct, marshals to text) and a client GUID (string) are both text.
			if f.Type.Name() == "GUID" {
				out[name] = "string"
			} else {
				out[name] = "object"
			}
		case reflect.Slice:
			out[name] = "array"
		default:
			out[name] = k.String()
		}
	}
	return out
}

// roundTrip marshals the daemon's value, strictly decodes it into the
// client's type, marshals that back and requires the same JSON.
func roundTrip(t *testing.T, what string, daemon, client any) {
	t.Helper()
	raw, err := json.Marshal(daemon)
	if err != nil {
		t.Fatal(err)
	}
	strict(t, what, raw, client)
	back, err := json.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	json.Unmarshal(raw, &a)
	json.Unmarshal(back, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%s changed through the client type:\n daemon %s\n client %s", what, raw, back)
	}
}

// TestServeClientBodyCapMatchesTheDaemon: the client's 64 KB request refusal
// is the daemon's own cap. A call at the cap reaches the daemon; one byte
// over is refused by the client, and the same body sent raw is the daemon's
// Parse error.
func TestServeClientBodyCapMatchesTheDaemon(t *testing.T) {
	a, dir, stop := startClientDaemon(t, true)
	defer stop()
	c, err := serveclient.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	body := func(textLen int) []byte {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "gm",
			"params": map[string]string{"text": strings.Repeat("a", textLen)}})
		return b
	}
	atCap := 64<<10 - len(body(0))
	if len(body(atCap)) != 64<<10 {
		t.Fatalf("calibration: %d", len(body(atCap)))
	}
	_, err = c.GM(context.Background(), strings.Repeat("a", atCap))
	if errors.Is(err, serveclient.ErrRequestTooLarge) {
		t.Fatal("a request exactly at the cap was refused")
	}
	if code, ok := serveclient.CodeOf(err); ok && code == rpc.CodeParse {
		t.Fatalf("the daemon parse-errored a request at the cap: %v", err)
	}
	if _, err = c.GM(context.Background(), strings.Repeat("a", atCap+1)); !errors.Is(err, serveclient.ErrRequestTooLarge) {
		t.Fatalf("one byte over the cap gave %v", err)
	}
	_, rerr, terr := a.rawPost(t, body(atCap+1))
	if terr != nil || rerr == nil || rerr.Code != rpc.CodeParse {
		t.Fatalf("the daemon took %d bytes: %+v, %v", atCap+1, rerr, terr)
	}
}

// rawPost posts body as is (the harness's tryCall always builds its own).
func (a *serveAgent) rawPost(t *testing.T, body []byte) (json.RawMessage, *rpcError, error) {
	t.Helper()
	req, err := http.NewRequest("POST", "http://"+a.addr+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, nil, err
	}
	return env.Result, env.Error, nil
}

// TestServeClientMethodPin pins which methods the daemon registers today and decodes the ones that landed with serve
// W2-W4 (event_log, act, teardown) against the real daemon. Commander is served too and has its own real-daemon test
// below. Teardown ends the daemon, so it is the last call.
func TestServeClientMethodPin(t *testing.T) {
	ported, _ := ServeRPCMethods()
	for _, m := range []string{"status", "units", "unit", "party", "gm", "bot", "events", "logout", "commander",
		"event_log", "act", "teardown", "mount", "dismount", "follow_start", "follow_stop", "follow_status",
		"vendor_list", "buy", "sell", "gossip_hello", "gossip_select",
		"quest_hello", "quest_query", "quest_accept", "quest_complete", "quest_reward", "quest_abandon", "quest_log",
		"taxi",
		"loot", "loot_take", "loot_close", "open_object", "use_object", "gameobjects", "roll",
		"trainer_list", "train", "trade"} {
		if !slices.Contains(ported, m) {
			t.Errorf("%s is not a ported method any more: %v", m, ported)
		}
	}

	a, dir, stop := startClientDaemon(t, true)
	defer stop()
	_ = a
	c, err := serveclient.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	page, err := c.EventLog(ctx, 0, 0, 0)
	if err != nil || page.Epoch == "" || len(page.Events) == 0 || page.Reset {
		t.Fatalf("EventLog: %+v, %v", page, err)
	}
	res, err := c.Act(ctx, []string{"wait dur=10ms"}, 5*time.Second)
	if err != nil || !res.OK || len(res.Lines) != 1 || !strings.HasPrefix(res.Lines[0], "step 1 wait ok") {
		t.Fatalf("Act: %+v, %v", res, err)
	}
	td, err := c.Teardown(ctx, "keep")
	if err != nil || td.Failed != 0 || len(td.Steps) < 2 || td.Steps[len(td.Steps)-1].Step != "logout" {
		t.Fatalf("Teardown: %+v, %v", td, err)
	}
	if td.Steps[len(td.Steps)-1].Status != "ok" {
		t.Fatalf("Teardown logout step: %+v", td.Steps)
	}
}

// TestServeClientCommanderAgainstTheRealDaemon: commander is served by the
// real daemon, so the typed client reaches its handler, and a refused call
// keeps its typed code through the client.
func TestServeClientCommanderAgainstTheRealDaemon(t *testing.T) {
	a, dir, stop := startClientDaemon(t, true)
	defer stop()
	waitServeUnits(t, a)
	c, err := serveclient.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// bind without lead_bot is refused by the handler before the module is touched.
	_, err = c.Commander(context.Background(), serveclient.CommanderParams{Op: "bind"})
	if code, ok := serveclient.CodeOf(err); !ok || code != rpc.CodeInvalidParams {
		t.Fatalf("Commander bind without lead_bot: %v, want a rpc.CodeInvalidParams *Error", err)
	}
}
