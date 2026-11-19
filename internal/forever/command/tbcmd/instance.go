package tbcmd

// The instance command's read-only answer: the boss state of the instance the leader's party is in. The command
// line is `.tb instance <leader> info`; its reply is a token-marked report whose records carry the bosses as
// `boss=<entry>:<alive|dead>` lists (`;` between bosses). A module that writes one boss per record is read the same
// way, and the end line's own `bosses=` list is accepted too: a caller that needs the boss state must not depend on
// which of the two shapes the module chose.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// InstanceBoss is one boss of an instance and whether its encounter script calls it dead.
type InstanceBoss struct {
	Entry uint32
	Alive bool
	// State is the word the module sent: alive or dead.
	State string
}

// InstanceInfoReport is a whole instance answer.
type InstanceInfoReport struct {
	Bosses  []InstanceBoss
	Target  string
	Count   int
	Records int
	End     map[string]string
	Raw     []string
}

// InstanceInfo reads the boss state of the instance a party leader's group is in. The leader is a character name;
// the report is only returned when it ended with its own marker and its counts agree with its lines.
func (c *Client) InstanceInfo(ctx context.Context, leader string) (InstanceInfoReport, error) {
	name, err := chat.NormaliseName(leader)
	if err != nil {
		return InstanceInfoReport{}, fmt.Errorf("tbcmd: leader: %w", err)
	}
	rep, err := c.call(ctx, ".tb instance "+name+" info", "instance-end")
	if err != nil {
		return InstanceInfoReport{}, err
	}
	return parseInstanceInfo(rep, name)
}

// instanceKinds are the record kind words this layer reads. The module names the record; one kind is enough to
// tell a well-formed report from a reply of some other command.
var instanceKinds = map[string]bool{"instance": true, "boss": true}

// instanceKeyWords are the two words a boss state can be.
var instanceKeyWords = map[string]bool{"alive": true, "dead": true}

// parseInstanceInfo reads the report. The record kind is the module's word for it; the two this layer knows are
// accepted, and any other kind is a bad report rather than a silent zero.
func parseInstanceInfo(rep *command.Reply, leader string) (InstanceInfoReport, error) {
	out := InstanceInfoReport{Target: leader}
	if rep == nil || !rep.Complete || len(rep.Lines) < 2 {
		return out, fmt.Errorf("%w: the reply ended without its instance-end line", ErrBadReport)
	}
	kind := strings.TrimSuffix(rep.Lines[1].Kind, "+")
	// An empty report has no record to name; the begin and end markers are then the only shape there is.
	if len(rep.Lines) == 2 {
		kind = "instance"
	}
	if !instanceKinds[kind] {
		return out, fmt.Errorf("%w: a %q record inside an instance reply", ErrBadReport, kind)
	}
	r, err := parseReport(rep, "instance-begin", "instance-end", kind, "boss")
	if err != nil {
		return out, err
	}
	end := r.End
	out.Raw = append([]string(nil), rep.Raw...)
	out.End = make(map[string]string, len(end.keys))
	for _, k := range end.keys {
		if s, err := decodePart(end.values[k]); err == nil {
			out.End[k] = s
		}
	}
	if out.Count, err = end.requireInt("count", 0, 1<<31-1); err != nil {
		return out, err
	}
	if out.Records, err = end.requireInt("records", 0, 1<<31-1); err != nil {
		return out, err
	}
	if out.Records != len(r.Records) {
		return out, fmt.Errorf("%w: instance-end records=%d, %d record line(s)", ErrBadReport, out.Records, len(r.Records))
	}
	seen := map[uint32]bool{}
	add := func(key, raw string) error {
		bosses, err := parseBossList(key, raw)
		if err != nil {
			return err
		}
		for _, b := range bosses {
			if seen[b.Entry] {
				return fmt.Errorf("%w: two bosses for entry %d", ErrBadReport, b.Entry)
			}
			seen[b.Entry] = true
			out.Bosses = append(out.Bosses, b)
		}
		return nil
	}
	for _, rec := range r.Records {
		raw, ok := rec.rawValue("boss")
		if !ok {
			return out, rec.missing("boss")
		}
		if err := add("boss", raw); err != nil {
			return out, err
		}
	}
	// The end line may carry the same list under its own `bosses=` key; a module that writes only that shape is
	// read too.
	if raw, ok := end.rawValue("bosses"); ok {
		if err := add("bosses", raw); err != nil {
			return out, err
		}
	}
	return out, nil
}

// parseBossList reads a '<entry>:<alive|dead>' list. An empty value is an empty list.
func parseBossList(key, raw string) ([]InstanceBoss, error) {
	if raw == "" {
		return nil, nil
	}
	items := strings.Split(raw, ";")
	out := make([]InstanceBoss, 0, len(items))
	for _, item := range items {
		if item == "" {
			return nil, fmt.Errorf("%w: %s has an empty item", ErrBadReport, key)
		}
		parsed, err := parseBossItem(key, item)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

// parseBossItem reads one boss item, decoding the entry and the state word separately so a bad item names itself.
func parseBossItem(key, item string) (InstanceBoss, error) {
	parts := strings.Split(item, ":")
	if len(parts) != 2 {
		return InstanceBoss{}, fmt.Errorf("%w: %s item %q is not entry:state", ErrBadValue, key, item)
	}
	entryText, err := decodePart(parts[0])
	if err != nil {
		return InstanceBoss{}, fmt.Errorf("%w: %s item %q: %v", ErrBadReport, key, item, err)
	}
	n, err := strconv.ParseUint(entryText, 10, 32)
	if err != nil || n == 0 {
		return InstanceBoss{}, fmt.Errorf("%w: %s item %q has no entry", ErrBadValue, key, item)
	}
	state, err := decodePart(parts[1])
	if err != nil {
		return InstanceBoss{}, fmt.Errorf("%w: %s item %q: %v", ErrBadReport, key, item, err)
	}
	if !instanceKeyWords[state] {
		return InstanceBoss{}, fmt.Errorf("%w: %s item %q is not alive or dead", ErrBadValue, key, item)
	}
	return InstanceBoss{Entry: uint32(n), Alive: state == "alive", State: state}, nil
}
