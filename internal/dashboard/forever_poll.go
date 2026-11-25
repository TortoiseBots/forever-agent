package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
)

// This file is the Forever poll (L5): one poll of a serve slot asks status,
// party, commander {op:status}, units {within:100} and the event_log tail
// through the merged serveclient package (its token rules, its -32005 retry,
// its typed errors). It never calls a tbm* method, and never dungeon_status,
// tune_list or bot_view: those are the turtle agent's methods.
//
// The poll also keeps two per-agent marks the daemon does not carry: the
// progress key of the run (so the card's stall is the time since the run last
// made progress, never its total elapsed time) and the event_log rows already
// shown (so the feed does not repeat them and stays bounded).

// foreverEventCap is the most event_log rows kept per agent on the card.
const foreverEventCap = 100

// foreverProgress is one agent's last progress mark: the run identity
// (step, leg, phase, note and the self position) and when it last changed.
type foreverProgress struct {
	key string
	at  time.Time
}

// foreverFeed is one agent's event_log tail: the last seq taken and the rows
// shown, oldest first, at most foreverEventCap.
type foreverFeed struct {
	last uint64
	list []Event
}

// pollForeverOne asks one serve slot its read questions and builds its card
// and its raw panel. A failed call leaves its answer absent from the card,
// never an error: the card shows what answered. A slot whose client cannot be
// opened (an unreadable token file) is an offline card with that reason: the
// call is never sent without authentication.
func (p *Poller) pollForeverOne(ctx context.Context, a Agent, cl *serveclient.Client, now time.Time) (Card, []byte) {
	ctx, cancel := context.WithTimeout(ctx, p.opt.CallTimeout)
	defer cancel()
	in := Inputs{Agent: a, Now: now}
	st, err := cl.Status(ctx)
	if err != nil {
		// The token never reaches the card (its error is served on /api/state) nor the live map's reason.
		in.StatusErr = scrubText(cl.Token(), err.Error())
		// The serve answers nothing at all: the card's live map says so, with the serve's own words.
		p.markLiveMapGone(a.ID, in.StatusErr)
		in.LiveMap = p.liveMapInfo(a.ID, now)
		return BuildForeverCard(in), nil
	}
	statusRaw, _ := json.Marshal(st)
	in.Status = statusRaw
	var (
		wg      sync.WaitGroup
		party   serveclient.Party
		cmd     serveclient.CommanderResult
		units   []serveclient.Unit
		page    serveclient.EventLogPage
		partyOK bool
		cmdOK   bool
		unitsOK bool
		pageOK  bool
		mu      sync.Mutex
	)
	since := p.foreverLastSeq(a.ID)
	call := func(fn func() error, ok *bool) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err == nil {
				mu.Lock()
				*ok = true
				mu.Unlock()
			}
		}()
	}
	call(func() error { var err error; party, err = cl.Party(ctx); return err }, &partyOK)
	call(func() error {
		var err error
		cmd, err = cl.Commander(ctx, serveclient.CommanderParams{Op: "status"})
		return err
	}, &cmdOK)
	call(func() error {
		var err error
		units, err = cl.Units(ctx, serveclient.UnitsParams{Within: 100})
		return err
	}, &unitsOK)
	call(func() error { var err error; page, err = cl.EventLog(ctx, since, foreverEventCap, 0); return err }, &pageOK)
	wg.Wait()
	if partyOK {
		in.Party, _ = json.Marshal(party)
	}
	if cmdOK {
		b, _ := json.Marshal(cmd)
		in.Commander = b
	}
	if unitsOK {
		in.Units, _ = json.Marshal(units)
	}
	in.ProgressSince = p.markForeverProgress(a.ID, foreverProgressKey(st, cmd, cmdOK), now)
	in.LiveMap = p.liveMapInfo(a.ID, now)
	card := BuildForeverCard(in)
	if !card.Online {
		return card, nil
	}
	var eventRaw json.RawMessage
	if pageOK {
		eventRaw, _ = json.Marshal(page)
		card.Events = p.addForeverEvents(a.ID, page)
	}
	raw, _ := json.Marshal(map[string]json.RawMessage{
		"status":    orNull(in.Status),
		"party":     orNull(in.Party),
		"commander": orNull(in.Commander),
		"units":     orNull(in.Units),
		"event_log": orNull(eventRaw),
	})
	raw, err = Scrub(raw, cl.Token())
	if err != nil {
		raw = nil
	}
	return card, raw
}

// markForeverProgress records the run's progress key and returns the time it
// last changed. A new key (a new step, leg, phase, note or position) stamps
// now; an empty key (no run record) stamps nothing: the card shows no stall.
func (p *Poller) markForeverProgress(id, key string, now time.Time) time.Time {
	if key == "" {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.foreverProg == nil {
		p.foreverProg = map[string]foreverProgress{}
	}
	prev, ok := p.foreverProg[id]
	if ok && prev.key == key {
		return prev.at
	}
	p.foreverProg[id] = foreverProgress{key: key, at: now}
	return now
}

// foreverProgressKey is the run's progress identity: what changes when the
// run does something (a step, leg, kind, phase, note, death/wipe counter or
// the self position), and not the elapsed time. Empty when no run runs.
func foreverProgressKey(st serveclient.Status, cmd serveclient.CommanderResult, haveCmd bool) string {
	if !haveCmd {
		return ""
	}
	card := foreverCommanderCard(cmd)
	if card == nil || card.Run == nil {
		return ""
	}
	r := card.Run
	if r.State != "" && r.State != "running" {
		return ""
	}
	return fmt.Sprintf("%d|%d|%s|%s|%s|%d|%d|%.1f|%.1f", r.Step, legOf(r.Leg), r.Kind, r.Phase, r.Note, r.Deaths, r.Wipes, st.X, st.Y)
}

func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// foreverLastSeq is the last event_log seq already on the card (0 before the
// first answer).
func (p *Poller) foreverLastSeq(id string) uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if f := p.foreverFeed[id]; f != nil {
		return f.last
	}
	return 0
}

// addForeverEvents keeps the event_log rows the card has not shown yet,
// ordered by seq, up to foreverEventCap. Rows are deduplicated by seq, so a
// poll that repeats the tail does not repeat a line on the page, and the
// list never grows past the cap.
func (p *Poller) addForeverEvents(id string, page serveclient.EventLogPage) []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.foreverFeed == nil {
		p.foreverFeed = map[string]*foreverFeed{}
	}
	f := p.foreverFeed[id]
	if f == nil {
		f = &foreverFeed{}
		p.foreverFeed[id] = f
	}
	if page.Next != 0 && page.Next < f.last {
		// The daemon restarted and its seq went backwards: the old rows
		// are gone, so the feed starts again rather than freezing.
		f.last, f.list = 0, nil
	}
	for _, ev := range page.Events {
		if ev.Seq <= f.last {
			continue
		}
		f.last = ev.Seq
		f.list = append(f.list, foreverCardEvent(ev))
	}
	if len(f.list) > foreverEventCap {
		f.list = append([]Event(nil), f.list[len(f.list)-foreverEventCap:]...)
	}
	return append([]Event(nil), f.list...)
}

// foreverCardEvent maps one event_log row onto the card's feed event.
func foreverCardEvent(ev serveclient.Event) Event {
	e := Event{At: ev.Time, Kind: ev.Kind, Text: ev.Text}
	if e.Kind == "" {
		e.Kind = "event"
	}
	if e.Text == "" {
		switch {
		case ev.Source != "" && ev.Target != "":
			e.Text = ev.Source + " -> " + ev.Target
		case ev.Source != "":
			e.Text = ev.Kind + ": " + ev.Source
		default:
			e.Text = ev.Kind
		}
	}
	return e
}

// legOf is a run record's leg, -1 when the record names none.
func legOf(leg *int) int {
	if leg == nil {
		return -1
	}
	return *leg
}
