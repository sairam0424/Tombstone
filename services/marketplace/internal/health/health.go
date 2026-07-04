package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// Checker performs bounded-timeout dependency checks for readiness probing.
// DB or RDB may be nil if the service has no such dependency, or if it is
// optional and currently unconfigured — a nil field is treated as healthy,
// not as a failure, so an optional-and-absent dependency never fails readiness.
type Checker struct {
	DB  *sql.DB
	RDB *redis.Client
}

const pingTimeout = 3 * time.Second

// Livez is an unconditional 200 — matches the existing /health contract exactly.
// Wire this at whatever path already serves /health if you want to reuse it,
// or mount it fresh; either way /health's current behavior must not change.
func (c *Checker) Livez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Readyz pings every configured dependency with a bounded timeout and
// returns 503 if any fail. New endpoint — mount at /readyz.
func (c *Checker) Readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()

	checks := map[string]string{}
	healthy := true

	if c.DB != nil {
		if err := c.DB.PingContext(ctx); err != nil {
			checks["postgres"] = "unreachable: " + err.Error()
			healthy = false
		} else {
			checks["postgres"] = "ok"
		}
	}
	if c.RDB != nil {
		if err := c.RDB.Ping(ctx).Err(); err != nil {
			checks["redis"] = "unreachable: " + err.Error()
			healthy = false
		} else {
			checks["redis"] = "ok"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": map[bool]string{true: "ready", false: "not_ready"}[healthy],
		"checks": checks,
	})
}
