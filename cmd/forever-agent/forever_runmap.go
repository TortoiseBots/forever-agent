package main

// forever run-map: draw a finished (or cut-off) dungeon watch as a map. It reads the folder `dungeon-watch --out`
// wrote (trace, run summary, optional combat log) through runmap.Load and writes one SVG per floor and, with
// --html, a single self-contained HTML replay with a time slider. It needs no login and no server. The same writer
// runs after a watch with `dungeon-watch --map`.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/runmap"
	"github.com/pjerra/forever-agent/internal/mapview"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

const runMapUsage = "usage: forever-agent forever run-map DIR [--plan FILE] [--navmesh DIR [--parent MAP=PARENT[,..]]] [--out NAME.svg] [--html] [--names] [--floors auto|off|z1,z2] [--width PX] [--from-s N] [--to-s N] [--force]"

// runMapOpts is what writeRunMap needs: the watch folder and how to draw it. Out is a file name (one path
// component) written into Dir; empty is the run's id.
type runMapOpts struct {
	Dir, Plan, Navmesh, Out string
	Parents                 map[uint32]uint32
	HTML, Names, Force      bool
	Floors                  string
	Width                   int
	FromS, ToS              float64
	in                      string // the trace file when Dir was one
}

var outNameChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// svgName checks an output name and returns it with its .svg extension: one path component (no separator, no "..",
// no leading dot); an .svg extension in any case is the extension (written lowercase). Nothing is written before it passes.
func svgName(name string) (string, error) {
	if name == "" {
		return "", errors.New("the output name is empty")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
		return "", fmt.Errorf("--out %q must be one file name, not a path", name)
	}
	if !outNameChars.MatchString(name) {
		return "", fmt.Errorf("--out %q: use letters, digits, dot, dash and underscore, starting with a letter or digit", name)
	}
	ext := filepath.Ext(name)
	switch {
	case ext == "":
		return name + ".svg", nil
	case strings.EqualFold(ext, ".svg"):
		return strings.TrimSuffix(name, ext) + ".svg", nil
	}
	// A dotted run id ("a.b") is a stem, not an extension.
	return name + ".svg", nil
}

// defaultSVGName is the output name when --out is not given: the run id as a safe stem, never run through the
// user-name check (an id ending in .SVG is just a stem).
func defaultSVGName(id string) string {
	if strings.EqualFold(filepath.Ext(id), ".svg") {
		id = strings.TrimSuffix(id, filepath.Ext(id))
	}
	return safeStem(id) + ".svg"
}

// stemOf is the name without its .svg extension.
func stemOf(svg string) string { return strings.TrimSuffix(svg, ".svg") }

func foreverRunMapCmd(_ context.Context, args []string, stdout, stderr io.Writer) int {
	var dir string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		dir, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("forever run-map", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o runMapOpts
	fs.StringVar(&o.Plan, "plan", "", "the dungeon plan that was watched (default: the summary's plan name under dungeons/)")
	fs.StringVar(&o.Navmesh, "navmesh", "", "ForeverCore mmaps folder for the floor background (without it the map has none, and says so)")
	parents := fs.String("parent", "", "instance rule for the background: MAP=PARENT pairs, comma separated (with --navmesh)")
	fs.StringVar(&o.Out, "out", "", "the SVG's file name (one name, written into DIR; default <run id>.svg); several floors write NAME-f1.svg, ...")
	fs.BoolVar(&o.HTML, "html", false, "also write the single-file HTML replay with a time slider, next to the SVG")
	fs.BoolVar(&o.Names, "names", false, "label the routes with the characters' names (default: the party slots)")
	fs.StringVar(&o.Floors, "floors", "auto", `how to split a map into floors: auto (from the navmesh), off, or heights such as "-30,-10"`)
	fs.IntVar(&o.Width, "width", 960, "the longest side of the map in pixels")
	fs.Float64Var(&o.FromS, "from-s", 0, "only what happened at or after this second of the trace")
	fs.Float64Var(&o.ToS, "to-s", 0, "only what happened at or before this second of the trace (0: to the end)")
	fs.BoolVar(&o.Force, "force", false, "replace existing output files")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if dir == "" && fs.NArg() == 1 {
		dir = fs.Arg(0)
	} else if fs.NArg() != 0 {
		dir = ""
	}
	if dir == "" {
		fmt.Fprintln(stderr, runMapUsage)
		return exitUsage
	}
	o.Dir = dir
	var err error
	if o.Parents, err = parseParentPairs(*parents); err != nil {
		fmt.Fprintln(stderr, "forever run-map: --parent:", err)
		return exitUsage
	}
	if err := o.check(); err != nil {
		fmt.Fprintln(stderr, "forever run-map:", err)
		return exitUsage
	}
	if err := writeRunMap(o, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "forever run-map:", err)
		return exitWorld
	}
	return exitOK
}

// check validates what needs no file: the name, the floors, the width and the window.
func (o runMapOpts) check() error {
	if o.Out != "" {
		if _, err := svgName(o.Out); err != nil {
			return err
		}
	}
	if _, _, err := mapview.ParseCuts(o.floors()); err != nil {
		return err
	}
	if o.Width < 0 || o.Width > 8000 {
		return errors.New("--width is 0 (default) to 8000 pixels")
	}
	if math.IsNaN(o.FromS) || math.IsInf(o.FromS, 0) || math.IsNaN(o.ToS) || math.IsInf(o.ToS, 0) {
		return errors.New("--from-s and --to-s must be finite numbers")
	}
	if o.FromS < 0 || o.ToS < 0 || (o.ToS > 0 && o.ToS <= o.FromS) {
		return errors.New("--from-s and --to-s must be seconds into the trace, with --to-s after --from-s")
	}
	return nil
}

func (o runMapOpts) floors() string {
	if o.Floors == "" {
		return "auto"
	}
	return o.Floors
}

// writeRunMap loads the watch folder and writes its map files into it. Every file name is checked, and none may
// exist (without Force), before the first byte is written. Notes about the record go to log.
func writeRunMap(o runMapOpts, stdout, log io.Writer) error {
	if err := o.check(); err != nil {
		return err
	}
	if st, err := os.Stat(o.Dir); err == nil && !st.IsDir() {
		// A trace file: the map goes beside it, and it is the run that is drawn.
		o.in, o.Dir = o.Dir, filepath.Dir(o.Dir)
	}
	in := o.in
	if in == "" {
		in = o.Dir
	}
	l, err := runmap.LoadRun(in, runmap.Options{PlanPath: o.Plan, Names: o.Names, FromS: o.FromS, ToS: o.ToS})
	if err != nil {
		return err
	}
	name := o.Out
	if name == "" {
		// The default is built from the run id, never run through the user-name check: an id that ends in .SVG
		// or .svg is just a stem.
		name = defaultSVGName(l.ID)
	} else if name, err = svgName(name); err != nil {
		return err
	}
	cuts, auto, _ := mapview.ParseCuts(o.floors())
	run := l.Run
	bb, ok := run.Bounds(mapview.DefaultPad, 60, l.Overlay.Points()...)
	if !ok {
		return fmt.Errorf("no position on map %d: nothing to draw (the record holds no route)", run.Map)
	}
	var (
		polys []navmesh.Polygon
		trunc bool
		note  = "no navmesh background: --navmesh was not given"
	)
	if o.Navmesh != "" {
		planner := navmesh.NewForeverPlanner(o.Navmesh, o.Parents)
		defer planner.Close()
		polys, trunc, note = mapview.LoadBackground(planner, run.Map, bb)
	}
	scene, err := mapview.NewScene(run, mapview.SceneOptions{Box: &bb, Cuts: cuts, Auto: auto, Polys: polys,
		PolysTruncated: trunc, BackgroundNote: note, Overlay: l.Overlay, MaxSide: o.Width, Config: mapview.DefaultConfig()})
	if err != nil {
		return err
	}
	// The outputs are staged as temp files in the real (symlink-resolved) folder and renamed into place only when
	// every one was written, so a failure leaves the old set untouched. Each floor is drawn when it is staged and
	// not kept, so the set is never held in memory at once.
	real, err := filepath.EvalSymlinks(o.Dir)
	if err != nil {
		return err
	}
	stem := stemOf(name)
	st := &stager{dir: real}
	defer st.cleanup()
	n := scene.FloorCount()
	for i := 0; i < n; i++ {
		fn := name
		if n > 1 {
			fn = fmt.Sprintf("%s-f%d.svg", stem, i+1)
		}
		if err := st.stage(fn, o.Force, scene.SVG(i)); err != nil {
			return err
		}
	}
	if o.HTML {
		data, err := scene.HTML()
		if err != nil {
			return err
		}
		if err := st.stage(stem+".html", o.Force, data); err != nil {
			return err
		}
	}
	if err := st.commit(o.Force); err != nil {
		return err
	}
	for _, f := range st.files {
		fmt.Fprintf(stdout, "%s: %d bytes\n", f.name, f.size)
	}
	if o.Force {
		removeStale(real, stem, n)
	}
	fmt.Fprintf(log, "run-map: map %d, %d members, %d creatures, %d markers, %d floors; %s\n", run.Map,
		len(run.Tracks), len(run.Creatures), len(run.Markers), scene.FloorCount(), strings.Join(scene.Notes, "; "))
	return nil
}

var stemUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// safeStem makes a run id into a file stem: unsafe characters become underscores, a leading dot or dash is dropped.
func safeStem(id string) string {
	s := strings.TrimLeft(stemUnsafe.ReplaceAllString(id, "_"), ".-_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" {
		return "run"
	}
	return s
}

// stager writes a set of output files into one folder all-or-nothing.
type stager struct {
	dir   string
	files []stagedFile
	done  []string // finals created by this run, removed again if a later one fails
}

type stagedFile struct {
	name, tmp string
	size      int
}

// stage checks the destination (a symlink or any non-regular file is never touched, even with force; an existing
// file needs force) and writes the data to a new temp file in the folder. Temp files are created with O_EXCL, which
// never follows a link.
func (s *stager) stage(name string, force bool, data []byte) error {
	final := filepath.Join(s.dir, name)
	if rel, err := filepath.Rel(s.dir, final); err != nil || rel != name {
		return fmt.Errorf("%s would leave the output folder", name)
	}
	if fi, err := os.Lstat(final); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s exists and is not a plain file (a link or a folder is never replaced); nothing was written", name)
		}
		if !force {
			return fmt.Errorf("%s exists (--force replaces it); nothing was written", name)
		}
	}
	f, err := os.CreateTemp(s.dir, ".run-map-*.tmp")
	if err != nil {
		return err
	}
	s.files = append(s.files, stagedFile{name: name, tmp: f.Name(), size: len(data)})
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// commit moves the staged files into place. Without force a final that appeared meanwhile makes os.Link fail, and
// then every file this run already placed is removed again.
func (s *stager) commit(force bool) error {
	for _, f := range s.files {
		if err := os.Chmod(f.tmp, 0o600); err != nil {
			return err
		}
	}
	for _, f := range s.files {
		final := filepath.Join(s.dir, f.name)
		if force {
			if fi, err := os.Lstat(final); err == nil && !fi.Mode().IsRegular() {
				s.rollback()
				return fmt.Errorf("%s is not a plain file; nothing was replaced", f.name)
			}
			if err := os.Rename(f.tmp, final); err != nil {
				return err
			}
			continue
		}
		if err := os.Link(f.tmp, final); err != nil {
			s.rollback()
			return fmt.Errorf("%s: %w; nothing was written", f.name, err)
		}
		s.done = append(s.done, final)
	}
	return nil
}

func (s *stager) rollback() {
	for _, p := range s.done {
		os.Remove(p)
	}
	s.done = nil
}

// cleanup removes the temp files that were not renamed away.
func (s *stager) cleanup() {
	for _, f := range s.files {
		os.Remove(f.tmp)
	}
}

// removeStale deletes, after a successful --force run, the plain files this command's naming would have written for
// the same run name but that the new set no longer has: NAME-fK.svg beyond the floor count, and NAME.svg when the
// run now has several floors. A link or anything else of that name is left alone.
func removeStale(dir, stem string, floors int) {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(stem) + `-f([0-9]+)\.svg$`)
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		stale := false
		if m := re.FindStringSubmatch(e.Name()); m != nil {
			k, err := strconv.Atoi(m[1])
			stale = err == nil && (floors <= 1 || k > floors)
		} else if floors > 1 && e.Name() == stem+".svg" {
			stale = true
		}
		if stale && e.Type().IsRegular() {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
