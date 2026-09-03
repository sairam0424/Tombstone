// Package scheduler provides a background goroutine that executes pending
// scheduled flag changes once their scheduled_for timestamp is reached.
//
// Design:
//   - Ticks every 30 seconds.
//   - Selects all due rows: PENDING rows WHERE scheduled_for <= NOW(), PLUS
//     FAILED rows that still have retries remaining AND whose next_retry_at
//     has passed (see the retry/backoff section below).
//   - For each due change: updates flag_environments, marks the row EXECUTED,
//     writes an audit log entry, and publishes a Redis event.
//   - On per-change errors: increments retry_count and marks the row FAILED
//     with an error_message; if retries remain, schedules a future retry via
//     next_retry_at (exponential backoff — see markFailed/backoffDuration).
//     Once retries are exhausted, the row is permanently terminal, matching
//     the pre-retry behavior: a human must recreate it via the API.
//   - Respects context cancellation for graceful shutdown.
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
)

const tickInterval = 30 * time.Second

// Start launches the background scheduler goroutine.
// It blocks until ctx is cancelled (intended to be called as: go scheduler.Start(ctx, db, rdb, logger)).
func Start(ctx context.Context, db *sql.DB, rdb *redis.Client, logger *zap.Logger, auditW *audit.Writer) {
	logger.Info("scheduled-change executor starting", zap.Duration("interval", tickInterval))
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("scheduled-change executor stopped")
			return
		case <-ticker.C:
			runDue(ctx, db, rdb, logger, auditW)
		}
	}
}

// pendingChange is a row fetched from scheduled_changes.
type pendingChange struct {
	id            string
	flagKey       string
	environment   string
	changePayload []byte
	// projectID is "" for rows written before migration 016 (legacy rows —
	// see that migration's comment for why they are never backfilled).
	// applyChange refuses to execute a row with no project_id rather than
	// falling back to a project-blind match.
	projectID string
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

// runDue fetches and executes all due changes in one tick: fresh PENDING rows
// plus previously-FAILED rows that still have retry budget and whose
// next_retry_at backoff window has elapsed.
//
// FOR UPDATE SKIP LOCKED ensures that when flag-api runs as multiple replicas
// each instance claims a disjoint batch of rows — preventing duplicate audit
// entries and duplicate Redis events from concurrent execution of the same row.
func runDue(ctx context.Context, db *sql.DB, rdb *redis.Client, logger *zap.Logger, auditW *audit.Writer) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		logger.Error("scheduler: begin transaction failed", zap.Error(err))
		return
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := sqlcgen.New(tx).SelectDueScheduledChanges(ctx)
	if err != nil {
		logger.Error("scheduler: query pending changes failed", zap.Error(err))
		return
	}

	due := make([]pendingChange, 0, len(rows))
	for _, r := range rows {
		due = append(due, pendingChange{
			id:            r.ID,
			flagKey:       r.FlagKey,
			environment:   r.Environment,
			changePayload: r.ChangePayload,
			// project_id is nullable (legacy pre-migration-016 rows are left
			// NULL, never backfilled) -- sql.NullString.String is "" when
			// NULL, matching this field's "" == "no project" convention.
			// applyChange's own check refuses to execute such a row.
			projectID: r.ProjectID.String,
		})
	}
	// Commit the lock-acquisition transaction before executing changes
	// (each applyChange opens its own sub-operation).
	if err := tx.Commit(); err != nil {
		logger.Error("scheduler: commit lock-acquisition transaction failed", zap.Error(err))
		return
	}

	for _, pc := range due {
		applyChange(ctx, db, rdb, logger, auditW, pc)
	}
}

// applyChange executes a single scheduled change.
// On success: updates flag_environments, marks EXECUTED, writes audit + Redis event.
// On failure: marks FAILED with error_message, logs the error, and returns (does not panic).
func applyChange(ctx context.Context, db *sql.DB, rdb *redis.Client, logger *zap.Logger, auditW *audit.Writer, pc pendingChange) {
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

	// TEN-1a: a row with no project_id (written before migration 016) cannot
	// be matched to a flag safely — flags.key is unique only per
	// (project_id, key), so matching by key alone risks mutating a DIFFERENT
	// project's same-keyed flag. Refuse rather than guess.
	if pc.projectID == "" {
		markFailed(ctx, db, log, pc.id, "scheduled change has no project_id (legacy row) — cannot execute safely")
		return
	}
	projectID := pc.projectID

	// Read current flag environment state for audit and to fill in unchanged fields.
	q := sqlcgen.New(db)
	state, err := q.GetCurrentFlagEnvironmentState(ctx, sqlcgen.GetCurrentFlagEnvironmentStateParams{
		Key: pc.flagKey, Environment: pc.environment, ProjectID: projectID,
	})
	if err == sql.ErrNoRows {
		markFailed(ctx, db, log, pc.id,
			fmt.Sprintf("flag %q environment %q not found", pc.flagKey, pc.environment))
		return
	}
	if err != nil {
		markFailed(ctx, db, log, pc.id, "read current state failed: "+err.Error())
		return
	}
	curEnabled, curRollout := state.Enabled, int(state.RolloutPct)

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
	n, err := q.ApplyScheduledFlagEnvironmentUpdate(ctx, sqlcgen.ApplyScheduledFlagEnvironmentUpdateParams{
		Enabled:     newEnabled,
		RolloutPct:  int32(newRollout),
		Key:         pc.flagKey,
		Environment: pc.environment,
		ProjectID:   projectID,
	})
	if err != nil {
		markFailed(ctx, db, log, pc.id, "flag environment update failed: "+err.Error())
		return
	}
	if n == 0 {
		markFailed(ctx, db, log, pc.id,
			fmt.Sprintf("flag %q environment %q not found during update", pc.flagKey, pc.environment))
		return
	}

	// Mark EXECUTED.
	if err := q.MarkScheduledChangeExecuted(ctx, pc.id); err != nil {
		// The flag update already happened — log but don't fail the whole pipeline.
		log.Error("scheduler: failed to mark EXECUTED (flag was updated)", zap.Error(err))
	}

	// Write audit log entry.
	prev := map[string]any{"enabled": curEnabled, "rollout_pct": curRollout}
	curr := map[string]any{"enabled": newEnabled, "rollout_pct": newRollout, "scheduled_change_id": pc.id}
	writeAudit(ctx, auditW, log, projectID, pc.flagKey, pc.environment, "scheduler",
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

// baseRetryDelay and maxRetryDelay define the exponential backoff envelope
// for retried scheduled changes. Chosen to be consistent in shape with
// Phase 2's resilient HTTP client (internal/httpclient.DefaultConfig):
// bounded exponential growth with a hard cap. The absolute values differ
// because this retries a DB-driven background job on a ~30s poll tick, not
// a synchronous inter-service HTTP call — 200ms/5s (Phase 2's HTTP values)
// would be pointless here since the poll loop itself only ticks every 30s.
// Instead we pick delays that are multiples of the poll interval:
//
//	attempt 1 (retry_count 0->1): 1 minute  (2 poll ticks)
//	attempt 2 (retry_count 1->2): 2 minutes (4 poll ticks)
//	attempt 3 (retry_count 2->3): 4 minutes (8 poll ticks) -- exhausts default max_retries=3
//
// Formula: delay = baseRetryDelay * 2^(retryCount-1), capped at maxRetryDelay.
const (
	baseRetryDelay = 1 * time.Minute
	maxRetryDelay  = 4 * time.Minute
)

// backoffDuration returns the exponential backoff delay to apply after the
// Nth failure (retryCount is the POST-increment retry count, i.e. 1 for the
// first failure). Doubles each attempt starting from baseRetryDelay, capped
// at maxRetryDelay so a misconfigured max_retries can't produce runaway
// delays.
func backoffDuration(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	delay := baseRetryDelay << uint(retryCount-1) // baseRetryDelay * 2^(retryCount-1)
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay
}

// markFailed records a per-change failure and decides whether the row gets
// another chance or becomes permanently terminal.
//
// Retryable vs. permanent errors: this codebase currently treats ALL failure
// branches identically (invalid JSON payload, missing enabled/rollout_pct,
// flag/environment not found, DB read error, DB update error, zero rows
// affected) via the same bounded retry-count mechanism, rather than
// fast-failing "will never succeed" errors like "flag/environment not
// found" straight to terminal FAILED.
//
// This is a deliberate simplification, not an oversight: with the default
// max_retries=3 and the backoff schedule above, a doomed-to-fail change
// costs at most ~7 minutes (1+2+4) of extra poll cycles before reaching the
// same terminal FAILED state it would have hit immediately under fast-fail.
// That's cheap compared to the alternative — hand-classifying every current
// and future error branch as "transient" vs "permanent" and keeping that
// classification correct as new failure modes are added to applyChange. If
// a genuinely expensive or side-effecting failure mode is added later (e.g.
// one that pages someone on every attempt), revisit this and fast-fail it
// explicitly instead of changing the default here.
func markFailed(ctx context.Context, db *sql.DB, log *zap.Logger, id, errMsg string) {
	log.Error("scheduler: change failed", zap.String("error", errMsg))

	q := sqlcgen.New(db)
	retryState, err := q.GetScheduledChangeRetryState(ctx, id)
	if err != nil {
		log.Error("scheduler: failed to read retry state, marking terminal FAILED", zap.Error(err))
		if execErr := q.MarkScheduledChangeFailedNoRetryState(ctx, sqlcgen.MarkScheduledChangeFailedNoRetryStateParams{
			ErrorMessage: sql.NullString{String: errMsg, Valid: true}, ID: id,
		}); execErr != nil {
			log.Error("scheduler: failed to mark FAILED", zap.Error(execErr))
		}
		return
	}
	retryCount, maxRetries := int(retryState.RetryCount), int(retryState.MaxRetries)

	retryCount++

	if retryCount < maxRetries {
		// Retries remain: FAILED is a temporary holding state — the row is
		// excluded from THIS poll cycle but becomes eligible again once
		// next_retry_at passes (see the WHERE clause in runDue).
		nextRetryAt := time.Now().Add(backoffDuration(retryCount))
		log.Warn("scheduler: change failed, will retry",
			zap.Int("retry_count", retryCount),
			zap.Int("max_retries", maxRetries),
			zap.Time("next_retry_at", nextRetryAt))

		if err := q.MarkScheduledChangeFailedRetryPending(ctx, sqlcgen.MarkScheduledChangeFailedRetryPendingParams{
			ErrorMessage: sql.NullString{String: errMsg, Valid: true},
			RetryCount:   int32(retryCount),
			NextRetryAt:  sql.NullTime{Time: nextRetryAt, Valid: true},
			ID:           id,
		}); err != nil {
			log.Error("scheduler: failed to mark FAILED (retry pending)", zap.Error(err))
		}
		return
	}

	// Retry budget exhausted: permanently terminal, matching pre-retry
	// behavior. Leave next_retry_at NULL — the runDue WHERE clause only
	// re-selects rows with retry_count < max_retries, so this row will never
	// be picked up again regardless of next_retry_at, but leaving it unset
	// makes the terminal state unambiguous to a human inspecting the row.
	log.Error("scheduler: retry budget exhausted, permanently FAILED",
		zap.Int("retry_count", retryCount),
		zap.Int("max_retries", maxRetries))

	if err := q.MarkScheduledChangeFailedTerminal(ctx, sqlcgen.MarkScheduledChangeFailedTerminalParams{
		ErrorMessage: sql.NullString{String: errMsg, Valid: true},
		RetryCount:   int32(retryCount),
		ID:           id,
	}); err != nil {
		log.Error("scheduler: failed to mark FAILED (terminal)", zap.Error(err))
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
func writeAudit(ctx context.Context, auditW *audit.Writer, log *zap.Logger,
	projectID, flagKey, env, actor, eventType string, prev, curr any) {

	prevJSON, _ := json.Marshal(prev)
	currJSON, _ := json.Marshal(curr)

	// AUD-1: this function used to hand-duplicate the six-field pipe-joined
	// formula from api/v1 (it could not import the unexported helper across the
	// package boundary), and did an unlocked SELECT-last-then-INSERT. Both the
	// duplication and the fork race are gone — audit.Writer is the only writer.
	if auditW == nil {
		log.Warn("scheduler: audit log write skipped — no audit writer configured")
		return
	}
	if _, _, err := auditW.Append(ctx, audit.Entry{
		FlagKey:     flagKey,
		Environment: env,
		Actor:       actor,
		EventType:   eventType,
		PrevState:   prevJSON,
		NewState:    currJSON,
		ProjectID:   projectID,
	}); err != nil {
		log.Warn("scheduler: audit log write failed", zap.Error(err))
	}
}
