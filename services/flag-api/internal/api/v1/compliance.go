package v1

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/db/sqlcgen"
	"github.com/tombstone/flag-api/internal/secrets"
)

// ComplianceHandler serves SOC 2 evidence and control documentation.
type ComplianceHandler struct {
	db     *sql.DB
	logger *zap.Logger
	// signer signs audit exports with a key DISTINCT from JWT_SECRET (SEC-4).
	// nil means no dedicated key is configured; export then fails closed rather
	// than falling back to the JWT signing key.
	signer *secrets.ComplianceSigner
	// audit recomputes real chain integrity (AUD-1) instead of asserting it.
	audit *audit.Writer
	// policySource reports the live authorization source ("opa" or
	// "fallback_matrix"). A func, not a value, because OPA policies hot-reload.
	policySource func() string
}

// NewComplianceHandler constructs a ComplianceHandler.
func NewComplianceHandler(db *sql.DB, logger *zap.Logger, signer *secrets.ComplianceSigner, auditW *audit.Writer, policySource func() string) *ComplianceHandler {
	return &ComplianceHandler{db: db, logger: logger, signer: signer, audit: auditW, policySource: policySource}
}

// GetEvidence handles GET /api/v1/compliance/evidence
// Returns a SOC 2 evidence bundle computed from live database state.
func (h *ComplianceHandler) GetEvidence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := sqlcgen.New(h.db)

	// Total audit log entries.
	totalAuditEntries64, _ := q.CountAuditLogEntries(ctx)
	totalAuditEntries := int(totalAuditEntries64)

	// Approval rate: APPROVED or APPLIED out of all non-PENDING change requests.
	stats, _ := q.ChangeRequestApprovalStats(ctx)
	approved, total := int(stats.Approved), int(stats.Total)
	var approvalRate float64
	if total > 0 {
		approvalRate = float64(approved) / float64(total)
	}

	// Break-glass token uses in last 90 days.
	breakGlassUses64, _ := q.CountRecentBreakGlassUses(ctx)
	breakGlassUses := int(breakGlassUses64)

	// Active service tokens (not revoked).
	serviceTokensActive64, _ := q.CountActiveServiceTokens(ctx)
	serviceTokensActive := int(serviceTokensActive64)

	type control struct {
		ID          string `json:"id"`
		Criteria    string `json:"criteria"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Evidence    string `json:"evidence"`
	}

	controls := []control{
		{
			ID:          "CC6",
			Criteria:    "CC6.1 - Logical and Physical Access Controls",
			Description: "Role-based access control enforced via user_roles table with VIEWER/OPERATOR/OWNER/ADMIN tiers.",
			Status:      "IMPLEMENTED",
			Evidence:    fmt.Sprintf("RBAC enabled; %d active service tokens", serviceTokensActive),
		},
		{
			ID:          "CC7",
			Criteria:    "CC7.2 - System Monitoring",
			Description: "Circuit-breaker monitoring and rollback via evaluator service; all flag evaluations produce audit trail entries.",
			Status:      "IMPLEMENTED",
			Evidence:    fmt.Sprintf("%d total audit log entries with Merkle-chain integrity", totalAuditEntries),
		},
		{
			ID:          "CC8",
			Criteria:    "CC8.1 - Change Management",
			Description: "Four-eyes approval workflow enforced on all production flag changes via change_requests table.",
			Status:      "IMPLEMENTED",
			Evidence:    fmt.Sprintf("%.0f%% approval rate across %d resolved change requests", approvalRate*100, total),
		},
		{
			ID:          "CC9",
			Criteria:    "CC9.2 - Risk Mitigation",
			Description: "Tombstoning prevents accidental key reuse (Knight Capital prevention); break-glass tokens provide audited emergency access.",
			Status:      "IMPLEMENTED",
			Evidence:    fmt.Sprintf("%d break-glass token uses in last 90 days", breakGlassUses),
		},
	}

	// AUD-1: these three fields were hardcoded — audit_log_coverage was the
	// literal string "100%", and merkle_chain_integrity and rbac_enabled were
	// the literal `true`. Compliance evidence asserting facts nobody computed is
	// worse than no evidence, so each is now derived.
	//
	// Chain verification walks the whole audit_log, which is O(n); audit_log
	// partitioning (DATA-2) is what makes this cheap at scale.
	evidence := map[string]any{
		"generated_at":                    time.Now().UTC().Format(time.RFC3339),
		"total_audit_entries":             totalAuditEntries,
		"change_approval_rate":            approvalRate,
		"break_glass_token_uses_last_90d": breakGlassUses,
		"service_tokens_active":           serviceTokensActive,
		"controls":                        controls,
	}

	if h.audit == nil {
		evidence["merkle_chain_integrity"] = nil
		evidence["audit_log_coverage"] = nil
		evidence["merkle_chain_note"] = "AUDIT_HMAC_KEY is not configured — chain integrity cannot be computed and is NOT asserted"
	} else if report, err := h.audit.Verify(ctx, ""); err != nil { // "" = whole-log, cross-project figure by design
		h.logger.Error("compliance: audit verification failed", zap.Error(err))
		evidence["merkle_chain_integrity"] = nil
		evidence["audit_log_coverage"] = nil
		evidence["merkle_chain_note"] = "chain verification failed to run — integrity is NOT asserted"
	} else {
		evidence["merkle_chain_integrity"] = report.Intact
		// Real coverage: the share of entries whose keyed hash was recomputed and
		// checked. Pre-AUD-1 rows are excluded rather than counted as verified.
		coverage := 0.0
		if report.TotalEntries > 0 {
			coverage = float64(report.VerifiedEntries) / float64(report.TotalEntries) * 100
		}
		evidence["audit_log_coverage"] = fmt.Sprintf("%.1f%%", coverage)
		evidence["audit_chain_verified_entries"] = report.VerifiedEntries
		evidence["audit_chain_legacy_entries"] = report.LegacyEntries
		evidence["audit_chain_failures"] = report.FailureCount
		if report.Note != "" {
			evidence["merkle_chain_note"] = report.Note
		}
	}

	// Replaces the hardcoded rbac_enabled=true with facts: which policy engine is
	// actually live, and how many role assignments exist. Since SEC-1 every
	// /api/v1 route carries a RequirePermission gate (enforced by a structural
	// test), so enforcement is a property of the route table, not a self-claim.
	source := "unknown"
	if h.policySource != nil {
		source = h.policySource()
	}
	roleAssignments64, _ := q.CountRoleAssignments(ctx)
	roleAssignments := int(roleAssignments64)
	evidence["rbac"] = map[string]any{
		"policy_source":    source,
		"role_assignments": roleAssignments,
	}

	writeJSON(w, http.StatusOK, evidence)
}

// GetControls handles GET /api/v1/compliance/controls
// Returns a static mapping of Tombstone capabilities to SOC 2 2017 Trust Services Criteria.
func (h *ComplianceHandler) GetControls(w http.ResponseWriter, r *http.Request) {
	type capability struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		TableRef    string `json:"table_ref,omitempty"`
	}
	type controlDetail struct {
		ID           string       `json:"id"`
		Criteria     string       `json:"criteria"`
		Category     string       `json:"category"`
		Capabilities []capability `json:"capabilities"`
	}

	controls := []controlDetail{
		{
			ID:       "CC6",
			Criteria: "CC6.1 - Logical and Physical Access Controls",
			Category: "Logical Access",
			Capabilities: []capability{
				{
					Name:        "Role-Based Access Control",
					Description: "VIEWER, OPERATOR, OWNER, and ADMIN roles gating all flag write operations.",
					TableRef:    "user_roles",
				},
				{
					Name:        "Service Token Authentication",
					Description: "Per-environment bearer tokens issued to SDK clients; revocable at any time.",
					TableRef:    "service_tokens",
				},
				{
					Name:        "Break-Glass Emergency Access",
					Description: "Time-limited, scoped emergency tokens requiring incident reference; usage logged to audit trail.",
					TableRef:    "break_glass_tokens",
				},
			},
		},
		{
			ID:       "CC7",
			Criteria: "CC7.2 - System Monitoring",
			Category: "Monitoring and Anomaly Detection",
			Capabilities: []capability{
				{
					Name:        "Append-Only Audit Log",
					Description: "Merkle-linked audit_log with prev_hash chain; UPDATE and DELETE are blocked at the database rule level.",
					TableRef:    "audit_log",
				},
				{
					Name:        "Circuit-Breaker Auto-Rollback",
					Description: "Evaluator service monitors error rate thresholds and rolls back flags automatically when breached.",
				},
				{
					Name:        "Stale Flag Detection",
					Description: "Governance health score surfaces flags at 100% rollout for >30 days as stale.",
				},
			},
		},
		{
			ID:       "CC8",
			Criteria: "CC8.1 - Change Management",
			Category: "Change Control",
			Capabilities: []capability{
				{
					Name:        "Four-Eyes Approval Workflow",
					Description: "Production flag changes require a second approver; requests track requester, approvers, and rejection reasons.",
					TableRef:    "change_requests",
				},
				{
					Name:        "Scheduled Changes",
					Description: "Flag changes can be scheduled for a future time, allowing pre-approval before deployment windows.",
					TableRef:    "scheduled_changes",
				},
				{
					Name:        "Kill Switch",
					Description: "Instant forced-off mechanism for any flag/environment combination; logged to audit trail.",
				},
			},
		},
		{
			ID:       "CC9",
			Criteria: "CC9.2 - Risk Mitigation",
			Category: "Risk Management",
			Capabilities: []capability{
				{
					Name:        "Flag Tombstoning",
					Description: "Archived flag keys are permanently recorded in flag_tombstones; reuse is blocked at DB constraint and service layer (Knight Capital prevention).",
					TableRef:    "flag_tombstones",
				},
				{
					Name:        "Inventory Limits",
					Description: "Per-project flag inventory cap prevents unbounded proliferation; configurable by ADMIN role.",
					TableRef:    "inventory_limits",
				},
				{
					Name:        "Blast Radius Gating",
					Description: "Evaluator service calculates and enforces blast radius constraints before enabling a flag at scale.",
				},
			},
		},
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"framework":    "SOC 2 Type II — Trust Services Criteria 2017",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"controls":     controls,
	})
}

// ExportAuditLog handles GET /api/v1/compliance/export
// Streams the full audit log as JSONL with a trailing HMAC-SHA256 signature line.
func (h *ComplianceHandler) ExportAuditLog(w http.ResponseWriter, r *http.Request) {
	// SEC-4: this export was signed with JWT_SECRET, so anyone able to VERIFY an
	// export could also MINT auth tokens. Signing now uses a separate key and
	// fails closed if that key is absent — silently reusing JWT_SECRET would
	// reintroduce the vulnerability.
	if h.signer == nil {
		writeError(w, http.StatusServiceUnavailable,
			"compliance export signing key is not configured (set COMPLIANCE_SIGNING_KEY)")
		return
	}

	// TEN-1a-2: an adversarial review of the audit_log project-scoping fix
	// found this query STILL unscoped — ADMIN is a per-project role
	// (user_roles is keyed by (user_id, project_id)), so a Project A admin
	// has no special relationship to Project B, yet this exported EVERY
	// project's full raw audit rows (prev_state/new_state, actor, ip_address)
	// under a cryptographic signature that lends the leaked data false
	// authority. Unlike GetEvidence/GetControls just below — which report
	// system-wide AGGREGATE counts and stay intentionally cross-project — this
	// hands over row-level content, the same severity class ListAuditLog was.
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	// Deliberately NOT converted to sqlc (DATA-1b adversarial review): sqlc's
	// generated :many methods always fully materialize the whole result set
	// into a slice before returning, but this handler is written to STREAM —
	// scan and write one row at a time so memory use is O(1) regardless of
	// how large a project's audit history has grown (retention/DATA-2 prunes
	// the hot table on its own schedule, not on every export call) and so the
	// client starts receiving bytes immediately rather than waiting for the
	// entire history to be fetched first. Going through sqlc here would have
	// silently regressed both properties for no benefit, since the whole
	// point of streaming is bypassed by a method that returns []Row.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, COALESCE(flag_key,''), COALESCE(environment,''), actor, event_type,
		       COALESCE(prev_state::text,'null'), COALESCE(new_state::text,'null'),
		       COALESCE(ip_address,''), COALESCE(prev_hash,''),
		       EXTRACT(EPOCH FROM created_at)::bigint,
		       COALESCE(rekor_log_id,''), rekor_log_index
		FROM audit_log
		WHERE project_id = $1
		ORDER BY created_at ASC
	`, projectID)
	if err != nil {
		h.logger.Error("audit export query", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer func() { _ = rows.Close() }()

	mac := h.signer.New()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", "attachment; filename=\"audit_log_export.jsonl\"")
	w.WriteHeader(http.StatusOK)

	lineCount := 0
	for rows.Next() {
		var e AuditEntry
		var prevRaw, newRaw string
		if err := rows.Scan(&e.ID, &e.FlagKey, &e.Environment, &e.Actor, &e.EventType,
			&prevRaw, &newRaw, &e.IPAddress, &e.PrevHash, &e.CreatedAt,
			&e.RekorLogID, &e.RekorLogIndex); err != nil {
			h.logger.Error("audit export scan", zap.Error(err))
			return
		}
		e.PrevState = json.RawMessage(prevRaw)
		e.NewState = json.RawMessage(newRaw)

		line, err := json.Marshal(e)
		if err != nil {
			h.logger.Error("audit export marshal", zap.Error(err))
			return
		}

		mac.Write(line)
		_, _ = fmt.Fprintf(w, "%s\n", line)
		lineCount++
	}
	// A stream that failed mid-iteration (connection blip, context
	// cancellation) must NOT get a signature line: the HTTP status is
	// already committed to 200 by this point, so the only available signal
	// that the export is truncated is the ABSENCE of the trailing signature
	// -- a signature covering only the partial data already written would
	// otherwise validate as a complete, authentic export to any consumer
	// checking it, which is worse than no signature at all for a
	// cryptographically-signed compliance artifact.
	if err := rows.Err(); err != nil {
		h.logger.Error("audit export: stream failed before completion, omitting signature", zap.Error(err))
		return
	}

	sig := secrets.Sum(mac)
	sigLine, _ := json.Marshal(map[string]any{
		"_type":       "export_signature",
		"kid":         h.signer.KeyID(),
		"hmac_sha256": sig,
		"line_count":  lineCount,
		"exported_at": time.Now().Unix(),
	})
	_, _ = fmt.Fprintf(w, "%s\n", sigLine)
}
