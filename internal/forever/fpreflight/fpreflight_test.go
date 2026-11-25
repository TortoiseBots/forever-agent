package fpreflight

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/slotdir"
)

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

type fakeProbe struct{ alive, listening bool }

func (p fakeProbe) AgentPID(string) (int, bool) { return 4242, p.alive }
func (p fakeProbe) PortListening(int) bool      { return p.listening }

type fakeModule struct {
	cmd  serveclient.Command
	err  error
	line string
}

func (m *fakeModule) Bot(_ context.Context, line string, _ time.Duration) (serveclient.Command, error) {
	m.line = line
	return m.cmd, m.err
}

// fakeConsole answers .pinfo per name from a table; a name not in it is "Player not found".
type fakeConsole struct {
	rows map[string]string
	err  error
	got  []string
}

func (c *fakeConsole) Run(_ context.Context, cmd string) (string, error) {
	c.got = append(c.got, cmd)
	if c.err != nil {
		return "", c.err
	}
	name := strings.TrimPrefix(cmd, ".pinfo ")
	if r, ok := c.rows[strings.ToLower(name)]; ok {
		return r, nil
	}
	return "Player not found!", nil
}

func pinfo(name, account string, online bool) string {
	off := ""
	if !online {
		off = " (offline)"
	}
	return "Player" + off + " " + name + " (GUID: 7)\nAccount: " + account + " (ID: 3), GMLevel: 0, Last Login: today"
}

// serveFixture writes a slot dir and a lease, returning the dir and slots path.
type fx struct {
	dir, slots string
	lease      map[string]any
}

func newFx(t *testing.T) *fx {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "serve")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	slots := filepath.Join(root, "slots.json")
	m := slotdir.Manifest{Server: "forever", Slot: "1", Holder: "holder1", LeaseID: "lid", Port: 4001, Dir: dir,
		Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: slotdir.TokenPath(dir, 4001), NoAuth: true, SlotsPath: slots}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fx{dir: dir, slots: slots, lease: map[string]any{
		"server": "forever", "slot": "1", "holder": "holder1", "lease_id": "lid",
		"acquired": t0.Add(-time.Hour), "expires": t0.Add(time.Hour)}}
	f.writeLease(t)
	return f
}

func (f *fx) writeLease(t *testing.T) {
	t.Helper()
	ld := filepath.Join(filepath.Dir(f.slots), "leases", "forever")
	if err := os.MkdirAll(ld, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.lease)
	if err := os.WriteFile(filepath.Join(ld, "1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func goodDeps(m *fakeModule, c *fakeConsole) Deps {
	return Deps{
		Now:        func() time.Time { return t0 },
		Probe:      fakeProbe{alive: true, listening: true},
		OpenModule: func(string) (Module, error) { return m, nil },
		Console:    c,
	}
}

func okModule() *fakeModule {
	return &fakeModule{cmd: serveclient.Command{Sent: ".tb state party", Lines: []string{"bots 0"}, Complete: true, End: "end"}}
}

func okConsole() *fakeConsole {
	return &fakeConsole{rows: map[string]string{
		"hero": pinfo("Hero", "ACC1", true), "tank": pinfo("Tank", "ACC1", true), "heal": pinfo("Heal", "ACC1", true),
	}}
}

func find(t *testing.T, r *Report, check string) Result {
	t.Helper()
	for _, x := range r.Results {
		if x.Check == check {
			return x
		}
	}
	t.Fatalf("no %q line in %+v", check, r.Results)
	return Result{}
}

func baseOpts(f *fx) Options {
	return Options{
		Kind: KindPlay, ServeDir: f.dir, Character: "Hero", Bots: []string{"Tank", "Heal"},
		Plan: func() (string, uint32, error) { return "rfc", 389, nil },
	}
}

func TestAllGood(t *testing.T) {
	f := newFx(t)
	m, c := okModule(), okConsole()
	rep := Run(context.Background(), baseOpts(f), goodDeps(m, c))
	if rep.Blocked() {
		t.Fatalf("blocked: %+v", rep.Results)
	}
	if m.line != ".tb state party" {
		t.Fatalf("module probe sent %q", m.line)
	}
	if got := find(t, rep, CheckBots); got.Status != OK {
		t.Fatalf("bots: %+v", got)
	}
	if find(t, rep, CheckServe).Status != OK || find(t, rep, CheckLease).Status != OK || find(t, rep, CheckModule).Status != OK || find(t, rep, CheckConsole).Status != OK {
		t.Fatalf("results: %+v", rep.Results)
	}
}

func TestServePidDead(t *testing.T) {
	f := newFx(t)
	d := goodDeps(okModule(), okConsole())
	d.Probe = fakeProbe{alive: false, listening: true}
	rep := Run(context.Background(), baseOpts(f), d)
	if r := find(t, rep, CheckServe); r.Status != Fail || !strings.Contains(r.Detail, "not running") {
		t.Fatalf("%+v", r)
	}
	if !rep.Blocked() {
		t.Fatal("a dead serve must block")
	}
}

func TestServePortSilent(t *testing.T) {
	f := newFx(t)
	d := goodDeps(okModule(), okConsole())
	d.Probe = fakeProbe{alive: true, listening: false}
	rep := Run(context.Background(), baseOpts(f), d)
	if r := find(t, rep, CheckServe); r.Status != Fail || !strings.Contains(r.Detail, "port") {
		t.Fatalf("%+v", r)
	}
}

func TestServeDirNotASlot(t *testing.T) {
	o := baseOpts(&fx{dir: t.TempDir()})
	rep := Run(context.Background(), o, goodDeps(okModule(), okConsole()))
	if r := find(t, rep, CheckServe); r.Status != Fail {
		t.Fatalf("%+v", r)
	}
}

func TestNoServeDirWarnsOnce(t *testing.T) {
	o := baseOpts(&fx{})
	o.ServeDir = ""
	rep := Run(context.Background(), o, goodDeps(okModule(), okConsole()))
	if r := find(t, rep, CheckServe); r.Status != Warn {
		t.Fatalf("%+v", r)
	}
	if rep.Blocked() {
		t.Fatalf("no serve dir is a warning: %+v", rep.Results)
	}
}

func TestLeaseChecks(t *testing.T) {
	cases := map[string]struct {
		mut  func(*fx)
		want Status
		frag string
	}{
		"missing":  {func(f *fx) { os.RemoveAll(filepath.Join(filepath.Dir(f.slots), "leases")) }, Fail, "no lease"},
		"holder":   {func(f *fx) { f.lease["holder"] = "someone-else"; f.writeLease(t) }, Fail, "holder"},
		"id":       {func(f *fx) { f.lease["lease_id"] = "other"; f.writeLease(t) }, Fail, "lease id"},
		"expired":  {func(f *fx) { f.lease["expires"] = t0.Add(-time.Minute); f.writeLease(t) }, Warn, "expired"},
		"unleased": {func(f *fx) { rewriteManifest(t, f, func(m *slotdir.Manifest) { m.SlotsPath = "" }) }, OK, "not leased"},
		"garbled": {func(f *fx) {
			os.WriteFile(filepath.Join(filepath.Dir(f.slots), "leases", "forever", "1.json"), []byte("{"), 0o600)
		}, Fail, "unreadable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			tc.mut(f)
			rep := Run(context.Background(), baseOpts(f), goodDeps(okModule(), okConsole()))
			r := find(t, rep, CheckLease)
			if r.Status != tc.want || !strings.Contains(r.Detail, tc.frag) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func rewriteManifest(t *testing.T, f *fx, mut func(*slotdir.Manifest)) {
	t.Helper()
	_, m, err := slotdir.Read(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	mut(m)
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(f.dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPlanChecks(t *testing.T) {
	f := newFx(t)
	o := baseOpts(f)
	o.Plan = func() (string, uint32, error) { return "", 0, errors.New("plan refused: no legs") }
	rep := Run(context.Background(), o, goodDeps(okModule(), okConsole()))
	if r := find(t, rep, CheckPlan); r.Status != Fail || !strings.Contains(r.Detail, "no legs") {
		t.Fatalf("%+v", r)
	}
	o.Plan = nil
	rep = Run(context.Background(), o, goodDeps(okModule(), okConsole()))
	if r := find(t, rep, CheckPlan); r.Status != Fail {
		t.Fatalf("a missing plan check is a failure: %+v", r)
	}
}

func writeExport(t *testing.T, dir, plan string, mapID uint32, legs int) string {
	t.Helper()
	rep := planexport.ForeverReport{Rows: planexport.Rows{Plan: planexport.PlanRow{Plan: plan, Map: mapID}}}
	for i := 0; i < legs; i++ {
		rep.Rows.Legs = append(rep.Rows.Legs, planexport.LegRow{Plan: plan, Leg: i, Name: "leg" + string(rune('a'+i))})
	}
	p := filepath.Join(dir, "plan_"+plan+".sql")
	if err := os.WriteFile(p, []byte(planexport.WriteForeverSQL([]planexport.ForeverReport{rep})), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExportChecks(t *testing.T) {
	f := newFx(t)
	run := func(export string) Result {
		o := baseOpts(f)
		o.Export = export
		return find(t, Run(context.Background(), o, goodDeps(okModule(), okConsole())), CheckExport)
	}
	d := t.TempDir()
	if r := run(""); r.Status != Warn {
		t.Fatalf("no export given: %+v", r)
	}
	if r := run(filepath.Join(d, "missing.sql")); r.Status != Fail || !strings.Contains(r.Detail, "cannot read") {
		t.Fatalf("missing: %+v", r)
	}
	writeExport(t, d, "rfc", 389, 2)
	if r := run(d); r.Status != OK || !strings.Contains(r.Detail, "2 leg") || !strings.Contains(r.Detail, "not read") {
		t.Fatalf("a folder holds plan_<name>.sql: %+v", r)
	}
	if r := run(filepath.Join(d, "plan_rfc.sql")); r.Status != OK {
		t.Fatalf("a file: %+v", r)
	}
	bad := filepath.Join(d, "bad.sql")
	os.WriteFile(bad, []byte("this is not sql rows\nINSERT INTO nope VALUES (1);"), 0o600)
	if r := run(bad); r.Status != Fail {
		t.Fatalf("garbage: %+v", r)
	}
	other := t.TempDir()
	writeExport(t, other, "rfc", 999, 2)
	if r := run(other); r.Status != Fail || !strings.Contains(r.Detail, "map") {
		t.Fatalf("wrong map: %+v", r)
	}
	other2 := t.TempDir()
	writeExport(t, other2, "rfc", 389, 0)
	if r := run(other2); r.Status != Fail || !strings.Contains(r.Detail, "no legs") {
		t.Fatalf("no legs: %+v", r)
	}
	other3 := t.TempDir()
	p := writeExport(t, other3, "wc", 43, 1)
	os.Rename(p, filepath.Join(other3, "plan_rfc.sql"))
	if r := run(other3); r.Status != Fail || !strings.Contains(r.Detail, "no plan") {
		t.Fatalf("other plan inside: %+v", r)
	}
}

func TestModuleChecks(t *testing.T) {
	f := newFx(t)
	run := func(m *fakeModule, openErr error) Result {
		d := goodDeps(m, okConsole())
		if openErr != nil {
			d.OpenModule = func(string) (Module, error) { return nil, openErr }
		}
		return find(t, Run(context.Background(), baseOpts(f), d), CheckModule)
	}
	if r := run(okModule(), errors.New("no token")); r.Status != Fail {
		t.Fatalf("open: %+v", r)
	}
	if r := run(&fakeModule{err: errors.New("link down")}, nil); r.Status != Fail || !strings.Contains(r.Detail, "link down") {
		t.Fatalf("call error: %+v", r)
	}
	if r := run(&fakeModule{cmd: serveclient.Command{Lines: []string{"a"}, Complete: false}}, nil); r.Status != Fail || !strings.Contains(r.Detail, "did not end") {
		t.Fatalf("incomplete: %+v", r)
	}
	if r := run(&fakeModule{cmd: serveclient.Command{Lines: []string{"There is no such command."}, Complete: true}}, nil); r.Status != Fail || !strings.Contains(r.Detail, "module") {
		t.Fatalf("module not loaded: %+v", r)
	}
}

func TestSoapChecks(t *testing.T) {
	f := newFx(t)
	o := baseOpts(f)
	c := okConsole()
	c.err = errors.New("connection refused")
	rep := Run(context.Background(), o, goodDeps(okModule(), c))
	if r := find(t, rep, CheckConsole); r.Status != Fail || !strings.Contains(r.Detail, "connection refused") {
		t.Fatalf("%+v", r)
	}
	if r := find(t, rep, CheckBots); r.Status != Warn {
		t.Fatalf("bots cannot be read without SOAP, a warning beside the failure: %+v", r)
	}
	d := goodDeps(okModule(), nil)
	d.Console = nil
	rep = Run(context.Background(), o, d)
	if r := find(t, rep, CheckConsole); r.Status != Warn {
		t.Fatalf("not configured: %+v", r)
	}
	if rep.Blocked() {
		t.Fatal("a missing SOAP config is a warning")
	}
	// the character does not exist: SOAP answers but the commander is unknown
	c = okConsole()
	delete(c.rows, "hero")
	rep = Run(context.Background(), o, goodDeps(okModule(), c))
	if r := find(t, rep, CheckConsole); r.Status != Fail || !strings.Contains(r.Detail, "Hero") {
		t.Fatalf("%+v", r)
	}
	// a reply that stops after its Player line is cut off, not a character that does not exist
	c = okConsole()
	c.rows["hero"] = "Player Hero (GUID: 7)"
	rep = Run(context.Background(), o, goodDeps(okModule(), c))
	if r := find(t, rep, CheckConsole); r.Status != Fail || !strings.Contains(r.Detail, "cut off") {
		t.Fatalf("a cut-off reply must be named as one: %+v", r)
	}
	if r := find(t, rep, CheckBots); r.Status != Warn {
		t.Fatalf("bots cannot be read after a cut-off reply: %+v", r)
	}
}

func TestBotChecks(t *testing.T) {
	f := newFx(t)
	run := func(kind string, mut func(*fakeConsole), bots ...string) Result {
		c := okConsole()
		mut(c)
		o := baseOpts(f)
		o.Kind = kind
		o.Bots = bots
		return find(t, Run(context.Background(), o, goodDeps(okModule(), c)), CheckBots)
	}
	none := func(*fakeConsole) {}
	sleepy := func(c *fakeConsole) { c.rows["heal"] = pinfo("Heal", "ACC1", false) }
	if r := run(KindPlay, sleepy, "Tank", "Heal"); r.Status != Warn || !strings.Contains(r.Detail, "Heal") || !strings.Contains(r.Detail, "offline") {
		t.Fatalf("an offline bot is a warning, named: %+v", r)
	}
	if r := run(KindPlay, none, "Tank", "Ghost"); r.Status != Fail || !strings.Contains(r.Detail, "Ghost") || !strings.Contains(r.Detail, "does not exist") {
		t.Fatalf("missing bot: %+v", r)
	}
	foreign := func(c *fakeConsole) { c.rows["tank"] = pinfo("Tank", "OTHER", true) }
	if r := run(KindPlay, foreign, "Tank"); r.Status != Fail || !strings.Contains(r.Detail, "Tank") || !strings.Contains(r.Detail, "not owned") {
		t.Fatalf("play bots must be owned by the player's account: %+v", r)
	}
	if r := run(KindWatch, foreign, "Tank"); r.Status == Fail {
		t.Fatalf("a watched leader need not be owned: %+v", r)
	}
	if r := run(KindRun, none, "Tank"); r.Status != OK {
		t.Fatalf("%+v", r)
	}
	garbled := func(c *fakeConsole) { c.rows["tank"] = "???" }
	if r := run(KindPlay, garbled, "Tank"); r.Status != Fail {
		t.Fatalf("an unreadable reply must not pass: %+v", r)
	}
	// A bot's reply that stops after its Player line did not finish: it is cut off, not a bot the account misses,
	// and the run must not read it as an online bot (KindWatch has no account test to fail on instead).
	cut := func(c *fakeConsole) { c.rows["tank"] = "Player Tank (GUID: 7)" }
	if r := run(KindWatch, cut, "Tank"); r.Status != Fail || !strings.Contains(r.Detail, "cut off") || !strings.Contains(r.Detail, "Tank") {
		t.Fatalf("a cut-off bot reply must be named as one: %+v", r)
	}
	if r := run(KindPlay, none); r.Status != Warn && r.Status != OK {
		t.Fatalf("no bots named: %+v", r)
	}
}

func TestReportExit(t *testing.T) {
	r := &Report{Results: []Result{{Status: OK}, {Status: Warn}}}
	if r.Blocked() || r.ExitCode() != 1 {
		t.Fatalf("warn: %v %d", r.Blocked(), r.ExitCode())
	}
	r.Results = append(r.Results, Result{Status: Fail})
	if !r.Blocked() || r.ExitCode() != 2 {
		t.Fatalf("fail: %v %d", r.Blocked(), r.ExitCode())
	}
	if (&Report{}).ExitCode() != 0 {
		t.Fatal("empty is 0")
	}
}

func TestPrintHidesNothingButSecrets(t *testing.T) {
	var b strings.Builder
	(&Report{Results: []Result{{Check: CheckServe, Status: Fail, Detail: "x", Fix: "start serve"}}}).Print(&b)
	if !strings.Contains(b.String(), "FAIL") || !strings.Contains(b.String(), "fix: start serve") || !strings.Contains(b.String(), "1 failure") {
		t.Fatal(b.String())
	}
}
