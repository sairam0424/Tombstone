package blast

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestCompute_ScopesBothQueriesByProjectID closes a test-coverage gap
// flagged by adversarial review of PR #206 (INT-2's tenancy fix):
// blast_radius_test.go's existing tests only ever call the pure scoreRisk()
// method on a zero-value Calculator with a nil db -- Compute() itself, and
// the project_id-scoped SQL this PR added to it, had never been exercised
// by any test. A placeholder-ordering mistake or a typo'd column name would
// have compiled cleanly, passed go vet, and passed every existing test.
func TestCompute_ScopesBothQueriesByProjectID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() failed: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT DISTINCT flag_key FROM audit_log").
		WithArgs("checkout-v2", "production", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"flag_key"}).
			AddRow("fraud-check").
			AddRow("payments-v3"))

	mock.ExpectQuery("SELECT AVG").
		WithArgs("checkout-v2", "project-a").
		WillReturnRows(sqlmock.NewRows([]string{"avg"}).AddRow(0.01))

	calc := NewCalculator(db, "http://unused")
	result, err := calc.Compute(context.Background(), "checkout-v2", "production", "project-a", 50)
	if err != nil {
		t.Fatalf("Compute() returned an error: %v", err)
	}

	if result.DependentFlagsCount != 2 {
		t.Errorf("DependentFlagsCount = %d, want 2", result.DependentFlagsCount)
	}
	if result.HistoricalErrorDelta != 0.01 {
		t.Errorf("HistoricalErrorDelta = %v, want 0.01", result.HistoricalErrorDelta)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all SQL expectations were met (wrong query text, arg order, or arg values): %v", err)
	}
}

// TestCompute_DifferentProjectIDsProduceDifferentBoundArgs proves the
// project_id value passed to Compute() actually reaches BOTH queries as a
// real bound parameter, not just a comment/unused variable -- a stray
// "-- project_id filter removed" edit with an unused trailing arg would
// still compile and still pass TestCompute_ScopesBothQueriesByProjectID
// above if that test only checked its own hardcoded project_id; asserting
// on ExpectationsWereMet with a DIFFERENT project_id here, on a fresh
// mock, provides an independent check that the argument travels through.
func TestCompute_DifferentProjectIDsProduceDifferentBoundArgs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() failed: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT DISTINCT flag_key FROM audit_log").
		WithArgs("checkout-v2", "production", "project-b").
		WillReturnRows(sqlmock.NewRows([]string{"flag_key"}))

	mock.ExpectQuery("SELECT AVG").
		WithArgs("checkout-v2", "project-b").
		WillReturnRows(sqlmock.NewRows([]string{"avg"}))

	calc := NewCalculator(db, "http://unused")
	if _, err := calc.Compute(context.Background(), "checkout-v2", "production", "project-b", 10); err != nil {
		t.Fatalf("Compute() returned an error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("project-b was not bound to both queries as expected: %v", err)
	}
}
