-- Headless Forever add-on host: runs a real add-on's Lua on the client mock (lua/mock.lua), driven by the
-- daemon over stdin/stdout, one JSON request per line.
--
--   lua5.1 host.lua <addon dir> <toc file>
--
-- stdin: {"id":n,"op":"...",...}. File descriptor 3 (never stdout, so nothing the add-on or Lua prints can pass
-- for a frame): one JSON line per reply, {"id":n,"result":...} or {"id":n,"error":"..."}, and before it, while
-- the request runs, one {"out":"addon"|"chat"|"target",...} line per thing the add-on asked the client to do
-- (SendAddonMessage, SendChatMessage, TargetUnit, TargetByName). The daemon performs those on its session. The
-- host exits at the end of stdin.
--
-- The add-on runs sandboxed: each of its files gets an environment (setfenv) holding only the mocked client API
-- and safe parts of the Lua library (string, table, math, date). os, io, loadfile, dofile, require, package and
-- debug stay local to the host, so an add-on cannot write files or read the protocol.
--
-- Ops:
--   hello   {}                        the host has read its scripts and is ready
--   load    {world, in_world}         load the .toc files, fire ADDON_LOADED, then PLAYER_LOGIN and
--                                     PLAYER_ENTERING_WORLD when the character is in world
--   world   {world}                   the units the add-on may ask about (player, target, party, raid); fires
--                                     PARTY_MEMBERS_CHANGED and RAID_ROSTER_UPDATE where the group changed
--   events  {events:[{event,args}]}   fire game events to the frames that registered them
--   tick    {ms}                      advance GetTime() and run OnUpdate of every visible frame once
--   cmd     {line}                    type a slash command at the add-on, as the player would
--   state   {}                        what the host and the add-on did

local here = (arg[0]:match("^(.*)/[^/]*$")) or "."
local json = dofile(here .. "/json.lua")
local addonDir = arg[1]
local tocName = arg[2]
if not addonDir or not tocName then
    io.stderr:write("usage: lua5.1 host.lua <addon dir> <toc file>\n")
    os.exit(2)
end

local proto = io.open("/dev/fd/3", "w")
if not proto then
    io.stderr:write("host.lua: file descriptor 3 (the protocol) is not open\n")
    os.exit(2)
end
local function emit(t)
    proto:write(json.encode(t), "\n")
    proto:flush()
end

dofile(here .. "/mock.lua")

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
    "type", "unpack", "rawequal", "rawget", "rawset", "setmetatable", "getmetatable" }
local function copyTable(t)
    local out = {}
    for k, v in pairs(t) do out[k] = v end
    return out
end
for _, name in ipairs(WOW_API) do E[name] = _G[name] end
for _, name in ipairs(LUA_SAFE) do E[name] = _G[name] end
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
    if fmt ~= nil and type(fmt) ~= "string" then error("bad argument #1 to 'date' (string expected)", 2) end
    if t ~= nil and type(t) ~= "number" then error("bad argument #2 to 'date' (number expected)", 2) end
    return os.date(fmt, t)
end
-- print goes to the chat frame, never to the protocol.
function E.print(...)
    local parts = {}
    for i = 1, select("#", ...) do parts[#parts + 1] = tostring((select(i, ...))) end
    addonhost_chat(table.concat(parts, " "), "print")
end
-- A frame the add-on named is a global, as in the client.
setmetatable(E, { __index = function(_, k) return named[k] end })
E._G = E

-- ── bounded logs ────────────────────────────────────────────────────────────
local errors = {}
local function recordError(where, err)
    errors[#errors + 1] = { where = where, error = tostring(err) }
    while #errors > 20 do table.remove(errors, 1) end
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
    for _, f in ipairs(addonhost_frames()) do
        if f.events[ev] and addonhost_visible(f) then
            local cb = f.scripts.OnEvent
            if cb then
                E.this = f
                local ok, err = pcall(cb, f, ev)
                if not ok then recordError("OnEvent " .. ev .. " (" .. tostring(f.name or f.kind) .. ")", err) end
            end
        end
    end
    if CHAT_FRAME_EVENTS[ev] then
        -- The chat frame's own handler: what reaches it is what the player reads in chat. The add-on may replace
        -- ChatFrame_OnEvent (this client has no message filters), so the sandbox's copy is the one called.
        local cb = E.ChatFrame_OnEvent
        if type(cb) == "function" then
            local ok, err = pcall(cb)
            if not ok then recordError("ChatFrame_OnEvent " .. ev, err) end
        end
    end
end

-- ── the world the add-on asks about ────────────────────────────────────────
local lastGroup = nil
local function groupKey(w)
    local parts = {}
    for _, u in ipairs(w.party or {}) do parts[#parts + 1] = tostring(u.guid or u.name) end
    parts[#parts + 1] = "|"
    for _, u in ipairs(w.raid or {}) do parts[#parts + 1] = tostring(u.guid or u.name) end
    return table.concat(parts, ",")
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
    local word, msg = string.match(line or "", "^%s*/(%S+)%s*(.*)$")
    if not word then word, msg = string.match(line or "", "^%s*(%S+)%s*(.*)$") end
    if not word then error("empty command", 0) end
    local want = string.lower(word)
    for k, v in pairs(E) do
        local token = string.match(k, "^SLASH_(.+)$")
        if token and type(v) == "string" then
            local wire = string.lower(string.gsub(v, "^/", ""))
            if wire == want then
                local handler = E.SlashCmdList and E.SlashCmdList[string.gsub(token, "%d+$", "")]
                if type(handler) == "function" then
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
local addonName = string.match(addonDir, "([^/]+)$") or "Addon"
local version, files = nil, {}

function ops.hello() return {} end

function ops.load(p)
    if loaded then error("the add-on is already loaded", 0) end
    local toc = io.open(addonDir .. "/" .. tocName, "r")
    if not toc then error("cannot read " .. tocName .. " in " .. addonDir, 0) end
    local title
    for line in toc:lines() do
        line = string.gsub(line, "\r", "")
        local v = string.match(line, "^## Version:%s*(.-)%s*$")
        if v and v ~= "" then version = v end
        local t = string.match(line, "^## Title:%s*(.-)%s*$")
        if t and t ~= "" then title = t end
        if not string.find(line, "^#") and string.find(line, "%.lua%s*$") then
            files[#files + 1] = (string.gsub(line, "%s+$", ""))
        end
    end
    toc:close()
    setWorld(p.world)
    for _, file in ipairs(files) do
        file = string.gsub(file, "\\", "/")
        if string.find(file, "^/") or string.find(file, "%.%.") then
            error("the .toc names a file outside the add-on: " .. file, 0)
        end
        local chunk, err = loadfile(addonDir .. "/" .. file)
        if not chunk then error("cannot load " .. file .. ": " .. tostring(err), 0) end
        setfenv(chunk, E)
        local ok, rerr = pcall(chunk)
        if not ok then error(file .. " raised while loading: " .. tostring(rerr), 0) end
    end
    loaded = true
    fireEvent("ADDON_LOADED", { addonName })
    if p.in_world then
        fireEvent("PLAYER_LOGIN", {})
        fireEvent("PLAYER_ENTERING_WORLD", {})
    end
    return { name = addonName, title = title or addonName, version = version or "", files = json.array(files),
        prefixes = json.array(addonhost_prefixes()) }
end

function ops.world(p)
    if not loaded then error("the add-on is not loaded", 0) end
    setWorld(p.world)
    return {}
end

function ops.events(p)
    if not loaded then error("the add-on is not loaded", 0) end
    for _, e in ipairs(p.events or {}) do fireEvent(e.event, e.args) end
    return {}
end

function ops.tick(p)
    if not loaded then error("the add-on is not loaded", 0) end
    local ms = tonumber(p.ms) or 100
    if ms < 0 then ms = 0 end
    local dt = ms / 1000
    clock = clock + dt
    addonhost_set_clock(clock)
    for _, f in ipairs(addonhost_frames()) do
        local cb = f.scripts.OnUpdate
        if cb and addonhost_visible(f) then
            local ok, err = pcall(cb, f, dt)
            if not ok then recordError("OnUpdate " .. tostring(f.name or f.kind), err) end
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
    return { clock = clock, loaded = loaded, name = addonName, version = version or "", files = json.array(files),
        prefixes = json.array(addonhost_prefixes()), errors = json.array(errors),
        chat = json.array(addonhost_chat_log()), frames = #addonhost_frames() }
end

-- ── main loop ───────────────────────────────────────────────────────────────
local outbox = addonhost_outbox()
local function drain()
    for i = 1, #outbox do emit(outbox[i]) end
    for i = #outbox, 1, -1 do outbox[i] = nil end
end

for line in io.lines() do
    local ok, req = pcall(json.decode, line)
    if not ok or type(req) ~= "table" then
        emit({ error = "host.lua: bad request line" })
    else
        local fn = ops[req.op]
        if not fn then
            emit({ id = req.id, error = "host.lua: unknown op " .. tostring(req.op) })
        else
            local invoked, res = pcall(fn, req)
            drain()
            if invoked then
                emit({ id = req.id, result = res })
            else
                emit({ id = req.id, error = tostring(res) })
            end
        end
    end
end
