package bnet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
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

// TestLoginOfflineRealm reports the server's "listed but offline" state before the join is refused.
func TestLoginOfflineRealm(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmPopulation = 0

	_, err := Login(context.Background(), f.options())
	if err == nil {
		t.Fatal("an offline realm was joined")
	}
	var realmErr *RealmError
	if !errors.As(err, &realmErr) || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err = %v", err)
	}
}

// TestLoginBuildMismatch reports the realm's build when it is not the one the client claims.
func TestLoginBuildMismatch(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = 70707
	f.realmFlags = realmFlagVersionMismatch

	_, err := Login(context.Background(), f.options())
	if err == nil {
		t.Fatal("a realm for another build was joined")
	}
	var realmErr *RealmError
	if !errors.As(err, &realmErr) || !strings.Contains(err.Error(), "does not accept client build 70938") {
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

// TestLoginHidesEchoedRequest: a server or proxy that echoes the request into its error body must not put A, M1 or
// any other long hex run on stderr. The error keeps the status and the server's code.
func TestLoginHidesEchoedRequest(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	echoed := strings.Repeat("AB", 100)
	f.restFailBody = `{"error_code":"ERROR_ECHO","error_message":"` + echoed + `"}`
	_, err := Login(context.Background(), f.options())
	if err == nil {
		t.Fatal("a 500 answer logged in")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") || !strings.Contains(msg, "ERROR_ECHO") {
		t.Fatalf("error lacks the status or the code: %q", msg)
	}
	if strings.Contains(msg, echoed[:64]) {
		t.Fatalf("the echoed hex survived: %q", msg)
	}
	if len(msg) > 400 {
		t.Fatalf("error is %d bytes: %q", len(msg), msg)
	}
	for i := range len(msg) {
		if msg[i] < 0x20 || msg[i] > 0x7E {
			t.Fatalf("error carries byte %#x: %q", msg[i], msg)
		}
	}
}

// TestServerRefusalIsSanitised covers the other quote site: LoginResult's error code and message.
func TestServerRefusalIsSanitised(t *testing.T) {
	secret := strings.Repeat("AB", 64)
	result := loginResultJSON{
		AuthenticationState: "DONE",
		ErrorCode:           "ERROR_LOGIN_INCORRECT_PASSWORD",
		ErrorMessage:        "\x1b[31m" + secret + "\x00" + strings.Repeat("z", 300),
	}
	got := serverRefusal(&result)
	if !strings.Contains(got, "ERROR_LOGIN_INCORRECT_PASSWORD") {
		t.Fatalf("the error code was lost: %q", got)
	}
	if strings.Contains(got, secret) {
		t.Fatalf("the server's hex survived: %q", got)
	}
	if len(got) > maxServerText+len("ERROR_LOGIN_INCORRECT_PASSWORD")+2 {
		t.Fatalf("refusal is %d bytes: %q", len(got), got)
	}
	for i := range len(got) {
		if got[i] < 0x20 || got[i] > 0x7E {
			t.Fatalf("refusal carries byte %#x: %q", got[i], got)
		}
	}
	empty := serverRefusal(&loginResultJSON{AuthenticationState: "DONE"})
	if empty == "" {
		t.Fatal("an empty refusal has no explanation")
	}
}

// TestLoginOverallDeadline: the per-call timeout is 5 s, but the login's own deadline is 300 ms and the fake RPC
// port keeps sending ignorable pushes. Login must give up on its deadline, not on the per-call one.
func TestLoginOverallDeadline(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.rpcDrip = true

	opts := f.options()
	opts.Timeout = 5 * time.Second
	opts.Deadline = 300 * time.Millisecond
	start := time.Now()
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a server that never answers logged in")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Login took %s; the overall deadline did not apply", elapsed)
	}
}

// TestSecretsAreRedacted formats every type that holds a secret with the verbs a caller is likely to use; none of
// the secret byte sequences may appear.
func TestSecretsAreRedacted(t *testing.T) {
	const (
		loginTicket = "TC-0123456789ABCDEF0123456789ABCDEF01234567"
		sessionKey  = "SESSION-KEY-MATERIAL-0123456789abcdefghijklmnopqrstuvwxyz-0123456789"
		clientKey   = "CLIENT-SECRET-MATERIAL-0123456789abcdef"
		joinKey     = "JOIN-SECRET-MATERIAL-0123456789abcdefgh"
		joinTicket  = "JOIN-TICKET-MATERIAL-0123456789abcdef"
		password    = "correct-horse-battery-staple"
		account     = "someone@example.invalid"
		battleTag   = "Someone#1234"
	)
	secrets := []string{loginTicket, sessionKey, clientKey, joinKey, joinTicket, password, account, battleTag}

	result := Result{
		LoginTicket:  loginTicket,
		SessionKey:   []byte(sessionKey),
		ClientSecret: []byte(clientKey),
		JoinSecret:   []byte(joinKey),
		JoinTicket:   []byte(joinTicket),
		RealmName:    "Realm",
	}
	targets := []any{
		result, &result,
		Options{Account: account, Password: password},
		&Options{Account: account, Password: password},
		LogonResult{BattleTag: battleTag, SessionKey: []byte(sessionKey)},
		&LogonResult{BattleTag: battleTag, SessionKey: []byte(sessionKey)},
		&srpClient{
			n: big.NewInt(255), A: big.NewInt(7), salt: bytes.Repeat([]byte{1}, 32),
			x: big.NewInt(11), S: big.NewInt(13),
			M1: []byte("M1-SECRET-MATERIAL"), M2: []byte("M2-SECRET-MATERIAL"),
		},
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		for _, target := range targets {
			out := fmt.Sprintf(verb, target)
			for _, secret := range secrets {
				if strings.Contains(out, secret) {
					t.Fatalf("%s of %T prints %q: %q", verb, target, secret, out)
				}
				if hexed := hex.EncodeToString([]byte(secret)); strings.Contains(strings.ToLower(out), hexed) {
					t.Fatalf("%s of %T prints the hex of %q: %q", verb, target, secret, out)
				}
			}
		}
	}
}

// TestLoginAgainstVectorEvidence runs the whole login against a fake pinned to the C++ vectors, including both
// M2-leading-zero vectors: with the client private fixed to the vector's, the client must reproduce the vector's A
// and M1, and the server's M2 text (whose first zero byte is already missing) must verify. This is the end-to-end
// form of the SRP vectors, with the fake's verification compared against the C++ values.
func TestLoginAgainstVectorEvidence(t *testing.T) {
	for _, v := range loadVectors(t) {
		if v.Name != "bnet-srp6-v2-sha256" && !strings.HasSuffix(v.Name, "m2-leading-zero") {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			f := newFakeLoginForVector(t, v)
			defer f.close()
			private, ok := new(big.Int).SetString(v.ClientPrivate, 16)
			if !ok {
				t.Fatal("bad client private")
			}
			opts := f.options()
			opts.Account = v.Login
			opts.Password = v.Password
			opts.srpPrivate = private
			result, err := Login(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-f.errs:
				t.Fatal("fake server:", err)
			default:
			}
			if result.RealmID != 70 || len(result.SessionKey) != 64 {
				t.Fatalf("realm %d, session key %d bytes", result.RealmID, len(result.SessionKey))
			}
		})
	}
}
