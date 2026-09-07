package io.tombstone.client;

import io.tombstone.types.EvaluationContext;
import io.tombstone.types.EvaluationReason;
import io.tombstone.types.EvaluationResult;
import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import org.junit.jupiter.api.Test;

import java.io.BufferedReader;
import java.io.StringReader;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/** End-to-end regression suite for the SDK-4 prerequisites-streaming
 *  follow-up: a live "prerequisites_updated" SSE frame (services/flag-api/
 *  internal/api/v1/prerequisites.go's PrerequisitesEvent, relayed verbatim
 *  by the gateway) must actually change what evaluate() returns for the
 *  affected flag. Drives TombstoneClient.applyPrerequisitesEvent(String)
 *  directly with a hand-built JSON string shaped exactly like the real
 *  wire event, via the package-private test seam (mirrors
 *  parseSnapshotResponse's own test-seam visibility, same reasoning).
 *
 *  Also proactively closes the exact gap PR #235/#236's own adversarial
 *  reviews found: a staleness test that only uses a "clearly older" ts
 *  cannot distinguish a correct "&lt;" comparison from a buggy "&lt;="
 *  regression, since both reject that input identically. The "ts equal to
 *  the cached value" test below is the one that actually pins the "&lt;"
 *  behavior down. */
public class TombstoneClientPrerequisitesEventTest {

    private static TombstoneClient newClient() {
        return new TombstoneClient("test-key", "test", "http://api.invalid", "http://gw.invalid", Map.of());
    }

    private static List<FlagEnvironmentState> parentAndChild() {
        return List.of(
            new FlagEnvironmentState("1", "parent-flag", "test", false, 0, "false", 0L,
                List.of(), List.of(), List.of(), 1, 0L, 0L),
            new FlagEnvironmentState("2", "child-flag", "test", true, 100, "false", 0L,
                List.of(), List.of(), List.of(), 1, 0L, 0L)
        );
    }

    @Test
    void aLiveEventNewerThanTheSnapshotChangesEvaluatesOutcome() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 1000);

        // Before the live event: child-flag has no prerequisites, so it
        // evaluates by rollout alone.
        EvaluationResult<Boolean> before = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(Boolean.TRUE, before.value());
        assertNotEquals(EvaluationReason.PREREQUISITE_FAILED, before.reason());

        client.applyPrerequisitesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],
             "ts":2000}
            """);

        EvaluationResult<Boolean> after = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(EvaluationReason.PREREQUISITE_FAILED, after.reason(),
            "the live event must actually be applied to the cache and change evaluate()'s outcome");
        assertEquals(Boolean.FALSE, after.value());
    }

    @Test
    void anEventOlderThanTheCachedTsIsRejected() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 5000);

        client.applyPrerequisitesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],
             "ts":3000}
            """);

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertNotEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason(),
            "a stale out-of-order event must not overwrite the newer cached state");
        assertEquals(Boolean.TRUE, result.value());
    }

    @Test
    void anEventWithTsEqualToTheCachedTsIsApplied() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 5000);

        client.applyPrerequisitesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],
             "ts":5000}
            """);

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason(),
            "an equal-ts event must be applied, not dropped as stale");
        assertEquals(Boolean.FALSE, result.value());
    }

    @Test
    void anUpdateForAFlagNeverSeenIsANoOp() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 1000);

        assertDoesNotThrow(() -> client.applyPrerequisitesEvent("""
            {"flag_key":"never-configured-flag","environment":"test",
             "prerequisites":[{"flag_key":"parent-flag","required_variation":"true"}],
             "ts":9999}
            """));

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertNotEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason());
    }

    @Test
    void malformedJsonIsSwallowedNotThrown() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 1000);

        assertDoesNotThrow(() -> client.applyPrerequisitesEvent("not valid json{{{"));
    }

    @Test
    void gateOmittedOnTheWireDefaultsToTrue() {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 1000);

        client.applyPrerequisitesEvent("""
            {"flag_key":"child-flag","environment":"test",
             "prerequisites":[{"flag_key":"parent-flag","required_variation":"true"}],
             "ts":2000}
            """);

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason(),
            "gate omitted on the wire must default to true (hard-blocking), matching flag-api's own AddPrerequisite default");
    }

    @Test
    void anEmptyPrerequisitesListClearsAFlagsExistingGates() {
        // applyPrerequisitesEvent is documented as a full replacement, not a
        // delta -- an empty incoming list must actually clear a flag's
        // existing (non-empty) gates, not be mistaken for "nothing to apply".
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(List.of(
            new FlagEnvironmentState("1", "parent-flag", "test", false, 0, "false", 0L,
                List.of(), List.of(), List.of(), 1, 0L, 0L),
            new FlagEnvironmentState("2", "child-flag", "test", true, 100, "false", 0L,
                List.of(new FlagPrerequisite("parent-flag", "true", true)),
                List.of(), List.of(), 1, 1000L, 0L)
        ), 1000);

        // Confirm the gate is active before clearing it.
        assertEquals(EvaluationReason.PREREQUISITE_FAILED,
            client.evaluate("child-flag", EvaluationContext.of("u1")).reason());

        client.applyPrerequisitesEvent("""
            {"flag_key":"child-flag","environment":"test","prerequisites":[],"ts":2000}
            """);

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertNotEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason());
        assertEquals(Boolean.TRUE, result.value());
    }

    // Drives processSseLines(BufferedReader) directly with hand-built,
    // real SSE-formatted text -- the ACTUAL "event:"/"data:" line parsing
    // and eventType dispatch, not a bypass of it. Found by adversarial
    // review of this PR: every test above (and TombstoneClientLagTest's
    // own scheduleLagRefetch() calls) drove a downstream handler directly,
    // leaving the dispatch logic itself with zero coverage.
    @Test
    void theRealSseDispatchRoutesAPrerequisitesUpdatedFrameCorrectly() throws Exception {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 1000);
        client.setConnectedForTesting(true);

        String sse = "event: prerequisites_updated\n" +
            "data: {\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
            "\"prerequisites\":[{\"flag_key\":\"parent-flag\",\"required_variation\":\"true\",\"gate\":true}],\"ts\":2000}\n" +
            "\n";
        client.processSseLines(new BufferedReader(new StringReader(sse)));

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason(),
            "the real dispatch loop must route a prerequisites_updated frame to applyPrerequisitesEvent");
    }

    // Proves the blank-line eventType reset actually works via the REAL
    // dispatch loop: a prerequisites_updated frame (which sets a hard gate)
    // followed by a SECOND, ordinary flag-update frame (no "event:" line at
    // all -- the gateway's real default) must NOT have the second frame
    // misrouted to applyPrerequisitesEvent just because eventType leaked
    // from the first frame. If it leaked, the second frame's JSON (which
    // has no "prerequisites" key) would parse as an empty list via
    // parsePrerequisites(null) and, since its ts=3000 is newer than the
    // gate's ts=2000, would be ACCEPTED and wrongly CLEAR the just-set
    // gate -- applyEvent, by contrast, explicitly preserves prerequisites
    // unchanged, so correct routing leaves the gate active.
    @Test
    void eventTypeResetsBetweenFramesSoASubsequentPlainUpdateIsNotMisrouted() throws Exception {
        TombstoneClient client = newClient();
        client.loadSnapshotForTesting(parentAndChild(), 1000);
        client.setConnectedForTesting(true);

        String sse = "event: prerequisites_updated\n" +
            "data: {\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
            "\"prerequisites\":[{\"flag_key\":\"parent-flag\",\"required_variation\":\"true\",\"gate\":true}],\"ts\":2000}\n" +
            "\n" +
            "data: {\"flag_key\":\"child-flag\",\"enabled\":true,\"rollout_pct\":100,\"ts\":3000}\n" +
            "\n";
        client.processSseLines(new BufferedReader(new StringReader(sse)));

        EvaluationResult<Boolean> result = client.evaluate("child-flag", EvaluationContext.of("u1"));
        assertEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason(),
            "the plain flag-update frame after the blank line must be routed to applyEvent (which preserves "
                + "prerequisites), not misrouted to applyPrerequisitesEvent (which would wrongly clear the gate) "
                + "-- proving eventType actually resets between frames");
    }
}
