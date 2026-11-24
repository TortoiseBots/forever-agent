// liqcheck is an offline diagnostic: it plans one leg per consecutive pair of points on the navmesh's map 43 and
// says whether the planned points cross liquid (and, with noWater, whether the water-avoiding plan is dry). It needs
// no server and no capture: every point is the caller's own.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/pjerra/forever-agent/internal/navmesh"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = "usage: liqcheck <navmesh dir> x y z [x y z ...]  (each consecutive triple is a leg; map 43)"

// run plans every leg between the given points and prints one line per leg and variant.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 4 || (len(args)-1)%3 != 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	dir := args[0]
	var pts []navmesh.Vec3
	for i := 1; i+2 < len(args); i += 3 {
		x, errX := strconv.ParseFloat(args[i], 32)
		y, errY := strconv.ParseFloat(args[i+1], 32)
		z, errZ := strconv.ParseFloat(args[i+2], 32)
		if errX != nil || errY != nil || errZ != nil {
			fmt.Fprintf(stderr, "liqcheck: point %d is not three numbers: %q %q %q\n", i/3, args[i], args[i+1], args[i+2])
			return 2
		}
		pts = append(pts, navmesh.Vec3{float32(x), float32(y), float32(z)})
	}
	p := navmesh.NewForeverPlanner(dir, nil)
	defer p.Close()
	for i := 1; i < len(pts); i++ {
		from, to := pts[i-1], pts[i]
		for _, noWater := range []bool{false, true} {
			var path navmesh.Path
			var err error
			if noWater {
				path, err = p.PlanNoWater(43, from, to)
			} else {
				path, err = p.Plan(43, from, to)
			}
			if err != nil {
				fmt.Fprintf(stdout, "%d->%d nowater=%v ERROR %v\n", i-1, i, noWater, err)
				continue
			}
			wet, first, lz := 0, navmesh.Vec3{}, float32(0)
			all := append([][]navmesh.Vec3{path.Points}, path.Ground...)
			for _, seg := range all {
				for _, q := range seg {
					l, e := navmesh.LiquidAt(p, 43, q)
					if e == nil && l.Wet {
						wet++
						if wet == 1 {
							first, lz = q, l.Z
						}
					}
				}
			}
			if wet > 0 {
				fmt.Fprintf(stdout, "%d->%d nowater=%v %s len %.1f WET %d first %.1f %.1f %.1f level %.1f\n", i-1, i, noWater, path.Status, path.Length, wet, first[0], first[1], first[2], lz)
			} else {
				fmt.Fprintf(stdout, "%d->%d nowater=%v %s len %.1f dry\n", i-1, i, noWater, path.Status, path.Length)
			}
		}
	}
	return 0
}
