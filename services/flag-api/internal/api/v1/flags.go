package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/secrets"
	"github.com/tombstone/flag-api/internal/transparency"
)

type FlagHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
	rekor  *transparency.RekorClient
	// audit is the single writer for the hash-chained audit log (AUD-1).
	audit *audit.Writer
	// hasher validates break-glass tokens presented to bypass the
	// require_approval gate (SEC-3b part 2) — the same hasher BreakGlassHandler
	// uses, so a token created there validates here too.
	hasher *secrets.TokenHasher
}

func NewFlagHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger, rekor *transparency.RekorClient, auditW *audit.Writer, hasher *secrets.TokenHasher) *FlagHandler {
	return &FlagHandler{db: db, rdb: rdb, logger: logger, rekor: rekor, audit: auditW, hasher: hasher}
}

type Flag struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	FlagType    string `json:"flag_type"`
	State       string `json:"state"`
	OwnerID     string `json:"owner_id"`
	SafeDefault string `json:"safe_default"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
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
	SafeDefault string `json:"safe_default"`
}

// ValidFlagTypes is the exhaustive set of flag types accepted at the service layer.
// This mirrors the DB CHECK constraint so we catch invalid types before hitting Postgres.
var ValidFlagTypes = map[string]bool{
	"BOOLEAN": true,
	"STRING":  true,
	"INTEGER": true,
	"FLOAT":   true,
	"JSON":    true,
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
	// TEN-1a: project_id was previously a client-supplied query param, so any
	// caller could list any other project's flags by naming its UUID. It is
	// now the tenant resolved from the caller's own identity.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	rows, err := sqlcgen.New(h.db).ListFlags(r.Context(), projectID)
	if err != nil {
		h.logger.Error("list flags query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	flags := []Flag{}
	for _, row := range rows {
		flags = append(flags, Flag{
			ID: row.ID, Key: row.Key, ProjectID: row.ProjectID, Name: row.Name, Description: row.Description,
			FlagType: row.FlagType, State: row.State, OwnerID: row.OwnerID, SafeDefault: row.SafeDefault,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
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
	if !ValidFlagTypes[req.FlagType] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid flag_type %q: must be one of BOOLEAN, STRING, INTEGER, FLOAT, JSON", req.FlagType))
		return
	}
	if req.SafeDefault == "" {
		req.SafeDefault = "false"
	}

	// TEN-1a: a flag is always created in the caller's own resolved project —
	// CreateFlagRequest no longer accepts project_id at all (it defaulted to a
	// hardcoded UUID before, and a caller who did supply one could create
	// flags directly inside any project they could name).
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	// Check tombstone at service layer (DB trigger also enforces this).
	// flag_tombstones is DELIBERATELY global, not project-scoped: the Knight
	// Capital prevention this table exists for is "this exact key string must
	// never be reused, anywhere, once retired" — scoping it per-project would
	// let a different project silently reuse a key another project already
	// archived, which is precisely the fat-finger-reuse risk it exists to
	// close. This is a conscious TEN-1a decision, not an oversight.
	exists, _ := sqlcgen.New(h.db).FlagTombstoneExists(r.Context(), req.Key)
	if exists {
		writeError(w, http.StatusConflict, fmt.Sprintf("flag key %q is tombstoned and cannot be reused (Knight Capital prevention)", req.Key))
		return
	}

	actor := actorFromContext(r.Context())
	row, err := sqlcgen.New(h.db).CreateFlag(r.Context(), sqlcgen.CreateFlagParams{
		Key: req.Key, ProjectID: projectID, Name: req.Name, Description: req.Description,
		FlagType: req.FlagType, OwnerID: req.OwnerID, SafeDefault: req.SafeDefault,
	})
	if err != nil {
		h.logger.Error("create flag", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f := Flag{
		ID: row.ID, Key: row.Key, ProjectID: row.ProjectID, Name: row.Name, Description: row.Description,
		FlagType: row.FlagType, State: row.State, OwnerID: row.OwnerID, SafeDefault: row.SafeDefault,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}

	// Create default environment rows
	for _, env := range []string{"development", "staging", "production"} {
		_ = sqlcgen.New(h.db).CreateDefaultFlagEnvironment(r.Context(), sqlcgen.CreateDefaultFlagEnvironmentParams{
			FlagID: f.ID, Environment: env, UpdatedBy: actor,
		})
	}

	h.writeAudit(r.Context(), projectID, f.Key, "", actor, "flag_created", nil, &f, ipFromRequest(r))
	writeJSON(w, http.StatusCreated, f)
}

// GetFlag handles GET /api/v1/flags/{key}
func (h *FlagHandler) GetFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	// TEN-1a: this query had NO project filter at all — key alone, and
	// flags.key is only unique per (project_id, key) — so any authenticated
	// caller could read any OTHER project's flag by guessing/knowing its key.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	row, err := sqlcgen.New(h.db).GetFlag(r.Context(), sqlcgen.GetFlagParams{Key: key, ProjectID: projectID})
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f := Flag{
		ID: row.ID, Key: row.Key, ProjectID: row.ProjectID, Name: row.Name, Description: row.Description,
		FlagType: row.FlagType, State: row.State, OwnerID: row.OwnerID, SafeDefault: row.SafeDefault,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	writeJSON(w, http.StatusOK, f)
}

// UpdateEnvironment handles PATCH /api/v1/flags/{key}/environments/{env}
func (h *FlagHandler) UpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	env := chi.URLParam(r, "env")

	// TEN-1a: both the prev-state read and the UPDATE below matched on key
	// alone, so an operator in one project could flip rollout%/enabled for
	// another project's flag if it happened to share a key.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	// Inject flag state into the active trace span.
	span := trace.SpanFromContext(r.Context())

	var req UpdateEnvironmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// DATA-1b PR 4/4: this had no bounds check before sqlc — an out-of-range
	// rollout_pct simply hit flag_environments' own CHECK(rollout_pct BETWEEN
	// 0 AND 100) constraint and errored cleanly. sqlc's generated
	// UpdateFlagEnvironmentParams.RolloutPct is int32 (matching the column),
	// forcing an explicit int32(req.RolloutPct) conversion at the call
	// site below — Go's narrowing conversion wraps out-of-int32-range values
	// via modular arithmetic instead of erroring, so a sufficiently large
	// value could wrap into an in-range one and silently defeat the DB's own
	// check. Validating here (matching ProposeChangeRequest's identical
	// check) closes that gap instead of relying on a range check that this
	// conversion silently bypasses.
	if req.RolloutPct < 0 || req.RolloutPct > 100 {
		writeError(w, http.StatusBadRequest, "rollout_pct must be between 0 and 100")
		return
	}

	span.SetAttributes(
		attribute.String("flag.key", key),
		attribute.String("flag.environment", env),
		attribute.Bool("flag.enabled", req.Enabled),
		attribute.Int("flag.rollout_pct", req.RolloutPct),
	)

	actor := actorFromContext(r.Context())
	if req.UpdatedBy == "" {
		req.UpdatedBy = actor
	}

	// SEC-3b (part 2): a project with require_approval=true must not have
	// this endpoint write flag_environments directly at all — the whole
	// point of the gate is that routine changes go through
	// POST /change-requests instead. A valid, unused break-glass token is
	// the documented emergency escape hatch. Checked only AFTER the body
	// decodes and the request is otherwise well-formed: consuming a
	// one-shot emergency token for a request that was going to 400 anyway
	// would waste it for nothing.
	requireApproval, err := h.projectRequiresApproval(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	var breakGlassToken string
	if requireApproval {
		breakGlassToken = r.Header.Get(breakGlassHeader)
		if breakGlassToken == "" {
			writeError(w, http.StatusForbidden,
				"this project requires approval for environment changes — submit via POST /api/v1/change-requests, "+
					"or supply a valid "+breakGlassHeader+" header for an emergency bypass")
			return
		}
	}

	// Everything from here on — the break-glass token consumption (if any)
	// and the actual write — happens in ONE transaction. If the target
	// flag/environment doesn't exist (RowsAffected==0) or the write
	// otherwise fails, the whole transaction rolls back, so a one-shot
	// break-glass token is never burned for a mutation that never happened.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var bgScope, bgTokenID string
	if breakGlassToken != "" {
		bgScope, bgTokenID, err = consumeBreakGlassTokenTx(r.Context(), tx, h.hasher, breakGlassToken, actor, projectID)
		if err != nil {
			switch {
			case errors.Is(err, errBreakGlassTokenUsed):
				writeError(w, http.StatusGone, err.Error())
			case errors.Is(err, errBreakGlassTokenWrongProject):
				writeError(w, http.StatusForbidden, err.Error())
			default:
				writeError(w, http.StatusUnauthorized, err.Error())
			}
			return
		}
	}

	// Get current state for audit
	var prev FlagEnvironmentState
	if prevRow, prevErr := sqlcgen.New(tx).GetFlagEnvironmentPrevState(r.Context(), sqlcgen.GetFlagEnvironmentPrevStateParams{
		Key: key, Environment: env, ProjectID: projectID,
	}); prevErr == nil {
		prev = FlagEnvironmentState{
			FlagID: prevRow.FlagID, FlagKey: prevRow.Key, Environment: prevRow.Environment,
			Enabled: prevRow.Enabled, RolloutPct: int(prevRow.RolloutPct), SafeDefault: prevRow.SafeDefault,
			UpdatedAt: prevRow.UpdatedAt,
		}
	}

	n, err := sqlcgen.New(tx).UpdateFlagEnvironment(r.Context(), sqlcgen.UpdateFlagEnvironmentParams{
		Enabled: req.Enabled, RolloutPct: int32(req.RolloutPct), UpdatedBy: req.UpdatedBy,
		Key: key, Environment: env, ProjectID: projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "flag or environment not found")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}

	curr := FlagEnvironmentState{
		FlagKey: key, Environment: env,
		Enabled: req.Enabled, RolloutPct: req.RolloutPct,
	}
	eventType := "flag_environment_updated"
	if breakGlassToken != "" {
		eventType = "flag_environment_updated_via_breakglass"
	}
	h.writeAudit(r.Context(), projectID, key, env, actor, eventType, &prev, &curr, ipFromRequest(r))
	if breakGlassToken != "" {
		h.logger.Warn("require_approval bypassed via break-glass token",
			zap.String("project_id", projectID), zap.String("actor", actor),
			zap.String("scope", bgScope), zap.String("token_id", bgTokenID), zap.String("path", r.URL.Path))
		writeBreakGlassAuditEntry(r.Context(), h.audit, h.logger, ipFromRequest(r), actor, bgTokenID, bgScope,
			"bypassed require_approval on "+r.URL.Path, key, env, projectID)
	}
	// GW-1: publish ONE event value to both transports, not two independently
	// constructed literals — gateway's dedup (services/gateway/internal/hub/
	// dedup.go) keys on the full event including Ts, so two separate
	// time.Now().Unix() calls straddling a second boundary would make the
	// same logical mutation look like two different events and double-
	// broadcast to every gateway replica's clients.
	event := FlagEvent{
		FlagKey: key, Enabled: req.Enabled, RolloutPct: req.RolloutPct,
		Reason: "manual", Ts: time.Now().Unix(), Environment: env,
	}
	h.publishEvent(r.Context(), env, event)
	h.publishToStream(r.Context(), env, event)

	writeJSON(w, http.StatusOK, curr)
}

// KillSwitch handles POST /api/v1/flags/{key}/kill
func (h *FlagHandler) KillSwitch(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	// TEN-1a: the UPDATE below matched key alone — an OWNER/ADMIN in one
	// project could kill a same-keyed flag belonging to a different project.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

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

	// Inject kill-switch state into the active trace span.
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.String("flag.key", key),
		attribute.String("flag.environment", req.Environment),
		attribute.Bool("flag.enabled", false),
		attribute.Int("flag.rollout_pct", 0),
		attribute.String("flag.kill_reason", req.Reason),
	)

	actor := actorFromContext(r.Context())
	// Reuses UpdateFlagEnvironment (the same query UpdateEnvironment's own
	// handler calls) rather than a dedicated kill-only query -- as a side
	// effect this fixes a pre-existing gap where a full kill left
	// rollout_pct untouched in the DB even though the published event/audit
	// record always claimed 0: now both agree. KillSwitch stays a pure,
	// always-safe binary action (enabled=false, rollout_pct=0) -- EVAL-4's
	// graduated rollback-step capability is a SEPARATE endpoint
	// (RollbackStep) with its own, narrower permission, specifically so
	// this endpoint's existing require_approval bypass (see
	// projectRequiresApproval's absence from this handler, contrasting
	// UpdateEnvironment's explicit check) never widens into a general,
	// OWNER/ADMIN-usable approval-bypass path for arbitrary percentage
	// changes -- see PR #220's adversarial review.
	n, err := sqlcgen.New(h.db).UpdateFlagEnvironment(r.Context(), sqlcgen.UpdateFlagEnvironmentParams{
		Enabled: false, RolloutPct: 0, UpdatedBy: actor,
		Key: key, Environment: req.Environment, ProjectID: projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "flag or environment not found")
		return
	}

	h.writeAudit(r.Context(), projectID, key, req.Environment, actor, "kill_switch_activated",
		nil, map[string]any{"enabled": false, "rollout_pct": 0, "reason": req.Reason}, ipFromRequest(r))
	// GW-1: see UpdateEnvironment's identical comment above — one event
	// value shared by both transports, not two independently-timestamped
	// literals.
	killEvent := FlagEvent{
		FlagKey: key, Enabled: false, RolloutPct: 0,
		Reason: req.Reason, Ts: time.Now().Unix(), Environment: req.Environment,
	}
	h.publishEvent(r.Context(), req.Environment, killEvent)
	h.publishToStream(r.Context(), req.Environment, killEvent)

	writeJSON(w, http.StatusOK, map[string]any{"killed": true, "flag_key": key, "environment": req.Environment})
}

// RollbackStep handles POST /api/v1/flags/{key}/rollback-step -- EVAL-4's
// automated, graduated rollback capability, gated by flags:circuit_breaker
// (RoleCircuitBreaker, assignable only via service_tokens.role -- never a
// human project-membership grant, see migration 026) rather than
// flags:kill_switch, so no OWNER/ADMIN gets this for free the way they
// already hold the full kill switch. Deliberately bypasses require_approval
// the same way KillSwitch does (see that handler's own comment) -- an
// automated incident-response mechanism must not be blockable by a workflow
// gate meant for routine, human-initiated changes.
//
// Unlike KillSwitch, this endpoint is NOT unconditional: it reads the
// flag's current state first and REJECTS any request that would increase
// exposure (raise rollout_pct or re-enable a disabled flag). A "rollback
// step" that can accidentally widen blast radius during a real incident --
// e.g. from a misconfigured caller or a wrong percentage -- would defeat
// the entire point of this being a safety mechanism.
func (h *FlagHandler) RollbackStep(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	type stepReq struct {
		Environment string `json:"environment"`
		Reason      string `json:"reason"`
		// RolloutPct is a required pointer, not a plain int: this endpoint's
		// entire purpose is setting a SPECIFIC percentage, so an omitted
		// field must be a validation error, not silently indistinguishable
		// from an explicit 0 (full kill) -- a caller bug that drops the
		// field must not be misread as a deliberate full rollback (found
		// by adversarial review of PR #220).
		RolloutPct *int `json:"rollout_pct"`
	}
	var req stepReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Environment == "" {
		writeError(w, http.StatusBadRequest, "environment is required")
		return
	}
	if req.RolloutPct == nil {
		writeError(w, http.StatusBadRequest, "rollout_pct is required")
		return
	}
	if *req.RolloutPct < 0 || *req.RolloutPct > 100 {
		writeError(w, http.StatusBadRequest, "rollout_pct must be between 0 and 100")
		return
	}
	if req.Reason == "" {
		req.Reason = "circuit_breaker"
	}

	// currentExposure/targetExposure treat a disabled flag as 0% exposure
	// regardless of its stored rollout_pct -- enabled=false already means
	// no traffic sees it, so the comparison that matters is the EFFECTIVE
	// exposure, not the raw column value.
	targetEnabled := *req.RolloutPct > 0
	targetExposure := 0
	if targetEnabled {
		targetExposure = *req.RolloutPct
	}

	prev, err := sqlcgen.New(h.db).GetFlagEnvironmentPrevState(r.Context(), sqlcgen.GetFlagEnvironmentPrevStateParams{
		Key: key, Environment: req.Environment, ProjectID: projectID,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "flag or environment not found")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	currentExposure := 0
	if prev.Enabled {
		currentExposure = int(prev.RolloutPct)
	}
	// This early check gives a fast, friendly error in the common
	// (non-racing) case. It is NOT what actually enforces the invariant --
	// RollbackFlagEnvironment's own WHERE clause below re-checks the SAME
	// condition atomically as part of the write itself, closing the TOCTOU
	// gap this read-then-decide sequence would otherwise have between two
	// concurrent rollback-step calls (found by adversarial review of
	// PR #220's first version, which used a separate, unconditional
	// UpdateFlagEnvironment write here).
	if targetExposure > currentExposure {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"rollback-step cannot increase exposure: requested %d%%, current effective exposure is %d%%",
			targetExposure, currentExposure))
		return
	}

	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.String("flag.key", key),
		attribute.String("flag.environment", req.Environment),
		attribute.Bool("flag.enabled", targetEnabled),
		attribute.Int("flag.rollout_pct", targetExposure),
		attribute.String("flag.rollback_reason", req.Reason),
	)

	actor := actorFromContext(r.Context())
	n, err := sqlcgen.New(h.db).RollbackFlagEnvironment(r.Context(), sqlcgen.RollbackFlagEnvironmentParams{
		Enabled: targetEnabled, RolloutPct: int32(targetExposure), UpdatedBy: actor,
		Key: key, Environment: req.Environment, ProjectID: projectID,
		MinCurrentExposure: int32(targetExposure),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		// Either the flag/environment doesn't exist, or a concurrent
		// rollback-step call already reduced exposure below this
		// request's own target between our read above and this write --
		// re-read to tell the two apart rather than guessing.
		latest, latestErr := sqlcgen.New(h.db).GetFlagEnvironmentPrevState(r.Context(), sqlcgen.GetFlagEnvironmentPrevStateParams{
			Key: key, Environment: req.Environment, ProjectID: projectID,
		})
		if errors.Is(latestErr, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "flag or environment not found")
			return
		}
		if latestErr != nil {
			writeError(w, http.StatusInternalServerError, latestErr.Error())
			return
		}
		latestExposure := 0
		if latest.Enabled {
			latestExposure = int(latest.RolloutPct)
		}
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"a concurrent rollback step already reduced exposure to %d%%, below this request's target of %d%%",
			latestExposure, targetExposure))
		return
	}

	// A distinct event_type from KillSwitch's "kill_switch_activated" --
	// PR #220's adversarial review found reusing that event_type made a
	// destructive full kill indistinguishable from a non-destructive
	// graduated reduction without opening every entry's new_state payload.
	h.writeAudit(r.Context(), projectID, key, req.Environment, actor, "circuit_breaker_rollback_step",
		map[string]any{"enabled": prev.Enabled, "rollout_pct": prev.RolloutPct},
		map[string]any{"enabled": targetEnabled, "rollout_pct": targetExposure, "reason": req.Reason}, ipFromRequest(r))
	stepEvent := FlagEvent{
		FlagKey: key, Enabled: targetEnabled, RolloutPct: targetExposure,
		Reason: req.Reason, Ts: time.Now().Unix(), Environment: req.Environment,
	}
	h.publishEvent(r.Context(), req.Environment, stepEvent)
	h.publishToStream(r.Context(), req.Environment, stepEvent)

	writeJSON(w, http.StatusOK, map[string]any{
		"flag_key": key, "environment": req.Environment,
		"enabled": targetEnabled, "rollout_pct": targetExposure,
	})
}

// ArchiveFlag handles DELETE /api/v1/flags/{key}
func (h *FlagHandler) ArchiveFlag(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	actor := actorFromContext(r.Context())

	// TEN-1a: the UPDATE below matched key alone, so archiving a flag in one
	// project archived EVERY project's flag sharing that key. flag_tombstones
	// stays global on purpose (see the comment in CreateFlag) — only the
	// flags row itself needs to be scoped to the caller's project.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	n, err := sqlcgen.New(tx).ArchiveFlag(r.Context(), sqlcgen.ArchiveFlagParams{Key: key, ProjectID: projectID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}
	if err := sqlcgen.New(tx).CreateFlagTombstone(r.Context(), sqlcgen.CreateFlagTombstoneParams{Key: key, ArchivedBy: actor}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// INT-4: resolve the flag's REAL environments before commit, while the
	// row set is still guaranteed consistent within this transaction --
	// archiving doesn't touch flag_environments (no CASCADE fires; only
	// the flags row's state changes), so this is safe to read here.
	// Publishing to a single hardcoded "production" instead (an earlier
	// version of this fix did exactly that) only worked by coincidence of
	// every current deployment's TOMBSTONE_ENVIRONMENTS happening to be
	// "production" -- found by adversarial review of PR #210.
	envs, err := sqlcgen.New(tx).ListFlagEnvironmentsForKey(r.Context(), sqlcgen.ListFlagEnvironmentsForKeyParams{Key: key, ProjectID: projectID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(envs) == 0 {
		// Defensive fallback only -- CreateFlag always seeds a default
		// flag_environments row (CreateDefaultFlagEnvironment), so this
		// should be unreachable in practice.
		envs = []string{"production"}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.writeAudit(r.Context(), projectID, key, "", actor, "flag_archived", nil, map[string]any{"tombstoned": true}, ipFromRequest(r))

	// INT-4: notify intelligence's anomaly detector to evict this flag's
	// state (otherwise it leaks in-process forever, with no persistence or
	// TTL). Published once per environment the flag actually had state in
	// -- AnomalyDetector's state is keyed by flag_key alone with no per-
	// environment split, so this is redundant-but-harmless eviction calls
	// for a multi-environment flag, not a correctness issue; the goal is
	// reaching every stream ANY intelligence deployment might be
	// subscribed to, not just "production". Enabled/RolloutPct carry no
	// meaning for this event and are zero-valued, matching KillSwitch's
	// convention for fields the receiving side ignores.
	for _, env := range envs {
		archiveEvent := FlagEvent{
			FlagKey: key, Enabled: false, RolloutPct: 0,
			Reason: "archived", Ts: time.Now().Unix(), Environment: env,
		}
		h.publishEvent(r.Context(), env, archiveEvent)
		h.publishToStream(r.Context(), env, archiveEvent)
	}

	writeJSON(w, http.StatusOK, map[string]any{"archived": true, "tombstoned": true, "key": key})
}

// publishEvent publishes a flag change event to Redis pub/sub
func (h *FlagHandler) publishEvent(ctx context.Context, environment string, event FlagEvent) {
	publishFlagEvent(ctx, h.rdb, h.logger, environment, event)
}

// publishToStream publishes a flag change event to a Redis Stream (XADD).
// Runs alongside publishEvent for one release cycle (legacy pub/sub removed in v2.1).
// Stream key: tombstone:stream:{environment}, MaxLen: 10000 (approximate trim).
func (h *FlagHandler) publishToStream(ctx context.Context, environment string, event FlagEvent) {
	publishFlagEventToStream(ctx, h.rdb, h.logger, environment, event)
}

// publishFlagEvent/publishFlagEventToStream are standalone so ChangeRequestHandler
// (SEC-3b: applying an approved change_payload is the same kind of mutation
// UpdateEnvironment makes) can notify the same channels without holding a
// reference to FlagHandler.
func publishFlagEvent(ctx context.Context, rdb *redis.Client, logger *zap.Logger, environment string, event FlagEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	channel := fmt.Sprintf("stream:%s:updates", environment)
	if err := rdb.Publish(ctx, channel, payload).Err(); err != nil {
		logger.Warn("redis publish failed", zap.Error(err), zap.String("channel", channel))
	}
}

func publishFlagEventToStream(ctx context.Context, rdb *redis.Client, logger *zap.Logger, environment string, event FlagEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	streamKey := fmt.Sprintf("tombstone:stream:%s", environment)
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]interface{}{
			"event":       event.Reason,
			"flag_key":    event.FlagKey,
			"environment": environment,
			"payload":     string(payload),
		},
	}).Err(); err != nil {
		logger.Warn("redis xadd failed", zap.Error(err), zap.String("stream", streamKey))
	}
}

// writeAudit writes an append-only audit entry with Merkle hash linking, then
// asynchronously submits the entry hash to the Rekor transparency log (opt-in
// via REKOR_ENABLED=true). The Rekor submission never blocks the caller.
func (h *FlagHandler) writeAudit(ctx context.Context, projectID, flagKey, env, actor, eventType string, prev, curr any, ip string) {
	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

	// AUD-1: the chain is built by the single audit.Writer, which hashes with a
	// keyed HMAC inside an advisory-locked transaction. This handler previously
	// inlined its own SELECT-last-then-INSERT, which both forked under
	// concurrency and disagreed with scheduled.go's formula.
	if h.audit == nil {
		h.logger.Warn("audit log write skipped — no audit writer configured")
		return
	}
	entryID, entryHash, err := h.audit.Append(ctx, audit.Entry{
		FlagKey:     flagKey,
		Environment: env,
		Actor:       actor,
		EventType:   eventType,
		PrevState:   prevJSON,
		NewState:    currJSON,
		IPAddress:   ip,
		ProjectID:   projectID,
	})
	if err != nil {
		h.logger.Warn("audit log write failed", zap.Error(err))
		return
	}

	// Asynchronously submit the audit entry hash to Rekor transparency log.
	// Captures only what it needs; ctx is deliberately not forwarded so that
	// the goroutine outlives the HTTP request without inheriting its deadline.
	if h.rekor != nil {
		db := h.db
		logger := h.logger
		rekor := h.rekor
		go func() {
			// Submit the entry's own keyed hash: it commits to every field AND to
			// the preceding chain, so it is a strictly stronger witness than the
			// prev_hash snapshot this used to send.
			entrySnapshot := map[string]any{
				"id":          entryID,
				"flag_key":    flagKey,
				"environment": env,
				"actor":       actor,
				"event_type":  eventType,
				"entry_hash":  entryHash,
			}
			entryJSON, _ := json.Marshal(entrySnapshot)

			rekorCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()

			logID, logIndex, subErr := rekor.SubmitAuditEntry(rekorCtx, entryJSON)
			if subErr != nil || logID == "" {
				// SubmitAuditEntry already logs internally; nothing to do.
				return
			}

			if updateErr := sqlcgen.New(db).BackfillAuditLogRekor(rekorCtx, sqlcgen.BackfillAuditLogRekorParams{
				RekorLogID:    sql.NullString{String: logID, Valid: true},
				RekorLogIndex: sql.NullInt64{Int64: logIndex, Valid: true},
				ID:            entryID,
			}); updateErr != nil {
				logger.Warn("rekor back-fill update failed",
					zap.String("entry_id", entryID),
					zap.Error(updateErr),
				)
			}
		}()
	}
}

// actorFromContext reads the actor identity set by the auth middleware.
// Uses middleware.ContextKeyActor — the same named type as auth.go — so the
// context lookup succeeds regardless of package boundary.
func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(middleware.ContextKeyActor).(string); ok {
		return v
	}
	return "unknown"
}

// projectIDFromContext reads the tenant resolved by RequireProjectID
// (TEN-1a). Every route in this package runs behind that middleware, so a
// missing value here means the route is misconfigured, not that the caller
// is anonymous — callers must not fall back to any default project.
func projectIDFromContext(ctx context.Context) (string, bool) {
	return middleware.ProjectIDFromContext(ctx)
}

// requireProjectID is the shared guard every handler in this package opens
// with. It exists so a routing mistake (RequireProjectID missing from some
// future route) fails loudly as a 500, instead of a handler silently
// querying without a tenant filter — which is exactly how the pre-TEN-1a
// cross-tenant reads and writes happened.
func requireProjectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID, ok := projectIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project resolved for this request")
		return "", false
	}
	return projectID, true
}

// breakGlassHeader carries a break-glass token that bypasses the
// require_approval gate in UpdateEnvironment. A header, not a body field or
// query param, since it is out-of-band authorization for the request, not
// part of what the request is asking for.
const breakGlassHeader = "X-Break-Glass-Token"

// projectRequiresApproval reads the live, current require_approval policy
// for a project — a per-attempt policy check, not a value that needs to be
// pinned across steps of a longer-lived workflow (contrast with
// change_requests.required_approvals in change_requests.go, which IS
// snapshotted, because a proposal's quorum has to stay stable across
// multiple approvals collected over time).
func (h *FlagHandler) projectRequiresApproval(ctx context.Context, projectID string) (bool, error) {
	requireApproval, err := sqlcgen.New(h.db).GetProjectRequireApproval(ctx, projectID)
	if err != nil {
		h.logger.Error("read require_approval", zap.Error(err))
		return false, err
	}
	return requireApproval, nil
}

func ipFromRequest(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
