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
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
	"github.com/tombstone/flag-api/internal/secrets"
)

// Sentinel errors from consumeBreakGlassTokenTx/consumeBreakGlassToken —
// callers map these to the appropriate HTTP status (a used token is 410
// Gone, not 401, since it once existed and worked; invalid/expired/
// wrong-project are all 401/403-class caller errors).
var (
	errBreakGlassTokenInvalid      = errors.New("invalid break-glass token")
	errBreakGlassTokenUsed         = errors.New("break-glass token already used")
	errBreakGlassTokenExpired      = errors.New("break-glass token expired")
	errBreakGlassTokenWrongProject = errors.New("break-glass token is not valid for this project")
)

// consumeBreakGlassTokenTx atomically marks a break-glass token used within
// tx and returns its scope and id — shared by BreakGlassHandler.UseToken
// (via consumeBreakGlassToken below) and FlagHandler's require_approval
// gate (SEC-3b part 2), which lets a valid token bypass the gate during an
// incident.
//
// A single UPDATE ... WHERE used = false ... RETURNING, not a separate
// SELECT-then-UPDATE: Postgres's row-level locking under an UPDATE makes
// the read-and-flip atomic, so two concurrent callers racing the same
// token can never both succeed — exactly one UPDATE's WHERE clause matches;
// the loser gets 0 rows back. The prior SELECT-then-UPDATE version of this
// function had a real TOCTOU window where both could observe used=false
// before either committed.
//
// projectID, when non-empty, requires the token to have been created for
// that project (or before project scoping existed, i.e. project_id IS
// NULL) — otherwise an admin-issued token from one project could bypass a
// completely unrelated project's require_approval policy. Pass "" (as
// consumeBreakGlassToken does, for the standalone UseToken ceremony, which
// authorizes nothing on its own) to skip this check.
func consumeBreakGlassTokenTx(ctx context.Context, tx *sql.Tx, hasher *secrets.TokenHasher, token, usedBy, projectID string) (scope, tokenID string, err error) {
	if hasher == nil {
		return "", "", fmt.Errorf("token hashing is not configured")
	}

	tokenHash := hasher.Hash(token)

	// ProjectID is NULL (Valid: false), not "", for "no project scope
	// requested" — see queries/breakglass.sql's ConsumeBreakGlassToken doc
	// comment for why the query casts this parameter (not the column) to
	// ::uuid: a same-project caller whose X-Project-Id header casing merely
	// differs from the canonical stored value must still match.
	row, err := sqlcgen.New(tx).ConsumeBreakGlassToken(ctx, sqlcgen.ConsumeBreakGlassTokenParams{
		UsedBy:    sql.NullString{String: usedBy, Valid: true},
		TokenHash: sql.NullString{String: tokenHash, Valid: true},
		ProjectID: sql.NullString{String: projectID, Valid: projectID != ""},
	})
	if err == nil {
		return row.Scope, row.ID, nil
	}
	if err != sql.ErrNoRows {
		return "", "", fmt.Errorf("consume break-glass token: %w", err)
	}

	// The atomic UPDATE matched nothing — a read-only follow-up to explain
	// why. This SELECT cannot itself be raced into a bypass: it never sets
	// used, so it grants nothing regardless of what it observes.
	diag, selErr := sqlcgen.New(tx).GetBreakGlassTokenDiagnostics(ctx, sql.NullString{String: tokenHash, Valid: true})
	used, expiresAt, tokenProjectID := diag.Used, diag.ExpiresAt, diag.ProjectID
	switch {
	case selErr == sql.ErrNoRows:
		return "", "", errBreakGlassTokenInvalid
	case selErr != nil:
		return "", "", fmt.Errorf("consume break-glass token: %w", selErr)
	case used:
		return "", "", errBreakGlassTokenUsed
	case time.Now().After(expiresAt):
		return "", "", errBreakGlassTokenExpired
	case projectID != "" && tokenProjectID.Valid && tokenProjectID.String != projectID:
		return "", "", errBreakGlassTokenWrongProject
	default:
		// The row is unused, unexpired, and (if projectID was specified)
		// project-matched by this read — yet the atomic UPDATE above still
		// matched zero rows. The only way that happens is a concurrent
		// request consuming the token in the narrow window between the
		// failed UPDATE and this read-only diagnostic SELECT; report it the
		// same way a straightforwardly-already-used token would be.
		return "", "", errBreakGlassTokenUsed
	}
}

// consumeBreakGlassToken is consumeBreakGlassTokenTx wrapped in its own
// transaction (for callers, like UseToken, that have no other transaction
// to join) plus the audit write consumption always produces. Never
// project-scoped: UseToken is a standalone "burn my emergency token"
// ceremony that authorizes no write of its own, so there is no project
// boundary for it to violate.
func consumeBreakGlassToken(ctx context.Context, db *sql.DB, hasher *secrets.TokenHasher, auditW *audit.Writer, logger *zap.Logger, ip, token, usedBy, actionDesc string) (scope, tokenID string, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	scope, tokenID, err = consumeBreakGlassTokenTx(ctx, tx, hasher, token, usedBy, "")
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit: %w", err)
	}

	writeBreakGlassAuditEntry(ctx, auditW, logger, ip, usedBy, tokenID, scope, actionDesc, "", "", "")
	return scope, tokenID, nil
}

// writeBreakGlassAuditEntry records that a break-glass token was consumed,
// regardless of which caller consumed it — an auditor filtering audit_log
// for "break_glass_token_used" sees every use, whether via the standalone
// UseToken ceremony or the require_approval gate's bypass path.
//
// flagKey/environment/projectID are "" for the standalone ceremony (a
// break-glass token isn't inherently tied to one flag), but populated when
// the gate calls this from within a specific flag write — otherwise an
// auditor querying "everything that happened to this flag" would never see
// the break-glass entry alongside the flag_environment_updated_via_breakglass
// one it accompanies, only find it by knowing to search event_type instead.
func writeBreakGlassAuditEntry(ctx context.Context, auditW *audit.Writer, logger *zap.Logger, ip, actor, tokenID, scope, actionDesc, flagKey, environment, projectID string) {
	if auditW == nil {
		logger.Warn("break-glass audit write skipped — no audit writer configured")
		return
	}
	detailsJSON, _ := json.Marshal(map[string]any{"scope": scope, "action": actionDesc, "token_id": tokenID})
	if _, _, err := auditW.Append(ctx, audit.Entry{
		FlagKey:     flagKey,
		Environment: environment,
		Actor:       actor,
		EventType:   "break_glass_token_used",
		NewState:    detailsJSON,
		IPAddress:   ip,
		ProjectID:   projectID,
	}); err != nil {
		// Best-effort, matching every other audit write in this codebase — a
		// failed audit write must not undo an already-consumed token or block
		// the emergency action it just authorized.
		logger.Warn("break-glass audit write failed", zap.Error(err))
	}
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
	// SEC-3b part 2: the token is scoped to the caller's own project (ADMIN
	// is itself a per-project role) — otherwise a token minted for one
	// project's incident could bypass a completely unrelated project's
	// require_approval policy.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

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
	err := sqlcgen.New(h.db).CreateBreakGlassToken(r.Context(), sqlcgen.CreateBreakGlassTokenParams{
		TokenHash:   sql.NullString{String: h.hasher.Hash(token), Valid: true},
		Scope:       req.Scope,
		CreatedBy:   req.CreatedBy,
		ExpiresAt:   expiresAt,
		IncidentRef: sql.NullString{String: req.IncidentRef, Valid: true},
		ProjectID:   sql.NullString{String: projectID, Valid: true},
	})
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
	// SEC-3b part 2: scoped to the caller's project, plus legacy tokens that
	// predate project scoping (project_id IS NULL) — otherwise an ADMIN of
	// one project could see incident_ref/created_by metadata for every other
	// project's emergency tokens too.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	rows, err := sqlcgen.New(h.db).ListBreakGlassTokens(r.Context(), sql.NullString{String: projectID, Valid: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
	for _, r := range rows {
		tokens = append(tokens, tokenRow{
			ID:          r.ID,
			Scope:       r.Scope,
			CreatedBy:   r.CreatedBy,
			ExpiresAt:   r.ExpiresAt.Unix(),
			Used:        r.Used,
			UsedBy:      r.UsedBy,
			IncidentRef: r.IncidentRef,
		})
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
