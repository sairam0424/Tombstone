package v1

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/clientip"
)

// captureArg is a sqlmock argument matcher that accepts any value and records
// it, so a test can assert on what a query was actually sent.
type captureArg struct{ got *driver.Value }

func (c captureArg) Match(v driver.Value) bool {
	*c.got = v
	return true
}

// newMockDB opens a sqlmock database that is closed when the test ends.
func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// expectAuditAppend queues the statements audit.Writer.Append runs for one
// entry and returns where the ip_address the INSERT is given will be recorded.
// That value is the audit.Entry.IPAddress the writer persisted, and the one the
// hash chain commits to.
func expectAuditAppend(mock sqlmock.Sqlmock) *driver.Value {
	var stored driver.Value
	anyArg := sqlmock.AnyArg()
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`::jsonb::text`).WithArgs(anyArg, anyArg, anyArg, anyArg).
		WillReturnRows(sqlmock.NewRows([]string{"prev", "next", "tip"}).AddRow(nil, nil, nil))
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs(anyArg, anyArg, anyArg, anyArg, anyArg, anyArg, anyArg, captureArg{&stored}, anyArg, anyArg, anyArg, anyArg).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	return &stored
}

// storedIP reads the ip_address recorded by expectAuditAppend once the audit
// write has run, failing the test if it did not complete.
func storedIP(t *testing.T, mock sqlmock.Sqlmock, stored *driver.Value) string {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("audit write did not complete: %v", err)
	}
	ip, ok := (*stored).(string)
	if !ok {
		t.Fatalf("ip_address argument = %#v, want a string", *stored)
	}
	return ip
}

// auditedIP runs one audit write the way every flag mutation does, with the IP
// taken from ipFromRequest(req), through a real audit.Writer over a mock
// database, and returns the ip_address the INSERT was given.
func auditedIP(t *testing.T, req *http.Request) string {
	t.Helper()
	db, mock := newMockDB(t)
	stored := expectAuditAppend(mock)

	h := NewFlagHandler(db, nil, zap.NewNop(), nil, audit.NewWriter(db, nil), nil, "")
	h.writeAudit(context.Background(), "proj-1", "checkout-v2", "production", "alice",
		"flag_environment_updated", nil, nil, ipFromRequest(req))

	return storedIP(t, mock, stored)
}

// TestWriteAudit_RecordsSingleValidatedIP pins what ends up in the audit log:
// one bare IP, never the raw X-Forwarded-For chain or any text the caller
// chose. Before this, the whole header was stored, so an authenticated caller
// could put arbitrary text in a tamper-evident record of who did what.
func TestWriteAudit_RecordsSingleValidatedIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{"direct peer forging a chain", map[string]string{"X-Forwarded-For": "6.6.6.6, approved-by-security, 10.0.0.5"}, "203.0.113.9:4000", "203.0.113.9"},
		{"direct peer forging X-Real-IP", map[string]string{"X-Real-IP": "6.6.6.6"}, "203.0.113.9:4000", "203.0.113.9"},
		{"direct peer forging True-Client-IP", map[string]string{"True-Client-IP": "6.6.6.6"}, "203.0.113.9:4000", "203.0.113.9"},
		{"direct peer stuffing a long header", map[string]string{"X-Forwarded-For": strings.Repeat("A,", 40)}, "203.0.113.9:4000", "203.0.113.9"},
		{"no forwarding headers", nil, "203.0.113.9:4000", "203.0.113.9"},
		{"IPv6 peer", nil, "[2001:db8::1]:4000", "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/flags/checkout-v2/kill-switch", nil)
			req.RemoteAddr = tc.remoteAddr
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}

			got := auditedIP(t, req)

			if got != tc.want {
				t.Errorf("audit ip_address = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, ",") {
				t.Errorf("audit ip_address %q is a chain, want a single IP", got)
			}
			if _, err := netip.ParseAddr(got); err != nil {
				t.Errorf("audit ip_address %q is not a valid IP: %v", got, err)
			}
		})
	}
}

// TestWriteAudit_RecordsTheClientBehindATrustedProxy covers the deployed shape:
// the proxy's X-Forwarded-For entry is what gets recorded, not the proxy's own
// address and not any entry the caller prepended.
func TestWriteAudit_RecordsTheClientBehindATrustedProxy(t *testing.T) {
	trust, err := clientip.ParseTrust("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseTrust: %v", err)
	}
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"client seen by the proxy", "10.1.2.3:4000", "198.51.100.7", "198.51.100.7"},
		{"caller prepended entries", "10.1.2.3:4000", "6.6.6.6, approved-by-security, 198.51.100.7", "198.51.100.7"},
		{"two trusted hops", "10.1.2.3:4000", "198.51.100.7, 10.9.9.9", "198.51.100.7"},
		{"direct caller forging the header", "203.0.113.9:4000", "10.1.2.3", "203.0.113.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/flags/checkout-v2/kill-switch", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.forwarded)
			var got string

			trust.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = auditedIP(t, r)
			})).ServeHTTP(httptest.NewRecorder(), req)

			if got != tc.want {
				t.Errorf("audit ip_address = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestKillSwitch_AuditsTheDerivedClientIP drives a real mutation route, through
// the trusted-proxy middleware, over a mock database, and asserts the
// ip_address of the audit INSERT. The tests above compose ipFromRequest with
// writeAudit by hand, so only this one notices a handler that hands the audit
// log something other than the derived client.
func TestKillSwitch_AuditsTheDerivedClientIP(t *testing.T) {
	trust, err := clientip.ParseTrust("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseTrust: %v", err)
	}
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"client behind the trusted proxy", "10.1.2.3:4000", "6.6.6.6, approved-by-security, 198.51.100.7", "198.51.100.7"},
		{"direct caller forging the header", "203.0.113.9:4000", "6.6.6.6", "203.0.113.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			mock.ExpectExec(`UPDATE flag_environments`).WillReturnResult(sqlmock.NewResult(0, 1))
			stored := expectAuditAppend(mock)
			mr, err := miniredis.Run()
			if err != nil {
				t.Fatalf("miniredis: %v", err)
			}
			t.Cleanup(mr.Close)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			h := NewFlagHandler(db, rdb, zap.NewNop(), nil, audit.NewWriter(db, nil), nil, "")
			req := newTenancyRequest(t, http.MethodPost, "/api/v1/flags/checkout-v2/kill-switch",
				map[string]any{"environment": "production"}, "proj-1", map[string]string{"key": "checkout-v2"})
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.forwarded)
			rec := httptest.NewRecorder()

			trust.Middleware(http.HandlerFunc(h.KillSwitch)).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("KillSwitch status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			if got := storedIP(t, mock, stored); got != tc.want {
				t.Errorf("audit ip_address = %q, want %q", got, tc.want)
			}
		})
	}
}
