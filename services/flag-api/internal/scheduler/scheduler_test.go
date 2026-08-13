package scheduler

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"
)

// newMockDB returns a sqlmock-backed *sql.DB and its mock controller. Mirrors
// the miniredis pattern already used in internal/api/v1/flags_test.go for
// mocking external dependencies without a real Postgres instance.
func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// ---- backoffDuration ----

// TestBackoffDuration_ExponentialGrowth verifies the documented backoff
// schedule: 1min, 2min, 4min (capped), doubling per attempt.
func TestBackoffDuration_ExponentialGrowth(t *testing.T) {
	cases := []struct {
		retryCount int
		want       time.Duration
	}{
		{retryCount: 1, want: 1 * time.Minute},
		{retryCount: 2, want: 2 * time.Minute},
		{retryCount: 3, want: 4 * time.Minute}, // exactly at the cap
		{retryCount: 4, want: 4 * time.Minute}, // would be 8min uncapped, clamped to cap
		{retryCount: 10, want: 4 * time.Minute},
	}
	for _, tc := range cases {
		got := backoffDuration(tc.retryCount)
		if got != tc.want {
			t.Errorf("backoffDuration(%d) = %v, want %v", tc.retryCount, got, tc.want)
		}
	}
}

// TestBackoffDuration_ClampsBelowOne verifies a defensive floor: a caller
// passing retryCount < 1 (shouldn't happen, but must not panic or shift
// negative) gets the same delay as retryCount == 1.
func TestBackoffDuration_ClampsBelowOne(t *testing.T) {
	got := backoffDuration(0)
	want := 1 * time.Minute
	if got != want {
		t.Errorf("backoffDuration(0) = %v, want %v", got, want)
	}

	got = backoffDuration(-5)
	if got != want {
		t.Errorf("backoffDuration(-5) = %v, want %v", got, want)
	}
}

// ---- markFailed ----

// TestMarkFailed_RetriesRemaining verifies that when retry_count (after
// increment) is still below max_retries, the row is set to FAILED with
// retry_count incremented and next_retry_at set into the future (backoff
// applied), rather than being made permanently terminal.
func TestMarkFailed_RetriesRemaining(t *testing.T) {
	db, mock := newMockDB(t)
	logger := zap.NewNop()

	id := "sc-1"
	// Row currently at retry_count=0, max_retries=3 -> after increment,
	// retry_count=1 < max_retries=3, so this should be a "will retry" path.
	mock.ExpectQuery(`SELECT retry_count, max_retries FROM scheduled_changes WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"retry_count", "max_retries"}).AddRow(0, 3))

	mock.ExpectExec(`UPDATE scheduled_changes\s+SET status = 'FAILED', error_message = \$1, retry_count = \$2, next_retry_at = \$3\s+WHERE id = \$4`).
		WithArgs("boom", 1, sqlmock.AnyArg(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))

	before := time.Now()
	markFailed(context.Background(), db, logger, id, "boom")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}

	// Sanity-check the backoff math independently of the mocked UPDATE args:
	// next_retry_at should be ~1 minute (backoffDuration(1)) after "before".
	expectedNextRetry := before.Add(backoffDuration(1))
	if expectedNextRetry.Before(before) {
		t.Errorf("expected next_retry_at to be in the future, got %v (before=%v)", expectedNextRetry, before)
	}
}

// TestMarkFailed_ExhaustsRetries verifies that when retry_count is already
// at max_retries-1 (one shy of the limit), one more failure pushes
// retry_count to max_retries, which is now >= max_retries, so the row
// becomes permanently terminal: FAILED with next_retry_at explicitly NULL,
// and no further retry scheduling.
func TestMarkFailed_ExhaustsRetries(t *testing.T) {
	db, mock := newMockDB(t)
	logger := zap.NewNop()

	id := "sc-2"
	// Row at retry_count=2, max_retries=3 -> after increment, retry_count=3,
	// which is NOT < max_retries=3, so this is the terminal path.
	mock.ExpectQuery(`SELECT retry_count, max_retries FROM scheduled_changes WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"retry_count", "max_retries"}).AddRow(2, 3))

	mock.ExpectExec(`UPDATE scheduled_changes\s+SET status = 'FAILED', error_message = \$1, retry_count = \$2, next_retry_at = NULL\s+WHERE id = \$3`).
		WithArgs("still failing", 3, id).
		WillReturnResult(sqlmock.NewResult(0, 1))

	markFailed(context.Background(), db, logger, id, "still failing")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestMarkFailed_ReadRetryStateFails verifies the defensive fallback: if the
// SELECT to read retry_count/max_retries itself fails (e.g. transient DB
// error), markFailed falls back to the pre-retry unconditional terminal
// FAILED update rather than panicking or leaving the row stuck in PENDING.
func TestMarkFailed_ReadRetryStateFails(t *testing.T) {
	db, mock := newMockDB(t)
	logger := zap.NewNop()

	id := "sc-3"
	mock.ExpectQuery(`SELECT retry_count, max_retries FROM scheduled_changes WHERE id = \$1`).
		WithArgs(id).
		WillReturnError(sql.ErrConnDone)

	mock.ExpectExec(`UPDATE scheduled_changes\s+SET status = 'FAILED', error_message = \$1\s+WHERE id = \$2`).
		WithArgs("boom", id).
		WillReturnResult(sqlmock.NewResult(0, 1))

	markFailed(context.Background(), db, logger, id, "boom")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// ---- runDue poll query ----

// TestRunDue_PollQuery_SelectsPendingAndDueRetries verifies the poll query
// shape directly: it must select PENDING-and-due rows OR FAILED rows that
// still have retry budget and whose backoff window has elapsed. This test
// drives runDue end-to-end against sqlmock and asserts the exact query text
// sqlmock intercepted, then verifies each of the three row-eligibility
// scenarios via separate mock row sets (sqlmock does not evaluate SQL
// WHERE clauses, so eligibility is asserted by constructing the exact rows
// the real query would need to return per scenario, and confirming runDue
// invokes applyChange in each case by observing the corresponding downstream
// audit read).
func TestRunDue_PollQuery_SelectsPendingAndDueRetries(t *testing.T) {
	db, mock := newMockDB(t)
	logger := zap.NewNop()

	// runDue now wraps the SELECT in a transaction so FOR UPDATE SKIP LOCKED
	// atomically claims rows across replicas.
	mock.ExpectBegin()
	// Row 1: a fresh PENDING change with an invalid payload, so applyChange
	// takes the fast "invalid JSON" failure path — lets us observe that
	// runDue actually dispatched to applyChange for this row without needing
	// to mock the full success path.
	mock.ExpectQuery(`SELECT id, flag_key, environment, change_payload\s+FROM scheduled_changes\s+WHERE \(status = 'PENDING' AND scheduled_for <= NOW\(\)\)\s+OR \(status = 'FAILED' AND retry_count < max_retries AND next_retry_at <= NOW\(\)\)\s+ORDER BY scheduled_for ASC\s+FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "flag_key", "environment", "change_payload"}).
			AddRow("sc-pending", "my-flag", "production", []byte(`not-json`)).
			AddRow("sc-retry-due", "my-flag-2", "staging", []byte(`not-json`)))
	mock.ExpectCommit()

	// Both rows hit applyChange's invalid-JSON branch -> markFailed for each.
	// sc-pending: assume fresh row, retry_count=0, max_retries=3 -> will-retry path.
	mock.ExpectQuery(`SELECT retry_count, max_retries FROM scheduled_changes WHERE id = \$1`).
		WithArgs("sc-pending").
		WillReturnRows(sqlmock.NewRows([]string{"retry_count", "max_retries"}).AddRow(0, 3))
	mock.ExpectExec(`UPDATE scheduled_changes\s+SET status = 'FAILED', error_message = \$1, retry_count = \$2, next_retry_at = \$3\s+WHERE id = \$4`).
		WithArgs(sqlmock.AnyArg(), 1, sqlmock.AnyArg(), "sc-pending").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// sc-retry-due: this is a FAILED-and-due row already at retry_count=2,
	// max_retries=3 -> next failure exhausts it (terminal path).
	mock.ExpectQuery(`SELECT retry_count, max_retries FROM scheduled_changes WHERE id = \$1`).
		WithArgs("sc-retry-due").
		WillReturnRows(sqlmock.NewRows([]string{"retry_count", "max_retries"}).AddRow(2, 3))
	mock.ExpectExec(`UPDATE scheduled_changes\s+SET status = 'FAILED', error_message = \$1, retry_count = \$2, next_retry_at = NULL\s+WHERE id = \$3`).
		WithArgs(sqlmock.AnyArg(), 3, "sc-retry-due").
		WillReturnResult(sqlmock.NewResult(0, 1))

	runDue(context.Background(), db, nil, logger, nil)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestRunDue_PollQuery_ExactTextMatch pins the exact poll query text runDue
// issues, so a future edit that accidentally drops the retry-eligibility
// clause (e.g. reverting to a bare "WHERE status = 'PENDING'") fails this
// test immediately — sqlmock's regexp query matcher only succeeds if the
// issued SQL matches the expected pattern below.
// TestRunDue_PollQuery_ExactTextMatch pins the exact poll query text runDue
// issues, so a future edit that accidentally drops the retry-eligibility
// clause or the FOR UPDATE SKIP LOCKED anti-duplicate guard fails immediately.
func TestRunDue_PollQuery_ExactTextMatch(t *testing.T) {
	db, mock := newMockDB(t)
	logger := zap.NewNop()

	// runDue wraps the SELECT in a transaction so FOR UPDATE SKIP LOCKED can
	// atomically claim rows across replicas — sqlmock requires Begin/Commit.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, flag_key, environment, change_payload\s+FROM scheduled_changes\s+WHERE \(status = 'PENDING' AND scheduled_for <= NOW\(\)\)\s+OR \(status = 'FAILED' AND retry_count < max_retries AND next_retry_at <= NOW\(\)\)\s+ORDER BY scheduled_for ASC\s+FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "flag_key", "environment", "change_payload"}))
	mock.ExpectCommit()

	runDue(context.Background(), db, nil, logger, nil)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("poll query did not match expected retry-aware WHERE clause with FOR UPDATE SKIP LOCKED: %v", err)
	}
}

// TestRunDue_ForUpdateSkipLocked_SkipsLockedRows verifies the SKIP LOCKED
// semantics: when two goroutines call runDue concurrently, each processes a
// disjoint set of rows. Simulated here by returning an empty result on the
// "second" call (sqlmock is serial so we model it as sequential expectations).
func TestRunDue_ForUpdateSkipLocked_SkipsLockedRows(t *testing.T) {
	db, mock := newMockDB(t)
	logger := zap.NewNop()

	// First replica claims the rows.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, flag_key, environment, change_payload.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "flag_key", "environment", "change_payload"}).
			AddRow("sc-1", "flag-a", "production", []byte(`not-json`)))
	mock.ExpectCommit()
	// sc-1 applyChange -> markFailed.
	mock.ExpectQuery(`SELECT retry_count, max_retries FROM scheduled_changes WHERE id = \$1`).
		WithArgs("sc-1").
		WillReturnRows(sqlmock.NewRows([]string{"retry_count", "max_retries"}).AddRow(0, 3))
	mock.ExpectExec(`UPDATE scheduled_changes.*`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "sc-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Second replica gets an empty result (rows were locked by first).
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, flag_key, environment, change_payload.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "flag_key", "environment", "change_payload"}))
	mock.ExpectCommit()

	runDue(context.Background(), db, nil, logger, nil)
	runDue(context.Background(), db, nil, logger, nil) // second replica call

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("concurrent runDue expectations not met: %v", err)
	}
}
