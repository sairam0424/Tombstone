package blast

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/tombstone/evaluator/internal/circuit"
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
	RiskScore RiskScore `json:"risk_score"`
	// TrafficPctAffected is the requested rollout percentage itself -- under
	// Tombstone's uniform-random bucketing, that IS the fraction of this
	// flag's evaluations that will see the new configuration. It says
	// nothing about SCALE, which is why RecentEvaluationCount exists
	// alongside it: 10% of a flag evaluated once a minute and 10% of one
	// evaluated a million times a day carry the same percentage but very
	// different real exposure.
	TrafficPctAffected float64 `json:"traffic_pct_affected"`
	// RecentEvaluationCount is the real evaluation volume telemetry.
	// Aggregator.persistTelemetryBucket (EVAL-3) recorded for this flag+env
	// over the last recentTelemetryLookbackHours. Also drives Confidence.
	RecentEvaluationCount int64    `json:"recent_evaluation_count"`
	DependentFlagsCount   int      `json:"dependent_flags_count"`
	DependentFlagKeys     []string `json:"dependent_flag_keys"`
	// AffectedServices is owner_id-based (the distinct flags.owner_id values
	// among this flag's real dependents, per flag_prerequisites) -- the
	// closest real concept to "affected service" this schema has. There is
	// no service-registry table, so this is NOT a true service dependency
	// graph; it's disclosed as such rather than invented.
	AffectedServices []string `json:"affected_services"`
	// HistoricalErrorRate is this flag+env's REAL error rate over the last
	// recentTelemetryLookbackHours, read from the same telemetry buckets as
	// RecentEvaluationCount -- not a "delta" between two periods (this
	// schema has no before/after snapshot to diff), and previously a fake
	// constant capped at 0.02, structurally below the BLOCKED gate's > 0.05
	// threshold below and thus never reachable. Only trustworthy when
	// Confidence is HIGH; see Confidence's own doc comment.
	HistoricalErrorRate float64 `json:"historical_error_rate"`
	// Confidence is "LOW" when RecentEvaluationCount is below
	// coldStartMinEvaluations -- too little real traffic to trust a computed
	// HistoricalErrorRate either way, so it's left at 0 (unmeasured) rather
	// than a fabricated non-zero value. "LOW" means "we don't know", not
	// "verified safe" -- callers gating on risk tier should treat a LOW-
	// confidence result with its own caution rather than reading a LOW/
	// MEDIUM risk score as a clean bill of health.
	Confidence            string `json:"confidence"`
	JustificationRequired string `json:"justification_required,omitempty"`
}

// recentTelemetryLookbackHours bounds how far back Compute reads hourly
// telemetry buckets for RecentEvaluationCount/HistoricalErrorRate -- long
// enough to smooth over a quiet overnight window, short enough that a
// stale, long-resolved incident from days ago doesn't still depress a
// fresh rollout's risk score today.
const recentTelemetryLookbackHours = 24

// coldStartMinEvaluations mirrors circuit.Breaker's own default MinRequests
// -- below this many real evaluations in the lookback window, there isn't
// enough traffic to trust a computed error rate in either direction.
const coldStartMinEvaluations = 100

// dependentFlagsLimit bounds how many real prerequisite-graph dependents
// Compute reports -- a defensive cap against a pathological fan-out, not an
// expected real-world limit.
const dependentFlagsLimit = 20

// cacheTTL bounds how long Compute reuses a previously computed result for
// the identical (flagKey, environment, projectID, newRolloutPct) input.
// Blast radius is checked right before a risky change is applied, not on a
// hot path -- a short cache absorbs repeated pre-check calls (e.g. a
// dashboard re-checking while a user edits a rollout slider) without
// re-querying Postgres/Redis on every keystroke, while staying short enough
// that a real, fast-moving incident is never masked by a stale result.
const cacheTTL = 30 * time.Second

// Calculator computes blast radius using the audit log database, the
// flag_prerequisites dependency graph, and telemetry.Aggregator's
// real per-flag+env telemetry in Redis.
type Calculator struct {
	db         *sql.DB
	rdb        *redis.Client
	flagAPIURL string
	httpClient *http.Client

	cacheMu sync.Mutex
	cache   map[string]cachedResult
}

type cachedResult struct {
	result    *BlastRadiusResult
	expiresAt time.Time
}

func NewCalculator(db *sql.DB, rdb *redis.Client, flagAPIURL string) *Calculator {
	return &Calculator{
		db:         db,
		rdb:        rdb,
		flagAPIURL: flagAPIURL,
		httpClient: &http.Client{},
		cache:      make(map[string]cachedResult),
	}
}

// Compute calculates the blast radius for enabling/changing a flag.
func (c *Calculator) Compute(ctx context.Context, flagKey, environment, projectID string, newRolloutPct int) (*BlastRadiusResult, error) {
	cacheKey := flagKey + "\x00" + environment + "\x00" + projectID + "\x00" + fmt.Sprint(newRolloutPct)
	if cached := c.cacheGet(cacheKey); cached != nil {
		return cached, nil
	}

	result := &BlastRadiusResult{
		DependentFlagKeys: []string{},
		AffectedServices:  []string{},
	}

	// 1. Requested traffic percentage (see TrafficPctAffected's doc comment).
	result.TrafficPctAffected = float64(newRolloutPct)

	// 2. Real evaluation volume + error rate from telemetry buckets.
	total, errs := c.recentTelemetry(ctx, flagKey, environment)
	result.RecentEvaluationCount = total
	if total >= coldStartMinEvaluations {
		result.Confidence = "HIGH"
		result.HistoricalErrorRate = float64(errs) / float64(total)
	} else {
		result.Confidence = "LOW"
	}

	// 3. Real dependents from the flag_prerequisites graph, and their
	// owners as a proxy for affected services.
	if deps, owners, err := c.dependentFlags(ctx, flagKey, projectID); err == nil {
		result.DependentFlagKeys = deps
		result.AffectedServices = owners
	}
	result.DependentFlagsCount = len(result.DependentFlagKeys)

	// 4. Determine risk score
	result.RiskScore = c.scoreRisk(result)
	if result.RiskScore == RiskBlocked {
		result.JustificationRequired = fmt.Sprintf(
			"Risk score BLOCKED: %.0f%% traffic affected, %.1f%% historical error rate (%s confidence, %d evaluations/%dh), %d dependent flags. Type justification to proceed.",
			result.TrafficPctAffected, result.HistoricalErrorRate*100, result.Confidence, result.RecentEvaluationCount, recentTelemetryLookbackHours, result.DependentFlagsCount)
	}

	c.cacheSet(cacheKey, result)
	return result, nil
}

// recentTelemetry sums the last recentTelemetryLookbackHours hourly
// telemetry buckets telemetry.Aggregator.persistTelemetryBucket (EVAL-3)
// writes for flagKey/env -- the same real per-flag+env evaluation volume
// and error counts services/evaluator/internal/api/v1/slo.go's SLO endpoint
// reads. Returns (0, 0) on any Redis error or if rdb is nil (e.g. a
// Calculator constructed directly in a DB-only test) -- callers already
// treat total==0 as cold-start via Confidence, so a transient Redis outage
// degrades to "unknown", not a crash.
func (c *Calculator) recentTelemetry(ctx context.Context, flagKey, env string) (total, errs int64) {
	if c.rdb == nil {
		return 0, 0
	}
	nowHour := time.Now().UTC().Truncate(time.Hour).Unix() / 3600
	keys := make([]string, recentTelemetryLookbackHours)
	for i := 0; i < recentTelemetryLookbackHours; i++ {
		keys[i] = fmt.Sprintf("telemetry:%s:%s:hour:%d",
			circuit.EscapeKeyComponent(flagKey), circuit.EscapeKeyComponent(env), nowHour-int64(i))
	}
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, 0
	}
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		var bucket struct {
			Total  int64 `json:"total"`
			Errors int64 `json:"errors"`
		}
		if json.Unmarshal([]byte(s), &bucket) == nil {
			total += bucket.Total
			errs += bucket.Errors
		}
	}
	return total, errs
}

// dependentFlags queries the real flag_prerequisites graph for flags that
// declare flagKey as a prerequisite -- replacing the old audit_log
// co-change heuristic (flags merely edited within the same 30-day window),
// which conflated "changed around the same time" with "actually depends on
// this flag." owners is the distinct, order-preserving set of those
// dependents' owner_id values (see AffectedServices's doc comment).
func (c *Calculator) dependentFlags(ctx context.Context, flagKey, projectID string) (keys, owners []string, err error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT f.key, f.owner_id
		FROM flag_prerequisites fp
		JOIN flags f ON f.id = fp.flag_id
		WHERE fp.prereq_flag_key = $1 AND f.project_id = $2
		ORDER BY f.key
		LIMIT `+fmt.Sprint(dependentFlagsLimit), flagKey, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	seenOwner := make(map[string]bool)
	for rows.Next() {
		var key, owner string
		if rows.Scan(&key, &owner) != nil {
			continue
		}
		keys = append(keys, key)
		if owner != "" && !seenOwner[owner] {
			seenOwner[owner] = true
			owners = append(owners, owner)
		}
	}
	return keys, owners, rows.Err()
}

// cacheGet/cacheSet implement Compute's short-lived result cache (see
// cacheTTL's doc comment). cacheSet also opportunistically sweeps expired
// entries once the map grows past cacheSweepThreshold, bounding long-term
// memory growth without a background goroutine -- this endpoint's own
// input space (a handful of flag+env+project combinations, checked
// occasionally before a risky change) never approaches that threshold in
// any real deployment, so the sweep is a defensive backstop, not a
// load-bearing eviction policy.
const cacheSweepThreshold = 1000

func (c *Calculator) cacheGet(key string) *BlastRadiusResult {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := c.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry.result
}

func (c *Calculator) cacheSet(key string, result *BlastRadiusResult) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if len(c.cache) >= cacheSweepThreshold {
		now := time.Now()
		for k, v := range c.cache {
			if now.After(v.expiresAt) {
				delete(c.cache, k)
			}
		}
	}
	c.cache[key] = cachedResult{result: result, expiresAt: time.Now().Add(cacheTTL)}
}

func (c *Calculator) scoreRisk(r *BlastRadiusResult) RiskScore {
	if r.TrafficPctAffected >= 50 && r.HistoricalErrorRate > 0.05 {
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

// defaultProjectID matches services/intelligence's own DEFAULT_PROJECT_ID
// (app/graph/builder.py) — the seed "Default" project's real UUID. Exists
// only so a single-project deployment (the only kind that exists today)
// keeps working with no caller changes; real multi-project deployments
// must pass an explicit project_id.
const defaultProjectID = "00000000-0000-0000-0000-000000000001"

// HandleBlastRadius handles GET /api/v1/blast-radius?flag_key=...&environment=...&rollout_pct=...&project_id=...
func HandleBlastRadius(calc *Calculator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		flagKey := q.Get("flag_key")
		env := q.Get("environment")
		if env == "" {
			env = "production"
		}
		projectID := q.Get("project_id")
		if projectID == "" {
			projectID = defaultProjectID
		}
		pct := 100
		if p := q.Get("rollout_pct"); p != "" {
			_, _ = fmt.Sscanf(p, "%d", &pct)
		}

		result, err := calc.Compute(r.Context(), flagKey, env, projectID, pct)
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
