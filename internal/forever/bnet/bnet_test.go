package bnet

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLoginAgainstFakeServer runs the whole sequence against the fake REST + RPC server: SRP proof, ticket, connect,
// logon push, realm list ticket, sub-regions, realm list and the join answer.
func TestLoginAgainstFakeServer(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()

	var steps []string
	opts := f.options()
	opts.Logf = func(format string, args ...any) { steps = append(steps, format) }

	result, err := Login(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.errs:
		t.Fatal("fake server:", err)
	default:
	}

	if result.RealmName != "TestRealm" || result.RealmID != 70 || result.RealmAddress != 0x46010046 {
		t.Errorf("realm = %q id %d addr %#x", result.RealmName, result.RealmID, result.RealmAddress)
	}
	if result.ServerIP != "127.0.0.1" || result.ServerPort != 8085 {
		t.Errorf("world server %s:%d", result.ServerIP, result.ServerPort)
	}
	if len(result.SessionKey) != 64 || len(result.ClientSecret) != 32 || len(result.JoinSecret) != 32 {
		t.Errorf("secrets: session %d, client %d, join %d", len(result.SessionKey), len(result.ClientSecret), len(result.JoinSecret))
	}
	if !strings.Contains(string(result.JoinTicket), "7#1") {
		t.Errorf("join ticket %s", result.JoinTicket)
	}
	if result.SubRegion != "70-1-70" {
		t.Errorf("sub-region %q", result.SubRegion)
	}
	if len(steps) < 8 {
		t.Errorf("%d step lines", len(steps))
	}
	for _, step := range steps {
		if strings.Contains(step, f.ticket) || strings.Contains(step, f.srp.password) {
			t.Fatalf("a step line leaks a secret: %q", step)
		}
	}
}

// TestLoginWrongPassword checks the refusal path: the server verifies the evidence against its own verifier, so a
// wrong password ends in "DONE" without a ticket and the client reports it without inventing a success.
func TestLoginWrongPassword(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()

	opts := f.options()
	opts.Password = f.srp.password + "x"
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a wrong password logged in")
	}
	if !strings.Contains(err.Error(), "login refused") {
		t.Fatalf("err = %v", err)
	}
}

// TestLoadRootsRejectsGarbage makes sure there is no trust-on-first-use path.
func TestLoadRootsRejectsGarbage(t *testing.T) {
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRoots(path); err == nil {
		t.Fatal("garbage was accepted as a root CA")
	}
	if _, err := LoadRoots(path + ".missing"); err == nil {
		t.Fatal("a missing root CA file was accepted")
	}
}
