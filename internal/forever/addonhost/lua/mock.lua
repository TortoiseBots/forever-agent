-- Minimal Forever (Classic 1.60.1) client API for running a real add-on headless.
--
-- Only the surface an add-on touches while it loads and while its frames run: the widget objects, event
-- registration and dispatch, the chat frame, the add-on message functions and the unit functions over the world
-- the host pushes. Nothing here talks to a server: SendAddonMessage, SendChatMessage, TargetUnit and TargetByName
-- record the request for the Go host, which performs it on the session (see lua/host.lua).
--
-- The add-on's environment is built in host.lua from the globals defined here, so what an add-on can reach is
-- exactly this file's public functions plus the safe half of the Lua library.

local frames = {}      -- every frame, in creation order: events go to them in that order
local named = {}       -- frames created with a name, visible to the add-on as globals
local outbox = {}      -- what the add-on asked the client to do, drained by host.lua
local prefixes = {}    -- the add-on's registered prefixes, in registration order
local reserved = {}    -- prefixes the session holds: the add-on may neither register nor send under them
local gsub = string.gsub

-- ── the field limits ────────────────────────────────────────────────────────
-- The server's own limits, checked here so an oversized field never becomes a protocol line (and never a kept
-- row in the daemon): a prefix is 1..16 bytes and an add-on text at most 255 (the core's
-- HandleChatAddonMessage), a chat line at most 511 (HandleMessage), a whisper target at most 64 characters, and
-- one client may hold at most 64 registered prefixes. Chat text kept for addon_state is bounded per line and in
-- its encoded size (a control byte JSON-escapes to six), so a state reply stays under the daemon's protocol cap
-- whatever the add-on printed.
local MAX_PREFIX, MAX_ADDON_TEXT, MAX_CHAT_TEXT, MAX_TARGET = 16, 255, 511, 64
local MAX_PREFIXES = 64
local MAX_CHAT_LINE, MAX_CHAT_TOTAL, MAX_CHAT_ROWS = 2048, 12288, 40

-- jsonLen is the conservative encoded size of s (each escaped byte counts six, the short escapes are two).
local function jsonLen(s) return #gsub(s, '[%c"\\]', "XXXXXX") end

-- ── the send budget ─────────────────────────────────────────────────────────
-- An add-on is untrusted code. A million sends fit under the sidecar's memory limit and cost no measurable time,
-- and the daemon would then perform them all under the character's name on its session goroutine, so the client
-- refuses a send past its budget: the add-on sees a Lua error, the call answers throttled, and three calls in a
-- row over a limit stop the sidecar (host.go's maxStrikes).
local OUT_PER_CALL, OUT_PER_SECOND, OUT_QUEUE = 20, 10, 100
local sends, second, secondSends = 0, nil, 0
local throttled = false

-- addonhost_set_reserved names the prefixes this session holds (the ones it had before the add-on started and
-- the daemon's own): they belong to the session, so the add-on cannot register them and cannot send under them.
-- They also count against the server's 64-prefix registration cap, which covers the session's whole list.
-- host.lua calls it once, in the load op.
local reserved, reservedCount = {}, 0
function addonhost_set_reserved(list)
    reserved, reservedCount = {}, 0
    for _, p in ipairs(list or {}) do
        p = tostring(p)
        if not reserved[p] then
            reserved[p] = true
            reservedCount = reservedCount + 1
        end
    end
end

-- oversize refuses one field the server would refuse, as an over-limit send: the add-on sees a Lua error, the
-- call answers throttled, and the field never reaches the protocol.
local function oversize(what, limit)
    throttled = true
    error("the client refused the " .. what .. ": over " .. limit .. " is the server's limit", 3)
end

-- addonhost_begin_op resets the per-call counter; host.lua calls it before every op.
function addonhost_begin_op()
    sends, throttled = 0, false
end

-- addonhost_throttled says whether the last op went over the budget.
function addonhost_throttled() return throttled end

local function pushOut(entry)
    local now = os.time()
    if now ~= second then second, secondSends = now, 0 end
    if secondSends >= OUT_PER_SECOND or sends >= OUT_PER_CALL or #outbox >= OUT_QUEUE then
        throttled = true
        error("the client refused the send: the add-on is over its send budget (" .. OUT_PER_CALL .. " per call, "
            .. OUT_PER_SECOND .. " per second, " .. OUT_QUEUE .. " queued)", 3)
    end
    sends, secondSends = sends + 1, secondSends + 1
    outbox[#outbox + 1] = entry
end

function addonhost_outbox() return outbox end
function addonhost_prefixes() return prefixes end
function addonhost_frames() return frames end
function addonhost_named() return named end

-- ── widgets ─────────────────────────────────────────────────────────────────
local methods = {}
local objectMeta = { __index = methods }

local function object(kind, parent)
    local value = { kind = kind, parent = parent, shown = true, enabled = true, scripts = {}, events = {},
        fontStrings = {}, textures = {}, children = {} }
    return setmetatable(value, objectMeta)
end

function methods.SetPoint() end
function methods.ClearAllPoints() end
function methods.SetWidth() end
function methods.SetHeight() end
function methods.SetSize() end
function methods.SetScale() end
function methods.SetAlpha() end
function methods.SetFrameStrata() end
function methods.SetFrameLevel() end
function methods.SetMovable() end
function methods.SetClampedToScreen() end
function methods.SetToplevel() end
function methods.SetID(self, id) self.id = id end
function methods.GetID(self) return self.id end
function methods.Show(self) self.shown = true end
function methods.Hide(self) self.shown = false end
function methods.IsShown(self) return self.shown and true or false end
function methods.Enable(self) self.enabled = true end
function methods.Disable(self) self.enabled = false end
function methods.IsEnabled(self) return self.enabled and true or false end
function methods.SetScript(self, name, fn) self.scripts[name] = fn end
function methods.GetScript(self, name) return self.scripts[name] end
function methods.HookScript(self, name, fn) self.scripts[name] = fn end
function methods.SetText(self, text) self.text = tostring(text or "") end
function methods.GetText(self) return self.text end
function methods.GetName(self) return self.name end
function methods.RegisterEvent(self, event) self.events[event] = true end
function methods.UnregisterEvent(self, event) self.events[event] = nil end
function methods.RegisterForDrag() end
function methods.SetBackdrop() end
function methods.SetNormalTexture() end
function methods.SetHighlightTexture() end
function methods.SetFontString(self, fs) self.fontString = fs; return fs end
function methods.CreateTexture(self)
    local t = object("Texture", self)
    self.textures[#self.textures + 1] = t
    return t
end
function methods.CreateFontString(self)
    local fs = object("FontString", self)
    fs.text = ""
    self.fontStrings[#self.fontStrings + 1] = fs
    return fs
end
function methods.SetMinMaxValues(self, lo, hi) self.minValue, self.maxValue = lo, hi end
function methods.SetValue(self, v) self.value = v end
function methods.GetValue(self) return self.value or 0 end
function methods.SetChecked(self, v) self.checked = v and true or false end
function methods.GetChecked(self) return self.checked and true or false end
function methods.SetAutoHideDelay() end
function methods.AddLine() end
function methods.ClearLines() end
function methods.Click(self) if self.scripts.OnClick then self.scripts.OnClick(self) end end

function CreateFrame(kind, name, parent, template)
    local f = object(kind, parent)
    f.name = name
    if name then named[name] = f end
    frames[#frames + 1] = f
    if parent and parent.children then parent.children[#parent.children + 1] = f end
    if template and GameTooltip then f.tooltip = template end
    return f
end

-- UIParent and its friends every add-on expects to exist.
UIParent = CreateFrame("Frame", "UIParent")
Minimap = CreateFrame("Frame", "Minimap")
UISpecialFrames = {}
SlashCmdList = {}
GameTooltip = CreateFrame("Frame", "GameTooltip")

-- ── the clock, print, the chat frame ────────────────────────────────────────
local now = 1
function GetTime() return now end
function addonhost_set_clock(t) now = t end
function time() return os.time() end

DEFAULT_CHAT_FRAME = { name = "ChatFrame1",
    AddMessage = function(self, msg) addonhost_chat(tostring(msg or ""), "print") end }
function ChatFrame_OnEvent() end

local chatSink = function() end
function addonhost_on_chat(fn) chatSink = fn end

-- ── what the add-on asks the client to do ───────────────────────────────────
function SendAddonMessage(prefix, text, channel, target)
    prefix, text, channel = tostring(prefix or ""), tostring(text or ""), tostring(channel or "")
    if reserved[prefix] then
        error("the client refused the send: the prefix \"" .. prefix .. "\" belongs to the session, not to the add-on", 2)
    end
    if #prefix > MAX_PREFIX then oversize("add-on message prefix", MAX_PREFIX) end
    if #text > MAX_ADDON_TEXT then oversize("add-on message text", MAX_ADDON_TEXT) end
    if #channel > 32 then oversize("add-on message channel", 32) end
    local to = nil
    if target ~= nil then
        to = tostring(target)
        if #to > MAX_TARGET then oversize("add-on message target", MAX_TARGET) end
    end
    pushOut({ out = "addon", prefix = prefix, text = text, channel = channel, to = to })
end

-- RegisterAddonMessagePrefix returns false for a prefix the client cannot register (empty, over the server's
-- 16-byte cap, a prefix this session holds, or one that would take the session's whole list past the server's 64),
-- true otherwise; a prefix already in the list answers true whatever the count. The host registers the list with
-- the session before the add-on sends.
function RegisterAddonMessagePrefix(prefix)
    prefix = tostring(prefix or "")
    if prefix == "" or #prefix > MAX_PREFIX or reserved[prefix] then return false end
    for _, p in ipairs(prefixes) do if p == prefix then return true end end
    if #prefixes + reservedCount >= MAX_PREFIXES then return false end
    prefixes[#prefixes + 1] = prefix
    return true
end

function SendChatMessage(text, chatType, language, target)
    text, chatType = tostring(text or ""), tostring(chatType or "SAY")
    if #text > MAX_CHAT_TEXT then oversize("chat text", MAX_CHAT_TEXT) end
    if #chatType > 32 then oversize("chat type", 32) end
    local to = nil
    if target ~= nil then
        to = tostring(target)
        if #to > MAX_TARGET then oversize("chat target", MAX_TARGET) end
    end
    pushOut({ out = "chat", type = chatType, text = text, to = to })
end

function TargetUnit(unit)
    unit = tostring(unit or "")
    if #unit > MAX_TARGET then oversize("unit name", MAX_TARGET) end
    pushOut({ out = "target", name = unit })
end

function TargetByName(name)
    name = tostring(name or "")
    if #name > MAX_TARGET then oversize("unit name", MAX_TARGET) end
    pushOut({ out = "target", name = name })
end

-- ── units ───────────────────────────────────────────────────────────────────
-- W is the world the host last pushed: player, target (or nil), party (party1..4) and raid (raid1..40).
local W = { player = { name = "Player", player = true }, party = {}, raid = {} }

local function unitInfo(unit)
    unit = string.lower(tostring(unit or ""))
    if unit == "player" then return W.player end
    if unit == "target" then return W.target end
    local who = string.match(unit, "^(%a+)(%d+)$")
    if not who then return nil end
    local index = tonumber(string.match(unit, "(%d+)$"))
    local list = (string.find(unit, "^party") and W.party) or (string.find(unit, "^raid") and W.raid) or nil
    return list and list[index] or nil
end

function UnitExists(unit) return unitInfo(unit) ~= nil end
function UnitName(unit)
    local u = unitInfo(unit)
    return u and u.name or nil
end
function UnitIsPlayer(unit)
    local u = unitInfo(unit)
    return u and u.player and true or false
end
function UnitIsDead(unit)
    local u = unitInfo(unit)
    return u and u.dead and true or false
end
function UnitIsGhost(unit)
    local u = unitInfo(unit)
    return u and u.ghost and true or false
end
function UnitIsConnected(unit)
    local u = unitInfo(unit)
    return u and u.online ~= false or false
end
function UnitCanAttack(_, unit)
    local u = unitInfo(unit)
    return u and u.enemy and true or false
end
UnitIsEnemy = UnitCanAttack
function UnitLevel(unit)
    local u = unitInfo(unit)
    return u and u.level or 0
end
function UnitClass(unit)
    local u = unitInfo(unit)
    if u and u.class then return u.class, u.class_file end
    return nil
end
function GetNumPartyMembers() return table.getn(W.party) end
function GetNumRaidMembers() return table.getn(W.raid) end
function addonhost_set_world(world)
    W = { player = world.player or { name = "Player", player = true }, target = world.target,
        party = world.party or {}, raid = world.raid or {} }
end
function addonhost_world() return W end

-- ── chat ────────────────────────────────────────────────────────────────────
-- The chat log is what addon_state answers: bounded per line, in rows and in encoded size, so an add-on that
-- prints megabytes (or control bytes that escape to six each) cannot make the daemon hold them, or re-encode
-- them in every state reply.
local chatLog, chatBytes = {}, 0
local function dropChat()
    local old = table.remove(chatLog, 1)
    if old then chatBytes = chatBytes - jsonLen(old.text) - 20 end
end
function addonhost_chat(text, source)
    text = tostring(text or "")
    if #text > MAX_CHAT_LINE then text = string.sub(text, 1, MAX_CHAT_LINE) .. "..." end
    chatLog[#chatLog + 1] = { text = text, source = source }
    chatBytes = chatBytes + jsonLen(text) + 20
    while #chatLog > MAX_CHAT_ROWS do dropChat() end
    while chatBytes > MAX_CHAT_TOTAL and #chatLog > 1 do dropChat() end
    chatSink(text, source)
end
function addonhost_chat_log() return chatLog end

-- ── event dispatch ──────────────────────────────────────────────────────────
-- Visible as the client means it: shown, and every ancestor shown. host.lua walks addonhost_frames() with
-- addonhost_visible and sets the handler globals in the add-on's own environment.
local function visible(f)
    while f do
        if f.shown == false then return false end
        f = f.parent
    end
    return true
end
function addonhost_visible(f) return visible(f) end
