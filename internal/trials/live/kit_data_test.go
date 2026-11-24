package live

// The #115 druid kit: the Forever overlay's specs.json had only priest/holy, so
// a Balance druid had no Forever-proved talent kit. The overlay now carries one,
// and every id of it is checked against the generated Forever spell table
// (internal/foreverspells, tools/foreverspellgen from the 1.60 spell data): a
// hand-typed id that the table does not know fails here.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/foreverspells"
)

func TestDruidBalanceKitCarriesInnervateAndTheBalanceBasics(t *testing.T) {
	ids := talentKit("druid", "balance")
	if len(ids) == 0 {
		t.Fatal("no druid/balance kit from specs.json")
	}
	table := foreverspells.Default()
	if !table.Loaded() {
		t.Fatal("this build carries no generated Forever spell table")
	}
	names := map[string]uint32{}
	for _, id := range ids {
		sp, ok := table.Lookup(id)
		if !ok {
			t.Fatalf("druid/balance kit id %d is not in the Forever spell table: the kit must come from it", id)
		}
		if sp.Class != "druid" {
			t.Fatalf("druid/balance kit id %d (%s) is a %q spell", id, sp.Name, sp.Class)
		}
		if sp.Level > MaxPlayerLevel {
			t.Fatalf("druid/balance kit id %d (%s) needs level %d, above the cap %d", id, sp.Name, sp.Level, MaxPlayerLevel)
		}
		names[sp.Name] = id
	}
	for _, want := range []string{"Innervate", "Wrath", "Starfire", "Moonfire"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("druid/balance kit lacks %s: %v", want, ids)
		}
	}
	if got := names["Innervate"]; got != 29166 {
		t.Fatalf("Innervate is %d in the Forever spell table, the kit has %d", got, got)
	}
}
