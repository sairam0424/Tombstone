package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/secrets"
)

// TestExportAuditLog_OmitsSignatureWhenStreamFailsMidIteration is the direct
// regression proof for a gap caught while reverting ExportAuditLog back to
// hand-written streaming (DATA-1b PR1's sqlc conversion had regressed this
// handler to full in-memory buffering; reverting it lost the generated
// ':many' method's incidental rows.Err() check along the way). The HTTP
// status is already committed to 200 by the time streaming starts, so the
// only signal available that an export is truncated (connection blip,
// context cancellation) is the ABSENCE of the trailing HMAC signature line —
// a signature covering only the rows that made it out before the failure
// would otherwise validate as a complete, authentic export to any consumer
// checking it, which is worse than emitting none at all for a
// cryptographically-signed compliance artifact.
func TestExportAuditLog_OmitsSignatureWhenStreamFailsMidIteration(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Row 0 streams successfully; row 1's injected error simulates a real
	// mid-iteration failure (connection blip, context cancellation) —
	// proving the omitted-signature property holds even after some rows
	// have already been written to the response, not just on an immediate
	// failure before anything was sent.
	rows := sqlmock.NewRows([]string{
		"id", "flag_key", "environment", "actor", "event_type",
		"prev_state", "new_state", "ip_address", "prev_hash",
		"created_at", "rekor_log_id", "rekor_log_index",
	}).AddRow("audit-1", "flag-a", "production", "alice", "flag_created",
		"null", "null", "10.0.0.1", "", 1700000000, "", nil).
		AddRow("audit-2", "flag-b", "production", "alice", "flag_created",
			"null", "null", "10.0.0.1", "", 1700000001, "", nil).
		RowError(1, sqlmock.ErrCancelled)
	mock.ExpectQuery(`SELECT id, COALESCE\(flag_key,''\)`).WillReturnRows(rows)

	signer, err := secrets.NewComplianceSigner("export-test-key-000000000000", "", "jwt-test-key-000000000000")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	h := NewComplianceHandler(db, zap.NewNop(), signer, nil, nil)

	req := newTenancyRequest(t, http.MethodGet, "/api/v1/compliance/export", nil, "11111111-1111-1111-1111-111111111111", nil)
	rec := httptest.NewRecorder()
	h.ExportAuditLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (already committed before the stream failure is discovered)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "export_signature") {
		t.Fatalf("response contains a signature line despite the stream failing mid-iteration — "+
			"this would let a truncated export validate as complete and authentic: %s", body)
	}
}
