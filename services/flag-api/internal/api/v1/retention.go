package v1

import (
	"net/http"
	"time"

	"github.com/tombstone/flag-api/internal/audit"
	"go.uber.org/zap"
)

// RetentionHandler triggers audit_log retention (DATA-2): ensuring upcoming
// monthly partitions exist, then archiving whatever has aged past the
// server-configured retention window.
type RetentionHandler struct {
	logger        *zap.Logger
	retention     *audit.Retention
	retentionDays int
}

// NewRetentionHandler builds the handler. retentionDays is read once at
// startup from AUDIT_LOG_RETENTION_DAYS (see cmd/main.go), not per-request.
func NewRetentionHandler(logger *zap.Logger, retention *audit.Retention, retentionDays int) *RetentionHandler {
	return &RetentionHandler{logger: logger, retention: retention, retentionDays: retentionDays}
}

// RunRetention handles POST /api/v1/audit/retention/run.
//
// The retention window is server-configured, not caller-supplied: this
// endpoint takes no body and no query parameter that could widen it. A
// compromised or simply buggy caller (e.g. the audit-retention loop script)
// can therefore trigger a run, but can never make one run archive more
// aggressively than this server's own AUDIT_LOG_RETENTION_DAYS allows.
func (h *RetentionHandler) RunRetention(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().UTC()

	// Ahead of archiving, so a row written today always lands in a real
	// monthly partition rather than the DEFAULT catch-all, which is never a
	// candidate for archiving by month.
	if err := h.retention.EnsurePartitions(ctx, now, 3); err != nil {
		h.logger.Error("ensure audit_log partitions", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to ensure upcoming partitions")
		return
	}

	cutoff := now.AddDate(0, 0, -h.retentionDays)
	report, err := h.retention.Archive(ctx, cutoff)
	if err != nil {
		h.logger.Error("archive audit_log partitions", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "archive failed")
		return
	}
	writeJSON(w, http.StatusOK, report)
}
