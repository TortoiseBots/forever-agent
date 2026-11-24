// Package addonhost runs a real Forever add-on headless: a lua5.1 sidecar loads the add-on's .toc files on a
// small client mock (lua/mock.lua) and is driven over stdin/stdout, one JSON object per line (lua/host.lua
// documents the ops). What the add-on asks the client to do (SendAddonMessage, SendChatMessage, TargetUnit,
// TargetByName) comes back as Out values for the caller to perform on its session; the host itself touches no
// connection.
//
// A sidecar rather than a Go Lua VM: the add-on runs on the same C Lua 5.1 an add-on is written for, the module
// keeps no dependencies, and an add-on that errors or loops cannot take the daemon down: a call that does not
// answer in CallTimeout kills the process, and a handler error is recorded (State.Errors) and the dispatch goes
// on. The sidecar's address space is bounded too (MemoryLimit), so an add-on cannot exhaust the host's memory.
//
// The add-on's folder is read once, in this process, before the interpreter starts: every .toc and source path is
// resolved and must stay beneath the folder's own resolved path, so neither a symlink nor a ".." line can make the
// sidecar read or run a file outside it.
//
// The add-on message transport is not here: the caller registers State.Prefixes with its session and sends each
// Out through it (internal/forever/addonmsg's opcodes and channel kinds), so this package only knows the client
// API and the frame shapes.
package addonhost

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed lua/*.lua
var luaFiles embed.FS

// DefaultLua is the interpreter the host runs when none is given.
const DefaultLua = "lua5.1"

// CallTimeout bounds one call: an add-on that runs longer is taken to be stuck. A frame update is milliseconds of
// work, and the daemon's own steps never wait for the sidecar, so a stuck call costs the add-on its host, not the
// session.
var CallTimeout = 3 * time.Second

// MemoryLimit is the address space one sidecar may use, in bytes. It is enforced by the kernel before lua5.1
// starts (RLIMIT_AS on Linux); 0 leaves the sidecar unlimited.
var MemoryLimit int64 = 256 << 20

// maxAddonBytes bounds one add-on folder: its .toc and sources are read into this process before the sidecar
// starts, so an add-on's whole code is a handful of megabytes, never an unbounded read.
const maxAddonBytes = 8 << 20

// ErrStopped is returned by calls after the sidecar has exited or was stopped.
var ErrStopped = errors.New("addon host stopped")

// Out is one thing the add-on asked the client to do during a call.
type Out struct {
	Out     string `json:"out"`               // "addon", "chat" or "target"
	Prefix  string `json:"prefix,omitempty"`  // addon: the message prefix
	Channel string `json:"channel,omitempty"` // addon: PARTY, RAID, GUILD, OFFICER, WHISPER, INSTANCE_CHAT
	Type    string `json:"type,omitempty"`    // chat: SAY, PARTY, WHISPER, ...
	Text    string `json:"text,omitempty"`
	To      string `json:"to,omitempty"`   // addon whisper target, chat whisper target
	Name    string `json:"name,omitempty"` // target: the unit or player the add-on named
}

// Unit is one unit the mock's Unit* functions answer for.
type Unit struct {
	Name      string `json:"name,omitempty"`
	GUID      string `json:"guid,omitempty"`
	Level     int    `json:"level,omitempty"`
	Class     string `json:"class,omitempty"`
	ClassFile string `json:"class_file,omitempty"`
	Dead      bool   `json:"dead,omitempty"`
	Ghost     bool   `json:"ghost,omitempty"`
	Online    bool   `json:"online"`
	Player    bool   `json:"player,omitempty"`
	Enemy     bool   `json:"enemy,omitempty"`
}

// World is what the add-on may ask about: the player, its target, party1..4 and raid1..40.
type World struct {
	Player Unit   `json:"player"`
	Target *Unit  `json:"target,omitempty"`
	Party  []Unit `json:"party"`
	Raid   []Unit `json:"raid"`
}

// Event is one game event as the add-on's OnEvent handlers read it: the event name and arg1..argN.
type Event struct {
	Event string   `json:"event"`
	Args  []string `json:"args"`
}

// LoadResult is what the .toc said and what the add-on registered while loading.
type LoadResult struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Version  string   `json:"version"`
	Files    []string `json:"files"`
	Prefixes []string `json:"prefixes"`
}

// LuaError is one add-on handler error the host contained.
type LuaError struct {
	Where string `json:"where"`
	Error string `json:"error"`
}

// ChatLine is one line the add-on printed or sent to the chat frame.
type ChatLine struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// State is what the host and the add-on did so far.
type State struct {
	Clock    float64    `json:"clock"`
	Loaded   bool       `json:"loaded"`
	Name     string     `json:"name"`
	Version  string     `json:"version"`
	Files    []string   `json:"files"`
	Prefixes []string   `json:"prefixes"`
	Errors   []LuaError `json:"errors"`
	Chat     []ChatLine `json:"chat"`
	Frames   int        `json:"frames"`
}

// Host is one running sidecar. Calls are serialised.
type Host struct {
	mu     sync.Mutex
	addon  addonTOC
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan []byte
	exited chan struct{}
	// quit tells the reader to drop lines nobody will read any more.
	quit     chan struct{}
	quitOnce sync.Once
	waitMu   sync.Mutex
	err      error // why the process ended
	stderr   *tail
	next     int
}

// addonTOC is one add-on folder read and checked before the interpreter starts: the .toc's title and version and
// each file it names, with its source. The sidecar never opens a file of the add-on's own, so this is the only
// place the add-on's code comes from.
type addonTOC struct {
	Name    string        `json:"name"`
	Title   string        `json:"title"`
	Version string        `json:"version"`
	Files   []addonSource `json:"files"`
}

// addonSource is one file the .toc names: the name it names it by, and its text.
type addonSource struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Start runs lua5.1 (from PATH) on the extracted host scripts and the add-on in addonDir, whose .toc it finds.
// The add-on's .toc and sources are read and checked here: every path must stay beneath addonDir's resolved path.
// The scripts' temp directory is removed as soon as the host has read them. Replies come on the child's file
// descriptor 3; its stdout and stderr only feed Stderr.
func Start(addonDir string) (*Host, error) {
	dir, err := filepath.EvalSymlinks(addonDir)
	if err != nil {
		return nil, fmt.Errorf("add-on folder: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("add-on folder: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("add-on folder %s is not a directory", addonDir)
	}
	addon, err := readAddon(dir)
	if err != nil {
		return nil, err
	}
	path, err := exec.LookPath(DefaultLua)
	if err != nil {
		return nil, fmt.Errorf("%s not found (apt install lua5.1): %w", DefaultLua, err)
	}
	scripts, err := os.MkdirTemp("", "forever-agent-addonhost-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scripts) // read by the time hello answers (or the start failed)
	for _, name := range []string{"host.lua", "json.lua", "mock.lua"} {
		b, err := luaFiles.ReadFile("lua/" + name)
		if err == nil {
			err = os.WriteFile(filepath.Join(scripts, name), b, 0o600)
		}
		if err != nil {
			return nil, err
		}
	}
	protoR, protoW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	h := &Host{addon: addon, lines: make(chan []byte, 64), exited: make(chan struct{}), quit: make(chan struct{}), stderr: &tail{max: 4096}}
	h.cmd = exec.Command(path, filepath.Join(scripts, "host.lua"))
	h.cmd.Stdout, h.cmd.Stderr = h.stderr, h.stderr
	h.cmd.ExtraFiles = []*os.File{protoW} // fd 3 in the child
	setDeathSignal(h.cmd)
	if err := limitSidecar(h.cmd, MemoryLimit); err != nil {
		protoR.Close()
		protoW.Close()
		return nil, err
	}
	if h.stdin, err = h.cmd.StdinPipe(); err != nil {
		protoR.Close()
		protoW.Close()
		return nil, err
	}
	err = h.cmd.Start()
	protoW.Close() // the child holds its copy; EOF on protoR once it exits
	if err != nil {
		protoR.Close()
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(protoR)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			select {
			case h.lines <- append([]byte(nil), sc.Bytes()...):
			case <-h.quit:
			}
		}
		protoR.Close()
		err := h.cmd.Wait()
		h.waitMu.Lock()
		if err == nil {
			err = errors.New("exited")
		}
		h.err = err
		h.waitMu.Unlock()
		close(h.exited)
	}()
	if _, _, err := h.Call("hello", nil); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// findTOC returns the add-on's .toc file name: the one file of that kind in its folder (sorted, so a folder with
// more than one is refused by name rather than picked at random).
func findTOC(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("add-on folder: %w", err)
	}
	var tocs []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".toc") {
			tocs = append(tocs, e.Name())
		}
	}
	sort.Strings(tocs)
	switch len(tocs) {
	case 0:
		return "", fmt.Errorf("no .toc in %s", dir)
	case 1:
		return tocs[0], nil
	}
	return "", fmt.Errorf("%s holds %d .toc files (%s): point the daemon at the one add-on's folder", dir, len(tocs), strings.Join(tocs, ", "))
}

// readAddon reads the folder's .toc and every file it names. dir is the folder's resolved path; each path is
// resolved again and must stay beneath it, so a symlink pointing out and a path that climbs out are both refused
// here, before any interpreter sees the add-on. The .toc's own syntax is the client's: a header line ("## Title:",
// "## Version:") and a .lua line per file, trailing whitespace trimmed, backslashes as path separators.
func readAddon(dir string) (addonTOC, error) {
	a := addonTOC{Name: filepath.Base(dir)}
	tocName, err := findTOC(dir)
	if err != nil {
		return a, err
	}
	tocPath, err := contained(dir, tocName)
	if err != nil {
		return a, fmt.Errorf("the .toc: %w", err)
	}
	raw, err := os.ReadFile(tocPath)
	if err != nil {
		return a, fmt.Errorf("the .toc: %w", err)
	}
	total := len(raw)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.ReplaceAll(line, "\r", "")
		if v, ok := tocHeader(line, "## Version:"); ok {
			a.Version = v
			continue
		}
		if v, ok := tocHeader(line, "## Title:"); ok {
			a.Title = v
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.TrimRight(line, " \t\v\f")
		if !strings.HasSuffix(name, ".lua") {
			continue
		}
		rel := strings.ReplaceAll(name, "\\", "/")
		if strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
			return a, fmt.Errorf("the .toc names a file outside the add-on: %s", name)
		}
		file, err := contained(dir, rel)
		if err != nil {
			return a, fmt.Errorf("the .toc names %s: %w", name, err)
		}
		src, err := os.ReadFile(file)
		if err != nil {
			return a, fmt.Errorf("the .toc names %s: %w", name, err)
		}
		if !utf8.Valid(src) {
			// The source travels as JSON text; anything else would be replaced by U+FFFD on the way, so it is
			// refused by name instead of run mangled.
			return a, fmt.Errorf("the .toc names %s, which is not UTF-8 text", name)
		}
		total += len(src)
		if total > maxAddonBytes {
			return a, fmt.Errorf("the add-on's .toc and sources are over %d bytes", maxAddonBytes)
		}
		a.Files = append(a.Files, addonSource{Name: name, Source: string(src)})
	}
	return a, nil
}

// tocHeader is one .toc header line's value, trimmed; an empty value is not a header (as the client reads it).
func tocHeader(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	v := strings.TrimSpace(line[len(prefix):])
	return v, v != ""
}

// contained resolves name under root (already resolved) and refuses anything that does not stay beneath it: a
// symlink pointing outside the add-on is refused like a path that climbs out.
func contained(root, name string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(name))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s resolves outside %s (%s)", name, root, resolved)
	}
	return resolved, nil
}

// Load runs the load op: the .toc's files, ADDON_LOADED, then PLAYER_LOGIN and PLAYER_ENTERING_WORLD when the
// character is in world.
func (h *Host) Load(world World, inWorld bool) (LoadResult, []Out, error) {
	var res LoadResult
	raw, outs, err := h.Call("load", map[string]any{"world": world, "in_world": inWorld, "addon": h.addon})
	if err != nil {
		return res, outs, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, outs, fmt.Errorf("load result: %w", err)
	}
	return res, outs, nil
}

// PushWorld hands the add-on a new world; the group events fire where the party or raid changed.
func (h *Host) PushWorld(world World) ([]Out, error) {
	_, outs, err := h.Call("world", map[string]any{"world": world})
	return outs, err
}

// SendEvents fires game events to the frames that registered them.
func (h *Host) SendEvents(evs []Event) ([]Out, error) {
	if len(evs) == 0 {
		return nil, nil
	}
	_, outs, err := h.Call("events", map[string]any{"events": evs})
	return outs, err
}

// Tick advances the add-on's GetTime by ms and runs OnUpdate.
func (h *Host) Tick(ms int) (float64, []Out, error) {
	raw, outs, err := h.Call("tick", map[string]any{"ms": ms})
	if err != nil {
		return 0, outs, err
	}
	var res struct {
		Clock float64 `json:"clock"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, outs, fmt.Errorf("tick result: %w", err)
	}
	return res.Clock, outs, nil
}

// Command types one slash command at the add-on, as the player would.
func (h *Host) Command(line string) ([]Out, error) {
	_, outs, err := h.Call("cmd", map[string]any{"line": line})
	return outs, err
}

// State reads what the host and the add-on did so far.
func (h *Host) State() (State, []Out, error) {
	var st State
	raw, outs, err := h.Call("state", nil)
	if err != nil {
		return st, outs, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, outs, fmt.Errorf("state result: %w", err)
	}
	return st, outs, nil
}

// Call runs op with params (a JSON object, or nil) and returns its result and the Outs the add-on produced on
// the way, in the order it asked for them. The caller performs them on its session; a failure to perform one does
// not fail the call (the client would not tell the add-on either).
func (h *Host) Call(op string, params any) (json.RawMessage, []Out, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.exited:
		return nil, nil, h.stopped()
	default:
	}
	h.next++
	id := h.next
	req := map[string]any{}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(b, &req); err != nil {
			return nil, nil, fmt.Errorf("params of %s must be an object: %w", op, err)
		}
	}
	req["id"], req["op"] = id, op
	b, _ := json.Marshal(req)
	if _, err := h.stdin.Write(append(b, '\n')); err != nil {
		return nil, nil, h.stopped()
	}
	var outs []Out
	timer := time.NewTimer(CallTimeout)
	defer timer.Stop()
	for {
		select {
		case line := <-h.lines:
			var m struct {
				ID     int             `json:"id"`
				Out    string          `json:"out"`
				Result json.RawMessage `json:"result"`
				Error  string          `json:"error"`
			}
			if err := json.Unmarshal(line, &m); err != nil {
				// The protocol has lost its framing: nothing later on it can be trusted.
				h.kill()
				return nil, outs, fmt.Errorf("%w: it wrote %q, not a JSON frame: %v", ErrStopped, truncate(string(line), 200), err)
			}
			if m.Out != "" {
				var o Out
				json.Unmarshal(line, &o)
				outs = append(outs, o)
				continue
			}
			if m.ID != id {
				continue // a reply to an earlier call that timed out cannot happen: that kills the process
			}
			if m.Error != "" {
				return nil, outs, errors.New(m.Error)
			}
			return m.Result, outs, nil
		case <-h.exited:
			return nil, outs, h.stopped()
		case <-timer.C:
			h.kill()
			return nil, outs, fmt.Errorf("%w: %s took over %s and was killed (an endless loop in the add-on?)", ErrStopped, op, CallTimeout)
		}
	}
}

func (h *Host) stopped() error {
	h.waitMu.Lock()
	err := h.err
	h.waitMu.Unlock()
	detail := ""
	if err != nil {
		detail += ": " + err.Error()
	}
	if s := strings.TrimSpace(h.stderr.String()); s != "" {
		detail += ": " + s
	}
	return fmt.Errorf("%w%s", ErrStopped, detail)
}

func (h *Host) kill() {
	h.quitOnce.Do(func() { close(h.quit) })
	if h.cmd.Process != nil {
		h.cmd.Process.Kill()
	}
}

// Close ends the sidecar (end of stdin, then a kill if it does not exit).
func (h *Host) Close() error {
	h.quitOnce.Do(func() { close(h.quit) })
	h.stdin.Close()
	select {
	case <-h.exited:
	case <-time.After(2 * time.Second):
		h.kill()
		<-h.exited
	}
	return nil
}

// Stderr is the tail of what the sidecar wrote to stderr.
func (h *Host) Stderr() string { return h.stderr.String() }

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.ToValidUTF8(t.buf, []byte("?")))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
