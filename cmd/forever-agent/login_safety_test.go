//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/config"
	"github.com/pjerra/forever-agent/internal/game"
	"github.com/pjerra/forever-agent/internal/opcode"
	"github.com/pjerra/forever-agent/internal/packet"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/worldconn/worldtest"
)

// Review I1: a logout the server keeps refusing (in combat) is said once, retried, and
// then ends the CLI with exitLogoutRefused and the socket closed, within the retry window
// rather than after a 35 s hang.
func TestRefusedLogoutIsRetriedThenExits(t *testing.T) {
	setTimings(t, 1500*time.Millisecond, 200*time.Millisecond, 30*time.Second)
	requests := make(chan int, 1)
	cfg, done := worldScript(t, func(srv *worldtest.Server) error {
		n, err := answerLogouts(srv, func(int) uint32 { return 1 })
		requests <- n
		return err
	})
	start := time.Now()
	code, _, errOut := runCLI("login", "--config", cfg, "--stay", "100ms", "harness", "Harnessb")
	if code != exitLogoutRefused || time.Since(start) > 5*time.Second {
		t.Fatalf("code %d after %s, stderr %q", code, time.Since(start), errOut)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := <-requests; n < 3 {
		t.Fatalf("%d logout requests, want retries", n)
	}
	if strings.Count(errOut, "logout refused: in combat") != 1 || !strings.Contains(errOut, "still refused") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestRefusedLogoutRetriesUntilAccepted(t *testing.T) {
	setTimings(t, 5*time.Second, 100*time.Millisecond, 30*time.Second)
	requests := make(chan int, 1)
	cfg, done := worldScript(t, func(srv *worldtest.Server) error {
		n, err := answerLogouts(srv, func(n int) uint32 {
			if n < 3 {
				return 1
			}
			return 0
		})
		requests <- n
		return err
	})
	code, _, stderr := runCLI("login", "--config", cfg, "--stay", "100ms", "harness", "Harnessb")
	if code != exitOK || !strings.Contains(stderr, "logout: complete") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := <-requests; n != 3 {
		t.Fatalf("%d logout requests, want 3", n)
	}
}

// Review I2 and I3: after the server drops the session, the RPC stays up so status shows
// the disconnect and its reason and events hold the final session event, with the echoed
// account name redacted; the CLI then exits 6. A Ctrl-C ends the wait early.
func TestStatusAfterDisconnect(t *testing.T) {
	setTimings(t, 35*time.Second, 2*time.Second, 20*time.Second)
	cfg, done := worldScript(t, func(srv *worldtest.Server) error {
		pinfo := packet.NewWriter().U8(uint8(game.ChatSystem)).U32(0).GUID(0)
		text := "Player Harnessb is Account: harness (id 5), password " + safetyPassword
		srv.Send(opcode.SMSGMessagechat, pinfo.U32(uint32(len(text)+1)).CString(text).U8(0).Bytes())
		srv.Send(opcode.SMSGNotification, []byte("HARNESS "+safetyPassword)) // no NUL: a parse error
		time.Sleep(300 * time.Millisecond)
		return nil // the deferred Close drops the socket
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	codeC, exited := make(chan int, 1), make(chan struct{})
	go func() {
		defer close(exited)
		codeC <- run(ctx, []string{"login", "--config", cfg, "harness", "Harnessb"}, &bytes.Buffer{}, &stderr)
	}()
	t.Cleanup(func() { cancel(); <-exited }) // before setTimings restores the timings
	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for addr == "" && time.Now().Before(deadline) {
		if _, rest, ok := strings.Cut(stderr.String(), "rpc: listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, "/rpc")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("no rpc address: %q", stderr.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	for time.Now().Before(deadline) {
		if out := rpcCall(t, addr, "status", "{}"); out != nil {
			if st, _ = out["result"].(map[string]any); st["state"] == "disconnected" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st["state"] != "disconnected" || !strings.Contains(fmt.Sprint(st["error"]), "disconnected by the server") {
		t.Fatalf("status %v (stderr %q)", st, stderr.String())
	}
	// status turns disconnected before the session event is appended: read the events until it is there.
	var page map[string]any
	var raw []byte
	for until := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		page = rpcCall(t, addr, "events", `{"since":0}`)["result"].(map[string]any)
		raw, _ = json.Marshal(page)
		if strings.Contains(string(raw), `"state":"disconnected"`) || time.Now().After(until) {
			break
		}
	}
	if !strings.Contains(string(raw), `"state":"disconnected"`) || !strings.Contains(fmt.Sprint(page), "Account: <redacted> (id 5), password <redacted>") {
		t.Fatalf("events %s", raw)
	}
	if out := rpcCall(t, addr, "chat", `{"type":"say","text":"hi"}`); errCode(out) != -32002 {
		t.Fatalf("action after disconnect: %v", out)
	}
	select {
	case code := <-codeC:
		t.Fatalf("exited %d during the grace window", code)
	default:
	}
	cancel()
	select {
	case code := <-codeC:
		if code != exitDisconnected {
			t.Fatalf("code %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl-C did not end the grace window")
	}
	all := stderr.String() + string(raw)
	for _, secret := range []string{"HARNESS", "harness", safetyPassword} {
		if strings.Contains(all, secret) {
			t.Errorf("%q leaked: %q", secret, all)
		}
	}
	if !strings.Contains(stderr.String(), "no dump") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestDisconnectGraceEndsOnItsOwn(t *testing.T) {
	setTimings(t, 35*time.Second, 2*time.Second, 300*time.Millisecond)
	cfg, done := worldScript(t, func(*worldtest.Server) error { return nil })
	start := time.Now()
	code, _, errOut := runCLI("login", "--config", cfg, "harness", "Harnessb")
	if code != exitDisconnected || time.Since(start) < 300*time.Millisecond || !strings.Contains(errOut, "after the disconnect") {
		t.Fatalf("code %d after %s, stderr %q", code, time.Since(start), errOut)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Review I1: after the first Ctrl-C the signal goes back to its default action, so a
// second one ends the process at once. Run in a child process.
func TestSecondInterruptKills(t *testing.T) {
	if os.Getenv("TURTLE_AGENT_SIGNAL_CHILD") == "1" {
		ctx := signalContext()
		fmt.Println("ready")
		<-ctx.Done()
		fmt.Println("first")
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondInterruptKills$")
	cmd.Env = append(os.Environ(), "TURTLE_AGENT_SIGNAL_CHILD=1")
	var out syncBuffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitOut := func(s string) {
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), s); {
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatalf("child never printed %q: %q", s, out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitOut("ready")
	cmd.Process.Signal(os.Interrupt)
	waitOut("first")
	start := time.Now()
	cmd.Process.Signal(os.Interrupt)
	err := cmd.Wait()
	ee, ok := err.(*exec.ExitError)
	if !ok || time.Since(start) > 5*time.Second {
		t.Fatalf("child: %v after %s", err, time.Since(start))
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("child did not die of SIGINT: %v", err)
	}
}

// Without an rpc_token in the settings the agent generates one: rpc-<port>.token next to the
// settings file, mode 0600, required on every call and removed again at exit.
func TestLoginGeneratesRPCTokenFile(t *testing.T) {
	setTimings(t, 35*time.Second, 2*time.Second, 20*time.Second)
	cfg, done := worldScript(t, func(srv *worldtest.Server) error {
		time.Sleep(300 * time.Millisecond)
		return nil
	})
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(raw), `"rpc_token":"`+testRPCToken+`",`, "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	codeC, exited := make(chan int, 1), make(chan struct{})
	go func() {
		defer close(exited)
		codeC <- run(ctx, []string{"login", "--config", cfg, "harness", "Harnessb"}, &bytes.Buffer{}, &stderr)
	}()
	t.Cleanup(func() { cancel(); <-exited })
	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for addr == "" && time.Now().Before(deadline) {
		if _, rest, ok := strings.Cut(stderr.String(), "rpc: listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, "/rpc")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("no rpc address: %q", stderr.String())
	}
	_, port, _ := net.SplitHostPort(addr)
	path := filepath.Join(filepath.Dir(cfg), "rpc-"+port+".token")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v: %v", info, err)
	}
	tok, err := rpc.ReadTokenFile(path)
	if err != nil || len(tok) < config.MinTokenLen {
		t.Fatalf("token %q: %v", tok, err)
	}
	if strings.Contains(stderr.String(), tok) || !strings.Contains(stderr.String(), path) {
		t.Fatalf("stderr must name the file and never the token: %q", stderr.String())
	}
	call := func(auth string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"status"}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := call(""); code != http.StatusUnauthorized || strings.Contains(body, "state") {
		t.Fatalf("no token: %d %q", code, body)
	}
	if code, _ := call("Bearer " + tok + "x"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, body := call("Bearer " + tok); code != http.StatusOK || !strings.Contains(body, `"state"`) {
		t.Fatalf("right token: %d %q", code, body)
	}
	// The fleet supervisor finds the same token through the settings dir.
	if got := fleetToken(config.Settings{}, cfg, addr); got != tok {
		t.Fatalf("fleetToken %q, want the file's token", got)
	}
	<-done
	cancel()
	<-exited
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token file left behind: %v", err)
	}
}

func TestLoginAuthOffServesWithoutToken(t *testing.T) {
	setTimings(t, 35*time.Second, 2*time.Second, 20*time.Second)
	cfg, done := worldScript(t, func(srv *worldtest.Server) error {
		time.Sleep(300 * time.Millisecond)
		return nil
	})
	raw, _ := os.ReadFile(cfg)
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(raw), `"rpc_token":"`+testRPCToken+`"`, `"rpc_auth":"off"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		run(ctx, []string{"login", "--config", cfg, "harness", "Harnessb"}, &bytes.Buffer{}, &stderr)
	}()
	t.Cleanup(func() { cancel(); <-exited })
	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for addr == "" && time.Now().Before(deadline) {
		if _, rest, ok := strings.Cut(stderr.String(), "rpc: listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, "/rpc")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("no rpc address: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "authentication is OFF") {
		t.Fatalf("no warning: %q", stderr.String())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg), "rpc-*.token")); len(m) != 0 {
		t.Fatalf("token file written with auth off: %v", m)
	}
	resp, err := http.Post("http://"+addr+"/rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	<-done
}
