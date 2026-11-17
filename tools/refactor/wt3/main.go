// Command refactor-world-types-3 rewrites the step-3 files of the
// FOREVER-AGENT-WORLD-TYPES brief: objects.Unit/Aura/Cast/Item/GameObject/
// DynamicObject/QuestSlot/Filter/GOFilter/GOTypeByName/GOTypeName become
// world.X in every package outside the protocol layer, adding the world
// import and dropping the objects import only when no other objects.* use
// remains. Be consistent per package: all files in the list, tests included.
//
// Usage: go run ./tools/refactor/world-types-3 <file-list>
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var names = []string{
	"Unit", "Aura", "Cast", "Item", "GameObject", "DynamicObject",
	"QuestSlot", "Filter", "GOFilter", "GOTypeByName", "GOTypeName",
}

var (
	objRe   = regexp.MustCompile(`\bobjects\.(Unit|Aura|Cast|Item|GameObject|DynamicObject|QuestSlot|Filter|GOFilter|GOTypeByName|GOTypeName)\b`)
	anyObj  = regexp.MustCompile(`\bobjects\.[A-Z][A-Za-z0-9_]*\b`)
	objImp  = regexp.MustCompile(`(?m)^\t"github\.com/pjerra/forever-agent/internal/objects"\n`)
	worldIm = "github.com/pjerra/forever-agent/internal/world"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: world-types-3 <file-list>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fails := 0
	for _, f := range strings.Fields(string(raw)) {
		if err := rewrite(f); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			fails++
		}
	}
	if fails > 0 {
		os.Exit(1)
	}
}

func rewrite(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(b)
	if !objRe.MatchString(src) {
		return fmt.Errorf("no step-3 objects.* name found")
	}
	out := objRe.ReplaceAllString(src, "world.$1")

	// Add the world import after the objects import line when missing.
	if !strings.Contains(out, "\t\""+worldIm+"\"\n") {
		m := objImp.FindStringIndex(out)
		if m == nil {
			return fmt.Errorf("no objects import to anchor the world import")
		}
		out = out[:m[1]] + "\t\"" + worldIm + "\"\n" + out[m[1]:]
	}

	// Drop the objects import when nothing names objects.* anymore.
	if !anyObj.MatchString(out) {
		n := objImp.FindStringIndex(out)
		if n == nil {
			return fmt.Errorf("objects import already gone but objects.* still used?")
		}
		out = out[:n[0]] + out[n[1]:]
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
