package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
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
	q := sqlcgen.New(od.db)
	rows, err := q.ListOrphanedFlags(ctx)
	if err != nil {
		od.logger.Error("orphan detector query failed", zap.Error(err))
		return
	}

	type orphan struct {
		flagKey   string
		ownerID   string
		projectID string
	}

	orphans := make([]orphan, 0, len(rows))
	for _, r := range rows {
		orphans = append(orphans, orphan{flagKey: r.Key, ownerID: r.OwnerID, projectID: r.ProjectID})
	}

	od.logger.Info("orphan detector scan complete",
		zap.Int("orphaned_flags", len(orphans)),
	)

	if len(orphans) == 0 {
		return
	}

	for _, o := range orphans {
		payload := map[string]string{
			"reason":      "orphan_detected",
			"owner_email": o.ownerID,
			"detected_at": time.Now().UTC().Format(time.RFC3339),
		}
		payloadJSON, _ := json.Marshal(payload)

		// change_requests.project_id is nullable (TEN-1a-3: legacy rows), so
		// sqlc infers this parameter as sql.NullString too -- o.projectID
		// here always came from flags.project_id, which is NOT NULL.
		err := q.CreateOrphanChangeRequest(ctx, sqlcgen.CreateOrphanChangeRequestParams{
			FlagKey:       o.flagKey,
			ChangePayload: payloadJSON,
			ProjectID:     sql.NullString{String: o.projectID, Valid: true},
		})
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
