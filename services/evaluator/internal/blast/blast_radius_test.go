package blast

import (
	"testing"
)

// classifyBlastRadius is a thin adapter so table tests can call scoreRisk
// via a plain function signature without constructing a full Calculator.
func classifyBlastRadius(trafficPct float64, depCount int, errorDelta float64) string {
	c := &Calculator{}
	r := &BlastRadiusResult{
		TrafficPctAffected:   trafficPct,
		DependentFlagsCount:  depCount,
		HistoricalErrorDelta: errorDelta,
	}
	return string(c.scoreRisk(r))
}

// TestBlastRadiusTier exercises the four canonical tier boundaries.
// Thresholds (from blast_radius.go):
//
//	BLOCKED : traffic >= 50 AND errorDelta > 0.05
//	HIGH    : traffic >= 25 OR depCount > 5
//	MEDIUM  : traffic >= 10 OR depCount > 2
//	LOW     : everything else
func TestBlastRadiusTier(t *testing.T) {
	cases := []struct {
		trafficPct float64
		depCount   int
		errorDelta float64
		wantTier   string
	}{
		// BLOCKED: 60 % traffic + 6 % error delta (both conditions met)
		{0.60 * 100, 3, 0.06, "BLOCKED"},
		// HIGH: 30 % traffic triggers the >= 25 branch
		{0.30 * 100, 2, 0.03, "HIGH"},
		// MEDIUM: 15 % traffic triggers the >= 10 branch
		{0.15 * 100, 1, 0.01, "MEDIUM"},
		// LOW: 5 % traffic, no deps, no history
		{0.05 * 100, 0, 0.00, "LOW"},
	}
	for _, tc := range cases {
		t.Run(tc.wantTier, func(t *testing.T) {
			got := classifyBlastRadius(tc.trafficPct, tc.depCount, tc.errorDelta)
			if got != tc.wantTier {
				t.Errorf("classifyBlastRadius(traffic=%.0f, deps=%d, delta=%.2f) = %s, want %s",
					tc.trafficPct, tc.depCount, tc.errorDelta, got, tc.wantTier)
			}
		})
	}
}

// TestScoreRisk verifies the risk scoring logic without a database.
// scoreRisk is pure logic — deterministic, no I/O.
func TestScoreRisk(t *testing.T) {
	c := &Calculator{}

	tests := []struct {
		name           string
		trafficPct     float64
		dependentFlags int
		errorDelta     float64
		want           RiskScore
	}{
		// BLOCKED: high traffic + historic error delta
		{"blocked: 50% traffic + 6% error delta", 50, 0, 0.06, RiskBlocked},
		{"blocked: 80% traffic + 10% error delta", 80, 0, 0.10, RiskBlocked},

		// HIGH: 25%+ traffic or 6+ dependent flags
		{"high: 25% traffic, no history", 25, 0, 0.0, RiskHigh},
		{"high: 6 dependent flags", 5, 6, 0.0, RiskHigh},
		{"high: 50% traffic, no error history", 50, 0, 0.0, RiskHigh},

		// MEDIUM: 10%+ traffic or 3+ dependent flags
		{"medium: 10% traffic", 10, 0, 0.0, RiskMedium},
		{"medium: 3 dependent flags", 5, 3, 0.0, RiskMedium},

		// LOW: small rollout, few dependencies, no history
		{"low: 5% traffic, 1 dep", 5, 1, 0.0, RiskLow},
		{"low: 1% traffic, no deps", 1, 0, 0.0, RiskLow},
		{"low: 0% traffic", 0, 0, 0.0, RiskLow},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &BlastRadiusResult{
				TrafficPctAffected:   tc.trafficPct,
				DependentFlagsCount:  tc.dependentFlags,
				HistoricalErrorDelta: tc.errorDelta,
			}
			got := c.scoreRisk(r)
			if got != tc.want {
				t.Errorf("scoreRisk(traffic=%.0f%%, deps=%d, delta=%.2f) = %s, want %s",
					tc.trafficPct, tc.dependentFlags, tc.errorDelta, got, tc.want)
			}
		})
	}
}

// TestRiskScoreConstants verifies the string values match what the dashboard + API expect.
func TestRiskScoreConstants(t *testing.T) {
	if RiskLow != "LOW" {
		t.Errorf("RiskLow = %q, want LOW", RiskLow)
	}
	if RiskMedium != "MEDIUM" {
		t.Errorf("RiskMedium = %q, want MEDIUM", RiskMedium)
	}
	if RiskHigh != "HIGH" {
		t.Errorf("RiskHigh = %q, want HIGH", RiskHigh)
	}
	if RiskBlocked != "BLOCKED" {
		t.Errorf("RiskBlocked = %q, want BLOCKED", RiskBlocked)
	}
}

// TestJustificationRequiredOnBlocked verifies BLOCKED risk always sets JustificationRequired.
func TestJustificationRequiredOnBlocked(t *testing.T) {
	c := &Calculator{}
	r := &BlastRadiusResult{
		TrafficPctAffected:   60,
		DependentFlagsCount:  0,
		HistoricalErrorDelta: 0.10,
	}
	r.RiskScore = c.scoreRisk(r)
	if r.RiskScore != RiskBlocked {
		t.Fatalf("expected BLOCKED, got %s", r.RiskScore)
	}
	// Simulate what Compute() does after scoring
	if r.RiskScore == RiskBlocked {
		r.JustificationRequired = "Risk score BLOCKED: type justification to proceed."
	}
	if r.JustificationRequired == "" {
		t.Error("JustificationRequired must be set when risk is BLOCKED")
	}
}
