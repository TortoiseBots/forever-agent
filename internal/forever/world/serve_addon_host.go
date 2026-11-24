package world

// The add-on host's daemon half (T9): addon_start loads a real add-on headless (internal/forever/addonhost, a
// lua5.1 sidecar on a small client mock), addon_state reads what it did, addon_tick advances a manual clock and
// addon_stop ends it. Everything the add-on does with the world goes through this session's own add-on transport
// (internal/forever/addonmsg): the prefixes it registers, the frames it sends on party, raid, instance, guild,
// officer or whisper (a channel message is received but not sent, as addonmsg.Build documents), and the frames
// delivered to those prefixes. The add-on is fed the session's own event log, in order, on the daemon loop: chat
// and add-on frames, the group changes and a re-entry (PLAYER_ENTERING_WORLD), with the world pushed wherever the
// group changed so PARTY_MEMBERS_CHANGED arrives at the right place in the stream.
//
// The add-on message's 255-byte text limit is the server's (addonmsg.MaxText): a longer frame is refused by the
// transport and the refusal is kept on its sent row (the real client sends no chunking of its own; an add-on's
// protocol chunks itself, as the bot module's TBM protocol does). A Lua handler that raises is recorded in the
// add-on's own errors and the dispatch goes on, so a broken add-on never takes the daemon with it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/addonhost"
	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// addonHostStepMS is how often the daemon's own loop ticks a real-clock add-on (OnUpdate) and feeds it new
	// events. A manual-clock host only moves on addon_tick.
	addonHostStepMS = 100
	// addonHostKeep bounds the sent and received rows state keeps; the totals are not tracked beyond it.
	addonHostKeep = 50
	// addonHostPages bounds one feed: a burst larger than this waits for the next loop turn instead of holding the
	// session's events for longer.
	addonHostPages = 4
)

// serveAddonStartParams is addon_start's: the add-on's folder (its .toc is found in it), the clock and any extra
// prefixes to keep registered beside the ones the add-on asks for.
type serveAddonStartParams struct {
	Dir      string   `json:"dir"`
	Clock    string   `json:"clock,omitempty"` // real (default): ticks with the wall clock; manual: only addon_tick advances it
	Prefixes []string `json:"prefixes,omitempty"`
}

// serveAddonTickParams is addon_tick's: milliseconds to advance a manual clock by (100 when omitted).
type serveAddonTickParams struct {
	MS int `json:"ms"`
}

// serveAddonSent is one thing the add-on asked the client to do, and what came of it.
type serveAddonSent struct {
	Kind    string `json:"kind"` // addon, chat or target
	Prefix  string `json:"prefix,omitempty"`
	Channel string `json:"channel,omitempty"` // addon: PARTY, ...; chat: SAY, ...
	Text    string `json:"text,omitempty"`
	To      string `json:"to,omitempty"`
	Error   string `json:"error,omitempty"`
}

// serveAddonReceived is one add-on frame this host's prefixes delivered to the add-on.
type serveAddonReceived struct {
	Seq     uint64 `json:"seq"`
	Prefix  string `json:"prefix"`
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text"`
	Sender  string `json:"sender,omitempty"`
}

// serveAddonHostState is addon_start's, addon_state's and addon_stop's answer.
type serveAddonHostState struct {
	Running    bool                 `json:"running"`
	Dir        string               `json:"dir,omitempty"`
	Name       string               `json:"name,omitempty"` // the add-on's folder name (ADDON_LOADED's arg1)
	Version    string               `json:"version,omitempty"`
	Files      []string             `json:"files,omitempty"`
	Clock      string               `json:"clock"`                // real or manual
	Prefixes   []string             `json:"prefixes,omitempty"`   // the add-on's own registered list
	Registered []string             `json:"registered,omitempty"` // the list this session holds for it
	Sent       []serveAddonSent     `json:"sent,omitempty"`
	Received   []serveAddonReceived `json:"received,omitempty"`
	Errors     []addonhost.LuaError `json:"errors,omitempty"` // the add-on's handler errors, contained
	Chat       []addonhost.ChatLine `json:"chat,omitempty"`   // what it printed or wrote to the chat frame
	Inbox      serveAddonInbox      `json:"inbox"`            // this session's receive counters
	Dropped    int                  `json:"dropped,omitempty"`
	Stderr     string               `json:"stderr,omitempty"`
	Error      string               `json:"error,omitempty"` // why a host that is not running ended
}

// serveAddonInbox is the session's add-on receive counters: a frame that never reached the add-on was either
// filtered for an unregistered prefix (unregistered) or refused as malformed (bad).
type serveAddonInbox struct {
	Total        int `json:"total"`        // every add-on message the server delivered
	Unregistered int `json:"unregistered"` // those whose prefix this client did not have registered
	Bad          int `json:"bad"`          // those the parser refused
	Waiting      int `json:"waiting"`      // those still in the session's bounded inbox
}

// serveAddonTick is addon_tick's answer.
type serveAddonTick struct {
	Clock float64          `json:"clock"`
	Sent  []serveAddonSent `json:"sent,omitempty"`
}

// serveAddonHost is the daemon's one add-on host. Everything runs on the serve loop goroutine, so there is no
// mutex: the sidecar calls are made from the methods and from the loop's own step.
type serveAddonHost struct {
	h          *addonhost.Host
	dir        string
	name       string
	version    string
	files      []string
	registered []string // the add-on's prefixes, as reported by the sidecar
	manual     bool
	lastTick   time.Time
	cursor     uint64
	world      string // the last world pushed, JSON
	sent       []serveAddonSent
	received   []serveAddonReceived
	dropped    int
	lastErr    string // the last contained error (a registration or a feed failure)
	err        error  // why the host ended
}

func registerAddonHostMethods(srv *rpc.Server, st *serveState) {
	srv.Register("addon_start", serveCallTyped(st, func(s *session, _ context.Context, p serveAddonStartParams) (serveAddonHostState, error) {
		return st.addonStart(s, p)
	}))
	srv.Register("addon_stop", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (serveAddonHostState, error) {
		return st.addonStop(s)
	}))
	srv.Register("addon_state", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (serveAddonHostState, error) {
		return st.addonState(s)
	}))
	srv.Register("addon_tick", serveCallTyped(st, func(s *session, _ context.Context, p serveAddonTickParams) (serveAddonTick, error) {
		return st.addonTick(s, p)
	}))
}

// addonStart loads the add-on and registers its prefixes with this session.
func (st *serveState) addonStart(s *session, p serveAddonStartParams) (serveAddonHostState, error) {
	if st.addon != nil && st.addon.h != nil {
		return serveAddonHostState{}, errors.New("addon_start: the add-on host is already running (addon_stop first)")
	}
	if strings.TrimSpace(p.Dir) == "" {
		return serveAddonHostState{}, errors.New("addon_start: needs dir, the add-on's folder")
	}
	if s.opts.AddonOff {
		return serveAddonHostState{}, errors.New("addon_start: this session was started with the add-on off (--addon off), so it cannot register a prefix")
	}
	manual := false
	switch p.Clock {
	case "", "real":
	case "manual":
		manual = true
	default:
		return serveAddonHostState{}, fmt.Errorf("addon_start: clock %q: use real or manual", p.Clock)
	}
	if st.addon != nil {
		// A host that died on its own leaves its prefixes on the session's list; a fresh start takes them off
		// before registering its own.
		_ = st.addon.unregister(s)
	}
	// The cursor goes in before anything the add-on does while loading: a frame the server delivers during the
	// registration (or a plugin's own startup) is still fed, never skipped as already seen.
	a := &serveAddonHost{dir: p.Dir, manual: manual, lastTick: time.Now(), cursor: s.EventLog().Seq()}
	for _, prefix := range p.Prefixes {
		if !slices.Contains(a.registered, prefix) {
			a.registered = append(a.registered, prefix)
		}
	}
	st.addon = a
	h, err := addonhost.Start(a.dir, a.sink(st))
	if err != nil {
		st.addon = nil
		return serveAddonHostState{}, err
	}
	a.h = h
	res, outs, err := h.Load(a.worldOf(s), true)
	a.note(outs)
	if err != nil {
		h.Close()
		st.addon = nil
		return serveAddonHostState{}, fmt.Errorf("addon_start: loading the add-on: %w", err)
	}
	a.name, a.version, a.files = res.Name, res.Version, res.Files
	for _, prefix := range res.Prefixes {
		if !slices.Contains(a.registered, prefix) {
			a.registered = append(a.registered, prefix)
		}
	}
	if err := a.ensureRegistered(s); err != nil {
		h.Close()
		st.addon = nil
		return serveAddonHostState{}, fmt.Errorf("addon_start: registering the add-on's prefixes: %w", err)
	}
	a.world = ""
	if err := a.pushWorld(s); err != nil {
		h.Close()
		st.addon = nil
		return serveAddonHostState{}, fmt.Errorf("addon_start: the world: %w", err)
	}
	return st.addonHostState(s)
}

// addonStop ends the host and takes its prefixes off the session's list, leaving the other prefixes registered.
func (st *serveState) addonStop(s *session) (serveAddonHostState, error) {
	a := st.addon
	if a == nil || a.h == nil {
		return serveAddonHostState{Running: false}, errors.New("addon_stop: the add-on host is not running")
	}
	st.addon = nil
	a.h.Close()
	if err := a.unregister(s); err != nil {
		// The host is stopped whatever the registration answer: say the prefixes may still be on the session's list.
		return serveAddonHostState{Running: false, Error: fmt.Sprintf("addon_stop: the host stopped, but unregistering its prefixes failed: %v", err)}, nil
	}
	return serveAddonHostState{Running: false}, nil
}

// addonState feeds the add-on what arrived since its last read and answers what it did.
func (st *serveState) addonState(s *session) (serveAddonHostState, error) {
	if st.addon == nil {
		return serveAddonHostState{Running: false}, errors.New("addon_state: the add-on host is not running (addon_start)")
	}
	if err := st.addon.feed(s); err != nil {
		st.addon.fail(err)
	}
	return st.addonHostState(s)
}

// addonTick advances the add-on's clock and runs OnUpdate.
func (st *serveState) addonTick(s *session, p serveAddonTickParams) (serveAddonTick, error) {
	a := st.addon
	if a == nil || a.h == nil {
		return serveAddonTick{}, errors.New("addon_tick: the add-on host is not running (addon_start)")
	}
	if err := a.feed(s); err != nil {
		a.fail(err)
		return serveAddonTick{}, err
	}
	ms := p.MS
	if ms <= 0 {
		ms = addonHostStepMS
	}
	clock, outs, err := a.h.Tick(ms)
	a.note(outs)
	if err != nil {
		a.fail(err)
		return serveAddonTick{}, err
	}
	a.lastTick = time.Now()
	return serveAddonTick{Clock: clock, Sent: a.sentNow(outs)}, nil
}

// addonHostState is addon_state's answer from a running or ended host.
func (st *serveState) addonHostState(s *session) (serveAddonHostState, error) {
	a := st.addon
	if a == nil {
		return serveAddonHostState{Running: false}, nil
	}
	out := serveAddonHostState{
		Running: a.h != nil, Dir: a.dir, Name: a.name, Version: a.version, Files: a.files,
		Clock: "real", Prefixes: append([]string(nil), a.registered...),
		Registered: s.addonRegisteredPrefixes(),
		// The RPC goroutine encodes the answer while the loop may feed the host again: the rows go out as copies.
		Sent: append([]serveAddonSent(nil), a.sent...), Received: append([]serveAddonReceived(nil), a.received...),
		Dropped: a.dropped, Error: a.errText(), Inbox: addonInboxOf(s),
	}
	if a.manual {
		out.Clock = "manual"
	}
	if a.h != nil {
		stt, outs, err := a.h.State()
		if err != nil {
			a.fail(err)
			out.Running = false
			out.Error = a.errText()
		} else {
			a.note(outs)
			out.Errors, out.Chat = stt.Errors, stt.Chat
		}
	}
	if a.h != nil {
		out.Stderr = a.h.Stderr()
	}
	return out, nil
}

// addonInboxOf is the session's receive counters, zero before the inbox exists.
func addonInboxOf(s *session) serveAddonInbox {
	if s == nil || s.addon == nil {
		return serveAddonInbox{}
	}
	return serveAddonInbox{Total: s.addon.Total, Unregistered: s.addon.Unregistered, Bad: s.addon.Bad, Waiting: s.addon.Len()}
}

// addonHostStep is the daemon loop's own turn: it feeds the add-on what arrived and, on a real clock, ticks it by
// the time that passed. It never returns an error: a broken host is closed and its reason kept for addon_state.
func (st *serveState) addonHostStep(s *session) {
	a := st.addon
	if a == nil || a.h == nil {
		return
	}
	if err := a.feed(s); err != nil {
		a.fail(err)
		return
	}
	if a.manual {
		return
	}
	now := time.Now()
	elapsed := now.Sub(a.lastTick)
	if elapsed < addonHostStepMS*time.Millisecond {
		return
	}
	ms := int(elapsed / time.Millisecond)
	a.lastTick = a.lastTick.Add(time.Duration(ms) * time.Millisecond)
	_, outs, err := a.h.Tick(ms)
	a.note(outs)
	if err != nil {
		a.fail(err)
	}
}

// addonHostClose stops the sidecar when the daemon ends; the socket is going away, so nothing is unregistered.
func (st *serveState) addonHostClose() {
	if st.addon != nil && st.addon.h != nil {
		st.addon.h.Close()
		st.addon.h = nil
	}
}

// errText is why a host that is not running ended, or the last contained failure.
func (a *serveAddonHost) errText() string {
	if a.err != nil {
		return a.err.Error()
	}
	return a.lastErr
}

// fail ends a host whose sidecar broke: a protocol error or a stuck call leaves nothing to talk to.
func (a *serveAddonHost) fail(err error) {
	if a.h != nil {
		a.h.Close()
		a.h = nil
	}
	if a.err == nil {
		a.err = err
	}
}

// sink performs one Out on the session the daemon holds now (a re-entry replaces it). A failure is kept on the
// Out (the client would not tell the add-on).
func (a *serveAddonHost) sink(st *serveState) addonhost.Sink {
	return func(o addonhost.Out) error {
		s := st.session.Load()
		if s == nil {
			return errors.New("the daemon has no session")
		}
		switch o.Out {
		case "addon":
			ch, ok := addonChannel(o.Channel)
			if !ok {
				return fmt.Errorf("add-on message: %q is not a kind this client sends (PARTY, RAID, INSTANCE_CHAT, GUILD, OFFICER, WHISPER): a channel message is received but not sent", o.Channel)
			}
			target := ""
			if ch == addonmsg.Whisper {
				target = strings.TrimSpace(o.To)
				if target == "" {
					return errors.New("add-on whisper: the add-on named no target")
				}
			}
			if err := a.ensurePrefix(s, o.Prefix); err != nil {
				return err
			}
			return s.SendAddon(ch, target, o.Prefix, o.Text, false)
		case "chat":
			return s.serveChat(context.Background(), strings.ToLower(strings.TrimSpace(o.Type)), o.Text, o.To)
		case "target":
			name := strings.TrimSpace(o.Name)
			if name == "" {
				return errors.New("target: the add-on named no unit")
			}
			o, err := s.serveUnitByName(GUID{}, name)
			if err != nil {
				return err
			}
			return s.serveSelect(o)
		}
		return fmt.Errorf("the add-on asked for %q, which this client does not do", o.Out)
	}
}

// addonChannel maps the client API's channel word to this client's kind. A channel message has no sender-side
// form here (see addonmsg.Build), so only the kinds the transport sends are accepted.
func addonChannel(word string) (addonmsg.Channel, bool) {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "PARTY":
		return addonmsg.Party, true
	case "RAID":
		return addonmsg.Raid, true
	case "INSTANCE_CHAT", "INSTANCE":
		return addonmsg.InstanceChat, true
	case "GUILD":
		return addonmsg.Guild, true
	case "OFFICER":
		return addonmsg.Officer, true
	case "WHISPER":
		return addonmsg.Whisper, true
	}
	return addonmsg.Other, false
}

// addonTypeWord names a delivered channel the way the client's CHAT_MSG_ADDON argument does.
func addonTypeWord(channel string) string {
	switch strings.ToLower(channel) {
	case "instance":
		return "INSTANCE_CHAT"
	}
	return strings.ToUpper(channel)
}

// ensurePrefix makes sure the session holds one prefix the add-on sends under.
func (a *serveAddonHost) ensurePrefix(s *session, prefix string) error {
	if !slices.Contains(a.registered, prefix) {
		a.registered = append(a.registered, prefix)
	}
	return a.ensureRegistered(s)
}

// ensureRegistered registers the add-on's prefixes together with the ones the session already holds: the prefix
// list is per session and RegisterAddonPrefixes replaces it, so the bot module's prefix (or anything else bound
// to this session) stays. A re-entry's fresh session has an empty list, so the next feed registers again.
func (a *serveAddonHost) ensureRegistered(s *session) error {
	have := s.addonRegisteredPrefixes()
	want := append([]string(nil), have...)
	missing := false
	for _, prefix := range a.registered {
		if !slices.Contains(want, prefix) {
			want = append(want, prefix)
			missing = true
		}
	}
	if !missing {
		return nil
	}
	if err := s.RegisterAddonPrefixes(want...); err != nil {
		a.lastErr = err.Error()
		return err
	}
	a.lastErr = ""
	return nil
}

// unregister takes the add-on's prefixes off the session's list, leaving the rest.
func (a *serveAddonHost) unregister(s *session) error {
	have := s.addonRegisteredPrefixes()
	var keep []string
	for _, prefix := range have {
		if !slices.Contains(a.registered, prefix) {
			keep = append(keep, prefix)
		}
	}
	if len(keep) == len(have) {
		return nil
	}
	return s.RegisterAddonPrefixes(keep...)
}

// note keeps what the add-on asked for during a call, bounded.
func (a *serveAddonHost) note(outs []addonhost.Out) {
	for _, o := range outs {
		a.sent = append(a.sent, serveAddonSent{Kind: o.Out, Prefix: o.Prefix, Channel: o.Channel, Text: o.Text, To: o.To, Error: o.Error})
	}
	if len(a.sent) > addonHostKeep {
		a.sent = append([]serveAddonSent(nil), a.sent[len(a.sent)-addonHostKeep:]...)
	}
}

// sentNow is note's answer for the call the caller just made.
func (a *serveAddonHost) sentNow(outs []addonhost.Out) []serveAddonSent {
	rows := make([]serveAddonSent, 0, len(outs))
	for _, o := range outs {
		rows = append(rows, serveAddonSent{Kind: o.Out, Prefix: o.Prefix, Channel: o.Channel, Text: o.Text, To: o.To, Error: o.Error})
	}
	return rows
}

// feed turns the session's events above the cursor into the add-on's game events, pushing the world where the
// group changed, and records the add-on frames the host's prefixes delivered.
func (a *serveAddonHost) feed(s *session) error {
	if a.h == nil {
		return a.err
	}
	if err := a.ensureRegistered(s); err != nil {
		// The add-on's frames cannot arrive while its prefix is not registered; the next turn tries again.
		a.lastErr = err.Error()
	}
	log := s.EventLog()
	var batch []addonhost.Event
	last := a.cursor
	for range addonHostPages {
		rows, next, dropped, more := log.Since(last, EventPageMax)
		if dropped > 0 {
			a.dropped += dropped
		}
		last = next
		for _, ev := range rows {
			if addonWorldChanged(ev) {
				// The world goes first, so PARTY_MEMBERS_CHANGED (or the loading screen's entry) is at its place
				// in the stream; the event itself then follows the ordinary translation below.
				if err := a.sendBatch(batch); err != nil {
					return err
				}
				batch = nil
				if err := a.pushWorld(s); err != nil {
					return err
				}
			}
			if ev.Kind == events.KindAddon {
				a.receive(ev)
			}
			g, ok := addonGameEvent(ev)
			if !ok {
				continue
			}
			batch = append(batch, g)
		}
		if !more {
			break
		}
	}
	if err := a.sendBatch(batch); err != nil {
		return err
	}
	a.cursor = last
	return nil
}

// sendBatch hands one batch to the add-on and keeps what it sent.
func (a *serveAddonHost) sendBatch(batch []addonhost.Event) error {
	if len(batch) == 0 {
		return nil
	}
	outs, err := a.h.SendEvents(batch)
	a.note(outs)
	return err
}

// receive keeps one delivered add-on frame, bounded.
func (a *serveAddonHost) receive(ev Event) {
	prefix, _ := ev.Data["prefix"].(string)
	channel, _ := ev.Data["channel"].(string)
	a.received = append(a.received, serveAddonReceived{
		Seq: ev.Seq, Prefix: prefix, Channel: channel, Text: ev.Text, Sender: ev.Source,
	})
	if len(a.received) > addonHostKeep {
		a.received = append([]serveAddonReceived(nil), a.received[len(a.received)-addonHostKeep:]...)
	}
}

// pushWorld sends the units the add-on may ask about when they changed.
func (a *serveAddonHost) pushWorld(s *session) error {
	w := a.worldOf(s)
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if string(b) == a.world {
		return nil
	}
	a.world = string(b)
	outs, err := a.h.PushWorld(w)
	a.note(outs)
	return err
}

// worldOf is what the session knows of the units a client exposes to add-ons: player, target, party1..4 (the
// player's subgroup in a raid) and raid1..40. A class is known only where the roster names it.
func (a *serveAddonHost) worldOf(s *session) addonhost.World {
	name := s.report.Character.Name
	self := GUID(s.selfOrMover())
	w := addonhost.World{
		Player: addonhost.Unit{Name: name, GUID: self.String(), Online: true, Player: true},
		Party:  []addonhost.Unit{}, Raid: []addonhost.Unit{},
	}
	if s.serveRT != nil && !s.serveRT.target.IsZero() {
		if o, ok := s.world.Object(s.serveRT.target); ok {
			w.Target = &addonhost.Unit{Name: s.world.Name(o), GUID: GUID(o.GUID).String(), Online: true}
		}
	}
	p := s.servePartyInfo()
	if !p.InGroup {
		return w
	}
	for _, m := range p.Members {
		u := addonhost.Unit{Name: m.Name, GUID: m.GUID.String(), Level: int(m.Level),
			Dead: m.Dead, Online: m.Online, Player: true}
		if m.Name == name {
			w.Player.Level, w.Player.Dead = int(m.Level), m.Dead
			w.Player.GUID = u.GUID
		}
		if p.Raid {
			w.Raid = append(w.Raid, u)
			if p.Subgroup != 0 && m.Subgroup != p.Subgroup {
				continue
			}
		}
		if m.GUID == self {
			continue
		}
		if len(w.Party) < 4 {
			w.Party = append(w.Party, u)
		}
	}
	return w
}

// addonWorldChanged says whether an event is a group or world change the add-on sees as a world push (and, for a
// re-entry, PLAYER_ENTERING_WORLD).
func addonWorldChanged(ev Event) bool {
	switch ev.Kind {
	case events.KindParty:
		switch ev.Data["state"] {
		case "roster", "destroyed", "leader", "kicked":
			return true
		}
	case events.KindSession:
		switch ev.Data["state"] {
		case "in_world", "new_world", "reconnected", "teleported":
			return true
		}
	}
	return false
}

// addonGameEvent translates one session event into the client event an add-on's OnEvent handlers read.
func addonGameEvent(ev Event) (addonhost.Event, bool) {
	switch ev.Kind {
	case events.KindAddon:
		prefix, _ := ev.Data["prefix"].(string)
		channel, _ := ev.Data["channel"].(string)
		return addonhost.Event{Event: "CHAT_MSG_ADDON", Args: []string{prefix, ev.Text, addonTypeWord(channel), ev.Source}}, true
	case events.KindSystem:
		return addonhost.Event{Event: "CHAT_MSG_SYSTEM", Args: []string{ev.Text}}, true
	case events.KindWhisper:
		if typ, _ := ev.Data["type"].(string); typ == "whisper" {
			return addonhost.Event{Event: "CHAT_MSG_WHISPER", Args: []string{ev.Text, ev.Source}}, true
		}
	case events.KindSession:
		switch ev.Data["state"] {
		case "in_world", "new_world", "reconnected", "teleported":
			return addonhost.Event{Event: "PLAYER_ENTERING_WORLD", Args: []string{}}, true
		}
	}
	return addonhost.Event{}, false
}

// addonRegisteredPrefixes is the session's registered list, or nil while that list is unknown.
func (s *session) addonRegisteredPrefixes() []string {
	if s.addon == nil || s.addonState != addonRegistrationKnown {
		return nil
	}
	return s.addon.Registered()
}
