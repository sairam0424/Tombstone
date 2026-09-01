package v1

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
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
type Prerequisite struct {
	ID                string `json:"id"`
	FlagID            string `json:"flag_id"`
	PrereqFlagKey     string `json:"prereq_flag_key"`
	RequiredVariation string `json:"required_variation"`
	Gate              bool   `json:"gate"`
	Priority          int    `json:"priority"`
	CreatedAt         int64  `json:"created_at"`
}

// AddPrerequisiteRequest is the request body for POST /api/v1/flags/{key}/prerequisites.
type AddPrerequisiteRequest struct {
	PrereqFlagKey     string `json:"prereq_flag_key"`
	RequiredVariation string `json:"required_variation"`
	Gate              *bool  `json:"gate"`     // pointer so we can distinguish false from omitted
	Priority          int    `json:"priority"` // default 0
}

// AddPrerequisite handles POST /api/v1/flags/{key}/prerequisites
//
// Validates:
//  1. The parent flag ({key}) exists.
//  2. The prerequisite flag (prereq_flag_key) exists.
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
	if req.PrereqFlagKey == "" {
		writeError(w, http.StatusBadRequest, "prereq_flag_key is required")
		return
	}
	if req.RequiredVariation == "" {
		req.RequiredVariation = "true"
	}
	gate := true
	if req.Gate != nil {
		gate = *req.Gate
	}

	// Resolve parent flag ID.
	var flagID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM flags WHERE key = $1 AND project_id = $2`, key, projectID,
	).Scan(&flagID); errors.Is(err, sql.ErrNoRows) {
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
	var prereqExists bool
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM flags WHERE key = $1 AND project_id = $2)`, req.PrereqFlagKey, projectID,
	).Scan(&prereqExists)
	if !prereqExists {
		writeError(w, http.StatusUnprocessableEntity, "prereq_flag_key does not exist")
		return
	}

	// Circular dependency check (depth-first, max 5 hops).
	// We walk the prerequisite graph starting from prereq_flag_key and ensure we
	// never arrive back at key.
	if err := h.detectCycle(r, projectID, key, req.PrereqFlagKey, 0); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	var p Prerequisite
	err := h.db.QueryRowContext(r.Context(), `
		INSERT INTO flag_prerequisites (flag_id, prereq_flag_key, required_variation, gate, priority)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, flag_id, prereq_flag_key, required_variation, gate, priority,
		          EXTRACT(EPOCH FROM created_at)::bigint
	`, flagID, req.PrereqFlagKey, req.RequiredVariation, gate, req.Priority).
		Scan(&p.ID, &p.FlagID, &p.PrereqFlagKey, &p.RequiredVariation, &p.Gate, &p.Priority, &p.CreatedAt)
	if err != nil {
		h.logger.Error("insert prerequisite", zap.Error(err))
		// Unique-constraint violation (duplicate prereq for same flag).
		writeError(w, http.StatusConflict, "prerequisite already exists for this flag+prereq_flag_key pair")
		return
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

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT fp.id, fp.flag_id, fp.prereq_flag_key, fp.required_variation, fp.gate, fp.priority,
		       EXTRACT(EPOCH FROM fp.created_at)::bigint
		FROM flag_prerequisites fp
		JOIN flags f ON f.id = fp.flag_id
		WHERE f.key = $1 AND f.project_id = $2
		ORDER BY fp.priority ASC, fp.created_at ASC
	`, key, projectID)
	if err != nil {
		h.logger.Error("list prerequisites", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	prereqs := []Prerequisite{}
	for rows.Next() {
		var p Prerequisite
		if err := rows.Scan(&p.ID, &p.FlagID, &p.PrereqFlagKey, &p.RequiredVariation,
			&p.Gate, &p.Priority, &p.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		prereqs = append(prereqs, p)
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

	res, err := h.db.ExecContext(r.Context(), `
		DELETE FROM flag_prerequisites fp
		USING flags f
		WHERE f.id = fp.flag_id
		  AND f.key = $1
		  AND fp.id = $2
		  AND f.project_id = $3
	`, key, prereqID, projectID)
	if err != nil {
		h.logger.Error("delete prerequisite", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
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
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT fp.prereq_flag_key
		FROM flag_prerequisites fp
		JOIN flags f ON f.id = fp.flag_id
		WHERE f.key = $1 AND f.project_id = $2
	`, startKey, projectID)
	if err != nil {
		return nil // non-fatal: allow the insert if we can't walk the graph
	}
	defer rows.Close()

	for rows.Next() {
		var nextKey string
		if err := rows.Scan(&nextKey); err != nil {
			continue
		}
		if nextKey == flagKey {
			return errors.New("circular prerequisite dependency detected")
		}
		if err := h.detectCycle(r, projectID, flagKey, nextKey, depth+1); err != nil {
			return err
		}
	}
	return nil
}
