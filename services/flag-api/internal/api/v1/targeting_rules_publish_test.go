package v1

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestPublishTargetingRulesUpdated_NilRdbIsANoOp mirrors
// TestPublishPrerequisitesUpdated_NilRdbIsANoOp exactly, applying that same
// completeness lesson proactively here from the start: constructs a
// TargetingRuleHandler with rdb AND db both nil, proving the nil-rdb check
// happens BEFORE h.db is ever touched -- if that check were ever removed or
// reordered after the DB query, this would panic on the nil *sql.DB instead
// of returning cleanly.
func TestPublishTargetingRulesUpdated_NilRdbIsANoOp(t *testing.T) {
	h := &TargetingRuleHandler{db: nil, rdb: nil, logger: zap.NewNop()}

	h.publishTargetingRulesUpdated(context.Background(), "any-flag", "production", "any-project")
}
