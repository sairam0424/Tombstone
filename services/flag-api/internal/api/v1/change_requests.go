package v1

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ChangeRequestHandler handles the approval-queue endpoints for flag change requests.
type ChangeRequestHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

// NewChangeRequestHandler constructs a ChangeRequestHandler.
func NewChangeRequestHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *ChangeRequestHandler {
	return &ChangeRequestHandler{db: db, rdb: rdb, logger: logger}
}

// ChangeRequest is the JSON representation of a change_requests row.
type ChangeRequest struct {
	ID              string          `json:"id"`
	FlagKey         string          `json:"flag_key"`
	Environment     string          `json:"environment"`
	RequestedBy     string          `json:"requested_by"`
	Status          string          `json:"status"`
	ChangePayload   json.RawMessage `json:"change_payload"`
	ApprovedBy      []string        `json:"approved_by"`
	RejectedBy      *string         `json:"rejected_by,omitempty"`
	RejectionReason *string         `json:"rejection_reason,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
}

// ListChangeRequests handles GET /api/v1/change-requests?status=PENDING
// Returns up to 100 change requests ordered by created_at DESC.
// The status query param defaults to "PENDING".
func (h *ChangeRequestHandler) ListChangeRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "PENDING"
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, flag_key, environment, requested_by, status,
		       change_payload, COALESCE(approved_by, '{}'),
		       rejected_by, rejection_reason,
		       EXTRACT(EPOCH FROM created_at)::bigint,
		       EXTRACT(EPOCH FROM updated_at)::bigint
		FROM change_requests
		WHERE status = $1
		ORDER BY created_at DESC
		LIMIT 100
	`, status)
	if err != nil {
		h.logger.Error("list change requests query", zap.Error(err))
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	requests := []ChangeRequest{}
	for rows.Next() {
		var cr ChangeRequest
		var approvedBy []string
		if scanErr := rows.Scan(
			&cr.ID, &cr.FlagKey, &cr.Environment, &cr.RequestedBy, &cr.Status,
			&cr.ChangePayload, &approvedBy,
			&cr.RejectedBy, &cr.RejectionReason,
			&cr.CreatedAt, &cr.UpdatedAt,
		); scanErr != nil {
			h.logger.Warn("scan change request row", zap.Error(scanErr))
			continue
		}
		cr.ApprovedBy = approvedBy
		requests = append(requests, cr)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate change request rows", zap.Error(err))
		http.Error(w, `{"error":"iteration failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"requests": requests})
}

// ApproveChangeRequest handles POST /api/v1/change-requests/{id}/approve
// Body: { "approved_by": string }
func (h *ChangeRequestHandler) ApproveChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		ApprovedBy string `json:"approved_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ApprovedBy == "" {
		http.Error(w, `{"error":"approved_by required"}`, http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	result, err := h.db.ExecContext(r.Context(), `
		UPDATE change_requests
		SET status      = 'APPROVED',
		    approved_by = array_append(COALESCE(approved_by, '{}'), $1),
		    updated_at  = $2
		WHERE id = $3 AND status = 'PENDING'
	`, body.ApprovedBy, now, id)
	if err != nil {
		h.logger.Error("approve change request", zap.String("id", id), zap.Error(err))
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		http.Error(w, `{"error":"change request not found or not in PENDING state"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "APPROVED"})
}

// RejectChangeRequest handles POST /api/v1/change-requests/{id}/reject
// Body: { "rejected_by": string, "reason": string }
func (h *ChangeRequestHandler) RejectChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		RejectedBy string `json:"rejected_by"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RejectedBy == "" {
		http.Error(w, `{"error":"rejected_by required"}`, http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	result, err := h.db.ExecContext(r.Context(), `
		UPDATE change_requests
		SET status           = 'REJECTED',
		    rejected_by      = $1,
		    rejection_reason = $2,
		    updated_at       = $3
		WHERE id = $4 AND status = 'PENDING'
	`, body.RejectedBy, body.Reason, now, id)
	if err != nil {
		h.logger.Error("reject change request", zap.String("id", id), zap.Error(err))
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		http.Error(w, `{"error":"change request not found or not in PENDING state"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "REJECTED"})
}
