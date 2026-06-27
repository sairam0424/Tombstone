// Package scheduler provides a background goroutine that executes pending
// scheduled flag changes once their scheduled_for timestamp is reached.
//
// Design:
//   - Ticks every 30 seconds.
//   - Selects all PENDING rows WHERE scheduled_for <= NOW().
//   - For each due change: updates flag_environments, marks the row EXECUTED,
//     writes an audit log entry, and publishes a Redis event.
//   - On per-change errors: marks the row FAILED with an error_message, logs
//     the error, and continues processing the remaining changes (best-effort).
//   - Respects context cancellation for graceful shutdown.
package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const tickInterval = 30 * time.Second

// Start launches the background scheduler goroutine.
// It blocks until ctx is cancelled (intended to be called as: go scheduler.Start(ctx, db, rdb, logger)).
func Start(ctx context.Context, db *sql.DB, rdb *redis.Client, logger *zap.Logger) {
	logger.Info("scheduled-change executor starting", zap.Duration("interval", tickInterval))
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("scheduled-change executor stopped")
			return
		case <-ticker.C:
			runDue(ctx, db, rdb, logger)
		}
	}
}

// pendingChange is a row fetched from scheduled_changes.
type pendingChange struct {
	id            string
	flagKey       string
	environment   string
	changePayload []byte
}

// changePayloadFields mirrors the API struct — enabled and/or rollout_pct.
type changePayloadFields struct {
	Enabled    *bool `json:"enabled"`
	RolloutPct *int  `json:"rollout_pct"`
}

// flagEvent is the Redis pub/sub message format (matches v1.FlagEvent).
type flagEvent struct {
	FlagKey     string `json:"flag_key"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
	Reason      string `json:"reason"`
	Ts          int64  `json:"ts"`
	Environment string `json:"environment"`
}

// runDue fetches and executes all due pending changes in one tick.
func runDue(ctx context.Context, db *sql.DB, rdb *redis.Client, logger *zap.Logger) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, flag_key, environment, change_payload
		FROM scheduled_changes
		WHERE status = 'PENDING' AND scheduled_for <= NOW()
		ORDER BY scheduled_for ASC
	`)
	if err != nil {
		logger.Error("scheduler: query pending changes failed", zap.Error(err))
		return
	}
	defer func() { _ = rows.Close() }()

	var due []pendingChange
	for rows.Next() {
		var pc pendingChange
		if err := rows.Scan(&pc.id, &pc.flagKey, &pc.environment, &pc.changePayload); err != nil {
			logger.Error("scheduler: scan row failed", zap.Error(err))
			continue
		}
		due = append(due, pc)
	}
	if err := rows.Err(); err != nil {
		logger.Error("scheduler: rows iteration error", zap.Error(err))
		return
	}

	for _, pc := range due {
		applyChange(ctx, db, rdb, logger, pc)
	}
}

// applyChange executes a single scheduled change.
// On success: updates flag_environments, marks EXECUTED, writes audit + Redis event.
// On failure: marks FAILED with error_message, logs the error, and returns (does not panic).
func applyChange(ctx context.Context, db *sql.DB, rdb *redis.Client, logger *zap.Logger, pc pendingChange) {
	log := logger.With(
		zap.String("scheduled_change_id", pc.id),
		zap.String("flag_key", pc.flagKey),
		zap.String("environment", pc.environment),
	)

	var payload changePayloadFields
	if err := json.Unmarshal(pc.changePayload, &payload); err != nil {
		markFailed(ctx, db, log, pc.id, "invalid change_payload JSON: "+err.Error())
		return
	}
	if payload.Enabled == nil && payload.RolloutPct == nil {
		markFailed(ctx, db, log, pc.id, "change_payload has neither enabled nor rollout_pct")
		return
	}

	// Read current flag environment state for audit and to fill in unchanged fields.
	var curEnabled bool
	var curRollout int
	var flagID string
	err := db.QueryRowContext(ctx, `
		SELECT fe.enabled, fe.rollout_pct, fe.flag_id
		FROM flag_environments fe
		JOIN flags f ON f.id = fe.flag_id
		WHERE f.key = $1 AND fe.environment = $2
	`, pc.flagKey, pc.environment).Scan(&curEnabled, &curRollout, &flagID)
	if err == sql.ErrNoRows {
		markFailed(ctx, db, log, pc.id,
			fmt.Sprintf("flag %q environment %q not found", pc.flagKey, pc.environment))
		return
	}
	if err != nil {
		markFailed(ctx, db, log, pc.id, "read current state failed: "+err.Error())
		return
	}

	// Merge: apply only the fields present in the payload.
	newEnabled := curEnabled
	newRollout := curRollout
	if payload.Enabled != nil {
		newEnabled = *payload.Enabled
	}
	if payload.RolloutPct != nil {
		newRollout = *payload.RolloutPct
	}

	// Apply the flag environment update (same logic as handlers.UpdateEnvironment).
	res, err := db.ExecContext(ctx, `
		UPDATE flag_environments fe
		SET enabled = $1, rollout_pct = $2, updated_at = now(), updated_by = 'scheduler'
		FROM flags f
		WHERE f.id = fe.flag_id AND f.key = $3 AND fe.environment = $4
	`, newEnabled, newRollout, pc.flagKey, pc.environment)
	if err != nil {
		markFailed(ctx, db, log, pc.id, "flag environment update failed: "+err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		markFailed(ctx, db, log, pc.id,
			fmt.Sprintf("flag %q environment %q not found during update", pc.flagKey, pc.environment))
		return
	}

	// Mark EXECUTED.
	if _, err := db.ExecContext(ctx, `
		UPDATE scheduled_changes
		SET status = 'EXECUTED', executed_at = NOW()
		WHERE id = $1
	`, pc.id); err != nil {
		// The flag update already happened — log but don't fail the whole pipeline.
		log.Error("scheduler: failed to mark EXECUTED (flag was updated)", zap.Error(err))
	}

	// Write audit log entry.
	prev := map[string]any{"enabled": curEnabled, "rollout_pct": curRollout}
	curr := map[string]any{"enabled": newEnabled, "rollout_pct": newRollout, "scheduled_change_id": pc.id}
	writeAudit(ctx, db, log, pc.flagKey, pc.environment, "scheduler",
		"scheduled_change_applied", prev, curr)

	// Publish Redis event on the same channel as manual updates.
	event := flagEvent{
		FlagKey:     pc.flagKey,
		Enabled:     newEnabled,
		RolloutPct:  newRollout,
		Reason:      "scheduled",
		Ts:          time.Now().Unix(),
		Environment: pc.environment,
	}
	publishEvent(ctx, rdb, log, pc.environment, event)
	publishToStream(ctx, rdb, log, pc.environment, event)

	log.Info("scheduler: applied scheduled change",
		zap.Bool("enabled", newEnabled),
		zap.Int("rollout_pct", newRollout),
	)
}

// markFailed sets a scheduled change to FAILED with a descriptive error message.
func markFailed(ctx context.Context, db *sql.DB, log *zap.Logger, id, errMsg string) {
	log.Error("scheduler: change failed", zap.String("error", errMsg))
	if _, err := db.ExecContext(ctx, `
		UPDATE scheduled_changes
		SET status = 'FAILED', error_message = $1
		WHERE id = $2
	`, errMsg, id); err != nil {
		log.Error("scheduler: failed to mark FAILED", zap.Error(err))
	}
}

// publishEvent publishes a flag update event to Redis pub/sub.
// Channel format matches v1.FlagHandler.publishEvent: "stream:{env}:updates".
func publishEvent(ctx context.Context, rdb *redis.Client, log *zap.Logger, environment string, event flagEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		log.Warn("scheduler: failed to marshal event", zap.Error(err))
		return
	}
	channel := fmt.Sprintf("stream:%s:updates", environment)
	if err := rdb.Publish(ctx, channel, payload).Err(); err != nil {
		log.Warn("scheduler: redis publish failed", zap.Error(err), zap.String("channel", channel))
	}
}

// publishToStream publishes a flag update event to a Redis Stream (XADD).
// Runs alongside publishEvent for one release cycle (legacy pub/sub removed in v2.1).
// Stream key: tombstone:stream:{environment}, MaxLen: 10000 (approximate trim).
func publishToStream(ctx context.Context, rdb *redis.Client, log *zap.Logger, environment string, event flagEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		log.Warn("scheduler: failed to marshal event for stream", zap.Error(err))
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
		log.Warn("scheduler: redis xadd failed", zap.Error(err), zap.String("stream", streamKey))
	}
}

// writeAudit inserts an append-only audit log entry with Merkle hash linking.
// Mirrors the logic in v1.FlagHandler.writeAudit.
func writeAudit(ctx context.Context, db *sql.DB, log *zap.Logger,
	flagKey, env, actor, eventType string, prev, curr any) {

	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

	var lastID, lastTs string
	_ = db.QueryRowContext(ctx, `
		SELECT id, EXTRACT(EPOCH FROM created_at)::text
		FROM audit_log
		WHERE flag_key = $1
		ORDER BY created_at DESC LIMIT 1
	`, flagKey).Scan(&lastID, &lastTs)

	prevHash := ""
	if lastID != "" {
		hashBytes := sha256.Sum256([]byte(lastID + lastTs))
		prevHash = fmt.Sprintf("%x", hashBytes)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_log
		    (id, flag_key, environment, actor, event_type, prev_state, new_state, ip_address, prev_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8)
	`, uuid.New().String(), flagKey, env, actor, eventType, prevJSON, currJSON, prevHash); err != nil {
		log.Warn("scheduler: audit log write failed", zap.Error(err))
	}
}
