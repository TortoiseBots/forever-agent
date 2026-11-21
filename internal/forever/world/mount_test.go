package world

// W4 over the fake world server: the mount cast loop, the server's mount result, the dismount packet, the entry
// form's refusal, and the follow loop's RPC.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// buildMountValues is the character's own UNIT_FIELD_MOUNTDISPLAYID (retail bit 61) set to display.
func buildMountValues(guid wire.GUID, display int32) []byte {
	return buildUnitValuesUpdate(guid, unitValuesBody(61, func(v *wire.Buffer) { v.WriteI32(display) }))
}

// TestMountSpellMounts: a successful cast whose field update arrives is mounted, and the answer names the display.
func TestMountSpellMounts(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveKnownSpells = []int32{133}
	f.serveMountCast = true
	f.serveMountDisplay = 14338
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var m ServeMount
	json.Unmarshal(a.mustCall(t, "mount", map[string]any{"spell": 133}), &m)
	if !m.Mounted || m.DisplayID != 14338 || m.Spell != 133 || m.Attempts != 1 {
		t.Fatalf("mount %+v", m)
	}
	a.stop(t)
	f.wait(t)
}

// TestMountResultFailure: a cast that goes through but whose SMSG_MOUNT_RESULT is non-zero is a failure naming the
// result, and the method does not claim a mount.
func TestMountResultFailure(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveKnownSpells = []int32{133}
	f.serveMountCast = true
	f.serveMountResult = 3
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	_, rerr := a.call(t, "mount", map[string]any{"spell": 133})
	if rerr == nil {
		t.Fatal("a refused mount answered a result")
	}
	if !strings.Contains(rerr.Message, "SMSG_MOUNT_RESULT 3") {
		t.Fatalf("the refusal is %q", rerr.Message)
	}
	a.stop(t)
	f.wait(t)
}

// TestMountEntryNeedsItemUse: an item mount needs the item-use packet this round does not port; the method says so
// and sends nothing.
func TestMountEntryNeedsItemUse(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "mount", map[string]any{"entry": 5656}, -32003, "needs item use")
	f.mu.Lock()
	sawCast := f.serveCast
	f.mu.Unlock()
	if sawCast {
		t.Fatal("the entry form sent a cast")
	}
	// Both or neither is refused too, before anything is sent.
	a.mustFail(t, "mount", map[string]any{}, -32003, "exactly one")
	a.mustFail(t, "mount", map[string]any{"spell": 133, "entry": 5656}, -32003, "exactly one")
	a.stop(t)
	f.wait(t)
}

// TestDismountSendsCancelMountAura: a mounted character is dismounted by the empty CMSG_CANCEL_MOUNT_AURA, and the
// method answers once the field is zero.
func TestDismountSendsCancelMountAura(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveMounted = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	// The create burst's own mount field must be in the model before the dismount can be judged.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if modelMounted(t, a) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !modelMounted(t, a) {
		t.Fatalf("the model never showed the login's mount display\n%s", steps.joined())
	}

	var m ServeMount
	json.Unmarshal(a.mustCall(t, "dismount", nil), &m)
	if m.Mounted {
		t.Fatalf("dismount answered mounted: %+v", m)
	}
	f.mu.Lock()
	sawCancel := f.serveCancelMount
	f.mu.Unlock()
	if !sawCancel {
		t.Fatal("the fake never saw CMSG_CANCEL_MOUNT_AURA")
	}
	// A second dismount sends nothing more: the character is already on foot.
	a.mustCall(t, "dismount", nil)
	a.stop(t)
	f.wait(t)
}

// modelMounted says whether the session's own model shows a mount display, read from the self's unit row. A
// character on foot answers dismount with no packet, so a test that wants the packet must first see the login's
// mount field.
func modelMounted(t *testing.T, a *serveAgent) bool {
	t.Helper()
	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	var one ServeUnit
	json.Unmarshal(a.mustCall(t, "unit", map[string]any{"guid": st.GUID}), &one)
	return one.MountDisplayID != 0
}

// TestServeFollowStartStatusStop: the follow RPC starts one loop, reports it, refuses a second, and stops it with its
// result. The leader is not in the fake's model, so the loop is running with lead_seen false.
func TestServeFollowStartStatusStop(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_start", map[string]any{"lead": "Atank", "near": 3.0, "far": 8.0, "lost_s": 30}), &st)
	if !st.Running || st.State != "running" || st.Lead != "Atank" || st.Near != 3 || st.Far != 8 || st.LostS != 30 {
		t.Fatalf("follow_start %+v", st)
	}
	// A second loop is refused while the first runs.
	a.mustFail(t, "follow_start", map[string]any{"lead": "Bhealer"}, -32003, "already running")

	json.Unmarshal(a.mustCall(t, "follow_status", nil), &st)
	if !st.Running || st.LeadSeen {
		t.Fatalf("follow_status %+v", st)
	}
	json.Unmarshal(a.mustCall(t, "follow_stop", nil), &st)
	if st.Running || st.State != "stopped" || st.StoppedReason != "stopped" {
		t.Fatalf("follow_stop %+v", st)
	}
	// The bounds are refused before a loop starts, and starting clears the stopped one.
	a.mustFail(t, "follow_start", map[string]any{"lead": "Atank", "near": 8.0, "far": 8.0}, -32003, "far must be greater")
	a.mustFail(t, "follow_start", map[string]any{"lead": "Atank", "lost_s": 0}, -32003, "lost_s")
	json.Unmarshal(a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"}), &st)
	if !st.Running || st.State != "running" {
		t.Fatalf("the second follow_start %+v", st)
	}
	json.Unmarshal(a.mustCall(t, "follow_stop", nil), &st)
	a.stop(t)
	f.wait(t)
}

// TestServeActMountDismount: the act runner's mount and dismount verbs report through the same cast loop and packet,
// and a lost follow step fails the script.
func TestServeActMountDismount(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveKnownSpells = []int32{133}
	f.serveMountCast = true
	f.serveMountDisplay = 14338
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeState(t, a, "online", steps)

	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{
		"steps": []string{"mount spell=133", "dismount"},
	}), &res)
	if !res.OK {
		t.Fatalf("act mount/dismount failed: %+v", res)
	}
	joined := strings.Join(res.Lines, "\n")
	if !strings.Contains(joined, "step 1 mount ok spell 133, display 14338") {
		t.Fatalf("act lines:\n%s", joined)
	}
	if !strings.Contains(joined, "step 2 dismount ok on foot") {
		t.Fatalf("act lines:\n%s", joined)
	}
	f.mu.Lock()
	sawCancel := f.serveCancelMount
	f.mu.Unlock()
	if !sawCancel {
		t.Fatal("the act dismount sent no CMSG_CANCEL_MOUNT_AURA")
	}
	// A follow of a character the model does not hold is lost after a short window, which fails the step.
	json.Unmarshal(a.mustCall(t, "act", map[string]any{
		"steps": []string{"follow lead=Atank lost_s=1 dur=3s"},
	}), &res)
	if res.OK {
		t.Fatalf("act follow of an unseen leader answered ok: %+v", res)
	}
	if !strings.Contains(strings.Join(res.Lines, "\n"), "step 1 follow fail lost:") {
		t.Fatalf("act lines:\n%s", strings.Join(res.Lines, "\n"))
	}
	a.stop(t)
	f.wait(t)
}

// TestServeTeardownStopsFollow: a teardown ends the follow loop before the GM line and the logout, so no walk of
// ours can go out during the logout; the step says so.
func TestServeTeardownStopsFollow(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeState(t, a, "online", steps)

	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"}), &st)
	if !st.Running {
		t.Fatalf("follow_start %+v", st)
	}
	var td ServeTeardown
	json.Unmarshal(a.mustCall(t, "teardown", map[string]any{"gm": "keep"}), &td)
	if len(td.Steps) < 3 || td.Steps[0].Step != "follow" || td.Steps[0].Status != "ok" {
		t.Fatalf("teardown steps %+v", td.Steps)
	}
	if !strings.Contains(td.Steps[0].Detail, "follow stopped") {
		t.Fatalf("the follow step is %+v", td.Steps[0])
	}
	if td.Steps[1].Step != "gm" || td.Steps[2].Step != "logout" || td.Steps[2].Status != "ok" {
		t.Fatalf("teardown steps %+v", td.Steps)
	}
	if r := a.awaitResult(t); r.err != nil {
		t.Fatalf("the daemon ended with an error: %v\n%s", r.err, steps.joined())
	}
	f.wait(t)
}
