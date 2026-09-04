package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
	"go.uber.org/zap"
)

// ScheduledHandler handles the Scheduled Changes API.
// Routes:
//
//	POST   /api/v1/flags/{key}/schedule
//	GET    /api/v1/flags/{key}/schedule?environment=&status=
//	DELETE /api/v1/flags/{key}/schedule/{id}
type ScheduledHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
	audit  *audit.Writer
}

// NewScheduledHandler constructs a ScheduledHandler.
func NewScheduledHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger, auditW *audit.Writer) *ScheduledHandler {
	return &ScheduledHandler{db: db, rdb: rdb, logger: logger, audit: auditW}
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

	// TEN-1a: the existence check below matched key alone across ALL
	// projects, so a caller could schedule a change against a flag that only
	// exists in a different project — and the background scheduler would
	// then execute it (see scheduler.go), a real cross-tenant write.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

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

	// Verify the flag exists IN THE CALLER'S OWN PROJECT.
	flagExists, err := sqlcgen.New(h.db).FlagExistsInProjectNotArchived(r.Context(), sqlcgen.FlagExistsInProjectNotArchivedParams{
		Key:       key,
		ProjectID: projectID,
	})
	if err != nil || !flagExists {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}

	actor := actorFromContext(r.Context())
	payloadJSON, err := json.Marshal(req.ChangePayload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize change_payload")
		return
	}

	row, err := sqlcgen.New(h.db).CreateScheduledChange(r.Context(), sqlcgen.CreateScheduledChangeParams{
		ID:            uuid.New().String(),
		FlagKey:       key,
		Environment:   req.Environment,
		ScheduledFor:  scheduledAt,
		ChangePayload: payloadJSON,
		CreatedBy:     actor,
		ProjectID:     sql.NullString{String: projectID, Valid: true},
	})
	if err != nil {
		h.logger.Error("create scheduled change", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sc := ScheduledChange{
		ID:            row.ID,
		FlagKey:       row.FlagKey,
		Environment:   row.Environment,
		ScheduledFor:  row.ScheduledFor,
		ChangePayload: row.ChangePayload,
		CreatedBy:     row.CreatedBy,
		Status:        row.Status,
		CreatedAt:     row.CreatedAt,
	}
	if row.ExecutedAt.Valid {
		ts := row.ExecutedAt.Time.Unix()
		sc.ExecutedAt = &ts
	}
	if row.ErrorMessage.Valid {
		sc.ErrorMessage = &row.ErrorMessage.String
	}

	h.writeAudit(r.Context(), projectID, key, req.Environment, actor, "scheduled_change_created",
		nil, &sc, ipFromRequest(r))

	writeJSON(w, http.StatusCreated, sc)
}

// ListSchedule handles GET /api/v1/flags/{key}/schedule?environment=&status=
// Returns all scheduled changes for the flag, optionally filtered by environment and/or status.
func (h *ScheduledHandler) ListSchedule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	envFilter := r.URL.Query().Get("environment")
	statusFilter := r.URL.Query().Get("status")

	// TEN-1a: this previously matched flag_key alone across ALL projects — a
	// VIEWER in one project could read another project's pending scheduled
	// change_payload (future enabled/rollout_pct) for a same-keyed flag.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	rows, err := sqlcgen.New(h.db).ListScheduledChanges(r.Context(), sqlcgen.ListScheduledChangesParams{
		FlagKey:           key,
		ProjectID:         sql.NullString{String: projectID, Valid: true},
		EnvironmentFilter: envFilter,
		StatusFilter:      statusFilter,
	})
	if err != nil {
		h.logger.Error("list scheduled changes", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	changes := []ScheduledChange{}
	for _, row := range rows {
		sc := ScheduledChange{
			ID:            row.ID,
			FlagKey:       row.FlagKey,
			Environment:   row.Environment,
			ScheduledFor:  row.ScheduledFor,
			ChangePayload: row.ChangePayload,
			CreatedBy:     row.CreatedBy,
			Status:        row.Status,
			CreatedAt:     row.CreatedAt,
		}
		if row.ExecutedAt.Valid {
			ts := row.ExecutedAt.Time.Unix()
			sc.ExecutedAt = &ts
		}
		if row.ErrorMessage.Valid {
			sc.ErrorMessage = &row.ErrorMessage.String
		}
		changes = append(changes, sc)
	}

	writeJSON(w, http.StatusOK, map[string]any{"scheduled_changes": changes, "total": len(changes)})
}

// CancelSchedule handles DELETE /api/v1/flags/{key}/schedule/{id}.
// Sets status=CANCELLED — never hard-deletes.
func (h *ScheduledHandler) CancelSchedule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	id := chi.URLParam(r, "id")

	// TEN-1a: this previously matched id+flag_key alone across ALL projects —
	// a caller with flags:write in their own project could cancel another
	// project's scheduled change if they knew (e.g. via the ListSchedule leak
	// this same fix closes) its id.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	actor := actorFromContext(r.Context())

	n, err := sqlcgen.New(h.db).CancelScheduledChange(r.Context(), sqlcgen.CancelScheduledChangeParams{
		ID:        id,
		FlagKey:   key,
		ProjectID: sql.NullString{String: projectID, Valid: true},
	})
	if err != nil {
		h.logger.Error("cancel scheduled change", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if n == 0 {
		// Might not exist, might already be non-PENDING, or belongs to another project.
		status, scanErr := sqlcgen.New(h.db).GetScheduledChangeStatus(r.Context(), sqlcgen.GetScheduledChangeStatusParams{
			ID:        id,
			FlagKey:   key,
			ProjectID: sql.NullString{String: projectID, Valid: true},
		})
		if scanErr == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "scheduled change not found")
			return
		}
		writeError(w, http.StatusConflict, "scheduled change cannot be cancelled (status: "+status+")")
		return
	}

	h.writeAudit(r.Context(), projectID, key, "", actor, "scheduled_change_cancelled",
		map[string]any{"id": id}, map[string]any{"status": "CANCELLED"}, ipFromRequest(r))

	w.WriteHeader(http.StatusNoContent)
}

// writeAudit writes an append-only audit entry with Merkle hash linking.
// Mirrors v1.FlagHandler.writeAudit exactly so ScheduledHandler can log without
// holding a reference to FlagHandler.
func (h *ScheduledHandler) writeAudit(ctx context.Context, projectID, flagKey, env, actor, eventType string, prev, curr any, ip string) {
	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

	// AUD-1: this used to hash sha256(lastID + lastTs) — two fields, no
	// separator — while flags.go and scheduler.go hashed six pipe-joined fields.
	// Any chain containing a scheduled-change entry was therefore unverifiable.
	// All writers now share one keyed, advisory-locked implementation.
	if h.audit == nil {
		h.logger.Warn("audit log write skipped — no audit writer configured")
		return
	}
	if _, _, err := h.audit.Append(ctx, audit.Entry{
		FlagKey:     flagKey,
		Environment: env,
		Actor:       actor,
		EventType:   eventType,
		PrevState:   prevJSON,
		NewState:    currJSON,
		IPAddress:   ip,
		ProjectID:   projectID,
	}); err != nil {
		h.logger.Warn("audit log write failed", zap.Error(err))
	}
}
