package main

// forever run-map and dungeon-watch --map over a hand-made watch record (testdata/runmap: a two-member party, one
// boss killed, one pull, one pause). The golden files hold the exact SVG and HTML for it. A different drawing fails
// the test; after a deliberate change run it once with -update-runmap-golden, which rewrites the files and still
// fails, so the new files are read in the diff and committed on purpose.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var updateRunMapGolden = flag.Bool("update-runmap-golden", false, "rewrite the run-map golden files (the test still fails)")

const fixtureTrace = "r-20261120T120000-cd34"

// runMapFixture copies the record into a fresh folder (the command writes beside it) and returns the folder and
// the plan's path.
func runMapFixture(t *testing.T) (dir, plan string) {
	t.Helper()
	dir = t.TempDir()
	for _, n := range []string{fixtureTrace + ".jsonl", fixtureTrace + ".forever-run.json", "plan.json"} {
		b, err := os.ReadFile(filepath.Join("testdata", "runmap", n))
		if err != nil {
			t.Fatal(err)
		}
		dst := dir
		if n == "plan.json" {
			dst = t.TempDir()
			plan = filepath.Join(dst, n)
		}
		if err := os.WriteFile(filepath.Join(dst, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, plan
}

func runRunMap(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"run-map"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "runmap", "golden", name)
	if *updateRunMapGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("%s rewritten: read the diff, commit it, and run the test again without -update-runmap-golden", name)
	}
	if want := readTestFile(t, path); got != want {
		t.Fatalf("%s differs from its golden file (%d bytes against %d); -update-runmap-golden rewrites it after a deliberate change", name, len(got), len(want))
	}
}

func TestRunMapWritesGoldenSVGAndHTML(t *testing.T) {
	dir, plan := runMapFixture(t)
	code, stdout, stderr := runRunMap(t, dir, "--plan", plan, "--html")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, fixtureTrace+".svg: ") || !strings.Contains(stdout, fixtureTrace+".html: ") {
		t.Fatalf("output names no files:\n%s", stdout)
	}
	svg := readTestFile(t, filepath.Join(dir, fixtureTrace+".svg"))
	html := readTestFile(t, filepath.Join(dir, fixtureTrace+".html"))
	golden(t, "run.svg", svg)
	golden(t, "run.html", html)
	// Slots, never the characters' names.
	for _, name := range []string{"Tanka", "Healera"} {
		if strings.Contains(svg, name) || strings.Contains(html, name) {
			t.Fatalf("the map holds the character name %q without --names", name)
		}
	}
}

// TestRunMapHTMLIsSelfContained: the replay loads nothing from outside: no script, stylesheet, font or image by
// URL, and a working slider and its script inline.
func TestRunMapHTMLIsSelfContained(t *testing.T) {
	dir, plan := runMapFixture(t)
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--html"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	html := readTestFile(t, filepath.Join(dir, fixtureTrace+".html"))
	for _, bad := range []string{`src="http`, `src='http`, `href="http`, `href='http`, "@import", "url(http", "<link"} {
		if strings.Contains(html, bad) {
			t.Fatalf("the replay reaches outside the file: %q", bad)
		}
	}
	if m := regexp.MustCompile(`<script[^>]*\ssrc=`).FindString(html); m != "" {
		t.Fatalf("the replay loads a script by src: %q", m)
	}
	for _, want := range []string{`type="range"`, "<script>"} {
		if !strings.Contains(html, want) {
			t.Fatalf("the replay has no %q", want)
		}
	}
}

func TestRunMapNoNavmeshNote(t *testing.T) {
	dir, plan := runMapFixture(t)
	code, _, stderr := runRunMap(t, dir, "--plan", plan)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "no navmesh background") {
		t.Fatalf("the log has no navmesh note: %s", stderr)
	}
	if svg := readTestFile(t, filepath.Join(dir, fixtureTrace+".svg")); !strings.Contains(svg, "no navmesh background") {
		t.Fatal("the image does not say it has no navmesh background")
	}
}

// TestRunMapBadNavmeshStillDraws: a navmesh folder with no tiles (or a build without cgo) never fails the map.
func TestRunMapBadNavmeshStillDraws(t *testing.T) {
	dir, plan := runMapFixture(t)
	code, _, stderr := runRunMap(t, dir, "--plan", plan, "--navmesh", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if svg := readTestFile(t, filepath.Join(dir, fixtureTrace+".svg")); !strings.Contains(svg, "no navmesh background") {
		t.Fatal("the image does not say why the background is missing")
	}
}

func TestRunMapNames(t *testing.T) {
	dir, plan := runMapFixture(t)
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--names"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if svg := readTestFile(t, filepath.Join(dir, fixtureTrace+".svg")); !strings.Contains(svg, "Tanka") {
		t.Fatal("--names did not label the routes with the names")
	}
}

func TestRunMapRefusesToOverwrite(t *testing.T) {
	dir, plan := runMapFixture(t)
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--html"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	svgPath := filepath.Join(dir, fixtureTrace+".svg")
	if err := os.WriteFile(svgPath, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runRunMap(t, dir, "--plan", plan, "--html")
	if code == 0 || !strings.Contains(stderr, "--force") {
		t.Fatalf("exit %d, stderr %q: an existing map was overwritten or the refusal does not name --force", code, stderr)
	}
	if got := readTestFile(t, svgPath); got != "keep" {
		t.Fatalf("the refused run changed the file: %q", got)
	}
	// Refused as a whole: with only the HTML in the way, the SVG is not written either.
	if err := os.Remove(svgPath); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runRunMap(t, dir, "--plan", plan, "--html"); code == 0 {
		t.Fatal("an existing replay was overwritten")
	}
	if _, err := os.Stat(svgPath); err == nil {
		t.Fatal("a refused run still wrote the SVG")
	}
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--html", "--force"); code != 0 {
		t.Fatalf("--force exit %d: %s", code, stderr)
	}
}

// TestRunMapRefusesAPathName: --out is one file name; a path (up out of the folder, a separator) is refused before
// anything is written.
func TestRunMapRefusesAPathName(t *testing.T) {
	dir, plan := runMapFixture(t)
	before := strings.Join(listDir(t, dir), ",")
	for _, name := range []string{"../x.svg", "sub/x.svg", `sub\x.svg`, "/tmp/x.svg", "..", ".hidden.svg", "a b.svg", ""} {
		args := []string{dir, "--plan", plan, "--out", name}
		if name == "" {
			continue // an empty --out is the default name
		}
		code, _, stderr := runRunMap(t, args...)
		if code != exitUsage {
			t.Fatalf("--out %q: exit %d, want %d", name, code, exitUsage)
		}
		if !strings.Contains(stderr, "--out") {
			t.Fatalf("--out %q: the refusal does not name the flag: %s", name, stderr)
		}
	}
	if after := strings.Join(listDir(t, dir), ","); after != before {
		t.Fatalf("a refused name wrote files: %s -> %s", before, after)
	}
	outside := filepath.Join(filepath.Dir(dir), "x.svg")
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("../x.svg was written")
	}
	// A plain name works and gets its extension.
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--out", "mine"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "mine.svg")); err != nil {
		t.Fatal(err)
	}
}

func TestRunMapArguments(t *testing.T) {
	dir, plan := runMapFixture(t)
	cases := [][]string{
		{},
		{dir, "extra"},
		{dir, "--plan", plan, "--floors", "sideways"},
		{dir, "--plan", plan, "--width", "-1"},
		{dir, "--plan", plan, "--from-s", "5", "--to-s", "3"},
		{dir, "--plan", plan, "--from-s", "-1"},
		{dir, "--plan", plan, "--parent", "nonsense"},
		{dir, "--plan", plan, "--bogus"},
	}
	for _, args := range cases {
		if code, _, _ := runRunMap(t, args...); code != exitUsage {
			t.Fatalf("args %v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if got := listDir(t, dir); len(got) != 2 {
		t.Fatalf("a refused call wrote files: %v", got)
	}
	if code, _, _ := runRunMap(t, filepath.Join(t.TempDir(), "nothing"), "--plan", plan); code == 0 {
		t.Fatal("a missing folder was accepted")
	}
}

// TestRunMapWindowAndTraceFile: a window draws one stretch, and a trace file works as the argument (the map is
// written beside it).
func TestRunMapWindowAndTraceFile(t *testing.T) {
	dir, plan := runMapFixture(t)
	trace := filepath.Join(dir, fixtureTrace+".jsonl")
	code, _, stderr := runRunMap(t, trace, "--plan", plan, "--from-s", "4", "--to-s", "12", "--out", "window.svg")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "window.svg")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "outside the run") {
		t.Fatalf("the log does not report the events outside the window: %s", stderr)
	}
}

// TestForeverWatchMapNeedsOut: --map writes beside the trace, so it needs --out.
func TestForeverWatchMapNeedsOut(t *testing.T) {
	code, _, stderr := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--map")
	if code != exitUsage || !strings.Contains(stderr, "--map needs --out") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// TestForeverWatchMapNeverFailsTheWatch: the dry watch with --map ends the same as without it; the map is one line
// of output, written or explained.
func TestForeverWatchMapNeverFailsTheWatch(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--out", out, "--map")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, ".svg: ") && !strings.Contains(stdout, "map: not written") {
		t.Fatalf("the watch says nothing about its map:\n%s", stdout)
	}
	if strings.Contains(stdout, "panic") {
		t.Fatalf("the map step panicked:\n%s", stdout)
	}
}

// hostileRunMapFixture is the record with character names that try to break out of the SVG and the HTML.
func hostileRunMapFixture(t *testing.T) (dir, plan string) {
	t.Helper()
	dir, plan = runMapFixture(t)
	path := filepath.Join(dir, fixtureTrace+".jsonl")
	b := readTestFile(t, path)
	b = strings.Replace(b, `"name":"Tanka"`, `"name":"</script><script>alert(1)</script>"`, 1)
	b = strings.Replace(b, `"name":"Healera"`, `"name":"\"><img src=x onerror=alert(2)>'&amp;"`, 1)
	if err := os.WriteFile(path, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, plan
}

// TestRunMapHostileNamesEndToEnd: names with script tags and quotes reach neither the SVG nor the HTML as markup.
func TestRunMapHostileNamesEndToEnd(t *testing.T) {
	cleanDir, plan := runMapFixture(t)
	if code, _, stderr := runRunMap(t, cleanDir, "--plan", plan, "--html", "--names"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	cleanHTML := readTestFile(t, filepath.Join(cleanDir, fixtureTrace+".html"))
	dir, plan := hostileRunMapFixture(t)
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--html", "--names"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	svg := readTestFile(t, filepath.Join(dir, fixtureTrace+".svg"))
	html := readTestFile(t, filepath.Join(dir, fixtureTrace+".html"))
	for what, doc := range map[string]string{"svg": svg, "html": html} {
		for _, bad := range []string{"<img", "alert(1)</script>", "<script>alert", `"><img`} {
			if strings.Contains(doc, bad) {
				t.Fatalf("%s holds the markup %q from a name", what, bad)
			}
		}
	}
	for _, tag := range []string{"<script", "</script"} {
		if got, want := strings.Count(html, tag), strings.Count(cleanHTML, tag); got != want {
			t.Fatalf("%d %q tags in the hostile replay, %d in the plain one", got, tag, want)
		}
	}
	if !strings.Contains(svg, "&lt;/script&gt;") {
		t.Fatal("the name is not shown escaped in the SVG")
	}
}

// TestRunMapForceNeverFollowsALink: an output name that is a link is refused, with --force too, and its target is
// not touched.
func TestRunMapForceNeverFollowsALink(t *testing.T) {
	dir, plan := runMapFixture(t)
	outside := filepath.Join(t.TempDir(), "precious.txt")
	if err := os.WriteFile(outside, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{outside, filepath.Join(filepath.Dir(outside), "missing.txt")} {
		for _, name := range []string{fixtureTrace + ".svg", fixtureTrace + ".html"} {
			link := filepath.Join(dir, name)
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("no symlinks here: %v", err)
			}
			code, _, stderr := runRunMap(t, dir, "--plan", plan, "--html", "--force")
			if code == 0 || !strings.Contains(stderr, "not a plain file") {
				t.Fatalf("%s -> %s: exit %d, stderr %q", name, filepath.Base(target), code, stderr)
			}
			if got := readTestFile(t, outside); got != "precious" {
				t.Fatalf("the file behind the link was written: %q", got)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "missing.txt")); err == nil {
				t.Fatal("the missing target behind the link was created")
			}
			if left := listDir(t, dir); len(left) != 3 { // trace, summary, the link, no others (no temp files)
				t.Fatalf("the refused run left files: %v", left)
			}
			os.Remove(link)
		}
	}
}

// TestRunMapForceKeepsTheOldSetWhenOneFileIsRefused: a refusal on the HTML leaves the SVG as it was.
func TestRunMapForceKeepsTheOldSetWhenOneFileIsRefused(t *testing.T) {
	dir, plan := runMapFixture(t)
	svgPath := filepath.Join(dir, fixtureTrace+".svg")
	if err := os.WriteFile(svgPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, fixtureTrace+".html"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runRunMap(t, dir, "--plan", plan, "--html", "--force")
	if code == 0 {
		t.Fatal("a folder in the way was accepted")
	}
	if got := readTestFile(t, svgPath); got != "old" {
		t.Fatalf("the old SVG was replaced although the set failed: %d bytes", len(got))
	}
	for _, n := range listDir(t, dir) {
		if strings.HasPrefix(n, ".run-map-") {
			t.Fatalf("a temp file was left behind: %s", n)
		}
	}
}

// TestRunMapForceRemovesStaleFloors: a rerun with fewer floors removes this run's old NAME-fN.svg files and nothing
// else.
func TestRunMapForceRemovesStaleFloors(t *testing.T) {
	dir, plan := runMapFixture(t)
	for _, n := range []string{fixtureTrace + "-f2.svg", fixtureTrace + "-f3.svg", "other-f2.svg", fixtureTrace + "-fx.svg"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--floors", "off", "--force"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := strings.Join(listDir(t, dir), ",")
	for _, gone := range []string{fixtureTrace + "-f2.svg", fixtureTrace + "-f3.svg"} {
		if strings.Contains(got, gone) {
			t.Fatalf("the stale %s is still there: %s", gone, got)
		}
	}
	for _, kept := range []string{"other-f2.svg", fixtureTrace + "-fx.svg", fixtureTrace + ".svg"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("%s is gone: %s", kept, got)
		}
	}
	// Without --force nothing is removed.
	if err := os.WriteFile(filepath.Join(dir, fixtureTrace+"-f2.svg"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, fixtureTrace+".svg"))
	if code, _, _ := runRunMap(t, dir, "--plan", plan, "--floors", "off"); code != 0 {
		t.Fatal("rerun failed")
	}
	if !strings.Contains(strings.Join(listDir(t, dir), ","), fixtureTrace+"-f2.svg") {
		t.Fatal("a file was removed without --force")
	}
}

func TestRunMapRejectsNonFiniteWindow(t *testing.T) {
	dir, plan := runMapFixture(t)
	for _, args := range [][]string{{"--from-s", "NaN"}, {"--to-s", "NaN"}, {"--from-s", "Inf"}, {"--to-s", "+Inf"}} {
		code, _, stderr := runRunMap(t, append([]string{dir, "--plan", plan}, args...)...)
		if code != exitUsage || !strings.Contains(stderr, "finite") {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
	}
}

// TestRunMapUppercaseExtension: an .SVG name is accepted and written lowercase; a run id ending .SVG still gets its
// default name.
func TestRunMapUppercaseExtension(t *testing.T) {
	dir, plan := runMapFixture(t)
	if code, _, stderr := runRunMap(t, dir, "--plan", plan, "--out", "Mine.SVG"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mine.svg")); err != nil {
		t.Fatal(err)
	}
	if got := defaultSVGName("r-1.SVG"); got != "r-1.svg" {
		t.Fatalf("default name %q", got)
	}
	if got := defaultSVGName("r-1"); got != "r-1.svg" {
		t.Fatalf("default name %q", got)
	}
}

func swapMapWriter(t *testing.T, f func(runMapOpts, io.Writer, io.Writer) error, timeout time.Duration) {
	t.Helper()
	oldW, oldT := mapWriter, watchMapTimeout
	mapWriter, watchMapTimeout = f, timeout
	t.Cleanup(func() { mapWriter, watchMapTimeout = oldW, oldT })
}

// TestWatchMapPanicKeepsTheWatchsExit: a panic in the map step is one log line; the watch's exit code is unchanged.
func TestWatchMapPanicKeepsTheWatchsExit(t *testing.T) {
	swapMapWriter(t, func(runMapOpts, io.Writer, io.Writer) error { panic("boom") }, time.Minute)
	out := t.TempDir()
	code, stdout, stderr := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--out", out, "--map")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "map: not written: the map step failed (panic: boom)") {
		t.Fatalf("the panic is not one log line:\n%s", stdout)
	}
}

// TestWatchMapHasADeadline: a map step that hangs is cut off, and the watch still ends.
func TestWatchMapHasADeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	swapMapWriter(t, func(runMapOpts, io.Writer, io.Writer) error { <-release; return nil }, 50*time.Millisecond)
	var out bytes.Buffer
	start := time.Now()
	writeWatchMap(context.Background(), &out, "x.jsonl", "", "")
	if time.Since(start) > 5*time.Second || !strings.Contains(out.String(), "stopped after") {
		t.Fatalf("no deadline: %q after %s", out.String(), time.Since(start))
	}
}

// TestWatchMapHonoursCtrlC: a cancelled context ends the map step at once.
func TestWatchMapHonoursCtrlC(t *testing.T) {
	var started atomic.Bool
	release := make(chan struct{})
	defer close(release)
	swapMapWriter(t, func(runMapOpts, io.Writer, io.Writer) error {
		started.Store(true)
		<-release
		return errors.New("late")
	}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for !started.Load() {
			runtime.Gosched()
		}
		cancel()
	}()
	var out bytes.Buffer
	writeWatchMap(ctx, &out, "x.jsonl", "", "")
	if !strings.Contains(out.String(), "interrupted") {
		t.Fatalf("Ctrl-C was not honoured: %q", out.String())
	}
}
