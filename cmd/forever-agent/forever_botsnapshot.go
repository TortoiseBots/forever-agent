package main

// `forever bot-snapshot`: build a bot snapshot (the file `trials realism` judges) from what a serve
// already reads for a character it runs and the bots it groups with.
//
// The snapshot format is internal/trials/realism's (see docs/manual/testing/realism.md). The taker
// fills a field only from a read path that exists today, and every field it cannot fill is stated in
// that bot's notes, so the checks answer INCONCLUSIVE with the reason instead of guessing:
//
//	filled   class and level (the `unit` row / `bot_view`), worn items with slot, item id, name,
//	         quality, item level and required level (`equipment` + `item_info`) — for bot1, the
//	         character the serve is logged in as (the agent's own character, which the bot module's
//	         realism work does not gear); a grouped bot's row carries class and level only.
//	empty    spec (no module read-out names a bot's spec), talents (the module proves learned spells
//	         only for ids it is asked to learn, never the whole build), enchants and an item's
//	         source (the equipment read names neither).
//
// A real character name is never written: bots are bot1, bot2, … in the file, and the names the
// command needs for its calls die with the process.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/realism"
)

const botSnapshotUsage = "usage: forever-agent forever bot-snapshot --dir DIR [--bots NAME,...] [--wait MS] [--out FILE]\n" +
	"  reads the serve in DIR (forever serve --dir DIR) and writes a bot snapshot: bot1 is the character\n" +
	"  the serve runs, with its class, level and worn items; each party bot has its class and level\n" +
	"  only (never a character name). Spec, talents, stats, enchants and item sources are not read on\n" +
	"  this tree: they stay empty and noted, and the checks that need them answer INCONCLUSIVE."

// serve calls bot-snapshot makes; each is a method the serve already registers.
const (
	botSnapshotCallTimeout = 30 * time.Second
	botSnapshotItemBatch   = 64 // item_info's own bound
)

// snapshotRPC is the serve surface the taker reads: the slot client and a test's fake both answer.
type snapshotRPC interface {
	Call(ctx context.Context, method string, params, out any, timeout time.Duration) error
}

// botSnapshotOptions is what the taker varies: which grouped bots to read, how long to let the
// serve's model catch up, and the clock the file's `taken` is stamped from.
type botSnapshotOptions struct {
	Bots   []string
	WaitMS int
	Now    func() time.Time
}

// botSnapshotStatus mirrors the `status` answer's fields the taker reads.
type botSnapshotStatus struct {
	Character string `json:"character"`
	GUID      string `json:"guid"`
	Level     int32  `json:"level"`
}

// botSnapshotUnit mirrors the `unit` answer's fields the taker reads.
type botSnapshotUnit struct {
	GUID  string `json:"guid"`
	Name  string `json:"name"`
	Class uint8  `json:"class"`
	Level int32  `json:"level"`
}

// botSnapshotEquipRow mirrors one `equipment` row.
type botSnapshotEquipRow struct {
	Slot      int    `json:"slot"`
	SlotName  string `json:"slot_name"`
	ItemEntry int32  `json:"item_entry"`
	Name      string `json:"name"`
	Quality   *int   `json:"quality"`
}

// botSnapshotBotView mirrors the `bot_view` answer's fields the taker reads.
type botSnapshotBotView struct {
	Bots []struct {
		Name string           `json:"name"`
		GUID string           `json:"guid"`
		Unit *botSnapshotUnit `json:"unit"`
	} `json:"bots"`
}

// botSnapshotItem is one `item_info` row's fields the taker reads.
type botSnapshotItem struct {
	ItemEntry     int32  `json:"item_entry"`
	Found         bool   `json:"found"`
	Name          string `json:"name"`
	Quality       int    `json:"quality"`
	ItemLevel     int    `json:"item_level"`
	RequiredLevel int    `json:"required_level"`
}

type botSnapshotItemInfo struct {
	Items []botSnapshotItem `json:"items"`
}

// botSnapshotNotes are the fields this taker cannot fill, per bot, so the report states the missing
// read-out instead of failing on it.
var (
	selfSnapshotNotes = []string{
		"spec: no read-out names this character's spec (the module's state answer carries no spec)",
		"talents: the module proves learned spells only for ids it is asked to learn, never the whole build",
		"enchant_id: the equipment read names no enchant",
		"stats_known: the equipment read names no item stats",
		"item source: no read-out says where a worn item comes from",
	}
	groupedSnapshotNotes = []string{
		"items: not read (equipment answers only for the character this serve runs)",
		"spec: no read-out names a bot's spec",
		"talents: no whole-build talent read-out",
	}
)

// takeBotSnapshot reads the serve into a snapshot, with the names it skipped (a party member the
// serve has no class for) for the command's stderr.
func takeBotSnapshot(ctx context.Context, c snapshotRPC, o botSnapshotOptions) (realism.Snapshot, []string, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	var status botSnapshotStatus
	if err := c.Call(ctx, "status", nil, &status, botSnapshotCallTimeout); err != nil {
		return realism.Snapshot{}, nil, fmt.Errorf("status: %w", err)
	}
	snap := realism.Snapshot{
		Format: realism.Format,
		Taken:  now().UTC().Format(time.RFC3339),
		Source: "forever bot-snapshot",
	}
	self, err := takeSelfBot(ctx, c, status)
	if err != nil {
		return realism.Snapshot{}, nil, err
	}
	snap.Bots = append(snap.Bots, self)

	bots, skipped, err := takeGroupedBots(ctx, c, o)
	if err != nil {
		return realism.Snapshot{}, nil, err
	}
	snap.Bots = append(snap.Bots, bots...)
	return snap, skipped, nil
}

// takeSelfBot reads the character the serve runs: its class from its own unit row, its items from
// `equipment` and their item levels from `item_info`.
func takeSelfBot(ctx context.Context, c snapshotRPC, status botSnapshotStatus) (realism.Bot, error) {
	bot := realism.Bot{ID: "bot1", Level: int(status.Level), ItemsKnown: true, Notes: append([]string(nil), selfSnapshotNotes...)}
	var unit botSnapshotUnit
	if err := c.Call(ctx, "unit", map[string]any{"guid": status.GUID}, &unit, botSnapshotCallTimeout); err != nil {
		return realism.Bot{}, fmt.Errorf("unit: %w", err)
	}
	bot.Class = realism.ClassName(unit.Class)
	if bot.Class == "" {
		return realism.Bot{}, fmt.Errorf("unit: the serve's own class id %d is not a class this build knows", unit.Class)
	}
	if bot.Level == 0 {
		bot.Level = int(unit.Level)
	}
	var rows []botSnapshotEquipRow
	if err := c.Call(ctx, "equipment", map[string]any{"names": true}, &rows, botSnapshotCallTimeout); err != nil {
		return realism.Bot{}, fmt.Errorf("equipment: %w", err)
	}
	items, err := readEquipment(ctx, c, rows)
	if err != nil {
		return realism.Bot{}, err
	}
	bot.Items = items
	return bot, nil
}

// readEquipment turns the equipment rows into snapshot items, reading item levels and qualities
// from `item_info` in one batch per 64 entries (the method's own bound).
func readEquipment(ctx context.Context, c snapshotRPC, rows []botSnapshotEquipRow) ([]realism.Item, error) {
	var entries []int32
	seen := map[int32]bool{}
	for _, row := range rows {
		if row.ItemEntry > 0 && !seen[row.ItemEntry] {
			seen[row.ItemEntry] = true
			entries = append(entries, row.ItemEntry)
		}
	}
	info := map[int32]botSnapshotItem{}
	for start := 0; start < len(entries); start += botSnapshotItemBatch {
		end := min(start+botSnapshotItemBatch, len(entries))
		var answer botSnapshotItemInfo
		if err := c.Call(ctx, "item_info", map[string]any{"entries": entries[start:end]}, &answer, botSnapshotCallTimeout); err != nil {
			return nil, fmt.Errorf("item_info: %w", err)
		}
		for _, it := range answer.Items {
			if it.Found {
				info[it.ItemEntry] = it
			}
		}
	}
	items := make([]realism.Item, 0, len(rows))
	for _, row := range rows {
		slot, known := realism.SlotName(row.SlotName)
		if !known || row.ItemEntry <= 0 {
			continue
		}
		item := realism.Item{Slot: slot, ItemID: int64(row.ItemEntry), Name: row.Name}
		if row.Quality != nil {
			q := *row.Quality
			item.Quality = &q
		}
		if it, ok := info[row.ItemEntry]; ok {
			item.ItemLevel = it.ItemLevel
			item.RequiredLevel = it.RequiredLevel
			if item.Name == "" {
				item.Name = it.Name
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// takeGroupedBots reads the party's bots (`bot_view`), in name order, as bot2, bot3, …; a member the
// serve has no class or level for is skipped and named for stderr.
func takeGroupedBots(ctx context.Context, c snapshotRPC, o botSnapshotOptions) ([]realism.Bot, []string, error) {
	var view botSnapshotBotView
	params := map[string]any{}
	if o.WaitMS > 0 {
		params["wait_ms"] = o.WaitMS
	}
	if len(o.Bots) > 0 {
		params["bots"] = o.Bots
	}
	if err := c.Call(ctx, "bot_view", params, &view, botSnapshotCallTimeout); err != nil {
		return nil, nil, fmt.Errorf("bot_view: %w", err)
	}
	wanted := map[string]bool{}
	for _, name := range o.Bots {
		wanted[strings.ToLower(strings.TrimSpace(name))] = true
	}
	rows := make([]botSnapshotUnit, 0, len(view.Bots))
	var skipped []string
	for _, row := range view.Bots {
		if len(wanted) > 0 && !wanted[strings.ToLower(row.Name)] {
			continue
		}
		if row.Unit == nil {
			skipped = append(skipped, row.Name)
			continue
		}
		unit := *row.Unit
		if unit.Name == "" {
			unit.Name = row.Name
		}
		if realism.ClassName(unit.Class) == "" || unit.Level <= 0 {
			skipped = append(skipped, row.Name)
			continue
		}
		rows = append(rows, unit)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].Name), strings.ToLower(rows[j].Name)
		if a != b {
			return a < b
		}
		return rows[i].GUID < rows[j].GUID
	})
	bots := make([]realism.Bot, 0, len(rows))
	for i, unit := range rows {
		bots = append(bots, realism.Bot{
			ID:      fmt.Sprintf("bot%d", i+2),
			Class:   realism.ClassName(unit.Class),
			Level:   int(unit.Level),
			Notes:   append([]string(nil), groupedSnapshotNotes...),
			Talents: nil,
		})
	}
	return bots, skipped, nil
}

// botSnapshotArgs is the parsed `forever bot-snapshot` command line.
type botSnapshotArgs struct {
	dir    string
	out    string
	bots   []string
	waitMS int
}

// parseBotSnapshotArgs parses the command line: flags in any position, then the checks the serve's
// own parameters need.
func parseBotSnapshotArgs(args []string, stderr io.Writer) (botSnapshotArgs, int) {
	fs := flag.NewFlagSet("forever bot-snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "the serve's dir (forever serve --dir DIR)")
	out := fs.String("out", "", "write the snapshot here instead of stdout")
	bots := fs.String("bots", "", "read only these grouped bots, comma-separated")
	wait := fs.Int("wait", 0, "milliseconds to let the serve's model catch up before the read (0-2000)")
	if err := fs.Parse(args); err != nil {
		return botSnapshotArgs{}, exitUsage
	}
	if fs.NArg() != 0 || *dir == "" {
		fmt.Fprintln(stderr, botSnapshotUsage)
		return botSnapshotArgs{}, exitUsage
	}
	if *wait < 0 || *wait > 2000 {
		fmt.Fprintln(stderr, "forever bot-snapshot: --wait takes 0-2000 ms (the serve's own bound)")
		return botSnapshotArgs{}, exitUsage
	}
	o := botSnapshotArgs{dir: *dir, out: *out, waitMS: *wait}
	if strings.TrimSpace(*bots) != "" {
		for _, name := range strings.Split(*bots, ",") {
			if name = strings.TrimSpace(name); name != "" {
				o.bots = append(o.bots, name)
			}
		}
	}
	return o, exitOK
}

// botsnapshotCmd runs `forever bot-snapshot`.
func botsnapshotCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("forever bot-snapshot", stderr)
	}
	o, code := parseBotSnapshotArgs(args, stderr)
	if code != exitOK {
		return code
	}
	c, err := openSlotClient(o.dir)
	if err != nil {
		var opened *slotOpenError
		if errors.As(err, &opened) {
			fmt.Fprintln(stderr, strings.Replace(opened.msg, "rpc:", "forever bot-snapshot:", 1))
			return opened.code
		}
		fmt.Fprintf(stderr, "forever bot-snapshot: %v\n", err)
		return exitUsage
	}
	snap, skipped, err := takeBotSnapshot(ctx, c, botSnapshotOptions{Bots: o.bots, WaitMS: o.waitMS})
	if err != nil {
		fmt.Fprintf(stderr, "forever bot-snapshot: %v\n", rpcScrub(err.Error(), c.token))
		return exitSlotRefused
	}
	for _, name := range skipped {
		fmt.Fprintf(stderr, "forever bot-snapshot: skipped %s: the serve proves no class and level for it\n", name)
	}
	out, err := realism.Marshal(snap)
	if err != nil {
		fmt.Fprintf(stderr, "forever bot-snapshot: %v\n", err)
		return exitUsage
	}
	// The command's own output goes through the loader before it is written: a serve whose status
	// has no level (a character not in the world, say) would otherwise write a file that `trials
	// realism` refuses, after the command reported success.
	if _, err := realism.Decode(bytes.NewReader(out)); err != nil {
		fmt.Fprintf(stderr, "forever bot-snapshot: the serve's data does not make a loadable snapshot: %v\n", err)
		return exitSlotRefused
	}
	if o.out != "" {
		if err := os.WriteFile(o.out, out, 0o644); err != nil {
			fmt.Fprintf(stderr, "forever bot-snapshot: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stdout, "wrote %s: %d bot(s)\n", o.out, len(snap.Bots))
		return exitOK
	}
	stdout.Write(out)
	return exitOK
}
