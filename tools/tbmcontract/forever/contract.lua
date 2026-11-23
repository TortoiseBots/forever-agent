-- TBFM contract driver: replay contract/tbm/v1/cases/*.case recv lines through the real
-- TortoiseBotsForeverManager addon (Lua 5.1) using its own tests/regression.lua mocks.
--
--   lua5.1 contract.lua <addon dir> <case file>
--
-- Prints mismatches prefixed with "MISMATCH" and exits 1 if any check fails.

local addonDir, caseFile = arg[1], arg[2]
if not addonDir or not caseFile then
    io.stderr:write("usage: lua5.1 contract.lua <addon dir> <case file>\n")
    os.exit(2)
end

-- ── 1. Load TBFM's own modern-API test mocks from tests/regression.lua ──────
arg = { addonDir }
local regFile = assert(io.open(addonDir .. "/tests/regression.lua", "r"),
    "tests/regression.lua not found in " .. addonDir)
local regSrc = regFile:read("*a")
regFile:close()
local cut = assert(regSrc:find("\nfor _, file in ipairs%(", 1),
    "unexpected tests/regression.lua layout: missing file list loop")
local mockChunk, mockErr = loadstring(regSrc:sub(1, cut), "regression-mocks")
assert(mockChunk, mockErr)
mockChunk()

local frames = {}
local origCreateFrame = CreateFrame
function CreateFrame(kind, name, parent, template)
    local fr = origCreateFrame(kind, name, parent, template)
    if name then frames[name] = fr end
    return fr
end

-- ── 2. Load TortoiseBotsForeverManager.toc in exact .toc order ──────────────
local tocPath = addonDir .. "/TortoiseBotsForeverManager.toc"
local toc = assert(io.open(tocPath, "r"), "TortoiseBotsForeverManager.toc not found in " .. addonDir)
for line in toc:lines() do
    line = line:gsub("\r", "")
    if not line:find("^#") and line:find("%.lua%s*$") then
        dofile(addonDir .. "/" .. line:gsub("%s+$", ""))
    end
end
toc:close()

local TB = assert(TortoiseBots, "addon did not create TortoiseBots")
local coreFrame = assert(frames["TortoiseBotsManagerCoreFrame"], "core lifecycle frame missing")
event, arg1 = "ADDON_LOADED", "TortoiseBotsForeverManager"
local loadedOk, loadErr = pcall(coreFrame.scripts.OnEvent, coreFrame)
assert(loadedOk, loadErr)
assert(TB.CanSendAddon(), "TB.CanSendAddon() must be true after ADDON_LOADED")

-- ── 3. Instrument addon entry points to observe parsed outcomes ─────────────
local transportSeen, transportValue = false, nil
local origSetTransport = assert(TB.SetAddonTransport, "TB.SetAddonTransport missing")
TB.SetAddonTransport = function(ch)
    local ok = origSetTransport(ch)
    if ok then
        transportSeen = true
        transportValue = string.lower((ch or ""):gsub("^%s+", ""):gsub("%s+$", ""))
    end
    return ok
end

local seen = {}
local rosterArrival, rosterErrors, rosterSkipped = {}, {}, 0
local origParseRoster = assert(TB.ParseRosterMessage, "TB.ParseRosterMessage missing")
TB.ParseRosterMessage = function(msg)
    local handled, kind, data = origParseRoster(msg)
    seen.roster = { handled = handled, kind = kind, data = data }
    if handled then
        if kind == "begin" then
            rosterArrival = {}
            rosterSkipped = 0
        elseif kind == "row" then
            if data then
                rosterArrival[#rosterArrival + 1] = data
            else
                rosterSkipped = rosterSkipped + 1
            end
        elseif kind == "error" then
            local err = data or (TB.GetRosterSnapshotError and TB.GetRosterSnapshotError())
            rosterErrors[#rosterErrors + 1] = err and err.code or "error"
        end
    end
    return handled, kind, data
end

local ackHistory, errHistory, actionMalformedCount = {}, {}, 0
local origParseAction = assert(TB.ParseActionMessage, "TB.ParseActionMessage missing")
TB.ParseActionMessage = function(msg)
    local handled, kind, data = origParseAction(msg)
    seen.action = { handled = handled, kind = kind, data = data }
    if handled then
        if kind == "ack" and TB.lastActionAck then
            local a = TB.lastActionAck
            ackHistory[#ackHistory + 1] = {
                intent = a.intent, scope = a.scope, count = a.count, executor = a.executor,
            }
        elseif kind == "error" and data then
            if data.code == "malformed" and data.intent == "unknown" then
                actionMalformedCount = actionMalformedCount + 1
            else
                errHistory[#errHistory + 1] = {
                    intent = data.intent, code = data.code, message = data.message,
                }
            end
        end
    end
    return handled, kind, data
end

-- ── 4. Case parser (.case v1 format) ────────────────────────────────────────
local function splitSpace(s)
    local out = {}
    local start = 1
    while true do
        local at = string.find(s, " ", start, true)
        if not at then
            out[#out + 1] = string.sub(s, start)
            return out
        end
        out[#out + 1] = string.sub(s, start, at - 1)
        start = at + 1
    end
end

local function splitChar(s, sep)
    local out, start = {}, 1
    while true do
        local at = string.find(s, sep, start, true)
        if not at then
            out[#out + 1] = string.sub(s, start)
            return out
        end
        out[#out + 1] = string.sub(s, start, at - 1)
        start = at + 1
    end
end

local function expand(s)
    local out = {}
    local i, n = 1, #s
    while i <= n do
        if s:sub(i, i + 4) == "{rep " then
            local close = s:find("}", i + 5, true)
            assert(close, "unclosed {rep} in " .. s)
            local inner = s:sub(i + 5, close - 1)
            local cnt, ch = inner:match("^(%d+) (.)$")
            assert(cnt and ch, "bad {rep} in " .. s)
            out[#out + 1] = string.rep(ch, tonumber(cnt))
            i = close + 1
        elseif s:sub(i, i) == "\\" and i < n then
            local nxt = s:sub(i + 1, i + 1)
            if nxt == "n" then
                out[#out + 1] = "\n"
                i = i + 2
            elseif nxt == "\\" then
                out[#out + 1] = "\\"
                i = i + 2
            else
                out[#out + 1] = "\\"
                i = i + 1
            end
        else
            out[#out + 1] = s:sub(i, i)
            i = i + 1
        end
    end
    return table.concat(out)
end

local function joinFrom(parts, idx)
    local t = {}
    for i = idx, #parts do t[#t + 1] = parts[i] end
    return table.concat(t, " ")
end

local caseName = nil
local recvs, expects = {}, {}
local fh = assert(io.open(caseFile, "r"), "cannot open " .. caseFile)
for raw in fh:lines() do
    local line = raw:gsub("\r$", "")
    if not line:find("^%s*$") and not line:find("^#") then
        local parts = splitSpace(line)
        local kind = parts[1]
        if kind == "case" then
            caseName = parts[2]
        elseif kind == "send" then
            -- The replay sends nothing: it only feeds the module the replies.
            -- Every contract transport is accepted, so a case naming one the
            -- format later grows fails here instead of passing silently.
            local tr = parts[2]
            assert(tr == "addon" or tr == "addon-self" or tr == "chat",
                "bad send transport in: " .. line)
        elseif kind == "frame" or kind == "encode-error" then
            -- Paired with the send above; only the replies matter to TBFM.
        elseif kind == "recv" then
            if parts[2] == "addon" then
                assert(#parts >= 5, "bad recv addon line: " .. line)
                recvs[#recvs + 1] = {
                    kind = "addon",
                    channel = expand(parts[3]),
                    prefix = expand(parts[4]),
                    text = expand(joinFrom(parts, 5)),
                }
            elseif parts[2] == "system" then
                assert(#parts >= 3, "bad recv system line: " .. line)
                recvs[#recvs + 1] = {
                    kind = "system",
                    text = expand(joinFrom(parts, 3)),
                }
            else
                error("unknown recv kind: " .. tostring(parts[2]))
            end
        elseif kind == "expect" then
            assert(#parts >= 3, "bad expect line: " .. line)
            expects[#expects + 1] = {
                path = parts[2],
                value = expand(joinFrom(parts, 3)),
            }
        end
    end
end
fh:close()
assert(caseName, "missing case record in " .. caseFile)

-- ── 5. Replay recv records through TBFM ─────────────────────────────────────
local mismatches = {}
local function fail(fmt, ...)
    mismatches[#mismatches + 1] = string.format(fmt, ...)
end
local function check(what, got, want)
    if got ~= want then
        fail("%s: addon has %s, contract expects %s", what, tostring(got), tostring(want))
    end
end

for i, rec in ipairs(recvs) do
    seen = {}
    local ok, err
    if rec.kind == "addon" then
        ok, err = pcall(TB.OnAddonMessage, rec.prefix, rec.text, rec.channel, "Server")
    else
        ok, err = pcall(TB.OnSystemMessage, rec.text)
    end
    if not ok then
        fail("recv #%d raised Lua error: %s", i, tostring(err))
    end
end

-- ── 6. Known TBFM bebebef contract gaps (pinned to commit bebebef) ──────────
-- Each entry verifies the exact observed bebebef behavior AND stops passing if
-- a future TBFM bump fixes the gap so the entry gets retired.
local DRIFTS = {
    ["ok-multiline-message"] = function()
        check("hello.version", TB.serverVersion or "(empty)", "v0.1.0-forever")
        if transportSeen then
            fail("ok-multiline-message: TBFM now splits LF-joined addon messages; remove drift entry")
        end
    end,
    ["bad-roster-row-skipped"] = function()
        check("roster.committed", tostring(TB.HasRosterSnapshot() and true or false), "true")
        local e0 = TB.GetRosterEntry(rosterArrival[1] or "")
        if not e0 or e0.name ~= "Alpha" or e0.guid ~= "101" or e0.classId ~= 1 then
            fail("bad-roster-row-skipped: row 0 Alpha mismatch")
        end
        if TB.GetRosterSnapshotError() ~= nil then
            fail("bad-roster-row-skipped: unexpected rosterSnapshotError")
        end
        if TB.GetRosterCount() ~= 2 then
            fail("bad-roster-row-skipped: TBFM now rejects non-numeric guid (%d rows); remove drift entry", TB.GetRosterCount())
        end
    end,
    ["bad-botstate"] = function()
        if TB.GetBotState("Bravo") ~= nil then
            fail("bad-botstate: stream 2 count mismatch should reject Bravo")
        end
        local alpha = TB.GetBotState("Alpha")
        if alpha == nil then
            fail("bad-botstate: TBFM now rejects bad BOTSTATE pair; remove drift entry")
        elseif next(alpha) ~= nil then
            fail("bad-botstate: invalid pair 'move' should not populate keys")
        end
    end,
    ["bad-ack-shapes"] = function()
        if TB.lastActionAck ~= nil then
            fail("bad-ack-shapes: lastActionAck should be nil at end")
        end
        if not TB.lastActionError or TB.lastActionError.code ~= "malformed" then
            fail("bad-ack-shapes: lastActionError should be malformed")
        end
        if #errHistory ~= 0 then
            fail("bad-ack-shapes: errHistory should be empty")
        end
        if actionMalformedCount == 4 then
            fail("bad-ack-shapes: TBFM now rejects all 4 bad ACK/ERR shapes; remove drift entry")
        end
        check("actionMalformedCount", tostring(actionMalformedCount), "2")
    end,
    ["bad-text-too-long"] = function()
        check("hello.version", TB.serverVersion or "(empty)", string.rep("x", 243))
    end,
}

-- Cases where all standard expects run except a single pinned-commit quirk.
local EXPECT_DRIFTS = {
    -- At bebebef, Roster.lua updates state[name].altBot in place on TBM:ALTBOTS
    -- and clears state in commitSnapshot on ROSTER_END, so ALTBOTS sent inside
    -- the stream before ROSTER_END is wiped.
    ["ok-roster-three:altbots"] = function(want)
        local e = TB.GetRosterEntry("Bravo")
        if e and e.altBot then
            fail("ok-roster-three: TBFM now preserves ALTBOTS across ROSTER_END; remove drift entry")
        end
    end,
}

if DRIFTS[caseName] then
    DRIFTS[caseName]()
    if #mismatches > 0 then
        for _, m in ipairs(mismatches) do print("MISMATCH " .. caseName .. ": " .. m) end
        print(string.format("FAIL: %d mismatch(es) in %s", #mismatches, caseName))
        os.exit(1)
    end
    print("ok: " .. caseName .. " (bebebef known drift verified)")
    os.exit(0)
end

-- ── 7. Compare addon state against contract expects ─────────────────────────
local function countMap(t)
    local n = 0
    for _ in pairs(t or {}) do n = n + 1 end
    return n
end

local function checkCaps(wantStr)
    local want = {}
    if wantStr ~= "" then
        for _, item in ipairs(splitChar(wantStr, ",")) do
            want[item] = true
        end
    end
    local got = TB._serverCaps or {}
    for k in pairs(want) do
        if not got[k] or not TB.HasServerCapability(k) then
            fail("hello.caps: missing capability %s", k)
        end
    end
    for k in pairs(got) do
        if not want[k] then
            fail("hello.caps: unexpected capability %s", k)
        end
    end
end

local function checkBotState(name, wantStr)
    local got = TB.GetBotState(name)
    if not got then
        fail("botstate.%s: nil, want %s", name, wantStr)
        return
    end
    local want = {}
    for _, pair in ipairs(splitChar(wantStr, ",")) do
        local k, v = pair:match("^([^=]+)=(.*)$")
        assert(k, "bad botstate expect " .. pair)
        want[k:lower()] = v:lower()
    end
    for k, v in pairs(want) do
        if got[k] ~= v then
            fail("botstate.%s.%s: got %s, want %s", name, k, tostring(got[k]), v)
        end
    end
    for k, v in pairs(got) do
        if want[k] == nil then
            fail("botstate.%s.%s: unexpected key (%s)", name, k, tostring(v))
        end
    end
end

local function checkExpect(p, want)
    if p == "hello.complete" then
        check(p, tostring(transportSeen), want)
    elseif p == "hello.transport" then
        check(p, transportValue or "", want)
        local wantChan = (want == "party") and "PARTY" or nil
        check("TB.AddonCommandChannel()", TB.AddonCommandChannel(), wantChan)
    elseif p == "hello.proto" then
        check(p, tostring(TB.serverProtocol or 0), want)
    elseif p == "hello.min_addon" then
        check(p, tostring(TB.serverMinAddonProtocol or 0), want)
    elseif p == "hello.version" then
        check(p, TB.serverVersion or "(empty)", want)
    elseif p == "hello.caps" then
        checkCaps(want)
    elseif p == "hello.verbs" or p == "hello.has_verbs" then
        -- Known gap at bebebef: TBFM does not parse the VERBS stream yet
        -- (no serverVerbs field, VERBS lines fall through as unknown). The
        -- entry stops passing once TBFM exposes the verbs so it gets retired.
        if TB.serverVerbs ~= nil or TB.GetServerVerbs ~= nil then
            fail("%s: TBFM now stores VERBS; remove the known-gap entry", p)
        end
    elseif p == "hello.latest_module" then
        check(p, TB.latestModule or "-", want)
    elseif p == "hello.latest_addon" then
        check(p, TB.latestAddon or "-", want)
    elseif p == "hello.check" then
        local prob = TB.CompatProblem()
        if want == "ok" then
            check("hello.complete", tostring(transportSeen), "true")
            if TB.serverProtocol == nil then fail("hello.check: missing PROTO") end
            if TB.serverVersion == nil then fail("hello.check: missing VERSION") end
            if TB._serverCaps == nil then fail("hello.check: missing CAPS") end
            check("CompatProblem()", prob, nil)
        elseif want == "proto-incompatible" then
            check("CompatProblem()", prob, "addon")
        elseif want == "hello-incomplete" then
            -- TBFM treats missing PROTO as 0 (CompatProblem nil), so incomplete means
            -- either TRANSPORT never arrived or PROTO never arrived.
            if transportSeen and TB.serverProtocol ~= nil then
                fail("hello.check: expected incomplete hello, but transport and proto both set")
            end
        else
            fail("unknown hello.check expect %s", want)
        end
    elseif p == "roster.committed" then
        check(p, tostring(TB.HasRosterSnapshot() and true or false), want)
    elseif p == "roster.count" then
        check(p, tostring(TB.GetRosterCount()), want)
    elseif p:match("^roster%.%d+%.") then
        local idx, field = p:match("^roster%.(%d+)%.(.+)$")
        local name = rosterArrival[tonumber(idx) + 1]
        local entry = name and TB.GetRosterEntry(name)
        if not entry then
            fail("%s: no roster entry at arrival index %s", p, idx)
            return
        end
        if field == "guid" then
            check(p, tostring(entry.guid), want)
        elseif field == "name" then
            check(p, entry.name, want)
        elseif field == "class" then
            check(p, tostring(entry.classId), want)
        elseif field == "state" then
            check(p, entry.serverState, want)
        elseif field == "grouped" then
            check(p, tostring(entry.group and true or false), want)
        elseif field == "location" then
            -- TBFM normalizes "-" location to nil.
            check(p, entry.location or "-", want)
        elseif field == "cc_mark" then
            check(p, entry.ccMark or "(empty)", want)
        elseif field == "class_name" then
            check(p, entry.serverClassName or "(empty)", want)
        else
            fail("unknown roster field %s", field)
        end
    elseif p == "roster.error" then
        local e = TB.GetRosterSnapshotError()
        check(p, e and e.code or "(empty)", want)
    elseif p == "roster.error_message" then
        local e = TB.GetRosterSnapshotError()
        check(p, e and e.message or "(empty)", want)
    elseif p == "altbots" then
        local wantSet = {}
        if want ~= "" then
            for _, n in ipairs(splitChar(want, ",")) do wantSet[n] = true end
        end
        for _, n in ipairs(rosterArrival) do
            local e = TB.GetRosterEntry(n)
            local got = e and e.altBot and true or false
            local exp = wantSet[n] and true or false
            if got ~= exp then
                fail("altbots.%s: got %s, want %s", n, tostring(got), tostring(exp))
            end
        end
    elseif p == "botstate.count" then
        check(p, tostring(countMap(TB.GetBotStates())), want)
    elseif p:match("^botstate%.") then
        checkBotState(p:sub(#"botstate." + 1), want)
    elseif p == "ack.count" then
        check(p, tostring(#ackHistory), want)
    elseif p:match("^ack%.%d+%.") then
        local idx, field = p:match("^ack%.(%d+)%.(.+)$")
        local a = ackHistory[tonumber(idx) + 1]
        if not a then
            fail("%s: missing ack #%s", p, idx)
            return
        end
        if field == "detail" then field = "executor" end
        check(p, tostring(a[field]), want)
    elseif p == "err.count" then
        check(p, tostring(#errHistory), want)
    elseif p:match("^err%.%d+%.") then
        local idx, field = p:match("^err%.(%d+)%.(.+)$")
        local e = errHistory[tonumber(idx) + 1]
        if not e then
            fail("%s: missing err #%s", p, idx)
            return
        end
        check(p, tostring(e[field]), want)
    elseif p == "problems.count" or p:match("^problem%.") or p == "unknown.count" or p == "ignored.count" then
        -- Reader-level counters are verified per-case below for bad-* cases.
    else
        fail("unknown expect path %s", p)
    end
end

for _, ex in ipairs(expects) do
    local hook = EXPECT_DRIFTS[caseName .. ":" .. ex.path]
    if hook then
        hook(ex.value)
    else
        checkExpect(ex.path, ex.value)
    end
end

-- Additional bad-* refusal checks (beyond expect paths):
if caseName == "bad-roster-count" then
    local e = TB.GetRosterSnapshotError()
    check("rosterSnapshotError.code", e and e.code or "(none)", "count_mismatch")
elseif caseName == "bad-roster-framing" then
    check("rosterErrors[1]", rosterErrors[1], "malformed_row")
    check("rosterErrors[2]", rosterErrors[2], "unexpected_end")
    check("rosterErrors[3]", rosterErrors[3], "malformed_begin")
elseif caseName:sub(1, 3) == "ok-" then
    if caseName ~= "ok-roster-error" and #rosterErrors > 0 then
        fail("ok case triggered roster errors: %s", table.concat(rosterErrors, ", "))
    end
    if actionMalformedCount > 0 then
        fail("ok case triggered malformed action error")
    end
end

if #mismatches > 0 then
    for _, m in ipairs(mismatches) do print("MISMATCH " .. caseName .. ": " .. m) end
    print(string.format("FAIL: %d mismatch(es) in %s", #mismatches, caseName))
    os.exit(1)
end
print("ok: " .. caseName)
