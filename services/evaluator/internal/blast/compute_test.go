package blast

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCalculatorDeps(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *redis.Client) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return db, mock, rdb
}

// seedTelemetryBucket writes a real telemetry bucket at the current hour,
// matching the JSON shape telemetry.Aggregator.persistTelemetryBucket
// writes -- Compute's recentTelemetry reads this exact key shape.
func seedTelemetryBucket(t *testing.T, rdb *redis.Client, flagKey, env string, total, errors int64) {
	t.Helper()
	unixHour := time.Now().UTC().Truncate(time.Hour).Unix() / 3600
	key := "telemetry:" + flagKey + ":" + env + ":hour:" + strconv.FormatInt(unixHour, 10)
	payload, err := json.Marshal(struct {
		Total  int64 `json:"total"`
		Errors int64 `json:"errors"`
	}{Total: total, Errors: errors})
	if err != nil {
		t.Fatalf("marshal seed bucket: %v", err)
	}
	if err := rdb.Set(context.Background(), key, payload, time.Hour).Err(); err != nil {
		t.Fatalf("seed telemetry bucket: %v", err)
	}
}

// TestCompute_ScopesDependentFlagsQueryByProjectID proves the real
// flag_prerequisites query (which replaced the old audit_log co-change
// heuristic) binds flagKey and projectID correctly -- a placeholder-
// ordering mistake or typo'd column name would compile cleanly, pass go
// vet, and pass every test that only exercises the pure scoreRisk() method.
func TestCompute_ScopesDependentFlagsQueryByProjectID(t *testing.T) {
	db, mock, rdb := newTestCalculatorDeps(t)

	mock.ExpectQuery("SELECT f.key, f.owner_id FROM flag_prerequisites").
		WithArgs("checkout-v2", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"key", "owner_id"}).
			AddRow("fraud-check", "payments-team").
			AddRow("payments-v3", "payments-team").
			AddRow("receipts", "billing-team"))

	calc := NewCalculator(db, rdb, "http://unused")
	result, err := calc.Compute(context.Background(), "checkout-v2", "production", "project-a", 50)
	if err != nil {
		t.Fatalf("Compute() returned an error: %v", err)
	}

	if result.DependentFlagsCount != 3 {
		t.Errorf("DependentFlagsCount = %d, want 3", result.DependentFlagsCount)
	}
	if len(result.AffectedServices) != 2 {
		t.Errorf("AffectedServices = %v, want 2 distinct owners", result.AffectedServices)
	}
	if result.Confidence != "LOW" {
		t.Errorf("Confidence = %q, want LOW (no telemetry seeded)", result.Confidence)
	}
	if result.HistoricalErrorRate != 0 {
		t.Errorf("HistoricalErrorRate = %v, want 0 when cold-start", result.HistoricalErrorRate)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all SQL expectations were met (wrong query text, arg order, or arg values): %v", err)
	}
}

// TestCompute_ReadsRealTelemetryForErrorRateAndConfidence proves Compute
// reads the SAME Redis telemetry buckets telemetry.Aggregator writes
// (EVAL-3), rather than the old fake constant capped at 0.02.
func TestCompute_ReadsRealTelemetryForErrorRateAndConfidence(t *testing.T) {
	db, mock, rdb := newTestCalculatorDeps(t)

	mock.ExpectQuery("SELECT f.key, f.owner_id FROM flag_prerequisites").
		WithArgs("checkout-v2", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"key", "owner_id"}))

	seedTelemetryBucket(t, rdb, "checkout-v2", "production", 1000, 80)

	calc := NewCalculator(db, rdb, "http://unused")
	result, err := calc.Compute(context.Background(), "checkout-v2", "production", "project-a", 60)
	if err != nil {
		t.Fatalf("Compute() returned an error: %v", err)
	}

	if result.RecentEvaluationCount != 1000 {
		t.Errorf("RecentEvaluationCount = %d, want 1000", result.RecentEvaluationCount)
	}
	if result.Confidence != "HIGH" {
		t.Errorf("Confidence = %q, want HIGH (1000 >= coldStartMinEvaluations)", result.Confidence)
	}
	if result.HistoricalErrorRate != 0.08 {
		t.Errorf("HistoricalErrorRate = %v, want 0.08", result.HistoricalErrorRate)
	}
	// 60% traffic + 8% real error rate crosses the BLOCKED gate that a fake
	// constant capped at 0.02 could never reach.
	if result.RiskScore != RiskBlocked {
		t.Errorf("RiskScore = %s, want BLOCKED", result.RiskScore)
	}
	if result.JustificationRequired == "" {
		t.Error("JustificationRequired must be set when risk is BLOCKED")
	}
}

// TestCompute_CachesRepeatedIdenticalCalls proves the second Compute() call
// for the identical input reuses the cached result instead of re-querying
// Postgres -- expecting the DB query exactly once, then asserting a second
// call still succeeds, is the regression test for the cache actually being
// consulted rather than merely present and unused.
func TestCompute_CachesRepeatedIdenticalCalls(t *testing.T) {
	db, mock, rdb := newTestCalculatorDeps(t)

	mock.ExpectQuery("SELECT f.key, f.owner_id FROM flag_prerequisites").
		WithArgs("checkout-v2", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"key", "owner_id"}))

	calc := NewCalculator(db, rdb, "http://unused")
	ctx := context.Background()

	first, err := calc.Compute(ctx, "checkout-v2", "production", "project-a", 10)
	if err != nil {
		t.Fatalf("first Compute() returned an error: %v", err)
	}
	second, err := calc.Compute(ctx, "checkout-v2", "production", "project-a", 10)
	if err != nil {
		t.Fatalf("second Compute() returned an error: %v", err)
	}
	if first != second {
		t.Error("second Compute() call did not return the cached *BlastRadiusResult")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("dependentFlags query ran more than once -- cache was not consulted: %v", err)
	}
}

// TestCompute_DifferentRolloutPctBypassesCache proves the cache key is
// scoped by newRolloutPct, not just flag+env+project -- otherwise a second
// call with a DIFFERENT candidate percentage would incorrectly reuse a
// result computed for a different TrafficPctAffected.
func TestCompute_DifferentRolloutPctBypassesCache(t *testing.T) {
	db, mock, rdb := newTestCalculatorDeps(t)

	mock.ExpectQuery("SELECT f.key, f.owner_id FROM flag_prerequisites").
		WithArgs("checkout-v2", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"key", "owner_id"}))
	mock.ExpectQuery("SELECT f.key, f.owner_id FROM flag_prerequisites").
		WithArgs("checkout-v2", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"key", "owner_id"}))

	calc := NewCalculator(db, rdb, "http://unused")
	ctx := context.Background()

	if _, err := calc.Compute(ctx, "checkout-v2", "production", "project-a", 10); err != nil {
		t.Fatalf("Compute(10) returned an error: %v", err)
	}
	if _, err := calc.Compute(ctx, "checkout-v2", "production", "project-a", 90); err != nil {
		t.Fatalf("Compute(90) returned an error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected the dependentFlags query to run twice, once per distinct rollout pct: %v", err)
	}
}
