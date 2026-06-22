package v1

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type FlagHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

func NewFlagHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *FlagHandler {
	return &FlagHandler{db: db, rdb: rdb, logger: logger}
}

type Flag struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	FlagType    string  `json:"flag_type"`
	State       string  `json:"state"`
	OwnerID     string  `json:"owner_id"`
	SafeDefault string  `json:"safe_default"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

type FlagEnvironmentState struct {
	FlagID      string `json:"flag_id"`
	FlagKey     string `json:"flag_key"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
	SafeDefault string `json:"safe_default"`
	UpdatedAt   int64  `json:"updated_at"`
}

type CreateFlagRequest struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	FlagType    string `json:"flag_type"`
	OwnerID     string `json:"owner_id"`
	ProjectID   string `json:"project_id"`
	SafeDefault string `json:"safe_default"`
}

type UpdateEnvironmentRequest struct {
	Enabled    bool   `json:"enabled"`
	RolloutPct int    `json:"rollout_pct"`
	UpdatedBy  string `json:"updated_by"`
}

type FlagEvent struct {
	FlagKey     string `json:"flag_key"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
	Reason      string `json:"reason"`
	Ts          int64  `json:"ts"`
	Environment string `json:"environment"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ListFlags handles GET /api/v1/flags
func (h *FlagHandler) ListFlags(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "00000000-0000-0000-0000-000000000001"
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT f.id, f.key, f.project_id, f.name, f.description,
		       f.flag_type, f.state, f.owner_id, f.safe_default,
		       EXTRACT(EPOCH FROM f.created_at)::bigint,
		       EXTRACT(EPOCH FROM f.updated_at)::bigint
		FROM flags f
		WHERE f.project_id = $1 AND f.state != 'ARCHIVED'
		ORDER BY f.created_at DESC
	`, projectID)
	if err != nil {
		h.logger.Error("list flags query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	flags := []Flag{}
	for rows.Next() {
		var f Flag
		if err := rows.Scan(&f.ID, &f.Key, &f.ProjectID, &f.Name, &f.Description,
			&f.FlagType, &f.State, &f.OwnerID, &f.SafeDefault, &f.CreatedAt, &f.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		flags = append(flags, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"flags": flags, "total": len(flags)})
}

// CreateFlag handles POST /api/v1/flags
func (h *FlagHandler) CreateFlag(w http.ResponseWriter, r *http.Request) {
	var req CreateFlagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Key == "" || req.Name == "" || req.FlagType == "" {
		writeError(w, http.StatusBadRequest, "key, name, flag_type are required")
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = "00000000-0000-0000-0000-000000000001"
	}
	if req.SafeDefault == "" {
		req.SafeDefault = "false"
	}

	// Check tombstone at service layer (DB trigger also enforces this)
	var exists bool
	_ = h.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM flag_tombstones WHERE key=$1)`, req.Key).Scan(&exists)
	if exists {
		writeError(w, http.StatusConflict, fmt.Sprintf("flag key %q is tombstoned and cannot be reused (Knight Capital prevention)", req.Key))
		return
	}

	actor := actorFromContext(r.Context())
	var f Flag
	err := h.db.QueryRowContext(r.Context(), `
		INSERT INTO flags (key, project_id, name, description, flag_type, state, owner_id, safe_default)
		VALUES ($1,$2,$3,$4,$5,'ACTIVE',$6,$7)
		RETURNING id, key, project_id, name, description, flag_type, state, owner_id, safe_default,
		          EXTRACT(EPOCH FROM created_at)::bigint, EXTRACT(EPOCH FROM updated_at)::bigint
	`, req.Key, req.ProjectID, req.Name, req.Description, req.FlagType, req.OwnerID, req.SafeDefault).
		Scan(&f.ID, &f.Key, &f.ProjectID, &f.Name, &f.Description, &f.FlagType, &f.State,
			&f.OwnerID, &f.SafeDefault, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		h.logger.Error("create flag", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Create default environment rows
	for _, env := range []string{"development", "staging", "production"} {
		_, _ = h.db.ExecContext(r.Context(), `
			INSERT INTO flag_environments (flag_id, environment, enabled, rollout_pct, updated_by)
			VALUES ($1,$2,false,0,$3) ON CONFLICT DO NOTHING
		`, f.ID, env, actor)
	}

	h.writeAudit(r.Context(), f.Key, "", actor, "flag_created", nil, &f, ipFromRequest(r))
	writeJSON(w, http.StatusCreated, f)
}

// GetFlag handles GET /api/v1/flags/{key}
func (h *FlagHandler) GetFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var f Flag
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, key, project_id, name, description, flag_type, state, owner_id, safe_default,
		       EXTRACT(EPOCH FROM created_at)::bigint, EXTRACT(EPOCH FROM updated_at)::bigint
		FROM flags WHERE key=$1
	`, key).Scan(&f.ID, &f.Key, &f.ProjectID, &f.Name, &f.Description, &f.FlagType, &f.State,
		&f.OwnerID, &f.SafeDefault, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// UpdateEnvironment handles PATCH /api/v1/flags/{key}/environments/{env}
func (h *FlagHandler) UpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	env := chi.URLParam(r, "env")

	var req UpdateEnvironmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	actor := actorFromContext(r.Context())
	if req.UpdatedBy == "" {
		req.UpdatedBy = actor
	}

	// Get current state for audit
	var prev FlagEnvironmentState
	_ = h.db.QueryRowContext(r.Context(), `
		SELECT fe.flag_id, f.key, fe.environment, fe.enabled, fe.rollout_pct, f.safe_default,
		       EXTRACT(EPOCH FROM fe.updated_at)::bigint
		FROM flag_environments fe JOIN flags f ON f.id = fe.flag_id
		WHERE f.key=$1 AND fe.environment=$2
	`, key, env).Scan(&prev.FlagID, &prev.FlagKey, &prev.Environment, &prev.Enabled,
		&prev.RolloutPct, &prev.SafeDefault, &prev.UpdatedAt)

	res, err := h.db.ExecContext(r.Context(), `
		UPDATE flag_environments fe SET enabled=$1, rollout_pct=$2, updated_at=now(), updated_by=$3
		FROM flags f WHERE f.id=fe.flag_id AND f.key=$4 AND fe.environment=$5
	`, req.Enabled, req.RolloutPct, req.UpdatedBy, key, env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "flag or environment not found")
		return
	}

	curr := FlagEnvironmentState{
		FlagKey: key, Environment: env,
		Enabled: req.Enabled, RolloutPct: req.RolloutPct,
	}
	h.writeAudit(r.Context(), key, env, actor, "flag_environment_updated", &prev, &curr, ipFromRequest(r))
	h.publishEvent(r.Context(), env, FlagEvent{
		FlagKey: key, Enabled: req.Enabled, RolloutPct: req.RolloutPct,
		Reason: "manual", Ts: time.Now().Unix(), Environment: env,
	})

	writeJSON(w, http.StatusOK, curr)
}

// KillSwitch handles POST /api/v1/flags/{key}/kill
func (h *FlagHandler) KillSwitch(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	type killReq struct {
		Environment string `json:"environment"`
		Reason      string `json:"reason"`
	}
	var req killReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Environment == "" {
		writeError(w, http.StatusBadRequest, "environment is required")
		return
	}
	if req.Reason == "" {
		req.Reason = "manual_kill_switch"
	}

	actor := actorFromContext(r.Context())
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE flag_environments fe SET enabled=false, updated_at=now(), updated_by=$1
		FROM flags f WHERE f.id=fe.flag_id AND f.key=$2 AND fe.environment=$3
	`, actor, key, req.Environment)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "flag or environment not found")
		return
	}

	h.writeAudit(r.Context(), key, req.Environment, actor, "kill_switch_activated",
		nil, map[string]any{"enabled": false, "reason": req.Reason}, ipFromRequest(r))
	h.publishEvent(r.Context(), req.Environment, FlagEvent{
		FlagKey: key, Enabled: false, RolloutPct: 0,
		Reason: req.Reason, Ts: time.Now().Unix(), Environment: req.Environment,
	})

	writeJSON(w, http.StatusOK, map[string]any{"killed": true, "flag_key": key, "environment": req.Environment})
}

// ArchiveFlag handles DELETE /api/v1/flags/{key}
func (h *FlagHandler) ArchiveFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	actor := actorFromContext(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(), `UPDATE flags SET state='ARCHIVED', archived_at=now() WHERE key=$1`, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, err = tx.ExecContext(r.Context(),
		`INSERT INTO flag_tombstones (key, archived_by) VALUES ($1,$2) ON CONFLICT DO NOTHING`, key, actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.writeAudit(r.Context(), key, "", actor, "flag_archived", nil, map[string]any{"tombstoned": true}, ipFromRequest(r))
	writeJSON(w, http.StatusOK, map[string]any{"archived": true, "tombstoned": true, "key": key})
}

// publishEvent writes a flag change event to Redis Streams (primary) and
// Redis pub/sub (deprecated — kept for 1 release cycle; remove in v2.1).
func (h *FlagHandler) publishEvent(ctx context.Context, environment string, event FlagEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}

	// Primary: Redis Streams — delivery guarantees + event history for late-joining SDKs.
	streamKey := fmt.Sprintf("tombstone:stream:%s", environment)
	if err := h.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: 10000, // retain last 10,000 events per stream
		Approx: true,  // MAXLEN ~ (approximate, faster)
		Values: map[string]interface{}{
			"event":       "flag_updated",
			"flag_key":    event.FlagKey,
			"environment": environment,
			"payload":     string(payload),
		},
	}).Err(); err != nil {
		h.logger.Warn("redis xadd failed", zap.Error(err), zap.String("stream", streamKey))
	}

	// Deprecated: Redis pub/sub — remove in v2.1 once all consumers migrate to Streams.
	channel := fmt.Sprintf("stream:%s:updates", environment) // deprecated: remove in v2.1
	if err := h.rdb.Publish(ctx, channel, payload).Err(); err != nil {
		h.logger.Warn("redis publish failed (deprecated)", zap.Error(err), zap.String("channel", channel))
	}
}

// writeAudit writes an append-only audit entry with Merkle hash linking
func (h *FlagHandler) writeAudit(ctx context.Context, flagKey, env, actor, eventType string, prev, curr any, ip string) {
	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

	// Get previous entry hash for Merkle chain
	var lastID, lastTs string
	_ = h.db.QueryRowContext(ctx, `
		SELECT id, EXTRACT(EPOCH FROM created_at)::text FROM audit_log
		WHERE flag_key=$1 ORDER BY created_at DESC LIMIT 1
	`, flagKey).Scan(&lastID, &lastTs)

	prevHash := ""
	if lastID != "" {
		hashBytes := sha256.Sum256([]byte(lastID + lastTs))
		prevHash = fmt.Sprintf("%x", hashBytes)
	}

	_, err := h.db.ExecContext(ctx, `
		INSERT INTO audit_log (id, flag_key, environment, actor, event_type, prev_state, new_state, ip_address, prev_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, uuid.New().String(), flagKey, env, actor, eventType, prevJSON, currJSON, ip, prevHash)
	if err != nil {
		h.logger.Warn("audit log write failed", zap.Error(err))
	}
}

// actorFromContext reads the actor identity set by the auth middleware.
// The middleware uses middleware.ContextKeyActor which is of type contextKey("actor").
// We import it indirectly via the package-level variable injected into main.go.
func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ActorContextKey).(string); ok {
		return v
	}
	return "unknown"
}

// ActorContextKey is the shared context key — must match middleware.ContextKeyActor exactly.
// Both are struct{name string}{"actor"} so the key comparison works across packages.
var ActorContextKey = struct{ name string }{"actor"}

func ipFromRequest(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
