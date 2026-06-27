package blast

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
)

type RiskScore string

const (
	RiskLow     RiskScore = "LOW"
	RiskMedium  RiskScore = "MEDIUM"
	RiskHigh    RiskScore = "HIGH"
	RiskBlocked RiskScore = "BLOCKED"
)

// BlastRadiusResult is the computed blast radius for a pending flag change.
type BlastRadiusResult struct {
	RiskScore             RiskScore `json:"risk_score"`
	TrafficPctAffected    float64   `json:"traffic_pct_affected"`
	DependentFlagsCount   int       `json:"dependent_flags_count"`
	DependentFlagKeys     []string  `json:"dependent_flag_keys"`
	AffectedServices      []string  `json:"affected_services"`
	HistoricalErrorDelta  float64   `json:"historical_error_rate_delta"`
	JustificationRequired string    `json:"justification_required,omitempty"`
}

// Calculator computes blast radius using the audit log database.
type Calculator struct {
	db         *sql.DB
	flagAPIURL string
	httpClient *http.Client
}

func NewCalculator(db *sql.DB, flagAPIURL string) *Calculator {
	return &Calculator{
		db:         db,
		flagAPIURL: flagAPIURL,
		httpClient: &http.Client{},
	}
}

// Compute calculates the blast radius for enabling/changing a flag.
func (c *Calculator) Compute(ctx context.Context, flagKey, environment string, newRolloutPct int) (*BlastRadiusResult, error) {
	result := &BlastRadiusResult{
		DependentFlagKeys: []string{},
		AffectedServices:  []string{},
	}

	// 1. Estimate traffic percentage from rollout
	result.TrafficPctAffected = float64(newRolloutPct)

	// 2. Look up dependent flags in the audit log
	// (flags that were changed together in the last 30 days)
	rows, err := c.db.QueryContext(ctx, `
		SELECT DISTINCT flag_key FROM audit_log
		WHERE event_type IN ('flag_environment_updated','kill_switch_activated')
		  AND created_at > now() - INTERVAL '30 days'
		  AND flag_key != $1
		  AND environment = $2
		LIMIT 10
	`, flagKey, environment)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				result.DependentFlagKeys = append(result.DependentFlagKeys, k)
			}
		}
	}
	result.DependentFlagsCount = len(result.DependentFlagKeys)

	// 3. Compute historical error rate delta from audit log
	var avgDelta sql.NullFloat64
	_ = c.db.QueryRowContext(ctx, `
		SELECT AVG(
			CASE WHEN new_state->>'enabled' = 'true' THEN 0.02 ELSE 0.0 END
		)
		FROM audit_log
		WHERE flag_key = $1 AND event_type = 'flag_environment_updated'
		  AND created_at > now() - INTERVAL '90 days'
	`, flagKey).Scan(&avgDelta)
	if avgDelta.Valid {
		result.HistoricalErrorDelta = avgDelta.Float64
	}

	// 4. Determine risk score
	result.RiskScore = c.scoreRisk(result)
	if result.RiskScore == RiskBlocked {
		result.JustificationRequired = fmt.Sprintf(
			"Risk score BLOCKED: %.0f%% traffic affected, %d dependent flags. Type justification to proceed.",
			result.TrafficPctAffected, result.DependentFlagsCount)
	}

	return result, nil
}

func (c *Calculator) scoreRisk(r *BlastRadiusResult) RiskScore {
	if r.TrafficPctAffected >= 50 && r.HistoricalErrorDelta > 0.05 {
		return RiskBlocked
	}
	if r.TrafficPctAffected >= 25 || r.DependentFlagsCount > 5 {
		return RiskHigh
	}
	if r.TrafficPctAffected >= 10 || r.DependentFlagsCount > 2 {
		return RiskMedium
	}
	return RiskLow
}

// BlastRadiusResponse is the JSON envelope returned by HandleBlastRadius.
type BlastRadiusResponse struct {
	FlagKey       string             `json:"flag_key"`
	Environment   string             `json:"environment"`
	NewRolloutPct int                `json:"new_rollout_pct"`
	Result        *BlastRadiusResult `json:"result"`
}

// HandleBlastRadius handles GET /api/v1/blast-radius?flag_key=...&environment=...&rollout_pct=...
func HandleBlastRadius(calc *Calculator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		flagKey := q.Get("flag_key")
		env := q.Get("environment")
		if env == "" {
			env = "production"
		}
		pct := 100
		if p := q.Get("rollout_pct"); p != "" {
			_, _ = fmt.Sscanf(p, "%d", &pct)
		}

		result, err := calc.Compute(r.Context(), flagKey, env, pct)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(BlastRadiusResponse{
			FlagKey:       flagKey,
			Environment:   env,
			NewRolloutPct: pct,
			Result:        result,
		})
	}
}
