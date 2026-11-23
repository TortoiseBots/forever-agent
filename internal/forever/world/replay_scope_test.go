package world

// The skip list's guard: replaySkip may only name opcodes handle's own switch knows, read from the source like the
// scope tests do, so a handler that moves out of the switch breaks the test instead of leaving a stale skip entry.

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

// handleSwitchCases reads session.go and answers every opcode constant named by a case of handle's dispatch switch,
// by its constant name.
func handleSwitchCases(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatalf("session.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "session.go", src, 0)
	if err != nil {
		t.Fatalf("session.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "handle" && f.Recv != nil {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatal("session.go holds no handle method")
	}
	cases := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if !isCoreSwitch(sw) {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range clause.List {
				if sel, ok := e.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "wire" {
						cases[sel.Sel.Name] = true
					}
				}
			}
		}
		return false
	})
	if len(cases) == 0 {
		t.Fatal("handle's switch names no opcodes")
	}
	return cases
}

// isCoreSwitch reports whether a switch's tag is p.Core, the dispatch switch.
func isCoreSwitch(sw *ast.SwitchStmt) bool {
	sel, ok := sw.Tag.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Core" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "p"
}

// wireConstantNames reads internal/forever/wire/opcodes.go and maps each opcode's value to its Go constant name, so a
// case of handle (a selector name) can be compared with the value a record carries.
func wireConstantNames(t *testing.T) map[uint32]string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "wire", "opcodes.go"))
	if err != nil {
		t.Fatalf("opcodes.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "opcodes.go", src, 0)
	if err != nil {
		t.Fatalf("opcodes.go: %v", err)
	}
	names := map[uint32]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				continue
			}
			v, err := strconv.ParseUint(lit.Value, 0, 32)
			if err != nil {
				continue
			}
			if prev, seen := names[uint32(v)]; !seen || (!strings.HasPrefix(prev, "SMSG") && strings.HasPrefix(name.Name, "SMSG")) {
				names[uint32(v)] = name.Name
			}
		}
		return true
	})
	if len(names) == 0 {
		t.Fatal("opcodes.go names no opcode values")
	}
	return names
}
