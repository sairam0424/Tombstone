package v1

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ScheduledHandler handles the Scheduled Changes API.
// Routes:
//   POST   /api/v1/flags/{key}/schedule
//   GET    /api/v1/flags/{key}/schedule?environment=&status=
//   DELETE /api/v1/flags/{key}/schedule/{id}
type ScheduledHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

// NewScheduledHandler constructs a ScheduledHandler.
func NewScheduledHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *ScheduledHandler {
	return &ScheduledHandler{db: db, rdb: rdb, logger: logger}
}

// ScheduledChange is the API representation of a scheduled_changes row.
type ScheduledChange struct {
	ID            string          `json:"id"`
	FlagKey       string          `json:"flag_key"`
	Environment   string          `json:"environment"`
	ScheduledFor  int64           `json:"scheduled_for"` // Unix timestamp
	ChangePayload json.RawMessage `json:"change_payload"`
	CreatedBy     string          `json:"created_by"`
	Status        string          `json:"status"`
	ExecutedAt    *int64          `json:"executed_at,omitempty"`
	ErrorMessage  *string         `json:"error_message,omitempty"`
	CreatedAt     int64           `json:"created_at"`
}

// ChangePayloadFields is what callers supply — either enabled or rollout_pct (or both).
type ChangePayloadFields struct {
	Enabled    *bool `json:"enabled"`
	RolloutPct *int  `json:"rollout_pct"`
}

// CreateScheduleRequest is the body for POST /api/v1/flags/{key}/schedule.
type CreateScheduleRequest struct {
	Environment   string              `json:"environment"`
	ScheduledFor  int64               `json:"scheduled_for"` // Unix timestamp (must be future)
	ChangePayload ChangePayloadFields `json:"change_payload"`
}

// CreateSchedule handles POST /api/v1/flags/{key}/schedule.
// Validates that scheduled_for is in the future and change_payload has at least one field.
// Inserts a PENDING scheduled_change row and returns 201.
func (h *ScheduledHandler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Environment == "" {
		writeError(w, http.StatusBadRequest, "environment is required")
		return
	}
	if req.ScheduledFor == 0 {
		writeError(w, http.StatusBadRequest, "scheduled_for is required")
		return
	}
	scheduledAt := time.Unix(req.ScheduledFor, 0)
	if !scheduledAt.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "scheduled_for must be a future timestamp")
		return
	}
	if req.ChangePayload.Enabled == nil && req.ChangePayload.RolloutPct == nil {
		writeError(w, http.StatusBadRequest, "change_payload must contain at least one of: enabled, rollout_pct")
		return
	}

	// Verify the flag exists
	var flagExists bool
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM flags WHERE key=$1 AND state != 'ARCHIVED')`, key,
	).Scan(&flagExists); err != nil || !flagExists {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}

	actor := actorFromContext(r.Context())
	payloadJSON, err := json.Marshal(req.ChangePayload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize change_payload")
		return
	}

	var sc ScheduledChange
	var executedAt sql.NullTime
	var errorMsg sql.NullString

	err = h.db.QueryRowContext(r.Context(), `
		INSERT INTO scheduled_changes
		    (id, flag_key, environment, scheduled_for, change_payload, created_by, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING')
		RETURNING
		    id, flag_key, environment,
		    EXTRACT(EPOCH FROM scheduled_for)::bigint,
		    change_payload, created_by, status,
		    executed_at,
		    error_message,
		    EXTRACT(EPOCH FROM created_at)::bigint
	`, uuid.New().String(), key, req.Environment, scheduledAt, payloadJSON, actor).
		Scan(
			&sc.ID, &sc.FlagKey, &sc.Environment, &sc.ScheduledFor,
			&sc.ChangePayload, &sc.CreatedBy, &sc.Status,
			&executedAt, &errorMsg, &sc.CreatedAt,
		)
	if err != nil {
		h.logger.Error("create scheduled change", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if executedAt.Valid {
		ts := executedAt.Time.Unix()
		sc.ExecutedAt = &ts
	}
	if errorMsg.Valid {
		sc.ErrorMessage = &errorMsg.String
	}

	h.writeAudit(r.Context(), key, req.Environment, actor, "scheduled_change_created",
		nil, &sc, ipFromRequest(r))

	writeJSON(w, http.StatusCreated, sc)
}

// ListSchedule handles GET /api/v1/flags/{key}/schedule?environment=&status=
// Returns all scheduled changes for the flag, optionally filtered by environment and/or status.
func (h *ScheduledHandler) ListSchedule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	envFilter := r.URL.Query().Get("environment")
	statusFilter := r.URL.Query().Get("status")

	query := `
		SELECT
		    id, flag_key, environment,
		    EXTRACT(EPOCH FROM scheduled_for)::bigint,
		    change_payload, created_by, status,
		    executed_at,
		    error_message,
		    EXTRACT(EPOCH FROM created_at)::bigint
		FROM scheduled_changes
		WHERE flag_key = $1
	`
	args := []any{key}
	argIdx := 2

	if envFilter != "" {
		query += ` AND environment = $` + itoa(argIdx)
		args = append(args, envFilter)
		argIdx++
	}
	if statusFilter != "" {
		query += ` AND status = $` + itoa(argIdx)
		args = append(args, statusFilter)
	}
	query += ` ORDER BY scheduled_for ASC`

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list scheduled changes", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	changes := []ScheduledChange{}
	for rows.Next() {
		var sc ScheduledChange
		var executedAt sql.NullTime
		var errorMsg sql.NullString

		if err := rows.Scan(
			&sc.ID, &sc.FlagKey, &sc.Environment, &sc.ScheduledFor,
			&sc.ChangePayload, &sc.CreatedBy, &sc.Status,
			&executedAt, &errorMsg, &sc.CreatedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		if executedAt.Valid {
			ts := executedAt.Time.Unix()
			sc.ExecutedAt = &ts
		}
		if errorMsg.Valid {
			sc.ErrorMessage = &errorMsg.String
		}
		changes = append(changes, sc)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "rows iteration failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"scheduled_changes": changes, "total": len(changes)})
}

// CancelSchedule handles DELETE /api/v1/flags/{key}/schedule/{id}.
// Sets status=CANCELLED — never hard-deletes.
func (h *ScheduledHandler) CancelSchedule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	id := chi.URLParam(r, "id")

	actor := actorFromContext(r.Context())

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE scheduled_changes
		SET status = 'CANCELLED'
		WHERE id = $1 AND flag_key = $2 AND status = 'PENDING'
	`, id, key)
	if err != nil {
		h.logger.Error("cancel scheduled change", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		// Might not exist or might already be non-PENDING
		var status string
		scanErr := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM scheduled_changes WHERE id=$1 AND flag_key=$2`, id, key,
		).Scan(&status)
		if scanErr == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "scheduled change not found")
			return
		}
		writeError(w, http.StatusConflict, "scheduled change cannot be cancelled (status: "+status+")")
		return
	}

	h.writeAudit(r.Context(), key, "", actor, "scheduled_change_cancelled",
		map[string]any{"id": id}, map[string]any{"status": "CANCELLED"}, ipFromRequest(r))

	w.WriteHeader(http.StatusNoContent)
}

// writeAudit writes an append-only audit entry with Merkle hash linking.
// Mirrors v1.FlagHandler.writeAudit exactly so ScheduledHandler can log without
// holding a reference to FlagHandler.
func (h *ScheduledHandler) writeAudit(ctx context.Context, flagKey, env, actor, eventType string, prev, curr any, ip string) {
	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

	var lastID, lastTs string
	_ = h.db.QueryRowContext(ctx, `
		SELECT id, EXTRACT(EPOCH FROM created_at)::text FROM audit_log
		WHERE flag_key=$1 ORDER BY created_at DESC LIMIT 1
	`, flagKey).Scan(&lastID, &lastTs)

	prevHash := ""
	if lastID != "" {
		hb := sha256.Sum256([]byte(lastID + lastTs))
		prevHash = fmt.Sprintf("%x", hb)
	}

	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO audit_log (id, flag_key, environment, actor, event_type, prev_state, new_state, ip_address, prev_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, uuid.New().String(), flagKey, env, actor, eventType, prevJSON, currJSON, ip, prevHash); err != nil {
		h.logger.Warn("audit log write failed", zap.Error(err))
	}
}

// itoa converts a small integer to its decimal string — avoids importing strconv just for this.
func itoa(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	// For arg indices beyond 9 (unlikely in this codebase, but correct).
	return string(digits[n/10]) + string(digits[n%10])
}
