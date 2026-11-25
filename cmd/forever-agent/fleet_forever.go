// fleet --forever: fleet members that are `forever serve` daemons instead of
// old `login` children. One serve per member: `forever serve --dir --listen
// --account --character --password-file` plus the server flags from the slots
// file's `forever` block. `online` (the serve link lifecycle) counts as in
// world, on this path only; logins are paced by a loginBudget of one per 5 s.
// Before any login every member is checked against the slots file's leases
// and allowlist (fleetForeverCheck).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/netutil"
	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

// fleetForeverMember is one entry of a `fleet --forever` fleet file: the
// account, the character, the slot-style dir the serve writes its manifest
// into, the RPC listen address, and the password file the child reads. The
// file carries no secret: only paths and names. GameAccount names the game
// account the login joins, by the join ticket's #N (the serve's own
// --game-account); absent or 0 is the first listed (#236 point 3: a
// Battle.net account with several game accounts needs it).
type fleetForeverMember struct {
	Account      string `json:"account"`
	Character    string `json:"character"`
	Dir          string `json:"dir"`
	Listen       string `json:"listen"`
	PasswordFile string `json:"password_file"`
	GameAccount  int    `json:"game_account,omitempty"`
}

type fleetForeverFile struct {
	Members []fleetForeverMember `json:"members"`
}

// loadFleetForever reads and validates a `fleet --forever` fleet file: every
// entry needs an account, a character, a dir, a loopback listen address with
// a fixed port, and a password file. Accounts, characters, dirs and listen
// ports must be distinct. A secret smuggled into the file is refused by the strict
// decoder, like any other unknown field.
func loadFleetForever(path string) ([]fleetForeverMember, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fleet %s: %w", path, err)
	}
	var f fleetForeverFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		detail := ""
		if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
			detail = ": " + strings.TrimPrefix(msg, "json: ")
		}
		return nil, fmt.Errorf("fleet %s is not valid fleet JSON%s", path, detail)
	}
	if len(f.Members) == 0 {
		return nil, fmt.Errorf("fleet: \"members\" must list at least one entry")
	}
	chars := map[string]bool{}
	accounts := map[string]bool{}
	ports := map[int]bool{}
	dirs := map[string]bool{}
	for i := range f.Members {
		m := &f.Members[i]
		n := i + 1
		if m.Account == "" || m.Character == "" || m.Dir == "" || m.Listen == "" || m.PasswordFile == "" {
			return nil, fmt.Errorf("fleet: member %d needs account, character, dir, listen and password_file", n)
		}
		host, port, err := netutil.CheckLoopbackAddr(m.Listen)
		if err != nil {
			return nil, fmt.Errorf("fleet: member %d listen %w", n, err)
		}
		if port == 0 {
			return nil, fmt.Errorf("fleet: member %d listen needs a fixed port (the supervisor polls it)", n)
		}
		// Ports must differ outright: 127.0.0.1:N and ::1:N are different
		// sockets, but a fleet that mixes them is almost surely a typo.
		if ports[port] {
			return nil, fmt.Errorf("fleet: member %d listen port %d is used twice", n, port)
		}
		ports[port] = true
		m.Listen = net.JoinHostPort(host, strconv.Itoa(port))
		if dirs[m.Dir] {
			return nil, fmt.Errorf("fleet: member %d dir %q is used twice", n, m.Dir)
		}
		dirs[m.Dir] = true
		// One realmd session per account: two members on one account kick
		// each other and the fleet never settles.
		acc := strings.ToLower(m.Account)
		if accounts[acc] {
			return nil, fmt.Errorf("fleet: account %q is listed twice (one session per account)", m.Account)
		}
		accounts[acc] = true
		key := strings.ToLower(m.Character)
		if chars[key] {
			return nil, fmt.Errorf("fleet: character %q is listed twice", m.Character)
		}
		chars[key] = true
		if m.GameAccount < 0 {
			return nil, fmt.Errorf("fleet: member %d (%s): game_account is 1 or more (0 or absent joins the first listed)", n, m.Character)
		}
	}
	return f.Members, nil
}

// foreverChildArgs is the `forever serve` command line of one member (after
// the executable). The password travels by file, never on the command line.
func foreverChildArgs(ext live.ServerExt, m fleetForeverMember) []string {
	args := []string{"forever", "serve",
		"--rest", ext.Rest, "--rpc", ext.RPC, "--ca", ext.CA,
		"--account", m.Account, "--character", m.Character,
		"--password-file", m.PasswordFile,
		"--listen", m.Listen, "--dir", m.Dir,
	}
	if m.GameAccount > 0 {
		args = append(args, "--game-account", strconv.Itoa(m.GameAccount))
	}
	if ext.Build != "" && ext.Build != "0" {
		args = append(args, "--build", ext.Build)
	}
	return args
}

// fleetReady reports whether a polled `forever serve` state counts as in
// world: its link lifecycle answers "online"; "reconnecting" and "offline"
// are not ready. The Turtle `fleet` keeps its own rule (in_world only, see
// fleetReadyTurtle in fleet.go): "online" never counts there.
func fleetReady(state string) bool { return state == "online" }

// fleetForeverPace is the login pacing between `forever serve` children: one
// login per 5 s, so the fleet stays under realmd's per-IP login limit.
const fleetForeverPace = 5 * time.Second

// fleetGameAccounts asks the login service how many game accounts an account lists, by logging in to it (SRP, the
// RPC logon and the realm join; no world socket, no character). before is bnet's own BeforeAttempt, so every attempt
// of an unpinned build is booked in the fleet's login budget like any other login. It is a variable so the tests
// script the answer.
var fleetGameAccounts = func(ctx context.Context, ext live.ServerExt, m fleetForeverMember, password string,
	before func(context.Context) error) (int, error) {
	opts := bnet.Options{
		RESTAddr: ext.Rest, RPCAddr: ext.RPC, CAFile: ext.CA,
		Account: m.Account, Password: password, GameAccount: m.GameAccount,
		BeforeAttempt: before,
	}
	if ext.Build != "" && ext.Build != "0" {
		n, err := strconv.ParseUint(ext.Build, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("the slots file's build %q is not a number", ext.Build)
		}
		opts.ClientBuild = uint32(n)
	}
	res, err := bnet.Login(ctx, opts)
	if err != nil {
		return 0, err
	}
	return res.GameAccounts, nil
}

// fleetCheckGameAccounts errors before any serve starts when a member names no game account and its account holds
// several: the serve would join the first listed and then fail on the character (#236 point 3, the live run's exit 5
// after a login that was spent for nothing). The count is the login service's, so a member without game_account costs
// a login, taken through the fleet's shared budget; a member that names one is not probed. The password is read the
// way the child reads it and never printed.
func fleetCheckGameAccounts(ctx context.Context, ext live.ServerExt, members []fleetForeverMember, budget *loginBudget,
	now func() time.Time, stderr io.Writer) error {
	for i, m := range members {
		if m.GameAccount != 0 {
			continue
		}
		password, err := servePassword(m.PasswordFile)
		if err != nil {
			return fmt.Errorf("member %d (%s): %w", i+1, m.Character, err)
		}
		// Each login attempt waits for its turn in the fleet's budget: an unpinned build logs in once per build it
		// tries, and realmd's per-IP limit counts each one.
		before := func(ctx context.Context) error {
			if budget == nil {
				return nil
			}
			at, _ := bookFleetLogin(budget, now())
			if wait := at.Sub(now()); wait > 0 {
				fmt.Fprintf(stderr, "fleet: waiting %s to check %s's game accounts\n", wait.Round(time.Second), m.Character)
				timer := time.NewTimer(wait)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		fmt.Fprintf(stderr, "fleet: checking %s's game accounts: no game_account is named\n", m.Character)
		n, err := fleetGameAccounts(ctx, ext, m, password, before)
		if err != nil {
			return fmt.Errorf("member %d (%s): the login: %w", i+1, m.Character, err)
		}
		if n > 1 {
			return fmt.Errorf("member %d (%s): the account has %d game accounts and none is named: name game_account in the fleet file (the world joins the first listed)",
				i+1, m.Character, n)
		}
	}
	return nil
}

// fleetForeverCmd is `fleet --forever FILE`: it loads the slots file's
// `forever` block (the serve's connection flags) and the fleet file, starts
// one `forever serve` per member, and supervises them through runFleet.
func fleetForeverCmd(ctx context.Context, fleetPath, slotsPath, server string, o fleetOpts, stdout, stderr io.Writer) int {
	if slotsPath == "" {
		slotsPath = slots.DefaultPath()
	}
	sf, err := live.LoadSlotsFile(slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "fleet: %v\n", err)
		return exitUsage
	}
	members, err := loadFleetForever(fleetPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if server == "" {
		// One server only: the fleet's serves all log in to the same
		// server, so its `forever` block is unambiguous.
		for name := range sf.Forever {
			if server != "" {
				fmt.Fprintln(stderr, "fleet: --server is required: the slots file names more than one `forever` block")
				return exitUsage
			}
			server = name
		}
	}
	ext, ok := sf.Forever[server]
	if !ok || ext.Rest == "" || ext.RPC == "" || ext.CA == "" {
		fmt.Fprintf(stderr, "fleet: server %q has no complete `forever` block (rest, rpc, ca) in %s\n", server, slotsPath)
		return exitUsage
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "fleet: %v\n", err)
		return exitUsage
	}
	// One fleet per account at a time, held for this fleet's life and taken
	// before the check, so a second fleet started at once is refused.
	unlock, err := fleetForeverLockAccounts(slotsPath, server, members)
	if err != nil {
		fmt.Fprintf(stderr, "fleet: %v\n", err)
		return exitSlotRefused
	}
	defer unlock()
	// No member logs in before every member is cleared against the slots
	// file: the account's lease, the character allowlist and the dir's
	// manifest.
	if err := fleetForeverCheck(cfg, slots.NewStore(cfg, slotsPath), server, members); err != nil {
		fmt.Fprintf(stderr, "fleet: %v\n", err)
		return exitSlotRefused
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "fleet:", err)
		return exitUsage
	}
	// Pdeathsig (see detachProcessGroup) fires when the thread that started
	// a child exits, so the supervisor stays on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// The same pacing budget as the old fleet, one login per 5 s, but local
	// to this run: serve logins share realmd's per-IP limit but no budget
	// port of their own.
	budget := newLoginBudget(1, fleetForeverPace)
	if o.budget != nil {
		budget = o.budget
	}
	o.budget = budget
	if o.stagger <= 0 {
		o.stagger = fleetForeverPace
	}
	// Before a single serve starts: a member that names no game account on an account with several fails at the world,
	// after spending a login, and the fleet only sees exit 5 (#236 point 3).
	if err := fleetCheckGameAccounts(ctx, ext, members, budget, o.clock, stderr); err != nil {
		if ctx.Err() != nil {
			return exitFleet
		}
		fmt.Fprintf(stderr, "fleet: %v\n", err)
		return exitUsage
	}
	start := func(m fleetForeverMember, w io.Writer) (fleetChild, error) {
		return startProcess(exe, foreverChildArgs(ext, m), nil, w)
	}
	return runFleetForever(ctx, members, fleetForeverDeps{
		start: start,
		status: func(ctx context.Context, dir string) (string, error) {
			return serveStatusState(ctx, dir)
		},
	}, o, stdout, stderr)
}

// fleetForeverDeps adapts runFleet's child interface to serve members: start
// runs `forever serve`, status reads the serve's status state through its
// slot dir.
type fleetForeverDeps struct {
	start func(m fleetForeverMember, stderr io.Writer) (fleetChild, error)
	// status returns the serve status state ("online", ...) of the serve
	// whose slot dir is dir.
	status func(ctx context.Context, dir string) (string, error)
}

// runFleetForever starts every member's serve, waits until all are online,
// prints the table, then supervises until ctx ends or every child has
// exited. It reuses runFleet with proxy members: the supervisor polls the
// serve's listen address and the status adapter reads the serve's dir.
func runFleetForever(ctx context.Context, members []fleetForeverMember, deps fleetForeverDeps, o fleetOpts, stdout, stderr io.Writer) int {
	proxy := make([]fleetMember, len(members))
	byRPC := make(map[string]fleetForeverMember, len(members))
	byChar := make(map[string]fleetForeverMember, len(members))
	for i, m := range members {
		proxy[i] = fleetMember{Account: m.Account, Character: m.Character, RPC: m.Listen}
		byRPC[m.Listen] = m
		byChar[strings.ToLower(m.Character)] = m
	}
	if o.budget == nil {
		o.budget = newLoginBudget(1, fleetForeverPace)
	}
	o.ready = fleetReady
	return runFleet(ctx, proxy, fleetDeps{
		start: func(p fleetMember, w io.Writer) (fleetChild, error) {
			m, ok := byChar[strings.ToLower(p.Character)]
			if !ok {
				return nil, fmt.Errorf("fleet: no serve member %q", p.Character)
			}
			return deps.start(m, w)
		},
		status: func(ctx context.Context, addr string) (string, error) {
			m, ok := byRPC[addr]
			if !ok {
				return "", fmt.Errorf("fleet: no serve member on %s", addr)
			}
			return deps.status(ctx, m.Dir)
		},
	}, o, stdout, stderr)
}

// serveStatusState calls the serve's status method in dir and returns its
// state ("online", "reconnecting", "offline"). The token comes from the
// derived token file (openSlotClient, as the rpc command does) and is never
// printed.
func serveStatusState(ctx context.Context, dir string) (string, error) {
	c, err := openSlotClient(dir)
	if err != nil {
		return "", err
	}
	var st struct {
		State string `json:"state"`
	}
	if err := c.Call(ctx, "status", nil, &st, 2*time.Second); err != nil {
		return "", err
	}
	return st.State, nil
}

// fleetForeverCheck clears every member against the slots file before any
// login, so a member can never log in on an account another live session
// holds. For each member:
//   - its account must be a slot account of the server, and its character
//     one that slot allows;
//   - no slot of that account may hold a live lease unless it is this
//     member's own: the member's dir carries a slot.json from slot setup
//     (server, slot, holder and lease id) that matches the live lease, and
//     the manifest's port is the member's listen port;
//   - a manifest that names a lease the slots file no longer holds (taken
//     over, released, stale) is refused.
//
// A dir with no manifest, or one written by a bare serve (no lease id), is
// fine when no slot of the account is leased: nobody else holds it.
func fleetForeverCheck(cfg *slots.Config, store *slots.Store, serverName string, members []fleetForeverMember) error {
	var srv *slots.Server
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == serverName {
			srv = &cfg.Servers[i]
		}
	}
	if srv == nil {
		return fmt.Errorf("server %q is not in the slots file", serverName)
	}
	var errs []error
	for i, m := range members {
		if err := fleetForeverCheckMember(cfg, store, srv, m); err != nil {
			errs = append(errs, fmt.Errorf("member %d (%s): %w", i+1, m.Character, err))
		}
	}
	return errors.Join(errs...)
}

func fleetForeverCheckMember(cfg *slots.Config, store *slots.Store, srv *slots.Server, m fleetForeverMember) error {
	var man *teardownSlotManifest
	if raw, err := privfile.Read(filepath.Join(m.Dir, "slot.json"), 64*1024); err == nil {
		var t teardownSlotManifest
		if json.Unmarshal(raw, &t) != nil {
			return fmt.Errorf("%s/slot.json is not a slot manifest", m.Dir)
		}
		if t.LeaseID != "" {
			man = &t
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s/slot.json is not readable: %v", m.Dir, err)
	}
	var own, mineSlot *slots.Slot // the slot this member runs on; the one its manifest's lease holds
	for i := range srv.Slots {
		_, slot, err := cfg.Find(srv.Name, srv.Slots[i].ID)
		if err != nil || slot.Reserved != "" || !strings.EqualFold(slot.Account, m.Account) {
			continue
		}
		cur, err := store.Get(srv.Name, slot.ID)
		if err != nil {
			return err
		}
		live := cur != nil && !store.IsStale(cur)
		mine := man != nil && man.Server == srv.Name && man.Slot == slot.ID && live &&
			man.Holder == cur.Holder && man.LeaseID == cur.LeaseID
		if live && !mine {
			return fmt.Errorf("account %s is held on %s/%s by %s: refusing to log in on it", m.Account, srv.Name, slot.ID, cur.Holder)
		}
		if mine {
			mineSlot = slot
		}
		if own == nil {
			own = slot
		}
	}
	if own == nil {
		return fmt.Errorf("account %s is not a slot account of %s", m.Account, srv.Name)
	}
	if man != nil {
		if mineSlot == nil {
			return fmt.Errorf("the lease in %s/slot.json (%s/%s) is no longer held: run slot setup again", m.Dir, man.Server, man.Slot)
		}
		own = mineSlot
	}
	if !slotSetupAllows(own, m.Character) {
		return fmt.Errorf("%s/%s allows %s", srv.Name, own.ID, slotSetupAllowed(own))
	}
	if man != nil {
		_, port, err := netutil.CheckLoopbackAddr(m.Listen)
		if err != nil || man.Port != port {
			return fmt.Errorf("%s/slot.json is for port %d, the member listens on %s", m.Dir, man.Port, m.Listen)
		}
		if !strings.EqualFold(man.Account, m.Account) || !strings.EqualFold(man.Character, m.Character) {
			return fmt.Errorf("%s/slot.json is for %s/%s, not this member", m.Dir, man.Account, man.Character)
		}
	}
	return nil
}
