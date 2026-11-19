//go:build cgo

package world

// The walker's own port over real sockets: the link dies on the first movement packet, in the middle of a navmesh
// walk (POSITION walks to its stand). The build tag is cgo's because the planner needs the Detour engine; the
// adapter's translation is also pinned without sockets in dungeon_session_test.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// TestDungeonRunLinkDeathDuringWalk: the fake drops the link on the first CMSG_MOVE_START_FORWARD.
func TestDungeonRunLinkDeathDuringWalk(t *testing.T) {
	sc := &dungeonScene{x: 12, y: 12, z: 0} // the fixture's tile
	sc.standOffset = -40                    // POSITION walks 40 yd north before the pull
	sc.dropOnMove = true
	cfg := dungeonConfig(t, sc, "none")
	// One flat polygon over the start and the stand.
	dir := navfixture.WriteForeverDirAt(t, 0, []navfixture.Poly{{
		{2, 2, 0}, {62, 2, 0}, {62, 62, 0}, {2, 62, 0},
	}}, nil, sc.x, sc.y)

	f := newFakeServer(t)
	f.dungeon = sc
	f.clientMayLeave = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	o := dungeonOptions(f, &cfg, 2*time.Second)
	o.NavmeshDir = dir
	report, err := Enter(t.Context(), o)
	f.wait(t)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	if report.Dungeon.Exit != driver.ExitLinkLost {
		t.Fatalf("exit %d, want %d (stop %q states %v)", report.Dungeon.Exit, driver.ExitLinkLost,
			report.Dungeon.Stop, report.Dungeon.States)
	}
	if report.Dungeon.Stop != "link-lost" {
		t.Fatalf("stop %q, want link-lost", report.Dungeon.Stop)
	}
	sc.mu.Lock()
	moved := sc.sawMove
	sc.mu.Unlock()
	if !moved {
		t.Fatal("the walk never started")
	}
}
