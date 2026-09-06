package v1

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
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
//
// FlagKey's wire tag is "flag_key", matching proto/v1/flags/flags.proto's
// ParentCondition message and every SDK's FlagPrerequisite type -- NOT
// "prereq_flag_key" (that's only flag_prerequisites' own DB column name).
// Before this fix this struct's wire tag was "prereq_flag_key", silently
// diverging from the proto contract every SDK was written against: every
// SDK's prerequisite dependency lookup read the wrong key against a real
// snapshot response (found while investigating SDK-4's prerequisites-
// streaming follow-up).
type SnapshotPrerequisite struct {
	ID                string `json:"id"`
	FlagKey           string `json:"flag_key"`
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

	q := sqlcgen.New(h.db)
	snapRows, err := q.GetEnvironmentSnapshot(r.Context(), sqlcgen.GetEnvironmentSnapshotParams{
		Environment: env,
		ProjectID:   projectID,
	})
	if err != nil {
		h.logger.Error("snapshot query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// Build index: flag_id -> FlagEnvironmentStateWithPrereqs
	// We use a slice to preserve ORDER BY f.key ordering.
	flags := []FlagEnvironmentStateWithPrereqs{}
	flagIndexByID := map[string]int{} // flag_id -> slice index

	for _, row := range snapRows {
		s := FlagEnvironmentStateWithPrereqs{
			FlagID:        row.FlagID,
			FlagKey:       row.Key,
			Environment:   row.Environment,
			Enabled:       row.Enabled,
			RolloutPct:    int(row.RolloutPct),
			SafeDefault:   row.SafeDefault,
			UpdatedAt:     row.UpdatedAt,
			Prerequisites: []SnapshotPrerequisite{}, // always a JSON array, never null
		}
		flagIndexByID[s.FlagID] = len(flags)
		flags = append(flags, s)
	}

	// Load all prerequisites for flags in this environment in a single query
	// and attach them to the corresponding flag entries.
	prereqRows, err := q.GetEnvironmentSnapshotPrerequisites(r.Context(), sqlcgen.GetEnvironmentSnapshotPrerequisitesParams{
		Environment: env,
		ProjectID:   projectID,
	})
	if err != nil {
		// Non-fatal: return snapshot without prerequisites rather than fail.
		h.logger.Warn("prerequisites query failed; returning snapshot without prerequisites",
			zap.Error(err))
	} else {
		for _, pr := range prereqRows {
			p := SnapshotPrerequisite{
				ID:                pr.ID,
				FlagKey:           pr.PrereqFlagKey,
				RequiredVariation: pr.RequiredVariation,
				Gate:              pr.Gate,
				Priority:          int(pr.Priority),
			}
			if idx, ok := flagIndexByID[pr.FlagID]; ok {
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
