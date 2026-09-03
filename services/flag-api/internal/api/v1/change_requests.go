package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
)

// ChangeRequestHandler handles the approval-queue endpoints for flag change requests.
type ChangeRequestHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
	// audit is the single writer for the hash-chained audit log (AUD-1).
	// change_requests previously had no audit integration at all (SEC-3b).
	audit *audit.Writer
}

// NewChangeRequestHandler constructs a ChangeRequestHandler.
func NewChangeRequestHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger, auditW *audit.Writer) *ChangeRequestHandler {
	return &ChangeRequestHandler{db: db, rdb: rdb, logger: logger, audit: auditW}
}

// flagEnvironmentChangePayload is the change_requests.change_payload shape
// this handler understands for a flag-environment mutation — the only kind
// ProposeChangeRequest creates today. detectOrphans/orphan_detector.go write
// a different, unrelated payload shape ({"reason":...}) for their own
// informational change requests, which are never approved through the
// apply path below (they exist to flag orphaned flags for human review, not
// to propose a specific enabled/rollout_pct value).
type flagEnvironmentChangePayload struct {
	Enabled    bool `json:"enabled"`
	RolloutPct int  `json:"rollout_pct"`
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

	rows, err := sqlcgen.New(h.db).ListChangeRequests(r.Context(), sqlcgen.ListChangeRequestsParams{
		Status: status, ProjectID: sql.NullString{String: projectID, Valid: true},
	})
	if err != nil {
		h.logger.Error("list change requests query", zap.Error(err))
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}

	requests := []ChangeRequest{}
	for _, row := range rows {
		cr := ChangeRequest{
			ID: row.ID, FlagKey: row.FlagKey, Environment: row.Environment, RequestedBy: row.RequestedBy,
			Status: row.Status, ChangePayload: row.ChangePayload, ApprovedBy: row.ApprovedBy,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		if row.RejectedBy.Valid {
			cr.RejectedBy = &row.RejectedBy.String
		}
		if row.RejectionReason.Valid {
			cr.RejectionReason = &row.RejectionReason.String
		}
		requests = append(requests, cr)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"requests": requests})
}

// ProposeChangeRequestBody is the request body for POST /api/v1/change-requests.
type ProposeChangeRequestBody struct {
	FlagKey     string `json:"flag_key"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
}

// ProposeChangeRequest handles POST /api/v1/change-requests.
//
// SEC-3b: change_requests previously had exactly two writers — scim.go and
// orphan_detector.go — both hardcoding requested_by to a literal 'system'
// string; there was no way for a human to actually propose a normal flag
// mutation through the four-eyes queue. This is that endpoint: it requires
// the same environments:write permission UpdateEnvironment itself requires
// (proposing a change should need no more, and no less, privilege than
// making it directly would), and creates a PENDING row whose change_payload
// ApproveChangeRequest below knows how to apply once quorum is met.
func (h *ChangeRequestHandler) ProposeChangeRequest(w http.ResponseWriter, r *http.Request) {
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor := actorFromContext(r.Context())

	var body ProposeChangeRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.FlagKey == "" || body.Environment == "" {
		writeError(w, http.StatusBadRequest, "flag_key and environment are required")
		return
	}
	if body.RolloutPct < 0 || body.RolloutPct > 100 {
		writeError(w, http.StatusBadRequest, "rollout_pct must be between 0 and 100")
		return
	}

	// Fail fast with a clear error now rather than a confusing one at apply
	// time, potentially much later once quorum is finally met.
	if _, err := sqlcgen.New(h.db).ChangeRequestTargetExists(r.Context(), sqlcgen.ChangeRequestTargetExistsParams{
		Key: body.FlagKey, Environment: body.Environment, ProjectID: projectID,
	}); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "flag or environment not found")
			return
		}
		h.logger.Error("propose change request existence check", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	payload, err := json.Marshal(flagEnvironmentChangePayload{Enabled: body.Enabled, RolloutPct: body.RolloutPct})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal change payload")
		return
	}

	// Snapshot the project's CURRENT quorum policy onto the row now, rather
	// than having ApproveChangeRequest re-read projects.required_approvals
	// fresh on every single approval call. Without this, lowering a
	// project's quorum mid-flight would silently downgrade the bar an
	// in-flight proposal is held to — the two approvers who already voted
	// under a stricter policy would never know the goalposts moved.
	requiredApprovals32, err := sqlcgen.New(h.db).GetProjectRequiredApprovals(r.Context(), projectID)
	if err != nil {
		h.logger.Error("propose change request read required_approvals", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	createRow, err := sqlcgen.New(h.db).CreateChangeRequest(r.Context(), sqlcgen.CreateChangeRequestParams{
		FlagKey: body.FlagKey, Environment: body.Environment, RequestedBy: actor, ChangePayload: payload,
		ProjectID: sql.NullString{String: projectID, Valid: true}, RequiredApprovals: requiredApprovals32,
	})
	if err != nil {
		// idx_change_requests_one_pending_proposal (migration 020): only one
		// applicable (flag-environment-shaped) proposal may be PENDING at a
		// time for a given flag+environment — otherwise two independently
		// quorum-approved requests could apply in sequence and silently
		// clobber each other's result with no error to either approving group.
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code.Name() == "unique_violation" {
			writeError(w, http.StatusConflict, "a change request for this flag and environment is already pending")
			return
		}
		h.logger.Error("propose change request insert", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "insert failed")
		return
	}
	cr := ChangeRequest{
		ID: createRow.ID, CreatedAt: createRow.CreatedAt, UpdatedAt: createRow.UpdatedAt,
		FlagKey: body.FlagKey, Environment: body.Environment, RequestedBy: actor, Status: "PENDING",
		ChangePayload: payload, ApprovedBy: []string{},
	}

	h.writeAudit(r.Context(), projectID, body.FlagKey, body.Environment, actor, "change_request_proposed",
		nil, map[string]any{"change_request_id": cr.ID, "enabled": body.Enabled, "rollout_pct": body.RolloutPct}, ipFromRequest(r))

	writeJSON(w, http.StatusCreated, cr)
}

// decodeFlagEnvironmentChangePayload reports whether raw is a
// flagEnvironmentChangePayload (both "enabled" and "rollout_pct" present) —
// as opposed to, e.g., an orphan-detection informational payload
// ({"reason":...}), which has neither key and json.Unmarshal would otherwise
// silently decode into an all-zero-value (and wrong) payload instead of
// reporting that it doesn't apply here.
func decodeFlagEnvironmentChangePayload(raw json.RawMessage) (flagEnvironmentChangePayload, bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return flagEnvironmentChangePayload{}, false
	}
	if _, ok := probe["enabled"]; !ok {
		return flagEnvironmentChangePayload{}, false
	}
	if _, ok := probe["rollout_pct"]; !ok {
		return flagEnvironmentChangePayload{}, false
	}
	var payload flagEnvironmentChangePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return flagEnvironmentChangePayload{}, false
	}
	return payload, true
}

// ApproveChangeRequest handles POST /api/v1/change-requests/{id}/approve.
// No request body: the approver is the authenticated caller (see SEC-3 note
// below), not something a caller can name.
//
// SEC-3b: this used to flip PENDING straight to APPROVED after exactly one
// approval and stop there — there was no quorum concept (regardless of a
// project's intended N-of-M policy) and nothing ever actually applied the
// proposed change to flag_environments; APPLIED existed as a schema value
// but nothing ever wrote it. Approval now accumulates distinct approvers
// (a caller who already approved gets 409, not a second, self-duplicating
// vote) until len(approved_by) reaches the project's required_approvals,
// at which point — in the same transaction as recording the approval — the
// change_payload is applied and the row moves straight to APPLIED. Rows
// whose payload isn't a flag-environment proposal (the orphan-detection
// informational rows scim.go/orphan_detector.go create) fall back to the
// pre-SEC-3b behavior of a plain APPROVED status flip, since there is
// nothing for those to apply.
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
	// It is now the authenticated actor.
	actor := actorFromContext(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer func() { _ = tx.Rollback() }()

	crRow, err := sqlcgen.New(tx).GetChangeRequestForApproval(r.Context(), sqlcgen.GetChangeRequestForApprovalParams{
		ID: id, ProjectID: sql.NullString{String: projectID, Valid: true},
	})
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "change request not found or not in PENDING state")
		return
	}
	if err != nil {
		h.logger.Error("approve change request lookup", zap.String("id", id), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	cr := struct {
		FlagKey           string
		Environment       string
		RequestedBy       string
		ChangePayload     json.RawMessage
		ApprovedBy        []string
		RequiredApprovals int
	}{
		FlagKey: crRow.FlagKey, Environment: crRow.Environment, RequestedBy: crRow.RequestedBy,
		ChangePayload: crRow.ChangePayload, ApprovedBy: crRow.ApprovedBy, RequiredApprovals: int(crRow.RequiredApprovals),
	}

	// requested_by == actor rejects self-approval: the person who proposed a
	// change must not be the one who approves it.
	if cr.RequestedBy == actor {
		writeError(w, http.StatusForbidden, "a change request cannot be approved by the user who requested it")
		return
	}
	for _, a := range cr.ApprovedBy {
		if a == actor {
			writeError(w, http.StatusConflict, "you have already approved this change request")
			return
		}
	}
	newApprovedBy := append(cr.ApprovedBy, actor)

	// required_approvals is read from THIS ROW (captured at propose time),
	// not projects.required_approvals live — that pinned value is exactly
	// what was already locked by the FOR UPDATE above, so there is nothing
	// further to read or lock here. Re-reading projects fresh on every
	// approval call would let lowering a project's quorum mid-flight
	// silently downgrade the bar an in-flight proposal is held to.
	requiredApprovals := cr.RequiredApprovals
	quorumMet := len(newApprovedBy) >= requiredApprovals

	payload, applicable := decodeFlagEnvironmentChangePayload(cr.ChangePayload)

	if !quorumMet || !applicable {
		status := "PENDING"
		if quorumMet {
			// Quorum met but nothing to apply (an informational request) —
			// this is the only case that still lands on APPROVED.
			status = "APPROVED"
		}
		if err := sqlcgen.New(tx).RecordApproval(r.Context(), sqlcgen.RecordApprovalParams{
			ApprovedBy: newApprovedBy, Status: status, ID: id,
		}); err != nil {
			h.logger.Error("record approval", zap.String("id", id), zap.Error(err))
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "commit failed")
			return
		}
		h.writeAudit(r.Context(), projectID, cr.FlagKey, cr.Environment, actor, "change_request_approved",
			nil, map[string]any{"change_request_id": id, "approvals": newApprovedBy, "required_approvals": requiredApprovals}, ipFromRequest(r))
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status, "approvals": len(newApprovedBy), "required_approvals": requiredApprovals,
		})
		return
	}

	// Quorum met on a real flag-environment proposal — apply it now, in the
	// same transaction as recording the approval that completed the quorum.
	var prev FlagEnvironmentState
	if prevRow, prevErr := sqlcgen.New(tx).GetFlagEnvironmentPrevState(r.Context(), sqlcgen.GetFlagEnvironmentPrevStateParams{
		Key: cr.FlagKey, Environment: cr.Environment, ProjectID: projectID,
	}); prevErr == nil {
		prev = FlagEnvironmentState{
			FlagID: prevRow.FlagID, FlagKey: prevRow.Key, Environment: prevRow.Environment,
			Enabled: prevRow.Enabled, RolloutPct: int(prevRow.RolloutPct), SafeDefault: prevRow.SafeDefault,
			UpdatedAt: prevRow.UpdatedAt,
		}
	}

	// updated_by is the approver whose action just executed this write, not
	// the original proposer (cr.RequestedBy) — matching how every other
	// direct-write path (UpdateEnvironment, KillSwitch) attributes
	// updated_by to whoever's API call performed the mutation. Self-approval
	// is already rejected above, so actor != cr.RequestedBy is guaranteed
	// here.
	n, err := sqlcgen.New(tx).UpdateFlagEnvironment(r.Context(), sqlcgen.UpdateFlagEnvironmentParams{
		Enabled: payload.Enabled, RolloutPct: int32(payload.RolloutPct), UpdatedBy: actor,
		Key: cr.FlagKey, Environment: cr.Environment, ProjectID: projectID,
	})
	if err != nil {
		h.logger.Error("apply change request", zap.String("id", id), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "apply failed")
		return
	}
	if n == 0 {
		// The flag or environment this proposal targeted no longer exists —
		// rolling back leaves the approval unrecorded so the caller can see
		// the real error and decide (reject the now-stale request, etc.)
		// rather than silently losing an approval to a phantom apply.
		writeError(w, http.StatusConflict, "flag or environment no longer exists — cannot apply this change request")
		return
	}

	if err := sqlcgen.New(tx).FinalizeAppliedChangeRequest(r.Context(), sqlcgen.FinalizeAppliedChangeRequestParams{
		ApprovedBy: newApprovedBy, ID: id,
	}); err != nil {
		h.logger.Error("finalize applied change request", zap.String("id", id), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}

	curr := FlagEnvironmentState{FlagKey: cr.FlagKey, Environment: cr.Environment, Enabled: payload.Enabled, RolloutPct: payload.RolloutPct}
	h.writeAudit(r.Context(), projectID, cr.FlagKey, cr.Environment, actor, "change_request_applied", &prev, &curr, ipFromRequest(r))
	// GW-1: one event value shared by both transports — see flags.go's
	// UpdateEnvironment/KillSwitch for why two independent time.Now().Unix()
	// calls would break gateway's dedup for this same logical mutation.
	applyEvent := FlagEvent{
		FlagKey: cr.FlagKey, Enabled: payload.Enabled, RolloutPct: payload.RolloutPct,
		Reason: "change_request_applied", Ts: time.Now().Unix(), Environment: cr.Environment,
	}
	publishFlagEvent(r.Context(), h.rdb, h.logger, cr.Environment, applyEvent)
	publishFlagEventToStream(r.Context(), h.rdb, h.logger, cr.Environment, applyEvent)

	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "status": "APPLIED", "approvals": len(newApprovedBy), "required_approvals": requiredApprovals,
	})
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
	rejectRow, err := sqlcgen.New(h.db).RejectChangeRequest(r.Context(), sqlcgen.RejectChangeRequestParams{
		RejectedBy:      sql.NullString{String: actor, Valid: true},
		RejectionReason: sql.NullString{String: body.Reason, Valid: true},
		UpdatedAt:       now,
		ID:              id,
		ProjectID:       sql.NullString{String: projectID, Valid: true},
	})
	flagKey, environment := rejectRow.FlagKey, rejectRow.Environment
	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"change request not found or not in PENDING state"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("reject change request", zap.String("id", id), zap.Error(err))
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}

	// SEC-3b: change_requests had no audit_log integration at all — a
	// rejected proposal left no compliance trail of who rejected what, or why.
	h.writeAudit(r.Context(), projectID, flagKey, environment, actor, "change_request_rejected",
		nil, map[string]any{"change_request_id": id, "reason": body.Reason}, ipFromRequest(r))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "REJECTED"})
}

// writeAudit writes an append-only audit entry with Merkle hash linking.
// Mirrors v1.FlagHandler.writeAudit exactly so ChangeRequestHandler can log
// without holding a reference to FlagHandler.
func (h *ChangeRequestHandler) writeAudit(ctx context.Context, projectID, flagKey, env, actor, eventType string, prev, curr any, ip string) {
	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

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
