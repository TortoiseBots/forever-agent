package driver

// The watch's one-way-out proof: the driver package reaches the command channel's sending methods (Act, Order) from
// exactly one place each, both inside sendLine's callers, and builds no `.tb` command line of its own. Reads
// (State, Threat, Party, RunStatus, RunEvents) are not sends and may sit wherever they are needed.

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

func TestOnlySendFunctionsReachTheChannel(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sends := map[string][]string{}
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
			if ok && lit.Kind == token.STRING {
				v, err := strconv.Unquote(lit.Value)
				// A line that starts with the command prefix is a command; the module's own reason text ("stopped by
				// .tb run stop") is data the scripted module reports back.
				if err == nil && strings.HasPrefix(strings.TrimSpace(v), ".tb") {
					t.Errorf("%s: a `.tb` command line is built in the driver: %q", name, v)
				}
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "cmd" {
				return true
			}
			switch sel.Sel.Name {
			case "Act", "Order":
				sends[sel.Sel.Name] = append(sends[sel.Sel.Name], name)
			}
			return true
		})
	}
	if len(sends["Act"]) != 1 || sends["Act"][0] != "actions.go" {
		t.Fatalf("Act calls: %v, want one in actions.go (sendLine's caller)", sends["Act"])
	}
	if len(sends["Order"]) != 1 || sends["Order"][0] != "orders.go" {
		t.Fatalf("Order calls: %v, want one in orders.go (sendLine's caller)", sends["Order"])
	}
}

func TestRunActionsAreClassified(t *testing.T) {
	for _, kind := range []ActionKind{ActionRunStart, ActionRunStop, ActionRunPause, ActionRunResume} {
		class, ok := classifyAction(kind)
		if !ok {
			t.Fatalf("action %d is not classified", kind)
		}
		want := actionFight
		if kind == ActionRunStop || kind == ActionRunPause {
			want = actionPassive
		}
		if class != want {
			t.Fatalf("action %d class %d, want %d", kind, class, want)
		}
	}
}
