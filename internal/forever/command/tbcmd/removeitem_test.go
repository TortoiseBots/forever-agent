package tbcmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// removeItemRunner answers only the module's real removal form. The old
// negative-count `.tb additem` line is a usage refusal, like the real module.
func removeItemRunner(t *testing.T, sent *[]string, answer string) *fakeRunner {
	return &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		*sent = append(*sent, line)
		if strings.HasPrefix(line, ".tb additem ") {
			return reply(o, "TortoiseBots: usage: .tb additem <bot> <item id> [count]"), nil
		}
		if line == ".tb removeitem Bheal 1708 20" {
			return reply(o, answer), nil
		}
		return reply(o, "TortoiseBots: unknown command"), nil
	}}
}

func TestFreeTextRemoveItemSendsTheRealForm(t *testing.T) {
	var sent []string
	c := NewClient(removeItemRunner(t, &sent, "removeitem bot=Bheal item=1708 removed=20 left=3"))
	res, _, err := c.FreeTextRemoveItem(context.Background(), "Bheal", 1708, 20)
	if err != nil {
		t.Fatalf("removal: %v", err)
	}
	if res.Removed != 20 || res.Left != 3 || res.Item != 1708 || !strings.EqualFold(res.Bot, "Bheal") {
		t.Fatalf("parsed answer %+v", res)
	}
	if len(sent) != 1 || sent[0] != ".tb removeitem Bheal 1708 20" {
		t.Fatalf("sent %v, want the removeitem form only (never a negative additem)", sent)
	}
}

func TestFreeTextRemoveItemRefusesWrongOrMissingRecords(t *testing.T) {
	cases := map[string]string{
		"wrong bot":     "removeitem bot=Other item=1708 removed=20 left=3",
		"wrong item":    "removeitem bot=Bheal item=99 removed=20 left=3",
		"no left":       "removeitem bot=Bheal item=1708 removed=20",
		"not a number":  "removeitem bot=Bheal item=1708 removed=x left=3",
		"no record":     "TortoiseBots: Bheal now has 0 of item 1708.",
		"usage refusal": "TortoiseBots: usage: .tb removeitem <bot> <item id> [count]",
		"named refusal": "TortoiseBots: Bheal is not a logged-in bot.",
	}
	for name, answer := range cases {
		var sent []string
		c := NewClient(removeItemRunner(t, &sent, answer))
		if _, _, err := c.FreeTextRemoveItem(context.Background(), "Bheal", 1708, 20); err == nil {
			t.Errorf("%s: %q must not read as a removal", name, answer)
		} else if name == "named refusal" && !IsRefusal(err) {
			t.Errorf("%s: want a refusal, got %v", name, err)
		} else if name == "no record" && !errors.Is(err, ErrBadReport) {
			t.Errorf("%s: want a bad report, got %v", name, err)
		}
	}
}
