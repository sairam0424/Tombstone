package v1

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type SnapshotHandler struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewSnapshotHandler(db *sql.DB, logger *zap.Logger) *SnapshotHandler {
	return &SnapshotHandler{db: db, logger: logger}
}

// SnapshotPrerequisite is a lightweight prerequisite record embedded in the snapshot.
// SDKs use this to evaluate prerequisite gates in-process without extra API calls.
type SnapshotPrerequisite struct {
	ID                string `json:"id"`
	PrereqFlagKey     string `json:"prereq_flag_key"`
	RequiredVariation string `json:"required_variation"`
	Gate              bool   `json:"gate"`
	Priority          int    `json:"priority"`
}

// FlagEnvironmentStateWithPrereqs extends FlagEnvironmentState with the prerequisites
// slice required for in-process evaluation.
type FlagEnvironmentStateWithPrereqs struct {
	FlagID        string                 `json:"flag_id"`
	FlagKey       string                 `json:"flag_key"`
	Environment   string                 `json:"environment"`
	Enabled       bool                   `json:"enabled"`
	RolloutPct    int                    `json:"rollout_pct"`
	SafeDefault   string                 `json:"safe_default"`
	UpdatedAt     int64                  `json:"updated_at"`
	Prerequisites []SnapshotPrerequisite `json:"prerequisites"`
}

type Snapshot struct {
	Environment string                            `json:"environment"`
	Flags       []FlagEnvironmentStateWithPrereqs `json:"flags"`
	Hash        string                            `json:"hash"`
	Ts          int64                             `json:"ts"`
}

// GetSnapshot handles GET /api/v1/environments/{env}/snapshot
// Used by SDKs on initialization to load full flag state into memory.
// Each flag entry includes its prerequisites array so SDKs can evaluate
// prerequisite gates in-process without additional API round-trips.
//
// TEN-1a: this is the primary SDK hot path — before this fix, NEITHER query
// below filtered by project at all, so any service token (any project) could
// fetch every OTHER project's complete flag configuration for a given
// environment name just by calling this endpoint. It is now scoped to the
// caller's own resolved project.
func (h *SnapshotHandler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = "production"
	}

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT fe.flag_id, f.key, fe.environment, fe.enabled, fe.rollout_pct, f.safe_default,
		       EXTRACT(EPOCH FROM fe.updated_at)::bigint
		FROM flag_environments fe
		JOIN flags f ON f.id = fe.flag_id
		WHERE fe.environment = $1 AND f.state = 'ACTIVE' AND f.project_id = $2
		ORDER BY f.key
	`, env, projectID)
	if err != nil {
		h.logger.Error("snapshot query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	// Build index: flag_id -> FlagEnvironmentStateWithPrereqs
	// We use a slice to preserve ORDER BY f.key ordering.
	flags := []FlagEnvironmentStateWithPrereqs{}
	flagIndexByID := map[string]int{} // flag_id -> slice index

	for rows.Next() {
		var s FlagEnvironmentStateWithPrereqs
		s.Prerequisites = []SnapshotPrerequisite{} // always a JSON array, never null
		if err := rows.Scan(&s.FlagID, &s.FlagKey, &s.Environment, &s.Enabled,
			&s.RolloutPct, &s.SafeDefault, &s.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		flagIndexByID[s.FlagID] = len(flags)
		flags = append(flags, s)
	}

	// Load all prerequisites for flags in this environment in a single query
	// and attach them to the corresponding flag entries.
	prereqRows, err := h.db.QueryContext(r.Context(), `
		SELECT fp.flag_id, fp.id, fp.prereq_flag_key, fp.required_variation, fp.gate, fp.priority
		FROM flag_prerequisites fp
		JOIN flag_environments fe ON fe.flag_id = fp.flag_id
		JOIN flags f ON f.id = fp.flag_id
		WHERE fe.environment = $1 AND f.state = 'ACTIVE' AND f.project_id = $2
		ORDER BY fp.flag_id, fp.priority ASC, fp.created_at ASC
	`, env, projectID)
	if err != nil {
		// Non-fatal: return snapshot without prerequisites rather than fail.
		h.logger.Warn("prerequisites query failed; returning snapshot without prerequisites",
			zap.Error(err))
	} else {
		defer prereqRows.Close()
		for prereqRows.Next() {
			var flagID string
			var p SnapshotPrerequisite
			if err := prereqRows.Scan(&flagID, &p.ID, &p.PrereqFlagKey,
				&p.RequiredVariation, &p.Gate, &p.Priority); err != nil {
				continue
			}
			if idx, ok := flagIndexByID[flagID]; ok {
				flags[idx].Prerequisites = append(flags[idx].Prerequisites, p)
			}
		}
	}

	// Compute deterministic snapshot hash for change detection.
	// Hash covers the full state including prerequisites.
	raw, _ := json.Marshal(flags)
	hashBytes := sha256.Sum256(raw)
	hash := fmt.Sprintf("%x", hashBytes)

	snap := Snapshot{
		Environment: env,
		Flags:       flags,
		Hash:        hash,
		Ts:          time.Now().Unix(),
	}
	writeJSON(w, http.StatusOK, snap)
}
