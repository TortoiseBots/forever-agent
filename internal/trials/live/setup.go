package live

import (
	"context"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// Setup.go: one function per setup-check id (22 ids), returning the Python
// row {id, ok, value, evidence}. The bench rule holds: a row is ok ONLY when
// the server confirmed the state by a read-back; anything not read back is
// UNVERIFIED and an unverifiable REQUIRED row makes the trial INVALID (never
// PASS). Failed rows are retried once, then the repeat is INVALID.
// `config_read` has no Forever source: ok=false, the 20/50/70 defaults
// (non-fatal, as Python). A row that cannot be done on Forever yet (no item
// source: the blue bot kit, the equipment read) is SKIPPED/failed with the
// reason, and a trial whose required rows are SKIPPED is SKIPPED, not PASS.
// A missing server capability (`.tb role <own character>` answered
// `not-a-bot`) makes the trial SKIPPED with needs=<check> in RUN.json —
// never a FAIL.

// CheckIDs is the full setup-check vocabulary (22 ids, in run_setup order).
var CheckIDs = []string{
	"player_level", "player_talents", "player_stance", "player_visible", "player_forced_role",
	"player_gear", "player_no_shield",
	"bot_present", "bot_level", "bot_role", "bot_spec", "bot_items", "bot_mana",
	"grouped",
	"extra_bots_present", "extra_bots_role", "extra_bots_in_group", "extra_bots_no_shield",
	"mob_caster_casts", "mobs_ready", "cc_mark_set",
	"config_read",
}

// skippedTrial marks a trial the server cannot run: every repeat ends
// SKIPPED with needs=<check> in RUN.json, never a FAIL.
type skippedTrial struct {
	needs string
	why   string
}

func (e *skippedTrial) Error() string { return e.why }

// IsSkipped reports whether err is a *skippedTrial; needs is the check.
func IsSkipped(err error) (string, bool) {
	for e := err; e != nil; {
		if s, ok := e.(*skippedTrial); ok {
			return s.needs, true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return "", false
		}
		e = u.Unwrap()
	}
	return "", false
}

// LiveSetup is the A3 real setup: player + bot provisioning with
// read-backs. It implements Runner.Setup. The Situation field stays nil
// until A4 (runRepeat skips a nil situation the way it skips --dry's).
type LiveSetup struct {
	Client *Client
	Guard  *Guard
	Ledger *Ledger

	// Player is the slot character's name; Pool the D5 hire pool by class.
	Player string
	Pool   map[string][]string

	// Previous carries the hire names across repeats (reuse by name).
	Previous       string
	PreviousExtras []string

	// Timeout bounds one check; 0 means 20 s.
	Timeout time.Duration

	// Server is the test server's name (the recovery marks' word);
	// RecoveryPath is the recovery mark file of the items the trial gives;
	// empty means RecoveryPathFor(server, player) under the state dir.
	Server       string
	RecoveryPath string

	// TBRunner overrides the `.tb` transport (tests inject the scripted
	// fake); nil means the serve-backed runner over Client.
	TBRunner *TBRunner

	// Console is the optional server-console backend (the SOAP endpoint):
	// ConsoleCommands run before the client-side setup, so an instance or a
	// save can be prepared with no login. A configured command that fails
	// makes the setup invalid; a command without a backend is invalid too,
	// never silently skipped.
	Console         Console
	ConsoleCommands []string
}

// Console runs one console-capable server command with no client session
// (the SOAP endpoint's executeCommand). The live setup's Console is one.
type Console interface {
	Run(ctx context.Context, command string) (string, error)
}

// consoleRow runs the configured console setup commands and answers their
// setup row ({id, ok, value, evidence}). Nothing configured is no row.
func consoleRow(ctx context.Context, console Console, commands []string) map[string]any {
	if len(commands) == 0 {
		return nil
	}
	row := map[string]any{"id": "server_console", "ok": true, "value": []any{}, "evidence": ""}
	var out []any
	for _, command := range commands {
		if console == nil {
			row["ok"] = false
			row["evidence"] = "console command " + command + " is configured but no SOAP backend is (server's slip: forever.soap)"
			break
		}
		text, err := console.Run(ctx, command)
		if err != nil {
			row["ok"] = false
			row["evidence"] = "console command " + command + ": " + err.Error()
			break
		}
		out = append(out, map[string]any{"command": command, "text": strings.TrimSpace(text)})
	}
	row["value"] = out
	return row
}

// Run builds setup.json's doc: {ok, bot_name, extra_names, guids, checks,
// config}. Failed rows are retried once (except config_read); a hard
// transport error aborts the repeat.
func (s *LiveSetup) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	trial := r.Trial
	// The console backend's setup commands run first: an instance boss state or a
	// save is prepared before any character logs in. A failed (or backend-less)
	// console row already invalidates the repeat: return now, so no character is
	// provisioned and no later row runs behind a setup that did not happen.
	consoleCheck := consoleRow(ctx, s.Console, s.ConsoleCommands)
	if consoleCheck != nil && !boolOf(consoleCheck["ok"]) {
		return map[string]any{
			"ok": false, "invalid_reason": checkEvidence(consoleCheck),
			"bot_name": "", "extra_names": []any{},
			"guids":  map[string]any{"player": "", "bot": nil, "pet": nil, "mobs": []any{}, "marked": nil},
			"checks": []any{consoleCheck},
			"config": map[string]any{"critical": 20, "low": 50, "medium": 70, "ok": false, "source": "default"},
		}, nil
	}
	wanted := wantedChecks(trial)
	tb := tbcmd.NewClient(s.runnerOr(r))
	recovery := s.RecoveryPath
	if recovery == "" {
		recovery = RecoveryPathFor(s.Server, s.playerOr(r))
	}
	r.RecoveryPath = recovery
	prov := &Provisioner{Client: s.clientOr(r), TB: tb, Guard: r.Guard, Ledger: s.ledgerOr(r),
		Player: s.playerOr(r), Pool: s.Pool, Timeout: s.Timeout, Server: s.Server, RecoveryPath: recovery}
	// The hires are known to the repeat the moment they log in: a setup that
	// fails or is SKIPPED later still gets them logged out and cleared.
	prov.OnHire = func(name string) { r.BotName = name }
	prov.OnExtra = func(name string) {
		for _, have := range r.ExtraNames {
			if have == name {
				return
			}
		}
		r.ExtraNames = append(r.ExtraNames, name)
	}
	ps := &PlayerSetup{Client: s.clientOr(r), TB: tb, Guard: r.Guard, Ledger: s.ledgerOr(r),
		Player: s.playerOr(r), Timeout: s.Timeout}

	guids := map[string]any{"player": playerGUIDOf(r, s), "bot": nil, "pet": nil, "mobs": []any{}, "marked": nil}
	checks := []any{}
	// Player side first (level, talents, stance, visible, forced role). The
	// visibility read-back needs the hire's name, so it finalizes after
	// provisioning below; here the row stays (failed, retried once) to keep
	// its trial order.
	playerRows, err := ps.SetupPlayer(ctx, trial)
	if err != nil {
		return nil, err
	}
	for _, row := range playerRows {
		id := strOf(row["id"])
		if !wanted[id] && id != "player_visible" {
			continue
		}
		checks = append(checks, retryOnce(func() map[string]any { return row }))
	}
	if skip := skippedNeed(checks); skip != "" {
		return nil, &skippedTrial{needs: skip, why: skipEvidence(checks, skip)}
	}
	// The hire (provision retries nothing itself; the setup round retries
	// each failed row once below).
	botCtx, botRows, err := prov.Provision(ctx, trial, guids, s.Previous)
	if err != nil {
		return nil, err
	}
	s.Previous = botCtx.HireName
	rowByID := map[string]map[string]any{}
	for _, row := range botRows {
		rowByID[strOf(row["id"])] = row
	}
	for _, id := range []string{"bot_present", "bot_level", "bot_role", "bot_spec", "bot_items", "bot_mana", "grouped"} {
		if !wanted[id] {
			continue
		}
		row, ok := rowByID[id]
		if !ok {
			// Not provisioned (e.g. bot_mana on a rage trial is
			// written by ensureMana itself): never invent a row.
			continue
		}
		checks = append(checks, retryOnce(func() map[string]any { return row }))
	}
	if skip := skippedNeed(checks); skip != "" {
		return nil, &skippedTrial{needs: skip, why: skipEvidence(checks, skip)}
	}
	// Visibility proves after the hire exists: re-run the State read-back
	// against the provisioned bot whenever the trial wants the row. The
	// retry keeps the bench rule (the first ok row stands).
	if wanted["player_visible"] && botCtx.HireName != "" {
		final := func() map[string]any {
			row, err := ps.ensureVisibleFor(ctx, botCtx.HireName)
			if err != nil {
				return rowOf("player_visible", false, nil, "visibility read-back: "+err.Error())
			}
			return row
		}
		for i, c := range checks {
			if m, _ := c.(map[string]any); m != nil && strOf(m["id"]) == "player_visible" {
				checks[i] = retryOnce(final)
			}
		}
	}
	var extraNames []any
	if hasExtras(trial) {
		exCtx, exRows, err := prov.ProvisionExtras(ctx, trial, guids, s.PreviousExtras)
		if err != nil {
			return nil, err
		}
		s.PreviousExtras = exCtx.ExtraNames
		for _, n := range exCtx.ExtraNames {
			extraNames = append(extraNames, n)
		}
		for _, row := range exRows {
			if wanted[strOf(row["id"])] {
				checks = append(checks, retryOnce(func() map[string]any { return row }))
			}
		}
		_ = exCtx
	}
	// Player gear / off-hand rows (non-fatal strip, shield check).
	for _, row := range s.playerGearRows(ctx, r, trial, wanted) {
		checks = append(checks, retryOnce(func() map[string]any { return row }))
	}
	// Config: no Forever source — ok=false, defaults assumed (Python path).
	if wanted["config_read"] {
		checks = append(checks, rowOf("config_read", false, nil,
			"thresholds unreadable; rules use defaults 20/50/70 (assumed)"))
	}
	if consoleCheck != nil {
		checks = append(checks, consoleCheck)
	}
	ok := true
	invalid := ""
	for _, c := range checks {
		m, _ := c.(map[string]any)
		if m == nil || strOf(m["id"]) == "config_read" {
			continue
		}
		if !boolOf(m["ok"]) {
			ok = false
			if invalid == "" {
				invalid = checkEvidence(m)
			}
		}
	}
	doc := map[string]any{
		"ok": ok, "invalid_reason": invalid,
		"bot_name": botCtx.HireName, "extra_names": extraNames,
		"guids": guids, "checks": checks,
		"config": map[string]any{"critical": 20, "low": 50, "medium": 70, "ok": false, "source": "default"},
	}
	return doc, nil
}

func (s *LiveSetup) runnerOr(r *Repeat) *TBRunner {
	if s.TBRunner != nil {
		return s.TBRunner
	}
	return NewTBRunner(s.clientOr(r))
}

func (s *LiveSetup) clientOr(r *Repeat) *Client {
	if s.Client != nil {
		return s.Client
	}
	return r.Client
}

func (s *LiveSetup) ledgerOr(r *Repeat) *Ledger {
	if s.Ledger != nil {
		return s.Ledger
	}
	return r.Ledger
}

func (s *LiveSetup) playerOr(r *Repeat) string {
	if s.Player != "" {
		return s.Player
	}
	if st, err := r.Client.StatusOf(context.Background()); err == nil && st.Character != "" {
		return st.Character
	}
	return "Acmd"
}

func playerGUIDOf(r *Repeat, s *LiveSetup) string {
	st, err := s.clientOr(r).StatusOf(context.Background())
	if err != nil {
		return ""
	}
	return normGUIDOf(st.GUID)
}

// wantedChecks is the trial's setup_checks set.
func wantedChecks(trial map[string]any) map[string]bool {
	out := map[string]bool{}
	if list, ok := trial["setup_checks"].([]any); ok {
		for _, e := range list {
			if s, ok := e.(string); ok {
				out[s] = true
			}
		}
		return out
	}
	// Decoded through pyjson: []any of strings.
	if list, ok := pyjson.List(trial["setup_checks"]); ok {
		for _, e := range list {
			out[pyjson.Str(e)] = true
		}
	}
	return out
}

// retryOnce runs fn up to twice (the Python _retry): the first ok row
// stands, else the second row stands (failed or not). A nil row is kept.
func retryOnce(fn func() map[string]any) map[string]any {
	out := fn()
	if out == nil || boolOf(out["ok"]) {
		return out
	}
	return fn()
}

// skippedNeed scans rows for a SKIPPED marker: a failed row whose evidence
// names needs=<check> (the TB-1 capability path).
func skippedNeed(checks []any) string {
	for _, c := range checks {
		m, _ := c.(map[string]any)
		if m == nil || boolOf(m["ok"]) {
			continue
		}
		if needs := skippedIn(strOf(m["evidence"])); needs != "" {
			return needs
		}
	}
	return ""
}

// skippedIn extracts needs=<check> from evidence.
func skippedIn(evidence string) string {
	const marker = "SKIPPED needs="
	i := strings.Index(evidence, marker)
	if i < 0 {
		return ""
	}
	rest := evidence[i+len(marker):]
	end := strings.IndexAny(rest, ": ;")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func skipEvidence(checks []any, needs string) string {
	for _, c := range checks {
		m, _ := c.(map[string]any)
		if m == nil || boolOf(m["ok"]) {
			continue
		}
		if skippedIn(strOf(m["evidence"])) == needs {
			return strOf(m["evidence"])
		}
	}
	return "SKIPPED needs=" + needs
}

func hasExtras(trial map[string]any) bool {
	if list, ok := trial["extra_bots"].([]any); ok {
		return len(list) > 0
	}
	if list, ok := pyjson.List(trial["extra_bots"]); ok {
		return len(list) > 0
	}
	return false
}

// playerGearRows writes the player gear rows: the tier kit is out of scope
// this round (no Forever source for the carry-over/strip path), so a trial
// that wants player_gear gets ok=false with the reason (INVALID, never a
// silent pass); player_no_shield fails closed (ok=false, UNVERIFIED): the
// serve model has no equipment read (`equipment` answers -32003), so the
// off-hand can never be proven empty this round and a REQUIRED row that is
// unverifiable makes the trial INVALID, never PASS.
func (s *LiveSetup) playerGearRows(ctx context.Context, r *Repeat, trial map[string]any, wanted map[string]bool) []map[string]any {
	var out []map[string]any
	if wanted["player_gear"] {
		out = append(out, rowOf("player_gear", false, nil,
			"player gear kit not provisioned this round (no Forever item source); trial needs a data pass"))
	}
	if wanted["player_no_shield"] {
		out = append(out, s.playerNoShield(ctx, r))
	}
	return out
}

// playerNoShield checks the player's off-hand: the serve unit row carries
// no equipment slots and the `equipment` method is NOT-PORTED, so the
// off-hand state is unreadable and the row fails with UNVERIFIED (never
// ok=true): an unverifiable REQUIRED row makes the trial INVALID, never
// PASS.
func (s *LiveSetup) playerNoShield(ctx context.Context, r *Repeat) map[string]any {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st, err := s.clientOr(r).StatusOf(cctx)
	if err != nil {
		return rowOf("player_no_shield", false, "unreadable", "UNVERIFIED: no status read: "+err.Error())
	}
	u, err := s.clientOr(r).Unit(cctx, st.GUID)
	if err != nil {
		return rowOf("player_no_shield", false, "unreadable", "UNVERIFIED: no unit read: "+err.Error())
	}
	_ = u
	return rowOf("player_no_shield", false, "unreadable", "UNVERIFIED: the serve unit row carries no equipment slots and `equipment` is not ported, so the off-hand cannot be proven empty")
}

// TrialNeeds extracts the SKIPPED needs of a setup doc ("" when none).
func TrialNeeds(setup map[string]any) string {
	list, _ := setup["checks"].([]any)
	rows := list
	if rows == nil {
		if l, ok := pyjson.List(setup["checks"]); ok {
			rows = l
		}
	}
	for _, c := range rows {
		m, _ := c.(map[string]any)
		if m == nil {
			continue
		}
		if needs := skippedIn(strOf(m["evidence"])); needs != "" {
			return needs
		}
	}
	return ""
}
