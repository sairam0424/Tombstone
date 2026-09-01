package v1

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tombstone/flag-api/internal/audit"
	"go.uber.org/zap"
)

type AuditHandler struct {
	db     *sql.DB
	logger *zap.Logger
	audit  *audit.Writer
}

func NewAuditHandler(db *sql.DB, logger *zap.Logger, auditW *audit.Writer) *AuditHandler {
	return &AuditHandler{db: db, logger: logger, audit: auditW}
}

// VerifyChain handles GET /api/v1/audit/verify.
//
// AUD-1: this endpoint recomputes every keyed hash in the audit log and checks
// each link. Previously no such endpoint existed and the compliance evidence
// endpoint simply asserted merkle_chain_integrity=true unconditionally.
func (h *AuditHandler) VerifyChain(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil || !h.audit.HasKey() {
		writeError(w, http.StatusServiceUnavailable,
			"audit chain verification unavailable — AUDIT_HMAC_KEY is not configured")
		return
	}
	// TEN-1a-2: scoped to the caller's own project — an unscoped verify would
	// both leak other projects' flag keys via VerifyFailure and let one
	// project's tampering (or an unrelated legacy/legacy-chain artifact)
	// produce a false-tampering report for a caller who never touched it.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	report, err := h.audit.Verify(r.Context(), projectID)
	if err != nil {
		h.logger.Error("audit verify failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "verification failed")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

type AuditEntry struct {
	ID            string `json:"id"`
	FlagKey       string `json:"flag_key"`
	Environment   string `json:"environment"`
	Actor         string `json:"actor"`
	EventType     string `json:"event_type"`
	PrevState     any    `json:"prev_state"`
	NewState      any    `json:"new_state"`
	IPAddress     string `json:"ip_address"`
	PrevHash      string `json:"prev_hash"`
	CreatedAt     int64  `json:"created_at"`
	RekorLogID    string `json:"rekor_log_id,omitempty"`
	RekorLogIndex *int64 `json:"rekor_log_index,omitempty"`
}

// ListAuditLog handles GET /api/v1/audit
// Supports filters: flag_key, environment, from (unix ts), to (unix ts), limit
func (h *AuditHandler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	// TEN-1a-2: this previously had no project filter at all, so any VIEWER
	// (human or read-only service token) could read every project's full
	// audit history — including prev_state/new_state flag snapshots — by
	// calling this endpoint with no flag_key filter.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	flagKey := q.Get("flag_key")
	env := q.Get("environment")

	limit := 50
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	fromTs := int64(0)
	toTs := time.Now().Unix()
	if f := q.Get("from"); f != "" {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil {
			fromTs = n
		}
	}
	if t := q.Get("to"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			toTs = n
		}
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, COALESCE(flag_key,''), COALESCE(environment,''), actor, event_type,
		       COALESCE(prev_state::text,'null'), COALESCE(new_state::text,'null'),
		       COALESCE(ip_address,''), COALESCE(prev_hash,''),
		       EXTRACT(EPOCH FROM created_at)::bigint,
		       COALESCE(rekor_log_id,''), rekor_log_index
		FROM audit_log
		WHERE ($1='' OR flag_key=$1)
		  AND ($2='' OR environment=$2)
		  AND created_at >= to_timestamp($3)
		  AND created_at <= to_timestamp($4)
		  AND project_id = $6
		ORDER BY created_at DESC
		LIMIT $5
	`, flagKey, env, fromTs, toTs, limit, projectID)
	if err != nil {
		h.logger.Error("audit log query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	entries := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var prevRaw, newRaw string
		if err := rows.Scan(&e.ID, &e.FlagKey, &e.Environment, &e.Actor, &e.EventType,
			&prevRaw, &newRaw, &e.IPAddress, &e.PrevHash, &e.CreatedAt,
			&e.RekorLogID, &e.RekorLogIndex); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		e.PrevState = json.RawMessage(prevRaw)
		e.NewState = json.RawMessage(newRaw)
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": len(entries)})
}
