// dungeon_run's start preflight (#216): the checks the CLI's module-led run command (`forever dungeon-watch`, whose
// `.tb run start` dungeon_run reproduces) makes before it starts anything, run by the same code
// (internal/forever/fpreflight) over the daemon's own facts:
//
//	check      the daemon's input
//	plan       driver.LoadWatchRoute over <plans-dir>/<plan>.json (the CLI's plan loader)
//	export     no path: `forever serve` holds no export setting, so the shared check reports its own warning
//	serve      the slot.json `forever serve --dir` wrote (ServeConfig.Dir)
//	lease      the lease that slot names
//	module     `.tb state party` over this session's command channel, the same probe the CLI sends over the link
//	console    `.pinfo NAME` over this character's own GM console, the same command and parser the SOAP endpoint
//	           runs for the CLI; the bots are the run's one lead
//
// A check that cannot run is never skipped: the slot, its lease and the module link need the serve's dir, so a
// daemon started without `--dir` refuses the method instead of starting a run blind, and points at the CLI command
// that makes the whole preflight itself. The SOAP endpoint itself is not checked here (the daemon is given none);
// its place is the GM console, which reads the same `.pinfo` lines. A blocked report refuses the run with the
// failed checks' own lines; nothing is sent.
package world

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/fpreflight"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
)

// The preflight's own session waits: the console's quiet window after the last `.pinfo` line, and how long a console
// read or the module probe may take. Neither wait runs on the session queue: the send is one short queued call and
// the reply is collected off the queue in servePreflightPump slices (preflightCommand), so status and every other
// call keep answering while the console or the module is heard from.
const (
	serveConsoleQuiet       = 500 * time.Millisecond
	serveConsoleWait        = 5 * time.Second
	servePreflightPump      = 50 * time.Millisecond
	servePreflightCloseWait = 2 * time.Second
)

// serveModule is fpreflight.Module over the session's own command channel: the `.tb state party` probe is sent the
// way the CLI sends it through the serve link (the `bot` method's own body), so the daemon checks the same line and
// reads the same reply shape.
type serveModule struct{ st *serveState }

func (m serveModule) Bot(ctx context.Context, line string, wait time.Duration) (serveclient.Command, error) {
	lower := strings.ToLower(strings.TrimSpace(line))
	if !strings.HasPrefix(lower, serveBotPrefix+" ") && !strings.HasPrefix(lower, serveTBPrefix) {
		return serveclient.Command{}, errors.New("bot: the line must start with " + serveBotPrefix + " or " + serveTBPrefix)
	}
	rep, err := m.st.preflightCommand(ctx, line, command.Options{Deadline: wait})
	if err != nil {
		return serveclient.Command{}, err
	}
	return serveclient.Command{Sent: line, Lines: rep.Raw, Complete: rep.Complete, End: rep.End}, nil
}

// serveConsole is fpreflight.Console over this character's own GM console: the preflight's `.pinfo NAME` reads go
// out through the command channel as the serving character (a GM), so a serving daemon checks the named bots with
// no SOAP endpoint and no admin password of its own. The reply is the console's exact text, the same text the SOAP
// endpoint returns for the command.
type serveConsole struct{ st *serveState }

func (c serveConsole) Run(ctx context.Context, line string) (string, error) {
	rep, err := c.st.preflightCommand(ctx, line, command.Options{
		Quiet: serveConsoleQuiet, Deadline: serveConsoleWait,
	})
	if err != nil {
		if errors.Is(err, command.ErrNoReply) {
			return "", fmt.Errorf("no answer within %s", serveConsoleWait)
		}
		return "", err
	}
	if !rep.Complete || len(rep.RawText) == 0 {
		return "", fmt.Errorf("no answer within %s", serveConsoleWait)
	}
	return strings.Join(rep.RawText, "\n"), nil
}

// preflightCommand runs one tokenless command for the preflight without holding the session queue for the
// reply's whole wait. It is the command-channel shape of the TBM methods' off-queue wait (tbm_adapter.go): the GM
// guard, the send and each packet-pump slice are separate short calls, and a chat tap queues the reply lines as the
// session goroutine sees them while Pump hands them to the collector on its own goroutine. A tokenless reply ends
// at its quiet window, so the tap is the only way to hear it without owning the queue.
//
// The run holds the command channel's one-run lock (command.Session.Exclusive), so no other command starts inside
// its reply: the core's console answer carries no marker that would tell its lines from a dashboard `bot` call's or a
// module probe's, and a stray "Player not found!" from another command would refuse the preflight (a "not found" is
// the report's own "does not exist" row). The lock is the channel's serialisation, not the session queue: status and
// every other call still answer while the read waits.
func (st *serveState) preflightCommand(ctx context.Context, line string, opts command.Options) (*command.Reply, error) {
	c := &preflightCommand{st: st}
	if err := c.prepare(ctx, opts); err != nil {
		return nil, err
	}
	defer c.close()
	var rep *command.Reply
	err := st.exclusiveCommand(func() error {
		var rerr error
		rep, rerr = command.Run(ctx, c, line, c.opts)
		return rerr
	})
	return rep, err
}

// exclusiveCommand runs fn as the command channel's only run. A daemon that never installed a channel (no session
// yet) has nothing to exclude, so fn runs alone.
func (st *serveState) exclusiveCommand(fn func() error) error {
	if st.q == nil {
		return fn()
	}
	return st.q.Exclusive(fn)
}

// preflightCommand is a command.Transport over the serving session that never waits on the session queue: prepare
// confirms the GM guard and reads the session's own options, SendCommand sends and installs the tap, Pump drives one
// short packet slice per queued call, and close removes the tap.
type preflightCommand struct {
	st   *serveState
	opts command.Options

	mu      sync.Mutex
	pending []chat.Line // lines the tap delivered since the last pump slice
	closed  bool        // the run is over: a tap whose removal had to wait queues nothing more

	s      *session // the session the tap was installed on
	remove func()   // the tap's remover, session goroutine only
}

// prepare confirms the GM guard (the preflight's lines are GM lines) and fills the session's own options, exactly
// as serveCommandOptions does for a queued run.
func (c *preflightCommand) prepare(ctx context.Context, base command.Options) error {
	v, err := c.st.call(ctx, func(s *session) (any, error) {
		if s.gmStop != nil {
			return nil, s.gmStop
		}
		if !s.gmConfirmed {
			if err := s.confirmGM(ctx); err != nil {
				return nil, err
			}
		}
		return s.commandOptions(base), nil
	})
	if err != nil {
		return err
	}
	opts, ok := v.(command.Options)
	if !ok {
		return errors.New("internal: preflight command options type")
	}
	c.opts = opts
	return nil
}

// SendCommand sends the line and installs the tap in one queued call, so the tap's window starts with its own command:
// no line the session handled before the send reaches it. The command channel's one-run lock keeps other commands out
// of the window (preflightCommand); what it cannot exclude is a line already on the wire from a command that ran
// before the preflight (a tokenless run's tail is waited out only for a tokened one, command.Session) or an
// unsolicited system line (an announcement, another player's GM output). A tokenless core reply carries no marker to
// tell those from its own answer, so the quiet window can still take one: a stray "Player not found!" would be read
// as the bot not existing.
func (c *preflightCommand) SendCommand(ctx context.Context, text string) error {
	_, err := c.st.call(ctx, func(s *session) (any, error) {
		if s.gmStop != nil {
			return nil, s.gmStop
		}
		if command.IsCommandText(text) && !s.gmConfirmed {
			return nil, errCommandNotConfirmed
		}
		if err := s.SendCommandCtx(ctx, text); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.s = s
		c.remove = s.addChatTap(c.deliver)
		c.mu.Unlock()
		return nil, nil
	})
	return err
}

// deliver queues one line for the collector. It runs on the session goroutine (a pump slice, or the loop's idle
// collect between slices), so it never calls the collector itself: only Pump does, on its own goroutine.
func (c *preflightCommand) deliver(line chat.Line) {
	c.mu.Lock()
	if !c.closed {
		c.pending = append(c.pending, line)
	}
	c.mu.Unlock()
}

// take returns the lines delivered since the last slice.
func (c *preflightCommand) take() []chat.Line {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.pending
	c.pending = nil
	return out
}

// Pump drives the session's packets for one short slice per queued call and hands the tap's lines to the collector.
func (c *preflightCommand) Pump(ctx context.Context, feed func(chat.Line), done func() bool) error {
	for {
		for _, line := range c.take() {
			feed(line)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if done() {
			return nil
		}
		if _, err := c.st.call(ctx, func(s *session) (any, error) {
			if s.gmStop != nil {
				return nil, s.gmStop
			}
			return nil, s.collect(ctx, servePreflightPump)
		}); err != nil {
			return err
		}
	}
}

// close removes the tap as one queued call. A session that was replaced by a reconnect is not touched: its tap died
// with it.
func (c *preflightCommand) close() {
	c.mu.Lock()
	c.closed = true
	s, remove := c.s, c.remove
	c.pending = nil
	c.mu.Unlock()
	if s == nil || remove == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), servePreflightCloseWait)
	defer cancel()
	_, _ = c.st.call(ctx, func(cur *session) (any, error) {
		if cur == s {
			remove()
		}
		return nil, nil
	})
}

// servePreflightDeps is the effects the start preflight runs through: the live process probe, the session's own
// command channel for the module link and console, and the run's plan loader. The tests replace them (ServeConfig's
// preflight seam) so no test needs /proc, a token file or a real server console.
func (st *serveState) servePreflightDeps() fpreflight.Deps {
	if st.preflight != nil {
		return st.preflight(st)
	}
	d := fpreflight.DefaultDeps(serveConsole{st: st})
	d.OpenModule = func(string) (fpreflight.Module, error) { return serveModule{st: st}, nil }
	return d
}

// serveRunPreflight runs the CLI's run preflight for one dungeon_run and refuses a blocked report: the returned
// error lists the failed checks and nothing has been sent. plan is the module's plan name (servePlanName) and lead
// the bot the start names.
func (st *serveState) serveRunPreflight(ctx context.Context, character, plan, lead string) error {
	if st.dir == "" {
		return errors.New("dungeon_run: the start preflight cannot check the serve slot, its lease and the module link: " +
			"this daemon was started without --dir. Start it as `forever serve --dir DIR`, or start the run with " +
			"`forever dungeon-watch`, which makes the whole preflight itself")
	}
	path := filepath.Join(st.plansFolder(), plan+".json")
	rep := fpreflight.Run(ctx, fpreflight.Options{
		Kind:      fpreflight.KindWatch,
		ServeDir:  st.dir,
		Plan:      fpreflight.PlanLoader(driver.LoadWatchRoute, path),
		Character: character,
		Bots:      []string{lead},
	}, st.servePreflightDeps())
	if rep.Blocked() {
		return errors.New("dungeon_run: the start preflight failed; nothing was started: " + fpreflightFailures(rep) +
			" (start the run with `forever dungeon-watch`, or fix the serve setup)")
	}
	return nil
}

// fpreflightFailures lists one line per failed check, the report's own words.
func fpreflightFailures(rep *fpreflight.Report) string {
	var out []string
	for _, r := range rep.Results {
		if r.Status == fpreflight.Fail {
			line := r.Check + ": " + r.Detail
			if r.Fix != "" {
				line += " (fix: " + r.Fix + ")"
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, "; ")
}
