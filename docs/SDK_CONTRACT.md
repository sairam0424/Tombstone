# SDK Contract — Canonical Evaluation Spec

This is the normative specification for the 5-step evaluation pipeline that Java, Ruby,
and .NET SDKs must implement (as of v1.5.0). It resolves every divergence found between
the two reference implementations (TypeScript, Python) into one target model — TypeScript
and Python remain on their existing shipped behavior; this document does not change them,
and their mutual divergence is documented in the "Known Pre-Existing Divergence" section
below.

Executable contract vectors live in `packages/sdks/test-contract/vectors.json` (v1.2+) —
every SDK's test suite must load and assert against this file. It is the objective
definition of "parity."

## Canonical Model (Java/Ruby/.NET target)

| Step | Behavior |
|---|---|
| 1. Preliminary | Missing flag -> `ERROR`, caller's default. Disabled -> `OFF`, parses `flag_state.safe_default` into the target type (falls back to caller's default on parse failure). |
| 2. Prerequisites | For each prerequisite: evaluate the dependency (memoized per top-level call via a cache keyed by dependency flag key), compare `String(dependency_result.variation)` against `required_variation`. Mismatch + `gate=true` -> `PREREQUISITE_FAILED`, caller's default. Mismatch + `gate=false` -> skip, continue. Missing dependency + `gate=true` -> `PREREQUISITE_FAILED`. Cycle detected (dependency already in the current evaluation chain) -> fails open, skip that one prerequisite edge only. |
| 3. Target list | If `context.user_id` is in `flag_state.target_list` -> `TARGET_MATCH`, value `true`. |
| 4. Rule matching | Sort `flag_state.targeting_rules` ascending by `priority` (0 = highest). For each rule: evaluate ALL conditions with AND semantics; any condition raising "inconclusive" (see below) skips the whole rule, continue to next. If all conditions match, compute `bucket = murmur3_v1(flag_key + user_id) % 100`; if `bucket < rule.rollout_pct` -> `RULE_MATCH`, rule's variation. Otherwise continue to next rule (NOT step 5). |
| 5. Fallthrough | `rollout_pct >= 100` -> `true`. `rollout_pct <= 0` -> caller's default. Else hash-compare via `hash_version` (1 = MurmurHash3, 2 = FNV-1a); in cohort -> `true`, else caller's default. |

## Operator Surface (Step 4 conditions)

- **Equality**: `eq`, `neq`, `in`, `nin` — exact string comparison/membership.
- **String**: `contains`, `startswith` (alias `prefix`), `endswith` (alias `suffix`) — **case-insensitive**, both sides uppercased, multi-value ANY semantics.
- **Numeric**: `gt`, `gte`, `lt`, `lte` — cast both sides to float; cast failure -> inconclusive.
- **Semver**: `semver_gt`, `semver_gte`, `semver_lt`, `semver_lte`, `semver_eq` — compared via `_padded_version()` string padding (strip `v` prefix and build metadata, left-pad numeric segments to 5 chars, append `~` to 3-part releases so prereleases sort below their release).
- **Date**: `date_before`, `date_after` — ISO-8601 parse (`Z` normalized to `+00:00`); parse failure -> inconclusive.
- **Geo**: `in`/`nin` against `geo.country` / `geo.region` attribute paths — case-insensitive, resolved via dot-notation.
- **Regex**: declared but **not implemented** in this release (matches current TS behavior — always returns `false`, not inconclusive). Future work.
- **Attribute resolution**: dot-notation nested paths on the context's attribute map, with a flat single-segment fallback. Missing attribute -> inconclusive (a raised/thrown exception in every language, caught by the rule-matching loop, which treats it as "this rule did not match" and continues to the next rule).
- **`negate`**: a boolean on every condition; inverts the final boolean result after evaluation (does not apply to inconclusive outcomes — inconclusive stays inconclusive regardless of `negate`).

## Hashing

- **v1 (MurmurHash3)**: unsigned 32-bit, seed=0, `concat(flag_key, user_id)` as UTF-8 bytes, mod 100.
- **v2 (FNV-1a)**: double-pass — `inner = fnv1a(concat(flag_key, user_id))`, `outer = fnv1a(decimal_string(inner))`, `bucket = (outer % 10000) / 10000`, compared against `rollout_pct / 100`. MUST iterate over UTF-8 bytes (not UTF-16 code units) for portability across Java/Ruby/.NET string encodings.

## Known Pre-Existing Divergence (TypeScript vs Python — NOT fixed by this spec)

TypeScript and Python are each other's oldest SDKs and diverge in ways this release does
not touch:

| Aspect | TypeScript | Python |
|---|---|---|
| OFF-path default | Parses `safeDefault` | Raw caller default |
| Prerequisite comparison | String variation match | Boolean-only match |
| String op case sensitivity | Case-sensitive | Case-insensitive |
| GEO operators | Implemented | Not implemented |
| Semver/date operators | Declared, not implemented | Implemented |
| Missing-attribute signaling | Silent `false` | Raises `InconclusiveMatchError` |

New SDKs (Java/Ruby/.NET) follow the Canonical Model above, which resolves each of these
in one direction — they do not match either TS or Python exactly on every point. The full
design rationale and decision matrix are documented in the v1.5.0 design specification
(available on branch `docs/v1.5.0-upgrade-design`).

## Live Streaming (`prerequisites_updated` / `targeting_rules_updated`)

Added after v1.5.0 — all 5 SDKs (TypeScript, Python, Java, Ruby, .NET) consume both events;
not yet reflected in the Parity Matrix's own historical framing below, which predates this
work.

flag-api publishes a dedicated SSE event (a `"kind"` discriminator on the wire, never
colliding with a free-text `FlagEvent.Reason`) whenever a mutation commits:
- `prerequisites_updated` on `AddPrerequisite`/`DeletePrerequisite`, carrying the flag's
  current FULL prerequisite list (not a delta).
- `targeting_rules_updated` on the targeting-rules CRUD endpoints
  (`POST/GET/DELETE /flags/{key}/environments/{env}/rules[/{id}]`), same full-list shape.

Both fan out through gateway's live SSE path, the reconnect XRANGE replay path, and the DLQ
reclaim path identically. Every SDK applies the identical design, independent of language:

- A `>=` staleness guard comparing the cached `*_updated_at` timestamp against the event's
  `ts` — rejects a strictly-older event (`<`, not `<=`).
- A one-shot live-event-provenance flag per flag+field, reset to `false` on every subsequent
  snapshot load — lets a live event be trusted over a *stale* snapshot without pinning that
  trust forever (a snapshot with a genuinely newer `ts` still wins the next time).
- Malformed/non-object SSE payloads are swallowed, not raised — one bad event must not crash
  the whole streaming connection.
- An event for a `flag_key` not present in the local cache is a no-op.

**Known, disclosed, shared limitation across all 5 SDKs**: the one-shot provenance guard
only protects the FIRST snapshot received after a live event. A SECOND independent snapshot
whose own `ts` is still older than the live event's can regress `*_updated_at` backward —
this needs a signal finer than flag-api's 1-second wall-clock `ts` to close fully, and is
accepted as a shared limitation, not a per-SDK bug, until that signal exists.

## Parity Matrix (updated after v1.5.0)

| Capability | TypeScript | Python | Java | Ruby | .NET |
|---|---|---|---|---|---|
| Steps 1+5 (preliminary + fallthrough) | Yes | Yes | Yes | Yes | Yes |
| Step 2 (prerequisites) | Yes (string variation) | Yes (boolean-only) | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Step 3 (target list) | Yes | Yes | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Step 4 (rule matching) | Yes (single condition) | Yes (multi-condition) | Yes (canonical, multi-condition) | Yes (canonical, multi-condition) | Yes (canonical, multi-condition) |
| Hash v1 (MurmurHash3) | Yes | Yes | Yes | Yes | Yes |
| Hash v2 (FNV-1a) | Yes | Yes | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Semver/date operators | No | Yes | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| GEO operators | Yes | Yes (added post-v1.5.0, targeting_rules streaming work) | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Regex operator | No (declared, returns `false`) | No (declared, returns `false`) | No (declared, returns `false`) | No (declared, returns `false`) | No (declared, returns `false`) |
| Cross-language contract vectors | Hash-only | Hash-only | Full (v1.2 vectors) | Full (v1.2 vectors) | Full (v1.2 vectors) |
| Live `prerequisites_updated` streaming | Yes | Yes | Yes | Yes | Yes |
| Live `targeting_rules_updated` streaming | Yes | Yes | Yes | Yes | Yes |

*(The v1.5.0 plan's own Phases 2-4 completed some time ago — every "Yes (canonical)" cell
above reflects actually-merged, live-code-verified state as of the live-streaming rows'
addition, not a target end-state anymore. Keep it that way: update this table when a real
PR changes the underlying behavior, per this project's read-the-actual-code verification
discipline — don't let it drift back into aspirational language.)*
