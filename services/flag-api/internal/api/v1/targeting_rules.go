package v1

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/tombstone/flag-api/internal/db/sqlcgen"
)

// TargetingRuleHandler manages per-environment targeting rules -- the
// individual-target/rule-matching steps (3-4 of the 5-step evaluation
// pipeline) every SDK already implements (found while scoping this gap:
// Python/Java/Ruby/.NET/TypeScript all already have working evaluation
// logic for targeting_rules, just starved of real backend data -- the
// `targeting_rules` table itself has existed since the baseline schema with
// zero query file, zero REST endpoint, and zero snapshot wiring against it).
//
// Unlike PrerequisiteHandler's flag_prerequisites (global, no environment
// column -- see that struct's own doc comment), targeting_rules already has
// its own `environment` column, matching flag_environments' granularity. A
// mutation here only ever affects the ONE environment it targets -- no
// PrerequisitesEvent-style per-environment fan-out loop is needed.
type TargetingRuleHandler struct {
	db     *sql.DB
	rdb    *redis.Client
	logger *zap.Logger
}

func NewTargetingRuleHandler(db *sql.DB, rdb *redis.Client, logger *zap.Logger) *TargetingRuleHandler {
	return &TargetingRuleHandler{db: db, rdb: rdb, logger: logger}
}

// TargetingRule is the API-level representation of a targeting_rules row --
// returned by AddTargetingRule/ListTargetingRules and embedded (as
// SnapshotTargetingRule, a slimmer variant) in GetSnapshot.
//
// Field names/casing mirror every SDK's already-existing TargetingRule type
// (e.g. @tombstone/core's src/types.ts) exactly: rule_type/attribute/
// operator/values/variation/priority -- these SDKs were written against
// proto/v1/flags/flags.proto's TargetingRule message well before this
// backend surface existed, so the wire contract is fixed by them, not by
// this handler.
type TargetingRule struct {
	ID          string          `json:"id"`
	FlagID      string          `json:"flag_id"`
	Environment string          `json:"environment"`
	RuleType    string          `json:"rule_type"`
	Attribute   string          `json:"attribute"`
	Operator    string          `json:"operator"`
	Values      json.RawMessage `json:"values"`
	Variation   string          `json:"variation"`
	Priority    int             `json:"priority"`
	CreatedAt   int64           `json:"created_at"`
}

// AddTargetingRuleRequest is the request body for
// POST /api/v1/flags/{key}/environments/{env}/rules.
type AddTargetingRuleRequest struct {
	RuleType  string          `json:"rule_type"`
	Attribute string          `json:"attribute"`
	Operator  string          `json:"operator"`
	Values    json.RawMessage `json:"values"`
	Variation string          `json:"variation"`
	Priority  int             `json:"priority"` // default 0
}

var validRuleTypes = map[string]bool{"USER": true, "ORG": true, "SEGMENT": true, "CUSTOM": true}

// validOperators mirrors targeting_rules' own CHECK constraint (schema.sql)
// exactly -- validating here first turns a would-be constraint-violation
// 500 into a proper 400 with a useful message.
var validOperators = map[string]bool{
	"IN": true, "NOT_IN": true, "EQ": true, "NEQ": true, "LT": true, "LTE": true,
	"GT": true, "GTE": true, "CONTAINS": true, "PREFIX": true, "SUFFIX": true,
	"REGEX": true, "SEMVER_GTE": true, "SEMVER_LTE": true, "GEO_COUNTRY": true,
	"GEO_REGION": true, "DATE_BEFORE": true, "DATE_AFTER": true,
}

// validate returns a non-empty message describing the first violation found,
// or "" if req is well-formed. A pure function (no I/O) deliberately
// factored out of AddTargetingRule so it's testable without a database --
// see targeting_rules_test.go.
func (req AddTargetingRuleRequest) validate() string {
	if !validRuleTypes[req.RuleType] {
		return "rule_type must be one of USER, ORG, SEGMENT, CUSTOM"
	}
	if req.Attribute == "" {
		return "attribute is required"
	}
	if !validOperators[req.Operator] {
		return "operator is not a recognized operator"
	}
	if req.Variation == "" {
		return "variation is required"
	}
	// Priority is stored as int32 (InsertTargetingRuleParams.Priority); an
	// out-of-range value silently wraps via Go's truncating conversion
	// (int32(req.Priority) below) instead of erroring, which -- since rules
	// are ORDER BY priority ASC (lower = evaluated first) -- can invert a
	// rule's intended evaluation order without any error surfacing at all.
	// Found by adversarial review of this PR.
	if req.Priority < math.MinInt32 || req.Priority > math.MaxInt32 {
		return "priority must fit in a signed 32-bit integer"
	}
	return ""
}

// AddTargetingRule handles POST /api/v1/flags/{key}/environments/{env}/rules
func (h *TargetingRuleHandler) AddTargetingRule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	env := chi.URLParam(r, "env")

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	var req AddTargetingRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if len(req.Values) == 0 {
		req.Values = json.RawMessage("[]")
	}

	q := sqlcgen.New(h.db)

	flagID, err := q.ResolveFlagIDByKey(r.Context(), sqlcgen.ResolveFlagIDByKeyParams{Key: key, ProjectID: projectID})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	} else if err != nil {
		h.logger.Error("resolve flag id", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// targeting_rules.environment has no foreign key to flag_environments
	// (only flag_id references flags) -- without this check, a typo'd or
	// otherwise nonexistent environment would still 201, silently creating
	// a permanently orphaned rule no SDK could ever reach (GetSnapshot only
	// ever looks up flags that HAVE a flag_environments row for the
	// requested environment; a rule attached to one that doesn't exist is
	// dropped by that lookup, forever). UpdateEnvironment (flags.go) cannot
	// hit this because flag_environments rows are only ever created for a
	// fixed set at flag-creation time -- reusing the SAME existence check
	// change_requests.go already uses for the identical reason. Found by
	// adversarial review of this PR, confirmed via a real Postgres repro.
	if _, err := q.ChangeRequestTargetExists(r.Context(), sqlcgen.ChangeRequestTargetExistsParams{
		Key: key, Environment: env, ProjectID: projectID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "flag or environment not found")
			return
		}
		h.logger.Error("targeting rule environment existence check", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	inserted, err := q.InsertTargetingRule(r.Context(), sqlcgen.InsertTargetingRuleParams{
		FlagID:      flagID,
		Environment: env,
		RuleType:    req.RuleType,
		Attribute:   req.Attribute,
		Operator:    req.Operator,
		Values:      req.Values,
		Variation:   req.Variation,
		Priority:    int32(req.Priority),
	})
	if err != nil {
		h.logger.Error("insert targeting rule", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rule := TargetingRule{
		ID: inserted.ID, FlagID: inserted.FlagID, Environment: env,
		RuleType: inserted.RuleType, Attribute: inserted.Attribute, Operator: inserted.Operator,
		Values: inserted.Values, Variation: inserted.Variation, Priority: int(inserted.Priority),
		CreatedAt: inserted.CreatedAt,
	}

	h.publishTargetingRulesUpdated(r.Context(), key, env, projectID)
	writeJSON(w, http.StatusCreated, rule)
}

// ListTargetingRules handles GET /api/v1/flags/{key}/environments/{env}/rules
func (h *TargetingRuleHandler) ListTargetingRules(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	env := chi.URLParam(r, "env")

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	rows, err := sqlcgen.New(h.db).ListTargetingRulesForFlag(r.Context(), sqlcgen.ListTargetingRulesForFlagParams{
		Key: key, Environment: env, ProjectID: projectID,
	})
	if err != nil {
		h.logger.Error("list targeting rules", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	rules := []TargetingRule{}
	for _, row := range rows {
		rules = append(rules, TargetingRule{
			ID: row.ID, FlagID: row.FlagID, Environment: env,
			RuleType: row.RuleType, Attribute: row.Attribute, Operator: row.Operator,
			Values: row.Values, Variation: row.Variation, Priority: int(row.Priority),
			CreatedAt: row.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"targeting_rules": rules, "total": len(rules)})
}

// DeleteTargetingRule handles DELETE /api/v1/flags/{key}/environments/{env}/rules/{id}
func (h *TargetingRuleHandler) DeleteTargetingRule(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	env := chi.URLParam(r, "env")
	ruleID := chi.URLParam(r, "id")

	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}

	n, err := sqlcgen.New(h.db).DeleteTargetingRule(r.Context(), sqlcgen.DeleteTargetingRuleParams{
		Key: key, Environment: env, ID: ruleID, ProjectID: projectID,
	})
	if err != nil {
		h.logger.Error("delete targeting rule", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "targeting rule not found")
		return
	}
	h.publishTargetingRulesUpdated(r.Context(), key, env, projectID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": ruleID})
}

// SnapshotTargetingRule is the slimmer variant of TargetingRule embedded in
// GetSnapshot's per-flag entry (environments.go) -- omits flag_id (redundant,
// already nested under that flag) and created_at (not needed for in-process
// evaluation), mirroring SnapshotPrerequisite's own relationship to
// Prerequisite exactly.
type SnapshotTargetingRule struct {
	ID        string          `json:"id"`
	RuleType  string          `json:"rule_type"`
	Attribute string          `json:"attribute"`
	Operator  string          `json:"operator"`
	Values    json.RawMessage `json:"values"`
	Variation string          `json:"variation"`
	Priority  int             `json:"priority"`
}

// TargetingRulesEvent is published to the Redis Stream (tombstone:stream:
// {environment}) whenever a flag's targeting-rule set changes IN THAT
// ENVIRONMENT (AddTargetingRule/DeleteTargetingRule), carrying the flag's
// CURRENT FULL rule list for that environment post-mutation -- SDKs apply it
// as a full replacement, not a delta, mirroring PrerequisitesEvent's own
// design (see that struct's doc comment for the reasoning, including the
// disclosed non-fully-race-free SELECT-then-XAdd sequence this inherits
// unchanged).
type TargetingRulesEvent struct {
	FlagKey        string                  `json:"flag_key"`
	Environment    string                  `json:"environment"`
	TargetingRules []SnapshotTargetingRule `json:"targeting_rules"`
	Ts             int64                   `json:"ts"`
}

// targetingRulesEventKind is the Streams-entry discriminator gateway checks
// (via the dedicated "kind" field -- see prerequisitesEventKind's own doc
// comment for why "kind", never "event") to route a TargetingRulesEvent
// differently from a regular FlagEvent.
const targetingRulesEventKind = "targeting_rules_updated"

// publishTargetingRulesUpdated fetches (key, environment)'s current full
// targeting-rule list and publishes a TargetingRulesEvent. Unlike
// publishPrerequisitesUpdated, there is no per-environment fan-out loop --
// targeting_rules already carries its own environment column, so exactly
// one environment is ever affected by one mutation. Fail-soft: a query or
// publish failure is logged and swallowed, matching
// publishPrerequisitesUpdated/publishEvent's own convention -- a broken
// live-update path must never fail the actual mutation request that already
// committed.
func (h *TargetingRuleHandler) publishTargetingRulesUpdated(ctx context.Context, key, environment, projectID string) {
	if h.rdb == nil {
		return
	}
	q := sqlcgen.New(h.db)

	rows, err := q.ListTargetingRulesForFlag(ctx, sqlcgen.ListTargetingRulesForFlagParams{
		Key: key, Environment: environment, ProjectID: projectID,
	})
	if err != nil {
		h.logger.Warn("publish targeting_rules_updated: list query failed",
			zap.String("flag", key), zap.String("environment", environment), zap.Error(err))
		return
	}
	rules := make([]SnapshotTargetingRule, 0, len(rows))
	for _, row := range rows {
		rules = append(rules, SnapshotTargetingRule{
			ID: row.ID, RuleType: row.RuleType, Attribute: row.Attribute, Operator: row.Operator,
			Values: row.Values, Variation: row.Variation, Priority: int(row.Priority),
		})
	}

	publishTargetingRulesEvent(ctx, h.rdb, h.logger, environment, TargetingRulesEvent{
		FlagKey: key, Environment: environment, TargetingRules: rules, Ts: time.Now().Unix(),
	})
}

// publishTargetingRulesEvent XAdds a TargetingRulesEvent to the Streams-only
// path, mirroring publishPrerequisitesEvent exactly (see that function's own
// doc comment for why "kind" is a dedicated field, and why this is
// Streams-only with no legacy pub/sub dual-write).
func publishTargetingRulesEvent(ctx context.Context, rdb *redis.Client, logger *zap.Logger, environment string, event TargetingRulesEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	streamKey := fmt.Sprintf("tombstone:stream:%s", environment)
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]interface{}{
			"kind":        targetingRulesEventKind,
			"event":       targetingRulesEventKind,
			"flag_key":    event.FlagKey,
			"environment": environment,
			"payload":     string(payload),
		},
	}).Err(); err != nil {
		logger.Warn("redis xadd failed", zap.String("stream", streamKey), zap.Error(err))
	}
}
