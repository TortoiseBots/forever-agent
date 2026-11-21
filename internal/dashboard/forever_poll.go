package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/rpc"
)

// This file is the Forever poll (L5): one poll of a serve slot asks status,
// party, commander {op:status}, units {within:100} and the event_log tail.
// It never calls a tbm* method, and never dungeon_status, tune_list or
// bot_view: those are the turtle agent's methods.

// foreverReadMethods are the serve methods the Forever poll reads. None of
// them changes anything in the game.
var foreverReadMethods = map[string]bool{
	"status":    true,
	"party":     true,
	"commander": true,
	"units":     true,
	"event_log": true,
}

// foreverClient calls the serve read methods of one slot. It holds no cache:
// the poller makes one per poll. Params are posted as JSON; callers pass
// only the allowlisted methods above.
type foreverClient struct {
	Agent Agent
	HTTP  *http.Client
	mu    sync.Mutex
	token string // the token of the last call, so a reply that echoes it can be redacted
}

// newForeverClient makes a client with its own bounded transport.
func newForeverClient(a Agent, timeout time.Duration) *foreverClient {
	return &foreverClient{Agent: a, HTTP: &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}}
}

// Token is the bearer token of the last call ("" before the first, or when the slot has none).
func (c *foreverClient) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// Call runs one allowlisted serve method with params and returns the result.
func (c *foreverClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !foreverReadMethods[method] {
		return nil, fmt.Errorf("dashboard: method %q is not a Forever read method", method)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.Agent.Addr+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok, err := rpc.ReadTokenFile(c.Agent.TokenPath); err == nil {
		req.Header.Set("Authorization", "Bearer "+tok)
		c.mu.Lock()
		c.token = tok
		c.mu.Unlock()
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized: no readable token for the slot")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return nil, err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, &RPCError{Code: env.Error.Code, Message: env.Error.Message}
	}
	return env.Result, nil
}

// pollForeverOne asks one serve slot its read questions and builds its card
// and its raw panel. A failed call leaves its answer absent from the card,
// never an error: the card shows what answered.
func (p *Poller) pollForeverOne(ctx context.Context, cl *foreverClient, now time.Time) (Card, []byte) {
	ctx, cancel := context.WithTimeout(ctx, p.opt.CallTimeout)
	defer cancel()
	in := Inputs{Agent: cl.Agent, Now: now}
	status, err := cl.Call(ctx, "status", map[string]any{})
	if err != nil {
		in.StatusErr = err.Error()
		return BuildForeverCard(in), nil
	}
	in.Status = status
	var wg sync.WaitGroup
	call := func(method string, params any, dst *json.RawMessage) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := cl.Call(ctx, method, params); err == nil {
				*dst = r
			}
		}()
	}
	call("party", map[string]any{}, &in.Party)
	call("commander", map[string]any{"op": "status"}, &in.Commander)
	call("units", map[string]any{"within": 100}, &in.Units)
	var eventLog json.RawMessage
	call("event_log", map[string]any{"limit": 100}, &eventLog)
	wg.Wait()
	_ = eventLog // the tail is kept for the run's event lines (L5 follow-up); the card reads its fields, not the raw tail
	card := BuildForeverCard(in)
	raw, _ := json.Marshal(map[string]json.RawMessage{
		"status":    orNull(in.Status),
		"party":     orNull(in.Party),
		"commander": orNull(in.Commander),
		"units":     orNull(in.Units),
	})
	raw, err = Scrub(raw, cl.Token())
	if err != nil {
		raw = nil
	}
	return card, raw
}
