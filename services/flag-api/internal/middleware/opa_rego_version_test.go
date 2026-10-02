package middleware

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// Rule bodies without the `if` keyword are Rego v0 syntax. The deprecated
// opa/rego package parsed them by default; the Rego v1 default rejects them. An
// operator may have written their POLICY_DIR files this way, so the loader has
// to keep accepting them: a file that fails to compile silently swaps the live
// policy for the hardcoded permissionMatrix.
const (
	v0PolicyViewerReadOnly = `package tombstone.flags

default allow = false

allow {
	input.role == "viewer"
	input.action == "read"
}
`
	// Valid under both Rego v0 and v1, like the shipped flags.rego.
	bothVersionsPolicyNobodyAllowed = `package tombstone.flags

import future.keywords.if

default allow = false

allow if {
	input.role == "nobody"
}
`
	v0PolicyAdminEverything = `package tombstone.flags

default allow = false

allow {
	input.role == "admin"
}
`
	// Rego v1 syntax opted into per file. The policies README tells operators
	// to write v1 syntax this way, so the pinned v0 loader has to accept it.
	v1PolicyViaRegoV1Import = `package tombstone.flags

import rego.v1

default allow := false

allow if {
	input.role == "viewer"
	input.action == "read"
}
`
	// Rego v1 syntax with no import. The README says the loader rejects this
	// and the middleware decides from the hardcoded permissionMatrix instead.
	v1PolicyWithoutImport = `package tombstone.flags

default allow := false

allow if input.role == "admin"
`
)

func writePolicyFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertOPADecision(t *testing.T, eval *opaEvaluator, role, action string, want bool) {
	t.Helper()
	input := map[string]interface{}{"role": role, "resource": "flags", "action": action}
	got, ok := eval.evaluate(context.Background(), input)
	if !ok {
		t.Fatalf("evaluator unavailable deciding %s %s", role, action)
	}
	if got != want {
		t.Errorf("%s %s: allow = %v, want %v", role, action, got, want)
	}
}

// The two load paths are tested separately so a regression in either shows up
// on its own. The initial load is newOPAEvaluator; hot reload is watchPolicies
// calling the same evaluator's load on every .rego change, so calling load
// here is the exact call the fsnotify handler makes.
func TestOPAEvaluatorInitialLoadAcceptsV0SyntaxPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "flags.rego"), v0PolicyViewerReadOnly)

	eval := newOPAEvaluator(dir, "data.tombstone.flags.allow", zap.NewNop())
	if !eval.available {
		t.Fatalf("initial load rejected a v0-syntax policy: %v", eval.load(nil))
	}
	assertOPADecision(t, eval, "viewer", "read", true)
	assertOPADecision(t, eval, "admin", "write", false)
}

func TestOPAEvaluatorHotReloadAcceptsV0SyntaxPolicy(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "flags.rego")
	writePolicyFile(t, policyPath, bothVersionsPolicyNobodyAllowed)

	eval := newOPAEvaluator(dir, "data.tombstone.flags.allow", zap.NewNop())
	if !eval.available {
		t.Fatalf("setup: a policy valid in both Rego versions failed to load: %v", eval.load(nil))
	}
	assertOPADecision(t, eval, "admin", "write", false)

	writePolicyFile(t, policyPath, v0PolicyAdminEverything)
	if err := eval.load(nil); err != nil {
		t.Fatalf("hot reload rejected a v0-syntax policy: %v", err)
	}
	assertOPADecision(t, eval, "admin", "write", true)
	assertOPADecision(t, eval, "viewer", "read", false)
}

func TestOPAEvaluatorAcceptsV1SyntaxOptedInWithRegoV1Import(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "flags.rego"), v1PolicyViaRegoV1Import)

	eval := newOPAEvaluator(dir, "data.tombstone.flags.allow", zap.NewNop())
	if !eval.available {
		t.Fatalf("a v1-syntax policy with import rego.v1 was rejected: %v", eval.load(nil))
	}
	assertOPADecision(t, eval, "viewer", "read", true)
	assertOPADecision(t, eval, "viewer", "write", false)
}

func TestOPAEvaluatorRejectsV1SyntaxWithoutImport(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "flags.rego"), v1PolicyWithoutImport)

	eval := newOPAEvaluator(dir, "data.tombstone.flags.allow", zap.NewNop())
	if eval.available {
		t.Fatal("a v1-syntax policy with no import compiled; the README documents that it is rejected")
	}
	if err := eval.load(nil); err == nil {
		t.Error("load() returned nil for a v1-syntax policy with no import, want a compile error")
	}
	if _, ok := eval.evaluate(context.Background(), map[string]interface{}{"role": "admin"}); ok {
		t.Error("evaluate() reported OPA available after a failed compile, want the matrix fallback")
	}
}
