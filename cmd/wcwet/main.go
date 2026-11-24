// wcwet is an offline diagnostic: it reads a walk's TSV (one row per waypoint leg) and plans each leg on the
// navmesh's map 43, reporting every leg whose planned points cross liquid — the first wet point, the surface, the
// ground under it and the depth — or, with -tsv, the legs' endpoints for a later pass. It needs no server and no
// capture: every row is the caller's own.
package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/navmesh"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = "usage: wcwet <navmesh dir> <walk.tsv> [-tsv]  (the TSV's rows are tab-separated; map 43)"

type row struct {
	leg, seq   int
	name, kind string
	x, y, z    float64
}

func f(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// run reads the TSV, plans every consecutive leg and prints the wet ones.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	dir := args[0]
	tsv := args[1]
	asTsv := len(args) > 2 && args[2] == "-tsv"
	fh, err := os.Open(tsv)
	if err != nil {
		fmt.Fprintf(stderr, "wcwet: %v\n", err)
		return 1
	}
	defer fh.Close()
	var rows []row
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 4096*1024)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 14 || p[0] == "P" || p[0] == "U" {
			continue
		}
		leg, _ := strconv.Atoi(p[0])
		seq, _ := strconv.Atoi(p[2])
		rows = append(rows, row{leg: leg, seq: seq, name: p[1], kind: p[3], x: f(p[5]), y: f(p[6]), z: f(p[7])})
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(stderr, "wcwet: %v\n", err)
		return 1
	}
	pl := navmesh.NewForeverPlanner(dir, nil)
	defer pl.Close()
	water := uint16(0x04)
	// sample a leg every <= 1.5 yd, both ends
	sample := func(add func(x, y, z float32), a, b [3]float32) {
		dx, dy, dz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
		l := math.Sqrt(float64(dx*dx + dy*dy + dz*dz))
		n := 1
		if l > 1.5 {
			n = int(math.Ceil(l / 1.5))
		}
		for i := 0; i <= n; i++ {
			t := float32(i) / float32(n)
			add(a[0]+t*dx, a[1]+t*dy, a[2]+t*dz)
		}
	}
	type wetHit struct{ x, y, z, surf, ground, depth float32 }
	totalWet := 0
	for i := 1; i < len(rows); i++ {
		a := [3]float32{float32(rows[i-1].x), float32(rows[i-1].y), float32(rows[i-1].z)}
		b := [3]float32{float32(rows[i].x), float32(rows[i].y), float32(rows[i].z)}
		path, err := pl.Plan(43, navmesh.Vec3{a[0], a[1], a[2]}, navmesh.Vec3{b[0], b[1], b[2]})
		if err != nil {
			fmt.Fprintf(stdout, "leg %d %-14s seq %3d %-9s ERROR %v\n", rows[i].leg, rows[i].name, rows[i].seq, rows[i].kind, err)
			continue
		}
		var hits []wetHit
		seen := map[[3]int32]bool{}
		check := func(x, y, z float32) {
			key := [3]int32{int32(math.Round(float64(x) * 10)), int32(math.Round(float64(y) * 10)), int32(math.Round(float64(z) * 10))}
			if seen[key] {
				return
			}
			seen[key] = true
			if z < -100000 {
				return
			}
			// The module's point rule: a liquid whose surface is at or above the walked height (z - 0.6 slack), asked
			// in the point's own column, like the core's TerrainInfo liquid read.
			fl, e := pl.Floors(43, x, y, z-0.6, z+35.0)
			if e != nil {
				return
			}
			var surf float32
			found := false
			for _, q := range fl {
				if q.Flags&water == 0 {
					continue
				}
				if !found || q.Z < surf {
					surf, found = q.Z, true
				}
			}
			if !found {
				return
			}
			var g float32
			gfound := false
			gs, e3 := pl.Floors(43, x, y, surf-40.0, surf+0.5)
			if e3 == nil {
				for _, q := range gs {
					if q.Flags&water != 0 {
						continue
					}
					if !gfound || q.Z > g {
						g, gfound = q.Z, true
					}
				}
			}
			hit := wetHit{x: x, y: y, z: z, surf: surf}
			if gfound {
				hit.ground = g
			}
			hit.depth = surf - z
			if hit.depth < 0 {
				hit.depth = 0
			}
			hits = append(hits, hit)
		}
		for j := 1; j < len(path.Points); j++ {
			sample(check, [3]float32(path.Points[j-1]), [3]float32(path.Points[j]))
		}
		for _, grp := range path.Ground {
			for j := 1; j < len(grp); j++ {
				sample(check, [3]float32(grp[j-1]), [3]float32(grp[j]))
			}
		}
		if len(hits) == 0 {
			continue
		}
		totalWet++
		// max depth among hits
		maxd := float32(-1)
		shallowOnly := true
		for _, h := range hits {
			if h.depth < 0 {
				shallowOnly = false
			}
			if h.depth > maxd {
				maxd = h.depth
			}
		}
		h := hits[0]
		for _, q := range hits {
			if q.depth > h.depth && q.depth == maxd {
				h = q
				break
			}
		}
		endWet := false
		endDepth := float32(-1)
		if fs, e := pl.Floors(43, b[0], b[1], b[2]-0.6, b[2]+35.0); e == nil {
			var surf float32
			for _, q := range fs {
				if q.Flags&water != 0 && (!endWet || q.Z < surf) {
					surf, endWet = q.Z, true
				}
			}
			if endWet {
				endDepth = surf - b[2]
			}
		}
		if asTsv {
			fmt.Fprintf(stdout, "%d\t%d\t%s\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n", rows[i].leg, rows[i].seq, rows[i].kind,
				rows[i-1].x, rows[i-1].y, rows[i-1].z, rows[i].x, rows[i].y, rows[i].z)
			continue
		}
		fmt.Fprintf(stdout, "WET leg %d %-14s seq %3d %-9s from %.1f %.1f %.1f to %.1f %.1f %.1f hits %d first %.1f %.1f %.1f surf %.1f ground %.1f depth %.2f maxdepth %.2f shallowOnly %v status %s endWet %v endDepth %.2f\n",
			rows[i].leg, rows[i].name, rows[i].seq, rows[i].kind, rows[i-1].x, rows[i-1].y, rows[i-1].z, rows[i].x, rows[i].y, rows[i].z, len(hits), h.x, h.y, h.z, h.surf, h.ground, h.depth, maxd, shallowOnly, path.Status, endWet, endDepth)
	}
	if !asTsv {
		fmt.Fprintf(stdout, "wet hops: %d of %d\n", totalWet, len(rows)-1)
	}
	return 0
}
