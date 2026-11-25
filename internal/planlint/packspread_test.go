package planlint

// The pack-spread rule's own tests: the world link the plan check asks, and the list of packs it flags in every plan
// whose map has a world export in the tree (the issue's pull request carries that list; the plans are not changed).

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

func TestPackLinkOracle(t *testing.T) {
	leader := worldspawn.Spawn{GUID: 1, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 0}, GroupLeader: 1, GroupFlags: worldspawn.AggroTogether}
	mate := worldspawn.Spawn{GUID: 2, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 60}, GroupLeader: 1, GroupFlags: worldspawn.AggroTogether}
	other := worldspawn.Spawn{GUID: 3, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 120}}
	w := &worldspawn.World{Map: 1, Spawns: []worldspawn.Spawn{leader, mate, other}}

	link := PackLinkOracle(w)
	at := func(x float32) dungeon.Point { return dungeon.Point{X: x} }
	if !link(at(0), at(60)) {
		t.Fatal("the two spawns of one AGGRO_TOGETHER group are not one pack")
	}
	if link(at(0), at(120)) {
		t.Fatal("two separate spawns are one pack")
	}
	if link(at(0), at(5000)) {
		t.Fatal("a point with no spawn near it is linked to something")
	}
	// A snapped position still finds its spawn (within PackLinkLookupYd), and past that it finds none.
	if !link(at(0.5), at(60.5+PackLinkLookupYd-1)) {
		t.Fatal("a point near its spawn did not find it")
	}
	if link(at(0), at(60+PackLinkLookupYd+1)) {
		t.Fatal("a point past the lookup radius found a spawn")
	}
	if PackLinkOracle(nil) != nil {
		t.Fatal("no world export gave an oracle")
	}

	// A formation-only group does not wake together: its far members are no pack, and the warning must stand.
	formLeader := worldspawn.Spawn{GUID: 10, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 0}, GroupLeader: 10, GroupFlags: worldspawn.FormationMove}
	formMate := worldspawn.Spawn{GUID: 11, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 40}, GroupLeader: 10, GroupFlags: worldspawn.FormationMove}
	// A creature_linking edge without an aggro flag wakens nothing a pull shares; one with it does.
	plain := worldspawn.Spawn{GUID: 12, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 80}}
	aggro := worldspawn.Spawn{GUID: 13, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 120}}
	flagged := &worldspawn.World{Map: 1, Spawns: []worldspawn.Spawn{formLeader, formMate, plain, aggro},
		Links: []worldspawn.Link{{Slave: 12, Master: 10, Flag: 0x0100}, {Slave: 13, Master: 12, Flag: 0x0001}}}
	lf := PackLinkOracle(flagged)
	if lf(at(0), at(40)) {
		t.Fatal("a formation-only group is one pack")
	}
	if !lf(at(80), at(120)) {
		t.Fatal("an aggro link is no pack")
	}
	if lf(at(0), at(80)) {
		t.Fatal("a link without an aggro flag is one pack")
	}

	// An older export with no group_flags column at all names only who leads whom: the leader rows are the group.
	bare := &worldspawn.World{Map: 1, Spawns: []worldspawn.Spawn{
		{GUID: 20, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 0}, GroupLeader: 20},
		{GUID: 21, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 30}, GroupLeader: 20},
		{GUID: 22, Entry: 100, Faction: 14, Pos: dungeon.Point{X: 200}},
	}}
	lb := PackLinkOracle(bare)
	if !lb(at(0), at(30)) {
		t.Fatal("the leader-only export's own group rows are no pack")
	}
	if lb(at(0), at(200)) {
		t.Fatal("a spawn outside any leader row is one pack")
	}
}

// TestPackSpreadOverEveryPlan prints the packs the plan check flags in every plan whose map has a world export in the
// tree, with the world data's creature_groups as the link: the list the issue's pull request carries.
func TestPackSpreadOverEveryPlan(t *testing.T) {
	files, err := filepath.Glob("../../dungeons/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("plan files: %v, %d", err, len(files))
	}
	flagged, plans := 0, 0
	for _, f := range files {
		p, err := dungeon.Load(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		dir := filepath.Join("../../tools/dungeongen/testdata", "map"+strconv.Itoa(int(p.Map)))
		if _, err := os.Stat(filepath.Join(dir, "creatures.tsv")); err != nil {
			continue
		}
		world, _, err := worldspawn.Load(dir, p.Map)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		plans++
		for _, issue := range p.PackSpreads(0, PackLinkOracle(world)) {
			flagged++
			t.Logf("pack spread in %s: %s", filepath.Base(f), issue.String())
		}
	}
	t.Logf("%d flagged packs in %d plans with a world export", flagged, plans)
}
