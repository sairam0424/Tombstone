package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"go.uber.org/zap"
)

// OrphanDetector runs a periodic background scan for ACTIVE flags whose
// owner_id no longer corresponds to an active SCIM-provisioned user.
// Any orphaned flags result in PENDING change_requests to prompt remediation.
type OrphanDetector struct {
	db     *sql.DB
	logger *zap.Logger
}

// NewOrphanDetector constructs an OrphanDetector.
func NewOrphanDetector(db *sql.DB, logger *zap.Logger) *OrphanDetector {
	return &OrphanDetector{db: db, logger: logger}
}

// Run loops every 24 hours and calls detectAndReport. It returns when ctx is
// cancelled (e.g. on graceful shutdown).
func (od *OrphanDetector) Run(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// Run immediately on startup so we don't wait 24 h for the first report.
	od.detectAndReport(ctx)

	for {
		select {
		case <-ctx.Done():
			od.logger.Info("orphan detector stopped")
			return
		case <-ticker.C:
			od.detectAndReport(ctx)
		}
	}
}

// detectAndReport queries for ACTIVE flags whose owner_id is not present in
// the active scim_users set, logs the count, and creates a PENDING
// change_request for each orphaned flag.
func (od *OrphanDetector) detectAndReport(ctx context.Context) {
	rows, err := od.db.QueryContext(ctx, `
		SELECT f.key, f.owner_id
		FROM flags f
		WHERE f.state = 'ACTIVE'
		  AND NOT EXISTS (
		      SELECT 1 FROM scim_users su
		      WHERE su.email = f.owner_id
		        AND su.active = true
		  )
		ORDER BY f.key
	`)
	if err != nil {
		od.logger.Error("orphan detector query failed", zap.Error(err))
		return
	}
	defer func() { _ = rows.Close() }()

	type orphan struct {
		flagKey string
		ownerID string
	}

	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.flagKey, &o.ownerID); err != nil {
			od.logger.Error("orphan detector scan", zap.Error(err))
			continue
		}
		orphans = append(orphans, o)
	}

	od.logger.Info("orphan detector scan complete",
		zap.Int("orphaned_flags", len(orphans)),
	)

	if len(orphans) == 0 {
		return
	}

	for _, o := range orphans {
		payload := map[string]string{
			"reason":     "orphan_detected",
			"owner_email": o.ownerID,
			"detected_at": time.Now().UTC().Format(time.RFC3339),
		}
		payloadJSON, _ := json.Marshal(payload)

		_, err := od.db.ExecContext(ctx, `
			INSERT INTO change_requests
			    (flag_key, environment, requested_by, status, change_payload)
			VALUES ($1, 'production', 'system-orphan-detector', 'PENDING', $2)
		`, o.flagKey, payloadJSON)
		if err != nil {
			od.logger.Error("orphan detector create change_request",
				zap.Error(err),
				zap.String("flag_key", o.flagKey),
				zap.String("owner", o.ownerID),
			)
			continue
		}

		od.logger.Warn("orphan flag change_request created",
			zap.String("flag_key", o.flagKey),
			zap.String("owner", o.ownerID),
		)
	}
}
