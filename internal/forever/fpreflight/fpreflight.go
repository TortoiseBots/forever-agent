// Package fpreflight checks a Forever run before it starts: the serve slot (its pid and its port), the lease, the plan
// and its exported rows, the module answering a cheap `.tb` read over the serve link, the SOAP console and the bots named
// on the command line. It is the Forever side of walk-preflight (internal/preflight, the Turtle raid walk's check) and is
// read-only: nothing here writes a lease, a file or a server setting.
//
// The server side is read over SOAP, never over ssh and never through the Turtle toolkit. The setup allow-list of the
// SOAP client has no command that prints a config key, so the console check proves the endpoint answers and
// authenticates (a `.pinfo` read) and the bots check reads each bot the same way.
//
// The export check reads the exported migration FILE (present, parses, names the plan with its map and legs). No console
// or module command reads back which plan rows the server holds, so a file that was never applied, or rows left from an
// older export, are not seen: the report says so beside every OK.
package fpreflight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
)

// Check names.
const (
	CheckServe  = "serve"
	CheckLease  = "lease"
	CheckPlan   = "plan"
	CheckExport = "export"
	CheckModule = "module"
	CheckSoap   = "soap"
	CheckBots   = "bots"
)

// The run commands the preflight serves. Play needs the bots owned by the player's account; a watch or a run names bots
// the module leads, which need exist only.
const (
	KindWatch = "dungeon-watch"
	KindRun   = "dungeon-run"
	KindPlay  = "dungeon-play"
)

// moduleProbe is the cheap read that proves the module answers over the serve link.
const moduleProbe = ".tb state party"

// moduleWait is how long the probe may wait for the module's reply.
const moduleWait = 5 * time.Second

// Status is the outcome of one check.
type Status string

// The three outcomes. A Fail blocks a run; a Warn does not.
const (
	OK   Status = "OK"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// Result is one line of the report.
type Result struct {
	Check  string
	Status Status
	Detail string
	Fix    string
}

// Report is the whole run.
type Report struct{ Results []Result }

// Counts returns the number of warnings and failures.
func (r *Report) Counts() (warns, fails int) {
	for _, x := range r.Results {
		switch x.Status {
		case Warn:
			warns++
		case Fail:
			fails++
		}
	}
	return warns, fails
}

// Blocked is true when any check failed: the run must not start.
func (r *Report) Blocked() bool {
	_, fails := r.Counts()
	return fails > 0
}

// ExitCode is walk-preflight's contract: 0 all OK, 1 warnings only, 2 any FAIL.
func (r *Report) ExitCode() int {
	warns, fails := r.Counts()
	switch {
	case fails > 0:
		return 2
	case warns > 0:
		return 1
	}
	return 0
}

// Print writes one line per check and a total.
func (r *Report) Print(w io.Writer) {
	for _, x := range r.Results {
		line := fmt.Sprintf("preflight: %-4s %-7s %s", x.Status, x.Check, x.Detail)
		if x.Fix != "" {
			line += " -- fix: " + x.Fix
		}
		fmt.Fprintln(w, line)
	}
	warns, fails := r.Counts()
	fmt.Fprintf(w, "preflight: %d check line(s): %d warning(s), %d failure(s)\n", len(r.Results), warns, fails)
}

// Module is the serve link's `.tb` line: serveclient.Client satisfies it.
type Module interface {
	Bot(ctx context.Context, line string, wait time.Duration) (serveclient.Command, error)
}

// Console runs one SOAP console command (soap.Client satisfies it).
type Console interface {
	Run(ctx context.Context, command string) (string, error)
}

// Deps are the effects the checks use; a test replaces every one, so no test touches the network or /proc.
type Deps struct {
	Now        func() time.Time
	Probe      slots.Probe
	OpenModule func(dir string) (Module, error)
	// Console is nil when no SOAP endpoint is configured.
	Console Console
}

// DefaultDeps are the live effects: the real clock, /proc and the typed serve client. The console is the caller's.
func DefaultDeps(console Console) Deps {
	return Deps{
		Now:   time.Now,
		Probe: slots.LinuxProbe{},
		OpenModule: func(dir string) (Module, error) {
			c, err := serveclient.Open(dir)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Console: console,
	}
}

// Options describe the run to check.
type Options struct {
	Kind string
	// ServeDir is the `forever serve --dir`; empty skips the serve, lease and module checks with one warning.
	ServeDir string
	// Plan loads the plan with the run command's own loader and returns its name and map, or why it is refused.
	Plan func() (name string, mapID uint32, err error)
	// Export is the plan's exported migration, or a folder holding plan_<name>.sql; empty is a warning.
	Export string
	// Character is the commander or player; its SOAP account is the one the owned bots must share.
	Character string
	// Bots are the bots named on the command line.
	Bots []string
}

// Run does every check and returns the report. It never stops at the first failure: one run shows every problem.
func Run(ctx context.Context, o Options, d Deps) *Report {
	if d.Now == nil {
		d.Now = time.Now
	}
	r := &Report{}
	add := func(check string, s Status, detail, fix string) {
		r.Results = append(r.Results, Result{Check: check, Status: s, Detail: detail, Fix: fix})
	}
	name, mapID := checkPlan(o, add)
	checkExport(o, name, mapID, add)
	if o.ServeDir == "" {
		add(CheckServe, Warn, "no --serve-dir: the serve slot, its lease and the module link are not checked", "pass --serve-dir")
	} else {
		abs, m := checkServe(o.ServeDir, d, add)
		if m != nil {
			checkLease(abs, m, d, add)
			checkModule(ctx, abs, d, add)
		}
	}
	checkSoapAndBots(ctx, o, d, add)
	return r
}

func checkPlan(o Options, add func(string, Status, string, string)) (string, uint32) {
	if o.Plan == nil {
		add(CheckPlan, Fail, "no plan check was given", "pass the plan")
		return "", 0
	}
	name, mapID, err := o.Plan()
	if err != nil {
		add(CheckPlan, Fail, "the plan is refused: "+err.Error(), "fix the plan file")
		return "", 0
	}
	add(CheckPlan, OK, fmt.Sprintf("plan %s, map %d", name, mapID), "")
	return name, mapID
}

func checkExport(o Options, name string, mapID uint32, add func(string, Status, string, string)) {
	if o.Export == "" {
		add(CheckExport, Warn, "no --export: the plan's export file is not checked", "pass --export FILE or its folder")
		return
	}
	path := o.Export
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		if name == "" {
			add(CheckExport, Fail, "the export is a folder and the plan has no name yet", "fix the plan first")
			return
		}
		path = filepath.Join(path, "plan_"+name+".sql")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		add(CheckExport, Fail, "cannot read the export: "+err.Error(), "export the plan (plan-export --forever)")
		return
	}
	reports, err := planexport.ParseForever(string(raw))
	if err != nil {
		add(CheckExport, Fail, fmt.Sprintf("%s does not parse: %v", filepath.Base(path), err), "export the plan again")
		return
	}
	if name == "" {
		add(CheckExport, Fail, "the export cannot be matched: the plan is refused", "fix the plan first")
		return
	}
	for _, rep := range reports {
		if rep.Rows.Plan.Plan != name {
			continue
		}
		switch {
		case rep.Rows.Plan.Map != mapID:
			add(CheckExport, Fail, fmt.Sprintf("%s: map %d, the plan's map is %d", filepath.Base(path), rep.Rows.Plan.Map, mapID), "export the plan again")
		case len(rep.Rows.Legs) == 0:
			add(CheckExport, Fail, filepath.Base(path)+" has no legs for the plan", "export the plan again")
		default:
			add(CheckExport, OK, fmt.Sprintf("%s: map %d, %d leg(s) (the file only: the rows the server loaded are not read)", filepath.Base(path), rep.Rows.Plan.Map, len(rep.Rows.Legs)), "")
		}
		return
	}
	add(CheckExport, Fail, fmt.Sprintf("%s has no plan %s", filepath.Base(path), name), "export the plan again")
}

// checkServe reads the slot manifest, then asks the probe whether the serve daemon of that dir is alive and listening.
func checkServe(dir string, d Deps, add func(string, Status, string, string)) (string, *slotdir.Manifest) {
	abs, m, err := slotdir.Read(dir)
	if err != nil {
		add(CheckServe, Fail, "the serve dir has no readable slot.json", "start `forever serve --dir`")
		return abs, nil
	}
	if err := slotdir.Check("preflight", abs, m); err != nil {
		add(CheckServe, Fail, err.Error(), "use a dir made by forever serve")
		return abs, nil
	}
	if d.Probe == nil {
		add(CheckServe, Fail, "no process probe", "")
		return abs, nil
	}
	pid, alive := d.Probe.AgentPID(abs)
	if !alive {
		add(CheckServe, Fail, "the serve daemon is not running (its pid file names no live serve of this dir)", "start `forever serve --dir`")
		return abs, nil
	}
	if !d.Probe.PortListening(m.Port) {
		add(CheckServe, Fail, fmt.Sprintf("serve pid %d is alive but its port %d does not accept connections", pid, m.Port), "restart the serve")
		return abs, nil
	}
	add(CheckServe, OK, fmt.Sprintf("serve pid %d alive, port %d listening", pid, m.Port), "")
	return abs, m
}

// checkLease reads the lease the manifest names, without taking, renewing or reaping it.
func checkLease(abs string, m *slotdir.Manifest, d Deps, add func(string, Status, string, string)) {
	if m.SlotsPath == "" {
		add(CheckLease, OK, "this serve dir is not leased (no slots path)", "")
		return
	}
	path := filepath.Join(slots.LeasesDir(m.SlotsPath), m.Server, m.Slot+".json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		add(CheckLease, Fail, fmt.Sprintf("no lease for %s slot %s", m.Server, m.Slot), "acquire the slot again")
		return
	}
	var l slots.Lease
	if err == nil {
		err = json.Unmarshal(raw, &l)
	}
	if err != nil {
		add(CheckLease, Fail, "the lease file is unreadable", "acquire the slot again")
		return
	}
	switch {
	case l.Holder != m.Holder:
		add(CheckLease, Fail, fmt.Sprintf("the lease is held by %q, this serve's holder is %q", l.Holder, m.Holder), "another run owns the slot")
	case m.LeaseID != "" && l.LeaseID != m.LeaseID:
		add(CheckLease, Fail, "the lease id is not the one the slot manifest names (the lease was taken over)", "acquire the slot again")
	case !l.Expires.IsZero() && !d.Now().Before(l.Expires):
		add(CheckLease, Warn, "the lease expired at "+l.Expires.UTC().Format("15:04:05Z")+" (the serve is alive; renew it)", "renew the lease")
	default:
		add(CheckLease, OK, fmt.Sprintf("lease held by %s until %s", l.Holder, l.Expires.UTC().Format("15:04:05Z")), "")
	}
}

// checkModule sends the cheap read over the serve link: the module must end its reply.
func checkModule(ctx context.Context, dir string, d Deps, add func(string, Status, string, string)) {
	if d.OpenModule == nil {
		add(CheckModule, Fail, "no module link", "")
		return
	}
	mod, err := d.OpenModule(dir)
	if err != nil {
		add(CheckModule, Fail, "cannot open the serve link: "+err.Error(), "check the serve's token file")
		return
	}
	cmd, err := mod.Bot(ctx, moduleProbe, moduleWait)
	if err != nil {
		add(CheckModule, Fail, fmt.Sprintf("%s: %v", moduleProbe, err), "check the serve session and its GM rank")
		return
	}
	for _, line := range cmd.Lines {
		if strings.Contains(strings.ToLower(line), "no such command") {
			add(CheckModule, Fail, "the server does not know "+moduleProbe+": the bot module is not loaded", "load the module")
			return
		}
	}
	if !cmd.Complete {
		add(CheckModule, Fail, fmt.Sprintf("%s did not end its reply (%d line(s))", moduleProbe, len(cmd.Lines)), "check the module")
		return
	}
	add(CheckModule, OK, fmt.Sprintf("%s answered, %d line(s)", moduleProbe, len(cmd.Lines)), "")
}

var accountRe = regexp.MustCompile(`(?i)\baccount:\s*(\S+)`)

// pinfoRow is what a `.pinfo NAME` reply says about one character. The core prints `Player[ (offline)] NAME (GUID: N)`
// and `Account: NAME (ID: N), GMLevel: ...`, and `Player not found!` for an unknown name.
type pinfoRow struct {
	found, online bool
	account       string
}

func parsePinfo(text string) pinfoRow {
	low := strings.ToLower(text)
	row := pinfoRow{found: strings.Contains(low, "guid:") && !strings.Contains(low, "not found")}
	if !row.found {
		return row
	}
	row.online = !strings.Contains(low, "(offline)")
	if m := accountRe.FindStringSubmatch(text); m != nil {
		row.account = strings.ToLower(m[1])
	}
	return row
}

func checkSoapAndBots(ctx context.Context, o Options, d Deps, add func(string, Status, string, string)) {
	if d.Console == nil {
		add(CheckSoap, Warn, "no SOAP endpoint configured: the server is not read", "pass --soap-user and a password file")
		add(CheckBots, Warn, "the bots are not checked without SOAP", "")
		return
	}
	read := func(name string) (pinfoRow, error) {
		text, err := d.Console.Run(ctx, ".pinfo "+name)
		if err != nil {
			return pinfoRow{}, err
		}
		return parsePinfo(text), nil
	}
	var player pinfoRow
	subject := o.Character
	if subject == "" && len(o.Bots) > 0 {
		subject = o.Bots[0]
	}
	if subject == "" {
		add(CheckSoap, Warn, "nothing to read: no character and no bots named", "")
		return
	}
	player, err := read(subject)
	if err != nil {
		add(CheckSoap, Fail, "the SOAP console does not answer: "+err.Error(), "check --soap-host, --soap-port and the account")
		add(CheckBots, Warn, "the bots are not checked: SOAP does not answer", "")
		return
	}
	if o.Character != "" && !player.found {
		add(CheckSoap, Fail, fmt.Sprintf("SOAP answers but character %s does not exist", o.Character), "check --character")
		add(CheckBots, Warn, "the bots are not checked: the character is unknown", "")
		return
	}
	add(CheckSoap, OK, "SOAP answers", "")
	if len(o.Bots) == 0 {
		add(CheckBots, OK, "no bots named", "")
		return
	}
	var missing, offline, foreign []string
	for _, b := range o.Bots {
		row, err := read(b)
		if err != nil {
			add(CheckBots, Fail, fmt.Sprintf("reading %s: %v", b, err), "")
			return
		}
		switch {
		case !row.found:
			missing = append(missing, b)
		default:
			if !row.online {
				offline = append(offline, b)
			}
			if o.Kind == KindPlay && (player.account == "" || row.account != player.account) {
				foreign = append(foreign, b)
			}
		}
	}
	switch {
	case len(missing) > 0:
		add(CheckBots, Fail, "bot(s) "+strings.Join(missing, ", ")+" does not exist", "check --bots / --party / --leader")
	case len(foreign) > 0:
		add(CheckBots, Fail, "bot(s) "+strings.Join(foreign, ", ")+" not owned by the player's account", "use bots the account owns")
	case len(offline) > 0:
		add(CheckBots, Warn, "bot(s) "+strings.Join(offline, ", ")+" offline (the run logs them in)", "")
	default:
		add(CheckBots, OK, fmt.Sprintf("%d bot(s) exist and are online", len(o.Bots)), "")
	}
}
