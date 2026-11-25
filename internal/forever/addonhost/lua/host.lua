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
-- An add-on is untrusted. These bounds are its whole reach: the environment (an explicit allow-list, with its
-- own copies of the string functions and no route to the shared string table), text-only chunks (bytecode starts
-- with ESC and is refused before loadstring, and so is a NUL byte), a send budget enforced in mock.lua (20
-- sends per call, 10 per second, 100 queued), the server's own field limits on every send (a prefix of 1..16
-- bytes, an add-on text of 255, a chat line of 511, a target of 64, at most 64 prefixes), the pattern caps below,
-- and the reserved prefixes the session holds. Over the budget or a field limit the op answers an error and is
-- marked throttled; the daemon stops a sidecar that writes a protocol line over its own cap.
--
-- Ops:
--   hello   {}                        the host has read its scripts and is ready
--   load    {world, in_world, addon, reserved}
--                                     run the addon's files (name, title, version, files:[{name,source}]); reserved
--                                     lists the session's prefixes, which the add-on may not use; fire
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
local gmatch_, sub_ = string.gmatch, string.sub
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

-- ── bounded text ────────────────────────────────────────────────────────────
-- Everything the host writes back about the add-on (an error, a chat line, a file name, a result field) is
-- bounded by the size it takes on the wire, not just in bytes: json.lua escapes a control byte, a quote or a
-- backslash to six characters. A state or load reply therefore stays under the daemon's protocol cap whatever the
-- add-on printed or named, instead of being killed as if it had misbehaved.
local MAX_TEXT = 2048
local MAX_IDENTITY = 256
local MAX_FILES, MAX_FILES_JSON = 64, 8192

-- jsonLen is the encoded size of s, conservatively (every escaped byte counts six; the short escapes are two).
local function jsonLen(s) return #gsub_(s, '[%c"\\]', "XXXXXX") end

local function safeText(v)
    local t = type_(v)
    if t == "string" then return v end
    if t == "number" or t == "boolean" then return tostring_(v) end
    -- A table's __tostring is add-on code: it is never called here, so an error object cannot run anything (or
    -- answer a table) while the host writes its reply.
    return "<" .. t .. ">"
end

local function boundedText(v, limit)
    local s = safeText(v)
    limit = limit or MAX_TEXT
    if #s > limit then return sub_(s, 1, limit) .. "..." end
    return s
end

-- ── bounded patterns ────────────────────────────────────────────────────────
-- Lua 5.1's pattern matcher recurses on the pattern without a limit, so a pattern such as 200000 "a?" items can
-- overflow the C stack and kill the interpreter. The add-on is untrusted, so its pattern functions (and the
-- shared ones, which method syntax reaches through the string metatable) refuse a pattern over MAX_PATTERN bytes
-- and a subject over MAX_SUBJECT bytes.
local MAX_PATTERN, MAX_SUBJECT = 512, 1024 * 1024

local function boundPattern(name, fn, findPlain)
    return function(s, p, a, b)
        if type_(s) == "string" and #s > MAX_SUBJECT then
            error_(name .. ": the subject is over " .. MAX_SUBJECT .. " bytes", 2)
        end
        if not (findPlain and b) and type_(p) == "string" and #p > MAX_PATTERN then
            error_(name .. ": the pattern is over " .. MAX_PATTERN .. " bytes", 2)
        end
        return fn(s, p, a, b)
    end
end
string.find = boundPattern("string.find", find_, true)
string.match = boundPattern("string.match", match_)
string.gmatch = boundPattern("string.gmatch", gmatch_)
string.gfind = string.gmatch
string.gsub = boundPattern("string.gsub", gsub_)

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
-- The error list is what addon_state answers: bounded per entry and in encoded size, like the chat log.
local MAX_ERRORS, MAX_ERRORS_TOTAL = 20, 8192
local errors, errorBytes = {}, 0
local function dropError()
    local old = tremove(errors, 1)
    if old then errorBytes = errorBytes - jsonLen(old.where) - jsonLen(old.error) - 24 end
end
local function recordError(where, err)
    where, err = boundedText(where), boundedText(err)
    local size = jsonLen(where) + jsonLen(err) + 24
    errors[#errors + 1] = { where = where, error = err }
    errorBytes = errorBytes + size
    while #errors > MAX_ERRORS do dropError() end
    while errorBytes > MAX_ERRORS_TOTAL and #errors > 1 do dropError() end
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
                if not ok then recordError("OnEvent " .. ev .. " (" .. boundedText(f.name or f.kind) .. ")", err) end
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
    error("no slash command '" .. boundedText(word) .. "' is registered", 0)
end

-- ── ops ─────────────────────────────────────────────────────────────────────
local ops = {}
local loaded = false
local addonName, title, version = "Addon", nil, ""
local files = {}

-- boundedFiles is the .toc's file list as the reports carry it: at most MAX_FILES names inside MAX_FILES_JSON
-- encoded bytes, then a "..." marker. A folder with hundreds of long names (legal: the .toc and the sources are
-- 8 MiB in all) can therefore not push a load or state reply over the daemon's protocol cap.
local function boundedFiles()
    local out, total = {}, 0
    for _, name in ipairs_(files) do
        local n = jsonLen(name) + 4
        if #out >= MAX_FILES or total + n > MAX_FILES_JSON then
            out[#out + 1] = "..."
            break
        end
        out[#out + 1] = name
        total = total + n
    end
    return jsonArray(out)
end

function ops.hello() return {} end

function ops.load(p)
    if loaded then error("the add-on is already loaded", 0) end
    local addon = p.addon or {}
    addonName = boundedText(addon.name or "Addon", MAX_IDENTITY)
    if addon.title ~= nil then title = boundedText(addon.title, MAX_IDENTITY) end
    version = boundedText(addon.version or "", MAX_IDENTITY)
    setWorld(p.world)
    -- The prefixes this session held before the add-on started are the session's, not the add-on's.
    addonhost_set_reserved(p.reserved)
    for _, file in ipairs_(addon.files or {}) do
        local name = boundedText(file.name)
        local chunk, err = loadChunk(tostring_(file.source), name)
        if not chunk then error("cannot load " .. name .. ": " .. boundedText(err), 0) end
        setfenv(chunk, E)
        local ok, rerr = pcall_(chunk)
        if not ok then error(name .. " raised while loading: " .. boundedText(rerr), 0) end
        files[#files + 1] = name
    end
    loaded = true
    fireEvent("ADDON_LOADED", { addonName })
    if p.in_world then
        fireEvent("PLAYER_LOGIN", {})
        fireEvent("PLAYER_ENTERING_WORLD", {})
    end
    return { name = addonName, title = title or addonName, version = version, files = boundedFiles(),
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
            if not ok then recordError("OnUpdate " .. boundedText(f.name or f.kind), err) end
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
    return { clock = clock, loaded = loaded, name = addonName, version = version, files = boundedFiles(),
        prefixes = jsonArray(addonhost_prefixes()), errors = jsonArray(errors),
        chat = jsonArray(addonhost_chat_log()), frames = #addonhost_frames() }
end

-- ── main loop ───────────────────────────────────────────────────────────────
-- Every field the daemon may perform is a string, and the object kind is one this client does: an entry that fails
-- that (a table in a field, or a kind nobody asked for) is dropped instead of being encoded.
local OUT_KINDS = { addon = true, chat = true, target = true }
local function outField(v)
    if v == nil then return nil end
    if type_(v) == "string" then return v end
    return "<" .. type_(v) .. ">"
end
local outbox = addonhost_outbox()
local function drain()
    for i = 1, #outbox do
        local e = outbox[i]
        if type_(e) == "table" then
            local kind = outField(e.out)
            if type_(e.out) == "string" and OUT_KINDS[kind] then
                emit({ out = kind, prefix = outField(e.prefix), text = outField(e.text),
                    channel = outField(e.channel), type = outField(e.type), to = outField(e.to),
                    name = outField(e.name) })
            end
        end
        outbox[i] = nil
    end
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
            emit({ id = req.id, error = "host.lua: unknown op " .. boundedText(req.op) })
        else
            local invoked, res = pcall_(fn, req)
            -- The failure text is made here, before drain, with a type check only: an error object's __tostring is
            -- add-on code, and running it after drain would leave its sends in the outbox for the next op.
            local failure = nil
            if not invoked then failure = boundedText(res) end
            drain()
            if failure then
                emit({ id = req.id, error = failure, throttled = addonhost_throttled() or nil })
            elseif addonhost_throttled() then
                emit({ id = req.id, error = throttledError(), throttled = true })
            else
                emit({ id = req.id, result = res })
            end
        end
    end
end
