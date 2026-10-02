package v1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// lastArg marks a call whose IP is its final argument.
const lastArg = -1

// ipRecordingCalls maps each function that hands an IP to the audit log to the
// position of that argument.
var ipRecordingCalls = map[string]int{
	"writeAudit":                lastArg,
	"writeBreakGlassAuditEntry": 3,
	"consumeBreakGlassToken":    5,
}

// TestAuditIPComesFromIPFromRequest pins where the audit log gets its IP from.
// The audit tests feed ipFromRequest to the writer themselves, and the header
// scan in internal/clientip only forbids reading a client-address header, so
// neither notices a handler that passes "" or some other string instead.
// Every non-test call that records an IP must pass ipFromRequest(r), or the
// enclosing function's own ip parameter, and every audit.Entry must be given
// its IPAddress the same way.
func TestAuditIPComesFromIPFromRequest(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	fset := token.NewFileSet()
	seen := map[string]int{}

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CallExpr:
				name := calleeName(n.Fun)
				position, tracked := ipRecordingCalls[name]
				if !tracked {
					break
				}
				seen[name]++
				if position == lastArg {
					position = len(n.Args) - 1
				}
				if position < 0 || position >= len(n.Args) || !isRequestIP(n.Args[position]) {
					t.Errorf("%s: %s must be passed ipFromRequest(r) as its IP argument", fset.Position(n.Pos()), name)
				}
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && key.Name == "IPAddress" {
					seen["IPAddress"]++
					if !isRequestIP(n.Value) {
						t.Errorf("%s: IPAddress must be ipFromRequest(r) or the caller's ip parameter", fset.Position(n.Pos()))
					}
				}
			}
			return true
		})
	}

	for name := range ipRecordingCalls {
		if seen[name] == 0 {
			t.Errorf("found no call to %s; the scan would pass vacuously", name)
		}
	}
	if seen["IPAddress"] == 0 {
		t.Error("found no IPAddress field; the scan would pass vacuously")
	}
}

// calleeName returns the function or method name a call invokes.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// isRequestIP reports whether expr is ipFromRequest(r) or an identifier named
// ip, the parameter through which a helper receives it.
func isRequestIP(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "ip"
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || calleeName(call.Fun) != "ipFromRequest" || len(call.Args) != 1 {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	return ok && arg.Name == "r"
}
