/**
 * FlagCache -- direct unit tests for the three races/edge-cases confirmed by
 * PR #236's adversarial review, none of which client.test.ts's end-to-end
 * suite (which drives everything through TombstoneClient's real fetch/SSE
 * plumbing) can deterministically reproduce, since they depend on the exact
 * ORDER two async operations resolve in -- easy to construct directly
 * against FlagCache itself, hard to force through a real (fake) network
 * race without a controllable-resolution-order fetch stub.
 */
import { strict as assert } from "assert";
import { FlagCache } from "../cache.js";
import type { FlagPrerequisite, FlagSnapshot } from "../types.js";

function snapshotWith(
  ts: number,
  prerequisites: FlagPrerequisite[] = [],
): FlagSnapshot {
  return {
    environment: "production",
    hash: "h",
    ts,
    flags: [
      {
        flagId: "1",
        flagKey: "child-flag",
        environment: "production",
        enabled: true,
        rolloutPct: 100,
        safeDefault: "false",
        updatedAt: ts,
        prerequisites,
      },
    ],
  };
}

describe("FlagCache — applyPrerequisitesEvent NaN-safety", () => {
  it("rejects a non-numeric ts outright instead of storing NaN into prerequisitesUpdatedAt", () => {
    const cache = new FlagCache();
    cache.loadSnapshot(
      snapshotWith(5000, [
        { flagKey: "parent-flag", requiredVariation: "true", gate: true },
      ]),
    );

    cache.applyPrerequisitesEvent(
      "child-flag",
      [{ flagKey: "corrupt-parent", requiredVariation: "true", gate: true }],
      NaN,
    );

    const state = cache.get("child-flag");
    assert.equal(
      state?.prerequisitesUpdatedAt,
      5000,
      "a NaN ts must be rejected -- prerequisitesUpdatedAt must stay at its last real value, not become NaN",
    );
    assert.deepEqual(
      state?.prerequisites,
      [{ flagKey: "parent-flag", requiredVariation: "true", gate: true }],
      "the NaN event's prerequisites must not have been applied",
    );
  });

  it("a genuinely stale event arriving AFTER a rejected NaN event is still correctly rejected -- proves NaN never corrupted the staleness floor", () => {
    /**
     * The actual failure mode the review found: once prerequisitesUpdatedAt
     * becomes NaN, EVERY future comparison against it is false (NaN < x and
     * x < NaN are both always false), so the staleness guard is permanently
     * defeated. This test proves that door stays shut -- a real ts of 1000
     * (older than the 5000 baseline) must still be rejected even after a
     * NaN event was thrown at the cache first.
     */
    const cache = new FlagCache();
    cache.loadSnapshot(
      snapshotWith(5000, [
        { flagKey: "parent-flag", requiredVariation: "true", gate: true },
      ]),
    );

    cache.applyPrerequisitesEvent(
      "child-flag",
      [{ flagKey: "corrupt-parent", requiredVariation: "true", gate: true }],
      NaN,
    );
    cache.applyPrerequisitesEvent(
      "child-flag",
      [{ flagKey: "stale-parent", requiredVariation: "true", gate: true }],
      1000, // older than the 5000 baseline -- must still be rejected
    );

    const state = cache.get("child-flag");
    assert.deepEqual(
      state?.prerequisites,
      [{ flagKey: "parent-flag", requiredVariation: "true", gate: true }],
      "a real stale event must still be rejected after a NaN event -- the guard must not have been permanently defeated",
    );
    assert.equal(state?.prerequisitesUpdatedAt, 5000);
  });
});

describe("FlagCache — loadSnapshot monotonicity (rejects an out-of-order OLDER snapshot)", () => {
  it("rejects an incoming snapshot whose own ts is older than the last one actually applied", () => {
    /**
     * Simulates two overlapping fetchSnapshot() calls (e.g. a lag-triggered
     * refetch racing an onReconnect-triggered one, both fire-and-forget per
     * client.ts) resolving out of order: the slower one has an OLDER ts but
     * resolves SECOND. Before this fix, loadSnapshot() had no memory of the
     * previously-applied snapshot's own ts and would blindly overwrite the
     * whole cache with the older data.
     */
    const cache = new FlagCache();
    cache.loadSnapshot(snapshotWith(5000));
    cache.loadSnapshot(snapshotWith(3000)); // older -- must be rejected wholesale

    // getHash() and the flag's own updatedAt both come from whichever
    // snapshot actually won -- if the older one had wrongly applied, this
    // flag's updatedAt would read 3000, not 5000.
    assert.equal(
      cache.get("child-flag")?.updatedAt,
      5000,
      "an older snapshot must not overwrite already-applied newer data",
    );
  });

  it("still applies a genuinely newer snapshot after an older one was rejected", () => {
    const cache = new FlagCache();
    cache.loadSnapshot(snapshotWith(5000));
    cache.loadSnapshot(snapshotWith(3000)); // rejected
    cache.loadSnapshot(snapshotWith(7000)); // genuinely newer -- must apply

    assert.equal(cache.get("child-flag")?.updatedAt, 7000);
  });

  it("the very first loadSnapshot call always applies regardless of ts", () => {
    const cache = new FlagCache();
    cache.loadSnapshot(snapshotWith(1)); // ts=1 -- no prior snapshot to compare against
    assert.equal(cache.get("child-flag")?.updatedAt, 1);
  });
});

describe("FlagCache — loadSnapshot preserves a fresher live prerequisites update", () => {
  it("does not regress a flag's prerequisites when a newer-but-still-behind-the-live-update snapshot reloads", () => {
    /**
     * flag-api's snapshot.ts is just time.Now() at response generation, not
     * a per-flag "last changed" value. So even a snapshot that correctly
     * passes the whole-snapshot monotonicity check above can still carry
     * STALE prerequisite data for one specific flag, if that flag's
     * prerequisites were advanced by a live event that arrived and applied
     * WHILE this snapshot's own (slower) HTTP request was in flight.
     */
    const cache = new FlagCache();
    cache.loadSnapshot(
      snapshotWith(1000, [
        { flagKey: "old-parent", requiredVariation: "true", gate: true },
      ]),
    );

    // A live event advances child-flag's prerequisites to ts=2000 while a
    // slower snapshot fetch (started before the live event, ts=1500) is
    // still in flight.
    cache.applyPrerequisitesEvent(
      "child-flag",
      [{ flagKey: "new-parent", requiredVariation: "true", gate: true }],
      2000,
    );

    // The slower snapshot now resolves. Its own ts=1500 is newer than the
    // cache's LAST SNAPSHOT ts (1000), so it passes the whole-snapshot
    // monotonicity guard -- but it still carries the OLD (pre-live-update)
    // prerequisites for this flag, since the server generated it before the
    // live update landed.
    cache.loadSnapshot(
      snapshotWith(1500, [
        { flagKey: "old-parent", requiredVariation: "true", gate: true },
      ]),
    );

    const state = cache.get("child-flag");
    assert.deepEqual(
      state?.prerequisites,
      [{ flagKey: "new-parent", requiredVariation: "true", gate: true }],
      "the already-fresher live update must survive a slower, stale-for-this-flag snapshot reload",
    );
    assert.equal(
      state?.prerequisitesUpdatedAt,
      2000,
      "prerequisitesUpdatedAt must not regress from 2000 back to the snapshot's 1500",
    );
    // The flag's OTHER fields (enabled, updatedAt, etc.) must still come
    // from the new snapshot -- only prerequisites/prerequisitesUpdatedAt are
    // specially preserved.
    assert.equal(state?.updatedAt, 1500);
  });

  it("preserves the live update on an exact ts tie", () => {
    /**
     * flag-api's snapshot endpoint (environments.go: Ts: time.Now().Unix())
     * and its prerequisites-event publisher (prerequisites.go: ts :=
     * time.Now().Unix()) both use the SAME 1-second-resolution wall clock,
     * so a live event and a racing/in-flight snapshot fetch that land in
     * the same wall-clock second get an IDENTICAL ts.
     * applyPrerequisitesEvent's own staleness guard uses strict "<",
     * meaning it treats an equal ts as "fresh enough to apply" -- this
     * preservation check must treat the SAME tie as "fresh enough to
     * keep" (i.e. use ">=", not ">"), or the snapshot silently overwrites
     * the just-applied live update purely because of a tie. Found by
     * adversarial review of the Ruby SDK's identical bug, PR #238.
     */
    const cache = new FlagCache();
    cache.loadSnapshot(
      snapshotWith(1000, [
        { flagKey: "old-parent", requiredVariation: "true", gate: true },
      ]),
    );
    cache.applyPrerequisitesEvent(
      "child-flag",
      [{ flagKey: "new-parent", requiredVariation: "true", gate: true }],
      2000,
    );

    // The in-flight snapshot resolves with the EXACT SAME ts as the live
    // update that already applied.
    cache.loadSnapshot(
      snapshotWith(2000, [
        { flagKey: "old-parent", requiredVariation: "true", gate: true },
      ]),
    );

    const state = cache.get("child-flag");
    assert.deepEqual(
      state?.prerequisites,
      [{ flagKey: "new-parent", requiredVariation: "true", gate: true }],
      "a tied ts must not let the snapshot silently overwrite the already-applied live update",
    );
    assert.equal(state?.prerequisitesUpdatedAt, 2000);
  });

  it("a SECOND snapshot sharing the exact same ts as a FIRST snapshot (no live event at all) still applies its own data", () => {
    /**
     * Regression test for a real bug the >= tie-break above introduced in
     * its own first draft (found by adversarial review of that fix): with
     * only a ts comparison, this cache cannot tell "existing.
     * prerequisitesUpdatedAt came from a live event that must win a tie"
     * from "existing.prerequisitesUpdatedAt came from a PRIOR SNAPSHOT LOAD
     * that merely happens to share flag-api's coarse 1-second-resolution ts
     * with a SECOND, later snapshot". Without prerequisitesFromLiveEvent
     * tracking, this second snapshot's genuinely different prerequisites
     * would be silently discarded and the cache would stay frozen on the
     * first snapshot's value forever (until a snapshot with a strictly
     * later ts eventually arrives) -- even though every OTHER field on the
     * same flag (enabled, updatedAt, etc.) correctly takes the second
     * snapshot's value in the very same call.
     */
    const cache = new FlagCache();
    cache.loadSnapshot(
      snapshotWith(5000, [
        { flagKey: "parent-a", requiredVariation: "true", gate: true },
      ]),
    );

    // A second, completely independent snapshot fetch resolves with the
    // EXACT SAME ts (flag-api's snapshot.ts is Time.now().Unix() at
    // response generation -- two requests within the same wall-clock
    // second get an identical value) but genuinely different data. No
    // live prerequisites_updated event is involved anywhere in this test.
    cache.loadSnapshot(
      snapshotWith(5000, [
        { flagKey: "parent-b", requiredVariation: "true", gate: true },
      ]),
    );

    const state = cache.get("child-flag");
    assert.deepEqual(
      state?.prerequisites,
      [{ flagKey: "parent-b", requiredVariation: "true", gate: true }],
      "a second snapshot's own prerequisites must apply even on a ts tie with the first snapshot, since no live event is involved",
    );
    assert.equal(state?.prerequisitesUpdatedAt, 5000);
  });

  it("a snapshot NEWER than the live update's own ts is applied normally -- no stale preservation needed", () => {
    const cache = new FlagCache();
    cache.loadSnapshot(
      snapshotWith(1000, [
        { flagKey: "old-parent", requiredVariation: "true", gate: true },
      ]),
    );
    cache.applyPrerequisitesEvent(
      "child-flag",
      [{ flagKey: "new-parent", requiredVariation: "true", gate: true }],
      2000,
    );

    // This snapshot's ts (3000) is NEWER than the live update's ts (2000) --
    // it genuinely reflects a later point in time, so it should win outright.
    cache.loadSnapshot(
      snapshotWith(3000, [
        { flagKey: "even-newer-parent", requiredVariation: "true", gate: true },
      ]),
    );

    const state = cache.get("child-flag");
    assert.deepEqual(state?.prerequisites, [
      { flagKey: "even-newer-parent", requiredVariation: "true", gate: true },
    ]);
    assert.equal(state?.prerequisitesUpdatedAt, 3000);
  });
});
