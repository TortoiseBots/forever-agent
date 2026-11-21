// Command teardown cleans up a slot-setup client dir: game-side teardown
// over RPC, the agent process, stray pids, secrets and the lease.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
)

// exitTeardownFailed is the exit code of a teardown with a failed step (14).
const exitTeardownFailed = 14

const teardownUsage = "usage: forever-agent teardown [--keep-lease] [--release-anyway] [--gm off|on|keep] [--dry-run] [--json] [--slots FILE] <dir>"

// teardownProbe is the slots.Probe the teardown CLI uses; tests replace it.
var teardownProbe slots.Probe = slots.LinuxProbe{}

// teardownCLISleep waits between liveness polls; tests replace it.
var teardownCLISleep = time.Sleep

type teardownCLIStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// teardownSlotManifest is the shared slot.json shape (internal/slotdir).
type teardownSlotManifest = slotdir.Manifest

// teardownCheckManifest treats slot.json paths as assertions: manifest.Dir
// must be the canonical teardown dir, and the settings/pid/binary/token
// paths must be the ones derived inside it (never symlinks, never outside).
// A tampered slot.json refuses the teardown before anything is deleted.
func teardownCheckManifest(dir string, manifest *teardownSlotManifest) error {
	return slotdir.Check("teardown", dir, manifest)
}

func teardownCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("teardown", stderr)
	}
	o, code := parseTeardownArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// Aliases so the body below reads as before.
	keepLease, releaseAnyway, gm := &o.keepLease, &o.releaseAnyway, &o.gm
	dryRun, asJSON, slotsPath := &o.dryRun, &o.asJSON, &o.slotsPath
	dir := o.dir
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "teardown: %v\n", err)
		return exitUsage
	}
	dir = absDir
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		fmt.Fprintf(stderr, "teardown: %s/slot.json missing or unreadable: teardown works on a dir made by slot setup\n", dir)
		return exitUsage
	}
	var manifest teardownSlotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil ||
		manifest.Server == "" || manifest.Slot == "" || manifest.Holder == "" || manifest.Port == 0 {
		fmt.Fprintf(stderr, "teardown: %s/slot.json missing or unreadable: teardown works on a dir made by slot setup\n", dir)
		return exitUsage
	}
	if err := teardownCheckManifest(dir, &manifest); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	var slotsPathErr error
	if *slotsPath == "" {
		// slot.json's slots_path is a claim about a file outside the dir: take it only when it is an absolute
		// regular file (never a symlink or a device), so a tampered manifest cannot make the lease step create
		// a leases dir and lock beside something else.
		*slotsPath = manifest.SlotsPath
		if *slotsPath != "" {
			if !filepath.IsAbs(*slotsPath) {
				slotsPathErr = fmt.Errorf("slot.json names a relative slots_path %q: pass --slots", *slotsPath)
			} else if fi, err := os.Lstat(*slotsPath); err != nil {
				slotsPathErr = fmt.Errorf("slot.json names %s, which is not readable: %v", *slotsPath, err)
			} else if !fi.Mode().IsRegular() {
				slotsPathErr = fmt.Errorf("slot.json names %s, which is not a regular file", *slotsPath)
			}
		}
	}

	steps := []teardownCLIStep{}
	failed := 0
	add := func(step, status, detail string) {
		steps = append(steps, teardownCLIStep{Step: step, Status: status, Detail: detail})
		if status == "fail" || status == "ending" { // ending: the agent's player follow loop did not exit in time
			failed++
		}
	}

	// 2. Game-side teardown over RPC.
	pid, alive := teardownProbe.AgentPID(dir)
	pidFilePresent := teardownPIDFilePresent(dir)
	gameFailed := false
	reused := false
	if alive {
		rpcSteps, rerr := teardownCallAgent(dir, manifest.Port, manifest.TokenFile, manifest.NoAuth, *dryRun, *gm)
		if rerr != nil {
			add("rpc", "fail", rerr.Error())
			gameFailed = true
		} else {
			for _, s := range rpcSteps {
				add(s.Step, s.Status, s.Detail)
				if s.Status == "fail" || s.Status == "ending" {
					gameFailed = true
				}
			}
		}
	} else if !pidFilePresent {
		add("rpc", "skip", "no agent was started from this dir (or it logged out and removed agent.pid)")
	} else if teardownPIDReused(dir, pid) {
		// The pid in agent.pid is running, but it is not this dir's agent. Signalling it could kill an unrelated
		// process, and deleting the pid/token files would hide it: report and leave everything alone.
		add("rpc", "fail", fmt.Sprintf("agent pid %d is another process (pid reused): not signalled, files kept", pid))
		gameFailed, reused = true, true
	} else {
		add("rpc", "fail", fmt.Sprintf("agent pid %d is gone without a clean exit: game-side steps not run (its hires end after the server's offline grace)", pid))
		gameFailed = true
	}

	if *dryRun {
		// 3-7 become plan lines; nothing is changed.
		teardownPlanSteps(dir, manifest, pid, alive, reused, *keepLease, add)
		return teardownReport(manifest, steps, failed, *asJSON, stdout)
	}

	// 3. agent_exit (only when it was alive).
	if alive {
		step, status, detail := teardownAgentExit(dir, pid)
		add(step, status, detail)
	}

	// 4. port.
	if teardownProbe.PortListening(manifest.Port) {
		add("port", "fail", fmt.Sprintf("port %d still has a listener (not this dir's agent: left alone)", manifest.Port))
	} else {
		add("port", "ok", fmt.Sprintf("port %d closed", manifest.Port))
	}

	// 5. other_pids.
	for _, s := range teardownOtherPIDs(dir) {
		add(s.Step, s.Status, s.Detail)
	}

	// 6. files: only the derived in-dir paths, never the manifest's strings.
	if reused {
		add("files", "skip", fmt.Sprintf("kept: agent pid %d is another process, not this dir's agent", pid))
	} else {
		step, status, detail := teardownDeleteFiles(dir, manifest.Port)
		add(step, status, detail)
	}

	// 7. lease.
	if !teardownSlotHoldsALease(manifest) {
		// A bare `forever serve --dir` writes its own slot.json: no lease and
		// no slots file are behind it, so there is nothing to release.
		add("lease", "skip", "a serve slot holds no lease (forever serve --dir)")
	} else if *keepLease {
		add("lease", "skip", "kept (--keep-lease)")
	} else if gameFailed && !*releaseAnyway {
		add("lease", "fail", fmt.Sprintf("kept: game-side teardown failed; check the slot, then forever-agent lease release %s %s --holder %s",
			manifest.Server, manifest.Slot, manifest.Holder))
	} else if slotsPathErr != nil {
		add("lease", "fail", slotsPathErr.Error())
	} else {
		step, status, detail := teardownReleaseLease(*slotsPath, manifest)
		add(step, status, detail)
	}

	return teardownReport(manifest, steps, failed, *asJSON, stdout)
}

func teardownPIDFilePresent(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, "agent.pid"))
	return err == nil
}

// teardownAgentPid reads the pid agent.pid names (0 when unreadable).
func teardownAgentPid(dir string) int {
	raw, err := os.ReadFile(filepath.Join(dir, "agent.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// teardownPIDReused reports whether the pid in agent.pid names a running process that the probe did not accept as
// this dir's agent: the pid was reused, so signalling it or deleting the pid file would reach the wrong process.
func teardownPIDReused(dir string, pid int) bool {
	if pid <= 0 {
		pid = teardownAgentPid(dir)
	}
	return pid > 0 && slots.PIDRunning(pid)
}

func teardownReport(manifest teardownSlotManifest, steps []teardownCLIStep, failed int, asJSON bool, stdout io.Writer) int {
	if asJSON {
		out := struct {
			Server string            `json:"server"`
			Slot   string            `json:"slot"`
			Steps  []teardownCLIStep `json:"steps"`
			Failed int               `json:"failed"`
		}{Server: manifest.Server, Slot: manifest.Slot, Steps: steps, Failed: failed}
		raw, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(stdout, string(raw))
	} else {
		fmt.Fprintf(stdout, "teardown %s/%s (%s, port %d):\n", manifest.Server, manifest.Slot, manifest.Character, manifest.Port)
		for _, s := range steps {
			tag := map[string]string{"ok": "ok", "skip": "skip", "fail": "FAIL", "plan": "plan"}[s.Status]
			if tag == "" {
				tag = s.Status
			}
			fmt.Fprintf(stdout, "%-4s %-14s %s\n", tag, s.Step, s.Detail)
		}
		ok, skipped := 0, 0
		for _, s := range steps {
			switch s.Status {
			case "ok":
				ok++
			case "skip", "plan":
				skipped++
			}
		}
		fmt.Fprintf(stdout, "teardown: %d ok, %d skipped, %d failed\n", ok, skipped, failed)
	}
	if failed > 0 {
		return exitTeardownFailed
	}
	return exitOK
}

// teardownRPCReply tolerates both the envelope {result:{steps,failed}} and a
// raw result {steps,failed}.
type teardownRPCReply struct {
	Result *struct {
		Steps  []teardownCLIStep `json:"steps"`
		Failed int               `json:"failed"`
	} `json:"result"`
	Steps []teardownCLIStep `json:"steps"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func teardownCallAgent(dir string, port int, tokenFile string, noAuth, dryRun bool, gm string) ([]teardownCLIStep, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "teardown",
		"params":  map[string]any{"dry_run": dryRun, "gm": gm},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/rpc", port), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if !noAuth {
		token, err := rpc.ReadTokenFile(tokenFile)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var reply teardownRPCReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Error != nil {
		return []teardownCLIStep{{Step: "rpc", Status: "fail", Detail: reply.Error.Message}}, nil
	}
	if reply.Result != nil {
		return reply.Result.Steps, nil
	}
	return reply.Steps, nil
}

// teardownAgentExit waits for the agent pid to end, then SIGTERM/SIGKILL.
// Each wait loop is bounded by iterations as well as time so a fake probe
// that always reports alive cannot hang tests (the sleeps are injected).
func teardownAgentExit(dir string, pid int) (string, string, string) {
	waitLoop := func(d time.Duration, poll time.Duration) bool {
		deadline := time.Now().Add(d)
		for i := 0; i < 1000 && time.Now().Before(deadline); i++ {
			if !teardownPIDAlive(dir, pid) {
				return true
			}
			teardownCLISleep(poll)
			if _, alive := teardownProbe.AgentPID(dir); !alive {
				return true
			}
		}
		return !teardownPIDAlive(dir, pid)
	}
	if waitLoop(20*time.Second, 500*time.Millisecond) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	if !teardownPIDAlive(dir, pid) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	// Re-check identity before signalling.
	if _, alive := teardownProbe.AgentPID(dir); !alive {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	if pid == os.Getpid() {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return "agent_exit", "ok", fmt.Sprintf("pid %d exited", pid)
	}
	_ = proc.Signal(syscall.SIGTERM)
	if waitLoop(40*time.Second, 500*time.Millisecond) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d ended by SIGTERM", pid)
	}
	if !teardownPIDAlive(dir, pid) {
		return "agent_exit", "ok", fmt.Sprintf("pid %d ended by SIGTERM", pid)
	}
	_ = proc.Signal(syscall.SIGKILL)
	if waitLoop(5*time.Second, 500*time.Millisecond) {
		return "agent_exit", "fail", fmt.Sprintf("pid %d killed (SIGKILL): the character may stay in world", pid)
	}
	return "agent_exit", "fail", fmt.Sprintf("pid %d killed (SIGKILL): the character may stay in world", pid)
}

func teardownPIDAlive(dir string, pid int) bool {
	got, alive := teardownProbe.AgentPID(dir)
	return alive && got == pid
}

// teardownDeleteFiles removes settings.json, the derived token file and
// agent.pid. All paths are derived inside dir (the manifest was asserted by
// teardownCheckManifest); a token_file outside the dir never reaches Remove.
func teardownDeleteFiles(dir string, port int) (string, string, string) {
	type gone struct {
		name string
		path string
	}
	token := fmt.Sprintf("rpc-%d.token", port)
	targets := []gone{
		{"settings.json", filepath.Join(dir, "settings.json")},
		{token, filepath.Join(dir, token)},
		{"agent.pid", filepath.Join(dir, "agent.pid")},
	}
	var deleted, missing []string
	for _, t := range targets {
		if err := os.Remove(t.path); err == nil {
			deleted = append(deleted, t.name)
		} else {
			missing = append(missing, t.name+" (already gone)")
		}
	}
	detail := "deleted " + strings.Join(append(deleted, missing...), ", ")
	return "files", "ok", detail
}

// teardownSlotHoldsALease says whether a slot.json was written by `slot setup` (a
// lease id and the slots file it came from) rather than by `forever serve --dir`,
// whose manifest names neither.
func teardownSlotHoldsALease(manifest teardownSlotManifest) bool {
	return manifest.LeaseID != "" || manifest.SlotsPath != ""
}

func teardownReleaseLease(slotsPath string, manifest teardownSlotManifest) (string, string, string) {
	path := slotsPath
	if path == "" {
		path = slots.DefaultPath()
	}
	cfg, err := slots.Load(path)
	if err != nil {
		return "lease", "fail", err.Error()
	}
	store := slots.NewStore(cfg, path)
	if _, err := store.Release(manifest.Server, manifest.Slot, manifest.Holder, manifest.LeaseID, false); err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			return "lease", "skip", fmt.Sprintf("not leased by %s any more (%s)", manifest.Holder, r.Msg)
		}
		return "lease", "fail", err.Error()
	}
	return "lease", "ok", fmt.Sprintf("released %s/%s", manifest.Server, manifest.Slot)
}

// teardownPlanSteps appends plan lines for steps 3-7 during --dry-run.
func teardownPlanSteps(dir string, manifest teardownSlotManifest, pid int, alive, reused, keepLease bool, add func(string, string, string)) {
	if alive {
		add("agent_exit", "plan", fmt.Sprintf("would wait for pid %d to exit", pid))
	} else {
		add("agent_exit", "plan", "no agent running")
	}
	if teardownProbe.PortListening(manifest.Port) {
		add("port", "plan", fmt.Sprintf("would check port %d", manifest.Port))
	} else {
		add("port", "plan", fmt.Sprintf("port %d closed", manifest.Port))
	}
	add("other_pids", "plan", "would end stray pids of this dir")
	if reused {
		add("files", "plan", fmt.Sprintf("would keep settings.json, token file and agent.pid (pid %d is another process)", pid))
	} else {
		add("files", "plan", "would delete settings.json, token file and agent.pid")
	}
	if !teardownSlotHoldsALease(manifest) {
		add("lease", "plan", "a serve slot holds no lease (forever serve --dir)")
	} else if keepLease {
		add("lease", "plan", "would keep the lease (--keep-lease)")
	} else {
		add("lease", "plan", fmt.Sprintf("would release %s/%s", manifest.Server, manifest.Slot))
	}
}
