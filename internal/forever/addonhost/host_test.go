package addonhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// fakeAddon is the test's tiny add-on: it registers a prefix at load, answers the login with one frame, prints
// every add-on frame it gets, raises on the frame whose text is "boom", and sends on its slash command.
//
// Mutations these tests catch: returning a frame without passing it back as an Out (TestHostLoadsAndSends sees
// none), a dispatch without pcall (TestHostContainsHandlerErrors loses the following frame), a Tick that runs
// every frame regardless of visibility (TestHostTick records the hidden frame's error), a Call that does not kill
// a stuck child (TestHostKillsStuckAddon hangs instead of answering), a sidecar without an address-space limit
// (TestHostBoundsSidecarMemory never sees the allocation fail), and a loader that does not resolve paths
// (TestStartRefusesEscape loads a symlinked file from outside the folder).
const fakeAddon = `
local f = CreateFrame("Frame", "TinyFrame")
f:RegisterEvent("PLAYER_LOGIN")
f:RegisterEvent("PLAYER_ENTERING_WORLD")
f:RegisterEvent("CHAT_MSG_ADDON")
f:RegisterEvent("CHAT_MSG_SYSTEM")
RegisterAddonMessagePrefix("TINY")
f:SetScript("OnEvent", function()
    if event == "PLAYER_LOGIN" then
        SendAddonMessage("TINY", "hello", "PARTY")
    elseif event == "CHAT_MSG_ADDON" then
        if arg2 == "boom" then error("handler boom") end
        print("got " .. tostring(arg1) .. "|" .. tostring(arg2) .. "|" .. tostring(arg3) .. "|" .. tostring(arg4))
    elseif event == "CHAT_MSG_SYSTEM" then
        print("sys " .. tostring(arg1))
    end
end)
SLASH_TINYADDON1 = "/tiny"
SlashCmdList["TINYADDON"] = function(msg)
    SendAddonMessage("TINY", msg, "GUILD", "Peer")
end
`

func requireLua(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(DefaultLua); err != nil {
		if os.Getenv("ADDON_HOST_STRICT") == "1" {
			t.Fatal("lua5.1 not installed")
		}
		t.Skip("lua5.1 not installed")
	}
}

// startAddon writes an add-on of one Lua file and one .toc into a folder named TinyAddon and starts the host on it.
func startAddon(t *testing.T, title, src string) *Host {
	t.Helper()
	requireLua(t)
	dir := filepath.Join(t.TempDir(), "TinyAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: "+title+"\n## Version: 1.2\nTiny.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// loadFake starts the host on fakeAddon and loads it in world.
func loadFake(t *testing.T) *Host {
	t.Helper()
	h := startAddon(t, "Tiny", fakeAddon)
	if _, _, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true, nil); err != nil {
		t.Fatal(err)
	}
	return h
}

// The .toc is read, the add-on's registered prefix is reported, and the frame it sent at PLAYER_LOGIN came back
// as an Out the caller performs.
func TestHostLoadsAndSends(t *testing.T) {
	h := startAddon(t, "Tiny", fakeAddon)
	res, outs, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "TinyAddon" || res.Title != "Tiny" || res.Version != "1.2" {
		t.Fatalf("load result %+v", res)
	}
	if len(res.Files) != 1 || res.Files[0] != "Tiny.lua" {
		t.Fatalf("files %+v", res.Files)
	}
	if len(res.Prefixes) != 1 || res.Prefixes[0] != "TINY" {
		t.Fatalf("prefixes %+v", res.Prefixes)
	}
	if len(outs) != 1 || outs[0].Out != "addon" || outs[0].Prefix != "TINY" || outs[0].Text != "hello" || outs[0].Channel != "PARTY" {
		t.Fatalf("outs from the login %+v", outs)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Loaded || st.Name != "TinyAddon" || len(st.Prefixes) != 1 {
		t.Fatalf("state %+v", st)
	}
}

// A delivered frame reaches the add-on as CHAT_MSG_ADDON with the client's four arguments.
func TestHostReceivesFrames(t *testing.T) {
	h := loadFake(t)
	if _, err := h.SendEvents([]Event{{Event: "CHAT_MSG_ADDON", Args: []string{"TINY", "from-peer", "PARTY", "Peer"}}}); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Chat) != 1 || st.Chat[0].Text != "got TINY|from-peer|PARTY|Peer" {
		t.Fatalf("chat %+v", st.Chat)
	}
	if len(st.Errors) != 0 {
		t.Fatalf("errors %+v", st.Errors)
	}
}

// An add-on handler that raises is recorded and the host goes on: the next frame is still delivered and state
// still answers.
func TestHostContainsHandlerErrors(t *testing.T) {
	h := loadFake(t)
	if _, err := h.SendEvents([]Event{{Event: "CHAT_MSG_ADDON", Args: []string{"TINY", "boom", "PARTY", "Peer"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SendEvents([]Event{{Event: "CHAT_MSG_ADDON", Args: []string{"TINY", "after", "RAID", "Peer"}}}); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Errors) != 1 || !strings.Contains(st.Errors[0].Error, "handler boom") {
		t.Fatalf("errors %+v", st.Errors)
	}
	if len(st.Chat) != 1 || st.Chat[0].Text != "got TINY|after|RAID|Peer" {
		t.Fatalf("chat after the error %+v", st.Chat)
	}
}

// A slash command reaches the add-on's handler, and what it sends comes back as an Out.
func TestHostCommandSends(t *testing.T) {
	h := loadFake(t)
	outs, err := h.Command("/tiny hello guild")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Text != "hello guild" || outs[0].Channel != "GUILD" || outs[0].To != "Peer" {
		t.Fatalf("outs %+v", outs)
	}
	if _, err := h.Command("/no-such-command"); err == nil {
		t.Fatal("an unknown slash command was accepted")
	}
}

// tick advances the add-on's GetTime and runs OnUpdate on the visible frames.
func TestHostTick(t *testing.T) {
	src := `
Ticks = 0
local f = CreateFrame("Frame", "TickFrame")
f:SetScript("OnUpdate", function(self, dt)
    Ticks = Ticks + 1
    if Ticks == 3 then SendAddonMessage("TINY", "tick " .. tostring(dt), "RAID") end
end)
local hidden = CreateFrame("Frame", "HiddenFrame")
hidden:Hide()
hidden:SetScript("OnUpdate", function() error("a hidden frame's OnUpdate ran") end)
`
	h := startAddon(t, "Tick", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	var outs []Out
	for range 3 {
		_, more, err := h.Tick(250)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, more...)
	}
	if len(outs) != 1 || outs[0].Text != "tick 0.25" || outs[0].Channel != "RAID" {
		t.Fatalf("outs %+v", outs)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Clock != 1.75 {
		t.Fatalf("clock %v after three 250 ms ticks from 1", st.Clock)
	}
	if len(st.Errors) != 0 {
		t.Fatalf("a hidden frame's OnUpdate ran: %+v", st.Errors)
	}
}

// The add-on is sandboxed: the host's own io, os and loadfile are not in its environment.
func TestHostSandbox(t *testing.T) {
	src := `
print((io == nil) and "noio" or "io")
print((os == nil) and "noos" or "os")
print((loadfile == nil) and "noloadfile" or "loadfile")
print((dofile == nil) and "nodofile" or "dofile")
print((require == nil) and "norequire" or "require")
print((debug == nil) and "nodebug" or "debug")
`
	h := startAddon(t, "Sandbox", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	want := "noio,noos,noloadfile,nodofile,norequire,nodebug"
	if strings.Join(lines, ",") != want {
		t.Fatalf("sandbox leaks: %v", lines)
	}
}

// An add-on stuck in a loop is killed after CallTimeout, and later calls say the host is gone.
func TestHostKillsStuckAddon(t *testing.T) {
	old := CallTimeout
	CallTimeout = 500 * time.Millisecond
	t.Cleanup(func() { CallTimeout = old })
	src := `
local f = CreateFrame("Frame", "SpinFrame")
Spin = false
f:SetScript("OnUpdate", function() if Spin then while true do end end end)
SLASH_SPIN1 = "/spin"
SlashCmdList["SPIN"] = function() Spin = true end
`
	h := startAddon(t, "Spin", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Command("/spin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Tick(100); err == nil {
		t.Fatal("the stuck add-on was not killed")
	}
	if _, _, err := h.State(); err == nil {
		t.Fatal("state answered after the host was killed")
	}
}

// Nothing the .toc names may leave the add-on's folder: a symlinked .toc or Lua file, an absolute path and a
// parent-relative path are all refused before the interpreter runs (an add-on is untrusted code and this folder is
// its whole world).
func TestStartRefusesEscape(t *testing.T) {
	requireLua(t)
	outside := filepath.Join(t.TempDir(), "Outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "Evil.toc"), []byte("## Title: Evil\nEvil.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "Evil.lua"), []byte("print('escaped')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newDir := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "TinyAddon")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// A .toc that is a symlink to a file outside the folder.
	dir := newDir(t)
	if err := os.Symlink(filepath.Join(outside, "Evil.toc"), filepath.Join(dir, "TinyAddon.toc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a symlinked .toc pointing outside the folder: %v", err)
	}

	// A Lua file the .toc names that is a symlink to a file outside the folder.
	dir = newDir(t)
	if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: Tiny\nEvil.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "Evil.lua"), filepath.Join(dir, "Evil.lua")); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a symlinked Lua file pointing outside the folder: %v", err)
	}

	// A .toc whose file lines are an absolute path or climb out with "..".
	for _, line := range []string{filepath.Join(outside, "Evil.lua"), "../Outside/Evil.lua"} {
		dir = newDir(t)
		if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: Tiny\n"+line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Start(dir); err == nil {
			t.Fatalf("the .toc line %q was accepted", line)
		}
	}
}

// An add-on cannot exhaust the host's memory: the sidecar runs under an enforced address-space limit, so an
// allocation bomb fails inside Lua (the host records the error and keeps answering) instead of growing until the
// machine's own memory is gone. The limit itself is read back from the kernel, not assumed.
func TestHostBoundsSidecarMemory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the address-space limit is enforced with RLIMIT_AS on Linux")
	}
	oldLimit, oldTimeout := MemoryLimit, CallTimeout
	MemoryLimit, CallTimeout = 64<<20, 5*time.Second
	t.Cleanup(func() { MemoryLimit, CallTimeout = oldLimit, oldTimeout })
	src := `
SLASH_BOMB1 = "/bomb"
SlashCmdList["BOMB"] = function()
    local blocks = {}
    for i = 1, 8192 do blocks[i] = string.rep("x", 65536) .. i end
end
`
	h := startAddon(t, "Bomb", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := addressSpaceLimit(t, h.cmd.Process.Pid); got != MemoryLimit {
		t.Fatalf("the sidecar's address space limit is %d, want %d", got, MemoryLimit)
	}
	if _, err := h.Command("/bomb"); err == nil || !strings.Contains(err.Error(), "not enough memory") {
		t.Fatalf("the allocation bomb answered %v, want a Lua memory error", err)
	}
	// The error stayed inside the sidecar: it still answers state.
	if _, _, err := h.State(); err != nil {
		t.Fatalf("the host did not survive the allocation bomb: %v", err)
	}
}

// addressSpaceLimit reads pid's soft RLIMIT_AS from /proc, in bytes (0 when unlimited).
func addressSpaceLimit(t *testing.T, pid int) int64 {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Max address space") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			t.Fatalf("unreadable limit line %q", line)
		}
		n, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			t.Fatalf("limit line %q: %v", line, err)
		}
		return n
	}
	t.Fatalf("no Max address space line in /proc/%d/limits", pid)
	return 0
}

// The source reaches the interpreter exactly: multi-byte text keeps its bytes, and a file that is not UTF-8 text
// is refused by name rather than replaced with U+FFFD on the way.
func TestStartKeepsSourcesExact(t *testing.T) {
	requireLua(t)
	dir := filepath.Join(t.TempDir(), "TinyAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: Tiny\nTiny.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), []byte("print(\"\u00e4\u00f6\u00fc\", #\"\u00e4\u00f6\u00fc\")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Chat) != 1 || st.Chat[0].Text != "\u00e4\u00f6\u00fc 6" {
		t.Fatalf("chat %+v, want the source's own bytes", st.Chat)
	}
	h.Close()

	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), []byte("print('\xe4')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "not UTF-8") {
		t.Fatalf("a Latin-1 source: %v", err)
	}
}

// Start refuses a folder without a .toc and one with several, by name.
func TestStartRequiresOneTOC(t *testing.T) {
	requireLua(t)
	if _, err := Start(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no .toc") {
		t.Fatalf("empty folder: %v", err)
	}
	dir := t.TempDir()
	for _, name := range []string{"A.toc", "B.toc"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("X.lua\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "A.toc, B.toc") {
		t.Fatalf("two .toc files: %v", err)
	}
}

// ── the host's bounds on untrusted code ────────────────────────────────────

// A source that is precompiled Lua bytecode is refused before any interpreter sees it: bytecode starts with ESC,
// which is valid UTF-8 and survives the JSON trip, and 5.1 bytecode is a known way out to native code. A source
// that holds a NUL byte is refused for the same reason (the JSON path would keep it, and it is never text).
//
// Mutations these tests catch: a loader that only checks UTF-8 (the dumped chunk is valid UTF-8), and a loader
// that checks ESC but not NUL (the second source loads).
func TestStartRefusesBinarySources(t *testing.T) {
	requireLua(t)
	dir := filepath.Join(t.TempDir(), "TinyAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: Tiny\nTiny.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dumped, err := exec.Command(DefaultLua, "-e", "io.write(string.dump(function() return 1 end))").Output()
	if err != nil {
		t.Fatalf("string.dump: %v", err)
	}
	if len(dumped) < 2 || dumped[0] != 27 || dumped[1] != 'L' {
		t.Fatalf("string.dump wrote %q, not a 5.1 bytecode chunk", dumped[:min(len(dumped), 8)])
	}
	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), dumped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "bytecode") {
		t.Fatalf("a bytecode chunk was accepted: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), []byte("print('a\x00b')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("a source holding a NUL byte was accepted: %v", err)
	}
}

// The sidecar's own load op refuses the same bytes: the Go reader is the first gate, this is the second, so a
// caller that hands the sidecar a chunk directly (the tests do) still cannot load bytecode.
//
// Mutation this test catches: the guard only in readAddon (the direct load answers a result, not an error).
func TestSidecarLoadRefusesBinarySources(t *testing.T) {
	requireLua(t)
	dir := t.TempDir()
	for _, name := range []string{"host.lua", "json.lua", "mock.lua"} {
		b, err := luaFiles.ReadFile("lua/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	protoR, protoW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(DefaultLua, filepath.Join(dir, "host.lua"))
	cmd.ExtraFiles = []*os.File{protoW}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	protoW.Close()
	defer func() {
		stdin.Close()
		cmd.Wait()
	}()
	protoR.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(protoR)
	ask := func(id int, source string) string {
		t.Helper()
		b, err := json.Marshal(map[string]any{"id": id, "op": "load", "world": map[string]any{},
			"addon": map[string]any{"name": "X", "files": []map[string]string{{"name": "X.lua", "source": source}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(stdin, "%s\n", b); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("no reply to the load: %v (stderr %s)", err, stderr.String())
		}
		return line
	}
	if reply := ask(1, "\x1bLuaQ\x00\x01\x04\x08"); !strings.Contains(reply, "bytecode") {
		t.Fatalf("the sidecar answered a bytecode chunk: %s", reply)
	}
	if reply := ask(2, "print('a\x00b')\n"); !strings.Contains(reply, "NUL") {
		t.Fatalf("the sidecar answered a source holding a NUL byte: %s", reply)
	}
}

// An add-on cannot queue a million sends in one call: the client's budget (20 per call, 10 per second, 100
// queued) refuses the send, the call answers an error and the host is marked throttled, not broken. The daemon
// would otherwise hold every Out and send them all from the character's name on its session goroutine.
//
// Mutations these tests catch: no per-call cap (a million Outs come back), a cap that kills the host on the first
// overrun (State no longer answers), and a cap whose error is not visible to the caller.
func TestHostCapsOutsPerCall(t *testing.T) {
	src := `
SLASH_FLOOD1 = "/flood"
SlashCmdList["FLOOD"] = function()
    for i = 1, 1000000 do SendChatMessage("x", "SAY") end
end
`
	h := startAddon(t, "Flood", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	outs, err := h.Command("/flood")
	if err == nil {
		t.Fatal("a flood of a million sends answered without an error")
	}
	// The per-second cap is the stricter one inside one wall second; the per-call cap is the bound either way.
	if len(outs) < MaxOutsPerSecond || len(outs) > MaxOutsPerCall {
		t.Fatalf("the flood produced %d Outs, want between the per-second cap %d and the per-call cap %d", len(outs), MaxOutsPerSecond, MaxOutsPerCall)
	}
	if !strings.Contains(err.Error(), "send budget") {
		t.Fatalf("the flood's error %q does not say it went over the send budget", err)
	}
	// The sidecar is still there, and the state says it is throttled.
	st, _, err := h.State()
	if err != nil {
		t.Fatalf("the host did not survive one over-budget call: %v", err)
	}
	if !st.Throttled {
		t.Fatalf("state %+v does not mark the add-on throttled", st)
	}
}

// Three calls in a row over a limit stop the sidecar: a flood on every call must not keep a host alive to flood
// again, while one over-budget call only marks it throttled (the test above).
func TestHostStopsAddonThatKeepsHittingTheLimit(t *testing.T) {
	src := `
SLASH_FLOOD1 = "/flood"
SlashCmdList["FLOOD"] = function()
    for i = 1, 1000000 do SendChatMessage("x", "SAY") end
end
`
	h := startAddon(t, "Flood", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := h.Command("/flood"); err == nil {
			t.Fatalf("flood %d answered without an error", i)
		}
	}
	if _, _, err := h.State(); err == nil {
		t.Fatal("the add-on kept its host after three calls in a row over the send budget")
	}
}

// A handler that burns most of the call budget on every call is stopped after three calls in a row: otherwise a
// real-clock OnUpdate that spins 2.9 s of every 3 s keeps one core busy forever without ever tripping CallTimeout.
//
// Mutation this test catches: only the per-call timeout (every Tick answers, and the host lives on).
func TestHostStopsAddonThatStaysSlow(t *testing.T) {
	oldTimeout, oldSlow, oldBudget := CallTimeout, SlowCallLimit, CPUBudget
	CallTimeout, SlowCallLimit, CPUBudget = 2*time.Second, 10*time.Millisecond, time.Hour
	t.Cleanup(func() { CallTimeout, SlowCallLimit, CPUBudget = oldTimeout, oldSlow, oldBudget })
	src := `
local hot = 0
local f = CreateFrame("Frame", "SlowFrame")
f:SetScript("OnUpdate", function()
    for i = 1, 10000000 do hot = hot + i end
end)
`
	h := startAddon(t, "Slow", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	var err error
	for i := 1; i <= 3 && err == nil; i++ {
		_, _, err = h.Tick(100)
	}
	if err == nil || !strings.Contains(err.Error(), "per-call") {
		t.Fatalf("three slow ticks answered without stopping the add-on: %v", err)
	}
	if _, _, err := h.State(); err == nil {
		t.Fatal("the slow add-on kept its host")
	}
}

// The add-on's environment is an explicit allow-list: the test compares every global it can see with the list the
// host means to give it, and every member of the library tables it can see (string, table, math) with an allowed
// list of their own. Adding loadstring, getfenv, a host function to the sandbox or one more member to a library
// table fails here. A member a build does not provide (the 5.0 compatibility names vary) is tolerated; one this
// list does not name is not.
func TestHostEnvironmentAllowList(t *testing.T) {
	src := `
local names = {}
for k in pairs(_G) do names[#names + 1] = tostring(k) end
table.sort(names)
print(table.concat(names, "\n"))
local function members(name, t)
    local n = {}
    for k in pairs(t) do n[#n + 1] = tostring(k) end
    table.sort(n)
    print(name .. " " .. table.concat(n, ","))
end
members("string", string)
members("table", table)
members("math", math)
`
	h := startAddon(t, "Env", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Chat) != 4 {
		t.Fatalf("chat %+v", st.Chat)
	}
	got := strings.Split(st.Chat[0].Text, "\n")
	want := []string{
		// the mocked client API
		"CreateFrame", "UIParent", "Minimap", "UISpecialFrames", "SlashCmdList", "DEFAULT_CHAT_FRAME",
		"GameTooltip", "GetTime", "time", "SendAddonMessage", "RegisterAddonMessagePrefix", "SendChatMessage",
		"TargetUnit", "TargetByName", "UnitExists", "UnitName", "UnitIsPlayer", "UnitIsDead", "UnitIsGhost",
		"UnitIsConnected", "UnitCanAttack", "UnitIsEnemy", "UnitLevel", "UnitClass", "GetNumPartyMembers",
		"GetNumRaidMembers",
		// the safe Lua library, copied
		"assert", "error", "ipairs", "next", "pairs", "pcall", "xpcall", "select", "tonumber", "tostring",
		"type", "unpack", "rawequal", "rawget", "rawset", "setmetatable",
		"getmetatable", // the wrapper, never the host's own
		"string", "table", "math",
		// the client's short names
		"getn", "tinsert", "tremove", "strfind", "strsub", "strlen", "strlower", "strupper", "strrep",
		"format", "gsub", "gfind", "floor", "ceil", "abs", "min", "max", "mod", "random", "date", "print",
		"_G",
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the add-on's globals\n got %v\nwant %v", got, want)
	}
	allowed := map[string][]string{
		"string": {"byte", "char", "find", "format", "gfind", "gmatch", "gsub", "len", "lower", "match", "rep",
			"reverse", "sub", "upper"},
		"table": {"concat", "foreach", "foreachi", "getn", "insert", "maxn", "remove", "setn", "sort"},
		"math": {"abs", "acos", "asin", "atan", "atan2", "ceil", "cos", "cosh", "deg", "exp", "floor", "fmod",
			"frexp", "huge", "ldexp", "log", "log10", "max", "min", "mod", "modf", "pi", "pow", "rad", "random",
			"randomseed", "sin", "sinh", "sqrt", "tan", "tanh"},
	}
	for _, line := range st.Chat[1:] {
		name, members, ok := strings.Cut(line.Text, " ")
		if !ok {
			t.Fatalf("member line %q", line.Text)
		}
		allow, ok := allowed[name]
		if !ok {
			t.Fatalf("a library table nobody listed: %q", line.Text)
		}
		for _, member := range strings.Split(members, ",") {
			if !slicesContains(allow, member) {
				t.Fatalf("%s.%s is not on the allow-list %v", name, member, allow)
			}
		}
	}
	for _, required := range []string{"string find", "string match", "string gmatch", "string gsub", "table sort",
		"math random"} {
		name, member, _ := strings.Cut(required, " ")
		found := false
		for _, line := range st.Chat[1:] {
			if n, ms, ok := strings.Cut(line.Text, " "); ok && n == name {
				for _, m := range strings.Split(ms, ",") {
					if m == member {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("%s.%s is missing from the environment", name, member)
		}
	}
}

// The add-on cannot reach string.dump by any route, and it cannot write through the string metatable into the
// functions the host's framing uses: getmetatable("") answers nil, the shared dump is gone, and replacing the
// add-on's own string.format leaves the host's JSON frames intact.
//
// Mutations these tests catch: getmetatable exposed (the metatable route answers a table), string.dump left in
// the shared table (direct and method routes answer a function), and host framing that reads the add-on's globals
// (the forged format corrupts a frame or a result).
func TestHostStringsAreIsolated(t *testing.T) {
	src := `
local mt = getmetatable("")
print("mt " .. tostring(mt))
print("direct " .. tostring(string.dump))
local shared = rawget(_G, "string")
print("shared " .. tostring(shared and shared.dump))
local s = ""
print("method " .. tostring(s.dump))
if mt and mt.__index then mt.__index.format = function() return "FORGED" end end
string.format = function() return "FORGED" end
SendAddonMessage("TINY", string.format("%d", 7) .. " " .. tostring(GetTime()), "PARTY")
`
	h := startAddon(t, "Strings", src)
	_, outs, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Text != "FORGED 1" {
		t.Fatalf("the add-on's send %+v, want its own format's text and the host's valid frame", outs)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	for _, want := range []string{"mt nil", "direct nil", "shared nil", "method nil"} {
		if !slicesContains(lines, want) {
			t.Fatalf("chat %v does not hold %q: string.dump is reachable", lines, want)
		}
	}
}

func slicesContains(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// A file the .toc names must be a regular file of bounded size: a FIFO would block addon_start (and the session
// loop with it) until a writer appeared, and a huge file is read before any size check could refuse it.
//
// Mutations these tests catch: os.ReadFile without a stat (Start hangs on the FIFO; the oversize file is read
// whole before the folder's total is checked).
func TestStartRefusesNonRegularAndOversizeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "TinyAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: Tiny\nTiny.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mkfifo", filepath.Join(dir, "Tiny.lua")).CombinedOutput(); err != nil {
		t.Skipf("mkfifo: %v (%s)", err, out)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Start(dir)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("a FIFO in the add-on folder: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start blocked on a FIFO in the add-on folder")
	}
	if err := os.Remove(filepath.Join(dir, "Tiny.lua")); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("a"), maxAddonFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("an oversize source file: %v", err)
	}
}

// The add-on's folder is resolved once, but a writer can swap a validated file (or a parent directory) for a
// symlink before the read. The read opens without following a final symlink and then requires the opened file to
// be what the path resolves to now, still beneath the folder, so a swapped-in link is refused rather than read.
//
// Mutation this test catches: containment checked only before the read (a swapped symlink is followed and the
// outside file is returned).
func TestReadAddonFileRefusesSwappedSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "Evil.lua")
	if err := os.WriteFile(target, []byte("print('outside')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Tiny.lua")
	if err := os.WriteFile(path, []byte("print('inside')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readAddonFile(root, path, "Tiny.lua", maxAddonBytes); err != nil || !strings.Contains(string(got), "inside") {
		t.Fatalf("the regular file read %q, %v", got, err)
	}
	// What a concurrent writer does between contained()'s resolution and the read.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if got, err := readAddonFile(root, path, "Tiny.lua", maxAddonBytes); err == nil {
		t.Fatalf("a source swapped for a symlink outside the folder was read: %q", got)
	}
}

// ── the CPU budget, the field limits and the protocol cap ───────────────────

// fakeCallClock makes each Call report runs[n] of run time: the host reads its clock at the start and at the reply
// of every call, so the test's clock hands out one duration per call.
func fakeCallClock(h *Host, runs []time.Duration) {
	base := time.Unix(1_700_000_000, 0)
	n := 0
	first := true
	h.now = func() time.Time {
		if first {
			first = false
			return base
		}
		first = true
		base = base.Add(runs[n%len(runs)])
		n++
		return base
	}
}

// The CPU budget is over a rolling window, not a strike count a fast call can reset: a call that spins just under
// the per-call limit on every call, and calls that alternate a slow one with a fast one, both stop the add-on.
// A normal add-on is never stopped by it.
//
// Mutations these tests catch: only the per-call timeout, and the strike counter reset by any fast call (the
// 1.4 s handler runs forever under both).
func TestHostStopsAddonOverItsCPUBudget(t *testing.T) {
	src := `
SLASH_NOOP1 = "/noop"
SlashCmdList["NOOP"] = function() end
`
	for _, c := range []struct {
		name string
		runs []time.Duration
	}{
		{"just under the slow-call limit", []time.Duration{1400 * time.Millisecond}},
		{"alternating slow and fast", []time.Duration{2900 * time.Millisecond, 100 * time.Millisecond}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := startAddon(t, "Budget", src)
			if _, _, err := h.Load(World{}, true, nil); err != nil {
				t.Fatal(err)
			}
			fakeCallClock(h, c.runs)
			var err error
			for range 3 {
				if _, _, err = h.State(); err != nil {
					break
				}
			}
			if err == nil || !strings.Contains(err.Error(), "run time") {
				t.Fatalf("three calls of %v each answered without stopping the add-on: %v", c.runs, err)
			}
			if _, _, err := h.State(); err == nil {
				t.Fatal("the add-on kept its host after going over the CPU budget")
			}
		})
	}
	t.Run("a normal add-on", func(t *testing.T) {
		h := startAddon(t, "Budget", src)
		if _, _, err := h.Load(World{}, true, nil); err != nil {
			t.Fatal(err)
		}
		fakeCallClock(h, []time.Duration{20 * time.Millisecond})
		for i := range 40 {
			if _, _, err := h.State(); err != nil {
				t.Fatalf("state %d: %v", i, err)
			}
		}
	})
}

// An add-on cannot make the daemon hold a 15 MiB field: the client refuses a text over the server's 255-byte limit
// before it becomes an Out, the call is marked throttled and the host keeps answering. The review's five sends in
// one call produce no Out at all.
//
// Mutations these tests catch: no field check in the client (the review's five 15 MiB Out lines come back), and a
// refusal that kills the host on the first overrun (State stops answering).
func TestHostRefusesOversizeSends(t *testing.T) {
	src := `
SLASH_BIG1 = "/big"
SlashCmdList["BIG"] = function()
    local big = string.rep("A", 15 * 1024 * 1024)
    for i = 1, 5 do SendAddonMessage("TINY", big, "PARTY") end
end
`
	h := startAddon(t, "Big", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	outs, err := h.Command("/big")
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("five 15 MiB sends answered %v, want the over-limit error", err)
	}
	if !strings.Contains(err.Error(), "255") {
		t.Fatalf("the refusal %q does not name the 255-byte limit", err)
	}
	if len(outs) != 0 {
		biggest := 0
		for _, o := range outs {
			biggest = max(biggest, len(o.Text))
		}
		t.Fatalf("the refused sends came back as %d Outs, the longest %d bytes", len(outs), biggest)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatalf("the host did not survive the refusal: %v", err)
	}
	if !st.Throttled {
		t.Fatalf("state %+v does not mark the add-on throttled", st)
	}
}

// The add-on's own prefix list is capped at its share of the server's 64: with two reserved prefixes that is 32
// (the share is never more than 32), a prefix already registered still answers true at the cap, and the next new
// one and a reserved one answer false.
//
// Mutations these tests catch: a cap of the whole server limit (the session's prefixes and the add-on's together
// would take the 64), and a capacity check that runs before the duplicate check (re-registering answers false).
func TestHostCapsRegisteredPrefixes(t *testing.T) {
	src := `
local n = 0
for i = 1, 40 do if RegisterAddonMessagePrefix(string.format("P%02d", i)) then n = n + 1 end end
print("registered " .. n)
print("again " .. tostring(RegisterAddonMessagePrefix("P01")))
print("over " .. tostring(RegisterAddonMessagePrefix("P33")))
print("reserved " .. tostring(RegisterAddonMessagePrefix("PBC")))
`
	h := startAddon(t, "Prefixes", src)
	if _, _, err := h.Load(World{}, true, []string{"PBC", "TBM"}); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	for _, want := range []string{"registered 32", "again true", "over false", "reserved false"} {
		found := false
		for _, line := range lines {
			if line == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("chat %q does not hold %q", lines, want)
		}
	}
	if len(st.Prefixes) != 32 {
		t.Fatalf("the add-on reports %d prefixes, want 32", len(st.Prefixes))
	}
}

// The mock checks a prefix at the call: an empty one, a tab or a control byte, a non-ASCII byte, a space at either
// end and one over 16 bytes are refused. RegisterAddonMessagePrefix answers false; SendAddonMessage raises (an
// over-limit send), so the prefix never becomes an Out. An inner space is part of a prefix and is taken.
//
// Mutations these tests catch: the old check (length only) that let "A\tB" through to the daemon, and a refusal
// that still reaches the outbox.
func TestHostRefusesBadPrefixes(t *testing.T) {
	src := `
SLASH_PROBE1 = "/probe"
SlashCmdList["PROBE"] = function()
    for _, p in ipairs({"", "A\tB", "A\1B", "ABC ", " ABC", string.rep("x", 17), "PÉ"}) do
        print("register " .. tostring(RegisterAddonMessagePrefix(p)))
        local ok, err = pcall(SendAddonMessage, p, "x", "PARTY")
        print("send " .. tostring(ok) .. " " .. tostring(err))
    end
    print("ok " .. tostring(RegisterAddonMessagePrefix("GOOD 1")))
end
`
	h := startAddon(t, "BadPrefix", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	// The call ended over its field limit (a refused send marks it), so it answers ErrThrottled; the prints are
	// still in the chat log and the host keeps answering.
	if _, err := h.Command("/probe"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("the probe answered %v, want the over-limit error", err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatalf("the host did not survive the refusals: %v", err)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	refusedReg, refusedSend := 0, 0
	for _, line := range lines {
		switch {
		case line == "register false":
			refusedReg++
		case strings.HasPrefix(line, "send false"):
			refusedSend++
		}
	}
	if refusedReg != 7 || refusedSend != 7 {
		t.Fatalf("refused %d registrations and %d sends, want 7 and 7: %q", refusedReg, refusedSend, lines)
	}
	if !slices.Contains(lines, "ok true") {
		t.Fatalf("a printable prefix was refused: %q", lines)
	}
	if len(st.Prefixes) != 1 || st.Prefixes[0] != "GOOD 1" {
		t.Fatalf("the add-on's prefixes %v, want [GOOD 1]", st.Prefixes)
	}
}

// A legal add-on folder can carry hundreds of long file names (its .toc and sources may be 8 MiB in all). The load
// and state replies report at most 64 names inside 8 KiB, then a "..." marker, so a legal folder can never push a
// reply over the daemon's protocol line cap and be killed as if its sidecar had misbehaved.
//
// Mutation this test catches: an unbounded file list (400 real names here make a load reply over 64 KiB).
func TestHostReportsManyFilesWithinTheProtocolCap(t *testing.T) {
	requireLua(t)
	dir := filepath.Join(t.TempDir(), "ManyAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 150)
	var toc strings.Builder
	toc.WriteString("## Title: Many\n## Version: 1\n")
	for i := range 400 {
		name := fmt.Sprintf("%s%03d.lua", long, i)
		toc.WriteString(name + "\n")
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ManyAddon.toc"), []byte(toc.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	res, _, err := h.Load(World{}, true, nil)
	if err != nil {
		t.Fatalf("a legal folder with 400 file names could not load: %v", err)
	}
	last := ""
	if len(res.Files) > 0 {
		last = res.Files[len(res.Files)-1]
	}
	if len(res.Files) == 0 || len(res.Files) > 65 || last != "..." {
		t.Fatalf("the load reports %d files (last %q), want at most 65 ending in \"...\"", len(res.Files), last)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatalf("state after the many-name load: %v", err)
	}
	if len(st.Files) != len(res.Files) {
		t.Fatalf("state reports %d files, the load %d", len(st.Files), len(res.Files))
	}
}

// A sidecar that writes a protocol line over MaxProtocolLine is killed and its host marked failed: with a 16 MiB
// line cap, the review's five 15 MiB sends made the daemon read and keep about 800 MB. The test's sidecar replaces
// the client's SendAddonMessage with one that skips its own checks, as a broken or replaced script would.
//
// Mutation this test catches: a scanner that accepts any line the sidecar writes.
func TestHostKillsSidecarOnOversizeProtocolLine(t *testing.T) {
	requireLua(t)
	old := hostScripts
	t.Cleanup(func() { hostScripts = old })
	read := func(name string) []byte {
		t.Helper()
		b, err := fs.ReadFile(luaFiles, "lua/"+name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	unbounded := []byte(`
-- Test sidecar: the client sends without its own field checks.
function SendAddonMessage(prefix, text, channel, target)
    local box = addonhost_outbox()
    box[#box + 1] = { out = "addon", prefix = tostring(prefix), text = tostring(text), channel = tostring(channel) }
end
`)
	hostScripts = fstest.MapFS{
		"lua/host.lua": {Data: read("host.lua")},
		"lua/json.lua": {Data: read("json.lua")},
		"lua/mock.lua": {Data: append(read("mock.lua"), unbounded...)},
	}
	src := `
SLASH_BIG1 = "/big"
SlashCmdList["BIG"] = function()
    local big = string.rep("A", 15 * 1024 * 1024)
    for i = 1, 5 do SendAddonMessage("TINY", big, "PARTY") end
end
`
	h := startAddon(t, "Big", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	outs, err := h.Command("/big")
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("an over-cap protocol line answered %v (%d Outs), want the sidecar stopped", err, len(outs))
	}
	if !strings.Contains(err.Error(), "protocol line") {
		t.Fatalf("the error %q does not name the protocol line cap", err)
	}
	if _, _, err := h.State(); err == nil {
		t.Fatal("the sidecar kept its host after writing an over-cap line")
	}
}

// A sidecar that crashes (any abnormal exit) is a dead add-on host, never a daemon error: later calls answer
// ErrStopped with the exit recorded, and nothing panics or blocks.
func TestHostTreatsCrashedSidecarAsStopped(t *testing.T) {
	h := loadFake(t)
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the sidecar's crash was not seen")
	}
	if _, _, err := h.State(); !errors.Is(err, ErrStopped) {
		t.Fatalf("a call to a crashed sidecar answered %v, want ErrStopped", err)
	}
}

// Lua 5.1's pattern matcher recurses on the pattern without a limit: the review's pattern (200000 "a?" items)
// segfaults lua5.1. The add-on's find/match/gmatch/gsub and the shared ones (reached by method syntax) refuse a
// pattern over 512 bytes and a subject over 1 MiB, and the host keeps answering.
//
// Mutations these tests catch: the wrapper only on the add-on's copies (the method route still segfaults or
// spends minutes), and a wrapper with no subject bound.
func TestHostRefusesHugePatterns(t *testing.T) {
	src := `
local subject = string.rep("a", 400000)
local pattern = string.rep("a?", 200000) .. "b"
local ok, err = pcall(string.find, subject, pattern)
print("find " .. tostring(ok) .. " " .. tostring(err))
local ok2, err2 = pcall(function() return ("aaa"):find(pattern) end)
print("method " .. tostring(ok2) .. " " .. tostring(err2))
local ok3, err3 = pcall(function() return (string.rep("a", 1024 * 1024 + 1)):find("a") end)
print("subject " .. tostring(ok3) .. " " .. tostring(err3))
local ok4 = pcall(string.match, "abc", "b")
print("match " .. tostring(ok4))
`
	h := startAddon(t, "Patterns", src)
	if _, _, err := h.Load(World{}, true, nil); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	for _, want := range []string{"find false", "method false", "subject false", "match true"} {
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("chat %q does not hold %q", lines, want)
		}
	}
}

// An error object's __tostring is add-on code. The host must not run it while writing a reply (after drain its
// sends would wait for the next op) and a table error must not reach a field: its text is "<table>" and the send
// the metamethod would make never happens.
//
// Mutations these tests catch: tostring of the error after drain (the "late" send comes back on the next call),
// and a field validation that lets a table through.
func TestHostErrorObjectRunsNoCodeAfterDrain(t *testing.T) {
	src := `
local mt = { __tostring = function()
    SendAddonMessage("TINY", "late", "PARTY")
    return "tostring ran"
end }
SLASH_BAD1 = "/bad"
SlashCmdList["BAD"] = function() error(setmetatable({}, mt)) end
`
	h := startAddon(t, "Bad", src)
	if _, outs, err := h.Load(World{}, true, nil); err != nil || len(outs) != 0 {
		t.Fatalf("load %v %+v", err, outs)
	}
	outs, err := h.Command("/bad")
	if err == nil {
		t.Fatal("the raising command answered without an error")
	}
	if !strings.Contains(err.Error(), "<table>") || strings.Contains(err.Error(), "tostring ran") {
		t.Fatalf("the error text %q is not the safe table form", err)
	}
	if len(outs) != 0 {
		t.Fatalf("the error object's __tostring ran add-on code: %+v", outs)
	}
	_, outs, err = h.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if o.Text == "late" {
			t.Fatalf("a send made by __tostring was performed on the next call: %+v", outs)
		}
	}
}

// A prefix the add-on only sends under is its own, and counts against its share: past the cap the send answers a Lua
// error the add-on can see, and the prefix is not added. The share is the mock's own account of the daemon's cap, so
// an add-on that fills it with sends cannot then register one more, and the daemon refuses none it was told was fine.
//
// Mutations these tests catch: a SendAddonMessage that never counts its prefix (the daemon's row carries the refusal,
// the add-on sees a send that "worked"), and a mock that lets one more register through than the daemon takes.
func TestHostCountsThePrefixesSentUnder(t *testing.T) {
	// The load registers the whole share (32 with nothing reserved), so the first send under a new prefix is over it.
	src := `
for i = 1, 40 do RegisterAddonMessagePrefix(string.format("R%02d", i)) end
SLASH_OVER1 = "/over"
SlashCmdList["OVER"] = function()
    local ok, err = pcall(SendAddonMessage, "S01", "hi", "PARTY")
    print("send " .. tostring(ok) .. " " .. tostring(err))
    print("register " .. tostring(RegisterAddonMessagePrefix("S02")))
end
`
	h := startAddon(t, "Share", src)
	res, _, err := h.Load(World{}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Prefixes) != AddonPrefixShare {
		t.Fatalf("the load registered %d prefixes, want the share %d: %v", len(res.Prefixes), AddonPrefixShare, res.Prefixes)
	}
	if _, err := h.Command("/over"); err == nil {
		t.Fatal("the over-share send answered without an error")
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "send false") || !strings.Contains(joined, "already holds") {
		t.Fatalf("chat %q does not carry the send's own refusal", joined)
	}
	if !strings.Contains(joined, "register false") {
		t.Fatalf("chat %q does not carry the register's refusal", joined)
	}
	if len(st.Prefixes) != AddonPrefixShare {
		t.Fatalf("the add-on's prefixes %v grew past the share", st.Prefixes)
	}
	if !st.Throttled {
		t.Fatal("a send over the prefix share is not marked throttled")
	}
}

// The prefixes the caller registered up front are the add-on's own: they count against its share in the mock, so the
// registrations it makes past them are refused there, the way the daemon refuses them.
//
// Mutation this test catches: a load that seeds only the reserved list (the add-on's own prefixes are not counted,
// so the mock answers true to a registration the daemon then refuses).
func TestHostCountsThePrefixesRegisteredUpFront(t *testing.T) {
	src := `
for i = 1, 40 do RegisterAddonMessagePrefix(string.format("N%02d", i)) end
`
	h := startAddon(t, "Own", src)
	res, _, err := h.LoadWith(World{}, true, nil, []string{"MINE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Prefixes) != AddonPrefixShare {
		t.Fatalf("the load answered %d prefixes, want the share %d (MINE and %d registrations): %v",
			len(res.Prefixes), AddonPrefixShare, AddonPrefixShare-1, res.Prefixes)
	}
	if !slices.Contains(res.Prefixes, "MINE") {
		t.Fatalf("the up-front prefix is not in the list: %v", res.Prefixes)
	}
}
