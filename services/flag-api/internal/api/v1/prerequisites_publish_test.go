package v1

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestPublishPrerequisitesUpdated_NilRdbIsANoOp closes a completeness gap
// found by adversarial review of the prerequisites-streaming PR: no test
// exercised publishPrerequisitesUpdated's nil-rdb early return at all.
// Unlike the DB-backed tests in prerequisites_db_test.go (which require
// TEST_DATABASE_URL and are skipped without it), this needs no database or
// Redis at all -- it proves the nil check happens BEFORE h.db is ever
// touched, by constructing a PrerequisiteHandler with rdb AND db both nil:
// if the nil-rdb check were ever removed or reordered after the DB query,
// this would panic on the nil *sql.DB instead of returning cleanly.
func TestPublishPrerequisitesUpdated_NilRdbIsANoOp(t *testing.T) {
	h := &PrerequisiteHandler{db: nil, rdb: nil, logger: zap.NewNop()}

	// Must return without panicking. A real *sql.DB is never dereferenced
	// because the rdb-nil check returns first.
	h.publishPrerequisitesUpdated(context.Background(), "any-flag", "any-project")
}
