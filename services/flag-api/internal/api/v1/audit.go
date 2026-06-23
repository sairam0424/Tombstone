package v1

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
)

type AuditHandler struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewAuditHandler(db *sql.DB, logger *zap.Logger) *AuditHandler {
	return &AuditHandler{db: db, logger: logger}
}

type AuditEntry struct {
	ID           string  `json:"id"`
	FlagKey      string  `json:"flag_key"`
	Environment  string  `json:"environment"`
	Actor        string  `json:"actor"`
	EventType    string  `json:"event_type"`
	PrevState    any     `json:"prev_state"`
	NewState     any     `json:"new_state"`
	IPAddress    string  `json:"ip_address"`
	PrevHash     string  `json:"prev_hash"`
	CreatedAt    int64   `json:"created_at"`
	RekorLogID   string  `json:"rekor_log_id,omitempty"`
	RekorLogIndex *int64 `json:"rekor_log_index,omitempty"`
}

// ListAuditLog handles GET /api/v1/audit
// Supports filters: flag_key, environment, from (unix ts), to (unix ts), limit
func (h *AuditHandler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
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
		ORDER BY created_at DESC
		LIMIT $5
	`, flagKey, env, fromTs, toTs, limit)
	if err != nil {
		h.logger.Error("audit log query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

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
