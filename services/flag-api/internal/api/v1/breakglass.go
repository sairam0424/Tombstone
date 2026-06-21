package v1

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type BreakGlassHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

func NewBreakGlassHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *BreakGlassHandler {
	return &BreakGlassHandler{db: db, rdb: rdb, logger: logger}
}

type CreateBreakGlassTokenRequest struct {
	Scope       string `json:"scope"`            // "all-flags" | "payment-flags" | "auth-flags"
	CreatedBy   string `json:"created_by"`
	ExpiresInH  int    `json:"expires_in_hours"` // default 4
	IncidentRef string `json:"incident_ref"`
}

// CreateToken handles POST /api/v1/break-glass/tokens
// Creates a pre-authorized emergency token. Requires ADMIN role.
func (h *BreakGlassHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	var req CreateBreakGlassTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Scope == "" {
		req.Scope = "all-flags"
	}
	expiresIn := req.ExpiresInH
	if expiresIn <= 0 {
		expiresIn = 4
	}

	actor := actorFromContext(r.Context())
	if req.CreatedBy == "" {
		req.CreatedBy = actor
	}

	// Generate cryptographically secure token
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	token := "bgt_" + hex.EncodeToString(raw)
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Hour)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO break_glass_tokens (token, scope, created_by, expires_at, incident_ref)
		VALUES ($1, $2, $3, $4, $5)
	`, token, req.Scope, req.CreatedBy, expiresAt, req.IncidentRef)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.writeAuditBreakGlass(r, actor, "break_glass_token_created", map[string]any{
		"scope": req.Scope, "expires_at": expiresAt.Unix(),
	})
	h.logger.Info("break-glass token created",
		zap.String("scope", req.Scope),
		zap.String("created_by", req.CreatedBy))

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"scope":      req.Scope,
		"expires_at": expiresAt.Unix(),
		"warning":    "Store this token securely. It cannot be retrieved again.",
	})
}

// UseToken handles POST /api/v1/break-glass/use
// Validates a break-glass token and records its use.
func (h *BreakGlassHandler) UseToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token      string `json:"token"`
		UsedBy     string `json:"used_by"`
		ActionDesc string `json:"action_description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	var id, scope string
	var expiresAt time.Time
	var used bool
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, scope, expires_at, used FROM break_glass_tokens WHERE token = $1
	`, req.Token).Scan(&id, &scope, &expiresAt, &used)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid break-glass token")
		return
	}
	if used {
		writeError(w, http.StatusGone, "break-glass token already used")
		return
	}
	if time.Now().After(expiresAt) {
		writeError(w, http.StatusUnauthorized, "break-glass token expired")
		return
	}

	_, _ = h.db.ExecContext(r.Context(), `
		UPDATE break_glass_tokens SET used = true, used_at = now(), used_by = $1 WHERE id = $2
	`, req.UsedBy, id)

	h.writeAuditBreakGlass(r, req.UsedBy, "break_glass_token_used", map[string]any{
		"scope": scope, "action": req.ActionDesc, "token_id": id,
	})
	h.logger.Warn("break-glass token used",
		zap.String("scope", scope),
		zap.String("used_by", req.UsedBy),
		zap.String("action", req.ActionDesc))

	writeJSON(w, http.StatusOK, map[string]any{
		"valid": true, "scope": scope, "token_id": id,
	})
}

// ListTokens handles GET /api/v1/break-glass/tokens (ADMIN only)
func (h *BreakGlassHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, scope, created_by, expires_at, used, COALESCE(used_by,''), COALESCE(incident_ref,'')
		FROM break_glass_tokens ORDER BY created_at DESC LIMIT 50
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type tokenRow struct {
		ID          string `json:"id"`
		Scope       string `json:"scope"`
		CreatedBy   string `json:"created_by"`
		ExpiresAt   int64  `json:"expires_at"`
		Used        bool   `json:"used"`
		UsedBy      string `json:"used_by,omitempty"`
		IncidentRef string `json:"incident_ref,omitempty"`
	}
	tokens := []tokenRow{}
	for rows.Next() {
		var t tokenRow
		var expiresAt time.Time
		if err := rows.Scan(&t.ID, &t.Scope, &t.CreatedBy, &expiresAt, &t.Used, &t.UsedBy, &t.IncidentRef); err != nil {
			continue
		}
		t.ExpiresAt = expiresAt.Unix()
		tokens = append(tokens, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens, "total": len(tokens)})
}

func (h *BreakGlassHandler) writeAuditBreakGlass(r *http.Request, actor, eventType string, details map[string]any) {
	// Delegate to FlagHandler's writeAudit which owns the Merkle-linked audit log.
	fh := &FlagHandler{db: h.db, rdb: h.rdb, logger: h.logger}
	fh.writeAudit(r.Context(), "", "", actor, eventType, nil, details, ipFromRequest(r))
}
