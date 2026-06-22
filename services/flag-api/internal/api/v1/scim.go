package v1

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// SCIMHandler implements SCIM 2.0 User provisioning endpoints.
type SCIMHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

// NewSCIMHandler constructs a SCIMHandler.
func NewSCIMHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *SCIMHandler {
	return &SCIMHandler{db: db, rdb: rdb, logger: logger}
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
	defer rows.Close()

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
		h.detectOrphans(ctx, email)
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
	w.WriteHeader(http.StatusNoContent)
}

// detectOrphans finds ACTIVE flags owned by the given email, creates PENDING
// change_requests for each, appends an audit_log entry, and publishes a Redis
// event so connected gateways are notified.
func (h *SCIMHandler) detectOrphans(ctx context.Context, userEmail string) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT key FROM flags
		WHERE owner_id = $1 AND state = 'ACTIVE'
	`, userEmail)
	if err != nil {
		h.logger.Error("scim detect orphans query", zap.Error(err), zap.String("owner", userEmail))
		return
	}
	defer rows.Close()

	var affectedKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			h.logger.Error("scim detect orphans scan", zap.Error(err))
			continue
		}
		affectedKeys = append(affectedKeys, key)
	}

	if len(affectedKeys) == 0 {
		return
	}

	for _, flagKey := range affectedKeys {
		payload := map[string]string{
			"reason":     "owner_deprovisioned",
			"owner_email": userEmail,
		}
		payloadJSON, _ := json.Marshal(payload)

		_, err := h.db.ExecContext(ctx, `
			INSERT INTO change_requests
			    (flag_key, environment, requested_by, status, change_payload)
			VALUES ($1, 'production', 'system', 'PENDING', $2)
		`, flagKey, payloadJSON)
		if err != nil {
			h.logger.Error("scim create change_request", zap.Error(err),
				zap.String("flag_key", flagKey))
		}

		_, err = h.db.ExecContext(ctx, `
			INSERT INTO audit_log (flag_key, environment, actor, event_type, new_state, ip_address)
			VALUES ($1, 'production', 'system', 'user_deprovisioned', $2, '')
		`, flagKey, payloadJSON)
		if err != nil {
			h.logger.Error("scim audit log insert", zap.Error(err),
				zap.String("flag_key", flagKey))
		}

		event := map[string]any{
			"type":     "flag_event",
			"flag_key": flagKey,
			"reason":   "owner_deprovisioned",
			"owner":    userEmail,
			"ts":       time.Now().UTC().Unix(),
		}
		eventJSON, _ := json.Marshal(event)
		if pubErr := h.rdb.Publish(ctx, "stream:production:updates", string(eventJSON)).Err(); pubErr != nil {
			h.logger.Warn("scim redis publish", zap.Error(pubErr), zap.String("flag_key", flagKey))
		}
	}

	h.logger.Info("scim orphan flags detected",
		zap.String("owner", userEmail),
		zap.Int("count", len(affectedKeys)),
		zap.Strings("flag_keys", affectedKeys),
	)
}

// SCIMAuthMiddleware returns HTTP middleware that validates Bearer token auth
// for SCIM endpoints. FAILS CLOSED: if token is empty the middleware rejects
// all requests with 503 rather than allowing unauthenticated access.
// Set SCIM_TOKEN env var before registering these routes.
func SCIMAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				// No token configured — fail closed. SCIM endpoints are
				// inaccessible until SCIM_TOKEN is set in the environment.
				writeSCIMError(w, http.StatusServiceUnavailable,
					"SCIM not configured: set SCIM_TOKEN environment variable")
				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeSCIMError(w, http.StatusUnauthorized, "missing Bearer token")
				return
			}

			provided := strings.TrimPrefix(authHeader, "Bearer ")
			// Use constant-time comparison to prevent timing side-channel attacks.
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
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
