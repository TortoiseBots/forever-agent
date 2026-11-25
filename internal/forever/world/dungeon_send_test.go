package world

// The one-way-out test for the dungeon adapters: the command channel is reached from exactly one function, and no
// adapter method accepts a command line. Both are read from the package's own source, so a new escape hatch is a
// test failure and not only a review finding.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// adapterMethods is the driver adapter's whole method set: the five port methods, the send the module's typed client
// runs over, the builders of the core's tokenless lines and the combat-log watch's unit resolution. A method outside
// it (a raw setup line, a new escape hatch) fails the test.
var adapterMethods = map[string]bool{
	"State": true, "Threat": true, "Order": true, "Party": true, "Act": true,
	"RunStatus": true, "RunEvents": true, "RunDecisions": true, "CommanderStatus": true,
	"Run": true, "gmOn": true, "teleOutside": true, "appear": true, "deathRecap": true,
	"combatLog": true, "combatLogUnit": true,
	// The group layer's raid calls (#108): typed client packets sent by the session, never a `.tb` line.
	"ConvertRaid": true, "RaidMove": true,
}

// TestOnlySendReachesTheCommandChannel: the driver adapter's only call of the command session's Run is the send the
// typed client is built over, no method hands a caller's string to it as a command line, and the package builds no
// `.tb` line of its own. (forever cmd-check's own line helper is the human-facing cmd-check feature, not one of the
// driver's ports, and is not part of this adapter.)
func TestOnlySendReachesTheCommandChannel(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil && strings.Contains(v, ".tb") {
				t.Errorf("%s: a `.tb` command line is built in this package: %q", name, v)
			}
			return true
		})
		for _, decl := range af.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || receiverName(fd) != "dungeonCommander" {
				continue
			}
			seen[fd.Name.Name] = true
			if !adapterMethods[fd.Name.Name] {
				t.Errorf("%s:%s is not in the adapter's method set; a setup line must be a typed action", name, fd.Name.Name)
			}
			params := paramNames(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "Run" {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "q" {
					sends++
					if fd.Name.Name != "Run" {
						t.Errorf("%s:%s reaches the command session outside the send", name, fd.Name.Name)
					}
					return true
				}
				// The adapter's own send: its second argument must not be a caller's string, which would make the
				// method a way to pass a command line in. Run is the transport itself and takes the line.
				if fd.Name.Name != "Run" && len(call.Args) >= 2 {
					if id, ok := call.Args[1].(*ast.Ident); ok && params[id.Name] {
						t.Errorf("%s:%s hands its parameter %q to the command channel", name, fd.Name.Name, id.Name)
					}
				}
				return true
			})
		}
	}
	if sends != 1 {
		t.Fatalf("%d command-session Run calls, want the one in the adapter's send", sends)
	}
	for m := range adapterMethods {
		if !seen[m] {
			t.Errorf("the adapter has no %s method", m)
		}
	}
	if seen["Setup"] || seen["Core"] {
		t.Fatal("the adapter still has a raw line method")
	}
}

// paramNames is a function's parameter identifiers.
func paramNames(fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	for _, f := range fd.Type.Params.List {
		for _, n := range f.Names {
			out[n.Name] = true
		}
	}
	return out
}

// receiverName is a method's receiver type name, without the pointer star.
func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	switch t := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}
