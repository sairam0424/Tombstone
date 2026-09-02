package v1

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/secrets"
)

// Sentinel errors from consumeBreakGlassToken — callers map these to the
// appropriate HTTP status (a used token is 410 Gone, not 401, since it once
// existed and worked; invalid/expired are both 401).
var (
	errBreakGlassTokenInvalid = errors.New("invalid break-glass token")
	errBreakGlassTokenUsed    = errors.New("break-glass token already used")
	errBreakGlassTokenExpired = errors.New("break-glass token expired")
)

// consumeBreakGlassToken validates a break-glass token, marks it used, and
// writes an audit entry — shared by BreakGlassHandler.UseToken (the
// standalone "burn my emergency token" ceremony) and FlagHandler's
// require_approval gate (SEC-3b part 2), which lets a valid token bypass
// the gate during an incident. A used token cannot bypass anything twice.
func consumeBreakGlassToken(ctx context.Context, db *sql.DB, hasher *secrets.TokenHasher, auditW *audit.Writer, logger *zap.Logger, ip, token, usedBy, actionDesc string) (scope, tokenID string, err error) {
	if hasher == nil {
		return "", "", fmt.Errorf("token hashing is not configured")
	}

	var id, tokenScope string
	var expiresAt time.Time
	var used bool
	if err := db.QueryRowContext(ctx, `
		SELECT id, scope, expires_at, used FROM break_glass_tokens WHERE token_hash = $1
	`, hasher.Hash(token)).Scan(&id, &tokenScope, &expiresAt, &used); err != nil {
		return "", "", errBreakGlassTokenInvalid
	}
	if used {
		return "", "", errBreakGlassTokenUsed
	}
	if time.Now().After(expiresAt) {
		return "", "", errBreakGlassTokenExpired
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE break_glass_tokens SET used = true, used_at = now(), used_by = $1 WHERE id = $2
	`, usedBy, id); err != nil {
		return "", "", fmt.Errorf("mark token used: %w", err)
	}

	if auditW == nil {
		logger.Warn("break-glass audit write skipped — no audit writer configured")
	} else {
		detailsJSON, _ := json.Marshal(map[string]any{"scope": tokenScope, "action": actionDesc, "token_id": id})
		if _, _, err := auditW.Append(ctx, audit.Entry{
			Actor:     usedBy,
			EventType: "break_glass_token_used",
			NewState:  detailsJSON,
			IPAddress: ip,
		}); err != nil {
			// Best-effort, matching every other audit write in this codebase —
			// a failed audit write must not undo an already-consumed token or
			// block the emergency action it just authorized.
			logger.Warn("break-glass audit write failed", zap.Error(err))
		}
	}

	return tokenScope, id, nil
}

type BreakGlassHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
	// hasher stores/looks up break-glass tokens as keyed hashes (SEC-4). The
	// plaintext is returned to the creator exactly once and never persisted.
	hasher *secrets.TokenHasher
	audit  *audit.Writer
}

func NewBreakGlassHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger, hasher *secrets.TokenHasher, auditW *audit.Writer) *BreakGlassHandler {
	return &BreakGlassHandler{db: db, rdb: rdb, logger: logger, hasher: hasher, audit: auditW}
}

type CreateBreakGlassTokenRequest struct {
	Scope       string `json:"scope"` // "all-flags" | "payment-flags" | "auth-flags"
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

	// SEC-4: persist ONLY the keyed hash. The plaintext below is returned to the
	// caller once and then discarded, which is what makes the response's
	// "cannot be retrieved again" promise actually true.
	if h.hasher == nil {
		writeError(w, http.StatusInternalServerError, "token hashing is not configured")
		return
	}
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO break_glass_tokens (token_hash, scope, created_by, expires_at, incident_ref)
		VALUES ($1, $2, $3, $4, $5)
	`, h.hasher.Hash(token), req.Scope, req.CreatedBy, expiresAt, req.IncidentRef)
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

	scope, tokenID, err := consumeBreakGlassToken(r.Context(), h.db, h.hasher, h.audit, h.logger,
		ipFromRequest(r), req.Token, req.UsedBy, req.ActionDesc)
	if err != nil {
		switch {
		case errors.Is(err, errBreakGlassTokenUsed):
			writeError(w, http.StatusGone, err.Error())
		case errors.Is(err, errBreakGlassTokenInvalid), errors.Is(err, errBreakGlassTokenExpired):
			writeError(w, http.StatusUnauthorized, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	h.logger.Warn("break-glass token used",
		zap.String("scope", scope),
		zap.String("used_by", req.UsedBy),
		zap.String("action", req.ActionDesc))

	writeJSON(w, http.StatusOK, map[string]any{
		"valid": true, "scope": scope, "token_id": tokenID,
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
	defer func() { _ = rows.Close() }()

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
	// AUD-1: append via the shared writer. This previously built a throwaway
	// FlagHandler to borrow its writeAudit, which meant break-glass events were
	// silently dropped whenever that handler's dependencies differed.
	if h.audit == nil {
		h.logger.Warn("break-glass audit write skipped — no audit writer configured")
		return
	}
	detailsJSON, _ := json.Marshal(details)
	if _, _, err := h.audit.Append(r.Context(), audit.Entry{
		Actor:     actor,
		EventType: eventType,
		NewState:  detailsJSON,
		IPAddress: ipFromRequest(r),
	}); err != nil {
		h.logger.Warn("break-glass audit write failed", zap.Error(err))
	}
}
