package main

// forever serve: the long-running session. It logs in like world-check, enters the named character, and then serves
// the local JSON-RPC socket (the same protocol and error codes the old daemon serves) until Ctrl-C or logout. A
// dropped world socket is logged in again within a bounded budget; only when that budget is spent does the daemon go
// offline and end (see internal/forever/world/serve.go for the limits). The password comes from --password-file or
// FOREVER_PASSWORD and is never printed.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/servesettings"
	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/netutil"
	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
)

// mapPairs is --parent MAP=PARENT, repeatable.
type mapPairs map[uint32]uint32

// serveLogin is the login step of `forever serve`: bnet.Login through foreverCommon, with the
// world-socket build probe. It is a variable so a test can stage the login (like authLogin does for
// the older commands) and drive the command's own lifetime without realmd.
var serveLogin = func(ctx context.Context, c *foreverCommon, stdout io.Writer, password string) (*bnet.Result, error) {
	return c.loginWithPassword(ctx, "forever serve", stdout, password, c.worldAccept(stdout))
}

// serveWorldEnter is the world half of `forever serve` (world.Enter). A test replaces it with a fake
// session to exercise the command's lease keeping and its shutdown without a game server.
var serveWorldEnter = world.Enter

func (m mapPairs) String() string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%d=%d", k, v))
	}
	return strings.Join(parts, ",")
}

func (m mapPairs) Set(s string) error {
	a, b, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("want MAP=PARENT")
	}
	from, err := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
	if err != nil {
		return fmt.Errorf("bad map id %q", a)
	}
	to, err := strconv.ParseUint(strings.TrimSpace(b), 10, 32)
	if err != nil {
		return fmt.Errorf("bad parent map id %q", b)
	}
	m[uint32(from)] = uint32(to)
	return nil
}

// servePassword reads the password: the file's bytes with one trailing newline removed, or the environment.
// --password-file has always taken any file the user can read; a settings file's password_file goes through
// serveSettingsPassword, which is strict.
func servePassword(path string) (string, error) {
	if path == "" {
		return os.Getenv("FOREVER_PASSWORD"), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("password file: %w", err)
	}
	if len(raw) > 4096 {
		return "", fmt.Errorf("password file: more than 4096 bytes")
	}
	s := string(raw)
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return "", fmt.Errorf("password file %s is empty", path)
	}
	return s, nil
}

// serveCheckDir decides what serve may do with --dir before it writes anything there. A dir with a slot.json
// made by `slot setup --forever` (or by an earlier serve) is kept as is when it describes this serve: the same
// dir, the same port and character, and the same account when the manifest names one. Its holder, slot and lease
// id are the slot's, not serve's to rewrite. A slot.json that does not match, one that cannot be read, or a
// non-empty dir with no slot.json is a foreign dir: serve refuses instead of overwriting. An empty dir (or a
// missing one, already made) is fresh: serve writes its own manifest.
func serveCheckDir(dir, account, character string, port int) (keep bool, err error) {
	raw, rerr := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if rerr != nil {
		if !errors.Is(rerr, os.ErrNotExist) {
			return false, fmt.Errorf("%s/slot.json is not readable (%v): serve will not overwrite it", dir, rerr)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			// A crash between the token and the manifest leaves only these.
			if n := e.Name(); n == "agent.pid" || n == serveLockName || (strings.HasPrefix(n, "rpc-") && strings.HasSuffix(n, ".token")) {
				continue
			}
			return false, fmt.Errorf("%s is not empty and has no slot.json: pick an empty --dir or one made by slot setup --forever (serve never overwrites a foreign dir)", dir)
		}
		return false, nil
	}
	var m teardownSlotManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Server == "" || m.Slot == "" || m.Holder == "" {
		return false, fmt.Errorf("%s/slot.json is not a slot manifest: serve will not overwrite it", dir)
	}
	if err := teardownCheckManifest(dir, &m); err != nil {
		return false, fmt.Errorf("%s/slot.json does not belong to this dir (%v): serve will not overwrite it", dir, err)
	}
	if m.Port != port {
		return false, fmt.Errorf("%s/slot.json is for port %d, this serve listens on %d: serve will not overwrite it", dir, m.Port, port)
	}
	if !strings.EqualFold(m.Character, character) || (m.Account != "" && !strings.EqualFold(m.Account, account)) {
		return false, fmt.Errorf("%s/slot.json is for another account or character: serve will not overwrite it", dir)
	}
	return true, nil
}

// serveManifest writes the no-secret slot.json that the existing `rpc` client reads, plus the pid file teardown
// uses. The paths are the ones teardownCheckManifest derives from the dir. keep leaves an existing manifest
// (see serveCheckDir) as it is, apart from its no_auth mark (which must say how this serve runs), and writes the
// pid file.
func serveManifest(dir, tokenFile, account, character string, port int, noAuth, keep bool) error {
	bin := filepath.Join(dir, "forever-agent")
	// The start time binds agent.pid to this process: a later teardown (or lease check) that finds the pid reused by
	// another process is not fooled by a command line alone. Off Linux there is no /proc and no serve slot either.
	start, _ := slots.ProcStartTicks(os.Getpid())
	m := teardownSlotManifest{
		Server: "forever", Slot: "1", Holder: "forever",
		Account: account, Character: character, Port: port, Dir: dir,
		Binary: bin, Settings: filepath.Join(dir, "settings.json"),
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: tokenFile,
		NoAuth:    noAuth,
		PIDStart:  start,
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	if keep {
		// The setup manifest stays, but a client tells a no-token daemon from a lost token file by no_auth, and the
		// pid file must name this process's start time.
		_, kept, err := slotdir.Read(dir)
		if err != nil {
			return err
		}
		if kept.NoAuth != noAuth || kept.PIDStart != start {
			kept.NoAuth = noAuth
			kept.PIDStart = start
			raw, err := json.MarshalIndent(kept, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
				return err
			}
		}
	} else {
		raw, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(m.PIDFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

func foreverServeCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever serve [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--password-file FILE] [--listen HOST:PORT] [--dir DIR] [--token-file FILE] [--no-auth] [--navmesh DIR] [--parent MAP=PARENT] [--plans-dir DIR] [--disconnect-grace DURATION] [--allow-gm-units] [--addon on|off] [--capture-dir DIR] [--settings FILE] [--force] [--login-budget ADDR] [--create RACE,CLASS[,SEX] (--create-choice OPTION:CHOICE ... | --create-no-appearance)]"
	fs := flag.NewFlagSet("forever serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	settingsPath := fs.String("settings", "", "settings file for a Forever slot (account, character, rest/rpc/ca, navmesh, plans and serve's other flags); a flag given on the command line wins over the file")
	force := fs.Bool("force", false, "start even when the slot's lease has expired (the serve renews it); never takes a live lease from another holder")
	passwordFile := fs.String("password-file", "", "file holding the account password (default: FOREVER_PASSWORD, or the settings file's password_file/password)")
	listen := fs.String("listen", "127.0.0.1:8765", "loopback address the RPC socket listens on")
	dir := fs.String("dir", "", "slot-style dir: slot.json, the pid file and the token file are written there so the rpc client works")
	tokenFile := fs.String("token-file", "", "RPC bearer token file (default: rpc-<port>.token in --dir or the working directory)")
	noAuth := fs.Bool("no-auth", false, "serve without a bearer token (loopback and Host checks only)")
	navmesh := fs.String("navmesh", "", "mmaps folder that enables path_to and walk_path")
	plansDir := fs.String("plans-dir", "dungeons", "folder map_snapshot reads a plan from")
	parents := mapPairs{}
	fs.Var(parents, "parent", "MAP=PARENT: an instance map whose missing tile comes from that parent (repeatable)")
	grace := fs.Duration("disconnect-grace", 30*time.Second, "how long the RPC stays up once the reconnect budget is spent (the daemon is offline)")
	allowGMUnits := fs.Bool("allow-gm-units", false, "test bench: allow gm_kill, gm_set_hp and gm_aura on the guids trial_units registered")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path, from the first login on (one cap for the whole daemon; keep it outside the repository)")
	createSpec := fs.String("create", "", "create the character on login when the account lacks it: RACE,CLASS[,SEX] (needs --create-choice or --create-no-appearance)")
	var createChoices charChoiceList
	fs.Var(&createChoices, "create-choice", "with --create: appearance choice OPTION:CHOICE (repeatable; sent as given)")
	createNoAppearance := fs.Bool("create-no-appearance", false, "with --create: a test character with no appearance choices")
	budgetAddr := fs.String("login-budget", "", "loopback address of the fleet's shared login budget (set by `fleet`; its token is in the environment)")
	addon := fs.String("addon", "on", "add-on transport: on registers the TBM and bot-module prefixes when a command needs one, off never registers any (the session never speaks TBM)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	// A settings file fills the flags the command line did not set. The serve may cancel its own
	// run: a renewal that loses the lease to another holder stops it cleanly.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var settings *servesettings.Settings
	if *settingsPath != "" {
		st, err := servesettings.Load(*settingsPath)
		if err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		}
		settings = &st
		serveFromSettings(st, set, &common, serveFlagOverrides{
			listen: listen, dir: dir, tokenFile: tokenFile, navmesh: navmesh, plansDir: plansDir,
			captureDir: captureDir, addon: addon, noAuth: noAuth, allowGMUnits: allowGMUnits,
			parents: parents, grace: grace,
		})
	}
	addonOff := false
	switch strings.ToLower(strings.TrimSpace(*addon)) {
	case "on":
	case "off":
		addonOff = true
	default:
		fmt.Fprintln(stderr, "forever serve: --addon must be on or off")
		return exitUsage
	}
	var budget *budgetClient
	if *budgetAddr != "" {
		if _, port, err := netutil.CheckLoopbackAddr(*budgetAddr); err != nil || port == 0 {
			fmt.Fprintln(stderr, "forever serve: --login-budget must be a loopback address with a port")
			return exitUsage
		}
		tok := os.Getenv(budgetTokenEnv)
		if tok == "" {
			fmt.Fprintf(stderr, "forever serve: --login-budget needs the token in %s\n", budgetTokenEnv)
			return exitUsage
		}
		budget = newBudgetClient(*budgetAddr, tok)
	}
	if err := common.check("forever serve"); err != nil {
		if settings != nil {
			fmt.Fprintf(stderr, "%v (from --settings %s and the command line)\n", err, *settingsPath)
		} else {
			fmt.Fprintln(stderr, err)
		}
		return exitUsage
	}
	create, err := parseServeCreate(*createSpec, createChoices, *createNoAppearance)
	if err != nil {
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitUsage
	}
	if *captureDir != "" {
		// Refused before the login: a parent inside a repository ends the daemon at once.
		if err := world.CheckCaptureDir(*captureDir); err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		}
	}
	if *grace < 0 || *grace > 10*time.Minute {
		fmt.Fprintln(stderr, "forever serve: --disconnect-grace must be 0 to 10m")
		return exitUsage
	}
	if *navmesh != "" {
		if info, err := os.Stat(*navmesh); err != nil || !info.IsDir() {
			fmt.Fprintf(stderr, "forever serve: --navmesh %s is not a folder\n", *navmesh)
			return exitUsage
		}
	}
	password, err := servePasswordFor(set["password-file"], *passwordFile, settings)
	if err != nil {
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitUsage
	}
	if password == "" {
		fmt.Fprintln(stderr, "forever serve: no password (set FOREVER_PASSWORD, pass --password-file, or give password/password_file in --settings)")
		return exitUsage
	}

	ln, err := rpc.Listen(ctx, *listen)
	if err != nil {
		fmt.Fprintln(stderr, "rpc:", err)
		return exitUsage
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	keepManifest := false
	var lease *serveLease
	var releaseDir func()
	if *dir != "" {
		if info, err := os.Lstat(*dir); err == nil {
			if !info.IsDir() {
				fmt.Fprintf(stderr, "forever serve: %s exists and is not a folder\n", *dir)
				return exitUsage
			}
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		} else if err := os.MkdirAll(*dir, 0o700); err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		}
		*dir, err = filepath.Abs(*dir)
		if err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		}
		keepManifest, err = serveCheckDir(*dir, common.account, common.character, port)
		if err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		}
		// One serve per dir, whatever its listen address: the dir's manifest, token file and lease
		// are one serve's. The lock is released by the kernel if this process dies.
		releaseDir, err = lockServeDir(*dir)
		if err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitSlotRefused
		}
		defer releaseDir()
		lease, err = openServeLease(*dir)
		if err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitSlotRefused
		}
		if lease != nil {
			// The lease is the slot's, so an expired one or another holder's stops the serve before it
			// logs in; --force only overrides the clock.
			if err := lease.adopt(*force); err != nil {
				fmt.Fprintf(stderr, "forever serve: %v\n", err)
				return exitSlotRefused
			}
			// The keeper logs renewals to stdout: the command waits for it (below) before it returns,
			// so no goroutine writes to an output the caller already owns again. The wait is bounded:
			// a renewal stuck in a write must not keep a shutdown from returning, and its lock wait
			// already ends with ctx (slots.Store.RenewContext).
			keepDone := lease.keep(ctx, cancel, stdout)
			defer func() {
				cancel()
				select {
				case <-keepDone:
				case <-time.After(serveLeaseStopWait):
					fmt.Fprintf(stdout, "serve: the lease keeper did not stop within %s; not waiting for it\n", serveLeaseStopWait)
				}
			}()
		}
	}
	if *tokenFile == "" {
		base := "."
		if *dir != "" {
			base = *dir
		}
		*tokenFile = filepath.Join(base, fmt.Sprintf("rpc-%d.token", port))
	}
	*tokenFile, err = filepath.Abs(*tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitUsage
	}
	token := ""
	if !*noAuth {
		token, err = rpc.NewToken()
		if err != nil {
			fmt.Fprintln(stderr, "rpc:", err)
			return exitUsage
		}
		removeToken, err := rpc.WriteTokenFile(*tokenFile, token)
		if err != nil {
			fmt.Fprintln(stderr, "rpc:", err)
			return exitUsage
		}
		defer removeToken()
	}
	if *dir != "" {
		if err := serveManifest(*dir, *tokenFile, common.account, common.character, port, *noAuth, keepManifest); err != nil {
			fmt.Fprintf(stderr, "forever serve: %v\n", err)
			return exitUsage
		}
		defer os.Remove(filepath.Join(*dir, "agent.pid"))
	}

	// Every login attempt of the daemon, the first login's build probes and each reconnect's, waits for its turn in the
	// gate and books it (bnet.Options.BeforeAttempt).
	gate := newLoginGate(budget, stdout)
	common.beforeAttempt = func(ctx context.Context) error { return gate.enter(ctx, 0) }
	if create != nil {
		if code := serveEnsureCharacter(ctx, &common, password, create, stdout, stderr); code != exitOK {
			return code
		}
	}
	result, err := serveLogin(ctx, &common, stdout, password)
	if err != nil {
		gate.failed(err)
		if lease != nil {
			if _, lost := lease.lostReason(); lost {
				return exitServeLeaseLost
			}
		}
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitAuth
	}
	opts := common.withLogin(result, stdout, world.Options{
		AddonOff: addonOff,
		Serve: &world.ServeConfig{
			Listener:        ln,
			Token:           token,
			NavmeshDir:      *navmesh,
			PlansDir:        *plansDir,
			Dir:             *dir,
			Parents:         parents,
			DisconnectGrace: *grace,
			AllowGMUnits:    *allowGMUnits,
			CaptureDir:      *captureDir,
			// A reconnect logs in again: the first login's join ticket is spent when the link drops.
			Relogin: func(ctx context.Context, o world.Options) (world.Options, error) {
				return reloginThrough(ctx, gate, o, func(lctx context.Context) (*bnet.Result, error) {
					return serveLogin(lctx, &common, stdout, password)
				}, func(r *bnet.Result, o world.Options) world.Options { return common.withLogin(r, stdout, o) })
			},
		},
	})
	fmt.Fprintf(stdout, "rpc: listening on http://%s/rpc\n", ln.Addr())
	if token != "" {
		fmt.Fprintf(stdout, "rpc: token file %s\n", *tokenFile)
	} else {
		fmt.Fprintln(stdout, "rpc: authentication off")
	}
	report, err := serveWorldEnter(ctx, opts)
	if lease != nil {
		// A renewal that found another holder cancelled the run and already logged why; the exit
		// code says the stop was the lease, not the game or the network.
		if _, lost := lease.lostReason(); lost {
			return exitServeLeaseLost
		}
	}
	if report != nil {
		fmt.Fprintf(stdout, "serve: %d server packet(s) in %d opcode(s)\n", reportTotal(report), len(report.Counts))
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stdout, "serve: logged out")
	return exitOK
}

func reportTotal(r *world.Report) int {
	total := 0
	for _, n := range r.Counts {
		total += n
	}
	return total
}
