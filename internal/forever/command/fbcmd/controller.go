package fbcmd

// Controller adapts Client to botctl.Controller: the shared interface the dungeon and party code of a later round
// uses, so it does not have to know which module answered.

import (
	"context"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/botctl"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
)

// Controller is the botctl view of a WoW Forever Bots server.
type Controller struct{ Client *Client }

var _ botctl.Controller = Controller{}

// Kind names the module family.
func (Controller) Kind() string { return "forever-bots" }

// Add asks for one bot.
func (c Controller) Add(ctx context.Context, class, role string) (botctl.Ref, error) {
	res, err := c.Client.Add(ctx, class, role)
	if err != nil {
		return botctl.Ref{}, err
	}
	return botctl.Ref{Name: res.Name}, nil
}

// Roster lists the account's bots. A read that the protocol cannot serve is an error here: the fallback to the
// chat list is the caller's decision, because it loses the ids and the live fields.
func (c Controller) Roster(ctx context.Context) ([]botctl.State, error) {
	rows, err := c.Client.Roster(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]botctl.State, 0, len(rows))
	for _, r := range rows {
		out = append(out, stateFromRoster(r))
	}
	return out, nil
}

// State reads one bot.
func (c Controller) State(ctx context.Context, ref botctl.Ref) (botctl.State, error) {
	id := ref.ID
	if id == 0 && ref.Name != "" {
		rows, err := c.Client.Roster(ctx)
		if err != nil {
			return botctl.State{}, err
		}
		for _, r := range rows {
			if equalFold(r.Name, ref.Name) {
				id = r.ID
				break
			}
		}
		if id == 0 {
			return botctl.State{}, fmt.Errorf("fbcmd: no bot of yours is called %q", ref.Name)
		}
	}
	d, err := c.Client.Detail(ctx, id)
	if err != nil {
		return botctl.State{}, err
	}
	return stateFromDetail(d), nil
}

// Order gives one bot an order.
func (c Controller) Order(ctx context.Context, ref botctl.Ref, verb, arg string) (string, error) {
	word := verb
	if arg != "" {
		word += " " + arg
	}
	ack, err := c.Client.Order(ctx, ref.Name, word)
	if err != nil {
		return "", err
	}
	return ack.Text, nil
}

// Dismiss dismisses one bot, or every bot when the reference names "all".
func (c Controller) Dismiss(ctx context.Context, ref botctl.Ref) error {
	who := ref.Name
	if who == "" {
		if ref.ID == 0 {
			who = "all"
		} else {
			// The chat command names a bot, so the id alone is not enough: read the roster once for the name.
			rows, err := c.Client.Roster(ctx)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.ID == ref.ID {
					who = r.Name
					break
				}
			}
			if who == "" {
				return fmt.Errorf("fbcmd: id %d is not on your roster", ref.ID)
			}
		}
	}
	_, err := c.Client.Dismiss(ctx, who)
	return err
}

func stateFromRoster(r pbc.RosterEntry) botctl.State {
	return botctl.State{
		Name:    r.Name,
		Class:   r.Class,
		Race:    r.Race,
		Level:   int(r.Level),
		Role:    r.Role,
		Zone:    r.Zone,
		HP:      r.HPPercent,
		Power:   r.ManaPercent,
		Online:  r.Online,
		Grouped: r.Grouped,
		Raw: map[string]string{
			"id":         fmt.Sprint(r.ID),
			"distance":   fmt.Sprint(r.Distance),
			"intent":     r.Intent,
			"last_rule":  r.LastRule,
			"spec":       r.Spec,
			"spec_id":    fmt.Sprint(r.SpecID),
			"headless":   boolWord(r.Headless),
			"saved":      boolWord(r.Saved),
			"hp_percent": fmt.Sprint(r.HPPercent),
			"mana_pct":   fmt.Sprint(r.ManaPercent),
			"group_id":   fmt.Sprint(r.GroupID),
		},
	}
}

func stateFromDetail(d pbc.BotDetail) botctl.State {
	raw := make(map[string]string, len(d.Extra)+5)
	for k, v := range d.Extra {
		raw[k] = v
	}
	raw["last_rule"] = d.LastRule
	raw["spec_id"] = fmt.Sprint(d.SpecID)
	raw["zone_id"] = fmt.Sprint(d.ZoneID)
	raw["area_id"] = fmt.Sprint(d.AreaID)
	raw["rest_xp"] = fmt.Sprint(d.RestXP)
	return botctl.State{
		Name:     d.Name,
		Class:    d.Class,
		Race:     d.Race,
		Level:    d.Level,
		Role:     d.Role,
		HP:       d.HP,
		HPMax:    d.HPMax,
		Power:    d.Mana,
		PowerMax: d.ManaMax,
		MapID:    d.Map,
		X:        d.X,
		Y:        d.Y,
		Z:        d.Z,
		Online:   true,
		Raw:      raw,
	}
}

func boolWord(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
