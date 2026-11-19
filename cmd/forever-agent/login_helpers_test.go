package main

// The helpers the login CLI tests share. They run a scripted world server in the test process and need no Unix
// process machinery, so they compile on every platform; the signal-driven tests live in login_safety_test.go.

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/auth"
	"github.com/pjerra/forever-agent/internal/opcode"
	"github.com/pjerra/forever-agent/internal/packet"
	"github.com/pjerra/forever-agent/internal/worldconn/worldtest"
)

const safetyPassword = "pw-secret"

// setTimings shortens the logout and disconnect timings for one test.
func setTimings(t *testing.T, retryFor, retryEvery, grace time.Duration) {
	oldFor, oldEvery, oldGrace := logoutRetryFor, logoutRetryEvery, disconnectGrace
	logoutRetryFor, logoutRetryEvery, disconnectGrace = retryFor, retryEvery, grace
	t.Cleanup(func() { logoutRetryFor, logoutRetryEvery, disconnectGrace = oldFor, oldEvery, oldGrace })
}

// worldScript runs a scripted world server: it logs the character in and then hands the
// server to script. It returns the config file for the CLI and the script's result.
func worldScript(t *testing.T, script func(srv *worldtest.Server) error) (cfg string, done <-chan error) {
	t.Helper()
	tk := &auth.Ticket{Account: "HARNESS", Realms: []auth.Realm{{Name: "Tortoise", Address: "127.0.0.1:1"}}}
	for i := range tk.Key {
		tk.Key[i] = byte(0x31 + i)
	}
	var calls int
	stubLogin(t, tk, &calls)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	res := make(chan error, 1)
	go func() {
		res <- func() error {
			nc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer nc.Close()
			srv, err := worldtest.Accept(nc, tk.KeyBytes(), worldtest.Options{})
			if err != nil {
				return err
			}
			defer srv.Close()
			step := func(op uint16) error { _, err := srv.Expect(op, 5*time.Second); return err }
			if err := step(opcode.CMSGCharEnum); err != nil {
				return err
			}
			srv.Send(opcode.SMSGCharEnum, e2eChars("Harnessb"))
			if err := step(opcode.CMSGPlayerLogin); err != nil {
				return err
			}
			srv.Send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(-8913).F32(-136).F32(82).F32(1.5).Bytes())
			if err := step(opcode.CMSGSetActiveMover); err != nil {
				return err
			}
			return script(srv)
		}()
	}()
	return settingsFile(t, `{"realmd":"127.0.0.1:1","world_addr":"`+ln.Addr().String()+`","rpc":"127.0.0.1:0","rpc_token":"`+testRPCToken+`","accounts":{"harness":"`+safetyPassword+`"}}`), res
}

// answerLogouts answers each CMSG_LOGOUT_REQUEST with the next reason (0: accepted and
// completed) and returns how many requests came before the client closed the socket.
func answerLogouts(srv *worldtest.Server, reasons func(n int) uint32) (int, error) {
	n := 0
	for {
		if _, err := srv.Expect(opcode.CMSGLogoutRequest, 10*time.Second); err != nil {
			if strings.Contains(err.Error(), "closed") {
				return n, nil
			}
			return n, err
		}
		n++
		r := reasons(n)
		if r != 0 {
			srv.Send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(r).U8(0).Bytes())
			continue
		}
		srv.Send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(0).U8(1).Bytes())
		srv.Send(opcode.SMSGLogoutComplete, nil)
		return n, nil
	}
}

// testRPCToken is the rpc_token of the settings worldScript and the reconnect tests write;
// rpcCall sends it.
const testRPCToken = "test-rpc-token-0123456789"

func rpcCall(t *testing.T, addr, method, params string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/rpc", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testRPCToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}
