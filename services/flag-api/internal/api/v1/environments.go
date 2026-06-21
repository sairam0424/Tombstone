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

type Snapshot struct {
	Environment string                 `json:"environment"`
	Flags       []FlagEnvironmentState `json:"flags"`
	Hash        string                 `json:"hash"`
	Ts          int64                  `json:"ts"`
}

// GetSnapshot handles GET /api/v1/environments/{env}/snapshot
// Used by SDKs on initialization to load full flag state into memory.
func (h *SnapshotHandler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = "production"
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT fe.flag_id, f.key, fe.environment, fe.enabled, fe.rollout_pct, f.safe_default,
		       EXTRACT(EPOCH FROM fe.updated_at)::bigint
		FROM flag_environments fe
		JOIN flags f ON f.id = fe.flag_id
		WHERE fe.environment = $1 AND f.state = 'ACTIVE'
		ORDER BY f.key
	`, env)
	if err != nil {
		h.logger.Error("snapshot query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	flags := []FlagEnvironmentState{}
	for rows.Next() {
		var s FlagEnvironmentState
		if err := rows.Scan(&s.FlagID, &s.FlagKey, &s.Environment, &s.Enabled, &s.RolloutPct, &s.SafeDefault, &s.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		flags = append(flags, s)
	}

	// Compute deterministic snapshot hash for change detection
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
