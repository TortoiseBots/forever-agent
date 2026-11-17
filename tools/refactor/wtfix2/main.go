// Command wtfix2 audits the world-types rewrite for comment and string
// mentions that name symbols which no longer (or never did) exist: every
// world.<Name> on an added diff line must be an exported identifier of
// internal/world, and every objects./session./game.<Name> on an added line
// must still be declared in that package when the type moved to world.
// Usage: go run ./tools/refactor/wtfix2 (no args); run from the repo root.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

var mentionRe = regexp.MustCompile(`\b(world|objects|session|game)\.([A-Za-z_][A-Za-z0-9_]*)`)

// pkgIdents collects the exported package-level identifiers declared in dir's
// non-test Go files: type names (aliases included), top-level funcs, and
// const/var names including members of GenDecl blocks.
func pkgIdents(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), dir+"/"+name, nil, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", name, err)
			continue
		}
		for _, d := range f.Decls {
			switch t := d.(type) {
			case *ast.GenDecl:
				for _, s := range t.Specs {
					switch sp := s.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(sp.Name.Name) {
							out[sp.Name.Name] = true
						}
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							if ast.IsExported(n.Name) {
								out[n.Name] = true
							}
						}
					}
				}
			case *ast.FuncDecl:
				if t.Recv == nil && ast.IsExported(t.Name.Name) {
					out[t.Name.Name] = true
				}
			}
		}
	}
	return out
}

// whereAt walks the line once and reports, for the mention starting at byte
// offset off, whether it sits in code, a "..." or `...` string, or a //
// comment. A backtick string spanning lines classifies as code; every hit is
// reviewed by hand against the file, so a wrong label only costs a look.
func whereAt(line string, off int) string {
	state, esc := 0, false // 0 code, 1 double-quoted, 2 raw
	for i := range len(line) {
		if i == off {
			switch state {
			case 0:
				return "code"
			case 1, 2:
				return "string"
			}
		}
		if i > off {
			break
		}
		c := line[i]
		switch state {
		case 0:
			if c == '/' && i+1 < len(line) && line[i+1] == '/' {
				return "comment"
			} else if c == '"' {
				state = 1
			} else if c == '`' {
				state = 2
			}
		case 1:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				state = 0
			}
		case 2:
			if c == '`' {
				state = 0
			}
		}
	}
	return "comment"
}

func main() {
	out, err := exec.Command("git", "diff", "main", "refactor/world-types", "--", "*.go").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	world := pkgIdents("internal/world")
	olds := map[string]map[string]bool{
		"objects": pkgIdents("internal/objects"),
		"session": pkgIdents("internal/session"),
		"game":    pkgIdents("internal/game"),
	}

	type hit struct {
		file, line, pkg, name, where, status string
	}
	var hits []hit
	file, newLn := "", 0
	for _, raw := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(raw, "+++ b/"):
			file = strings.TrimPrefix(raw, "+++ b/")
		case strings.HasPrefix(raw, "@@"):
			var a, b, c, d int
			fmt.Sscanf(raw, "@@ -%d,%d +%d,%d @@", &a, &b, &c, &d)
			newLn = c
		case strings.HasPrefix(raw, "+") && !strings.HasPrefix(raw, "+++"):
			body := raw[1:]
			for _, m := range mentionRe.FindAllStringSubmatchIndex(body, -1) {
				pkg, id := body[m[2]:m[3]], body[m[4]:m[5]]
				status := ""
				if pkg == "world" {
					if !world[id] {
						status = "STALE-not-in-world"
					}
				} else if world[id] && !olds[pkg][id] {
					status = "STALE-moved-no-alias"
				}
				if status != "" {
					hits = append(hits, hit{file, fmt.Sprint(newLn), pkg, id, whereAt(body, m[0]), status})
				}
			}
			newLn++
		case strings.HasPrefix(raw, "-"), strings.HasPrefix(raw, "---"):
		default:
			if file != "" {
				newLn++
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].file != hits[j].file {
			return hits[i].file < hits[j].file
		}
		return hits[i].line < hits[j].line
	})
	for _, h := range hits {
		fmt.Printf("%s:%s [%s] %s.%s %s\n", h.file, h.line, h.where, h.pkg, h.name, h.status)
	}
	fmt.Fprintf(os.Stderr, "%d stale mentions on added lines\n", len(hits))
}
