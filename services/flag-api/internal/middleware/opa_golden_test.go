package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

// opaGoldenPath holds the decisions the REAL policies/flags.rego, loaded by the
// REAL opaEvaluator, produced for the tables in opa_golden_cases_test.go. It
// was first recorded on the deprecated github.com/open-policy-agent/opa/rego
// package, before the move to opa/v1/rego, so this test is the proof that the
// move changed no decision. Re-record only for a deliberate policy change:
//
//	go test ./internal/middleware -run TestOPADecisionsMatchGolden -update-opa-golden
//
// and review the resulting diff of the JSON like any other policy change.
const opaGoldenPath = "testdata/opa_decisions_golden.json"

const maxReportedMismatches = 10

var updateOPAGolden = flag.Bool("update-opa-golden", false,
	"rewrite "+opaGoldenPath+" from the current implementation instead of comparing against it")

type opaGolden struct {
	Shapes []opaShape       `json:"shapes"`
	Checks []opaCheckRecord `json:"permission_checks"`
	Raw    []opaRawRecord   `json:"raw_inputs"`
}

func goldenShapesByName() map[string]opaShape {
	byName := make(map[string]opaShape, len(opaGoldenShapes))
	for _, s := range opaGoldenShapes {
		byName[s.Name] = s
	}
	return byName
}

// evaluateOPAChecks runs each record through checkPermissionWithOPA, the exact
// call RequirePermission makes, and returns new records carrying the decision.
func evaluateOPAChecks(rbac *RBACMiddleware, inputs []opaCheckRecord) []opaCheckRecord {
	shapes := goldenShapesByName()
	out := make([]opaCheckRecord, len(inputs))
	for i, rec := range inputs {
		shape := shapes[rec.Shape]
		req := (&http.Request{Method: shape.Method, URL: &url.URL{Path: shape.Path}}).
			WithContext(context.WithValue(context.Background(), ContextKeyActor, shape.Actor))
		rec.Allow, rec.Source = rbac.checkPermissionWithOPA(req, Role(rec.Role), rec.Resource, rec.Action)
		out[i] = rec
	}
	return out
}

// evaluateOPARaw hands each input document straight to opaEvaluator.evaluate.
func evaluateOPARaw(rbac *RBACMiddleware, inputs []opaRawRecord) []opaRawRecord {
	out := make([]opaRawRecord, len(inputs))
	for i, rec := range inputs {
		rec.Allow, rec.OK = rbac.flagsEval.evaluate(context.Background(), rec.Input)
		out[i] = rec
	}
	return out
}

func encodeGoldenLine(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func renderGoldenSection[T any](key string, records []T) (string, error) {
	lines := make([]string, len(records))
	for i, rec := range records {
		line, err := encodeGoldenLine(rec)
		if err != nil {
			return "", err
		}
		lines[i] = "    " + line
	}
	return "  " + strconv.Quote(key) + ": [\n" + strings.Join(lines, ",\n") + "\n  ]", nil
}

// renderOPAGolden writes one record per line so a policy change shows up as a
// small, reviewable line diff.
func renderOPAGolden(g opaGolden) (string, error) {
	shapes, err := renderGoldenSection("shapes", g.Shapes)
	if err != nil {
		return "", err
	}
	checks, err := renderGoldenSection("permission_checks", g.Checks)
	if err != nil {
		return "", err
	}
	raw, err := renderGoldenSection("raw_inputs", g.Raw)
	if err != nil {
		return "", err
	}
	return "{\n" + strings.Join([]string{shapes, checks, raw}, ",\n") + "\n}\n", nil
}

func mustEncodeGoldenLine(t *testing.T, v any) string {
	t.Helper()
	line, err := encodeGoldenLine(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return line
}

func assertSameRecords[T any](t *testing.T, kind string, want, got []T) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: golden has %d records, the current table has %d — if the table changed on purpose, re-record with -update-opa-golden",
			kind, len(want), len(got))
	}
	mismatches := 0
	for i := range want {
		wantLine, gotLine := mustEncodeGoldenLine(t, want[i]), mustEncodeGoldenLine(t, got[i])
		if wantLine == gotLine {
			continue
		}
		mismatches++
		if mismatches <= maxReportedMismatches {
			t.Errorf("%s[%d] decision changed\n  golden: %s\n  now:    %s", kind, i, wantLine, gotLine)
		}
	}
	if mismatches > maxReportedMismatches {
		t.Errorf("%s: %d more mismatches not shown (%d of %d records differ)",
			kind, mismatches-maxReportedMismatches, mismatches, len(want))
	}
}

// assertGoldenIsNotVacuous guards against a golden that would pass for the
// wrong reason: every request-shaped decision must have come from OPA (not the
// fallback matrix), and both allow and deny must be represented.
func assertGoldenIsNotVacuous(t *testing.T, g opaGolden) {
	t.Helper()
	allowed, denied := 0, 0
	for _, rec := range g.Checks {
		if rec.Source != "opa" {
			t.Fatalf("decision for %+v came from %q, not OPA — the golden would not exercise the policy", rec, rec.Source)
		}
		if rec.Allow {
			allowed++
		} else {
			denied++
		}
	}
	if allowed == 0 || denied == 0 {
		t.Fatalf("golden checks are one-sided: %d allowed, %d denied", allowed, denied)
	}
	rawAllowed := 0
	for _, rec := range g.Raw {
		if rec.OK && rec.Allow {
			rawAllowed++
		}
	}
	if rawAllowed == 0 {
		t.Fatal("no raw input is allowed — the raw table cannot detect a policy that denies everything")
	}
}

func TestOPADecisionsMatchGolden(t *testing.T) {
	rbac := realOPARBAC(t)
	got := opaGolden{
		Shapes: opaGoldenShapes,
		Checks: evaluateOPAChecks(rbac, goldenCheckInputs()),
		Raw:    evaluateOPARaw(rbac, goldenRawInputs()),
	}
	assertGoldenIsNotVacuous(t, got)

	if *updateOPAGolden {
		rendered, err := renderOPAGolden(got)
		if err != nil {
			t.Fatalf("render golden: %v", err)
		}
		if err := os.WriteFile(opaGoldenPath, []byte(rendered), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("recorded %d permission checks and %d raw inputs to %s", len(got.Checks), len(got.Raw), opaGoldenPath)
		return
	}

	data, err := os.ReadFile(opaGoldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var want opaGolden
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	assertSameRecords(t, "shapes", want.Shapes, got.Shapes)
	assertSameRecords(t, "permission_checks", want.Checks, got.Checks)
	assertSameRecords(t, "raw_inputs", want.Raw, got.Raw)
}
