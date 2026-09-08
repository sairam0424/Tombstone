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
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
)

// PrerequisiteHandler manages flag prerequisites — the GrowthBook ParentConditions pattern.
//
// gate=true  (default): if the prerequisite is not met, block the entire feature (serve safe_default).
// gate=false:           if the prerequisite is not met, skip only the current targeting rule and
//
//	continue evaluating the next rule (i.e. fallthrough behaviour).
type PrerequisiteHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

func NewPrerequisiteHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *PrerequisiteHandler {
	return &PrerequisiteHandler{db: db, rdb: rdb, logger: logger}
}

// PrerequisitesEvent is published to the Redis Stream (tombstone:stream:
// {environment}) whenever a flag's prerequisite set changes (AddPrerequisite/
// DeletePrerequisite), carrying the flag's CURRENT FULL prerequisite list
// post-mutation -- SDKs apply it as a full replacement, not a delta, which
// removes any add/remove-ORDERING ambiguity WITHIN a single mutation's own
// publish (no separate add-delta/remove-delta to sequence against each
// other).
//
// Disclosed, NOT fixed here (found by adversarial review of this PR): this
// does NOT make delivery fully race-free across CONCURRENT requests on the
// SAME flag. publishPrerequisitesUpdated's own SELECT-then-XAdd sequence
// has no per-flag lock or monotonic version, so two overlapping requests
// (e.g. a rapid Add then Delete) can have their XAdds reach Redis in an
// order that does NOT match their real DB-commit order, if the earlier
// commit's own publish step is delayed (GC pause, connection contention)
// past the later commit's. Under this Stream's "last-delivered-entry-wins"
// model, that leaves a connected client with a STALE prerequisite list
// until its next full snapshot refetch -- self-healing, not permanent, but
// real. The Ts field below already carries a real-time timestamp
// specifically so a future SDK-side consumer can guard against this: when
// building each SDK's own apply-prerequisites-event logic (a separate,
// following PR per SDK -- not yet done as of this commit), reject an
// incoming event whose Ts is OLDER than the currently-cached prerequisites'
// own last-applied Ts, rather than unconditionally overwriting on arrival
// order. That closes the gap at the point where staleness actually
// matters, since fully serializing concurrent publishes here would need a
// distributed (cross-replica) per-flag lock -- a materially bigger, and
// separable, change.
//
// Deliberately Streams-only, never dual-written to the legacy pub/sub
// channel the way FlagEvent still is (see flags.go's "legacy pub/sub
// removed in v2.1" convention note) -- this is new code with no backward-
// compat obligation to a transport already scheduled for removal. That
// also means it does NOT need gateway's eventDeduper at all (dedup only
// exists to suppress a duplicate delivery via the OTHER transport), and it
// is a wholly separate struct from FlagEvent so this never touches
// FlagEvent's own wire shape or eventDeduper's map-key comparability
// requirement (FlagEvent must stay a plain, comparable struct -- adding a
// slice field to it directly would break `map[FlagEvent]time.Time`).
type PrerequisitesEvent struct {
	FlagKey       string         `json:"flag_key"`
	Environment   string         `json:"environment"`
	Prerequisites []Prerequisite `json:"prerequisites"`
	Ts            int64          `json:"ts"`
}

// publishPrerequisitesUpdated fetches flagKey's current full prerequisite
// list and fans out a PrerequisitesEvent to every environment the flag has
// state in -- flag_prerequisites has no environment column of its own (a
// prerequisite applies across all environments), so this mirrors
// ArchiveFlag's own INT-4 per-environment loop (flags.go) rather than
// guessing a single hardcoded environment. Fail-soft: a query or publish
// failure is logged and swallowed, matching publishEvent/publishToStream's
// own convention -- a broken live-update path must never fail the actual
// mutation request that already committed.
func (h *PrerequisiteHandler) publishPrerequisitesUpdated(ctx context.Context, key, projectID string) {
	if h.rdb == nil {
		return
	}
	q := sqlcgen.New(h.db)

	rows, err := q.ListPrerequisitesForFlag(ctx, sqlcgen.ListPrerequisitesForFlagParams{Key: key, ProjectID: projectID})
	if err != nil {
		h.logger.Warn("publish prerequisites_updated: list query failed", zap.String("flag", key), zap.Error(err))
		return
	}
	prereqs := make([]Prerequisite, 0, len(rows))
	for _, r := range rows {
		prereqs = append(prereqs, Prerequisite{
			ID:                r.ID,
			FlagID:            r.FlagID,
			FlagKey:           r.PrereqFlagKey,
			RequiredVariation: r.RequiredVariation,
			Gate:              r.Gate,
			Priority:          int(r.Priority),
			CreatedAt:         r.CreatedAt,
		})
	}

	envs, err := q.ListFlagEnvironmentsForKey(ctx, sqlcgen.ListFlagEnvironmentsForKeyParams{Key: key, ProjectID: projectID})
	if err != nil {
		h.logger.Warn("publish prerequisites_updated: environment list query failed", zap.String("flag", key), zap.Error(err))
		return
	}

	ts := time.Now().Unix()
	for _, env := range envs {
		publishPrerequisitesEvent(ctx, h.rdb, h.logger, env, PrerequisitesEvent{
			FlagKey: key, Environment: env, Prerequisites: prereqs, Ts: ts,
		})
	}
}

// prerequisitesEventKind is the Streams-entry discriminator gateway checks
// (via a dedicated "kind" field, NOT "event" -- see publishPrerequisitesEvent's
// own doc comment for why) to route a PrerequisitesEvent differently from a
// regular FlagEvent.
const prerequisitesEventKind = "prerequisites_updated"

// publishPrerequisitesEvent XAdds a PrerequisitesEvent to the Streams-only
// path. Discriminated from a regular FlagEvent via a DEDICATED "kind" field
// in the XAdd Values map -- deliberately NOT the pre-existing "event" field,
// which publishFlagEventToStream (this file's sibling) always sets to a
// FlagEvent's own Reason, an unvalidated, free-text, caller-supplied string
// for KillSwitch/RollbackStep/RecoveryStep (their "reason" request field).
// An adversarial review of an earlier version of this function used "event"
// for this discriminator and found that ANY of those three endpoints being
// called with reason="prerequisites_updated" (plausible, not just
// hypothetical, now that this is a real domain term) would make gateway
// misroute a genuine flag-toggle event as a bogus "zero prerequisites"
// update, corrupting a connected SDK's cache. "kind" is never set by any
// FlagEvent publisher (confirmed by reading flags.go's publishFlagEvent/
// publishFlagEventToStream), so no caller-controlled string can ever
// collide with it, structurally, not just by convention. "event" is still
// set too (to the same literal, for a human directly inspecting the stream
// via redis-cli XRANGE) but is no longer load-bearing for routing.
func publishPrerequisitesEvent(ctx context.Context, rdb *redis.Client, logger *zap.Logger, environment string, event PrerequisitesEvent) {
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
			"kind":        prerequisitesEventKind,
			"event":       prerequisitesEventKind,
			"flag_key":    event.FlagKey,
			"environment": environment,
			"payload":     string(payload),
		},
	}).Err(); err != nil {
		logger.Warn("redis xadd failed", zap.String("stream", streamKey), zap.Error(err))
	}
}

// Prerequisite is the API-level representation of a flag_prerequisites row.
//
// FlagKey's json tag is "flag_key", NOT "prereq_flag_key" -- the latter is
// only the flag_prerequisites TABLE's own column name (distinguishing it
// from that row's flag_id, which refers to the PARENT flag). The wire-level
// API contract must match proto/v1/flags/flags.proto's ParentCondition
// message (flag_key/required_variation/gate/priority) since every SDK's
// FlagPrerequisite type (Python/Java/Ruby/.NET/TS) was written against that
// proto naming. Before this fix this struct used "prereq_flag_key" on the
// wire, silently diverging from the proto contract -- every SDK's
// prerequisite dependency lookup read the wrong key against a real
// response and got an empty/missing value (found while investigating
// SDK-4's prerequisites-streaming follow-up).
type Prerequisite struct {
	ID                string `json:"id"`
	FlagID            string `json:"flag_id"`
	FlagKey           string `json:"flag_key"`
	RequiredVariation string `json:"required_variation"`
	Gate              bool   `json:"gate"`
	Priority          int    `json:"priority"`
	CreatedAt         int64  `json:"created_at"`
}

// AddPrerequisiteRequest is the request body for POST /api/v1/flags/{key}/prerequisites.
// FlagKey's wire name is "flag_key" -- see Prerequisite's doc comment above.
type AddPrerequisiteRequest struct {
	FlagKey           string `json:"flag_key"`
	RequiredVariation string `json:"required_variation"`
	Gate              *bool  `json:"gate"`     // pointer so we can distinguish false from omitted
	Priority          int    `json:"priority"` // default 0
}

// AddPrerequisite handles POST /api/v1/flags/{key}/prerequisites
//
// Validates:
//  1. The parent flag ({key}) exists.
//  2. The prerequisite flag (flag_key) exists.
//  3. No circular dependency exists (depth-first, max 5 hops).
func (h *PrerequisiteHandler) AddPrerequisite(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	// TEN-1a: every query in this handler (parent lookup, prereq-existence
	// check, cycle walk) previously matched by key alone across ALL projects.
	// That meant a caller could attach a prerequisite gate to another
	// project's flag by guessing its key, or point a prerequisite AT another
	// project's flag key — making one project's flag evaluation depend on a
	// foreign flag's state, which also leaks that foreign flag's variation as
	// a side channel (whether your own flag gets gated reveals its value).
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	var req AddPrerequisiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.FlagKey == "" {
		writeError(w, http.StatusBadRequest, "flag_key is required")
		return
	}
	if req.RequiredVariation == "" {
		req.RequiredVariation = "true"
	}
	gate := true
	if req.Gate != nil {
		gate = *req.Gate
	}

	q := sqlcgen.New(h.db)

	// Resolve parent flag ID.
	flagID, err := q.ResolveFlagIDByKey(r.Context(), sqlcgen.ResolveFlagIDByKeyParams{Key: key, ProjectID: projectID})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	} else if err != nil {
		h.logger.Error("resolve flag id", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Verify the prerequisite flag exists IN THE SAME PROJECT — a
	// prerequisite pointing at another project's flag is never valid, not
	// even if that flag key also happens to exist there.
	prereqExists, _ := q.FlagExistsInProject(r.Context(), sqlcgen.FlagExistsInProjectParams{Key: req.FlagKey, ProjectID: projectID})
	if !prereqExists {
		writeError(w, http.StatusUnprocessableEntity, "flag_key does not exist")
		return
	}

	// Circular dependency check (depth-first, max 5 hops).
	// We walk the prerequisite graph starting from flag_key and ensure we
	// never arrive back at key.
	if err := h.detectCycle(r, projectID, key, req.FlagKey, 0); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	inserted, err := q.InsertPrerequisite(r.Context(), sqlcgen.InsertPrerequisiteParams{
		FlagID:            flagID,
		PrereqFlagKey:     req.FlagKey,
		RequiredVariation: req.RequiredVariation,
		Gate:              gate,
		Priority:          int32(req.Priority),
	})
	if err != nil {
		h.logger.Error("insert prerequisite", zap.Error(err))
		// Unique-constraint violation (duplicate prereq for same flag).
		writeError(w, http.StatusConflict, "prerequisite already exists for this flag+flag_key pair")
		return
	}
	p := Prerequisite{
		ID:                inserted.ID,
		FlagID:            inserted.FlagID,
		FlagKey:           inserted.PrereqFlagKey,
		RequiredVariation: inserted.RequiredVariation,
		Gate:              inserted.Gate,
		Priority:          int(inserted.Priority),
		CreatedAt:         inserted.CreatedAt,
	}

	h.publishPrerequisitesUpdated(r.Context(), key, projectID)
	writeJSON(w, http.StatusCreated, p)
}

// ListPrerequisites handles GET /api/v1/flags/{key}/prerequisites
func (h *PrerequisiteHandler) ListPrerequisites(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	rows, err := sqlcgen.New(h.db).ListPrerequisitesForFlag(r.Context(), sqlcgen.ListPrerequisitesForFlagParams{Key: key, ProjectID: projectID})
	if err != nil {
		h.logger.Error("list prerequisites", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	prereqs := []Prerequisite{}
	for _, r := range rows {
		prereqs = append(prereqs, Prerequisite{
			ID:                r.ID,
			FlagID:            r.FlagID,
			FlagKey:           r.PrereqFlagKey,
			RequiredVariation: r.RequiredVariation,
			Gate:              r.Gate,
			Priority:          int(r.Priority),
			CreatedAt:         r.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"prerequisites": prereqs, "total": len(prereqs)})
}

// DeletePrerequisite handles DELETE /api/v1/flags/{key}/prerequisites/{id}
func (h *PrerequisiteHandler) DeletePrerequisite(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	prereqID := chi.URLParam(r, "id")

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	n, err := sqlcgen.New(h.db).DeletePrerequisite(r.Context(), sqlcgen.DeletePrerequisiteParams{
		Key: key, ID: prereqID, ProjectID: projectID,
	})
	if err != nil {
		h.logger.Error("delete prerequisite", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "prerequisite not found")
		return
	}
	h.publishPrerequisitesUpdated(r.Context(), key, projectID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": prereqID})
}

// detectCycle performs a depth-first search to detect circular prerequisite chains.
// It starts from startKey (the new prereq) and checks whether flagKey (the parent)
// is reachable within maxDepth hops. The walk is confined to a single project —
// TEN-1a: without the project filter, this walked the ENTIRE cross-project
// prerequisite graph, so an unrelated flag in a different project sharing a
// key with one hop of a real chain could produce a false cycle rejection.
func (h *PrerequisiteHandler) detectCycle(r *http.Request, projectID, flagKey, startKey string, depth int) error {
	const maxDepth = 5
	if depth > maxDepth {
		return errors.New("prerequisite chain exceeds maximum depth of 5 hops")
	}

	// Fetch all prerequisites of startKey.
	nextKeys, err := sqlcgen.New(h.db).ListPrereqFlagKeysForFlag(r.Context(), sqlcgen.ListPrereqFlagKeysForFlagParams{
		Key: startKey, ProjectID: projectID,
	})
	if err != nil {
		return nil // non-fatal: allow the insert if we can't walk the graph
	}

	for _, nextKey := range nextKeys {
		if nextKey == flagKey {
			return errors.New("circular prerequisite dependency detected")
		}
		if err := h.detectCycle(r, projectID, flagKey, nextKey, depth+1); err != nil {
			return err
		}
	}
	return nil
}
