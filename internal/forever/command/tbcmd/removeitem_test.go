package tbcmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

const testItemGUID = "0000000000000000000000000000beef"

// removeItemRunner answers only the module's real removal form: one named
// item instance. A count-based line is a usage refusal, like the real module.
func removeItemRunner(t *testing.T, sent *[]string, answer string) *fakeRunner {
	return &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		*sent = append(*sent, line)
		if line == ".tb removeitem Bheal guid "+testItemGUID {
			return reply(o, answer), nil
		}
		return reply(o, "TortoiseBots: usage: .tb removeitem <character> guid <item guid>"), nil
	}}
}

func TestRemoveItemInstanceSendsTheGuidForm(t *testing.T) {
	var sent []string
	c := NewClient(removeItemRunner(t, &sent, "removeitem bot=Bheal item=1708 guid="+testItemGUID+" removed=1 left=3"))
	res, _, err := c.RemoveItemInstance(context.Background(), "Bheal", testItemGUID)
	if err != nil {
		t.Fatalf("removal: %v", err)
	}
	if res.Removed != 1 || res.Left != 3 || res.Item != 1708 || res.GUID != testItemGUID || !strings.EqualFold(res.Bot, "Bheal") {
		t.Fatalf("parsed answer %+v", res)
	}
	if len(sent) != 1 || sent[0] != ".tb removeitem Bheal guid "+testItemGUID {
		t.Fatalf("sent %v, want the guid form only", sent)
	}
}

// The GUID is canonicalised before it is sent: the module's grammar is the
// bare 32 hex digits, and a GUID that is not one is refused here.
func TestRemoveItemInstanceCanonicalisesTheGuid(t *testing.T) {
	var sent []string
	c := NewClient(removeItemRunner(t, &sent, "removeitem bot=Bheal item=1708 guid="+testItemGUID+" removed=1 left=0"))
	if _, _, err := c.RemoveItemInstance(context.Background(), "Bheal", "0x0000000000000000000000000000BEEF"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0] != ".tb removeitem Bheal guid "+testItemGUID {
		t.Fatalf("sent %v", sent)
	}
	if _, _, err := c.RemoveItemInstance(context.Background(), "Bheal", "beef"); err == nil {
		t.Fatal("a short word must not be sent as an item GUID")
	}
}

func TestRemoveItemInstanceRefusesWrongOrMissingRecords(t *testing.T) {
	cases := map[string]string{
		"wrong bot":     "removeitem bot=Other item=1708 guid=" + testItemGUID + " removed=1 left=3",
		"wrong guid":    "removeitem bot=Bheal item=1708 guid=00000000000000000000000000000001 removed=1 left=3",
		"no guid":       "removeitem bot=Bheal item=1708 removed=1 left=3",
		"two removed":   "removeitem bot=Bheal item=1708 guid=" + testItemGUID + " removed=2 left=3",
		"no left":       "removeitem bot=Bheal item=1708 guid=" + testItemGUID + " removed=1",
		"not a number":  "removeitem bot=Bheal item=1708 guid=" + testItemGUID + " removed=x left=3",
		"no record":     "TortoiseBots: Bheal now has 0 of item 1708.",
		"usage refusal": "TortoiseBots: usage: .tb removeitem <character> guid <item guid>",
		"named refusal": "TortoiseBots: Bheal is not a logged-in bot.",
	}
	for name, answer := range cases {
		var sent []string
		c := NewClient(removeItemRunner(t, &sent, answer))
		if _, _, err := c.RemoveItemInstance(context.Background(), "Bheal", testItemGUID); err == nil {
			t.Errorf("%s: %q must not read as a removal", name, answer)
		} else if name == "named refusal" && !IsRefusal(err) {
			t.Errorf("%s: want a refusal, got %v", name, err)
		} else if name == "no record" && !errors.Is(err, ErrBadReport) {
			t.Errorf("%s: want a bad report, got %v", name, err)
		}
	}
}

// The additem record names the instances that give created; a GUID that
// spells a refusal word ("dead") is still a GUID, not a refusal.
func TestAddItemInstancesReadsTheCreatedRecord(t *testing.T) {
	const g1 = "0000000000000000000000000000dead"
	const g2 = "0000000000000000000000000000beef"
	c := NewClient(&fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if line != ".tb additem Bheal 1708 20" {
			t.Fatalf("unexpected line %q", line)
		}
		return reply(o, "TortoiseBots: Bheal now has 25 of item 1708.",
			"additem bot=Bheal item=1708 created="+g1+";"+g2), nil
	}})
	res, _, err := c.AddItemInstances(context.Background(), "Bheal", 1708, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasCreated || len(res.Created) != 2 || res.Created[0] != g1 || res.Created[1] != g2 {
		t.Fatalf("the created instances must be read: %+v", res)
	}
}

// No record is no evidence: HasCreated false, no error. A record for another
// character or item is a bad report, never a grant.
func TestAddItemInstancesWithoutOrWithAForeignRecord(t *testing.T) {
	answer := "TortoiseBots: Bheal now has 25 of item 1708."
	c := NewClient(&fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, answer), nil
	}})
	res, _, err := c.AddItemInstances(context.Background(), "Bheal", 1708, 1)
	if err != nil || res.HasCreated || len(res.Created) != 0 {
		t.Fatalf("an answer without the record names nothing: %+v %v", res, err)
	}
	answer = "additem bot=Bheal item=1709 created=" + testItemGUID
	if _, _, err := c.AddItemInstances(context.Background(), "Bheal", 1708, 1); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a record for another item must be a bad report: %v", err)
	}
	answer = "additem bot=Bheal item=1708 created=zz"
	if _, _, err := c.AddItemInstances(context.Background(), "Bheal", 1708, 1); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a malformed created= value must be a bad report: %v", err)
	}
	answer = "additem bot=Bheal item=1708 created="
	res, _, err = c.AddItemInstances(context.Background(), "Bheal", 1708, 1)
	if err != nil || !res.HasCreated || len(res.Created) != 0 {
		t.Fatalf("an empty created= is a merge, not an error: %+v %v", res, err)
	}
}

// The instance unequip names the GUID, never a slot, and reads moved=.
func TestUnequipItemInstanceSendsTheGuidForm(t *testing.T) {
	var sent []string
	c := NewClient(&fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		sent = append(sent, line)
		return reply(o, "unequip bot=Bheal guid="+testItemGUID+" moved=1"), nil
	}})
	res, _, err := c.UnequipItemInstance(context.Background(), "Bheal", testItemGUID)
	if err != nil || res.Moved != 1 || res.GUID != testItemGUID {
		t.Fatalf("unequip: %+v %v", res, err)
	}
	if len(sent) != 1 || sent[0] != ".tb unequip Bheal guid "+testItemGUID {
		t.Fatalf("the unequip must name the instance: %v", sent)
	}
	c = NewClient(&fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "TortoiseBots: usage: .tb unequip <character> guid <item guid>"), nil
	}})
	if _, _, err := c.UnequipItemInstance(context.Background(), "Bheal", testItemGUID); !IsRefusal(err) {
		t.Fatalf("a module without the instance unequip must refuse: %v", err)
	}
}
