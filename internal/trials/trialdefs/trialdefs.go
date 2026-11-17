// Package trialdefs embeds the trial definition files unchanged from tortoise-trials
// (trials/<group>/*.json plus trials/_fixtures). Names are slash paths such as
// "tanks/tank-warrior-taunt-off-healer.json"; the content is never edited.
package trialdefs

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed */*.json
var files embed.FS

// Paths lists every embedded trial file in sorted order.
func Paths() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(fmt.Sprintf("trials trialdefs: %v", err))
	}
	var out []string
	for _, group := range entries {
		if !group.IsDir() {
			continue
		}
		names, err := files.ReadDir(group.Name())
		if err != nil {
			panic(fmt.Sprintf("trials trialdefs: %v", err))
		}
		for _, name := range names {
			if name.IsDir() || !strings.HasSuffix(name.Name(), ".json") {
				continue
			}
			out = append(out, group.Name()+"/"+name.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Read returns one embedded trial file.
func Read(path string) ([]byte, error) {
	return files.ReadFile(path)
}
