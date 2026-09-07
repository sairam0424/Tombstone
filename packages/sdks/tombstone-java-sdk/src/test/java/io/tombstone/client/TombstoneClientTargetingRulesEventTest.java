package io.tombstone.client;

import io.tombstone.types.EvaluationContext;
import io.tombstone.types.EvaluationReason;
import io.tombstone.types.EvaluationResult;
import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.PropertyCondition;
import io.tombstone.types.TargetingRule;
import org.junit.jupiter.api.Test;

import java.io.BufferedReader;
import java.io.StringReader;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/** End-to-end regression suite for the targeting_rules-streaming follow-up:
 *  a live "targeting_rules_updated" SSE frame (services/flag-api/internal/
 *  api/v1/targeting_rules.go's TargetingRulesEvent, relayed verbatim by the
 *  gateway) must actually change what evaluate() returns for the affected
 *  flag. Mirrors TombstoneClientPrerequisitesEventTest.java exactly, using
 *  a RULE_MATCH scenario instead of PREREQUISITE_FAILED. Drives
 *  TombstoneClient.applyTargetingRulesEvent(String) directly via the
 *  package-private test seam.
 *
 *  defaults maps "child-flag" to a String ("off") rather than leaving the
 *  Boolean.FALSE fallback default() uses -- otherwise T would be inferred
 *  as Boolean at the call site while a matched rule's variation ("matched")
 *  is a String, an avoidable type mismatch unrelated to what these tests
 *  actually exercise. */
public class TombstoneClientTargetingRulesEventTest {

    private static TombstoneClient newClient() {
        return new TombstoneClient(
            "test-key", "test", "http://api.invalid", "http://gw.invalid",
            Map.of("child-flag", "off")
        );
    }

    private static EvaluationContext matchingContext() {
        return new EvaluationContext("u1", "", Map.of("email", "x@example.com"));
    }

    private static List<FlagEnvironmentState> childFlagWithNoRules() {
        return List.of(new FlagEnvironmentState(
            "1", "child-flag", "test", true, 0, "off", 0L,
            List.of(), List.of(), List.of(), 1, 0L, 0L
        ));
    }

    @Test
    void aLiveEventNewerThanTheSnapshotChangesEvaluatesOutcome() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 1000);

        // Before the live event: child-flag has no targeting rules and 0%
        // rollout, so it falls through to the default value.
        EvaluationResult<String> before = client.evaluate("child-flag", matchingContext());
        assertNotEquals(EvaluationReason.RULE_MATCH, before.reason());

        client.applyTargetingRulesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "targeting_rules":[{"id":"r1","rule_type":"USER","attribute":"email",
             "operator":"EQ","values":["x@example.com"],"variation":"matched","priority":0}],
             "ts":2000}
            """);

        EvaluationResult<String> after = client.evaluate("child-flag", matchingContext());
        assertEquals(EvaluationReason.RULE_MATCH, after.reason(),
            "the live event must actually be applied to the cache and change evaluate()'s outcome");
        assertEquals("matched", after.value());
    }

    @Test
    void anEventOlderThanTheCachedTsIsRejected() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 5000);

        client.applyTargetingRulesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "targeting_rules":[{"id":"r1","rule_type":"USER","attribute":"email",
             "operator":"EQ","values":["x@example.com"],"variation":"matched","priority":0}],
             "ts":3000}
            """);

        EvaluationResult<String> result = client.evaluate("child-flag", matchingContext());
        assertNotEquals(EvaluationReason.RULE_MATCH, result.reason(),
            "a stale out-of-order event must not overwrite the newer cached state");
    }

    @Test
    void anEventWithTsEqualToTheCachedTsIsApplied() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 5000);

        client.applyTargetingRulesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "targeting_rules":[{"id":"r1","rule_type":"USER","attribute":"email",
             "operator":"EQ","values":["x@example.com"],"variation":"matched","priority":0}],
             "ts":5000}
            """);

        EvaluationResult<String> result = client.evaluate("child-flag", matchingContext());
        assertEquals(EvaluationReason.RULE_MATCH, result.reason(),
            "an equal-ts event must be applied, not dropped as stale");
        assertEquals("matched", result.value());
    }

    @Test
    void anUpdateForAFlagNeverSeenIsANoOp() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 1000);

        assertDoesNotThrow(() -> client.applyTargetingRulesEvent("""
            {"flag_key":"never-configured-flag","environment":"test",
             "targeting_rules":[{"id":"r1","rule_type":"USER","attribute":"email",
             "operator":"EQ","values":["x@example.com"],"variation":"matched","priority":0}],
             "ts":9999}
            """));

        EvaluationResult<String> result = client.evaluate("child-flag", matchingContext());
        assertNotEquals(EvaluationReason.RULE_MATCH, result.reason());
    }

    @Test
    void malformedJsonIsSwallowedNotThrown() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 1000);

        assertDoesNotThrow(() -> client.applyTargetingRulesEvent("not valid json{{{"));
    }

    @Test
    void anEmptyTargetingRulesListClearsAFlagsExistingRules() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(List.of(new FlagEnvironmentState(
            "1", "child-flag", "test", true, 0, "off", 0L,
            List.of(),
            List.of(new TargetingRule(
                "r1",
                List.of(new PropertyCondition("email", "eq", List.of("x@example.com"), false)),
                100.0, "matched", 0
            )),
            List.of(), 1, 0L, 1000L
        )), 1000);

        // Confirm the rule is active before clearing it.
        assertEquals(EvaluationReason.RULE_MATCH,
            client.evaluate("child-flag", matchingContext()).reason());

        client.applyTargetingRulesEvent("""
            {"flag_key":"child-flag","environment":"test","targeting_rules":[],"ts":2000}
            """);

        EvaluationResult<String> result = client.evaluate("child-flag", matchingContext());
        assertNotEquals(EvaluationReason.RULE_MATCH, result.reason());
    }

    // Drives processSseLines(BufferedReader) directly with hand-built, real
    // SSE-formatted text -- mirrors
    // theRealSseDispatchRoutesAPrerequisitesUpdatedFrameCorrectly exactly.
    @Test
    void theRealSseDispatchRoutesATargetingRulesUpdatedFrameCorrectly() throws Exception {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 1000);
        client.setConnectedForTesting(true);

        String sse = "event: targeting_rules_updated\n" +
            "data: {\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
            "\"targeting_rules\":[{\"id\":\"r1\",\"rule_type\":\"USER\",\"attribute\":\"email\"," +
            "\"operator\":\"EQ\",\"values\":[\"x@example.com\"],\"variation\":\"matched\",\"priority\":0}],\"ts\":2000}\n" +
            "\n";
        client.processSseLines(new BufferedReader(new StringReader(sse)));

        EvaluationResult<String> result = client.evaluate("child-flag", matchingContext());
        assertEquals(EvaluationReason.RULE_MATCH, result.reason(),
            "the real dispatch loop must route a targeting_rules_updated frame to applyTargetingRulesEvent");
    }

    // Mirrors eventTypeResetsBetweenFramesSoASubsequentPlainUpdateIsNotMisrouted
    // exactly: a targeting_rules_updated frame followed by a SECOND, plain
    // flag-update frame (no "event:" line) must NOT have the second frame
    // misrouted to applyTargetingRulesEvent -- if it leaked, the second
    // frame's JSON (no "targeting_rules" key) would parse as an empty list
    // via parseTargetingRules(null) and, since its ts=3000 is newer, would
    // wrongly CLEAR the just-set rule.
    @Test
    void eventTypeResetsBetweenFramesSoASubsequentPlainUpdateIsNotMisrouted() throws Exception {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(childFlagWithNoRules(), 1000);
        client.setConnectedForTesting(true);

        String sse = "event: targeting_rules_updated\n" +
            "data: {\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
            "\"targeting_rules\":[{\"id\":\"r1\",\"rule_type\":\"USER\",\"attribute\":\"email\"," +
            "\"operator\":\"EQ\",\"values\":[\"x@example.com\"],\"variation\":\"matched\",\"priority\":0}],\"ts\":2000}\n" +
            "\n" +
            "data: {\"flag_key\":\"child-flag\",\"enabled\":true,\"rollout_pct\":0,\"ts\":3000}\n" +
            "\n";
        client.processSseLines(new BufferedReader(new StringReader(sse)));

        EvaluationResult<String> result = client.evaluate("child-flag", matchingContext());
        assertEquals(EvaluationReason.RULE_MATCH, result.reason(),
            "the plain flag-update frame after the blank line must be routed to applyEvent (which preserves "
                + "targetingRules), not misrouted to applyTargetingRulesEvent (which would wrongly clear the rule) "
                + "-- proving eventType actually resets between frames");
    }
}
