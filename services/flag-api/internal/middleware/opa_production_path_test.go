package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// The tests in opa_rego_version_test.go build the evaluator directly. These go
// through NewRBACMiddleware, the constructor main wires in, so the POLICY_DIR
// lookup, the query string, and the fsnotify watcher that triggers hot reload
// are all exercised with the pinned Rego version.

const (
	// Rego v0 syntax, and it reads every field the middleware puts in the
	// input document, so dropping or blanking one on the Go side flips a
	// decision instead of passing unnoticed.
	v0PolicyUsesEveryInputField = `package tombstone.flags

default allow = false

allow {
	input.method == "PATCH"
	input.actor == "user-42"
	input.path == ["api", "v1", "flags", "checkout"]
	input.role == "operator"
	input.resource == "flags"
	input.action == "write"
}
`

	hotReloadDeadline = 10 * time.Second
	hotReloadPoll     = 25 * time.Millisecond
)

func decideViaMiddleware(mw *RBACMiddleware, role Role, resource, action string) (bool, string) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/flags", nil)
	return mw.checkPermissionWithOPA(req, role, resource, action)
}

func TestNewRBACMiddlewareLoadsV0PolicyFromPolicyDir(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "flags.rego"), v0PolicyViewerReadOnly)
	t.Setenv("POLICY_DIR", dir)

	mw := NewRBACMiddleware(nil, zap.NewNop())

	if got := mw.PolicySource(); got != "opa" {
		t.Fatalf("PolicySource() = %q, want %q: a v0-syntax policy in POLICY_DIR did not compile", got, "opa")
	}
	// The hardcoded matrix allows ADMIN flags:write and VIEWER flags:read, so
	// these two decisions can only come from the policy file.
	if allow, source := decideViaMiddleware(mw, RoleAdmin, "flags", "write"); allow || source != "opa" {
		t.Errorf("ADMIN flags:write = (%v, %q), want (false, \"opa\")", allow, source)
	}
	if allow, source := decideViaMiddleware(mw, RoleViewer, "flags", "read"); !allow || source != "opa" {
		t.Errorf("VIEWER flags:read = (%v, %q), want (true, \"opa\")", allow, source)
	}
}

// The fsnotify watcher only reacts to events that arrive after it has added the
// directory, and NewRBACMiddleware starts it on a goroutine with no readiness
// signal. So the test rewrites the file on every poll until the new policy is
// live, which makes it independent of when the watcher registers.
func TestNewRBACMiddlewareHotReloadsV0PolicyThroughWatcher(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "flags.rego")
	writePolicyFile(t, policyPath, v0PolicyViewerReadOnly)
	t.Setenv("POLICY_DIR", dir)

	mw := NewRBACMiddleware(nil, zap.NewNop())
	if allow, source := decideViaMiddleware(mw, RoleAdmin, "flags", "write"); allow || source != "opa" {
		t.Fatalf("setup: ADMIN flags:write = (%v, %q), want (false, \"opa\")", allow, source)
	}

	deadline := time.Now().Add(hotReloadDeadline)
	for {
		writePolicyFile(t, policyPath, v0PolicyAdminEverything)
		if allow, source := decideViaMiddleware(mw, RoleAdmin, "flags", "write"); allow && source == "opa" {
			return
		}
		if time.Now().After(deadline) {
			allow, source := decideViaMiddleware(mw, RoleAdmin, "flags", "write")
			t.Fatalf("watcher did not reload the rewritten policy within %s: ADMIN flags:write = (%v, %q), want (true, \"opa\")",
				hotReloadDeadline, allow, source)
		}
		time.Sleep(hotReloadPoll)
	}
}

func TestOPAInputDocumentCarriesEveryRequestField(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "flags.rego"), v0PolicyUsesEveryInputField)
	eval := newOPAEvaluator(dir, "data.tombstone.flags.allow", zap.NewNop())
	mw := &RBACMiddleware{logger: zap.NewNop(), flagsEval: eval}

	matching := opaInputCase{
		method: http.MethodPatch, path: "/api/v1/flags/checkout", actor: "user-42",
		role: RoleOperator, resource: "flags", action: "write",
	}
	cases := []struct {
		name  string
		input opaInputCase
		want  bool
	}{
		{"every field matches", matching, true},
		{"method differs", matching.with(func(c *opaInputCase) { c.method = http.MethodPost }), false},
		{"actor differs", matching.with(func(c *opaInputCase) { c.actor = "user-43" }), false},
		{"path differs", matching.with(func(c *opaInputCase) { c.path = "/api/v1/flags/other" }), false},
		{"role differs", matching.with(func(c *opaInputCase) { c.role = RoleViewer }), false},
		{"resource differs", matching.with(func(c *opaInputCase) { c.resource = "audit" }), false},
		{"action differs", matching.with(func(c *opaInputCase) { c.action = "read" }), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.input
			allow, source := mw.checkPermissionWithOPA(in.request(), in.role, in.resource, in.action)
			if allow != tc.want || source != "opa" {
				t.Errorf("decision = (%v, %q), want (%v, \"opa\")", allow, source, tc.want)
			}
		})
	}
}

type opaInputCase struct {
	method, path, actor string
	role                Role
	resource, action    string
}

// with returns a copy of c changed by edit, leaving c itself untouched.
func (c opaInputCase) with(edit func(*opaInputCase)) opaInputCase {
	edit(&c)
	return c
}

func (c opaInputCase) request() *http.Request {
	req := httptest.NewRequest(c.method, c.path, nil)
	return req.WithContext(context.WithValue(req.Context(), ContextKeyActor, c.actor))
}
