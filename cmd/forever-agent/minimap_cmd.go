package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/pjerra/forever-agent/internal/minimap"
)

const minimapUsage = "usage: forever-agent minimap-extract --client <Data dir> --out <dir> [--maps 70,209|all-instances] [--mmaps DIR] [--prefer minimap,worldmap,navmesh] [--list] [--verbose]\n" +
	"       forever-agent minimap-extract --forever (--client <extract dir> | --casc <install> [--product P]) --db2 <dbc dir> --out <dir> [...]\n" +
	"  reads the game client's MPQ archives and writes <out>/<map>.png + <map>.json for the dashboard (README \"Minimaps\")\n" +
	"  the minimap picture is the terrain tiles, the client's indoor WMO tiles, or the WMO tiles over the terrain\n" +
	"  with --forever: --db2 is the server's dbc/<locale> with Map.db2; --client is an outside CASC extractor's listfile folder,\n" +
	"  --casc the CASC install itself (.build.info): --casc also reads the world map (UiMapArt) textures by FileDataID"

// minimapCmd extracts the in-game minimaps of maps from the client the owner owns.
func minimapCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("minimap-extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "", "the client's Data folder (the MPQ archives; read only); with --forever, the CASC extractor's listfile-named folder")
	out := fs.String("out", "", "folder to write <map>.png and <map>.json into")
	maps := fs.String("maps", "all-instances", `map ids, comma separated, or "all-instances" (every dungeon and raid of Map.dbc)`)
	mmaps := fs.String("mmaps", "", "server mmaps folder(s), comma separated: a map with no minimap tiles gets a height-shaded floor render of its navmesh")
	prefer := fs.String("prefer", "", "order of the image sources for <map>.png, comma separated: minimap (the terrain and indoor WMO tiles), worldmap (the map picture of the area), navmesh (default worldmap,minimap,navmesh; with --forever minimap,navmesh)")
	forever := fs.Bool("forever", false, "extract the 1.60 client data: --client is an outside CASC extractor's folder, --casc the CASC install; --db2 is the server's extracted Map.db2 folder")
	casc := fs.String("casc", "", "with --forever: the CASC install folder (holding .build.info), whose FileDataIDs give the terrain and world-map textures; replaces --client")
	product := fs.String("product", "", "with --forever --casc: the .build.info product row to read when several are active")
	db2Dir := fs.String("db2", "", "with --forever: the server's dbc/<locale> folder holding Map.db2")
	list := fs.Bool("list", false, "only report what each map would get; write nothing")
	verbose := fs.Bool("verbose", false, "log every map and every skipped tile to stderr")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || (*out == "" && !*list) {
		fmt.Fprintln(stderr, minimapUsage)
		return exitUsage
	}
	if *forever {
		if *client != "" && *casc != "" {
			fmt.Fprintln(stderr, "minimap-extract: --forever takes --client (a CASC extractor's listfile folder) or --casc (the CASC install), not both: --casc replaces --client")
			return exitUsage
		}
		if *client == "" && *casc == "" {
			fmt.Fprintln(stderr, minimapUsage)
			return exitUsage
		}
		if *db2Dir == "" {
			fmt.Fprintln(stderr, "minimap-extract: --forever needs --db2 DIR (the server's dbc/<locale> folder with Map.db2)")
			return exitUsage
		}
		if *casc == "" && *product != "" {
			fmt.Fprintln(stderr, "minimap-extract: --product is a --casc flag (an install's build rows live in its .build.info)")
			return exitUsage
		}
	} else {
		if *client == "" {
			fmt.Fprintln(stderr, minimapUsage)
			return exitUsage
		}
		if *db2Dir != "" {
			fmt.Fprintln(stderr, "minimap-extract: --db2 is a --forever flag (the 1.12 client's maps come from Map.dbc inside its MPQ archives)")
			return exitUsage
		}
		if *casc != "" {
			fmt.Fprintln(stderr, "minimap-extract: --casc is a --forever flag (the 1.12 client's minimaps come from its MPQ archives)")
			return exitUsage
		}
		if *product != "" {
			fmt.Fprintln(stderr, "minimap-extract: --product is a --casc flag (with --forever)")
			return exitUsage
		}
	}
	ids, all, err := minimap.ParseMapList(*maps)
	if err != nil {
		fmt.Fprintln(stderr, "minimap-extract:", err)
		return exitUsage
	}
	var pref []string
	if *prefer != "" {
		pref, err = minimap.ParsePrefer(*prefer)
		if err != nil {
			fmt.Fprintln(stderr, "minimap-extract:", err)
			return exitUsage
		}
		if *forever {
			for _, p := range pref {
				if p == minimap.KindWorldMap && *casc == "" {
					fmt.Fprintf(stderr, "minimap-extract: --forever with --client has no %q image: --casc reads this client's world maps\n", p)
					return exitUsage
				}
			}
		}
	}
	opts := minimap.Options{ClientDir: expandHome(*client), CascDir: expandHome(*casc), Product: *product,
		DB2Dir: expandHome(*db2Dir), OutDir: expandHome(*out),
		MmapsDirs: splitDirs(*mmaps), Maps: ids, All: all, Prefer: pref, ListOnly: *list}
	if *verbose {
		opts.Log = func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) }
	}
	run := minimap.Run
	if *forever {
		run = minimap.RunForever
	}
	res, err := run(opts)
	if err != nil {
		fmt.Fprintln(stderr, "minimap-extract:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%-4s %-34s %-8s %-22s %-10s %s\n", "map", "name", "type", "image", "size", "other images")
	failed := 0
	for _, r := range res {
		size := "-"
		image := "-"
		if r.Kind != minimap.KindNone {
			size = fmt.Sprintf("%dx%d", r.Meta.Width, r.Meta.Height)
			if len(r.Layers) > 0 {
				image = r.Layers[0].Label()
			}
		}
		note := r.Note
		if r.Error != nil {
			failed++
			note = "ERROR: " + r.Error.Error()
		}
		var others []string
		for _, l := range r.Layers {
			if l.Kind != r.Kind || l.Floor != "" {
				others = append(others, l.Label())
			}
		}
		if len(others) > 0 {
			note = "also " + strings.Join(others, ",") + " " + note
		}
		fmt.Fprintf(stdout, "%-4d %-34s %-8s %-22s %-10s %s\n", r.ID, r.Name, typeName(r.Type), image, size, note)
	}
	if *out != "" && !*list {
		fmt.Fprintf(stdout, "wrote to %s\n", filepath.Clean(expandHome(*out)))
	}
	if failed > 0 {
		return 1
	}
	return exitOK
}

func typeName(t uint32) string {
	switch t {
	case minimap.InstanceParty:
		return "dungeon"
	case minimap.InstanceRaid:
		return "raid"
	case minimap.InstanceBG:
		return "bg"
	case minimap.InstanceArena:
		return "arena"
	}
	return "world"
}

// splitDirs reads a comma separated list of folders.
func splitDirs(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, expandHome(d))
		}
	}
	return out
}
