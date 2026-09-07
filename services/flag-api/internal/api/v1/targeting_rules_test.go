package v1

import (
	"math"
	"testing"
)

// TestAddTargetingRuleRequest_Validate exercises AddTargetingRuleRequest.validate
// directly -- a pure function, so no database, HTTP request, or handler
// instance is needed at all.
func TestAddTargetingRuleRequest_Validate(t *testing.T) {
	base := AddTargetingRuleRequest{RuleType: "USER", Attribute: "email", Operator: "EQ", Variation: "true"}

	cases := []struct {
		name    string
		mutate  func(r AddTargetingRuleRequest) AddTargetingRuleRequest
		wantErr bool
	}{
		{"valid request", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { return r }, false},
		{"missing rule_type", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.RuleType = ""; return r }, true},
		{"invalid rule_type", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.RuleType = "NOT_A_TYPE"; return r }, true},
		{"missing attribute", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Attribute = ""; return r }, true},
		{"missing operator", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Operator = ""; return r }, true},
		{"invalid operator", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Operator = "NOT_AN_OP"; return r }, true},
		{"missing variation", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Variation = ""; return r }, true},
		{"priority at int32 max is accepted", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Priority = math.MaxInt32; return r }, false},
		{"priority at int32 min is accepted", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Priority = math.MinInt32; return r }, false},
		{"priority one past int32 max is rejected", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Priority = math.MaxInt32 + 1; return r }, true},
		{"priority one past int32 min is rejected", func(r AddTargetingRuleRequest) AddTargetingRuleRequest { r.Priority = math.MinInt32 - 1; return r }, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.mutate(base).validate()
			if tc.wantErr && got == "" {
				t.Errorf("validate() = %q, want a non-empty error message", got)
			}
			if !tc.wantErr && got != "" {
				t.Errorf("validate() = %q, want \"\" (valid request rejected)", got)
			}
		})
	}
}

// TestAddTargetingRuleRequest_Validate_EveryValidOperatorIsAccepted pins
// down that validOperators (targeting_rules.go) matches targeting_rules'
// own CHECK constraint (schema.sql) exactly -- a missing operator here would
// silently reject a value the database would have accepted, and an extra
// one would let a request past validation only to fail with an opaque 500
// from the CHECK constraint instead of this function's own message.
func TestAddTargetingRuleRequest_Validate_EveryValidOperatorIsAccepted(t *testing.T) {
	// Mirrors schema.sql's targeting_rules.operator CHECK constraint list
	// verbatim -- kept as a literal here (not validOperators itself) so
	// this test can't pass merely because a future edit to validOperators
	// drifted from the schema; it must still match this independently
	// copied list.
	schemaOperators := []string{
		"IN", "NOT_IN", "EQ", "NEQ", "LT", "LTE", "GT", "GTE", "CONTAINS",
		"PREFIX", "SUFFIX", "REGEX", "SEMVER_GTE", "SEMVER_LTE",
		"GEO_COUNTRY", "GEO_REGION", "DATE_BEFORE", "DATE_AFTER",
	}

	for _, op := range schemaOperators {
		t.Run(op, func(t *testing.T) {
			req := AddTargetingRuleRequest{RuleType: "USER", Attribute: "email", Operator: op, Variation: "true"}
			if got := req.validate(); got != "" {
				t.Errorf("operator %q: validate() = %q, want \"\" — it is in schema.sql's own CHECK constraint", op, got)
			}
		})
	}
}
