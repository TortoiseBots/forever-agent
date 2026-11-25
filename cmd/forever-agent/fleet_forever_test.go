package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

// fakeServeFleet is a fake fleetForeverDeps: children keyed by dir, statuses
// keyed by dir.
type fakeServeFleet struct {
	mu       sync.Mutex
	children map[string]*fakeChild // by dir
	states   map[string]string     // by dir
	started  []string
	args     [][]string
	ext      live.ServerExt
}

func newFakeServeFleet(ext live.ServerExt) *fakeServeFleet {
	return &fakeServeFleet{children: map[string]*fakeChild{}, states: map[string]string{}, ext: ext}
}

func (ff *fakeServeFleet) deps() fleetForeverDeps {
	return fleetForeverDeps{
		start: func(m fleetForeverMember, w io.Writer) (fleetChild, error) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			c := newFake()
			ff.children[m.Dir] = c
			ff.started = append(ff.started, m.Character)
			ff.args = append(ff.args, foreverChildArgs(ff.ext, m))
			io.WriteString(w, "rpc: listening on http://127.0.0.1:1/rpc\npartial")
			return c, nil
		},
		status: func(_ context.Context, dir string) (string, error) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			if s, ok := ff.states[dir]; ok {
				return s, nil
			}
			return "", io.EOF
		},
	}
}

func (ff *fakeServeFleet) setState(dir, s string) { ff.mu.Lock(); ff.states[dir] = s; ff.mu.Unlock() }
func (ff *fakeServeFleet) child(dir string) *fakeChild {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.children[dir]
}

var serveParty = []fleetForeverMember{
	{Account: "ACC1", Character: "Tankone", Dir: "/tmp/fleet-test/serve-one", Listen: "127.0.0.1:19001", PasswordFile: "/tmp/fleet-test/pw-one"},
	{Account: "ACC2", Character: "Healtwo", Dir: "/tmp/fleet-test/serve-two", Listen: "127.0.0.1:19002", PasswordFile: "/tmp/fleet-test/pw-two"},
	{Account: "ACC3", Character: "Dpsthree", Dir: "/tmp/fleet-test/serve-three", Listen: "127.0.0.1:19003", PasswordFile: "/tmp/fleet-test/pw-three"},
}

var serveExt = live.ServerExt{Rest: "127.0.0.1:1", RPC: "127.0.0.1:2", CA: "ca.pem", Build: "70959"}

// TestFleetForeverOnlineCountsAsInWorld is the K1 failing case: a fake child
// answering state "online" is ready. The old fleet waited for "in_world" and
// timed out: serve's link lifecycle never says "in_world".
func TestFleetForeverOnlineCountsAsInWorld(t *testing.T) {
	ff := newFakeServeFleet(serveExt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuf
	code := make(chan int, 1)
	o := fastOpts
	o.stagger = 0
	o.budget = newLoginBudget(100, time.Second)
	go func() { code <- runFleetForever(ctx, serveParty, ff.deps(), o, &out, &errOut) }()

	// Two are online, one is still reconnecting: no table yet.
	ff.setState(serveParty[0].Dir, "online")
	ff.setState(serveParty[1].Dir, "online")
	ff.setState(serveParty[2].Dir, "reconnecting")
	time.Sleep(50 * time.Millisecond)
	if out.String() != "" {
		t.Fatalf("table printed early: %q", out.String())
	}
	// A status that only said "reconnecting" is not ready; "online" is.
	if !fleetReady("online") || fleetReady("reconnecting") || fleetReady("offline") {
		t.Fatal("fleetReady: want only online (and the old in_world) to count")
	}
	ff.setState(serveParty[2].Dir, "online")
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	for _, want := range []string{"CHARACTER", "RPC", "STATE", "Tankone", "127.0.0.1:19002", "online"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table missing %q:\n%s", want, out.String())
		}
	}
	cancel() // Ctrl-C
	if c := <-code; c != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", c, errOut.String())
	}
	for _, m := range serveParty {
		if !ff.child(m.Dir).stopped {
			t.Errorf("%s was not told to log out", m.Character)
		}
	}
}

// TestFleetForeverReadyIsOnlineOnly: the serve path counts only the serve's
// own lifecycle word. The old daemon's in_world is not its state.
func TestFleetForeverReadyIsOnlineOnly(t *testing.T) {
	if fleetReady("in_world") || fleetReady("") || !fleetReady("online") {
		t.Fatal("fleetReady: want only online")
	}
}

// TestFleetTurtleReadyRuleUnchanged is the Turtle fleet's rule, exactly as
// before K1: in_world is ready, "online" (a serve word) never is, so a
// version-skewed daemon on the endpoint cannot pass as in world.
func TestFleetTurtleReadyRuleUnchanged(t *testing.T) {
	if !fleetReadyTurtle("in_world") || fleetReadyTurtle("online") || fleetReadyTurtle("reconnecting") {
		t.Fatal("fleetReadyTurtle: want in_world only")
	}
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "online")
	}
	o := fastOpts
	o.wait = 300 * time.Millisecond
	var out, errOut syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if code := runFleet(ctx, party, ff.deps(), o, &out, &errOut); code != exitFleet {
		t.Fatalf("a Turtle fleet whose daemons answer online: exit %d, want %d\n%s", code, exitFleet, errOut.String())
	}
	if !strings.Contains(errOut.String(), "not every character") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

// TestFleetSlotsServerNeedForever: --slots and --server belong to --forever;
// the Turtle fleet refuses them instead of ignoring them.
func TestFleetSlotsServerNeedForever(t *testing.T) {
	for _, flag := range []string{"--slots", "--server"} {
		code, _, errOut := runCLI("fleet", "--config", "f.json", flag, "x")
		if code != exitUsage || !strings.Contains(errOut, "--forever") {
			t.Errorf("%s without --forever: code %d stderr %q", flag, code, errOut)
		}
	}
}

// TestFleetForeverRefusesDuplicateAccount: two members on one account kick
// each other (realmd keeps one session per account).
func TestFleetForeverRefusesDuplicateAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	body := `{"members":[
		{"account":"Acc","character":"Onea","dir":"d1","listen":"127.0.0.1:9001","password_file":"p"},
		{"account":"ACC","character":"Twob","dir":"d2","listen":"127.0.0.1:9002","password_file":"p"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFleetForever(path); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("two members on one account: %v", err)
	}
}

// TestFleetForeverPacing spaces serve starts one per 5 s: with two logins
// allowed per window, the third member waits for the first to leave it, and
// says so. The production default is one login per 5 s (fleetForeverPace).
func TestFleetForeverPacing(t *testing.T) {
	ff := newFakeServeFleet(serveExt)
	for _, m := range serveParty {
		ff.setState(m.Dir, "online")
	}
	o := fastOpts
	o.stagger = 0
	o.budget = newLoginBudget(2, 300*time.Millisecond)
	var out, errOut syncBuf
	begin := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- runFleetForever(ctx, serveParty, ff.deps(), o, &out, &errOut) }()
	waitFor(t, "third start", func() bool { return ff.child(serveParty[2].Dir) != nil })
	if d := time.Since(begin); d < 290*time.Millisecond {
		t.Errorf("third member started after %s, before the first left the window", d)
	}
	if !strings.Contains(errOut.String(), "waiting") || !strings.Contains(errOut.String(), "to log in Dpsthree") {
		t.Errorf("no budget line:\n%s", errOut.String())
	}
	if strings.Contains(errOut.String(), "to log in Healtwo") {
		t.Errorf("second member waited:\n%s", errOut.String())
	}
	cancel()
	<-code
}

// TestFleetFileRejectsPassword: the fleet file carries no secret. A
// "password" key is an unknown field and refused.
func TestFleetFileRejectsPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	body := `{"members":[{"account":"A","character":"C","dir":"d","listen":"127.0.0.1:9001","password_file":"p","password":"hunter2"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFleetForever(path); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("fleet file with a password: %v", err)
	}
	// The valid form loads.
	body = `{"members":[{"account":"A","character":"C","dir":"d","listen":"127.0.0.1:9001","password_file":"p"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := loadFleetForever(path)
	if err != nil || len(ms) != 1 || ms[0].Character != "C" {
		t.Fatalf("valid fleet file: %v %+v", err, ms)
	}
}

// TestFleetForeverChildArgs builds the serve command line: serve flags from
// the slots file's `forever` block, the member's own account, character,
// password file, listen address and dir. No secret on the line.
func TestFleetForeverChildArgs(t *testing.T) {
	m := fleetForeverMember{Account: "ACC", Character: "Testchar", Dir: "d", Listen: "127.0.0.1:8765", PasswordFile: "pw"}
	got := strings.Join(foreverChildArgs(serveExt, m), " ")
	want := "forever serve --rest 127.0.0.1:1 --rpc 127.0.0.1:2 --ca ca.pem --account ACC --character Testchar --password-file pw --listen 127.0.0.1:8765 --dir d --build 70959"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// No build in the block: no --build flag.
	noBuild := serveExt
	noBuild.Build = ""
	got = strings.Join(foreverChildArgs(noBuild, m), " ")
	if strings.Contains(got, "--build") {
		t.Errorf("no build in the block, but the line has one: %s", got)
	}
}

// foreverFleetEnv is a slots file with one slot (TESTACC / Gbmage, port 28771
// on test1) and a member dir set up by `slot setup --forever` under holder
// w-a, which holds the lease.
func foreverFleetEnv(t *testing.T) (slotsPath, dir string, member fleetForeverMember) {
	t.Helper()
	slotsPath = slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir = filepath.Join(t.TempDir(), "client")
	if code, _, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", foreverSetupBin(t), "--slots", slotsPath); code != exitOK {
		t.Fatalf("setup: %d %q", code, errOut)
	}
	return slotsPath, dir, fleetForeverMember{Account: "TESTACC", Character: "Gbmage", Dir: dir, Listen: "127.0.0.1:28771", PasswordFile: "pw"}
}

func foreverFleetCheck(t *testing.T, slotsPath string, m fleetForeverMember) error {
	t.Helper()
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	return fleetForeverCheck(cfg, slots.NewStore(cfg, slotsPath), "test1", []fleetForeverMember{m})
}

// TestFleetForeverCheckAcceptsOwnLease: the member whose dir came from slot
// setup under the live lease is cleared; so is a fresh dir on an unleased
// slot (nobody holds the account).
func TestFleetForeverCheckAcceptsOwnLease(t *testing.T) {
	slotsPath, _, m := foreverFleetEnv(t)
	if err := foreverFleetCheck(t, slotsPath, m); err != nil {
		t.Fatalf("own live lease refused: %v", err)
	}
	// Fresh dir, slot released: still fine.
	if code, _, errOut := runCLI("lease", "release", "test1", "1", "--holder", "w-a", "--force", "--slots", slotsPath); code != exitOK {
		t.Fatalf("release: %d %q", code, errOut)
	}
	fresh := m
	fresh.Dir = filepath.Join(t.TempDir(), "fresh")
	if err := foreverFleetCheck(t, slotsPath, fresh); err != nil {
		t.Fatalf("unleased account with a fresh dir refused: %v", err)
	}
}

// TestFleetForeverCheckRefusesHeldAccount is the K1 failing case: another
// session holds the slot's account. A member on a fresh dir, or one whose
// manifest lease was taken over, must not log in and kick it.
func TestFleetForeverCheckRefusesHeldAccount(t *testing.T) {
	slotsPath, _, m := foreverFleetEnv(t)
	fresh := m
	fresh.Dir = filepath.Join(t.TempDir(), "fresh")
	err := foreverFleetCheck(t, slotsPath, fresh)
	if err == nil || !strings.Contains(err.Error(), "held") || !strings.Contains(err.Error(), "w-a") {
		t.Fatalf("held account on a fresh dir: %v", err)
	}
	// Taken over: w-a's manifest names a lease that is gone.
	cfg, _ := slots.Load(slotsPath)
	st := slots.NewStore(cfg, slotsPath)
	cur, _ := st.Get("test1", "1")
	if _, err := st.Release("test1", "1", cur.Holder, cur.LeaseID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Acquire("test1", "1", false, "w-zf", 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	err = foreverFleetCheck(t, slotsPath, m)
	if err == nil || !strings.Contains(err.Error(), "w-zf") {
		t.Fatalf("taken-over lease: %v", err)
	}
	// Released and nobody holds it: the old manifest's lease is gone.
	if _, err := st.Release("test1", "1", "w-zf", "", true); err != nil {
		t.Fatal(err)
	}
	err = foreverFleetCheck(t, slotsPath, m)
	if err == nil || !strings.Contains(err.Error(), "no longer held") {
		t.Fatalf("released lease in the manifest: %v", err)
	}
}

// TestFleetForeverCheckAllowlistAndPort: the account must be a slot account,
// the character one the slot allows, the manifest's port the member's.
func TestFleetForeverCheckAllowlistAndPort(t *testing.T) {
	slotsPath, _, m := foreverFleetEnv(t)
	bad := m
	bad.Account = "OTHERACC"
	if err := foreverFleetCheck(t, slotsPath, bad); err == nil || !strings.Contains(err.Error(), "not a slot account") {
		t.Fatalf("foreign account: %v", err)
	}
	bad = m
	bad.Character = "Gbstranger"
	if err := foreverFleetCheck(t, slotsPath, bad); err == nil || !strings.Contains(err.Error(), "allows") {
		t.Fatalf("character outside the allowlist: %v", err)
	}
	ok := m
	ok.Character = "Gbrother" // other_characters of the slot
	if err := foreverFleetCheck(t, slotsPath, ok); err == nil || !strings.Contains(err.Error(), "TESTACC/Gbmage") {
		// allowed by the slot, but the manifest was made for Gbmage
		t.Fatalf("manifest for another character: %v", err)
	}
	bad = m
	bad.Listen = "127.0.0.1:28999"
	if err := foreverFleetCheck(t, slotsPath, bad); err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("manifest port: %v", err)
	}
}

// TestFleetForeverRefusesBeforeAnyLogin: the CLI path refuses a held account
// with exit 15 before it starts a single serve.
func TestFleetForeverRefusesBeforeAnyLogin(t *testing.T) {
	slotsPath, _, m := foreverFleetEnv(t)
	m.Dir = filepath.Join(t.TempDir(), "fresh")
	body, err := json.Marshal(fleetForeverFile{Members: []fleetForeverMember{m}})
	if err != nil {
		t.Fatal(err)
	}
	fleetPath := filepath.Join(t.TempDir(), "fleet.json")
	if err := os.WriteFile(fleetPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("fleet", "--forever", "--config", fleetPath, "--slots", slotsPath, "--server", "test1")
	if code != exitSlotRefused || !strings.Contains(errOut, "held") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if _, err := os.Stat(m.Dir); !os.IsNotExist(err) {
		t.Fatal("a serve started: its dir exists")
	}
}

// TestFleetForeverAccountGuard: two fleets started at once must not both log
// in on one account. The check reads the slots file only, so an account with
// no lease (a bare dir) passed in both; the per-account lock held for the
// fleet's life lets only one through, and a crash frees it (flock).
func TestFleetForeverAccountGuard(t *testing.T) {
	slotsPath, _, m := foreverFleetEnv(t)
	members := []fleetForeverMember{m}
	rel, err := fleetForeverLockAccounts(slotsPath, "test1", members)
	if err != nil {
		t.Fatalf("first fleet: %v", err)
	}
	if _, err := fleetForeverLockAccounts(slotsPath, "test1", members); err == nil || !strings.Contains(err.Error(), "another fleet") {
		t.Fatalf("second fleet on the same account: %v", err)
	}
	// The same account on another server is another account.
	if rel2, err := fleetForeverLockAccounts(slotsPath, "other", members); err != nil {
		t.Fatalf("other server: %v", err)
	} else {
		rel2()
	}
	rel()
	rel3, err := fleetForeverLockAccounts(slotsPath, "test1", members)
	if err != nil {
		t.Fatalf("after the first fleet ended: %v", err)
	}
	rel3()
}

// TestFleetForeverCLIRefusesLockedAccount: the CLI path exits 15 before any
// serve starts when another fleet holds one member's account.
func TestFleetForeverCLIRefusesLockedAccount(t *testing.T) {
	slotsPath, _, m := foreverFleetEnv(t)
	rel, err := fleetForeverLockAccounts(slotsPath, "test1", []fleetForeverMember{m})
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	body, _ := json.Marshal(fleetForeverFile{Members: []fleetForeverMember{m}})
	fleetPath := filepath.Join(t.TempDir(), "fleet.json")
	if err := os.WriteFile(fleetPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("fleet", "--forever", "--config", fleetPath, "--slots", slotsPath, "--server", "test1")
	if code != exitSlotRefused || !strings.Contains(errOut, "another fleet") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

// TestFleetForeverGameAccountFlag is #236 point 3: a member names its game account in the fleet file and the serve
// gets it as --game-account; an absent game_account sends no flag (the first listed), and a negative one is refused
// when the file loads.
func TestFleetForeverGameAccountFlag(t *testing.T) {
	m := fleetForeverMember{Account: "ACC", Character: "Testchar", Dir: "d", Listen: "127.0.0.1:8765", PasswordFile: "pw", GameAccount: 3}
	got := strings.Join(foreverChildArgs(serveExt, m), " ")
	if !strings.Contains(got, "--game-account 3") {
		t.Fatalf("the serve line has no game account: %s", got)
	}
	m.GameAccount = 0
	if got := strings.Join(foreverChildArgs(serveExt, m), " "); strings.Contains(got, "--game-account") {
		t.Fatalf("an absent game account sent one: %s", got)
	}

	path := filepath.Join(t.TempDir(), "f.json")
	body := `{"members":[{"account":"ACC","character":"Testchar","dir":"d","listen":"127.0.0.1:9001","password_file":"p","game_account":2}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, err := loadFleetForever(path)
	if err != nil || len(ms) != 1 || ms[0].GameAccount != 2 {
		t.Fatalf("a named game account: %v %+v", err, ms)
	}
	body = `{"members":[{"account":"ACC","character":"Testchar","dir":"d","listen":"127.0.0.1:9001","password_file":"p","game_account":-1}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFleetForever(path); err == nil || !strings.Contains(err.Error(), "game_account") {
		t.Fatalf("a negative game account loaded: %v", err)
	}
}

// TestFleetCheckGameAccountsErrorsEarly is #236 point 3: a member that names no game account on an account the login
// lists several for is refused before any serve starts, and the message says what to add to the file. A member that
// names one is not probed at all.
func TestFleetCheckGameAccountsErrorsEarly(t *testing.T) {
	old := fleetGameAccounts
	t.Cleanup(func() { fleetGameAccounts = old })

	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("not-a-real-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	members := []fleetForeverMember{{Account: "ACC", Character: "Onea", Dir: "d1", Listen: "127.0.0.1:9101", PasswordFile: pw}}
	var probed []string
	fleetGameAccounts = func(_ context.Context, _ live.ServerExt, m fleetForeverMember, password string, before func(context.Context) error) (int, error) {
		if before != nil {
			if err := before(context.Background()); err != nil {
				t.Fatalf("the budget hook: %v", err)
			}
		}
		if password != "not-a-real-password" {
			t.Fatalf("the probe got password %q", password)
		}
		probed = append(probed, m.Character)
		return 6, nil
	}
	var out strings.Builder
	err := fleetCheckGameAccounts(context.Background(), serveExt, members, nil, time.Now, &out)
	if err == nil || !strings.Contains(err.Error(), "game_account") || !strings.Contains(err.Error(), "6 game accounts") {
		t.Fatalf("six game accounts and none named: %v", err)
	}
	if len(probed) != 1 {
		t.Fatalf("probed %v, want the one unnamed member", probed)
	}

	// One game account: nothing to name, the fleet goes on.
	probed = nil
	fleetGameAccounts = func(_ context.Context, _ live.ServerExt, m fleetForeverMember, _ string, _ func(context.Context) error) (int, error) {
		probed = append(probed, m.Character)
		return 1, nil
	}
	if err := fleetCheckGameAccounts(context.Background(), serveExt, members, nil, time.Now, &out); err != nil {
		t.Fatalf("one game account: %v", err)
	}

	// A named game account is proof enough: no login is spent on the check.
	probed = nil
	named := members[0]
	named.GameAccount = 2
	if err := fleetCheckGameAccounts(context.Background(), serveExt, []fleetForeverMember{named}, nil, time.Now, &out); err != nil {
		t.Fatalf("named game account: %v", err)
	}
	if len(probed) != 0 {
		t.Fatalf("a member that names its game account was probed: %v", probed)
	}
}

// TestFleetCheckGameAccountsProbesOncePerAccount: the count belongs to the account, so two characters of one account
// cost one login, not one per member (a fleet of five characters on one account used to spend five logins on the same
// answer, through the fleet's shared budget).
//
// Mutation this test catches: a probe inside the member loop with no account cache (one login per member).
func TestFleetCheckGameAccountsProbesOncePerAccount(t *testing.T) {
	old := fleetGameAccounts
	t.Cleanup(func() { fleetGameAccounts = old })
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("not-a-real-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	members := []fleetForeverMember{
		{Account: "ACC", Character: "Onea", Dir: "d1", Listen: "127.0.0.1:9101", PasswordFile: pw},
		{Account: "ACC", Character: "Twob", Dir: "d2", Listen: "127.0.0.1:9102", PasswordFile: pw},
		{Account: "OTHER", Character: "Thre", Dir: "d3", Listen: "127.0.0.1:9103", PasswordFile: pw},
	}
	var probed []string
	fleetGameAccounts = func(_ context.Context, _ live.ServerExt, m fleetForeverMember, _ string, _ func(context.Context) error) (int, error) {
		probed = append(probed, m.Account+"/"+m.Character)
		return 1, nil
	}
	var out strings.Builder
	if err := fleetCheckGameAccounts(context.Background(), serveExt, members, nil, time.Now, &out); err != nil {
		t.Fatalf("one game account each: %v", err)
	}
	if len(probed) != 2 || probed[0] != "ACC/Onea" || probed[1] != "OTHER/Thre" {
		t.Fatalf("probed %v, want one probe per account (ACC/Onea, OTHER/Thre)", probed)
	}
}
