package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEveryAPIRouteIsPermissionGated is a structural regression guard for SEC-1.
//
// The original vulnerability was invisible in code review: the /api/v1 group
// applies Authenticate + LoadRole, so every route *looked* protected, but
// LoadRole only resolves a role — it never denies. Four+ routes had no
// RequirePermission at all, letting any authenticated token flip production
// flags and read the audit log.
//
// Rather than trusting review to catch that again, this test parses the route
// table in main.go and fails if a route inside /api/v1 is registered without a
// RequirePermission gate. Intentional exceptions must be listed in
// ungatedByDesign with a reason, so an exception is a visible decision instead
// of an accident.
//
// It is a source-level check because the router is built inline in main() and
// cannot be constructed in a test without a live DB, Redis, and OPA policy dir.
func TestEveryAPIRouteIsPermissionGated(t *testing.T) {
	// Routes deliberately reachable by any authenticated caller.
	ungatedByDesign := map[string]string{
		`Get("/change-requests"`: "TODO(SEC-3): four-eyes rework decides the tier; " +
			"listing is documented as open to any authenticated user and " +
			"approvals:read is currently OWNER/ADMIN-only, which would break OPERATOR",
	}

	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	block, err := apiV1Block(string(src))
	if err != nil {
		t.Fatalf("locate /api/v1 route block: %v", err)
	}

	routeVerb := regexp.MustCompile(`\.(Get|Post|Put|Patch|Delete)\("`)

	var ungated []string
	for _, stmt := range splitStatements(block) {
		if !routeVerb.MatchString(stmt) {
			continue // r.Use(...), r.Route(...) wrappers, comments
		}
		if strings.Contains(stmt, "RequirePermission") {
			continue
		}
		if reason, ok := matchExemption(stmt, ungatedByDesign); ok {
			t.Logf("exempt (%s): %s", reason, firstLine(stmt))
			continue
		}
		ungated = append(ungated, firstLine(stmt))
	}

	if len(ungated) > 0 {
		t.Errorf("these /api/v1 routes have NO RequirePermission gate — any authenticated token can reach them:\n  %s",
			strings.Join(ungated, "\n  "))
	}
}

// apiV1Block returns the body of the r.Route("/api/v1", ...) call by brace
// matching from the opening func literal to its matching close brace.
func apiV1Block(src string) (string, error) {
	const marker = `r.Route("/api/v1"`
	start := strings.Index(src, marker)
	if start < 0 {
		return "", errNotFound
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		return "", errNotFound
	}
	depth := 0
	for i := start + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start+open+1 : i], nil
			}
		}
	}
	return "", errNotFound
}

// splitStatements breaks a route block into logical statements. A statement
// starts at a line beginning with "r." (after trimming) and continues through
// any chained continuation lines (".With(...)", ".Get(...)").
func splitStatements(block string) []string {
	var stmts []string
	var cur strings.Builder
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "r.") {
			if cur.Len() > 0 {
				stmts = append(stmts, cur.String())
				cur.Reset()
			}
		}
		cur.WriteString(trimmed)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		stmts = append(stmts, cur.String())
	}
	return stmts
}

func matchExemption(stmt string, exemptions map[string]string) (string, bool) {
	for pattern, reason := range exemptions {
		if strings.Contains(stmt, pattern) {
			return reason, true
		}
	}
	return "", false
}

func firstLine(stmt string) string {
	for _, l := range strings.Split(stmt, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "//") {
			return l
		}
	}
	return strings.TrimSpace(stmt)
}

type constErr string

func (e constErr) Error() string { return string(e) }

const errNotFound constErr = "could not locate the /api/v1 route block in main.go"
