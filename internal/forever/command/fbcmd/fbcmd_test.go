package fbcmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/botctl"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
)

// fakeTransport answers the client's four calls from fixed tables.
type fakeTransport struct {
	runs     []string
	replies  map[string]*command.Reply
	runErrs  map[string]error
	whispers []string
	probed   bool
	acks     map[string]string
	ackErr   error
	party    []string
	partyErr error
	requests []Request
	answers  map[string]Answer
	reqErr   error
}

func (f *fakeTransport) Run(_ context.Context, line string, _ command.Options) (*command.Reply, error) {
	f.runs = append(f.runs, line)
	if err := f.runErrs[line]; err != nil {
		return &command.Reply{Line: line}, err
	}
	if rep := f.replies[line]; rep != nil {
		return rep, nil
	}
	return &command.Reply{Line: line}, nil
}

func (f *fakeTransport) MarkProbed() { f.probed = true }

func (f *fakeTransport) Whisper(_ context.Context, target, text string, _ time.Duration, accept func(string) bool) (string, error) {
	f.whispers = append(f.whispers, target+" "+text)
	if f.ackErr != nil {
		return "", f.ackErr
	}
	ack := f.acks[target]
	if accept != nil && !accept(ack) {
		return "", nil
	}
	return ack, nil
}

func (f *fakeTransport) PartySay(_ context.Context, text string) error {
	f.party = append(f.party, text)
	return f.partyErr
}

func (f *fakeTransport) Request(_ context.Context, req Request) (Answer, error) {
	f.requests = append(f.requests, req)
	if f.reqErr != nil {
		return Answer{}, f.reqErr
	}
	return f.answers[req.Type], nil
}

func reply(lines ...string) *command.Reply { return &command.Reply{Raw: lines, Complete: true} }

func TestProbe(t *testing.T) {
	about := "WoW Forever Bots by someone, based on a bot base. .bot add <class> [tank|healer|dps]"
	f := &fakeTransport{replies: map[string]*command.Reply{".bot about": reply(about)}}
	c := NewClient(f)
	got, err := c.Probe(t.Context())
	if err != nil || got != chat.Sanitise(about, 300) {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if len(f.runs) != 1 || f.runs[0] != ".bot about" {
		t.Fatalf("runs %q", f.runs)
	}
}

func TestProbeAbsent(t *testing.T) {
	cases := []struct {
		name string
		f    *fakeTransport
	}{
		{"echo", &fakeTransport{runErrs: map[string]error{".bot about": &command.RefusalError{Err: command.ErrNoPermission, Text: ".bot about"}}}},
		{"unknown command", &fakeTransport{runErrs: map[string]error{".bot about": &command.RefusalError{Err: command.ErrUnknownCommand, Text: "Command 'bot' does not exist"}}}},
		{"another answer", &fakeTransport{replies: map[string]*command.Reply{".bot about": reply("PlayerbotV2 not initialized.")}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewClient(c.f).Probe(t.Context())
			if !errors.Is(err, ErrModuleAbsent) {
				t.Fatalf("err %v, want ErrModuleAbsent", err)
			}
		})
	}
}

func TestLanguage(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{".bot language en": reply("Bot replies: English (your choice).")}}
	c := NewClient(f)
	line, err := c.Language(t.Context(), "en")
	if err != nil || line == "" {
		t.Fatalf("language = %q, %v", line, err)
	}
	if _, err := c.Language(t.Context(), "de"); err == nil {
		t.Fatal("an unknown language accepted")
	}
}

func TestAdd(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{
		".bot add warrior tank": reply("Raveni is on the way."),
		".bot add priest":       reply("Bramble is on the way."),
	}}
	c := NewClient(f)
	res, err := c.Add(t.Context(), "warrior", "tank")
	if err != nil || res.Name != "Raveni" {
		t.Fatalf("add = %+v, %v", res, err)
	}
	if len(f.runs) != 1 || f.runs[0] != ".bot add warrior tank" {
		t.Fatalf("runs %q", f.runs)
	}
	// A role the module does not need is omitted.
	f.runs = nil
	if _, err := c.Add(t.Context(), "priest", ""); err != nil {
		t.Fatal(err)
	}
	if f.runs[0] != ".bot add priest" {
		t.Fatalf("runs %q", f.runs)
	}
}

func TestAddRefusals(t *testing.T) {
	lines := []string{
		"Bot add failed: your raid is full.",
		"Bot add failed: the map is full: 3/3.",
		"Couldn't create the bot: no name",
		"Created Raveni but the login was refused (a reason). Try `.bot call Raveni`.",
		"That race can't be a Mage.",
		"That race is from the other faction, so it couldn't join your group.",
		"No race of your faction can be a Warrior.",
		"Don't know 'bard': expected tank, healer, dps, a race, male or female.",
		"WoW Forever Bots. Usage: .bot add <class> [tank|healer|dps]",
	}
	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			f := &fakeTransport{replies: map[string]*command.Reply{".bot add warrior dps": reply(line)}}
			_, err := NewClient(f).Add(t.Context(), "warrior", "dps")
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("err %v, want ErrRefused", err)
			}
			var ref *RefusalError
			if !errors.As(err, &ref) || ref.Text == "" {
				t.Fatalf("no refusal text: %v", err)
			}
		})
	}
}

func TestAddUnknownAnswer(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{".bot add warrior dps": reply("Something else entirely.")}}
	_, err := NewClient(f).Add(t.Context(), "warrior", "dps")
	if !errors.Is(err, ErrNoAck) {
		t.Fatalf("err %v, want ErrNoAck", err)
	}
}

func TestValidateClassRole(t *testing.T) {
	for _, class := range []string{"warrior", "priest", "druid", "Warrior"} {
		if err := ValidateClass(class); err != nil {
			t.Fatalf("%q: %v", class, err)
		}
	}
	for _, class := range []string{"", "deathknight", "bard"} {
		if err := ValidateClass(class); err == nil {
			t.Fatalf("%q accepted", class)
		}
	}
	for _, role := range []string{"", "tank", "healer", "dps"} {
		if err := ValidateRole(role); err != nil {
			t.Fatalf("%q: %v", role, err)
		}
	}
	if err := ValidateRole("dps "); err == nil {
		t.Fatal("a role with a space accepted")
	}
}

func TestList(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{
		".bot list": reply("Raveni: level 12 WARRIOR, online, saved", "Bramble: level 3 PRIEST, offline"),
	}}
	rows, err := NewClient(f).List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "Raveni" || rows[0].Level != 12 || !rows[0].Online || !rows[0].Saved ||
		rows[1].Name != "Bramble" || rows[1].Online {
		t.Fatalf("rows %+v", rows)
	}
	f.replies[".bot list"] = reply("No companions yet. `.bot add <class> [tank|healer|dps]` makes one.")
	rows, err = NewClient(f).List(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty list = %+v, %v", rows, err)
	}
	f.replies[".bot list"] = reply("PlayerbotV2 not initialized.")
	if _, err := NewClient(f).List(t.Context()); !errors.Is(err, ErrNoAck) {
		t.Fatalf("err %v, want ErrNoAck", err)
	}
}

func TestRosterAndDetail(t *testing.T) {
	rec := "42,Raveni,12,WARRIOR,HUMAN,TANK,Elwynn,1,91,40,7,follow,combat,0,-,1,0,1,71"
	f := &fakeTransport{answers: map[string]Answer{
		pbc.TypePing:      {Type: pbc.TypePong, Fields: []string{"0"}},
		pbc.TypeRosterReq: {Type: pbc.TypeRosterResp, Fields: []string{"1", rec}},
		pbc.TypeDetailReq: {Type: pbc.TypeDetailResp, Fields: []string{"42", "name=Raveni", "level=12", "hp=300"}},
	}}
	c := NewClient(f)
	if err := c.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Roster(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Name != "Raveni" || rows[0].ID != 42 {
		t.Fatalf("roster %+v, %v", rows, err)
	}
	d, err := c.Detail(t.Context(), 42)
	if err != nil || d.Name != "Raveni" || d.HP != 300 {
		t.Fatalf("detail %+v, %v", d, err)
	}
	if f.requests[0].Type != pbc.TypePing || f.requests[1].Type != pbc.TypeRosterReq ||
		f.requests[2].Type != pbc.TypeDetailReq || f.requests[2].BotID != 42 {
		t.Fatalf("requests %+v", f.requests)
	}
	// A roster with fewer values than this client reads is the drift signal, not a crash.
	f.answers[pbc.TypeRosterReq] = Answer{Fields: []string{"1", "42,Name"}}
	if _, err := c.Roster(t.Context()); !errors.Is(err, pbc.ErrProtoMismatch) {
		t.Fatalf("drift: %v", err)
	}
	// The transport's soft failure is passed through.
	f.reqErr = errors.New("wrapped: " + ErrPBCOff.Error())
	if _, err := c.Roster(t.Context()); err == nil {
		t.Fatal("a failed request returned no error")
	}
}

func TestPingBadEcho(t *testing.T) {
	f := &fakeTransport{answers: map[string]Answer{pbc.TypePing: {Type: pbc.TypePong, Fields: []string{"other"}}}}
	if err := NewClient(f).Ping(t.Context()); !errors.Is(err, pbc.ErrProtoMismatch) {
		t.Fatalf("err %v", err)
	}
}

func TestOrder(t *testing.T) {
	f := &fakeTransport{acks: map[string]string{"Raveni": "Raveni: following at 8 yards"}}
	c := NewClient(f)
	ack, err := c.Order(t.Context(), "raveni", "follow")
	if err != nil {
		t.Fatal(err)
	}
	if ack.Bot != "Raveni" || ack.Text != "following at 8 yards" || ack.Refused {
		t.Fatalf("ack %+v", ack)
	}
	if f.whispers[0] != "Raveni follow" {
		t.Fatalf("whispers %q", f.whispers)
	}
	// Every refusal the module words is a result with Refused set.
	for _, text := range []string{
		"Raveni: not attacking yet (raid pull gate: the main tank opens)",
		"Raveni: can't be summoned right now",
		"Raveni: not a tank, ignoring pull back",
		"Raveni: sorry, i only take orders from the player i belong to",
	} {
		f.acks["Raveni"] = text
		nack, err := c.Order(t.Context(), "Raveni", "follow")
		if err != nil || !nack.Refused || nack.Text == "" {
			t.Fatalf("%q: %+v, %v", text, nack, err)
		}
	}
	// An answer in an unknown shape is accepted as text, not refused.
	f.acks["Raveni"] = "Raveni: follow ok"
	if ack, err := c.Order(t.Context(), "Raveni", "follow"); err != nil || ack.Text != "follow ok" {
		t.Fatalf("plain ack %+v, %v", ack, err)
	}
}

func TestOrderNoAckAndValidation(t *testing.T) {
	f := &fakeTransport{}
	if _, err := NewClient(f).Order(t.Context(), "Raveni", "follow"); !errors.Is(err, ErrNoAck) {
		t.Fatalf("err %v, want ErrNoAck", err)
	}
	f.ackErr = errors.New("link lost")
	if _, err := NewClient(f).Order(t.Context(), "Raveni", "follow"); err == nil {
		t.Fatal("a link error was swallowed")
	}
	for _, word := range []string{"", "follow; die", ".bot add", "a\nb"} {
		if err := ValidateWord(word); err == nil {
			t.Fatalf("%q accepted", word)
		}
	}
	if _, err := NewClient(f).Order(t.Context(), "not a name", "follow"); err == nil {
		t.Fatal("a bad bot name accepted")
	}
}

func TestPartyOrder(t *testing.T) {
	f := &fakeTransport{}
	if err := NewClient(f).PartyOrder(t.Context(), "follow"); err != nil {
		t.Fatal(err)
	}
	if len(f.party) != 1 || f.party[0] != "follow" {
		t.Fatalf("party %q", f.party)
	}
	if err := NewClient(f).PartyOrder(t.Context(), "follow #t1"); err == nil {
		t.Fatal("a token-shaped word accepted")
	}
}

func TestDismiss(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{
		".bot dismiss all":     reply("Dismissed 2 companion(s)."),
		".bot dismiss Raveni":  reply("Dismissed 1 companion(s)."),
		".bot dismiss Unknown": reply("No bot or summoned alt of yours called 'Unknown'. `.bot list` shows your bots."),
		".bot dismiss Bad":     reply(".bot dismiss <name|all>: logs out one of your bots"),
	}}
	c := NewClient(f)
	res, err := c.Dismiss(t.Context(), "all")
	if err != nil || res.Count != 2 {
		t.Fatalf("dismiss = %+v, %v", res, err)
	}
	if res, err = c.Dismiss(t.Context(), "raveni"); err != nil || res.Count != 1 {
		t.Fatalf("dismiss by name = %+v, %v", res, err)
	}
	if _, err = c.Dismiss(t.Context(), "unknown"); !errors.Is(err, ErrRefused) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err = c.Dismiss(t.Context(), "bad"); !errors.Is(err, ErrRefused) {
		t.Fatalf("usage: %v", err)
	}
}

func TestController(t *testing.T) {
	rec := "42,Raveni,12,WARRIOR,HUMAN,TANK,Elwynn,1,91,40,7,follow,combat,0,-,1,0,1,71"
	f := &fakeTransport{
		replies: map[string]*command.Reply{
			".bot add priest healer": reply("Bramble is on the way."),
			".bot dismiss Raveni":    reply("Dismissed 1 companion(s)."),
		},
		answers: map[string]Answer{
			pbc.TypePing:      {Type: pbc.TypePong, Fields: []string{"0"}},
			pbc.TypeRosterReq: {Type: pbc.TypeRosterResp, Fields: []string{"1", rec}},
			pbc.TypeDetailReq: {Type: pbc.TypeDetailResp, Fields: []string{"42", "name=Raveni", "level=12", "hp=300", "hp_max=400"}},
		},
		acks: map[string]string{"Raveni": "Raveni: staying here"},
	}
	ctl := Controller{Client: NewClient(f)}
	if ctl.Kind() != "forever-bots" {
		t.Fatalf("kind %q", ctl.Kind())
	}
	ref, err := ctl.Add(t.Context(), "priest", "healer")
	if err != nil || ref.Name != "Bramble" {
		t.Fatalf("add %+v, %v", ref, err)
	}
	states, err := ctl.Roster(t.Context())
	if err != nil || len(states) != 1 || states[0].Name != "Raveni" || states[0].Level != 12 || !states[0].Grouped {
		t.Fatalf("roster %+v, %v", states, err)
	}
	st, err := ctl.State(t.Context(), ref2(42, ""))
	if err != nil || st.Name != "Raveni" || st.HP != 300 || st.HPMax != 400 || !st.Online {
		t.Fatalf("state %+v, %v", st, err)
	}
	text, err := ctl.Order(t.Context(), ref2(0, "raveni"), "stay", "")
	if err != nil || text != "staying here" {
		t.Fatalf("order %q, %v", text, err)
	}
	if err := ctl.Dismiss(t.Context(), ref2(42, "")); err != nil {
		t.Fatal(err)
	}
}

func ref2(id uint32, name string) botctl.Ref { return botctl.Ref{ID: id, Name: name} }

func TestProbeMatchesRawText(t *testing.T) {
	// The probe marker sits past the display bound: the old display copy cut it off, the raw line has it.
	raw := strings.Repeat("x", 295) + " Forever Bots"
	f := &fakeTransport{replies: map[string]*command.Reply{".bot about": {
		Raw: []string{chat.Sanitise(raw, 300)}, RawText: []string{raw}, Complete: true,
	}}}
	got, err := NewClient(f).Probe(t.Context())
	if err != nil || got == "" {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if !f.probed {
		t.Fatal("a successful probe did not mark the session")
	}
}

func TestAckMatchesOrder(t *testing.T) {
	cases := []struct {
		word, body string
		want       bool
	}{
		{"follow", "following at 8 yards", true},
		{"follow", "follow ok", true},
		{"stay", "staying here", true},
		// The module's phrases per order: a late follow acknowledgement is not a stay.
		{"stay", "following at 8 yards", false},
		{"follow", "staying here", false},
		{"stay", "stay ok", true},
		{"come", "coming to you", true},
		{"come", "can't be summoned right now", true},
		{"attack", "attacking a mob", true},
		{"attack", "not attacking yet (raid pull gate: the main tank opens)", true},
		{"unfollow", "stopped following", true},
		{"formation line", "formation line", true},
		{"follow", "how's it going?", false},
		{"follow", "", false},
	}
	for _, c := range cases {
		if got := ackMatchesOrder(c.word, c.body); got != c.want {
			t.Fatalf("ackMatchesOrder(%q, %q) = %v, want %v", c.word, c.body, got, c.want)
		}
	}
	// An order whose only answer is the earlier order's phrase ends as ErrNoAck, not with the wrong text.
	f := &fakeTransport{acks: map[string]string{"Raveni": "Raveni: following at 8 yards"}}
	if _, err := NewClient(f).Order(t.Context(), "Raveni", "stay"); !errors.Is(err, ErrNoAck) {
		t.Fatalf("a late follow acknowledgement answered stay: %v", err)
	}
}

func TestDetailNotFound(t *testing.T) {
	f := &fakeTransport{answers: map[string]Answer{
		pbc.TypeDetailReq: {Type: pbc.TypeDetailResp, Fields: []string{"42"}},
	}}
	c := NewClient(f)
	if _, err := c.Detail(t.Context(), 42); !errors.Is(err, ErrBotNotFound) {
		t.Fatalf("id-only detail: %v", err)
	}
	ctl := Controller{Client: c}
	if _, err := ctl.State(t.Context(), ref2(42, "")); !errors.Is(err, ErrBotNotFound) {
		t.Fatalf("Controller.State: %v", err)
	}
	// A known bot without a snapshot is not online.
	f.answers[pbc.TypeDetailReq] = Answer{Type: pbc.TypeDetailResp, Fields: []string{"42", "name=Raveni", "level=12", "class=WARRIOR", "race=HUMAN"}}
	st, err := ctl.State(t.Context(), ref2(42, ""))
	if err != nil || st.Online || st.HP != 0 || st.MapID != 0 {
		t.Fatalf("known, not live: %+v, %v", st, err)
	}
}

func TestLfg(t *testing.T) {
	cases := []struct {
		name string
		line string
		role string
		want LfgResult
	}{
		{"joining", "4 bot(s) are joining (1 tank, 2 damage missing).", "tank", LfgResult{Joining: 4, Missing: "1 tank, 2 damage"}},
		{"full", "Your group is already full: 5 players.", "", LfgResult{AlreadyFull: 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := ".bot lfg"
			if c.role != "" {
				want += " " + c.role
			}
			f := &fakeTransport{replies: map[string]*command.Reply{want: reply(c.line)}}
			got, err := NewClient(f).Lfg(t.Context(), c.role)
			if err != nil {
				t.Fatal(err)
			}
			if got.Joining != c.want.Joining || got.Missing != c.want.Missing || got.AlreadyFull != c.want.AlreadyFull {
				t.Fatalf("lfg %+v, want %+v", got, c.want)
			}
			if len(f.runs) != 1 || f.runs[0] != want {
				t.Fatalf("runs %q", f.runs)
			}
		})
	}
	f := &fakeTransport{replies: map[string]*command.Reply{".bot lfg": reply("Only the group leader can fill the group.")}}
	if _, err := NewClient(f).Lfg(t.Context(), ""); !errors.Is(err, ErrRefused) {
		t.Fatalf("refusal: %v", err)
	}
	f = &fakeTransport{replies: map[string]*command.Reply{".bot lfg": reply("something else entirely")}}
	if _, err := NewClient(f).Lfg(t.Context(), ""); !errors.Is(err, ErrNoAck) {
		t.Fatalf("unknown answer: %v", err)
	}
	if _, err := NewClient(&fakeTransport{}).Lfg(t.Context(), "wizard"); err == nil {
		t.Fatal("a bad role was accepted")
	}
}

func TestQueueDungeon(t *testing.T) {
	const line = "Dungeon finder: Ragefire Chasm (levels 13-18). 4 bot(s) are joining (1 tank, 2 damage missing). You go in when the group is complete."
	f := &fakeTransport{replies: map[string]*command.Reply{".bot dungeon rfc tank": reply(line)}}
	res, err := NewClient(f).QueueDungeon(t.Context(), "rfc", "tank")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "Ragefire Chasm" || res.Levels != [2]int{13, 18} || res.InPlace {
		t.Fatalf("queue %+v", res)
	}
	f = &fakeTransport{replies: map[string]*command.Reply{".bot dungeon rfc": reply("Dungeon finder: Ragefire Chasm (levels 13-18). Your group is complete: in you go.")}}
	res, err = NewClient(f).QueueDungeon(t.Context(), "rfc", "")
	if err != nil || !res.InPlace {
		t.Fatalf("complete queue %+v err=%v", res, err)
	}
	for _, refusal := range []string{
		`Dungeon finder: no dungeon called "rfcx". Dungeons for level 13:`,
		"Dungeon finder: not now, you are in combat.",
		"Dungeon finder: you are already in an instance. .bot dungeon leave takes you out.",
		"You are already queued for Ragefire Chasm. .bot dungeon cancel stops it.",
	} {
		f = &fakeTransport{replies: map[string]*command.Reply{".bot dungeon rfc": reply(refusal)}}
		if _, err := NewClient(f).QueueDungeon(t.Context(), "rfc", ""); !errors.Is(err, ErrRefused) {
			t.Fatalf("%q: %v", refusal, err)
		}
	}
	if _, err := NewClient(&fakeTransport{}).QueueDungeon(t.Context(), "rfc\n.bot add warrior", ""); err == nil {
		t.Fatal("a newline in the dungeon word was accepted")
	}
}

func TestDungeonCancelAndLeave(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{
		".bot dungeon cancel": reply("Dungeon finder: Ragefire Chasm cancelled. The bots that joined stay in your group (.bot dismiss sends them off)."),
		".bot dungeon leave":  reply("Dungeon finder: out you go. Your bots follow."),
	}}
	c := NewClient(f)
	if line, err := c.CancelDungeon(t.Context()); err != nil || !strings.Contains(line, "cancelled") {
		t.Fatalf("cancel %q, %v", line, err)
	}
	if line, err := c.LeaveDungeon(t.Context()); err != nil || !strings.Contains(line, "out you go") {
		t.Fatalf("leave %q, %v", line, err)
	}
	f = &fakeTransport{replies: map[string]*command.Reply{
		".bot dungeon cancel": reply("You are not queued for a dungeon."),
		".bot dungeon leave":  reply("Dungeon finder: not while you are in combat."),
	}}
	c = NewClient(f)
	if _, err := c.CancelDungeon(t.Context()); !errors.Is(err, ErrRefused) {
		t.Fatalf("cancel refusal: %v", err)
	}
	if _, err := c.LeaveDungeon(t.Context()); !errors.Is(err, ErrRefused) {
		t.Fatalf("leave refusal: %v", err)
	}
}

func TestLead(t *testing.T) {
	cases := []struct {
		wire  string
		word  string
		state string
		tank  string
	}{
		{"Bramble leads the dungeon now: it pulls pack by pack and waits for mana and for you. Say 'lead pause' to hold, 'lead off' to lead yourself.", "on", "on", "Bramble"},
		{"Bramble leads once you are inside a dungeon or a raid. Say 'lead off' to lead yourself.", "on", "on", "Bramble"},
		{"Bramble holds here and pulls nothing until you say 'go'.", "pause", "pause", "Bramble"},
		{"You lead again: your bots follow you.", "off", "off", ""},
		{"Bramble leads on.", "on", "on", "Bramble"},
		{"Bramble pulls the boss.", "on", "on", "Bramble"},
	}
	for _, c := range cases {
		f := &fakeTransport{replies: map[string]*command.Reply{".bot lead " + c.word: reply(c.wire)}}
		res, err := NewClient(f).Lead(t.Context(), c.word)
		if err != nil {
			t.Fatalf("%q: %v", c.wire, err)
		}
		if res.State != c.state || res.Tank != c.tank {
			t.Fatalf("%q: %+v", c.wire, res)
		}
	}
	for _, refusal := range []string{
		"You have no group yet: add a bot first.",
		"No bot of yours in the group.",
		"None of your bots in the group is a tank: add one (.bot add warrior tank), then say 'lead on'.",
		"Your tank does not lead now: say 'lead on' first.",
	} {
		f := &fakeTransport{replies: map[string]*command.Reply{".bot lead off": reply(refusal)}}
		if _, err := NewClient(f).Lead(t.Context(), "off"); !errors.Is(err, ErrRefused) {
			t.Fatalf("%q: %v", refusal, err)
		}
	}
	if _, err := NewClient(&fakeTransport{}).Lead(t.Context(), "sideways"); err == nil {
		t.Fatal("a bad lead word was accepted")
	}
}

func TestLfgStatus(t *testing.T) {
	f := &fakeTransport{answers: map[string]Answer{
		pbc.TypeLfgReq: {Type: pbc.TypeLfgResp, Fields: []string{"forming", "Ragefire Chasm", "2", "90", "5", "5", "0"}},
	}}
	st, err := NewClient(f).LfgStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "forming" || st.Name != "Ragefire Chasm" || st.Members != 5 || st.Size != 5 || st.CanLeave {
		t.Fatalf("status %+v", st)
	}
	if len(f.requests) != 1 || f.requests[0].Type != pbc.TypeLfgReq || len(f.requests[0].Fields) != 0 ||
		f.requests[0].Answer != pbc.TypeLfgResp {
		t.Fatalf("request %+v", f.requests)
	}
}

func TestLookupDungeon(t *testing.T) {
	for word, want := range map[string]uint32{"rfc": 389, "Ragefire Chasm": 389, "ragefire": 389, "wc": 43, "Zul'Farrak": 209} {
		d, ok := LookupDungeon(word)
		if !ok || d.MapID != want {
			t.Fatalf("%q -> %+v, %v", word, d, ok)
		}
	}
	// Two dungeons hold "scarlet": the word alone names neither.
	if _, ok := LookupDungeon("scarlet"); ok {
		t.Fatal("an ambiguous word resolved to one dungeon")
	}
	if _, ok := LookupDungeon("nowhere"); ok {
		t.Fatal("an unknown word resolved")
	}
}

func TestLeadOnABuildWithoutTheCommand(t *testing.T) {
	f := &fakeTransport{replies: map[string]*command.Reply{".bot lead on": reply(
		"### USAGE: .bot ...", "Possible subcommands:", "|- bot about", "|- bot add")}}
	_, err := NewClient(f).Lead(t.Context(), "on")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "does not know .bot lead") {
		t.Fatalf("err %v", err)
	}
}
