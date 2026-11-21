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

	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
)

// mapPairs is --parent MAP=PARENT, repeatable.
type mapPairs map[uint32]uint32

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
			if n := e.Name(); n == "agent.pid" || (strings.HasPrefix(n, "rpc-") && strings.HasSuffix(n, ".token")) {
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
	m := teardownSlotManifest{
		Server: "forever", Slot: "1", Holder: "forever",
		Account: account, Character: character, Port: port, Dir: dir,
		Binary: bin, Settings: filepath.Join(dir, "settings.json"),
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: tokenFile,
		NoAuth:    noAuth,
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	if keep {
		// The setup manifest stays, but a client tells a no-token daemon from a lost token file by no_auth.
		_, kept, err := slotdir.Read(dir)
		if err != nil {
			return err
		}
		if kept.NoAuth != noAuth {
			kept.NoAuth = noAuth
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
	const usage = "usage: forever-agent forever serve [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--password-file FILE] [--listen HOST:PORT] [--dir DIR] [--token-file FILE] [--no-auth] [--navmesh DIR] [--parent MAP=PARENT] [--disconnect-grace DURATION] [--allow-gm-units] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	passwordFile := fs.String("password-file", "", "file holding the account password (default: FOREVER_PASSWORD)")
	listen := fs.String("listen", "127.0.0.1:8765", "loopback address the RPC socket listens on")
	dir := fs.String("dir", "", "slot-style dir: slot.json, the pid file and the token file are written there so the rpc client works")
	tokenFile := fs.String("token-file", "", "RPC bearer token file (default: rpc-<port>.token in --dir or the working directory)")
	noAuth := fs.Bool("no-auth", false, "serve without a bearer token (loopback and Host checks only)")
	navmesh := fs.String("navmesh", "", "mmaps folder that enables path_to and walk_path")
	parents := mapPairs{}
	fs.Var(parents, "parent", "MAP=PARENT: an instance map whose missing tile comes from that parent (repeatable)")
	grace := fs.Duration("disconnect-grace", 30*time.Second, "how long the RPC stays up once the reconnect budget is spent (the daemon is offline)")
	allowGMUnits := fs.Bool("allow-gm-units", false, "test bench: allow gm_kill, gm_set_hp and gm_aura on the guids trial_units registered")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path, from the first login on (one cap for the whole daemon; keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever serve"); err != nil {
		fmt.Fprintln(stderr, err)
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
	password, err := servePassword(*passwordFile)
	if err != nil {
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitUsage
	}
	if password == "" {
		fmt.Fprintln(stderr, "forever serve: no password (set FOREVER_PASSWORD or pass --password-file)")
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

	result, err := common.loginWithPassword(ctx, "forever serve", stdout, password, common.worldAccept(stdout))
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitAuth
	}
	opts := common.withLogin(result, stdout, world.Options{
		Serve: &world.ServeConfig{
			Listener:        ln,
			Token:           token,
			NavmeshDir:      *navmesh,
			Parents:         parents,
			DisconnectGrace: *grace,
			AllowGMUnits:    *allowGMUnits,
			CaptureDir:      *captureDir,
		},
	})
	fmt.Fprintf(stdout, "rpc: listening on http://%s/rpc\n", ln.Addr())
	if token != "" {
		fmt.Fprintf(stdout, "rpc: token file %s\n", *tokenFile)
	} else {
		fmt.Fprintln(stdout, "rpc: authentication off")
	}
	report, err := world.Enter(ctx, opts)
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
