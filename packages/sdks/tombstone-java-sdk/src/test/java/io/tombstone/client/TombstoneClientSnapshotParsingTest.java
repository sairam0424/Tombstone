package io.tombstone.client;

import io.tombstone.types.EvaluationContext;
import io.tombstone.types.EvaluationReason;
import io.tombstone.types.EvaluationResult;
import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/** Regression suite for a bug found while investigating SDK-4's
 *  prerequisites-streaming follow-up: fetchSnapshot() built every real
 *  FlagEnvironmentState via FlagEnvironmentState.simple(...), which
 *  hardcodes prerequisites (and targetingRules/targetList/hashVersion) to
 *  empty/default regardless of what the wire actually sent -- this client's
 *  prerequisite gating never worked against a real backend at all. Exercises
 *  the real parseSnapshotResponse(String) parsing logic directly against a
 *  hand-built JSON string shaped exactly like flag-api's real snapshot
 *  endpoint (services/flag-api/internal/api/v1/environments.go), rather than
 *  standing up a mock HTTP server. */
public class TombstoneClientSnapshotParsingTest {

    private static TombstoneClient newClient() {
        return new TombstoneClient("test-key", "test", "http://api.invalid", "http://gw.invalid", Map.of());
    }

    @Test
    void topLevelFieldsParseCorrectlyFromRealWireJson() throws IOException {
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"1","flag_key":"known-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[]}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertEquals(1, states.size());
        FlagEnvironmentState s = states.get(0);
        assertEquals("1", s.flagId());
        assertEquals("known-flag", s.flagKey());
        assertEquals("production", s.environment());
        assertTrue(s.enabled());
        assertEquals(100, s.rolloutPct());
        assertEquals("false", s.safeDefault());
        assertEquals(1700000000L, s.updatedAt());
    }

    @Test
    void prerequisitesParseCorrectlyFromRealWireJson() throws IOException {
        // flag-api's real per-prerequisite wire shape: "flag_key" (NOT
        // "prereq_flag_key" -- that's only the DB column name), plus
        // "required_variation"/"gate"/"priority".
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[
                 {"id":"prereq-1","flag_key":"parent-flag","required_variation":"true","gate":true,"priority":0}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertEquals(1, states.size());
        List<FlagPrerequisite> prereqs = states.get(0).prerequisites();
        assertEquals(1, prereqs.size());
        assertEquals("parent-flag", prereqs.get(0).flagKey());
        assertEquals("true", prereqs.get(0).requiredVariation());
        assertTrue(prereqs.get(0).gate());
    }

    @Test
    void gateOmittedOnTheWireDefaultsToTrue() throws IOException {
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[
                 {"flag_key":"parent-flag","required_variation":"true"}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertTrue(states.get(0).prerequisites().get(0).gate(),
            "gate must default to true (hard-blocking) when the wire omits it, matching flag-api's own AddPrerequisite default");
    }

    @Test
    void explicitGateFalseParsesAsSoft() throws IOException {
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[
                 {"flag_key":"parent-flag","required_variation":"true","gate":false}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertFalse(states.get(0).prerequisites().get(0).gate());
    }

    @Test
    void flagWithNoPrerequisitesFieldAtAllParsesAsEmptyNotAnError() throws IOException {
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"1","flag_key":"known-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertEquals(List.of(), states.get(0).prerequisites());
    }

    // Mirrors prerequisitesParseCorrectlyFromRealWireJson exactly, for
    // targeting_rules (added by flag-api PR #245). flag-api's real
    // per-rule wire shape: "id"/"rule_type"/"attribute"/"operator"/
    // "values"/"variation"/"priority" -- ONE condition per rule row,
    // adapted into this SDK's own richer TargetingRule(conditions, rolloutPct,
    // variation, priority) model by parseTargetingRules (see that method's
    // own doc comment for why rolloutPct is always 100 for a wire-parsed rule).
    @Test
    void targetingRulesParseCorrectlyFromRealWireJson() throws IOException {
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "targeting_rules":[
                 {"id":"rule-1","rule_type":"USER","attribute":"email","operator":"CONTAINS",
                  "values":["@acme.com"],"variation":"true","priority":3}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertEquals(1, states.size());
        List<io.tombstone.types.TargetingRule> rules = states.get(0).targetingRules();
        assertEquals(1, rules.size());
        assertEquals("rule-1", rules.get(0).id());
        assertEquals("true", rules.get(0).variation());
        assertEquals(3, rules.get(0).priority());
        assertEquals(100.0, rules.get(0).rolloutPct());
        assertEquals(1, rules.get(0).conditions().size());
        assertEquals("email", rules.get(0).conditions().get(0).attribute());
        assertEquals("CONTAINS", rules.get(0).conditions().get(0).operator());
        assertEquals(List.of("@acme.com"), rules.get(0).conditions().get(0).values());
    }

    @Test
    void targetingRuleNumericValuesParseAsTheirStringRepresentation() throws IOException {
        // "values" can carry JSON numbers (e.g. for GT/GTE/LT/LTE operators)
        // -- must round-trip as the plain string form Double.parseDouble
        // can consume, not e.g. "18.0" for an integer 18.
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "targeting_rules":[
                 {"id":"rule-1","rule_type":"CUSTOM","attribute":"age","operator":"GTE",
                  "values":[18],"variation":"true","priority":0}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        var condition = states.get(0).targetingRules().get(0).conditions().get(0);
        assertEquals(List.of("18"), condition.values());
        assertTrue(io.tombstone.evaluation.RuleMatcher.evaluateCondition(
            condition, new EvaluationContext("u1", "", Map.of("age", "21"))),
            "the round-tripped numeric value must still be usable by evaluateNumeric");
    }

    @Test
    void flagWithNoTargetingRulesFieldAtAllParsesAsEmptyNotAnError() throws IOException {
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"1","flag_key":"known-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000}
            ],"hash":"h","ts":1700000000}
            """;
        List<FlagEnvironmentState> states = newClient().parseSnapshotResponse(json).flags();
        assertEquals(List.of(), states.get(0).targetingRules());
    }

    /** Regression suite for a SECOND bug, found by adversarial review of this
     *  PR's own fix above: evaluate() called EvaluationEngine's 4-arg
     *  convenience overload, which hardcodes flagLookup to `key -> null` --
     *  documented on that overload as being for "callers with no snapshot
     *  access". TombstoneClient DOES have snapshot access via its own cache,
     *  but never threaded it through. Before real prerequisites existed
     *  (fetchSnapshot always built FlagEnvironmentState.simple(), which
     *  hardcodes prerequisites to empty), this was dead code. Once
     *  prerequisites are real (this PR's other fix), a null-returning
     *  lookup makes EVERY hard-gated prerequisite permanently
     *  PREREQUISITE_FAILED regardless of the real dependency's state --
     *  worse than the original bug, not better. These tests drive the real,
     *  public evaluate()/isEnabled() entry points end to end (via the
     *  package-private loadSnapshotForTesting seam), not
     *  PrerequisiteChecker.checkAll or the 7-arg EvaluationEngine.evaluate()
     *  directly -- neither of which would have caught this, since both
     *  bypass TombstoneClient's own wiring entirely. */
    @Test
    void evaluateResolvesARealSatisfiedHardGatedPrerequisiteFromItsOwnCache() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(List.of(
            new FlagEnvironmentState("1", "parent-flag", "test", true, 100, "false", 0L,
                List.of(), List.of(), List.of(), 1, 0L, 0L),
            new FlagEnvironmentState("2", "child-flag", "test", true, 100, "false", 0L,
                List.of(new FlagPrerequisite("parent-flag", "true", true)),
                List.of(), List.of(), 1, 0L, 0L)
        ));

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(Boolean.TRUE, result.value());
        assertNotEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason());
    }

    @Test
    void evaluateBlocksOnAGenuinelyUnmetHardGatedPrerequisite() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(List.of(
            new FlagEnvironmentState("1", "parent-flag", "test", false, 0, "false", 0L,
                List.of(), List.of(), List.of(), 1, 0L, 0L),
            new FlagEnvironmentState("2", "child-flag", "test", true, 100, "false", 0L,
                List.of(new FlagPrerequisite("parent-flag", "true", true)),
                List.of(), List.of(), 1, 0L, 0L)
        ));

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason());
    }

    // End-to-end proof that a targeting rule parsed from a REAL snapshot
    // response (via parseSnapshotResponse, not a hand-built
    // FlagEnvironmentState) actually reaches evaluate() and changes its
    // outcome -- mirrors the two prerequisite tests above exactly, closing
    // the identical gap for targeting_rules.
    @Test
    void evaluateResolvesARealRuleMatchFromASnapshotParsedByTheRealWireParser() throws IOException {
        TombstoneClient client = new TombstoneClient(
            "test-key", "test", "http://api.invalid", "http://gw.invalid",
            Map.of("child-flag", "off")
        );
        String json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":0,"safe_default":"off","updated_at":1700000000,
               "targeting_rules":[
                 {"id":"rule-1","rule_type":"USER","attribute":"email","operator":"EQ",
                  "values":["x@example.com"],"variation":"matched","priority":0}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        var parsed = client.parseSnapshotResponse(json);
        client.loadSnapshotForTesting(parsed.flags(), parsed.ts());

        EvaluationResult<String> result = client.evaluate(
            "child-flag", new EvaluationContext("u1", "", Map.of("email", "x@example.com")));
        assertEquals(EvaluationReason.RULE_MATCH, result.reason(),
            "a targeting rule parsed from a real snapshot response must actually be evaluated, not silently dropped");
        assertEquals("matched", result.value());
    }
}
