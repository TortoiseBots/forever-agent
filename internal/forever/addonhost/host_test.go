package addonhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
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
	if _, _, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true); err != nil {
		t.Fatal(err)
	}
	return h
}

// The .toc is read, the add-on's registered prefix is reported, and the frame it sent at PLAYER_LOGIN came back
// as an Out the caller performs.
func TestHostLoadsAndSends(t *testing.T) {
	h := startAddon(t, "Tiny", fakeAddon)
	res, outs, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true)
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	if _, _, err := h.Load(World{}, true); err != nil {
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
	oldTimeout, oldSlow := CallTimeout, SlowCallLimit
	CallTimeout, SlowCallLimit = 2*time.Second, 10*time.Millisecond
	t.Cleanup(func() { CallTimeout, SlowCallLimit = oldTimeout, oldSlow })
	src := `
local hot = 0
local f = CreateFrame("Frame", "SlowFrame")
f:SetScript("OnUpdate", function()
    for i = 1, 10000000 do hot = hot + i end
end)
`
	h := startAddon(t, "Slow", src)
	if _, _, err := h.Load(World{}, true); err != nil {
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

// The add-on's environment is an explicit allow-list: a test compares every global it can see with the list the
// host means to give it. Adding loadstring, getfenv or any other host function to the sandbox fails here.
func TestHostEnvironmentAllowList(t *testing.T) {
	src := `
local names = {}
for k in pairs(_G) do names[#names + 1] = tostring(k) end
table.sort(names)
print(table.concat(names, "\n"))
`
	h := startAddon(t, "Env", src)
	if _, _, err := h.Load(World{}, true); err != nil {
		t.Fatal(err)
	}
	st, _, err := h.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Chat) != 1 {
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
	_, outs, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true)
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
