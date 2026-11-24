package world

// #216: tbm_handshake, tbm_send and tbm are the TBM client over this session, the same tbmclient the CLI's
// tbm-check runs. The unit tests drive it with the contract's golden module (internal/forever/tbmclient's
// FakeModule) so the hello and command mapping is checked without a socket; the daemon test proves the session
// adapter is what the methods speak through (a module that answers nothing is the typed timeout).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tbmcontract "github.com/pjerra/forever-agent/contract/tbm"
	"github.com/pjerra/forever-agent/internal/forever/tbmclient"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// tbmFakeModule is the contract's golden module: each frame the client sends is answered with its case's records.
func tbmFakeModule(t *testing.T) *tbmclient.FakeModule {
	t.Helper()
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	return tbmclient.NewFakeModule(cases)
}

func TestServeTBMHelloAndCommand(t *testing.T) {
	// The chat hello: the module answers its trailer without switching the transport, and the answer types every
	// field the CLI's tbm-check prints.
	hello, err := serveTBMHello(context.Background(), tbmFakeModule(t), "chat", 2*time.Second)
	if err != nil {
		t.Fatalf("chat hello: %v", err)
	}
	if !hello.Complete || hello.Transport != "none" || hello.After != "chat" {
		t.Fatalf("chat hello = %+v", hello)
	}
	if hello.Proto != 1 || hello.MinAddon != 1 || hello.Version != "v0.1.0-forever" || hello.Check != "" {
		t.Fatalf("chat hello trailer = %+v", hello)
	}
	if len(hello.Caps) != 1 || hello.Caps[0] != "behavior" {
		t.Fatalf("chat hello caps = %v", hello.Caps)
	}

	// The add-on hello: the module answers party, so commands go out on the add-on transport from then on.
	hello, err = serveTBMHello(context.Background(), tbmFakeModule(t), "addon", 2*time.Second)
	if err != nil {
		t.Fatalf("add-on hello: %v", err)
	}
	if hello.Transport != "party" || hello.After != "addon" {
		t.Fatalf("add-on hello = %+v", hello)
	}

	// A command's answer: the ACK the case carries, the transport it went out on and the command itself.
	res, err := serveTBMCommand(context.Background(), tbmFakeModule(t), "addon", "action follow", 2*time.Second)
	if err != nil {
		t.Fatalf("action follow: %v", err)
	}
	if res.Command != "action follow" || res.Transport != "addon" {
		t.Fatalf("command answer = %+v", res)
	}
	if len(res.Acks) != 1 || res.Acks[0].Intent != "follow" || len(res.Errs) != 0 {
		t.Fatalf("action follow acks = %+v errs = %+v", res.Acks, res.Errs)
	}
}

// TestServeTBMRefusals: an unknown transport is the caller's error, a module that never answers is the typed
// timeout, and tbm_send refuses an empty command without sending anything.
func TestServeTBMRefusals(t *testing.T) {
	_, err := serveTBMHello(context.Background(), tbmFakeModule(t), "carrier-pigeon", time.Second)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeInvalidParams || !strings.Contains(re.Message, "carrier-pigeon") {
		t.Fatalf("unknown transport: %v", err)
	}
	silent := tbmFakeModule(t)
	silent.IgnoreRegistration = true
	_, err = serveTBMHello(context.Background(), silent, "addon", 50*time.Millisecond)
	if !errors.As(err, &re) || re.Code != rpc.CodeTimeout || !strings.Contains(re.Message, "tbm_handshake") {
		t.Fatalf("silent module: %v", err)
	}
}

// TestServeTBMDaemon: through the daemon's own socket the methods speak the session's add-on layer; a module that
// answers nothing is the typed timeout, and an empty command is refused before anything is sent.
func TestServeTBMDaemon(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "tbm_handshake", map[string]any{"wait_ms": 150}, -32004, "tbm_handshake: the module did not finish the hello")
	a.mustFail(t, "tbm_send", map[string]any{"command": " "}, -32602, "command is required")
	a.mustFail(t, "tbm", map[string]any{}, -32602, "command is required")
	// The old daemon's transport name (via) is accepted where transport is; a bad one names the four choices.
	a.mustFail(t, "tbm_send", map[string]any{"command": "roster", "via": "carrier-pigeon"}, -32602, "auto, addon, addon-self or chat")
	a.stop(t)
	f.wait(t)
}

// TestServeTBMDaemonDoesNotHoldTheQueue: a TBM exchange waits for a silent module off the session queue, so status
// and dungeon_stop answer while it waits, and the wait has a fixed upper bound (15 s, whatever wait_ms says).
// Mutation: running the exchange as one queued call blocks both until the wait ends.
func TestServeTBMDaemonDoesNotHoldTheQueue(t *testing.T) {
	if got := serveTBMTimeout(10 * 60 * 1000); got != 15*time.Second {
		t.Fatalf("wait_ms is capped at %s, not %s", got, 15*time.Second)
	}
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case line == ".tb state party":
			return commanderStateLines("Tanka")
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case strings.HasPrefix(line, ".tb run stop "):
			return []string{"run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=stopped step=1 steps=80 leg=0 legname=tunnel kind=pull " +
					"phase=pull note=stopped pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 #T",
				"run-end count=1 records=1 verb=stop #T"}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	// Warm the GM guard once (the first command of a session pays the probe's quiet window) so the timing below
	// measures the queue, not the guard.
	a.mustCall(t, "bot", map[string]any{"line": ".tb state party"})

	// The module answers nothing (the fake drops add-on traffic), so the exchange pumps for its whole wait.
	type reply struct {
		rerr *rpcError
		err  error
	}
	done := make(chan reply, 1)
	go func() {
		_, rerr, err := a.tryCall("tbm_send", map[string]any{"command": "roster", "transport": "addon", "wait_ms": 3000})
		done <- reply{rerr: rerr, err: err}
	}()
	// Let the exchange install its tap and enter its first pump slice.
	time.Sleep(200 * time.Millisecond)

	started := time.Now()
	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("status took %s while a TBM exchange waited (the queue was held)", elapsed)
	}

	started = time.Now()
	var out ServeDungeonRun
	json.Unmarshal(a.mustCall(t, "dungeon_stop", map[string]any{"lead_bot": "Tanka"}), &out)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("dungeon_stop took %s while a TBM exchange waited (the queue was held)", elapsed)
	}
	if len(out.Sent) != 1 || !strings.HasPrefix(out.Sent[0], ".tb run stop Tanka") {
		t.Fatalf("dungeon_stop sent %q\n%s", out.Sent, steps.joined())
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("tbm_send: %v", r.err)
		}
		if r.rerr == nil || r.rerr.Code != rpc.CodeTimeout {
			t.Fatalf("tbm_send on a silent module: %+v, want the typed timeout", r.rerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tbm_send did not end after its wait")
	}
	a.stop(t)
	f.wait(t)
}
