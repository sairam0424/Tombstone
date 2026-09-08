/**
 * @tomb-stone/edge — client.ts tests: normalizeSnapshot's prerequisite
 * parsing plus an end-to-end EdgeFlagClient.evaluate() prerequisite check.
 *
 * flag-api's real snapshot response is snake_case (flag_key, rollout_pct,
 * safe_default, and per-prerequisite flag_key/required_variation/gate) —
 * normalizeSnapshot is not exported, so these tests drive it indirectly
 * through the public EdgeFlagClient API with a fake KV pre-seeded with raw
 * snake_case JSON, exactly the shape flag-api actually returns.
 */

import assert from "assert";
import { EdgeFlagClient } from "../client.js";
import type { FlagLookup } from "../evaluation.js";
import type { FlagSnapshot } from "../types.js";

class FakeKV {
  private store = new Map<string, string>();

  async get(key: string, type?: "json" | "text"): Promise<unknown> {
    const raw = this.store.get(key);
    if (raw === undefined) return null;
    return type === "text" ? raw : JSON.parse(raw);
  }

  async put(key: string, value: string): Promise<void> {
    this.store.set(key, value);
  }

  seed(environment: string, snapshot: unknown): void {
    this.store.set(
      `tombstone:snapshot:${environment}`,
      JSON.stringify(snapshot),
    );
  }
}

describe("@tomb-stone/edge — EdgeFlagClient prerequisite parsing + evaluation", () => {
  it("parses a real snake_case snapshot's prerequisites and blocks on a failed gating prerequisite", async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "parent-flag",
          enabled: false, // OFF -> "false"
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
        },
        {
          flag_key: "child-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
          prerequisites: [
            { flag_key: "parent-flag", required_variation: "true", gate: true },
          ],
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<boolean>("child-flag", {
      userId: "u",
    });
    assert.strictEqual(result.reason, "PREREQUISITE_FAILED");
    assert.strictEqual(result.value, false);
  });

  it("allows evaluation to proceed when the parsed prerequisite's variation matches", async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "parent-flag",
          enabled: true,
          rollout_pct: 100, // -> "true"
          safe_default: "false",
          environment: "production",
        },
        {
          flag_key: "child-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
          prerequisites: [
            { flag_key: "parent-flag", required_variation: "true", gate: true },
          ],
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<boolean>("child-flag", {
      userId: "u",
    });
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it('defaults gate to true and requiredVariation to "true" when the raw snapshot omits them', async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "parent-flag",
          enabled: false, // OFF -> "false", mismatches the implied default "true"
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
        },
        {
          flag_key: "child-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
          // No `required_variation`, no `gate` at all on this prerequisite.
          prerequisites: [{ flag_key: "parent-flag" }],
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<boolean>("child-flag", {
      userId: "u",
    });
    assert.strictEqual(
      result.reason,
      "PREREQUISITE_FAILED",
      "an omitted gate must default to true (gating), not false (permissive)",
    );
  });

  it("flags with no prerequisites field at all evaluate normally (backward compatible)", async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "solo-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<boolean>("solo-flag", { userId: "u" });
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("a malformed (null) prerequisite entry on one flag does not poison parsing of the ENTIRE snapshot", async () => {
    // Found by adversarial review of PR #244: normalizePrerequisites used
    // to do `p as Record<string, unknown>` with no null check, so a single
    // stray `null` in ANY flag's prerequisites array threw inside the one
    // .map() call that parses every flag in the snapshot -- silently
    // degrading an UNRELATED flag (one with no prerequisites at all) to
    // ERROR/default, since getSnapshot()'s try/catch swallows the error.
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "critical-checkout-flow",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
          // No prerequisites at all -- must be completely unaffected by
          // the OTHER flag's malformed data below.
        },
        {
          flag_key: "some-other-teams-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
          prerequisites: [null],
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<boolean>("critical-checkout-flow", {
      userId: "u",
    });
    assert.strictEqual(
      result.reason,
      "FALLTHROUGH",
      "an unrelated flag must evaluate normally despite malformed prerequisite data elsewhere in the snapshot",
    );
    assert.strictEqual(result.value, true);
  });

  it("memoizes the prerequisite lookup by snapshot identity — not rebuilt on every evaluate() call", async () => {
    // MEDIUM finding from adversarial review of PR #244: buildLookup()
    // previously ran on every single evaluate() call (O(N) Map allocation
    // over the WHOLE snapshot), even for a flag with zero prerequisites --
    // directly undermining this package's own "sub-1ms" goal for an org
    // with thousands of flags. Reaches the private getLookup/lookupCache
    // fields directly since there is no public way to observe allocation
    // count otherwise.
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "solo-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });
    const internals = client as unknown as {
      getLookup(snapshot: FlagSnapshot): FlagLookup;
    };

    await client.evaluate<boolean>("solo-flag", { userId: "u" });
    await client.evaluate<boolean>("solo-flag", { userId: "u" });
    const snapshot = (await client.getSnapshot())!;
    const first = internals.getLookup(snapshot);
    const second = internals.getLookup(snapshot);
    assert.strictEqual(
      first,
      second,
      "the SAME snapshot object must reuse the SAME lookup, not rebuild it",
    );
  });
});

describe("@tomb-stone/edge — EdgeFlagClient targeting_rules parsing + evaluation", () => {
  it("parses a real snake_case snapshot's targeting_rules and RULE_MATCHes on it end-to-end", async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "f",
          enabled: true,
          rollout_pct: 0, // 0: proves RULE_MATCH wins over FALLTHROUGH, not the other way around
          safe_default: "false",
          environment: "production",
          targeting_rules: [
            {
              id: "rule-pro",
              rule_type: "USER",
              attribute: "plan",
              operator: "IN",
              values: ["pro", "enterprise"],
              variation: "v2",
              priority: 10,
            },
          ],
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<string>("f", {
      userId: "u",
      attrs: { plan: "pro" },
    });
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "v2");
    assert.strictEqual(result.ruleId, "rule-pro");
  });

  it("a malformed (null) targeting_rules entry on one flag does not poison parsing of the ENTIRE snapshot", async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "unaffected-flag",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
          // No targeting_rules at all -- must be completely unaffected by
          // the OTHER flag's malformed entry below.
        },
        {
          flag_key: "malformed-flag",
          enabled: true,
          rollout_pct: 0,
          safe_default: "false",
          environment: "production",
          targeting_rules: [null],
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const unaffected = await client.evaluate<boolean>("unaffected-flag", {
      userId: "u",
    });
    assert.strictEqual(unaffected.reason, "FALLTHROUGH");
    assert.strictEqual(unaffected.value, true);

    const malformed = await client.evaluate<boolean>("malformed-flag", {
      userId: "u",
    });
    assert.strictEqual(
      malformed.reason,
      "FALLTHROUGH",
      "a null rule entry must be filtered out, not thrown on",
    );
  });

  it("flags with no targeting_rules field at all evaluate normally (backward compatible)", async () => {
    const kv = new FakeKV();
    kv.seed("production", {
      environment: "production",
      hash: "h1",
      ts: 1,
      flags: [
        {
          flag_key: "f",
          enabled: true,
          rollout_pct: 100,
          safe_default: "false",
          environment: "production",
        },
      ],
    });
    const client = new EdgeFlagClient({
      kv: kv as never,
      environment: "production",
    });

    const result = await client.evaluate<boolean>("f", { userId: "u" });
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });
});
