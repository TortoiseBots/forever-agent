package live

// The two new serve hands the trials use (SPEC #109): current-only health and the core's GM combat-log watch.

import (
	"encoding/json"
	"testing"
)

func TestClientCurrentHPAndCombatLogHands(t *testing.T) {
	fake := newFakeServe(t)
	c := NewClient(fake.rpc())
	ctx := ctxBackground()
	if err := c.GMSetCurrentHP(ctx, "0x00000000000000AB", 50); err != nil {
		t.Fatalf("GMSetCurrentHP: %v", err)
	}
	if err := c.GMCombatLog(ctx, "0x00000000000000AB", "group"); err != nil {
		t.Fatalf("GMCombatLog group: %v", err)
	}
	if err := c.GMCombatLog(ctx, "", "off"); err != nil {
		t.Fatalf("GMCombatLog off: %v", err)
	}
	fake.mu.Lock()
	calls := append([]fakeCall(nil), fake.calls...)
	fake.mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("%d calls, want 3: %+v", len(calls), calls)
	}
	var hp map[string]any
	if calls[0].method != "gm_set_hp" {
		t.Fatalf("first call %q, want gm_set_hp", calls[0].method)
	}
	_ = json.Unmarshal(calls[0].params, &hp)
	if hp["guid"] != "0x00000000000000AB" || numOrZero(hp["hp"]) != 50 || hp["current_only"] != true {
		t.Fatalf("gm_set_hp params %v, want the current-only hand", hp)
	}
	var watch map[string]any
	if calls[1].method != "gm_combatlog" {
		t.Fatalf("second call %q, want gm_combatlog", calls[1].method)
	}
	_ = json.Unmarshal(calls[1].params, &watch)
	if watch["mode"] != "group" || watch["guid"] != "0x00000000000000AB" {
		t.Fatalf("gm_combatlog group params %v", watch)
	}
	watch = map[string]any{}
	_ = json.Unmarshal(calls[2].params, &watch)
	if watch["mode"] != "off" {
		t.Fatalf("gm_combatlog off params %v", watch)
	}
	if _, has := watch["guid"]; has {
		t.Fatalf("off carried a guid: %v", watch)
	}
}

// TestGuardCombatLogWatchScope: a start is held to the trial's own units; off names none and is allowed.
func TestGuardCombatLogWatchScope(t *testing.T) {
	fake := newFakeServe(t)
	c := NewClient(fake.rpc())
	g := NewGuard(c, GuardOptions{PlayerGUID: "0x0000000000000001", DryRun: false})
	ctx := ctxBackground()
	if err := g.CombatLogWatch(ctx, "0x0000000000000002", "unit"); err == nil {
		t.Fatal("a start on a unit outside the trial's scope was allowed")
	}
	if err := g.CombatLogWatch(ctx, "0x0000000000000001", "unit"); err != nil {
		t.Fatalf("a start on the trial's own player was refused: %v", err)
	}
	if err := g.CombatLogWatch(ctx, "", "off"); err != nil {
		t.Fatalf("off was refused: %v", err)
	}
	if err := g.CombatLogWatch(ctx, "0x0000000000000001", "everyone"); err == nil {
		t.Fatal("a mode outside unit, group and off was allowed")
	}
	fake.mu.Lock()
	calls := append([]fakeCall(nil), fake.calls...)
	fake.mu.Unlock()
	var modes []string
	for _, call := range calls {
		if call.method != "gm_combatlog" {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(call.params, &p)
		m, _ := p["mode"].(string)
		modes = append(modes, m)
	}
	if len(modes) != 2 || modes[0] != "unit" || modes[1] != "off" {
		t.Fatalf("gm_combatlog modes %v, want the start and the off", modes)
	}
}
