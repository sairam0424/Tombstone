package v1

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
)

// SCIMHandler implements SCIM 2.0 User provisioning endpoints.
type SCIMHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
	audit  *audit.Writer
}

// NewSCIMHandler constructs a SCIMHandler.
func NewSCIMHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger, auditW *audit.Writer) *SCIMHandler {
	return &SCIMHandler{db: db, rdb: rdb, logger: logger, audit: auditW}
}

// SCIMEmail represents a SCIM email object.
type SCIMEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary"`
}

// SCIMUser is the SCIM 2.0 User resource representation.
type SCIMUser struct {
	ID          string      `json:"id,omitempty"`
	UserName    string      `json:"userName"`
	DisplayName string      `json:"displayName,omitempty"`
	Active      bool        `json:"active"`
	Emails      []SCIMEmail `json:"emails,omitempty"`
}

// scimListResponse wraps a slice of SCIMUser in the SCIM ListResponse envelope.
type scimListResponse struct {
	Schemas      []string   `json:"schemas"`
	TotalResults int        `json:"totalResults"`
	StartIndex   int        `json:"startIndex"`
	ItemsPerPage int        `json:"itemsPerPage"`
	Resources    []SCIMUser `json:"Resources"`
}

// primaryEmail extracts the primary email value from a SCIMUser, falling back
// to UserName when no primary email is set.
func primaryEmail(u SCIMUser) string {
	for _, e := range u.Emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(u.Emails) > 0 {
		return u.Emails[0].Value
	}
	return u.UserName
}

// ListUsers handles GET /scim/v2/Users
// Returns all provisioned users in SCIM ListResponse format.
func (h *SCIMHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := h.db.QueryContext(ctx, `
		SELECT external_id, user_id, email, display_name, active
		FROM scim_users
		ORDER BY synced_at DESC
	`)
	if err != nil {
		h.logger.Error("scim list users query", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	var users []SCIMUser
	for rows.Next() {
		var u SCIMUser
		var email string
		if err := rows.Scan(&u.ID, &u.UserName, &email, &u.DisplayName, &u.Active); err != nil {
			h.logger.Error("scim list users scan", zap.Error(err))
			writeSCIMError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		u.Emails = []SCIMEmail{{Value: email, Primary: true}}
		users = append(users, u)
	}
	if users == nil {
		users = []SCIMUser{}
	}

	writeJSON(w, http.StatusOK, scimListResponse{
		Schemas:      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		TotalResults: len(users),
		StartIndex:   1,
		ItemsPerPage: len(users),
		Resources:    users,
	})
}

// ProvisionUser handles POST /scim/v2/Users
// Upserts a user into scim_users (INSERT ON CONFLICT UPDATE).
func (h *SCIMHandler) ProvisionUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var u SCIMUser
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if u.UserName == "" {
		writeSCIMError(w, http.StatusBadRequest, "userName is required")
		return
	}

	email := primaryEmail(u)
	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.UserName
	}

	// external_id is provided by the IdP; fall back to userName when absent.
	externalID := u.ID
	if externalID == "" {
		externalID = u.UserName
	}

	_, err := h.db.ExecContext(ctx, `
		INSERT INTO scim_users (external_id, user_id, email, display_name, active, synced_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (external_id) DO UPDATE
		    SET user_id      = EXCLUDED.user_id,
		        email        = EXCLUDED.email,
		        display_name = EXCLUDED.display_name,
		        active       = EXCLUDED.active,
		        synced_at    = now()
	`, externalID, u.UserName, email, displayName, u.Active)
	if err != nil {
		h.logger.Error("scim provision user upsert", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "upsert failed")
		return
	}

	u.ID = externalID
	u.Emails = []SCIMEmail{{Value: email, Primary: true}}
	writeJSON(w, http.StatusCreated, u)
}

// GetUser handles GET /scim/v2/Users/{id}
// Returns a single SCIM user by external_id. Returns 404 if not found.
func (h *SCIMHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	var u SCIMUser
	var email string
	err := h.db.QueryRowContext(ctx, `
		SELECT external_id, user_id, email, display_name, active
		FROM scim_users
		WHERE external_id = $1
	`, id).Scan(&u.ID, &u.UserName, &email, &u.DisplayName, &u.Active)
	if err == sql.ErrNoRows {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		h.logger.Error("scim get user query", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "query failed")
		return
	}

	u.Emails = []SCIMEmail{{Value: email, Primary: true}}
	writeJSON(w, http.StatusOK, u)
}

// UpdateUser handles PUT /scim/v2/Users/{id}
// Updates user fields; if active becomes false triggers orphan detection.
func (h *SCIMHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	var u SCIMUser
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// SEC-5: the identity used to revoke a deactivated user's roles below
	// must be the email ALREADY on file for this external_id — not
	// primaryEmail(u), which is derived from this PUT's own body and falls
	// back to u.UserName whenever the caller's "emails" array is empty (a
	// legitimate deactivation-only payload some IdPs send). Using the
	// caller-supplied value would let a partial/malformed body silently
	// target the wrong (or nonexistent) user_roles.user_id, no-opping the
	// revocation with no error.
	var currentEmail string
	if err := h.db.QueryRowContext(ctx, `SELECT email FROM scim_users WHERE external_id = $1`, id).Scan(&currentEmail); err != nil {
		if err == sql.ErrNoRows {
			writeSCIMError(w, http.StatusNotFound, "user not found")
			return
		}
		h.logger.Error("scim update user lookup", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	email := primaryEmail(u)
	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.UserName
	}

	result, err := h.db.ExecContext(ctx, `
		UPDATE scim_users
		SET user_id      = $1,
		    email        = $2,
		    display_name = $3,
		    active       = $4,
		    synced_at    = now()
		WHERE external_id = $5
	`, u.UserName, email, displayName, u.Active, id)
	if err != nil {
		h.logger.Error("scim update user", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "update failed")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}

	if !u.Active {
		h.detectOrphans(ctx, currentEmail)
		h.revokeUserRoles(ctx, currentEmail)
	}

	u.ID = id
	u.Emails = []SCIMEmail{{Value: email, Primary: true}}
	writeJSON(w, http.StatusOK, u)
}

// DeprovisionUser handles DELETE /scim/v2/Users/{id}
// Sets active=false and triggers orphan detection. Returns 204.
func (h *SCIMHandler) DeprovisionUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	var email string
	err := h.db.QueryRowContext(ctx, `
		UPDATE scim_users
		SET active = false, synced_at = now()
		WHERE external_id = $1
		RETURNING email
	`, id).Scan(&email)
	if err == sql.ErrNoRows {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		h.logger.Error("scim deprovision user", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "deprovision failed")
		return
	}

	h.detectOrphans(ctx, email)
	h.revokeUserRoles(ctx, email)
	w.WriteHeader(http.StatusNoContent)
}

// revokeUserRoles deletes every project role a deactivated SCIM user held.
//
// SEC-5: deprovisioning (via DELETE, or PUT with active=false — many IdPs
// use the latter for what they call a "deprovision") only ever flipped
// scim_users.active, a bookkeeping column with no authorization effect of
// its own. user_roles (what rbac.go's resolveRole actually reads, keyed by
// the same email string as this handler's user_id) was never touched, so a
// deprovisioned user's RBAC grants across every project persisted
// indefinitely — deprovisioning revoked nothing.
func (h *SCIMHandler) revokeUserRoles(ctx context.Context, userEmail string) {
	// Case-insensitive: user_roles.user_id is a plain TEXT column populated
	// out-of-band (there is no API that ever inserts into it), so the case
	// an admin typed when granting a role has no guaranteed relationship to
	// the case an IdP's SCIM feed asserts later. Matching case-insensitively
	// only ever revokes MORE broadly, never less — the safe direction to err
	// in for a deprovisioning action, and it closes the silent no-op this
	// would otherwise produce on a case mismatch.
	rows, err := h.db.QueryContext(ctx, `DELETE FROM user_roles WHERE lower(user_id) = lower($1) RETURNING project_id`, userEmail)
	if err != nil {
		h.logger.Error("scim revoke user roles", zap.Error(err), zap.String("user", userEmail))
		return
	}
	defer func() { _ = rows.Close() }()

	var revokedProjectIDs []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			h.logger.Error("scim revoke user roles scan", zap.Error(err))
			continue
		}
		revokedProjectIDs = append(revokedProjectIDs, projectID)
	}
	if len(revokedProjectIDs) == 0 {
		return
	}

	h.logger.Warn("scim revoked user roles on deprovision",
		zap.String("user", userEmail), zap.Strings("project_ids", revokedProjectIDs))

	if h.audit == nil {
		h.logger.Warn("scim role-revocation audit write skipped — no audit writer configured")
		return
	}
	// One entry PER revoked project, each carrying its own ProjectID — not
	// one combined entry with the project list buried in NewState. Every
	// exposed read path (ListAuditLog, VerifyChain, ExportAuditLog) filters
	// on an exact project_id match with no NULL-fallback; a single
	// unscoped entry would be permanently unreachable through any of them,
	// the same TEN-1a-2 bug class detectOrphans below was fixed to avoid.
	detailsJSON, _ := json.Marshal(map[string]any{"user": userEmail})
	for _, projectID := range revokedProjectIDs {
		if _, _, err := h.audit.Append(ctx, audit.Entry{
			Actor:     "system-scim",
			EventType: "user_roles_revoked",
			NewState:  detailsJSON,
			ProjectID: projectID,
		}); err != nil {
			h.logger.Warn("scim role-revocation audit write failed", zap.Error(err), zap.String("project_id", projectID))
		}
	}
}

// orphanedFlag pairs a flag key with its owning project — needed so the
// audit entry below (TEN-1a-2) attributes to the RIGHT project rather than
// leaving it unattributed.
type orphanedFlag struct {
	key       string
	projectID string
}

// detectOrphans finds ACTIVE flags owned by the given email, creates PENDING
// change_requests for each, appends an audit_log entry, and publishes a Redis
// event so connected gateways are notified.
func (h *SCIMHandler) detectOrphans(ctx context.Context, userEmail string) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT key, project_id FROM flags
		WHERE owner_id = $1 AND state = 'ACTIVE'
	`, userEmail)
	if err != nil {
		h.logger.Error("scim detect orphans query", zap.Error(err), zap.String("owner", userEmail))
		return
	}
	defer func() { _ = rows.Close() }()

	var affected []orphanedFlag
	for rows.Next() {
		var f orphanedFlag
		if err := rows.Scan(&f.key, &f.projectID); err != nil {
			h.logger.Error("scim detect orphans scan", zap.Error(err))
			continue
		}
		affected = append(affected, f)
	}

	if len(affected) == 0 {
		return
	}

	affectedKeys := make([]string, len(affected))
	for i, f := range affected {
		affectedKeys[i] = f.key
	}

	for _, f := range affected {
		payload := map[string]string{
			"reason":      "owner_deprovisioned",
			"owner_email": userEmail,
		}
		payloadJSON, _ := json.Marshal(payload)

		_, err := h.db.ExecContext(ctx, `
			INSERT INTO change_requests
			    (flag_key, environment, requested_by, status, change_payload, project_id)
			VALUES ($1, 'production', 'system', 'PENDING', $2, $3)
		`, f.key, payloadJSON, f.projectID)
		if err != nil {
			h.logger.Error("scim create change_request", zap.Error(err),
				zap.String("flag_key", f.key))
		}

		// TEN-1a-2: this previously bypassed audit.Writer.Append entirely via
		// a raw INSERT with no project_id, no prev_hash/entry_hash, and no
		// HMAC — the row neither joined the hash chain nor was attributable
		// to any project, so it went permanently invisible to every
		// project-scoped GET /api/v1/audit view once that filter shipped.
		if h.audit != nil {
			if _, _, err := h.audit.Append(ctx, audit.Entry{
				FlagKey:     f.key,
				Environment: "production",
				Actor:       "system",
				EventType:   "user_deprovisioned",
				NewState:    payloadJSON,
				ProjectID:   f.projectID,
			}); err != nil {
				h.logger.Error("scim audit log write", zap.Error(err), zap.String("flag_key", f.key))
			}
		} else {
			h.logger.Warn("scim audit log write skipped — no audit writer configured",
				zap.String("flag_key", f.key))
		}

		event := map[string]any{
			"type":     "flag_event",
			"flag_key": f.key,
			"reason":   "owner_deprovisioned",
			"owner":    userEmail,
			"ts":       time.Now().UTC().Unix(),
		}
		eventJSON, _ := json.Marshal(event)
		if pubErr := h.rdb.Publish(ctx, "stream:production:updates", string(eventJSON)).Err(); pubErr != nil {
			h.logger.Warn("scim redis publish", zap.Error(pubErr), zap.String("flag_key", f.key))
		}
	}

	h.logger.Info("scim orphan flags detected",
		zap.String("owner", userEmail),
		zap.Int("count", len(affected)),
		zap.Strings("flag_keys", affectedKeys),
	)
}

// SCIMAuthMiddleware returns HTTP middleware that validates Bearer token auth
// for SCIM endpoints.
//
// SEC-5: this used to allow EVERY request through unauthenticated ("dev
// mode") whenever SCIM_TOKEN was unset — and SCIM_TOKEN is set nowhere in
// .env.example, ci.yml, or the northflank/helm deployment configs, so every
// currently-documented deployment ran with SCIM wide open: anyone could list,
// provision, update, or DEPROVISION users with no credential at all. It now
// fails closed instead — SCIM is unavailable, not unauthenticated, until a
// token is actually configured.
func SCIMAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				writeSCIMError(w, http.StatusServiceUnavailable, "SCIM is not configured (SCIM_TOKEN is unset)")
				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeSCIMError(w, http.StatusUnauthorized, "missing Bearer token")
				return
			}

			provided := strings.TrimPrefix(authHeader, "Bearer ")
			// Constant-time: a length/byte-position-dependent timing
			// difference on a bearer credential is a real side channel, the
			// same reasoning already applied to every other token comparison
			// in this codebase (internal/secrets' TokenHasher.Equal, AuditKey.Equal).
			if !hmac.Equal([]byte(provided), []byte(token)) {
				writeSCIMError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// writeSCIMError writes a minimal SCIM error response.
func writeSCIMError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"status":  status,
		"detail":  detail,
	})
}
