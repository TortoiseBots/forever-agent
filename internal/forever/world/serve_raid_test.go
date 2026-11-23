package world

// R1 of the raid work (#108): the party method reports a raid roster with each member's subgroup and assistant flag,
// and the character's own subgroup and assistant flag. The fake writes the fields from its own group state
// (PartyPackets.cpp:584-627, Group.h:59-108).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestServePartyShowsSubgroups: a ten-member raid roster, subgroups 1-2, one assistant, reported by the party method.
func TestServePartyShowsSubgroups(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Botone", "Bottwo", "Botthree", "Botfour", "Botfive", "Botsix", "Botseven", "Boteight", "Botnine"}
	f.groupRaid = true
	f.groupSubgroup = map[string]uint8{
		"botone": 0, "bottwo": 0, "botthree": 0, "botfour": 0, "botfive": 0,
		"botsix": 1, "botseven": 1, "boteight": 1, "botnine": 1, "testchar": 1,
	}
	// Botone is the roster's first slot, so its GUID is the fake's first member GUID.
	f.groupFlags = map[wire.GUID]uint8{fakeGroupMemberGUID(0): party.FlagAssistant}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	info := awaitGroup(t, a, steps, 10)

	if !info.Raid {
		t.Fatalf("the party method does not report the raid: %+v\n%s", info, steps.joined())
	}
	if info.Subgroup != 2 {
		t.Fatalf("own subgroup %d, want 2", info.Subgroup)
	}
	if info.Assistant {
		t.Fatalf("this character is reported as an assistant")
	}
	want := map[string]int{
		"Botone": 1, "Bottwo": 1, "Botthree": 1, "Botfour": 1, "Botfive": 1,
		"Botsix": 2, "Botseven": 2, "Boteight": 2, "Botnine": 2, "Testchar": 2,
	}
	assistants := 0
	for _, m := range info.Members {
		if got, ok := want[m.Name]; !ok || m.Subgroup != got {
			t.Fatalf("member %q subgroup %d, want %d\n%s", m.Name, m.Subgroup, want[m.Name], steps.joined())
		}
		if m.Assistant {
			assistants++
			if m.Name != "Botone" {
				t.Fatalf("member %q is reported as an assistant, only Botone is one", m.Name)
			}
		}
	}
	if assistants != 1 {
		t.Fatalf("%d assistant rows, want only Botone", assistants)
	}
	a.stop(t)
	f.wait(t)
}
