package v1

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/tombstone/flag-api/internal/audit"
	"github.com/tombstone/flag-api/internal/middleware"
	"github.com/tombstone/flag-api/internal/secrets"
)

// These tests drive BreakGlassHandler.CreateToken directly over a sqlmock
// database, so they need no Postgres and run everywhere (unlike
// TestRequireApprovalGate, which skips without TEST_DATABASE_URL). sqlmock is
// strict about ordering: any statement a test did not queue fails the call,
// which is how "nothing was created" is proved below.
//
// Two things sqlmock does NOT do, and the tests below compensate for:
//   - ExpectationsWereMet lists only expectations that went unmet. A statement
//     that was never queued is answered with an error to the caller and is not
//     recorded, so a surplus audit write passes it.
//   - audit.Writer errors are not returned to the handler's caller; the handler
//     logs them at Warn. The handler's logger is therefore an observer, and the
//     tests compare the exact set of Warn-or-worse entries. A second audit write
//     (the chain is queued once) shows up there as "break-glass audit write
//     failed". TestBreakGlassAudit_SecondWriteIsLogged is the positive control.

const (
	bgTestProjectID    = "11111111-1111-4111-8111-111111111111"
	bgTestHumanActor   = "alice@example.com"
	bgTestServiceActor = "sdk:test-sentinel-ci-bot"

	// bgTestAuthSentinel is a made-up credential, not a real one. Every test
	// request carries it in its Authorization header, and the tests then check
	// that it reaches neither an audit entry nor the log. The scheme word is
	// assembled so that no scheme-plus-token literal sits in the source.
	bgTestAuthSentinel = "SENTINEL-AUTHORIZATION-VALUE-NOT-A-REAL-CREDENTIAL"
	bgTestAuthScheme   = "Bear" + "er"

	// The contract a client sees; pinned as literals on purpose.
	bgServiceTokenDeniedMsg = "break-glass tokens can only be created by a human user, not a service token"
	bgExpiryTooLongMsg      = "expires_in_hours must be at most 24"

	// Log messages the tests look for.
	bgRefusalLogMsg     = "break-glass token creation refused for a service-token caller"
	bgAuditFailedLogMsg = "break-glass audit write failed"
)

// leakCheck names a value that must not appear where a test looks.
type leakCheck struct{ name, value string }

// bgAuthLeaks are the two spellings of the request's credential: the whole
// header value and the bare token.
var bgAuthLeaks = []leakCheck{
	{"Authorization header value", bgTestAuthScheme + " " + bgTestAuthSentinel},
	{"Authorization credential", bgTestAuthSentinel},
}

// newBreakGlassTestHandler returns a handler over a sqlmock database whose
// logger records every entry, so a test can see what the handler logged.
func newBreakGlassTestHandler(t *testing.T) (*BreakGlassHandler, sqlmock.Sqlmock, *observer.ObservedLogs) {
	t.Helper()
	db, mock := newMockDB(t)
	hasher, err := secrets.NewTokenHasher("break-glass-test-pepper")
	if err != nil {
		t.Fatalf("token hasher: %v", err)
	}
	core, logs := observer.New(zapcore.DebugLevel)
	return NewBreakGlassHandler(db, nil, zap.New(core), hasher, audit.NewWriter(db, nil)), mock, logs
}

// newCreateTokenRequest builds POST /api/v1/break-glass/tokens the way the
// middleware chain leaves it. Every caller carries an Authorization header and
// an actor. A service-token caller also carries the role Authenticate records
// in ContextKeyServiceRole, which it sets for service tokens and nothing else,
// together with its project. withProject controls whether a project id is in
// the context at all (RequireProjectID not having run).
func newCreateTokenRequest(body, actor string, viaServiceToken, withProject bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/break-glass/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bgTestAuthScheme+" "+bgTestAuthSentinel)
	ctx := context.WithValue(req.Context(), middleware.ContextKeyActor, actor)
	if withProject {
		ctx = context.WithValue(ctx, middleware.ContextKeyProjectID, bgTestProjectID)
	}
	if viaServiceToken {
		ctx = context.WithValue(ctx, middleware.ContextKeyServiceRole, middleware.RoleAdmin)
		if withProject {
			ctx = context.WithValue(ctx, middleware.ContextKeyServiceProjectID, bgTestProjectID)
		}
	}
	return req.WithContext(ctx)
}

// createTokenRequest is newCreateTokenRequest for a caller whose project was
// resolved, which is every request that reaches the handler in production.
func createTokenRequest(body, actor string, viaServiceToken bool) *http.Request {
	return newCreateTokenRequest(body, actor, viaServiceToken, true)
}

// capturedAudit records every value one audit.Writer.Append call bound to the
// database: the four of the chain-tip read and the twelve of the INSERT.
type capturedAudit struct {
	tip    [4]driver.Value
	insert [12]driver.Value
}

func (c *capturedAudit) actor() driver.Value     { return c.insert[3] }
func (c *capturedAudit) eventType() driver.Value { return c.insert[4] }

// newState is the audit entry's details as the writer was handed them.
func (c *capturedAudit) newState() driver.Value { return c.tip[2] }

// text renders every captured value as one string, for searching.
func (c *capturedAudit) text() string {
	var b strings.Builder
	for _, v := range slices.Concat(c.tip[:], c.insert[:]) {
		if raw, ok := v.([]byte); ok {
			b.Write(raw)
		} else {
			fmt.Fprint(&b, v)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// captureAll returns one capturing matcher for each slot, so WithArgs accepts
// any value and records it.
func captureAll(slots []driver.Value) []driver.Value {
	matchers := make([]driver.Value, len(slots))
	for i := range slots {
		matchers[i] = captureArg{&slots[i]}
	}
	return matchers
}

// expectAuditAppendCapturing queues the statements audit.Writer.Append runs for
// one entry and records every value it is given. It queues ONE entry; a second
// audit write finds no expectation and fails, which the handler logs at Warn.
func expectAuditAppendCapturing(mock sqlmock.Sqlmock) *capturedAudit {
	c := &capturedAudit{}
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	// The third argument of the chain-tip read is the new state as handed to the writer.
	mock.ExpectQuery(`::jsonb::text`).WithArgs(captureAll(c.tip[:])...).
		WillReturnRows(sqlmock.NewRows([]string{"prev", "next", "tip"}).AddRow(nil, nil, nil))
	mock.ExpectExec(`INSERT INTO audit_log`).WithArgs(captureAll(c.insert[:])...).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	return c
}

// capturedToken records what CreateBreakGlassToken persisted.
type capturedToken struct {
	createdBy, expiresAt driver.Value
}

// expectTokenInsert queues the break_glass_tokens INSERT CreateToken issues.
func expectTokenInsert(mock sqlmock.Sqlmock) *capturedToken {
	c := &capturedToken{}
	anyArg := sqlmock.AnyArg()
	mock.ExpectExec(`INSERT INTO break_glass_tokens`).
		WithArgs(anyArg, anyArg, captureArg{&c.createdBy}, captureArg{&c.expiresAt}, anyArg, anyArg).
		WillReturnResult(sqlmock.NewResult(1, 1))
	return c
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body["error"]
}

// decodeAuditState decodes the details of a captured audit entry into a map, so
// a test can compare the WHOLE set of keys and not only the ones it expects.
func decodeAuditState(t *testing.T, state driver.Value) map[string]any {
	t.Helper()
	text, ok := state.(string)
	if !ok {
		t.Fatalf("audit new_state argument = %#v, want a string", state)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(text), &details); err != nil {
		t.Fatalf("audit new_state %q is not a JSON object: %v", text, err)
	}
	return details
}

// requireAuditState fails unless the captured entry's details are exactly want.
func requireAuditState(t *testing.T, got *capturedAudit, want map[string]any) {
	t.Helper()
	if state := decodeAuditState(t, got.newState()); !reflect.DeepEqual(state, want) {
		t.Errorf("audit new_state = %v, want exactly %v", state, want)
	}
}

// warnMessages lists the message of every Warn-or-worse entry the handler logged.
func warnMessages(logs *observer.ObservedLogs) []string {
	messages := []string{}
	for _, entry := range logs.All() {
		if entry.Level >= zapcore.WarnLevel {
			messages = append(messages, entry.Message)
		}
	}
	return messages
}

// requireWarnings fails unless the handler logged exactly the given Warn-or-worse
// messages, in order. With none given it asserts a clean run: the audit write
// the test queued was the only one, and it did not fail.
func requireWarnings(t *testing.T, logs *observer.ObservedLogs, want ...string) {
	t.Helper()
	if got := warnMessages(logs); !slices.Equal(got, want) {
		t.Errorf("Warn-or-worse log entries = %q, want %q (a surplus or failed audit write is logged as %q)",
			got, want, bgAuditFailedLogMsg)
	}
}

// logText renders every logged entry, message and fields, as one string.
func logText(logs *observer.ObservedLogs) string {
	var b strings.Builder
	for _, entry := range logs.All() {
		fmt.Fprintf(&b, "%s %v\n", entry.Message, entry.ContextMap())
	}
	return b.String()
}

// requireNoLeak fails if haystack contains any forbidden value.
func requireNoLeak(t *testing.T, where, haystack string, forbidden ...leakCheck) {
	t.Helper()
	for _, f := range forbidden {
		if strings.Contains(haystack, f.value) {
			t.Errorf("%s carries the %s", where, f.name)
		}
	}
}

// TestCreateToken_RefusesServiceTokenCallers is the hardening for a leaked
// ADMIN service token: the SDK keys can hold the ADMIN role, which satisfies
// the route's admin:admin check, so without this guard such a key could mint
// emergency credentials that bypass the four-eyes approval gate. Emergency
// access is a human act; the refusal is audited exactly once, with exactly the
// details below and nothing from the request's credentials, and nothing is
// created.
func TestCreateToken_RefusesServiceTokenCallers(t *testing.T) {
	h, mock, logs := newBreakGlassTestHandler(t)
	// Only the audit write is queued. The token INSERT is deliberately absent:
	// sqlmock fails any statement it was not told to expect.
	got := expectAuditAppendCapturing(mock)

	rec := httptest.NewRecorder()
	h.CreateToken(rec, createTokenRequest(`{"scope":"all-flags","expires_in_hours":4}`, bgTestServiceActor, true))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec); msg != bgServiceTokenDeniedMsg {
		t.Errorf("error = %q, want %q", msg, bgServiceTokenDeniedMsg)
	}
	if strings.Contains(rec.Body.String(), "bgt_") {
		t.Errorf("response carries a break-glass token: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the denial was not audited, or something beyond the audit write ran: %v", err)
	}
	if got.eventType() != "break_glass_token_denied" {
		t.Errorf("audit event_type = %v, want break_glass_token_denied", got.eventType())
	}
	if got.actor() != bgTestServiceActor {
		t.Errorf("audit actor = %v, want %s", got.actor(), bgTestServiceActor)
	}
	requireAuditState(t, got, map[string]any{"reason": "service_token_caller", "project_id": bgTestProjectID})
	// Exactly one audit write: a second one finds no queued chain and is logged.
	// The refusal's own Warn is the positive control that this logger is wired.
	requireWarnings(t, logs, bgRefusalLogMsg)
	requireNoLeak(t, "the denial audit entry", got.text(), bgAuthLeaks...)
	requireNoLeak(t, "the log", logText(logs), bgAuthLeaks...)
}

// TestCreateToken_RefusesServiceTokenBeforeLookingAtTheBody pins the order: a
// refused caller learns nothing about request validation, and a body that would
// otherwise be rejected (or accepted) changes neither the status nor the audit.
func TestCreateToken_RefusesServiceTokenBeforeLookingAtTheBody(t *testing.T) {
	for _, body := range []string{`not json`, `{"expires_in_hours":9999}`, `{}`} {
		h, mock, logs := newBreakGlassTestHandler(t)
		got := expectAuditAppendCapturing(mock)

		rec := httptest.NewRecorder()
		h.CreateToken(rec, createTokenRequest(body, bgTestServiceActor, true))

		if rec.Code != http.StatusForbidden {
			t.Errorf("body %q: status = %d, want 403; response: %s", body, rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("body %q: %v", body, err)
		}
		requireAuditState(t, got, map[string]any{"reason": "service_token_caller", "project_id": bgTestProjectID})
		requireWarnings(t, logs, bgRefusalLogMsg)
	}
}

// TestCreateToken_RefusesServiceTokenWithoutAProject pins where the refusal
// sits relative to requireProjectID. That guard answers 500 when no project was
// resolved; the refusal comes first, so a service-token caller is always told
// 403 and always leaves a denial entry, whatever else its context lacks. With
// no project to record, the entry carries the reason alone.
func TestCreateToken_RefusesServiceTokenWithoutAProject(t *testing.T) {
	h, mock, logs := newBreakGlassTestHandler(t)
	got := expectAuditAppendCapturing(mock)

	rec := httptest.NewRecorder()
	h.CreateToken(rec, newCreateTokenRequest(`{"scope":"all-flags"}`, bgTestServiceActor, true, false))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (not requireProjectID's 500); body: %s", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec); msg != bgServiceTokenDeniedMsg {
		t.Errorf("error = %q, want %q", msg, bgServiceTokenDeniedMsg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the denial was not audited, or something beyond the audit write ran: %v", err)
	}
	if got.eventType() != "break_glass_token_denied" || got.actor() != bgTestServiceActor {
		t.Errorf("audit = %v by %v, want break_glass_token_denied by %s", got.eventType(), got.actor(), bgTestServiceActor)
	}
	requireAuditState(t, got, map[string]any{"reason": "service_token_caller"})
	requireWarnings(t, logs, bgRefusalLogMsg)
}

// TestCreateToken_HumanCallerStillCreatesToken is the positive control for the
// refusal test above: the same harness, with the service-token marker absent,
// reaches the INSERT and returns a token. Without it, "nothing was created"
// for the service caller would also be true of a harness that can never create.
// It also pins the creation audit entry: written once, with exactly the scope
// and the expiry, and without the minted token or the caller's credentials.
func TestCreateToken_HumanCallerStillCreatesToken(t *testing.T) {
	h, mock, logs := newBreakGlassTestHandler(t)
	tok := expectTokenInsert(mock)
	got := expectAuditAppendCapturing(mock)

	rec := httptest.NewRecorder()
	h.CreateToken(rec, createTokenRequest(`{"scope":"payment-flags","expires_in_hours":2,"incident_ref":"INC-1"}`, bgTestHumanActor, false))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(resp.Token, "bgt_") || resp.Scope != "payment-flags" {
		t.Errorf("response = %+v, want a bgt_ token with scope payment-flags", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("token insert and audit write did not both run: %v", err)
	}
	if tok.createdBy != bgTestHumanActor {
		t.Errorf("created_by = %v, want %s (defaults to the caller)", tok.createdBy, bgTestHumanActor)
	}
	if got.eventType() != "break_glass_token_created" || got.actor() != bgTestHumanActor {
		t.Errorf("audit = %v by %v, want break_glass_token_created by %s", got.eventType(), got.actor(), bgTestHumanActor)
	}
	expiresAt, ok := tok.expiresAt.(time.Time)
	if !ok {
		t.Fatalf("expires_at argument = %#v, want a time.Time", tok.expiresAt)
	}
	requireAuditState(t, got, map[string]any{"scope": "payment-flags", "expires_at": float64(expiresAt.Unix())})
	// Exactly one audit write, and nothing at Warn: this is the only log entry
	// of the happy path that could be a failed second write.
	requireWarnings(t, logs)
	leaks := slices.Concat(bgAuthLeaks, []leakCheck{{"minted token", resp.Token}})
	requireNoLeak(t, "the creation audit entry", got.text(), leaks...)
	requireNoLeak(t, "the log", logText(logs), leaks...)
}

// TestCreateToken_RejectsExpiryAboveTheCap covers the missing upper bound: any
// positive expires_in_hours was accepted, so one request minted a credential
// that outlived the incident by years, and a large enough value overflowed
// time.Duration into an arbitrary expiry. Nothing is written for a rejected
// request, so no statement is queued.
func TestCreateToken_RejectsExpiryAboveTheCap(t *testing.T) {
	for _, hours := range []string{"25", "1000", "8760", "9223372036854775807"} {
		t.Run(hours+"h", func(t *testing.T) {
			h, mock, logs := newBreakGlassTestHandler(t)

			rec := httptest.NewRecorder()
			h.CreateToken(rec, createTokenRequest(`{"expires_in_hours":`+hours+`}`, bgTestHumanActor, false))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
			if msg := errorBody(t, rec); msg != bgExpiryTooLongMsg {
				t.Errorf("error = %q, want %q", msg, bgExpiryTooLongMsg)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unexpected database activity for a rejected request: %v", err)
			}
			// A rejected request attempts no audit write either; an attempt would
			// fail (nothing is queued) and be logged.
			requireWarnings(t, logs)
		})
	}
}

// TestCreateToken_ExpiryLifetimes pins the accepted lifetimes: exactly the cap
// is allowed, and an omitted, zero or negative value keeps the default of 4h.
func TestCreateToken_ExpiryLifetimes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{"exactly the cap", `{"expires_in_hours":24}`, 24 * time.Hour},
		{"one hour", `{"expires_in_hours":1}`, time.Hour},
		{"omitted defaults to 4h", `{}`, 4 * time.Hour},
		{"zero defaults to 4h", `{"expires_in_hours":0}`, 4 * time.Hour},
		{"negative defaults to 4h", `{"expires_in_hours":-5}`, 4 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, mock, logs := newBreakGlassTestHandler(t)
			tok := expectTokenInsert(mock)
			expectAuditAppendCapturing(mock)

			before := time.Now()
			rec := httptest.NewRecorder()
			h.CreateToken(rec, createTokenRequest(tc.body, bgTestHumanActor, false))
			after := time.Now()

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
			}
			expiresAt, ok := tok.expiresAt.(time.Time)
			if !ok {
				t.Fatalf("expires_at argument = %#v, want a time.Time", tok.expiresAt)
			}
			if expiresAt.Before(before.Add(tc.want)) || expiresAt.After(after.Add(tc.want)) {
				t.Errorf("expires_at = %s, want between %s and %s (now + %s)",
					expiresAt, before.Add(tc.want), after.Add(tc.want), tc.want)
			}
			requireWarnings(t, logs)
		})
	}
}

// TestBreakGlassAudit_SecondWriteIsLogged is the positive control for the
// exactly-once assertions above. One audit chain is queued and two writes are
// made. ExpectationsWereMet still passes, which is why it cannot be the check,
// but the second write is answered with an error and the handler logs it, so
// requireWarnings does see it. A mutant that writes the same entry twice is
// caught the same way.
func TestBreakGlassAudit_SecondWriteIsLogged(t *testing.T) {
	h, mock, logs := newBreakGlassTestHandler(t)
	expectAuditAppendCapturing(mock)
	req := createTokenRequest(`{}`, bgTestHumanActor, false)

	h.writeAuditBreakGlass(req, bgTestHumanActor, "break_glass_token_created", map[string]any{"scope": "all-flags"})
	requireWarnings(t, logs)

	h.writeAuditBreakGlass(req, bgTestHumanActor, "break_glass_token_created", map[string]any{"scope": "all-flags"})
	requireWarnings(t, logs, bgAuditFailedLogMsg)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("ExpectationsWereMet = %v, want nil: it does not report the surplus write", err)
	}
}
