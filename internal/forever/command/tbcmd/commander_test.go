package tbcmd

// The commander binding card's typed reads: the samples of §11 (names
// replaced by Tanka, Healera and Cmdr), the empty card, the no-binding
// refusal, the run sample that must stay one card, and the lines the call
// builds from its argument.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

const commanderBindingTanka = "binding cmd=Cmdr cguid=15 online=yes lead=Tanka group=1 bound=5 bots=5 " +
	"system=no inparty=no raid=no playerled=no boundat=1795138681 orders=5 map=389 pos=0.80,-8.23,-15.53 #T"

func commanderMemberTanka(name, class, role string, guid int, lead string, hp, maxhp, power, maxpower int, ptype string) string {
	return "member cmd=Cmdr bot=" + name + " guid=" + itoa(guid) + " class=" + class + " role=" + role +
		" level=18 hp=" + itoa(hp) + " maxhp=" + itoa(maxhp) + " power=" + itoa(power) + " maxpower=" +
		itoa(maxpower) + " ptype=" + ptype + " alive=yes lead=" + lead + " map=389 zone=2437 " +
		"pos=-18.24,-46.69,-21.85 #T"
}

func itoa(n int) string { return strconv.Itoa(n) }

// commanderSmallCard is the §11 sample cut to one member and no orders: the
// declared totals (bound=1 bots=1 orders=0) match the records it emits.
func commanderSmallCard() []string {
	binding := strings.NewReplacer("bound=5", "bound=1", "bots=5", "bots=1", "orders=5", "orders=0").
		Replace(commanderBindingTanka)
	return []string{
		binding,
		commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 498, 498, 54, 100, "rage"),
	}
}

const commanderRunTanka = "run lead=Tanka plan=ragefire_chasm state=running step=1 steps=80 leg=0 " +
	"legname=tunnel kind=pull phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 " +
	"elapsed=39 ev=3 cmd=Cmdr #T"

func TestCommanderStatusRunCard(t *testing.T) {
	raws := []string{
		"commander-begin #T",
		commanderBindingTanka,
		commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 498, 498, 54, 100, "rage"),
		commanderMemberTanka("Healera", "priest", "healer", 11, "no", 407, 407, 80, 764, "mana"),
		commanderMemberTanka("Roguea", "rogue", "dps", 12, "no", 350, 350, 60, 100, "energy"),
		commanderMemberTanka("Magea", "mage", "dps", 13, "no", 300, 300, 200, 500, "mana"),
		commanderMemberTanka("Druida", "druid", "dps", 14, "no", 380, 380, 90, 100, "rage"),
		"order cmd=Cmdr at=1795138725 verb=stay detail= ok=yes result=queued%205%20of%205 #T",
		"order cmd=Cmdr at=1795138737 verb=move detail=0.8%20-8.2%20-15.5 ok=yes result=party%205 #T",
		"order cmd=Cmdr at=1795138741 verb=pullback detail=5 ok=yes result=bot%3ATanka%201 #T",
		"order cmd=Cmdr at=1795138748 verb=focus detail=5 ok=yes result=party%205 #T",
		"order cmd=Cmdr at=1795138762 verb=attack detail=5%20Tanka ok=yes result=bot%3ATanka%201 #T",
		commanderRunTanka,
		"commander-end count=12 records=12 bindings=1 #T",
	}
	r := oneReply(raws...)
	rep, err := NewClient(r).CommanderStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("CommanderStatus: %v", err)
	}
	if want := ".tb commander status"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if rep.Bindings != 1 || len(rep.Cards) != 1 {
		t.Fatalf("one binding, one card: %+v", rep)
	}
	card := rep.Cards[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Cmd", card.Cmd, "Cmdr"},
		{"CGUID", card.CGUID, "15"},
		{"Online", card.Online, true},
		{"Lead", card.Lead, "Tanka"},
		{"Group", card.Group, 1},
		{"HasGroup", card.HasGroup, true},
		{"Bound", card.Bound, 5},
		{"Bots", card.Bots, 5},
		{"System", card.System, false},
		{"InParty", card.InParty, false},
		{"Raid", card.Raid, false},
		{"PlayerLed", card.PlayerLed, false},
		{"BoundAt", card.BoundAt, 1795138681},
		{"Orders", card.Orders, 5},
		{"Map", card.Map, uint32(389)},
		{"HasMap", card.HasMap, true},
		{"Members", len(card.Members), 5},
		{"Log", len(card.Log), 5},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if card.Pos == nil || card.Pos.X != 0.8 || card.Pos.Z != -15.53 {
		t.Fatalf("card pos: %+v", card.Pos)
	}
	if card.Run == nil {
		t.Fatal("want the card's run")
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	lead := card.Members[0]
	if lead.Bot != "Tanka" || !lead.Lead || lead.GUID != "10" || lead.Class != "warrior" ||
		lead.Role != "tank" || lead.PowerTyp != "rage" || !lead.Alive || lead.Map != 389 ||
		lead.Zone != 2437 || lead.Level != 18 || lead.HP != 498 || lead.MaxHP != 498 ||
		lead.Power != 54 || lead.MaxPower != 100 {
		t.Fatalf("lead member: %+v", lead)
	}
	if lead.Pos.X != -18.24 || lead.Pos.Y != -46.69 || lead.Pos.Z != -21.85 {
		t.Fatalf("lead pos: %+v", lead.Pos)
	}
	second := card.Members[1]
	if second.Bot != "Healera" || second.Lead || second.GUID != "11" {
		t.Fatalf("second member: %+v", second)
	}
	first := card.Log[0]
	if first.Verb != "stay" || first.At != 1795138725 || !first.OK || first.Detail != "" ||
		first.Result != "queued 5 of 5" {
		t.Fatalf("first order: %+v", first)
	}
	move := card.Log[1]
	if move.Verb != "move" || move.Detail != "0.8 -8.2 -15.5" || move.Result != "party 5" {
		t.Fatalf("move order: %+v", move)
	}
	pull := card.Log[2]
	if pull.Verb != "pullback" || pull.Result != "bot:Tanka 1" {
		t.Fatalf("pullback order: %+v", pull)
	}
	if card.Run == nil || card.Run.Lead != "Tanka" || card.Run.Plan != "ragefire_chasm" ||
		card.Run.State != "running" || card.Run.Step != 1 || card.Run.Steps != 80 ||
		card.Run.Ev != 3 || !card.Run.HasEv {
		t.Fatalf("run: %+v", card.Run)
	}
}

// Map 0 is Eastern Kingdoms: a card standing there is a card with a map, not a refused record.
func TestCommanderStatusMapZero(t *testing.T) {
	binding := strings.Replace(commanderBindingTanka, "map=389", "map=0", 1)
	member := strings.Replace(commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 498, 498, 54, 100, "rage"),
		"map=389", "map=0", 1)
	binding = strings.NewReplacer("bound=5", "bound=1", "bots=5", "bots=1", "orders=5", "orders=0").Replace(binding)
	r := oneReply("commander-begin #T", binding, member, "commander-end count=2 records=2 bindings=1 #T")
	rep, err := NewClient(r).CommanderStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("CommanderStatus on map 0: %v", err)
	}
	card := rep.Cards[0]
	if !card.HasMap || card.Map != 0 || card.Pos == nil {
		t.Fatalf("card map: has=%v map=%d pos=%v, want map 0 present", card.HasMap, card.Map, card.Pos)
	}
	if card.Members[0].Map != 0 {
		t.Fatalf("member map %d", card.Members[0].Map)
	}
}

func TestCommanderStatusIdleCard(t *testing.T) {
	raws := []string{
		"commander-begin #T",
		strings.ReplaceAll(commanderBindingTanka, " orders=5 ", " orders=0 "),
		commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 498, 498, 54, 100, "rage"),
		commanderMemberTanka("Healera", "priest", "healer", 11, "no", 407, 407, 80, 764, "mana"),
		commanderMemberTanka("Roguea", "rogue", "dps", 12, "no", 350, 350, 60, 100, "energy"),
		commanderMemberTanka("Magea", "mage", "dps", 13, "no", 300, 300, 200, 500, "mana"),
		commanderMemberTanka("Druida", "druid", "dps", 14, "no", 380, 380, 90, 100, "rage"),
		"commander-end count=6 records=6 bindings=1 #T",
	}
	r := oneReply(raws...)
	rep, err := NewClient(r).CommanderStatus(context.Background(), "Cmdr")
	if err != nil {
		t.Fatalf("CommanderStatus: %v", err)
	}
	if want := ".tb commander status Cmdr"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if rep.Bindings != 1 || len(rep.Cards) != 1 {
		t.Fatalf("one card: %+v", rep)
	}
	card := rep.Cards[0]
	if len(card.Members) != 5 || len(card.Log) != 0 || card.Run != nil {
		t.Fatalf("idle card: %+v", card)
	}
	if card.Orders != 0 || card.Pos == nil || card.Pos.X != 0.8 {
		t.Fatalf("idle card fields: %+v", card.Pos)
	}
}

func TestCommanderStatusEmpty(t *testing.T) {
	r := oneReply("commander-begin #T", "commander-end count=0 records=0 bindings=0 #T")
	rep, err := NewClient(r).CommanderStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("CommanderStatus: %v", err)
	}
	if len(rep.Cards) != 0 || rep.Bindings != 0 {
		t.Fatalf("no cards: %+v", rep)
	}
}

func TestCommanderStatusNoBinding(t *testing.T) {
	r := oneReply("error code=no-binding cmd=commander msg=Nobody%20commands%20no%20bots #T")
	_, err := NewClient(r).CommanderStatus(context.Background(), "Nobody")
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("err %v, want *CommandError", err)
	}
	if ce.Code != "no-binding" || ce.Cmd != "commander" || !ce.HasMsg {
		t.Fatalf("%+v", ce)
	}
}

func TestCommanderStatusNames(t *testing.T) {
	r := oneReply("commander-begin #T", "commander-end count=0 records=0 bindings=0 #T")
	if _, err := NewClient(r).CommanderStatus(context.Background(), "cmdr"); err != nil {
		t.Fatalf("CommanderStatus: %v", err)
	}
	if want := ".tb commander status Cmdr"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if _, err := NewClient(oneReply()).CommanderStatus(context.Background(), "has space"); err == nil {
		t.Error("a commander with a space was accepted")
	}
	if _, err := NewClient(oneReply()).CommanderStatus(context.Background(), "bad#name"); err == nil {
		t.Error("a commander with # was accepted")
	}
}

func TestCommanderStatusRunAttachesToItsCard(t *testing.T) {
	// §11's failing case: the run record is the card's own run, not a kind
	// the parser does not know and not a second card's line.
	raws := append([]string{"commander-begin #T"}, commanderSmallCard()...)
	raws = append(raws, commanderRunTanka, "commander-end count=3 records=3 bindings=1 #T")
	rep, err := NewClient(oneReply(raws...)).CommanderStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("CommanderStatus: %v", err)
	}
	if len(rep.Cards) != 1 || rep.Cards[0].Run == nil {
		t.Fatalf("one card with its run: %+v", rep)
	}
	if rep.Cards[0].Run.State != "running" {
		t.Fatalf("run: %+v", rep.Cards[0].Run)
	}
}

func TestCommanderStatusContinuation(t *testing.T) {
	// records= counts records, count= counts lines: a continuation line
	// joins its record and moves only count=.
	small := commanderSmallCard()
	raws := []string{
		"commander-begin #T",
		small[0],
		small[1],
		"member+ cmd=Cmdr future_key=a%23b #T",
		"commander-end count=3 records=2 bindings=1 #T",
	}
	rep, err := NewClient(oneReply(raws...)).CommanderStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("CommanderStatus: %v", err)
	}
	if len(rep.Cards) != 1 || len(rep.Cards[0].Members) != 1 {
		t.Fatalf("one card, one member: %+v", rep)
	}
	if got := rep.Cards[0].Members[0].Extra["future_key"]; got != "a#b" {
		t.Fatalf("continuation key: %q", got)
	}
}

func TestCommanderStatusCrossCardRefusals(t *testing.T) {
	// A member, order or run whose cmd= does not name the open card is a bad
	// report. records= counts every record between the markers (§11).
	base := append([]string{"commander-begin #T"}, commanderSmallCard()...)
	last := "commander-end count=3 records=3 bindings=1 #T"
	with := func(extra ...string) []string {
		out := append([]string{}, base...)
		return append(out, extra...)
	}
	otherMember := strings.ReplaceAll(commanderMemberTanka("Healera", "priest", "healer", 11, "no", 407, 407, 80, 764, "mana"), "cmd=Cmdr", "cmd=Other")
	otherRun := strings.ReplaceAll(commanderRunTanka, "cmd=Cmdr", "cmd=Other")
	badMember := commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 499, 498, 54, 100, "rage")
	tests := []struct {
		name string
		raws []string
	}{
		{"other member", with(otherMember, last)},
		{"other order", with("order cmd=Other at=1795138725 verb=stay detail= ok=yes result=queued%205%20of%205 #T", last)},
		{"other run", with(otherRun, last)},
		{"member before binding", []string{"commander-begin #T",
			commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 498, 498, 54, 100, "rage"),
			"commander-end count=1 records=1 bindings=1 #T"}},
		{"unknown kind", with("threat mob=X #T", last)},
		{"bad count", with("commander-end count=9 records=3 bindings=1 #T")},
		{"bindings disagree", with("commander-end count=2 records=2 bindings=2 #T")},
		{"orders over cap", []string{"commander-begin #T",
			strings.ReplaceAll(commanderBindingTanka, "orders=5", "orders=9"),
			"commander-end count=1 records=1 bindings=1 #T"}},
		{"hp over max", []string{"commander-begin #T",
			strings.ReplaceAll(commanderBindingTanka, "orders=5", "orders=0"),
			badMember,
			"commander-end count=2 records=2 bindings=1 #T"}},
		{"second run", []string{"commander-begin #T", commanderBindingTanka, commanderRunTanka,
			commanderRunTanka, "commander-end count=3 records=3 bindings=1 #T"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(oneReply(tc.raws...)).CommanderStatus(context.Background(), "")
			if err == nil {
				t.Fatal("want an error")
			}
			var ce *CommandError
			if errors.As(err, &ce) {
				t.Fatalf("a bad card read as the module's refusal %v", ce)
			}
			if !errors.Is(err, ErrBadReport) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("err %v, want ErrBadReport or ErrBadValue", err)
			}
		})
	}
}

func TestCommanderStatusRecordsRequired(t *testing.T) {
	// §3's grammar: count= AND records= are required on every end line, an
	// empty answer included. Without records= a truncated report would pass.
	small := commanderSmallCard()
	raws := append([]string{"commander-begin #T"}, small...)
	raws = append(raws, "commander-end count=2 bindings=1 #T")
	tests := []struct {
		name string
		raws []string
	}{
		{"empty", []string{"commander-begin #T", "commander-end count=0 bindings=0 #T"}},
		{"one card", raws},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(oneReply(tc.raws...)).CommanderStatus(context.Background(), "")
			if !errors.Is(err, ErrBadReport) {
				t.Fatalf("err %v, want ErrBadReport", err)
			}
		})
	}
}

func TestCommanderStatusCardCounts(t *testing.T) {
	// §11: the binding's bot= count is the number of member records (one per
	// bound bot in the world) and orders= is the number of order records.
	// A missing or excess record is a bad report, never a truncated card.
	small := commanderSmallCard()
	binding, member := small[0], small[1]
	withOrders := func(n string) string { return strings.ReplaceAll(binding, "orders=0", "orders="+n) }
	tests := []struct {
		name string
		raws []string
	}{
		{"missing member", []string{"commander-begin #T", binding,
			"commander-end count=1 records=1 bindings=1 #T"}},
		{"excess member", []string{"commander-begin #T", binding, member,
			strings.ReplaceAll(member, "bot=Tanka", "bot=Healera"),
			"commander-end count=3 records=3 bindings=1 #T"}},
		{"missing order", []string{"commander-begin #T", withOrders("1"), member,
			"commander-end count=2 records=2 bindings=1 #T"}},
		{"excess order", []string{"commander-begin #T", binding, member,
			"order cmd=Cmdr at=1795138725 verb=stay detail= ok=yes result=queued%205%20of%205 #T",
			"commander-end count=3 records=3 bindings=1 #T"}},
		{"bound under bots", []string{"commander-begin #T", strings.ReplaceAll(binding, "bound=1", "bound=0"), member,
			"commander-end count=2 records=2 bindings=1 #T"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(oneReply(tc.raws...)).CommanderStatus(context.Background(), "")
			if !errors.Is(err, ErrBadReport) {
				t.Fatalf("err %v, want ErrBadReport", err)
			}
		})
	}
}

func TestCommanderStatusDuplicateMember(t *testing.T) {
	// §11: one member record per bound bot in the world. The same bot twice
	// would satisfy bots= while another bound bot went missing, so it is a
	// bad report, whatever the record's guid or case.
	binding := strings.NewReplacer("bound=1", "bound=2", "bots=1", "bots=2").
		Replace(commanderSmallCard()[0])
	member := commanderMemberTanka("Tanka", "warrior", "tank", 10, "yes", 498, 498, 54, 100, "rage")
	tests := []struct {
		name   string
		second string
	}{
		{"same record", member},
		{"another guid", strings.ReplaceAll(member, "guid=10", "guid=99")},
		{"another case", strings.ReplaceAll(member, "bot=Tanka", "bot=tanka")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raws := []string{"commander-begin #T", binding, member, tc.second,
				"commander-end count=3 records=3 bindings=1 #T"}
			_, err := NewClient(oneReply(raws...)).CommanderStatus(context.Background(), "")
			if !errors.Is(err, ErrBadReport) {
				t.Fatalf("err %v, want ErrBadReport", err)
			}
		})
	}
}

func TestCommanderCardDistinctMembers(t *testing.T) {
	// bots= counts distinct members, whatever the case: two member records
	// that name one bot cannot pass for two.
	card := Card{Cmd: "Cmdr", Bound: 2, Bots: 2, Members: []CardMember{
		{Bot: "Tanka"},
		{Bot: "tanka"},
	}}
	if err := checkCommanderCard(card); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want ErrBadReport", err)
	}
	card.Members[1].Bot = "Healera"
	if err := checkCommanderCard(card); err != nil {
		t.Fatalf("two distinct members: %v", err)
	}
}

func TestCommanderStatusDuplicateCommander(t *testing.T) {
	// §11: one card per commander. A second binding for the same commander,
	// whatever its case, is a bad report even when bindings= matches.
	first := strings.NewReplacer("bound=5", "bound=0", "bots=5", "bots=0", "orders=5", "orders=0").
		Replace(commanderBindingTanka)
	second := strings.ReplaceAll(first, "cmd=Cmdr", "cmd=cmdr")
	raws := []string{"commander-begin #T", first, second, "commander-end count=2 records=2 bindings=2 #T"}
	_, err := NewClient(oneReply(raws...)).CommanderStatus(context.Background(), "")
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want ErrBadReport", err)
	}
}
