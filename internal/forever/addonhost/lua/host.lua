-- Headless Forever add-on host: runs a real add-on's Lua on the client mock (lua/mock.lua), driven by the
-- daemon over stdin/stdout, one JSON request per line.
--
--   lua5.1 host.lua
--
-- stdin: {"id":n,"op":"...",...}. File descriptor 3 (never stdout, so nothing the add-on or Lua prints can pass
-- for a frame): one JSON line per reply, {"id":n,"result":...} or {"id":n,"error":"..."}, and before it, while
-- the request runs, one {"out":"addon"|"chat"|"target",...} line per thing the add-on asked the client to do
-- (SendAddonMessage, SendChatMessage, TargetUnit, TargetByName). The daemon performs those on its session. The
-- host exits at the end of stdin.
--
-- The add-on's files come in the load request as text: the daemon reads the .toc and its sources, checks that
-- every one stays inside the add-on's folder, and this host never opens a file of the add-on's own. Each file
-- gets an environment (setfenv) holding only the mocked client API and safe parts of the Lua library (string,
-- table, math, date). os, io, loadfile, loadstring, dofile, require, package and debug stay local to the host, so
-- an add-on cannot write files, read the protocol or load code of its own choosing.
--
-- An add-on is untrusted. Three bounds are its whole reach: the environment (an explicit allow-list, with its
-- own copies of the string functions and no route to the shared string table), text-only chunks (bytecode starts
-- with ESC and is refused before loadstring, and so is a NUL byte), and a send budget enforced in mock.lua (20
-- sends per call, 10 per second, 100 queued), over which the op answers an error and is marked throttled.
--
-- Ops:
--   hello   {}                        the host has read its scripts and is ready
--   load    {world, in_world, addon}  run the addon's files (name, title, version, files:[{name,source}]), fire
--                                     ADDON_LOADED, then PLAYER_LOGIN and PLAYER_ENTERING_WORLD when the
--                                     character is in world
--   world   {world}                   the units the add-on may ask about (player, target, party, raid); fires
--                                     PARTY_MEMBERS_CHANGED and RAID_ROSTER_UPDATE where the group changed
--   events  {events:[{event,args}]}   fire game events to the frames that registered them
--   tick    {ms}                      advance GetTime() and run OnUpdate of every visible frame once
--   cmd     {line}                    type a slash command at the add-on, as the player would
--   state   {}                        what the host and the add-on did

local here = (arg[0]:match("^(.*)/[^/]*$")) or "."
local json = dofile(here .. "/json.lua")

local proto = io.open("/dev/fd/3", "w")
if not proto then
    io.stderr:write("host.lua: file descriptor 3 (the protocol) is not open\n")
    os.exit(2)
end

-- Everything the host itself calls is captured here, before any add-on code runs: the add-on's environment gets
-- its own copies, and the string metatable is closed to it, so replacing string.format or gsub from the add-on
-- cannot change a frame or a result the host encodes.
local encode, decode, jsonArray = json.encode, json.decode, json.array
local pcall_, select_, tostring_, tonumber_, type_ = pcall, select, tostring, tonumber, type
local ipairs_, pairs_ = ipairs, pairs
local concat = table.concat
local tremove = table.remove
local byte_, find_ = string.byte, string.find
local match_, lower_, gsub_ = string.match, string.lower, string.gsub
local getmetatable_ = getmetatable
local error_ = error

local emit = function(t)
    proto:write(encode(t), "\n")
    proto:flush()
end

dofile(here .. "/mock.lua")

-- string.dump is not part of what an add-on may reach. Removing it from the shared table also hides it from the
-- implicit string method lookup ("").dump, which an add-on could otherwise read through the string metatable.
string.dump = nil

-- ── the add-on's environment ────────────────────────────────────────────────
-- E is the add-on's global table: the mocked API, the safe Lua library, and whatever the add-on defines.
local E = {}
local named = addonhost_named()

-- What a 1.60.1 add-on sees of the client. Everything else stays in the host.
local WOW_API = { "CreateFrame", "UIParent", "Minimap", "UISpecialFrames", "SlashCmdList", "DEFAULT_CHAT_FRAME",
    "GameTooltip", "GetTime", "time", "SendAddonMessage", "RegisterAddonMessagePrefix", "SendChatMessage",
    "TargetUnit", "TargetByName", "UnitExists", "UnitName", "UnitIsPlayer", "UnitIsDead", "UnitIsGhost",
    "UnitIsConnected", "UnitCanAttack", "UnitIsEnemy", "UnitLevel", "UnitClass", "GetNumPartyMembers",
    "GetNumRaidMembers" }
local LUA_SAFE = { "assert", "error", "ipairs", "next", "pairs", "pcall", "xpcall", "select", "tonumber", "tostring",
    "type", "unpack", "rawequal", "rawget", "rawset", "setmetatable" }
local function copyTable(t)
    local out = {}
    for k, v in pairs_(t) do out[k] = v end
    return out
end
for _, name in ipairs_(WOW_API) do E[name] = _G[name] end
for _, name in ipairs_(LUA_SAFE) do E[name] = _G[name] end
-- getmetatable hands back the metatable of the value it names; getmetatable("") would hand the add-on the host's
-- own string table (so it could replace string.format and forge frames). Strings answer nil; tables still work.
E.getmetatable = function(v)
    if type_(v) == "string" then return nil end
    return getmetatable_(v)
end
E.string, E.table, E.math = copyTable(string), copyTable(table), copyTable(math)
E.string.dump = nil
-- The client's short names for the same functions.
E.getn, E.tinsert, E.tremove = table.getn, table.insert, table.remove
E.strfind, E.strsub, E.strlen, E.strlower, E.strupper, E.strrep = string.find, string.sub, string.len,
    string.lower, string.upper, string.rep
E.format, E.gsub, E.gfind = string.format, string.gsub, string.gfind or string.gmatch
E.floor, E.ceil, E.abs, E.min, E.max, E.mod, E.random = math.floor, math.ceil, math.abs, math.min, math.max,
    math.mod or math.fmod, math.random
function E.date(fmt, t)
    if fmt ~= nil and type_(fmt) ~= "string" then error("bad argument #1 to 'date' (string expected)", 2) end
    if t ~= nil and type_(t) ~= "number" then error("bad argument #2 to 'date' (number expected)", 2) end
    return os.date(fmt, t)
end
-- print goes to the chat frame, never to the protocol.
function E.print(...)
    local parts = {}
    for i = 1, select_("#", ...) do parts[#parts + 1] = tostring_((select_(i, ...))) end
    addonhost_chat(concat(parts, " "), "print")
end
-- A frame the add-on named is a global, as in the client.
setmetatable(E, { __index = function(_, k) return named[k] end })
E._G = E

-- ── chunks are text only ───────────────────────────────────────────────────
-- Precompiled Lua 5.1 bytecode starts with ESC: it is valid UTF-8 and survives the JSON trip, and 5.1's bytecode is
-- a known way out of the sandbox to native code. lua5.1's loadstring has no mode argument, so the check is here,
-- on the bytes, and the daemon checks the same bytes before it sends a file.
local function loadChunk(source, name)
    if type_(source) ~= "string" then
        error_("cannot load " .. name .. ": the source is not text", 0)
    end
    if #source > 0 and byte_(source, 1) == 27 then
        error_("cannot load " .. name .. ": precompiled Lua bytecode is refused, an add-on loads from text only", 0)
    end
    if find_(source, "\0", 1, true) then
        error_("cannot load " .. name .. ": the source holds a NUL byte and is not text", 0)
    end
    return loadstring(source, "@" .. name)
end

-- ── bounded logs ────────────────────────────────────────────────────────────
local errors = {}
local function recordError(where, err)
    errors[#errors + 1] = { where = where, error = tostring_(err) }
    while #errors > 20 do tremove(errors, 1) end
end

-- ── events ──────────────────────────────────────────────────────────────────
-- fireEvent hands the event to every visible frame that registered it, in creation order, with the client's
-- handler globals (this, event, arg1..arg9) set in the add-on's own environment. A handler error is recorded and
-- the dispatch goes on: one broken add-on frame must not take the client (or the daemon) down.
local CHAT_FRAME_EVENTS = { CHAT_MSG_SYSTEM = true, CHAT_MSG_WHISPER = true, CHAT_MSG_ADDON = true }
local function fireEvent(ev, args)
    args = args or {}
    E.event, E.this = ev, nil
    for i = 1, 9 do E["arg" .. i] = args[i] end
    for _, f in ipairs_(addonhost_frames()) do
        if f.events[ev] and addonhost_visible(f) then
            local cb = f.scripts.OnEvent
            if cb then
                E.this = f
                local ok, err = pcall_(cb, f, ev)
                if not ok then recordError("OnEvent " .. ev .. " (" .. tostring_(f.name or f.kind) .. ")", err) end
            end
        end
    end
    if CHAT_FRAME_EVENTS[ev] then
        -- The chat frame's own handler: what reaches it is what the player reads in chat. The add-on may replace
        -- ChatFrame_OnEvent (this client has no message filters), so the sandbox's copy is the one called.
        local cb = E.ChatFrame_OnEvent
        if type_(cb) == "function" then
            local ok, err = pcall_(cb)
            if not ok then recordError("ChatFrame_OnEvent " .. ev, err) end
        end
    end
end

-- ── the world the add-on asks about ────────────────────────────────────────
local lastGroup = nil
local function groupKey(w)
    local parts = {}
    for _, u in ipairs_(w.party or {}) do parts[#parts + 1] = tostring_(u.guid or u.name) end
    parts[#parts + 1] = "|"
    for _, u in ipairs_(w.raid or {}) do parts[#parts + 1] = tostring_(u.guid or u.name) end
    return concat(parts, ",")
end

local function setWorld(world)
    if world == nil or world == json.null then world = {} end
    addonhost_set_world(world)
    local w = addonhost_world()
    local key = groupKey(w)
    if lastGroup ~= nil and key ~= lastGroup then
        fireEvent("PARTY_MEMBERS_CHANGED", {})
        if w.raid and #w.raid > 0 then fireEvent("RAID_ROSTER_UPDATE", {}) end
    end
    lastGroup = key
end

-- ── the clock ──────────────────────────────────────────────────────────────
local clock = 1

-- ── slash commands ─────────────────────────────────────────────────────────
-- A player's line: the command word maps to a SlashCmdList entry through the SLASH_* globals the add-on sets.
local function runSlash(line)
    local word, msg = match_(line or "", "^%s*/(%S+)%s*(.*)$")
    if not word then word, msg = match_(line or "", "^%s*(%S+)%s*(.*)$") end
    if not word then error("empty command", 0) end
    local want = lower_(word)
    for k, v in pairs_(E) do
        local token = match_(k, "^SLASH_(.+)$")
        if token and type_(v) == "string" then
            local wire = lower_(gsub_(v, "^/", ""))
            if wire == want then
                local handler = E.SlashCmdList and E.SlashCmdList[gsub_(token, "%d+$", "")]
                if type_(handler) == "function" then
                    handler(msg or "")
                    return
                end
            end
        end
    end
    error("no slash command '" .. word .. "' is registered", 0)
end

-- ── ops ─────────────────────────────────────────────────────────────────────
local ops = {}
local loaded = false
local addonName, title, version = "Addon", nil, ""
local files = {}

function ops.hello() return {} end

function ops.load(p)
    if loaded then error("the add-on is already loaded", 0) end
    local addon = p.addon or {}
    addonName = addon.name or "Addon"
    title = addon.title
    version = addon.version or ""
    setWorld(p.world)
    for _, file in ipairs_(addon.files or {}) do
        local name = tostring_(file.name)
        local chunk, err = loadChunk(tostring_(file.source), name)
        if not chunk then error("cannot load " .. name .. ": " .. tostring_(err), 0) end
        setfenv(chunk, E)
        local ok, rerr = pcall_(chunk)
        if not ok then error(name .. " raised while loading: " .. tostring_(rerr), 0) end
        files[#files + 1] = name
    end
    loaded = true
    fireEvent("ADDON_LOADED", { addonName })
    if p.in_world then
        fireEvent("PLAYER_LOGIN", {})
        fireEvent("PLAYER_ENTERING_WORLD", {})
    end
    return { name = addonName, title = title or addonName, version = version, files = jsonArray(files),
        prefixes = jsonArray(addonhost_prefixes()) }
end

function ops.world(p)
    if not loaded then error("the add-on is not loaded", 0) end
    setWorld(p.world)
    return {}
end

function ops.events(p)
    if not loaded then error("the add-on is not loaded", 0) end
    for _, e in ipairs_(p.events or {}) do fireEvent(e.event, e.args) end
    return {}
end

function ops.tick(p)
    if not loaded then error("the add-on is not loaded", 0) end
    local ms = tonumber_(p.ms) or 100
    if ms < 0 then ms = 0 end
    local dt = ms / 1000
    clock = clock + dt
    addonhost_set_clock(clock)
    for _, f in ipairs_(addonhost_frames()) do
        local cb = f.scripts.OnUpdate
        if cb and addonhost_visible(f) then
            local ok, err = pcall_(cb, f, dt)
            if not ok then recordError("OnUpdate " .. tostring_(f.name or f.kind), err) end
        end
    end
    return { clock = clock }
end

function ops.cmd(p)
    if not loaded then error("the add-on is not loaded", 0) end
    runSlash(p.line or "")
    return {}
end

function ops.state()
    return { clock = clock, loaded = loaded, name = addonName, version = version, files = jsonArray(files),
        prefixes = jsonArray(addonhost_prefixes()), errors = jsonArray(errors),
        chat = jsonArray(addonhost_chat_log()), frames = #addonhost_frames() }
end

-- ── main loop ───────────────────────────────────────────────────────────────
local outbox = addonhost_outbox()
local function drain()
    for i = 1, #outbox do emit(outbox[i]) end
    for i = #outbox, 1, -1 do outbox[i] = nil end
end

-- The send budget's error, as the caller sees it: a call over it answers an error (with throttled set, so the
-- daemon marks the add-on and keeps the host), never a silent truncation.
local function throttledError()
    return "the add-on is over its send budget this call: the client refused the rest of its sends"
end

for line in io.lines() do
    local ok, req = pcall_(decode, line)
    if not ok or type_(req) ~= "table" then
        emit({ error = "host.lua: bad request line" })
    else
        addonhost_begin_op()
        local fn = ops[req.op]
        if not fn then
            emit({ id = req.id, error = "host.lua: unknown op " .. tostring_(req.op) })
        else
            local invoked, res = pcall_(fn, req)
            drain()
            if not invoked then
                emit({ id = req.id, error = tostring_(res), throttled = addonhost_throttled() or nil })
            elseif addonhost_throttled() then
                emit({ id = req.id, error = throttledError(), throttled = true })
            else
                emit({ id = req.id, result = res })
            end
        end
    end
end
