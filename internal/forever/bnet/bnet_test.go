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

	"github.com/pjerra/forever-agent/internal/forever/wire"
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

	opts.ClientBuild = wire.Build70938 // the explicit claim is the one the logon makes
	result, err := Login(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.errs:
		t.Fatal("fake server:", err)
	default:
	}
	wantLogonBuilds(t, f, wire.Build70938)

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
	if !errors.Is(err, ErrLoginRefused) {
		t.Fatalf("err = %v, want %v", err, ErrLoginRefused)
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

// TestLoginBuildMismatch reports the realm's build when an explicitly claimed one is not accepted: a pinned build
// is never silently replaced.
func TestLoginBuildMismatch(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = 70707

	opts := f.options()
	opts.ClientBuild = wire.Build70938
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a realm for another build was joined")
	}
	var realmErr *RealmError
	if !errors.As(err, &realmErr) || !strings.Contains(err.Error(), "does not accept client build 70938") {
		t.Fatalf("err = %v", err)
	}
}

// TestLoginPrefersNewestKnownBuild: a realm that lists for more than one build shows the client's own claim for
// each build it accepts, so its build field is no hint at all. The login claims the newest build this client knows
// (70959) and keeps it: the older also-accepted build (70938) is what a run that trusted a claim echo used, and
// the world server then refused that build.
func TestLoginPrefersNewestKnownBuild(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938},
	}}

	result, err := Login(context.Background(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientBuild != wire.Build70959 || result.RealmBuild != wire.Build70959 {
		t.Fatalf("claimed %d, realm list showed %d, want the newest known build %d for both",
			result.ClientBuild, result.RealmBuild, wire.Build70959)
	}
	wantLogonBuilds(t, f, wire.Build70959)
}

// TestLoginAdvertisedBuild: with no build pinned, the login claims the newest build it knows (70959). A realm on
// 70707 refuses it and the realm list names its own build, which is tried next: the realm list and the join read the
// logon's build, not the realm list ticket's (RealmList.cpp:311, GameUtilitiesService.cpp:312,344).
func TestLoginAdvertisedBuild(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = wire.Build70707

	result, err := Login(context.Background(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientBuild != wire.Build70707 || result.RealmBuild != wire.Build70707 {
		t.Fatalf("claimed %d, realm list showed %d, want %d for both", result.ClientBuild, result.RealmBuild, wire.Build70707)
	}
	wantLogonBuilds(t, f, wire.Build70959, wire.Build70707)
}

// TestLoginFirstOnlineRealmOfSeveral: a realm list may carry several realms on different builds. The login joins the
// first realm that is online, whatever that realm's build is; an offline realm is only the fallback when every realm
// is offline. The chosen realm's build is what the automatic claim follows.
func TestLoginFirstOnlineRealmOfSeveral(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{
		{address: 0x46010046, name: "First", population: 1, build: wire.Build70707},
		{address: 0x46010047, name: "Second", population: 1, build: wire.Build70938},
	}
	result, err := Login(context.Background(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	if result.RealmAddress != 0x46010046 || result.RealmName != "First" {
		t.Fatalf("joined %q (0x%08X), want the first online realm First (0x46010046)", result.RealmName, result.RealmAddress)
	}
	if result.ClientBuild != wire.Build70707 || result.RealmBuild != wire.Build70707 {
		t.Fatalf("claimed %d for realm build %d, want %d", result.ClientBuild, result.RealmBuild, wire.Build70707)
	}
	wantLogonBuilds(t, f, wire.Build70959, wire.Build70707)

	// An offline first realm is skipped: the second, online one is joined and its build claimed in a second logon.
	g := newFakeLogin(t)
	defer g.close()
	g.realms = []fakeRealm{
		{address: 0x46010046, name: "Offline", population: 0, build: wire.Build70707},
		{address: 0x46010047, name: "Second", population: 1, build: wire.Build70938},
	}
	result, err = Login(context.Background(), g.options())
	if err != nil {
		t.Fatal(err)
	}
	if result.RealmAddress != 0x46010047 || result.RealmName != "Second" || result.ClientBuild != wire.Build70938 {
		t.Fatalf("joined %q (0x%08X) claiming %d, want the online Second (0x46010047) claiming %d",
			result.RealmName, result.RealmAddress, result.ClientBuild, wire.Build70938)
	}
	wantLogonBuilds(t, g, wire.Build70959, wire.Build70938)
}

// TestLoginAdvertisedBuildUnsupported refuses a realm whose build this client has no table for, naming the builds
// it does have and the realm's build, instead of claiming a build it cannot parse.
func TestLoginAdvertisedBuildUnsupported(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = 71000

	_, err := Login(context.Background(), f.options())
	if err == nil {
		t.Fatal("a realm at an unsupported build was joined")
	}
	if !strings.Contains(err.Error(), "accepts none of the client builds") ||
		!strings.Contains(err.Error(), "70707, 70938 and 70959") || !strings.Contains(err.Error(), "the realm is build 71000") {
		t.Fatalf("err = %v", err)
	}
	wantLogonBuilds(t, f, wire.Build70959, wire.Build70938, wire.Build70707)
}

// wantLogonBuilds reads the logon builds the fake recorded and checks they are exactly wants: a login makes no more
// attempts than the test expects.
func wantLogonBuilds(t *testing.T, f *fakeLogin, wants ...uint32) {
	t.Helper()
	for _, want := range wants {
		select {
		case got := <-f.logonBuilds:
			if got != want {
				t.Fatalf("a logon claimed %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%d logon(s) were expected, one with build %d", len(wants), want)
		}
	}
	select {
	case got := <-f.logonBuilds:
		t.Fatalf("an extra logon claimed %d; %d were expected", got, len(wants))
	case <-time.After(50 * time.Millisecond):
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
		Platform: fourCCWn64, BuildVariant: "Win", Type: fourCCWoW, VersionDataBuild: wire.Build70938,
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

// TestLoginJoinVersionMismatchTriesNextBuild: the realm list accepted the claimed build, but the realm's join
// answered ERROR_USER_SERVER_VERSION_MISMATCH (0x800000DF, RealmList.cpp:454-471). With no build pinned that is the
// same verdict the list's flag gives, so the login tries the next known build (newest first) instead of failing;
// the realm's own build is not named, so there is no hint to jump to.
func TestLoginJoinVersionMismatchTriesNextBuild(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	// The list accepts every build (Realm.AlsoAcceptBuilds): the join is the one refusing, so only the join's answer
	// decides the retry.
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938, wire.Build70707},
	}}
	f.joinStatus = func(build uint32) uint32 {
		if build == wire.Build70707 {
			return 0
		}
		return 0x800000DF
	}

	result, err := Login(context.Background(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	if result.ClientBuild != wire.Build70707 {
		t.Fatalf("claimed %d, want %d after the two join mismatches", result.ClientBuild, wire.Build70707)
	}
	wantLogonBuilds(t, f, wire.Build70959, wire.Build70938, wire.Build70707)
}

// TestLoginJoinNotPermittedNoRetry: ERROR_USER_SERVER_NOT_PERMITTED_ON_REALM (0x800000E1) is the realm's policy, not
// the build (a closed realm, a security level). Another build cannot pass it, so the login answers it at once.
func TestLoginJoinNotPermittedNoRetry(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938, wire.Build70707},
	}}
	f.joinStatus = func(uint32) uint32 { return 0x800000E1 }

	_, err := Login(context.Background(), f.options())
	if err == nil {
		t.Fatal("a realm that is not permitted was joined")
	}
	if !errors.Is(err, ErrRealmNotPermitted) {
		t.Fatalf("err = %v, want ErrRealmNotPermitted", err)
	}
	if !strings.Contains(err.Error(), "ERROR_USER_SERVER_NOT_PERMITTED_ON_REALM") {
		t.Fatalf("the error does not name the status: %v", err)
	}
	wantLogonBuilds(t, f, wire.Build70959)
}

// TestLoginExplicitBuildJoinMismatchIsError: a pinned build is never silently replaced. The join's mismatch names the
// claimed build and no realm build (the core's join answer carries none), and it is returned as the login's error.
func TestLoginExplicitBuildJoinMismatchIsError(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = wire.Build70959
	f.joinStatus = func(uint32) uint32 { return 0x800000DF }

	opts := f.options()
	opts.ClientBuild = wire.Build70959
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a join mismatch under a pinned build logged in")
	}
	var mismatch *RealmMismatch
	if !errors.As(err, &mismatch) || mismatch.Claimed != wire.Build70959 || mismatch.RealmBuild != 0 {
		t.Fatalf("err = %v, want a RealmMismatch claiming %d with no realm build", err, wire.Build70959)
	}
	wantLogonBuilds(t, f, wire.Build70959)
}
