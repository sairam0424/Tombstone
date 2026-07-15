# SDK Feature-Parity Matrix

Verified by reading each SDK's evaluation source directly (not assumed). TypeScript
(`@tomb-stone/core`, dir `@flagmind/core`) is the reference implementation.

Sources checked:
- TypeScript: `packages/sdks/@flagmind/core/src/evaluation.ts`, `types.ts`
- Python: `packages/sdks/flagmind-python/tombstone/{evaluation,matching,types}.py`
- Java: `packages/sdks/flagmind-java/src/main/java/io/tombstone/{evaluation/EvaluationEngine,types/*}.java`
- Ruby: `packages/sdks/flagmind-ruby/lib/flagmind/{evaluation_engine,types}.rb`
- .NET: `packages/sdks/flagmind-dotnet/src/FlagMind/{EvaluationEngine,Types}.cs`

## 5-Step Evaluation Pipeline

| Step | TypeScript | Python | Java | Ruby | .NET |
|---|---|---|---|---|---|
| 1. Preliminary (missing → ERROR, disabled → OFF) | Yes | Yes | Yes | Yes | Yes |
| 2. Prerequisites (recursive, cycle-safe) | Yes | Yes | No | No | No |
| 2a. `gate` soft/hard semantics | Yes — `gate: boolean` on `FlagPrerequisite`; `gate:false` unmet → skip/continue, `gate:true`/default → `PREREQUISITE_FAILED` | Yes — `prereq.get("gate", True)`; same soft/hard split, default `True` | N/A — no prerequisite field on `FlagEnvironmentState`, no prereq check in `EvaluationEngine.evaluate` | N/A — no prerequisite field on `FlagEnvironmentState`; `EvaluationReason::PREREQUISITE_FAILED` enum value exists but is never returned | N/A — no prerequisite field on `FlagEnvironmentState`; `EvaluationReason.PrerequisiteFailed` enum value exists but is never returned |
| 3. Individual target list → `TARGET_MATCH` | Yes — `flagState.targetList` | Yes — `flag_state.target_list` | No — no `targetList`/`TargetMatch` branch in `EvaluationEngine`; enum value unused | No — no `target_list` field; enum value unused | No — no `TargetList` field; enum value unused |
| 4. Priority-sorted rule matching → `RULE_MATCH` | Yes — merges `flagState.targetingRules` + passed-in `rules`, sorts ascending by `priority` (0 = highest) | Yes — `flag_state.targeting_rules` sorted ascending by `priority`; per-rule rollout bucket also applied on match | No — `evaluate()` takes a `rules` param but never reads it; no rule-matching code path | No — no rule/condition types exist at all | No — no rule/condition types exist at all |
| 5. Fallthrough rollout | Yes | Yes | Yes | Yes | Yes |

**Net result:** TypeScript and Python implement the full 5-step pipeline. Java, Ruby,
and .NET implement only **steps 1 and 5** — every flag in those three SDKs behaves as
if it has no prerequisites, no target list, and no targeting rules, falling straight
from the enabled/disabled check to the rollout-percentage check. Their `EvaluationReason`
enums declare `TARGET_MATCH`, `RULE_MATCH`, and `PREREQUISITE_FAILED` values, but no code
path in any of the three ever returns them — dead enum members.

## Rollout Hashing

| Capability | TypeScript | Python | Java | Ruby | .NET |
|---|---|---|---|---|---|
| `hash_version` field read at all | Yes — `flagState.hashVersion ?? 1` | Yes — `flag_state.hash_version` (default `1`) | No — `FlagEnvironmentState` has no hash-version field | No — `FlagEnvironmentState` has no hash-version field | No — `FlagEnvironmentState` has no hash-version field |
| `hash_version: 1` — MurmurHash3, 32-bit unsigned, seed 0, mod 100 | Yes — `murmurhash.v3(flagKey+userId) >>> 0 % 100` | Yes — `mmh3.hash(flag_key+user_id, seed=0, signed=False) % 100` | Yes (only mode) — Guava `Hashing.murmur3_32_fixed()` | Yes (only mode) — `MurmurHash3::V32.str_hash` | Yes (only mode) — `Murmur.MurmurHash.Create32(seed:0)` |
| `hash_version: 2` — double-pass FNV-1a, 10,000-bucket | Yes — `fnv(String(fnv(flagKey+userId))) % 10000 / 10000 < pct/100` | Yes — `_is_in_rollout_fnv`, bit-identical algorithm | No | No | No |
| Cross-language v1 parity | Verified against `test-contract/vectors.json` | Verified against same vectors | Verified against same vectors (v1 only) | Verified against same vectors (v1 only) | Verified against same vectors (v1 only) |
| Cross-language v2 parity | Verified against `test-contract/vectors.json` | Verified against same vectors | Not applicable — v2 not implemented | Not applicable — v2 not implemented | Not applicable — v2 not implemented |

Java/Ruby/.NET always compute rollout with the v1 (MurmurHash3) algorithm — there is no
branch to select v2, because there is no `hash_version` field to branch on.

## `target_list` Support

| SDK | Field present | Checked in evaluation | Notes |
|---|---|---|---|
| TypeScript | Yes — `FlagEnvironmentState.targetList?: string[]` | Yes — Step 3 | `context.userId` membership check before rule matching |
| Python | Yes — `FlagEnvironmentState.target_list: list` | Yes — Step 3 | `context.user_id in flag_state.target_list` |
| Java | No | No | — |
| Ruby | No | No | — |
| .NET | No | No | — |

## Operator Case-Normalization

| Operator family | TypeScript | Python |
|---|---|---|
| `IN` / `NOT_IN` / `EQ` / `NEQ` (eq/neq/in/nin) | Exact `String(value)` comparison — case-sensitive, no normalization | Exact membership/equality on raw string — case-sensitive, no normalization |
| `CONTAINS` / `PREFIX` / `SUFFIX` (contains/startswith/endswith) | Case-sensitive — plain `String.prototype.includes/startsWith/endsWith` | Case-**insensitive** — both attribute and rule values upper-cased via `.upper()` before comparison |
| `GEO_COUNTRY` / `GEO_REGION` | Case-**insensitive** — both context value and rule values upper-cased via `.toUpperCase()` | Not implemented — Python has no GEO operator or `geo` context field at all |
| Numeric (`LT`/`LTE`/`GT`/`GTE` vs `gt`/`gte`/`lt`/`lte`) | N/A (numeric, not string) | N/A (numeric, not string) |

TypeScript and Python disagree on case sensitivity for `CONTAINS`/`PREFIX`/`SUFFIX`:
TypeScript is case-sensitive, Python is case-insensitive. This is a real behavioral
divergence, not a documentation gap — a rule using `CONTAINS` with mixed-case input
can match in one SDK and not the other.

Java, Ruby, and .NET have no operator/rule-matching code at all (see pipeline table
above), so there is no operator behavior to compare for those three.

## Summary

| SDK | Pipeline completeness | Hash versions | `target_list` | Rule operators |
|---|---|---|---|---|
| TypeScript (`@tomb-stone/core`) | Full 5-step (reference) | v1 + v2 | Yes | Full set, case rules as above |
| Python (`tombstone-sdk`) | Full 5-step | v1 + v2 | Yes | Full set, case rules as above (diverges from TS on string ops) |
| Java (`tombstone-java-sdk`) | Steps 1 + 5 only | v1 only | No | None |
| Ruby (`tombstone-ruby`) | Steps 1 + 5 only | v1 only | No | None |
| .NET (`Tombstone.SDK`) | Steps 1 + 5 only | v1 only | No | None |
