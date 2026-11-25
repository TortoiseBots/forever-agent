package classrules

// The record loader: one repeat directory and the trial definition in, a Record out. The party
// comes from the trial (roles and classes) and the setup (the guids the run proved); the heals come
// from the normalised events. A missing or unreadable file leaves the matching Known flag false, so
// a rule reports the missing data instead of judging it.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// LoadRep builds one repeat's record.
func LoadRep(repDir string, trial map[string]any) Record {
	rec := Record{}
	setup := map[string]any{}
	if doc, err := pyjson.UnmarshalFile(filepath.Join(repDir, "setup.json")); err == nil {
		setup, _ = pyjson.Object(doc)
	}
	guids := pyjson.ObjectOrEmpty(setup["guids"])

	player := pyjson.ObjectOrEmpty(trial["player"])
	bot := pyjson.ObjectOrEmpty(trial["bot"])
	rec.Members = append(rec.Members, Member{
		Unit: "$player", GUID: guidOf(guids["player"]),
		Class: textOf(player["class"]), Spec: textOf(player["spec"]),
		Role: playerRole(player),
	})
	rec.Members = append(rec.Members, Member{
		Unit: "$bot", Name: textOf(setup["bot_name"]), GUID: guidOf(guids["bot"]),
		Class: textOf(bot["class"]), Spec: textOf(bot["spec"]),
		Role: textOf(bot["role"]),
	})
	extras, _ := pyjson.List(trial["extra_bots"])
	extraNames, _ := pyjson.List(setup["extra_names"])
	extraGUIDs, _ := pyjson.List(guids["extras"])
	for i, extraVal := range extras {
		extra := pyjson.ObjectOrEmpty(extraVal)
		m := Member{
			Unit: "$extra1", Class: textOf(extra["class"]),
			Spec: textOf(extra["spec"]), Role: textOf(extra["role"]),
		}
		switch i {
		case 1:
			m.Unit = "$extra2"
		case 2:
			m.Unit = "$extra3"
		}
		if i < len(extraNames) {
			m.Name = textOf(extraNames[i])
		}
		if i < len(extraGUIDs) {
			m.GUID = guidOf(extraGUIDs[i])
		}
		rec.Members = append(rec.Members, m)
	}

	eventsPath := filepath.Join(repDir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		// ReadJSONL reports a missing file as no rows and no error; the rule must know the
		// difference between an empty stream and no stream at all.
		return rec
	}
	rows, err := events.ReadJSONL(eventsPath)
	if err != nil {
		return rec
	}
	rec.EventsKnown = true
	byGUID := memberByGUID(rec.Members)
	unitOf := func(guid any) string {
		key := guidOf(guid)
		if i, ok := byGUID[key]; ok && key != "" {
			return rec.Members[i].Unit
		}
		return ""
	}
	for _, row := range rows {
		if pyjson.Str(row["kind"]) != "heal" {
			continue
		}
		at, _ := pyjson.Float(row["t"])
		heal := Heal{At: at, Src: unitOf(row["src_guid"]), Dst: unitOf(row["dst_guid"])}
		if amount, ok := pyjson.Float(row["amount"]); ok {
			heal.Amount, heal.AmountKnown = amount, true
		}
		rec.Heals = append(rec.Heals, heal)
	}
	return rec
}

// playerRole is the player's role: the trial's own role, else its forced role (the role the runner
// forces on the player when a trial stages it).
func playerRole(player map[string]any) string {
	if role := textOf(player["role"]); role != "" {
		return role
	}
	return textOf(player["forced_role"])
}

// textOf is a JSON string as it stands, "" when the key is absent or null: pyjson.Str writes
// Python's str(None) ("None") and a missing role must read as no role.
func textOf(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return strings.TrimSpace(pyjson.Str(v))
}

// guidOf is a setup guid as a lower-case string; a missing one is "".
func guidOf(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return strings.ToLower(pyjson.Str(v))
}
