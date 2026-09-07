-- targeting_rules has existed since the baseline schema (schema.sql) but was
-- pure scaffolding: no query file, no REST endpoint, no snapshot wiring, no
-- SSE event -- confirmed via a full repo scan while scoping the
-- targeting_rules/target_list real-time propagation gap. This migration
-- brings its table definition in line with its sibling flag_prerequisites
-- (migration 006, inline in schema.sql) before any of that surface is built:
--
-- created_at: flag_prerequisites has one (used in its API response and its
-- ORDER BY ... created_at ASC tiebreak for equal-priority rows); targeting_rules
-- had none, so two rules with the same priority had no deterministic order.
ALTER TABLE targeting_rules ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- flag_prerequisites is looked up by flag_id alone (idx_flag_prerequisites_flag_id,
-- schema.sql) since it has no environment column. targeting_rules IS scoped
-- per-environment (unlike prerequisites), and every query added by this
-- feature filters on both flag_id and environment together -- see
-- queries/targeting_rules.sql's ListTargetingRulesForFlag/DeleteTargetingRule
-- and queries/environments.sql's GetEnvironmentSnapshotTargetingRules.
CREATE INDEX IF NOT EXISTS idx_targeting_rules_flag_env ON targeting_rules(flag_id, environment);
