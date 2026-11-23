// plan_list_cmd.go: plan-list, one row per plan file of a folder. It is the only CLI that lists plans (the dashboard's
// /api/plans is the other reader), and it keeps the two facts apart: a plan's Turtle status and its Forever status
// (spec #102, round 2).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

const planListUsage = "usage: forever-agent plan-list [--dir dungeons] [--json]"

// planListInfo is one row: the plan's name and the facts that say which stack can run it.
type planListInfo struct {
	Name          string `json:"name"`
	Map           uint32 `json:"map"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	ForeverStatus string `json:"forever_status"`
}

// planListCmd prints one row per plan file in the folder, sorted by name: its map, its kind, the first word of its
// status and its forever_status (the whole value, free text and all).
func planListCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	asJSON := fs.Bool("json", false, "print the list as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, planListUsage)
		return exitUsage
	}
	files, err := filepath.Glob(filepath.Join(*dir, "*.json"))
	if err != nil {
		fmt.Fprintf(stderr, "plan-list: %v\n", err)
		return exitUsage
	}
	rows := make([]planListInfo, 0, len(files))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if !planName.MatchString(name) {
			continue // not a plan file: a scratch JSON beside the plans
		}
		p, err := dungeon.Load(f)
		if err != nil {
			fmt.Fprintf(stderr, "plan-list: %v\n", err)
			return exitUsage
		}
		kind := p.Kind
		if kind == "" {
			kind = dungeon.KindDungeon
		}
		word, _, _ := strings.Cut(p.Status, " ")
		rows = append(rows, planListInfo{Name: name, Map: p.Map, Kind: kind, Status: word, ForeverStatus: p.ForeverStatus})
	}
	if len(rows) == 0 {
		fmt.Fprintf(stderr, "plan-list: no plan files in %s\n", *dir)
		return exitUsage
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if *asJSON {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "plan-list: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stdout, "%s\n", b)
		return exitOK
	}
	fmt.Fprintln(stdout, "NAME\tMAP\tKIND\tSTATUS\tFOREVER")
	for _, r := range rows {
		fmt.Fprintf(stdout, "%s\t%d\t%s\t%s\t%s\n", r.Name, r.Map, r.Kind, r.Status, r.ForeverStatus)
	}
	return exitOK
}
