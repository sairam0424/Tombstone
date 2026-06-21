package v1

import (
	"database/sql"
	"net/http"

	"go.uber.org/zap"
)

type GovernanceHandler struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewGovernanceHandler(db *sql.DB, logger *zap.Logger) *GovernanceHandler {
	return &GovernanceHandler{db: db, logger: logger}
}

// GetProjectHealth handles GET /api/v1/projects/{id}/health
func (h *GovernanceHandler) GetProjectHealth(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		projectID = "00000000-0000-0000-0000-000000000001"
	}

	type healthReport struct {
		TotalFlags     int     `json:"total_flags"`
		ActiveFlags    int     `json:"active_flags"`
		StaleFlags     int     `json:"stale_flags"`
		ArchivedFlags  int     `json:"archived_flags"`
		InventoryUsed  int     `json:"inventory_used"`
		InventoryLimit int     `json:"inventory_limit"`
		HealthScore    float64 `json:"health_score"`
	}
	var h2 healthReport

	_ = h.db.QueryRowContext(r.Context(), `
		SELECT
		    COUNT(*) FILTER (WHERE state != 'ARCHIVED') AS total,
		    COUNT(*) FILTER (WHERE state = 'ACTIVE') AS active,
		    COUNT(*) FILTER (WHERE state = 'ARCHIVED') AS archived,
		    COUNT(*) FILTER (
		        WHERE state = 'ACTIVE'
		          AND id IN (
		              SELECT flag_id FROM flag_environments
		              WHERE environment = 'production'
		                AND rollout_pct = 100
		                AND updated_at < now() - INTERVAL '30 days'
		          )
		    ) AS stale
		FROM flags WHERE project_id = $1
	`, projectID).Scan(&h2.TotalFlags, &h2.ActiveFlags, &h2.ArchivedFlags, &h2.StaleFlags)

	_ = h.db.QueryRowContext(r.Context(), `
		SELECT current_count, max_flags FROM inventory_limits WHERE project_id = $1
	`, projectID).Scan(&h2.InventoryUsed, &h2.InventoryLimit)

	if h2.TotalFlags > 0 {
		h2.HealthScore = 1.0 - float64(h2.StaleFlags)/float64(h2.TotalFlags)
	} else {
		h2.HealthScore = 1.0
	}

	writeJSON(w, http.StatusOK, h2)
}

// CheckInventoryLimit returns 429 if the project is at or above its flag limit.
// Call this middleware before CreateFlag.
func (h *GovernanceHandler) CheckInventoryLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectID := r.URL.Query().Get("project_id")
		if projectID == "" {
			projectID = "00000000-0000-0000-0000-000000000001"
		}

		var current, max int
		err := h.db.QueryRowContext(r.Context(), `
			SELECT current_count, max_flags FROM inventory_limits WHERE project_id = $1
		`, projectID).Scan(&current, &max)
		if err == nil && current >= max {
			h.logger.Warn("inventory limit reached",
				zap.String("project_id", projectID),
				zap.Int("current", current),
				zap.Int("max", max))
			writeError(w, http.StatusTooManyRequests,
				"flag inventory limit reached — archive stale flags before creating new ones")
			return
		}
		next.ServeHTTP(w, r)
	})
}
