package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// cleanupInterval is how often expired idempotency_keys rows are purged.
const idempotencyCleanupInterval = 1 * time.Hour

// idempotencyHeader is the client-supplied opt-in header. Its absence means
// fully unchanged behavior — existing callers are unaffected.
const idempotencyHeader = "Idempotency-Key"

// IdempotencyMiddleware collapses network-retry-induced duplicate calls to
// flag-mutation endpoints (CreateFlag, UpdateEnvironment, KillSwitch) into a
// single execution of the real handler. Phase 2's resilient HTTP client
// means a retried inter-service call could otherwise execute the same
// mutation twice, producing a duplicate audit_log row and a duplicate
// downstream Redis broadcast.
//
// Behavior is opt-in via the "Idempotency-Key" request header:
//   - Header absent -> pass straight through to next.ServeHTTP unchanged.
//   - Header present -> hash the request body and either run the handler
//     exactly once (recording its response), or replay the previously
//     recorded response without invoking the handler again.
type IdempotencyMiddleware struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewIdempotencyMiddleware(db *sql.DB, logger *zap.Logger) *IdempotencyMiddleware {
	return &IdempotencyMiddleware{db: db, logger: logger}
}

// idempotencyResponseRecorder wraps http.ResponseWriter to capture the status
// code and body written by the real handler so they can be persisted for replay.
type idempotencyResponseRecorder struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (rec *idempotencyResponseRecorder) WriteHeader(status int) {
	if !rec.wroteHeader {
		rec.status = status
		rec.wroteHeader = true
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *idempotencyResponseRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	rec.body.Write(b)
	return rec.ResponseWriter.Write(b)
}

// Handle returns the middleware. resource identifies the endpoint for the
// (actor, idempotency_key, endpoint) uniqueness constraint — callers pass a stable
// string per route (e.g. "POST /flags", "PATCH /flags/{key}/environments/{env}").
// The actor is extracted from the request context (set by AuthMiddleware) so that
// two different callers sharing the same Idempotency-Key string never share a cached
// response — preventing SEC-001 (cross-caller cache poisoning).
func (m *IdempotencyMiddleware) Handle(endpoint string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(idempotencyHeader)
			if key == "" {
				// No opt-in header — fully unchanged behavior.
				next.ServeHTTP(w, r)
				return
			}

			// Actor is injected by AuthMiddleware before this middleware runs.
			// Default to empty string so existing tests without auth context still work,
			// but in production the auth middleware always runs first.
			actor := ""
			if v, ok := r.Context().Value(ContextKeyActor).(string); ok {
				actor = v
			}

			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				writeIdempotencyError(w, http.StatusBadRequest, "failed to read request body")
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			requestHash := fmt.Sprintf("%x", sha256.Sum256(bodyBytes))

			var id string
			insertErr := m.db.QueryRowContext(r.Context(), `
				INSERT INTO idempotency_keys (actor, idempotency_key, endpoint, request_hash)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (actor, idempotency_key, endpoint) DO NOTHING
				RETURNING id
			`, actor, key, endpoint, requestHash).Scan(&id)

			if insertErr == nil {
				// New key — this is the only path where the real handler runs,
				// which is the load-bearing property: idempotent replay must
				// never write a second audit_log row.
				rec := &idempotencyResponseRecorder{ResponseWriter: w}
				next.ServeHTTP(rec, r)

				if _, updateErr := m.db.ExecContext(r.Context(), `
					UPDATE idempotency_keys
					SET response_status = $1, response_body = $2, completed_at = now()
					WHERE id = $3
				`, rec.status, rec.body.Bytes(), id); updateErr != nil {
					m.logger.Warn("idempotency: failed to persist response",
						zap.String("idempotency_key", key),
						zap.String("endpoint", endpoint),
						zap.Error(updateErr))
				}
				return
			}

			if insertErr != sql.ErrNoRows {
				m.logger.Error("idempotency: insert failed",
					zap.String("idempotency_key", key),
					zap.String("endpoint", endpoint),
					zap.Error(insertErr))
				writeIdempotencyError(w, http.StatusInternalServerError, "idempotency check failed")
				return
			}

			// Conflict — a row already exists for (idempotency_key, endpoint).
			var (
				storedHash     string
				completedAt    sql.NullTime
				responseStatus sql.NullInt64
				responseBody   []byte
			)
			lookupErr := m.db.QueryRowContext(r.Context(), `
				SELECT request_hash, completed_at, response_status, response_body
				FROM idempotency_keys
				WHERE actor = $1 AND idempotency_key = $2 AND endpoint = $3
			`, actor, key, endpoint).Scan(&storedHash, &completedAt, &responseStatus, &responseBody)
			if lookupErr != nil {
				m.logger.Error("idempotency: lookup failed",
					zap.String("idempotency_key", key),
					zap.String("endpoint", endpoint),
					zap.Error(lookupErr))
				writeIdempotencyError(w, http.StatusInternalServerError, "idempotency check failed")
				return
			}

			if storedHash != requestHash {
				writeIdempotencyError(w, http.StatusConflict,
					"idempotency key reused with a different request")
				return
			}

			if !completedAt.Valid {
				// Fail fast rather than poll/block — a self-hosted/free-tier
				// deployment shouldn't have one request wait on another's DB row.
				writeIdempotencyError(w, http.StatusConflict, "request still processing")
				return
			}

			// Replay the stored response verbatim. The real handler is never
			// invoked, so writeAudit never runs a second time.
			w.Header().Set("Content-Type", "application/json")
			status := http.StatusOK
			if responseStatus.Valid {
				status = int(responseStatus.Int64)
			}
			w.WriteHeader(status)
			_, _ = w.Write(responseBody)
		})
	}
}

func writeIdempotencyError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// StartCleanup launches a background goroutine that purges expired
// idempotency_keys rows once per hour. Intended to be called as
// "go idempotencyMw.StartCleanup(bgCtx)" alongside the other background
// workers in cmd/main.go — it shares the same cancellable root context.
func (m *IdempotencyMiddleware) StartCleanup(ctx context.Context) {
	m.logger.Info("idempotency-key cleanup starting", zap.Duration("interval", idempotencyCleanupInterval))
	ticker := time.NewTicker(idempotencyCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("idempotency-key cleanup stopped")
			return
		case <-ticker.C:
			m.purgeExpired(ctx)
		}
	}
}

func (m *IdempotencyMiddleware) purgeExpired(ctx context.Context) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at < NOW()`)
	if err != nil {
		m.logger.Warn("idempotency-key cleanup failed", zap.Error(err))
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m.logger.Info("idempotency-key cleanup purged expired rows", zap.Int64("count", n))
	}
}
