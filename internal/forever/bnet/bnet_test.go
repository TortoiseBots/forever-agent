package bnet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	// Format the callback the way the command prints it: an earlier version stored the bare format string, so a
	// secret that only exists in an argument could never be seen by the leak assertion below.
	var steps []string
	opts := f.options()
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }

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
	// the join ticket is a secret, so a failure names its size and content shape, never its bytes
	var joinTicket realmJoinTicket
	if err := json.Unmarshal(result.JoinTicket, &joinTicket); err != nil {
		t.Fatalf("join ticket (%d bytes) is not the fake's JSON: %v", len(result.JoinTicket), err)
	}
	if joinTicket.GameAccount != "agent#0000" || joinTicket.Platform != fourCCWn64 || joinTicket.Type != fourCCWoW || joinTicket.ClientArch != fourCCTextX64 {
		t.Errorf("join ticket names game account %q, variant %08X/%08X/%08X", joinTicket.GameAccount,
			joinTicket.Platform, joinTicket.Type, joinTicket.ClientArch)
	}
	if result.SubRegion != "70-1-70" {
		t.Errorf("sub-region %q", result.SubRegion)
	}
	if len(steps) < 8 {
		t.Errorf("%d step lines", len(steps))
	}
	if len(steps) > 0 && (!strings.Contains(steps[0], "login form") || !strings.Contains(steps[len(steps)-1], "realm join")) {
		t.Errorf("step lines run %q .. %q, want the form first and the join last", steps[0], steps[len(steps)-1])
	}
	// the callback has to be captured with its arguments, not as the bare format string: this line carries one
	if !strings.Contains(strings.Join(steps, "\n"), "sub-region: 70-1-70") {
		t.Errorf("the step lines were captured without their arguments: %q", steps)
	}
	for _, step := range steps {
		if strings.Contains(step, f.ticket) || strings.Contains(step, f.srp.password) || strings.Contains(step, opts.Account) {
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
	// the error kind, not a sentence: a message can be reworded without the refusal changing
	if !errors.Is(err, errLoginRefused) {
		t.Fatalf("err = %v, want %v", err, errLoginRefused)
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
	c := &restClient{account: "Someone@Example.test"}
	got := c.serverRefusal(&result)
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
	empty := c.serverRefusal(&loginResultJSON{AuthenticationState: "DONE"})
	if empty == "" {
		t.Fatal("an empty refusal has no explanation")
	}
}

// TestRefusalDoesNotEchoTheAccount: the login service answers a refusal with error_message; it can name the account
// it was asked about. That name is the caller's, and no error may carry it, in any spelling the service uses
// (the name is upper-cased before it is looked up).
func TestRefusalDoesNotEchoTheAccount(t *testing.T) {
	const account = "Someone@Example.test"
	c := &restClient{account: account}
	for _, echo := range []string{
		account,
		strings.ToUpper(account),
		strings.ToLower(account),
		"incorrect password for " + account,
		"unknown account " + strings.ToUpper(account),
	} {
		got := c.serverRefusal(&loginResultJSON{ErrorCode: "ERROR_LOGIN_INCORRECT_PASSWORD", ErrorMessage: echo})
		for _, form := range []string{account, strings.ToUpper(account), strings.ToLower(account)} {
			if strings.Contains(got, form) {
				t.Fatalf("refusal %q echoes the account as %q", got, form)
			}
		}
		got = c.describeRESTFailure("401 Unauthorized", []byte(`{"error_code":"ERROR_LOGIN_INCORRECT_PASSWORD","error_message":"`+echo+`"}`))
		for _, form := range []string{account, strings.ToUpper(account), strings.ToLower(account)} {
			if strings.Contains(got, form) {
				t.Fatalf("REST failure %q echoes the account as %q", got, form)
			}
		}
	}
	// a short name is still removed, even where it is an ordinary word in the server's message
	c = &restClient{account: "bob"}
	if got := c.serverRefusal(&loginResultJSON{ErrorMessage: "bob is banned"}); strings.Contains(got, "bob") {
		t.Fatalf("refusal %q echoes the account", got)
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

// TestSecretsAreRedacted formats every type that holds a secret through the verbs and containers a caller is likely
// to use: alone, behind a pointer, as an exported struct field, as a slice element, in an error wrapped with %w, and
// with Println. The package's own containers are included on their own because fmt does not call methods on the
// unexported fields of a containing struct (rpcSession keeps the logon result that way), so a container has to redact.
func TestSecretsAreRedacted(t *testing.T) {
	const (
		loginTicket  = "TC-0123456789ABCDEF0123456789ABCDEF01234567"
		sessionKey   = "SESSION-KEY-MATERIAL-0123456789abcdefghijklmnopqrstuvwxyz-0123456789"
		clientSecret = "CLIENT-SECRET-MATERIAL-0123456789abcdef"
		joinSecret   = "JOIN-SECRET-MATERIAL-0123456789abcdefgh"
		joinTicket   = "JOIN-TICKET-MATERIAL-0123456789abcdef"
		password     = "correct-horse-battery-staple"
		account      = "someone@example.invalid"
		battleTag    = "Someone#1234"
		proofA       = "PUBLIC-A-MATERIAL-0123456789abcdefghij"
		proofM1      = "CLIENT-EVIDENCE-M1-0123456789abcdefgh"
		identityBlob = "IDENTITY-BLOB-0123456789abcdefghijklmn"
		m2Text       = "M2-EVIDENCE-0123456789abcdefghijklmnopq"
	)
	secrets := []string{loginTicket, sessionKey, clientSecret, joinSecret, joinTicket, password, account, battleTag,
		proofA, proofM1, identityBlob, m2Text}

	resultValue := Result{
		LoginTicket: loginTicket, SessionKey: []byte(sessionKey), ClientSecret: []byte(clientSecret),
		JoinSecret: []byte(joinSecret), JoinTicket: []byte(joinTicket),
		RealmName: "Realm\x1b[2J", SubRegion: "70-1-70\x1b[31m", ServerIP: "127.0.0.1\x00",
	}
	optionsValue := Options{Account: account, Password: password}
	logonResultValue := logonResult{BattleTag: battleTag, SessionKey: []byte(sessionKey), GeoIPCountry: "US"}
	logonRequestValue := logonRequest{Program: "WoW", Platform: "Wn64", Locale: "enUS", CachedWebCredentials: []byte(loginTicket)}
	attributeValue := attribute{
		Name: "Param_ClientInfo", Value: variant{BlobValue: []byte(identityBlob)},
	}
	clientRequestValue := clientRequest{Attributes: []attribute{attributeValue}}
	clientResponseValue := clientResponse{Attributes: []attribute{
		{Name: "Param_JoinSecret", Value: variant{BlobValue: []byte(joinSecret)}},
		{Name: "Param_RealmJoinTicket", Value: variant{BlobValue: []byte(joinTicket)}},
	}}
	variantValue := variant{BlobValue: []byte(joinSecret)}
	loginFormValueValue := loginFormValue{InputID: "public_A", Value: proofA}
	loginFormValueFull := loginForm{Inputs: []loginFormValue{
		{InputID: "public_A", Value: proofA},
		{InputID: "client_evidence_M1", Value: proofM1},
	}}
	loginResultValue := loginResultJSON{
		AuthenticationState: "DONE", ErrorCode: "ERROR_TEST", ErrorMessage: "denied\x1b[2J",
		NextURL: "/next\x00", LoginTicket: loginTicket, ServerEvidenceM2: m2Text,
	}
	joinAnswerValue := joinAnswer{ticket: []byte(joinTicket), addresses: []byte("ADDRESSES-0123456789"), secret: []byte(joinSecret)}
	clientInformationValue := clientInformation{
		Platform: fourCCWn64, BuildVariant: "Win", Type: fourCCWoW, VersionDataBuild: build70938,
		Secret: jsonBytes(clientSecret),
	}
	ticketInformationValue := realmListTicketClientInformation{Info: clientInformationValue}
	rpcSessionValue := rpcSession{
		ciid:  "000000016AFD1AB1\x1b[2J",
		logon: &logonResult{SessionKey: []byte(sessionKey), BattleTag: battleTag},
	}

	targets := []struct {
		name  string
		value any
	}{
		{"Result", resultValue},
		{"Result (pointer)", &resultValue},
		{"Options", optionsValue},
		{"Options (pointer)", &optionsValue},
		{"logonResult", logonResultValue},
		{"logonResult (pointer)", &logonResultValue},
		{"srpClient", &srpClient{n: big.NewInt(255), A: big.NewInt(7), salt: bytes.Repeat([]byte{1}, 32),
			x: big.NewInt(11), S: big.NewInt(13), M1: []byte(proofM1), M2: []byte(m2Text)}},
		{"logonRequest", logonRequestValue},
		{"logonRequest (pointer)", &logonRequestValue},
		{"clientRequest", clientRequestValue},
		{"clientResponse", clientResponseValue},
		{"attribute", attributeValue},
		{"attribute (pointer)", &attributeValue},
		{"variant", variantValue},
		{"variant (pointer)", &variantValue},
		{"loginForm", loginFormValueFull},
		{"loginForm (pointer)", &loginFormValueFull},
		{"loginFormValue", loginFormValueValue},
		{"loginFormValue (pointer)", &loginFormValueValue},
		{"loginResultJSON", loginResultValue},
		{"loginResultJSON (pointer)", &loginResultValue},
		{"joinAnswer", joinAnswerValue},
		{"joinAnswer (pointer)", &joinAnswerValue},
		{"clientInformation", clientInformationValue},
		{"clientInformation (pointer)", &clientInformationValue},
		{"realmListTicketClientInformation", ticketInformationValue},
		{"realmListTicketClientInformation (pointer)", &ticketInformationValue},
		// the container cases: the secret sits in an unexported field, where fmt does not reach a method
		{"rpcSession holding the logon result", rpcSessionValue},
		{"rpcSession holding the logon result (pointer)", &rpcSessionValue},
		{"realmListTicketClientInformation holding the client information", ticketInformationValue},
	}

	formatters := []struct {
		name   string
		render func(any) string
	}{
		{"%v", func(v any) string { return fmt.Sprintf("%v", v) }},
		{"%+v", func(v any) string { return fmt.Sprintf("%+v", v) }},
		{"%#v", func(v any) string { return fmt.Sprintf("%#v", v) }},
		{"%s", func(v any) string { return fmt.Sprintf("%s", v) }},
		{"%q", func(v any) string { return fmt.Sprintf("%q", v) }},
		{"%x", func(v any) string { return fmt.Sprintf("%x", v) }},
		{"Println", func(v any) string { return fmt.Sprintln(v) }},
		{"wrapped %w", func(v any) string {
			return fmt.Errorf("caller: %w", fmt.Errorf("holder: %v", v)).Error()
		}},
	}

	for _, target := range targets {
		containers := []struct {
			name  string
			value any
		}{
			{target.name, target.value},
			{target.name + " as an exported field", struct{ Secret any }{target.value}},
			{target.name + " as a slice element", []any{target.value}},
		}
		for _, container := range containers {
			for _, formatter := range formatters {
				out := formatter.render(container.value)
				for _, secret := range secrets {
					for _, form := range secretForms(secret) {
						if strings.Contains(out, form) {
							t.Fatalf("%s under %s prints %q: %q", container.name, formatter.name, secret, out)
						}
					}
				}
			}
		}
	}
}

// secretForms is every rendering of a secret a verb can produce: the text, its hex in both cases, its base64 and the
// decimal byte list a jsonBytes field would print.
func secretForms(secret string) []string {
	hexed := hex.EncodeToString([]byte(secret))
	return []string{
		secret,
		hexed,
		strings.ToUpper(hexed),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		fmt.Sprint([]byte(secret)),
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
