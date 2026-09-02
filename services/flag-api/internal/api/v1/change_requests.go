package v1

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
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
	// TEN-1a-3: this had no project filter at all, so any authenticated user
	// (this route is deliberately reachable by any role — see the SEC-3
	// exemption in cmd/authz_routes_test.go, unchanged here) could list every
	// project's pending/approved/rejected change requests and payloads.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

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
		WHERE status = $1 AND project_id = $2
		ORDER BY created_at DESC
		LIMIT 100
	`, status, projectID)
	if err != nil {
		h.logger.Error("list change requests query", zap.Error(err))
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	requests := []ChangeRequest{}
	for rows.Next() {
		var cr ChangeRequest
		var approvedBy []string
		// approved_by is a Postgres TEXT[]; lib/pq only converts that to a Go
		// []string through pq.Array — scanning directly into &approvedBy (as
		// this did before) fails on every row with "unsupported Scan", so the
		// row is silently dropped by the continue below and this endpoint has
		// never actually returned anything. Found by TEN-1a-3's tenancy test,
		// which is the first thing to ever exercise this against real rows.
		if scanErr := rows.Scan(
			&cr.ID, &cr.FlagKey, &cr.Environment, &cr.RequestedBy, &cr.Status,
			&cr.ChangePayload, pq.Array(&approvedBy),
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

// ApproveChangeRequest handles POST /api/v1/change-requests/{id}/approve.
// No request body: the approver is the authenticated caller (see SEC-3 note
// below), not something a caller can name.
func (h *ChangeRequestHandler) ApproveChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// TEN-1a-3: this matched id alone — a caller who learned another
	// project's change_request id (e.g. via the ListChangeRequests leak this
	// same fix closes) could approve a mutation of a flag they have no role in.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	// SEC-3: the approver used to be a client-supplied "approved_by" body
	// field, trusted verbatim — any caller could claim to be any approver.
	// It is now the authenticated actor, and requested_by != $1 in the WHERE
	// below rejects self-approval: the person who proposed a change must not
	// be the one who approves it.
	actor := actorFromContext(r.Context())

	now := time.Now().UTC()
	result, err := h.db.ExecContext(r.Context(), `
		UPDATE change_requests
		SET status      = 'APPROVED',
		    approved_by = array_append(COALESCE(approved_by, '{}'), $1),
		    updated_at  = $2
		WHERE id = $3 AND status = 'PENDING' AND project_id = $4 AND requested_by != $1
	`, actor, now, id, projectID)
	if err != nil {
		h.logger.Error("approve change request", zap.String("id", id), zap.Error(err))
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// The UPDATE's WHERE clause can't say WHY it matched nothing — ask
		// separately so a self-approval attempt gets an honest 403 instead of
		// being indistinguishable from a genuine 404.
		var requestedBy string
		scanErr := h.db.QueryRowContext(r.Context(),
			`SELECT requested_by FROM change_requests WHERE id=$1 AND project_id=$2 AND status='PENDING'`,
			id, projectID,
		).Scan(&requestedBy)
		if scanErr == nil && requestedBy == actor {
			http.Error(w, `{"error":"a change request cannot be approved by the user who requested it"}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":"change request not found or not in PENDING state"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "APPROVED"})
}

// RejectChangeRequest handles POST /api/v1/change-requests/{id}/reject
// Body: { "reason": string } (optional)
func (h *ChangeRequestHandler) RejectChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// TEN-1a-3: same cross-project fix as ApproveChangeRequest above.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	// SEC-3: the rejecter is the authenticated actor, not a client-supplied
	// "rejected_by" body field, mirroring the ApproveChangeRequest fix above.
	// Unlike approval, self-rejection is fine — withdrawing your own proposal
	// isn't a security concern, so there is no requested_by check here.
	actor := actorFromContext(r.Context())

	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // reason is optional; an empty/absent body is not an error

	now := time.Now().UTC()
	result, err := h.db.ExecContext(r.Context(), `
		UPDATE change_requests
		SET status           = 'REJECTED',
		    rejected_by      = $1,
		    rejection_reason = $2,
		    updated_at       = $3
		WHERE id = $4 AND status = 'PENDING' AND project_id = $5
	`, actor, body.Reason, now, id, projectID)
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
