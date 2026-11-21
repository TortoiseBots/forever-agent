package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// foreverStatusJSON is a serve status answer: the link lifecycle word
// "online" is the world (the turtle agent says "in_world").
const foreverStatusJSON = `{"state":"online","character":"Atank","guid":"0x00000000000000010000000000000001","level":60,"map":389,"x":-18.24,"y":-46.69,"z":-21.85,
 "health":5000,"max_health":5000,"power":100,"max_power":100,"power_type":1,"in_combat":false,"dead":false,"ghost":false,
 "server_packets":10,"parse_errors":0,"mono_ms":1000}`

// foreverPartyJSON is a serve party answer for a five-member party.
const foreverPartyJSON = `{"in_group":true,"leader":"0x00000000000000010000000000000001","members":[
 {"name":"Atank","guid":"0x00000000000000010000000000000001","online":true,"dead":false,"leader":true,"health":5000,"max_health":5000,"power_type":1,"power":100,"max_power":100,"level":60,"zone":2437},
 {"name":"Aheal","guid":"0x00000000000000020000000000000002","online":true,"dead":false,"leader":false,"health":4000,"max_health":4000,"power_type":0,"power":8000,"max_power":8000,"level":60,"zone":2437},
 {"name":"Adpsa","guid":"0x00000000000000030000000000000003","online":true,"dead":false,"leader":false,"health":3500,"max_health":3500,"power_type":0,"power":9000,"max_power":9000,"level":60,"zone":2437},
 {"name":"Adpsb","guid":"0x00000000000000040000000000000004","online":true,"dead":false,"leader":false,"health":3500,"max_health":3500,"power_type":3,"power":100,"max_power":100,"level":60,"zone":2437},
 {"name":"Adpsc","guid":"0x00000000000000050000000000000005","online":true,"dead":false,"leader":false,"health":3000,"max_health":3000,"power_type":0,"power":7000,"max_power":7000,"level":60,"zone":2437}]}`

// foreverCommanderJSON is a serve commander {op:status} answer in the daemon's own shape (hand-made from
// world.ServeCommander): one card per commander with typed members (max_hp, a pos object) and the run record
// beside them; sent is a list of lines and there is no status field.
const foreverCommanderJSON = `{"self":"Atank","sent":[".tb commander status"],"cards":[{
 "cmd":"Atank","cguid":"1","online":true,"lead":"Atank","bound":5,"bots":5,"system":false,"in_party":false,"raid":false,"player_led":false,"orders":0,
 "map":389,"pos":{"x":-18.24,"y":-46.69,"z":-21.85},
 "members":[
  {"bot":"Atank","guid":"0x00000000000000010000000000000001","class":"warrior","role":"tank","level":60,"hp":5000,"max_hp":5000,"power":100,"max_power":100,"power_type":"rage","alive":true,"lead":true,"map":389,"zone":2437,"pos":{"x":-18.24,"y":-46.69,"z":-21.85}},
  {"bot":"Aheal","guid":"0x00000000000000020000000000000002","class":"priest","role":"healer","level":60,"hp":4000,"max_hp":4000,"power":8000,"max_power":8000,"power_type":"mana","alive":true,"lead":false,"map":389,"zone":2437,"pos":{"x":-16.0,"y":-44.0,"z":-21.85}},
  {"bot":"Adpsa","guid":"0x00000000000000030000000000000003","class":"mage","role":"dps","level":60,"hp":3500,"max_hp":3500,"power":9000,"max_power":9000,"power_type":"mana","alive":true,"lead":false,"map":389,"zone":2437,"pos":{"x":-17.0,"y":-45.0,"z":-21.85}},
  {"bot":"Adpsb","guid":"0x00000000000000040000000000000004","class":"rogue","role":"dps","level":60,"hp":3500,"max_hp":3500,"power":100,"max_power":100,"power_type":"energy","alive":true,"lead":false,"map":389,"zone":2437,"pos":{"x":-19.0,"y":-47.0,"z":-21.85}},
  {"bot":"Adpsc","guid":"0x00000000000000050000000000000005","class":"warlock","role":"dps","level":60,"hp":3000,"max_hp":3000,"power":7000,"max_power":7000,"power_type":"mana","alive":true,"lead":false,"map":389,"zone":2437,"pos":{"x":-20.0,"y":-48.0,"z":-21.85}}],
 "run":{"lead":"Atank","plan":"ragefire_chasm","state":"running","step":1,"steps":80,"leg":0,"leg_name":"tunnel","kind":"pull","phase":"pull","note":"fighting","pulls":51,"deaths":0,"wipes":0,"pauses":0,"elapsed":39}}],"final":{}}`

// foreverUnitsJSON is a serve units {within:100} answer: two hostiles.
const foreverUnitsJSON = `[
 {"guid":"0xF130001C4E00AC91","type":"creature","entry":7001,"name":"Scorpid","x":-16.0,"y":-44.0,"z":-21.0,"o":0,"distance":5,"distance_2d":5,"health":800,"max_health":1000,"level":60,"in_combat":true,"dead":false,"moving":false},
 {"guid":"0x00000000000000010000000000000001","type":"player","name":"Atank","x":-18.24,"y":-46.69,"z":-21.85,"o":0,"distance":0,"distance_2d":0,"health":5000,"max_health":5000,"level":60,"in_combat":true,"dead":false,"moving":false}]`

// TestForeverCardBotsFromCommanderStatus is the failing case the round names:
// five members come from the commander status on the card; a card built from
// bot_view is empty of them.
func TestForeverCardBotsFromCommanderStatus(t *testing.T) {
	in := Inputs{
		Agent:     Agent{ID: "127.0.0.1:8774", Addr: "127.0.0.1:8774", Kind: AgentForever},
		Now:       t0,
		Status:    json.RawMessage(foreverStatusJSON),
		Party:     json.RawMessage(foreverPartyJSON),
		Commander: json.RawMessage(foreverCommanderJSON),
		Units:     json.RawMessage(foreverUnitsJSON),
	}
	c := BuildForeverCard(in)
	if !c.Online || c.Character != "Atank" || c.Level != 60 || c.Map != 389 {
		t.Fatalf("identity: %+v", c)
	}
	if len(c.Bots) != 5 {
		t.Fatalf("five members from commander status, got %d: %+v", len(c.Bots), c.Bots)
	}
	tank := c.Bots[0]
	if tank.Name != "Atank" || !tank.Lead || tank.Role != "tank" || tank.Class != "warrior" || tank.HPPct != 100 {
		t.Fatalf("tank: %+v", tank)
	}
	if c.Bots[1].Role != "healer" || c.Bots[1].HPPct != 100 {
		t.Fatalf("healer: %+v", c.Bots[1])
	}
	if c.Run.State != "running" || c.Label.Plan != "ragefire_chasm" {
		t.Fatalf("run: %+v label %+v", c.Run, c.Label)
	}
	// A card built from bot_view alone (no commander status) is empty of bots.
	turtle := BuildCard(Inputs{Agent: Agent{ID: "127.0.0.1:8768", Addr: "127.0.0.1:8768"}, Now: t0,
		Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(`{"state":"idle"}`)})
	if len(turtle.Bots) != 0 {
		t.Fatalf("turtle card without bot_view must have no bots: %+v", turtle.Bots)
	}
}

// TestForeverControlNeverCallsTBM pins that every Forever control goes to
// commander {op:cmd|run}: no tbm* or dungeon_* call on a Forever agent.
func TestForeverControlNeverCallsTBM(t *testing.T) {
	f := newForeverFakeServe(t)
	_, agent, srv := newForeverRig(t, f)
	for _, tc := range []struct {
		action, verb string
		wantOp       string
	}{
		{"order", "stay", "cmd"}, {"order", "follow", "cmd"}, {"order", "come", "cmd"},
		{"order", "attack", "cmd"}, {"order", "pull", "cmd"}, {"order", "release", "cmd"},
		{"run", "pause", "run"}, {"run", "resume", "run"}, {"run", "stop", "run"},
	} {
		body := `{"verb":` + jsonQuote(tc.verb) + `}`
		if tc.action == "run" {
			body = `{"verb":` + jsonQuote(tc.verb) + `,"confirm":true}`
		}
		code, out := postForeverControl(t, srv, agent.ID, tc.action, body)
		if code != 200 {
			t.Fatalf("%s %s: status %d (%v)", tc.action, tc.verb, code, out)
		}
		if out["rpc"] != "commander" {
			t.Fatalf("%s %s: rpc %v, want commander", tc.action, tc.verb, out["rpc"])
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c.Method, "tbm") || strings.HasPrefix(c.Method, "dungeon_") {
			t.Fatalf("Forever control called %q", c.Method)
		}
		if c.Method != "commander" {
			t.Fatalf("Forever control called %q, want commander", c.Method)
		}
		var p struct {
			Op string `json:"op"`
		}
		// A run verb reads the lead from a status call first; nothing but the commander is ever called.
		if err := json.Unmarshal(c.Params, &p); err != nil || (p.Op != "cmd" && p.Op != "run" && p.Op != "status") {
			t.Fatalf("commander params %s: want op cmd|run|status", c.Params)
		}
	}
	// Nine controls, and the status read each of the three run verbs makes first to name its lead.
	if len(f.calls) != 12 {
		t.Fatalf("nine controls and three lead reads, got %d: %v", len(f.calls), f.calls)
	}
}

// TestForeverGUIDKeyDistinct pins that two serve guids that share their low
// 64 bits are two units on the card, keyed by the serveclient Key.
func TestForeverGUIDKeyDistinct(t *testing.T) {
	lo := "0x00000000000000001111111122222222"
	hi := "0x00000000000000021111111122222222"
	in := Inputs{
		Agent:     Agent{ID: "127.0.0.1:8774", Addr: "127.0.0.1:8774", Kind: AgentForever},
		Now:       t0,
		Status:    json.RawMessage(foreverStatusJSON),
		Party:     json.RawMessage(foreverPartyJSON),
		Commander: json.RawMessage(foreverCommanderJSON),
		Units: json.RawMessage(`[{"guid":` + jsonQuote(lo) + `,"type":"creature","entry":1,"name":"Lo","x":0,"y":0,"z":0,"o":0,"distance":5,"distance_2d":5,"health":100,"max_health":100,"level":1},
		 {"guid":` + jsonQuote(hi) + `,"type":"creature","entry":2,"name":"Hi","x":1,"y":1,"z":0,"o":0,"distance":6,"distance_2d":6,"health":100,"max_health":100,"level":1}]`),
	}
	c := BuildForeverCard(in)
	if len(c.Mobs) != 2 {
		t.Fatalf("two guids, two mobs: %+v", c.Mobs)
	}
	if c.Mobs[0].GUID == c.Mobs[1].GUID {
		t.Fatalf("guids collapsed: %q", c.Mobs[0].GUID)
	}
	if keyOf(c.Mobs[0].GUID) == keyOf(c.Mobs[1].GUID) {
		t.Fatalf("keys collapsed for %q and %q", c.Mobs[0].GUID, c.Mobs[1].GUID)
	}
}

// TestTurtleAgentUnchanged pins that a turtle agent's card and controls are
// exactly what they were: bot_view bots, dungeon_* run verbs, tbm-free reads.
func TestTurtleAgentUnchanged(t *testing.T) {
	in := Inputs{Agent: Agent{ID: "127.0.0.1:8768", Addr: "127.0.0.1:8768"}, Now: t0, Status: json.RawMessage(statusJSON),
		Dungeon: json.RawMessage(dungeonJSON("leg 0 (one): pull group 2 of 3", 150*time.Second)), View: json.RawMessage(viewJSON),
		Plan: tinyPlan(t)}
	c := BuildCard(in)
	if len(c.Bots) != 3 || c.Bots[0].Name != "Forthur" {
		t.Fatalf("turtle bots from bot_view broke: %+v", c.Bots)
	}
	if c.Run.State != "running" || c.Run.Mode != "remote" {
		t.Fatalf("turtle run broke: %+v", c.Run)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
