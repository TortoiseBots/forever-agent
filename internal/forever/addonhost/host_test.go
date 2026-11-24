package addonhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAddon is the test's tiny add-on: it registers a prefix at load, answers the login with one frame, prints
// every add-on frame it gets, raises on the frame whose text is "boom", and sends on its slash command.
//
// Mutations these tests catch: returning a frame without passing it to the sink (TestHostLoadsAndSends sees no
// Out), a dispatch without pcall (TestHostContainsHandlerErrors loses the following frame), a Tick that runs
// every frame regardless of visibility (TestHostTick records the hidden frame's error), and a Call that does not
// kill a stuck child (TestHostKillsStuckAddon hangs instead of answering).
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
func startAddon(t *testing.T, title, src string, sink Sink) *Host {
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
	h, err := Start(dir, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// loadFake starts the host on fakeAddon and loads it in world.
func loadFake(t *testing.T, sink Sink) *Host {
	t.Helper()
	h := startAddon(t, "Tiny", fakeAddon, sink)
	if _, _, err := h.Load(World{Player: Unit{Name: "Tester", Player: true, Online: true}}, true); err != nil {
		t.Fatal(err)
	}
	return h
}

// The .toc is read, the add-on's registered prefix is reported, and the frame it sent at PLAYER_LOGIN came back
// as an Out the caller performs.
func TestHostLoadsAndSends(t *testing.T) {
	var done []Out
	h := startAddon(t, "Tiny", fakeAddon, func(o Out) error {
		done = append(done, o)
		return nil
	})
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
		t.Fatalf("offs from the login %+v", outs)
	}
	if len(done) != 1 || done[0].Error != "" {
		t.Fatalf("sink saw %+v", done)
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
	h := loadFake(t, nil)
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
	h := loadFake(t, nil)
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
	h := loadFake(t, nil)
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
	h := startAddon(t, "Tick", src, nil)
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
	h := startAddon(t, "Sandbox", src, nil)
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
	h := startAddon(t, "Spin", src, nil)
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

// Start refuses a folder without a .toc and one with several, by name.
func TestStartRequiresOneTOC(t *testing.T) {
	requireLua(t)
	if _, err := Start(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "no .toc") {
		t.Fatalf("empty folder: %v", err)
	}
	dir := t.TempDir()
	for _, name := range []string{"A.toc", "B.toc"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("X.lua\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Start(dir, nil); err == nil || !strings.Contains(err.Error(), "A.toc, B.toc") {
		t.Fatalf("two .toc files: %v", err)
	}
}
