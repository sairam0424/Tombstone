package v1

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
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
	logger *zap.Logger
}

func NewPrerequisiteHandler(db *sql.DB, logger *zap.Logger) *PrerequisiteHandler {
	return &PrerequisiteHandler{db: db, logger: logger}
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
