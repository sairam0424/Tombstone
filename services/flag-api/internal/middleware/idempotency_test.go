package middleware

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"
)

const testEndpoint = "POST /flags"

func newIdempotencyTestMiddleware(t *testing.T) (*IdempotencyMiddleware, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	return NewIdempotencyMiddleware(db, zap.NewNop()), mock, func() { _ = db.Close() }
}

func countingHandler(callCount *int, status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// TestIdempotency_NoHeader_PassesThroughUnchanged verifies that when the
// Idempotency-Key header is absent, the middleware performs no DB access at
// all and simply invokes the next handler.
func TestIdempotency_NoHeader_PassesThroughUnchanged(t *testing.T) {
	mw, mock, cleanup := newIdempotencyTestMiddleware(t)
	defer cleanup()

	var calls int
	handler := mw.Handle(testEndpoint)(countingHandler(&calls, http.StatusCreated, `{"ok":true}`))

	req := httptest.NewRequest(http.MethodPost, "/flags", bytes.NewBufferString(`{"key":"f1"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("expected handler to be called once, got %d", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected DB interaction with no idempotency header: %v", err)
	}
}

// TestIdempotency_FirstCall_RunsHandlerOnceAndPersists verifies the new-key
// path: INSERT ... RETURNING id succeeds, the real handler runs exactly
// once, and the response is persisted via UPDATE.
func TestIdempotency_FirstCall_RunsHandlerOnceAndPersists(t *testing.T) {
	mw, mock, cleanup := newIdempotencyTestMiddleware(t)
	defer cleanup()

	body := `{"key":"f1","name":"Flag One","flag_type":"BOOLEAN"}`
	hash := requestHashForTest(body)

	// actor is "" in tests because no auth context is injected.
	mock.ExpectQuery(`INSERT INTO idempotency_keys`).
		WithArgs("", "key-abc", testEndpoint, hash).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("row-1"))

	mock.ExpectExec(`UPDATE idempotency_keys`).
		WithArgs(http.StatusCreated, []byte(`{"created":true}`), "row-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	var calls int
	handler := mw.Handle(testEndpoint)(countingHandler(&calls, http.StatusCreated, `{"created":true}`))

	req := httptest.NewRequest(http.MethodPost, "/flags", bytes.NewBufferString(body))
	req.Header.Set("Idempotency-Key", "key-abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("expected real handler to run exactly once, got %d", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec.Code)
	}
	if rec.Body.String() != `{"created":true}` {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}

// TestIdempotency_Replay_DoesNotInvokeHandlerAgain verifies the conflict +
// completed path: the real handler must NOT be called a second time, and
// the byte-for-byte stored response is written back to the client.
func TestIdempotency_Replay_DoesNotInvokeHandlerAgain(t *testing.T) {
	mw, mock, cleanup := newIdempotencyTestMiddleware(t)
	defer cleanup()

	body := `{"key":"f1","name":"Flag One","flag_type":"BOOLEAN"}`
	hash := requestHashForTest(body)

	// Conflict: INSERT returns no rows.
	mock.ExpectQuery(`INSERT INTO idempotency_keys`).
		WithArgs("", "key-abc", testEndpoint, hash).
		WillReturnError(nowRowsErr())

	mock.ExpectQuery(`SELECT request_hash, completed_at, response_status, response_body`).
		WithArgs("", "key-abc", testEndpoint).
		WillReturnRows(sqlmock.NewRows([]string{"request_hash", "completed_at", "response_status", "response_body"}).
			AddRow(hash, time.Now(), int64(http.StatusCreated), []byte(`{"created":true}`)))

	var calls int
	handler := mw.Handle(testEndpoint)(countingHandler(&calls, http.StatusCreated, `{"created":true}`))

	req := httptest.NewRequest(http.MethodPost, "/flags", bytes.NewBufferString(body))
	req.Header.Set("Idempotency-Key", "key-abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if calls != 0 {
		t.Fatalf("expected real handler NOT to be called on replay, got %d calls", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected replayed status 201, got %d", rec.Code)
	}
	if rec.Body.String() != `{"created":true}` {
		t.Fatalf("replayed body mismatch: got %q, want %q", rec.Body.String(), `{"created":true}`)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}

// TestIdempotency_SecondCallSameKeyDifferentBody_Returns409 verifies that
// reusing an idempotency key with a different request body is rejected.
func TestIdempotency_SecondCallSameKeyDifferentBody_Returns409(t *testing.T) {
	mw, mock, cleanup := newIdempotencyTestMiddleware(t)
	defer cleanup()

	firstBody := `{"key":"f1"}`
	secondBody := `{"key":"f2"}`
	firstHash := requestHashForTest(firstBody)
	secondHash := requestHashForTest(secondBody)

	mock.ExpectQuery(`INSERT INTO idempotency_keys`).
		WithArgs("", "key-abc", testEndpoint, secondHash).
		WillReturnError(nowRowsErr())

	mock.ExpectQuery(`SELECT request_hash, completed_at, response_status, response_body`).
		WithArgs("", "key-abc", testEndpoint).
		WillReturnRows(sqlmock.NewRows([]string{"request_hash", "completed_at", "response_status", "response_body"}).
			AddRow(firstHash, time.Now(), int64(http.StatusCreated), []byte(`{"created":true}`)))

	var calls int
	handler := mw.Handle(testEndpoint)(countingHandler(&calls, http.StatusCreated, `{"created":true}`))

	req := httptest.NewRequest(http.MethodPost, "/flags", bytes.NewBufferString(secondBody))
	req.Header.Set("Idempotency-Key", "key-abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if calls != 0 {
		t.Fatalf("expected handler NOT to run when body hash mismatches, got %d calls", calls)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}

// TestIdempotency_StillProcessing_Returns409WithoutBlocking verifies that a
// row with completed_at IS NULL fails fast with 409 rather than polling.
func TestIdempotency_StillProcessing_Returns409WithoutBlocking(t *testing.T) {
	mw, mock, cleanup := newIdempotencyTestMiddleware(t)
	defer cleanup()

	body := `{"key":"f1"}`
	hash := requestHashForTest(body)

	mock.ExpectQuery(`INSERT INTO idempotency_keys`).
		WithArgs("", "key-abc", testEndpoint, hash).
		WillReturnError(nowRowsErr())

	mock.ExpectQuery(`SELECT request_hash, completed_at, response_status, response_body`).
		WithArgs("", "key-abc", testEndpoint).
		WillReturnRows(sqlmock.NewRows([]string{"request_hash", "completed_at", "response_status", "response_body"}).
			AddRow(hash, nil, nil, nil))

	var calls int
	handler := mw.Handle(testEndpoint)(countingHandler(&calls, http.StatusCreated, `{"created":true}`))

	req := httptest.NewRequest(http.MethodPost, "/flags", bytes.NewBufferString(body))
	req.Header.Set("Idempotency-Key", "key-abc")
	rec := httptest.NewRecorder()

	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if calls != 0 {
		t.Fatalf("expected handler NOT to run while still processing, got %d calls", calls)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for still-processing request, got %d", rec.Code)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected fail-fast behavior (no polling/blocking), took %v", elapsed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}

// TestIdempotency_ExpiredKey_ExecutesFreshAfterCleanup verifies that once an
// expired row has been purged (simulating the hourly cleanup ticker), a
// resubmission with the same key is treated as brand new and the handler
// runs again.
func TestIdempotency_ExpiredKey_ExecutesFreshAfterCleanup(t *testing.T) {
	mw, mock, cleanup := newIdempotencyTestMiddleware(t)
	defer cleanup()

	body := `{"key":"f1"}`
	hash := requestHashForTest(body)

	// After the cleanup ticker has deleted the expired row, the unique index
	// no longer has a conflicting entry, so INSERT succeeds again.
	mock.ExpectQuery(`INSERT INTO idempotency_keys`).
		WithArgs("", "key-abc", testEndpoint, hash).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("row-2"))

	mock.ExpectExec(`UPDATE idempotency_keys`).
		WithArgs(http.StatusCreated, []byte(`{"created":true}`), "row-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	var calls int
	handler := mw.Handle(testEndpoint)(countingHandler(&calls, http.StatusCreated, `{"created":true}`))

	req := httptest.NewRequest(http.MethodPost, "/flags", bytes.NewBufferString(body))
	req.Header.Set("Idempotency-Key", "key-abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("expected handler to run fresh after expired key was purged, got %d calls", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet DB expectations: %v", err)
	}
}

// nowRowsErr simulates the "no row returned" outcome from
// "... ON CONFLICT DO NOTHING RETURNING id" when a conflicting row already
// exists — sqlmock surfaces this the same way database/sql does: sql.ErrNoRows.
func nowRowsErr() error {
	return sql.ErrNoRows
}

// requestHashForTest mirrors the sha256 hex-encoding the middleware itself
// uses, so tests can assert against the exact same hash the middleware
// would compute for a given body.
func requestHashForTest(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
}
