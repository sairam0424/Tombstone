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
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
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

	rows, err := sqlcgen.New(h.db).ListSCIMUsers(ctx)
	if err != nil {
		h.logger.Error("scim list users query", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "query failed")
		return
	}

	var users []SCIMUser
	for _, row := range rows {
		u := SCIMUser{ID: row.ExternalID, UserName: row.UserID, DisplayName: row.DisplayName, Active: row.Active}
		u.Emails = []SCIMEmail{{Value: row.Email, Primary: true}}
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

	err := sqlcgen.New(h.db).UpsertSCIMUser(ctx, sqlcgen.UpsertSCIMUserParams{
		ExternalID: externalID, UserID: u.UserName, Email: email, DisplayName: displayName, Active: u.Active,
	})
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

	row, err := sqlcgen.New(h.db).GetSCIMUser(ctx, id)
	if err == sql.ErrNoRows {
		writeSCIMError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		h.logger.Error("scim get user query", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "query failed")
		return
	}

	u := SCIMUser{ID: row.ExternalID, UserName: row.UserID, DisplayName: row.DisplayName, Active: row.Active}
	u.Emails = []SCIMEmail{{Value: row.Email, Primary: true}}
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
	currentEmail, err := sqlcgen.New(h.db).GetSCIMUserEmail(ctx, id)
	if err != nil {
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

	rowsAffected, err := sqlcgen.New(h.db).UpdateSCIMUser(ctx, sqlcgen.UpdateSCIMUserParams{
		UserID: u.UserName, Email: email, DisplayName: displayName, Active: u.Active, ExternalID: id,
	})
	if err != nil {
		h.logger.Error("scim update user", zap.Error(err))
		writeSCIMError(w, http.StatusInternalServerError, "update failed")
		return
	}

	if rowsAffected == 0 {
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

	email, err := sqlcgen.New(h.db).DeprovisionSCIMUser(ctx, id)
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
	revokedProjectIDs, err := sqlcgen.New(h.db).RevokeUserRoles(ctx, userEmail)
	if err != nil {
		h.logger.Error("scim revoke user roles", zap.Error(err), zap.String("user", userEmail))
		return
	}

	// SEC-5: deprovisioning must also invalidate any JWT already issued to
	// this email — deleting user_roles above only affects FUTURE
	// authorization checks; the SSO JWT itself is stateless and stays
	// cryptographically valid for up to 24h otherwise. Written
	// unconditionally (even when revokedProjectIDs is empty) so
	// deprovisioning always forces re-authentication for this identity,
	// not just when it happened to find roles to delete.
	if err := sqlcgen.New(h.db).UpsertUserTokenWatermark(ctx, userEmail); err != nil {
		h.logger.Warn("scim: failed to set token revocation watermark", zap.Error(err), zap.String("user", userEmail))
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
	rows, err := sqlcgen.New(h.db).ListActiveFlagsByOwner(ctx, userEmail)
	if err != nil {
		h.logger.Error("scim detect orphans query", zap.Error(err), zap.String("owner", userEmail))
		return
	}

	var affected []orphanedFlag
	for _, row := range rows {
		affected = append(affected, orphanedFlag{key: row.Key, projectID: row.ProjectID})
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

		err := sqlcgen.New(h.db).CreateSCIMOrphanChangeRequest(ctx, sqlcgen.CreateSCIMOrphanChangeRequestParams{
			FlagKey: f.key, ChangePayload: payloadJSON, ProjectID: sql.NullString{String: f.projectID, Valid: true},
		})
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
