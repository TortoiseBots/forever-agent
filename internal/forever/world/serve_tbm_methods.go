// The TBM methods (#216): tbm_handshake, tbm_send and tbm, the old daemon's names for talking the module's TBM
// add-on protocol. They are thin wrappers over the code `forever tbm-check` already runs (internal/forever/tbmclient
// over this session's own add-on layer: the prefix registration, the party or self add-on send, the self whisper and
// the delivered-message feed, in tbm_adapter.go), so the daemon and the CLI speak the protocol with one
// implementation:
//
//	tbm_handshake   the client's hello: the roster probe, then the trailer (transport, version, protocol, caps,
//	                verbs), and the transport the module's answer switches commands to;
//	tbm_send        one command, read until its answer (an ACK or ERR with the command's intent);
//	tbm             the old name for the same thing: one command and its answer.
//
// The add-on HOST (running a real add-on headless, which is what addon_* and the buttons need) is a later ticket;
// these three need no host, only the module's own answers.
package world

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/tbmclient"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// The TBM methods' bounds: the command's own wait, defaulting to the CLI's 5 s and never more than 2 minutes (the
// module answers a command in well under a second; a long bound only covers a slow link).
const (
	serveTBMWaitDefault = 5 * time.Second
	serveTBMWaitMax     = 2 * time.Minute
)

// serveTBMHelloParams is tbm_handshake's: the transport to try ("auto" follows the module's own answer, "addon",
// "addon-self" or "chat" pins one) and how long the hello may take.
type serveTBMHelloParams struct {
	Transport string `json:"transport,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
}

// serveTBMSendParams is tbm_send's and tbm's: the command, the transport and the command's own wait. tbm keeps the
// old daemon's parameter name for the transport (via).
type serveTBMSendParams struct {
	Command   string `json:"command"`
	Transport string `json:"transport,omitempty"`
	Via       string `json:"via,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
}

// ServeTBMHello is a hello's typed answer: the trailer the module sent, the transport commands go out on after it,
// and the hello's own check ("" when it is whole).
type ServeTBMHello struct {
	Complete    bool     `json:"complete"`
	Transport   string   `json:"transport,omitempty"`
	After       string   `json:"after,omitempty"` // the transport tbm_send uses from now on
	Version     string   `json:"version,omitempty"`
	Proto       int      `json:"proto,omitempty"`
	MinAddon    int      `json:"min_addon,omitempty"`
	Caps        []string `json:"caps,omitempty"`
	Verbs       []string `json:"verbs,omitempty"`
	LatestMod   string   `json:"latest_module,omitempty"`
	LatestAddon string   `json:"latest_addon,omitempty"`
	Check       string   `json:"check,omitempty"`
}

// ServeTBMRow is one roster row of a command's answer.
type ServeTBMRow struct {
	GUID      uint32 `json:"guid"`
	Name      string `json:"name"`
	Class     uint32 `json:"class,omitempty"`
	ClassName string `json:"class_name,omitempty"`
	State     string `json:"state,omitempty"`
	Grouped   bool   `json:"grouped,omitempty"`
	Location  string `json:"location,omitempty"`
	CCMark    string `json:"cc_mark,omitempty"`
}

// ServeTBMAck is one ACTION_ACK of a command's answer.
type ServeTBMAck struct {
	Intent string `json:"intent"`
	Scope  string `json:"scope"`
	Count  int    `json:"count"`
	Detail string `json:"detail,omitempty"`
}

// ServeTBMErr is one ACTION_ERR (or the roster's own error) of a command's answer.
type ServeTBMErr struct {
	Intent  string `json:"intent,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// ServeTBMResult is tbm_send's and tbm's answer: exactly what the CLI's tbm-check prints, typed (counts, codes and
// the roster the command itself returned).
type ServeTBMResult struct {
	Command         string            `json:"command"`
	Transport       string            `json:"transport"`
	Hello           ServeTBMHello     `json:"hello"`
	Roster          []ServeTBMRow     `json:"roster,omitempty"`
	RosterCommitted bool              `json:"roster_committed,omitempty"`
	AltBots         []string          `json:"alt_bots,omitempty"`
	BotState        map[string]string `json:"bot_state,omitempty"`
	Acks            []ServeTBMAck     `json:"acks,omitempty"`
	Errs            []ServeTBMErr     `json:"errs,omitempty"`
	Problems        []string          `json:"problems,omitempty"`
	Unknown         []string          `json:"unknown,omitempty"`
	Ignored         int               `json:"ignored,omitempty"`
}

// registerTBMMethods wires the three TBM methods. Each takes the session's chat feed over for the length of the
// call (tbm_adapter.go) and gives it back afterwards, so the command channel keeps its lines.
func registerTBMMethods(srv *rpc.Server, st *serveState) {
	srv.Register("tbm_handshake", serveCallTyped(st, func(s *session, ctx context.Context, p serveTBMHelloParams) (ServeTBMHello, error) {
		a := newTBMAdapter(s)
		defer a.close()
		return serveTBMHello(ctx, a, p.Transport, serveTBMTimeout(p.WaitMS))
	}))
	send := func(s *session, ctx context.Context, p serveTBMSendParams) (ServeTBMResult, error) {
		if strings.TrimSpace(p.Command) == "" {
			return ServeTBMResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "tbm: command is required (e.g. \"roster\" or \"action follow\")"}
		}
		transport := p.Transport
		if strings.TrimSpace(transport) == "" {
			transport = p.Via // the old daemon's name for the same choice
		}
		a := newTBMAdapter(s)
		defer a.close()
		return serveTBMCommand(ctx, a, transport, p.Command, serveTBMTimeout(p.WaitMS))
	}
	srv.Register("tbm_send", serveCallTyped(st, send))
	srv.Register("tbm", serveCallTyped(st, send))
}

// serveTBMTimeout is the wait a TBM call gets: the default (the CLI's 5 s) for zero, at most two minutes.
func serveTBMTimeout(waitMS int) time.Duration {
	if waitMS <= 0 {
		return serveTBMWaitDefault
	}
	d := time.Duration(waitMS) * time.Millisecond
	if d > serveTBMWaitMax {
		return serveTBMWaitMax
	}
	return d
}

// serveTBMTransport builds a client on the named transport. Empty or "auto" leaves the client's own choice (the
// hello follows the module's answer: party means add-on, none keeps the self whisper).
func serveTBMTransport(conn tbmclient.Conn, transport string) (*tbmclient.Client, error) {
	cli := tbmclient.New(conn)
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "", "auto":
	case "addon":
		cli.SetTransport(tbmproto.TransportAddon)
	case "addon-self":
		cli.SetTransport(tbmproto.TransportAddonSelf)
	case "chat":
		cli.SetTransport(tbmproto.TransportChat)
	default:
		return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("tbm: transport %q is auto, addon, addon-self or chat", transport)}
	}
	return cli, nil
}

// serveTBMHello runs the hello and types its answer. A trailer that fails its own check is an error naming the
// check: a caller must not read a broken hello as a working module.
func serveTBMHello(ctx context.Context, conn tbmclient.Conn, transport string, timeout time.Duration) (ServeTBMHello, error) {
	cli, err := serveTBMTransport(conn, transport)
	if err != nil {
		return ServeTBMHello{}, err
	}
	h, err := cli.Hello(ctx, timeout)
	if err != nil {
		if errors.Is(err, tbmclient.ErrTimeout) {
			return ServeTBMHello{}, &rpc.Error{Code: rpc.CodeTimeout, Message: fmt.Sprintf("tbm_handshake: the module did not finish the hello within %s", timeout)}
		}
		return ServeTBMHello{}, &rpc.Error{Code: rpc.CodeGame, Message: "tbm_handshake: " + err.Error()}
	}
	out := serveTBMHelloRows(h)
	out.After = string(cli.Transport())
	return out, nil
}

// serveTBMCommand runs one command and types its answer.
func serveTBMCommand(ctx context.Context, conn tbmclient.Conn, transport, command string, timeout time.Duration) (ServeTBMResult, error) {
	cli, err := serveTBMTransport(conn, transport)
	if err != nil {
		return ServeTBMResult{}, err
	}
	res, err := cli.Do(ctx, command, timeout)
	if err != nil {
		if errors.Is(err, tbmclient.ErrTimeout) {
			return ServeTBMResult{}, &rpc.Error{Code: rpc.CodeTimeout, Message: fmt.Sprintf("tbm_send: the module did not answer %q within %s", command, timeout)}
		}
		return ServeTBMResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "tbm_send: " + err.Error()}
	}
	return serveTBMResultRows(command, string(cli.Transport()), res), nil
}

// serveTBMHelloRows types the hello's trailer.
func serveTBMHelloRows(h tbmproto.Hello) ServeTBMHello {
	out := ServeTBMHello{
		Complete: h.Complete, Transport: h.Transport, Version: h.Version,
		Proto: h.Proto, MinAddon: h.MinAddon, Caps: h.Caps, Verbs: h.Verbs,
		LatestMod: h.LatestModule, LatestAddon: h.LatestAddon,
	}
	if err := h.Check(); err != nil {
		out.Check = err.Error()
	}
	return out
}

// serveTBMResultRows types one command's reader output.
func serveTBMResultRows(command, transport string, res tbmproto.Result) ServeTBMResult {
	out := ServeTBMResult{Command: command, Transport: transport, Hello: serveTBMHelloRows(res.Hello)}
	for _, r := range res.Roster {
		out.Roster = append(out.Roster, ServeTBMRow{
			GUID: r.GUID, Name: r.Name, Class: r.Class, ClassName: r.ClassName,
			State: r.State, Grouped: r.Grouped, Location: r.Location, CCMark: r.CCMark,
		})
	}
	out.RosterCommitted, out.AltBots, out.BotState = res.RosterCommitted, res.AltBots, res.BotState
	if res.RosterError != nil {
		out.Errs = append(out.Errs, ServeTBMErr{Code: res.RosterError.Code, Message: res.RosterError.Message})
	}
	for _, a := range res.Acks {
		out.Acks = append(out.Acks, ServeTBMAck{Intent: a.Intent, Scope: a.Scope, Count: a.Count, Detail: a.Detail})
	}
	for _, e := range res.Errs {
		out.Errs = append(out.Errs, ServeTBMErr{Intent: e.Intent, Code: e.Code, Message: e.Message})
	}
	for _, p := range res.Problems {
		out.Problems = append(out.Problems, p.Error())
	}
	out.Unknown, out.Ignored = res.Unknown, res.Ignored
	return out
}
