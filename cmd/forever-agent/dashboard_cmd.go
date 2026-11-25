package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/forever-agent/internal/dashboard"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/netutil"
	"github.com/pjerra/forever-agent/internal/routeedit"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const dashboardUsage = "usage: forever-agent dashboard [--listen 127.0.0.1:8790] [--allow-host HOST[,HOST]] [--agents auto|<rpc,...>] [--tests-dir DIR] [--traces-dir DIR] [--plans-dir DIR[,DIR]] [--minimap-dir DIR] [--records-dir DIR] [--embed-from ORIGIN[,ORIGIN]] [--config-dir DIR] [--mmaps-dir DIR [--mmaps-format turtle|forever] [--parent MAP=PARENT[,MAP=PARENT]]]\n" +
	"                          [--edit [--edit-dir DIR] [--edit-source DIR] [--edit-tokens FILE] [--edit-allow-default-dir FILE[,FILE]] [--edit-trust-local [--edit-trust-label LABEL]]]\n" +
	"                          [--control [--control-tokens FILE] [--control-trust-local [--control-trust-label LABEL]] [--control-log FILE]]\n" +
	"  serves a read-only live page of the running agents (README \"Live dashboard\"); with --edit or --control the page can also write.\n" +
	"  --edit turns on the route editor (README \"Route editor\"): the only thing the page can then write is a checked version of a plan's route\n" +
	"  --control turns on the run controls (README \"Controls (--control)\"): the page can pause, resume, skip or stop a run"

// expandHome turns a leading ~/ into the home folder.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// dashboardCmd serves the live dashboard until ctx ends.
func dashboardCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:8790", "address to serve the page on (loopback unless you say otherwise)")
	allowHosts := fs.String("allow-host", "", "host names or IP addresses, comma separated, the page accepts in its Host header besides loopback (the name you open it by, such as a Tailscale MagicDNS name); the --listen IP is always accepted")
	agents := fs.String("agents", "auto", `agents to watch: "auto" (the rpc field of the settings files) or a comma list of RPC ports or addresses`)
	tests := fs.String("tests-dir", filepath.Join(home, "forever-agent", "tests"), "the only folder whose files the page serves (run traces and reports)")
	traces := fs.String("traces-dir", filepath.Join(home, "forever-agent", "traces"), "where the agents write run traces (read for orders and the binary SHA, never served)")
	plans := fs.String("plans-dir", filepath.Join(home, "forever-agent", "dungeons"), "folder(s) with the dungeon plan files, comma separated")
	records := fs.String("records-dir", filepath.Join(home, "forever-agent", "tests"), "folder of run record folders the 3D view replays (one folder per run, with its trace .jsonl); every read stays inside it")
	minimaps := fs.String("minimap-dir", filepath.Join(home, "forever-agent-minimaps"), "folder with the map images `forever-agent minimap-extract` wrote (<map>.png, <map>.json); the run map draws over the image of its map id when there is one")
	embed := fs.String("embed-from", "", "extra origins (http://host:port, comma separated) that may show this page in a frame; loopback pages always may")
	edit := fs.Bool("edit", false, "turn on the route editor: POST /api/edit/<plan> with the editing token writes a navmesh-checked version of a plan's route into --edit-dir (off: the page is GET/HEAD only)")
	editDir := fs.String("edit-dir", filepath.Join(home, "forever-agent-route-edits"), "with --edit: where the versions are written (<plan>/vNNNN.json, .meta.json, .diff); must not be, contain or lie inside any slot's dungeons_dir")
	editSource := fs.String("edit-source", "", "with --edit: the folder whose plan files an edit starts from (default: the first --plans-dir); must not lie inside any slot's dungeons_dir")
	editTokens := fs.String("edit-tokens", filepath.Join(home, ".config", "forever-agent", "route-edit-tokens"), "with --edit: file of `label token` lines, mode 0600; the label is recorded as who made a version")
	editAllow := fs.String("edit-allow-default-dir", "", "with --edit: settings files (by file name, comma separated) that have an rpc but no dungeons_dir, whose default plans folder (dungeons/ under the agent's working folder) you vouch is not the edits folder; any other such file stops the editor")
	trustLocal := fs.Bool("edit-trust-local", false, "with --edit: a save from this machine (loopback) or from a Tailscale address in --allow-host / the --listen address needs no token typed: the server uses its own token for --edit-trust-label (the token never reaches the page); any other caller still needs the token. Off by default; read README \"Route editor\" first")
	trustLabel := fs.String("edit-trust-label", "owner", "with --edit-trust-local: the label in the --edit-tokens file whose token the trusted saves are made as")
	control := fs.Bool("control", false, "turn on the run controls (README \"Controls (--control)\"): POST /api/control/<agent id>/run with a control token pauses, resumes, skips or stops that agent's run (off: the page is GET/HEAD only)")
	controlTokens := fs.String("control-tokens", filepath.Join(home, ".config", "forever-agent", "dashboard-control-tokens"), "with --control: file of `label token` lines, mode 0600; its own file, never the route-editor one (an editing token must not press run buttons); the label is the who in the control log")
	controlTrustLocal := fs.Bool("control-trust-local", false, "with --control: a control from this machine (loopback) or from a Tailscale address in --allow-host / the --listen address needs no token typed: the server uses its own token for --control-trust-label (the token never reaches the page); off by default")
	controlTrustLabel := fs.String("control-trust-label", "owner", "with --control-trust-local: the label in the --control-tokens file whose token the trusted controls are made as")
	controlLog := fs.String("control-log", filepath.Join(home, ".local", "state", "forever-agent", "dashboard-control.jsonl"), "with --control: every control appends who sent it and what the agent answered (a request and a result line each, no token); the folder is made 0700, the file 0600")
	editMmaps := fs.String("mmaps-dir", "", "the navmesh folder of the server the plans are run on (a check on another server's mesh proves nothing): the plan view draws the real route on it (GET /api/plan/<name>/route); required with --edit")
	mmapsFormat := fs.String("mmaps-format", "turtle", `the mmap generation of --mmaps-dir: "turtle" (the 1.12 ancestor's MaNGOS v6 files) or "forever" (ForeverCore's v16 files); never guessed from the folder, so the wrong one stops the dashboard at start`)
	parent := fs.String("parent", "", "with --mmaps-format forever: the server's instance parent rule, MAP=PARENT pairs comma separated (for example 389=0), so an instance with no tile of its own uses the parent map's")
	cfgDir := fs.String("config-dir", dashboard.DefaultConfigDir(), "folder with settings*.json and the rpc-<port>.token files")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, dashboardUsage)
		return exitUsage
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard: --listen:", err)
		return exitUsage
	}
	format, err := dashNavFormat(*mmapsFormat)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard: --mmaps-format:", err)
		return exitUsage
	}
	parents, err := parseParentPairs(*parent)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard: --parent:", err)
		return exitUsage
	}
	if len(parents) > 0 && format != navmesh.Forever {
		fmt.Fprintln(stderr, "dashboard: --parent needs --mmaps-format forever: the instance parent rule is a Forever server's")
		return exitUsage
	}
	if !netutil.IsLoopback(host) {
		fmt.Fprintf(stderr, "dashboard: warning: %s is not loopback; anyone who can reach it sees the runs and the files under --tests-dir\n", *listen)
	}
	if *controlTrustLocal && !*control {
		fmt.Fprintln(stderr, "dashboard: --control-trust-local needs --control")
		return exitUsage
	}
	if *control && !dashboard.IsTailnetListen(host) {
		fmt.Fprintf(stderr, "dashboard: --control refuses the listen host %q: a control token must not cross a LAN in plain HTTP (listen on loopback or a literal Tailscale address)\n", host)
		return exitUsage
	}
	allow, err := dashboard.AllowedHosts(host, *allowHosts)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard: --allow-host:", err)
		return exitUsage
	}
	if _, err := dashboard.DiscoverAgents(expandHome(*cfgDir), *agents); err != nil {
		fmt.Fprintln(stderr, "dashboard: --agents:", err)
		return exitUsage
	}
	var embedFrom []string
	for _, o := range strings.Split(*embed, ",") {
		if o = strings.TrimSpace(o); o != "" {
			if err := dashboard.CheckEmbedOrigin(o); err != nil {
				fmt.Fprintln(stderr, "dashboard: --embed-from:", err)
				return exitUsage
			}
			embedFrom = append(embedFrom, o)
		}
	}
	var planDirs []string
	for _, d := range strings.Split(*plans, ",") {
		if d = strings.TrimSpace(d); d != "" {
			planDirs = append(planDirs, expandHome(d))
		}
	}
	testsDir := expandHome(*tests)
	if st, err := os.Stat(testsDir); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "dashboard: --tests-dir %s is not a folder: no files will be linked\n", testsDir)
		testsDir = ""
	}
	// A Forever slot's live map (map_snapshot, drawn by the daemon) is polled by the poller itself: every 5 s, never
	// under 2 s, under a 10 s bound (internal/dashboard/live_map.go), so no flag is needed here.
	p := dashboard.NewPoller(dashboard.Options{ConfigDir: expandHome(*cfgDir), Agents: *agents, TestsDir: testsDir, TracesDir: expandHome(*traces), PlansDirs: planDirs, MinimapDir: minimapDir(expandHome(*minimaps), stderr), RecordsDir: expandHome(*records)})
	var editor *dashboard.Editor
	if *editMmaps != "" && !*edit { // the real route needs the navmesh, not the editor
		mm := expandHome(*editMmaps)
		planner, err := openDashNav(mm, format, parents)
		if err != nil {
			fmt.Fprintln(stderr, "dashboard:", err)
			return exitUsage
		}
		defer planner.Close()
		p.SetNavmesh(dashNav(planner, format), mm)
		scenePlanner, err := openDashNav(mm, format, parents)
		if err != nil {
			fmt.Fprintln(stderr, "dashboard:", err)
			return exitUsage
		}
		defer scenePlanner.Close()
		p.SetSceneNavmesh(scenePlanner, dashNavFormatName(format))
		fmt.Fprintf(stderr, "dashboard: the plan view draws the real route on the %s navmesh in %s\n", dashNavFormatName(format), mm)
	}
	if *trustLocal && !*edit {
		fmt.Fprintln(stderr, "dashboard: --edit-trust-local needs --edit")
		return exitUsage
	}
	if *edit {
		src := expandHome(*editSource)
		if src == "" && len(planDirs) > 0 {
			src = planDirs[0]
		}
		ed, planner, err := newEditor(p, src, expandHome(*editDir), expandHome(*editTokens), expandHome(*editMmaps), splitList(*editAllow), format, parents)
		if err != nil {
			fmt.Fprintln(stderr, "dashboard: --edit:", err)
			return exitUsage
		}
		defer planner.Close()
		scenePlanner, err := openDashNav(expandHome(*editMmaps), format, parents)
		if err != nil {
			fmt.Fprintln(stderr, "dashboard:", err)
			return exitUsage
		}
		defer scenePlanner.Close()
		p.SetSceneNavmesh(scenePlanner, dashNavFormatName(format))
		editor = ed
		if *trustLocal {
			peers, ignored := dashboard.TrustPeers(allow)
			if err := ed.EnableTrustLocal(*trustLabel, peers); err != nil {
				fmt.Fprintln(stderr, "dashboard: --edit-trust-local:", err)
				return exitUsage
			}
			fmt.Fprintf(stderr, "dashboard: --edit-trust-local: saves from loopback and from %v need no token (made as %q)\n", peers, *trustLabel)
			if len(ignored) > 0 {
				fmt.Fprintf(stderr, "dashboard: --edit-trust-local: not trusted as callers (only literal Tailscale addresses are): %s\n", strings.Join(ignored, ", "))
			}
		}
		fmt.Fprintf(stderr, "dashboard: route editing is ON: edits start from %s, versions go to %s, checked on the mesh in %s\n", src, expandHome(*editDir), expandHome(*editMmaps))
	}
	var ctl *dashboard.Controller
	if *control {
		tk, err := routeedit.LoadTokens(expandHome(*controlTokens))
		if err != nil {
			fmt.Fprintln(stderr, "dashboard: --control:", err)
			return exitUsage
		}
		if err := checkControlTokens(expandHome(*controlTokens), tk, expandHome(*editTokens)); err != nil {
			fmt.Fprintln(stderr, "dashboard: --control:", err)
			return exitUsage
		}
		c, err := dashboard.NewController(p, dashboard.ControlConfig{Tokens: tk, LogPath: expandHome(*controlLog)})
		if err != nil {
			fmt.Fprintln(stderr, "dashboard: --control:", err)
			return exitUsage
		}
		ctl = c
		if *controlTrustLocal {
			peers, ignored := dashboard.TrustPeers(allow)
			if err := ctl.EnableTrustLocal(*controlTrustLabel, peers); err != nil {
				fmt.Fprintln(stderr, "dashboard: --control-trust-local:", err)
				return exitUsage
			}
			fmt.Fprintf(stderr, "dashboard: --control-trust-local: controls from loopback and from %v need no token (made as %q)\n", peers, *controlTrustLabel)
			if len(ignored) > 0 {
				fmt.Fprintf(stderr, "dashboard: --control-trust-local: not trusted as callers (only literal Tailscale addresses are): %s\n", strings.Join(ignored, ", "))
			}
		}
		fmt.Fprintf(stderr, "dashboard: controls ON: every control is logged (who, and what the agent answered) to %s\n", expandHome(*controlLog))
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard:", err)
		return exitUsage
	}
	pctx, stop := context.WithCancel(ctx)
	defer stop()
	go p.Run(pctx)
	mode := "read-only"
	switch {
	case editor != nil && ctl != nil:
		mode = "route editing and controls ON"
	case editor != nil:
		mode = "route editing ON"
	case ctl != nil:
		mode = "controls ON"
	}
	fmt.Fprintf(stdout, "forever-agent dashboard %s on http://%s/ (%s; agents: %s)\n", version, ln.Addr(), mode, *agents)
	err = rpc.Serve(ctx, ln, dashboard.HandlerFull(p, testsDir, allow, embedFrom, editor, ctl))
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "dashboard:", err)
		return 1
	}
	return exitOK
}

// checkControlTokens refuses a control token file that is the route editor's file (the same file by any path) or that holds a token the
// editor's file holds too: an editing token must never press run buttons. The editor's file may be absent (nothing can be shared); one that
// cannot be read is the editor's own problem (--edit refuses it), and holds nothing this can compare.
func checkControlTokens(controlPath string, control *routeedit.Tokens, editPath string) error {
	if cs, err := os.Stat(controlPath); err == nil {
		if es, err := os.Stat(editPath); err == nil && os.SameFile(cs, es) {
			return fmt.Errorf("--control-tokens %s is the route editor's --edit-tokens file: the controls need a file of their own", controlPath)
		}
	}
	edit, err := routeedit.LoadTokens(editPath)
	if err != nil {
		return nil
	}
	if label, ok := control.SharesToken(edit); ok {
		return fmt.Errorf("--control-tokens %s holds the token of %q, which --edit-tokens %s also holds: an editing token must not press run buttons", controlPath, label, editPath)
	}
	return nil
}

// newEditor sets up the route editor: the token file, the versions folder, the navmesh the plans are checked on, and the check that the folders
// stay clear of every slot's plan folder. The planner is the caller's to close.
func newEditor(p *dashboard.Poller, source, editDir, tokens, mmaps string, allowDefault []string, format navmesh.Format, parents map[uint32]uint32) (*dashboard.Editor, *navmesh.Planner, error) {
	if mmaps == "" {
		return nil, nil, errors.New("--mmaps-dir is needed: the navmesh folder of the server the plans are run on")
	}
	if source == "" {
		return nil, nil, errors.New("--edit-source (or a --plans-dir) is needed")
	}
	tk, err := routeedit.LoadTokens(tokens)
	if err != nil {
		return nil, nil, err
	}
	store, err := routeedit.NewStore(editDir)
	if err != nil {
		return nil, nil, err
	}
	planner, err := openDashNav(mmaps, format, parents)
	if err != nil {
		return nil, nil, err
	}
	ed, err := dashboard.NewEditor(p, dashboard.EditConfig{Source: source, Store: store, Tokens: tk, Nav: dashNav(planner, format), Mmaps: mmaps, AllowDefaultDir: allowDefault})
	if err != nil {
		planner.Close()
		return nil, nil, err
	}
	return ed, planner, nil
}

// openDashNav opens the navmesh the plan view draws and the editor checks plans on. The generation is the one named, never guessed: the folder
// must hold at least one tile file that generation's own parser accepts, so a wrong --mmaps-format fails at start instead of leaving every plan
// point off the mesh. parents is the server's instance parent rule for the Forever format (navmesh.NewForeverPlanner).
func openDashNav(dir string, format navmesh.Format, parents map[uint32]uint32) (*navmesh.Planner, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("--mmaps-dir %s is not a folder", dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("--mmaps-dir %s: %w", dir, err)
	}
	for _, e := range ents {
		if _, _, _, ok := format.TileParse(e.Name()); ok {
			return navmesh.NewPlannerFormat(dir, format, parents), nil
		}
	}
	return nil, fmt.Errorf("--mmaps-dir %s has no %s tiles", dir, dashNavFormatName(format))
}

// dashNavFormat reads the --mmaps-format value.
func dashNavFormat(s string) (navmesh.Format, error) {
	switch strings.TrimSpace(s) {
	case "turtle":
		return navmesh.MaNGOS, nil
	case "forever":
		return navmesh.Forever, nil
	}
	return 0, fmt.Errorf("%q: want turtle or forever", s)
}

// dashNavFormatName is an mmap generation as the flag and the messages name it.
func dashNavFormatName(f navmesh.Format) string {
	if f == navmesh.Forever {
		return "Forever"
	}
	return "Turtle"
}

// dashNav is the planner as the plan view and the editor take it: the Turtle planner itself, and a Forever one behind dashForeverNav. A
// v16 tile set cannot replay the 1.12 core's own path query, so the wrapper offers no ServerPath and the plan check plans every hop with
// the v16 walk instead of refusing the plan: the same rule plan-check --forever applies with its foreverNav.
func dashNav(p *navmesh.Planner, format navmesh.Format) dashboard.Nav {
	if format == navmesh.Forever {
		return dashForeverNav{p}
	}
	return p
}

// dashForeverNav is a v16 planner as the dashboard's checks take it (dungeon.Navmesh, the routeedit floor snaps and the planner's own
// generation), without ServerQuerier.
type dashForeverNav struct{ p *navmesh.Planner }

func (n dashForeverNav) Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return n.p.Plan(mapID, from, to)
}
func (n dashForeverNav) Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	return n.p.Raycast(mapID, from, to)
}
func (n dashForeverNav) Nearest(mapID uint32, pt navmesh.Vec3) (navmesh.Vec3, bool, error) {
	return n.p.Nearest(mapID, pt)
}
func (n dashForeverNav) Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	return n.p.Floors(mapID, x, y, lo, hi)
}
func (n dashForeverNav) SetAvoid(mapID uint32, boxes []navmesh.Box) { n.p.SetAvoid(mapID, boxes) }
func (n dashForeverNav) Format() navmesh.Format                     { return n.p.Format() }

// splitList splits a comma list, leaving out empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// minimapDir is dir when it is a folder, else "" (the page then draws no background).
func minimapDir(dir string, stderr io.Writer) string {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "dashboard: --minimap-dir %s is not a folder: the map has no background image (run forever-agent minimap-extract)\n", dir)
		return ""
	}
	return dir
}
