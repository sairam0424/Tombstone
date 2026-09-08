import { useState, useEffect } from "react";

// The targeting_rules table + full evaluation logic in every SDK
// (TypeScript/Python/Java/Ruby/.NET/WASM) have existed since PR #245-251,
// and the real REST surface (services/flag-api/internal/api/v1/
// targeting_rules.go) has existed since PR #245 too -- but nothing in the
// dashboard ever called it. An operator could not create, view, or delete
// a targeting rule without hitting the API directly. This is the literal
// CRUD gap: Add + List + Delete, matching the backend's own three
// endpoints exactly (there is no update endpoint -- targeting rules are
// add/delete only, matching the "full replacement" semantics of the live
// targeting_rules_updated SSE event; editing a rule means deleting it and
// adding a new one).

interface TargetingRule {
  id: string;
  flag_id: string;
  environment: string;
  rule_type: "USER" | "ORG" | "SEGMENT" | "CUSTOM";
  attribute: string;
  operator: string;
  values: unknown;
  variation: string;
  priority: number;
  created_at: number;
}

interface TargetingRulesTabProps {
  flagKey: string;
  apiUrl: string;
  token: string;
  environment: string;
  environments: readonly string[];
  onEnvironmentChange: (env: string) => void;
}

const RULE_TYPES = ["USER", "ORG", "SEGMENT", "CUSTOM"] as const;

// Mirrors validOperators in targeting_rules.go exactly -- the backend's
// own CHECK constraint (schema.sql) is the source of truth; this list
// exists only to give the dropdown real options, not to duplicate
// validation (the backend still validates and returns a 400 with a real
// message on rejection, shown as-is below).
const OPERATORS = [
  "IN",
  "NOT_IN",
  "EQ",
  "NEQ",
  "LT",
  "LTE",
  "GT",
  "GTE",
  "CONTAINS",
  "PREFIX",
  "SUFFIX",
  "REGEX",
  "SEMVER_GTE",
  "SEMVER_LTE",
  "GEO_COUNTRY",
  "GEO_REGION",
  "DATE_BEFORE",
  "DATE_AFTER",
] as const;

// docs/SDK_CONTRACT.md's Parity Matrix, verified against every SDK's real
// applyOperator switch (e.g. @flagmind/core's evaluation.ts, which falls
// through to `default: return false` for any operator with no case) --
// the backend happily accepts these operators (its own validOperators
// whitelist doesn't know or care about SDK-level parity), so a rule using
// one silently NEVER matches for the affected client, forever, with no
// error anywhere. REGEX is "declared, returns false" in ALL FIVE SDKs;
// semver/date operators only fail this way for TypeScript
// (@flagmind/core / @flagmind/react) -- Python/Java/Ruby/.NET implement
// them for real.
const NON_FUNCTIONAL_OPERATOR_WARNINGS: Record<string, string> = {
  REGEX:
    "Not implemented in ANY SDK yet (TypeScript, Python, Java, Ruby, .NET all declare it but return false) -- a rule using this will never match, for anyone.",
  SEMVER_GTE:
    "Not implemented in the TypeScript SDK (@flagmind/core/@flagmind/react) -- a rule using this will never match for JS/TS clients. Works correctly in Python, Java, Ruby, .NET.",
  SEMVER_LTE:
    "Not implemented in the TypeScript SDK (@flagmind/core/@flagmind/react) -- a rule using this will never match for JS/TS clients. Works correctly in Python, Java, Ruby, .NET.",
  DATE_BEFORE:
    "Not implemented in the TypeScript SDK (@flagmind/core/@flagmind/react) -- a rule using this will never match for JS/TS clients. Works correctly in Python, Java, Ruby, .NET.",
  DATE_AFTER:
    "Not implemented in the TypeScript SDK (@flagmind/core/@flagmind/react) -- a rule using this will never match for JS/TS clients. Works correctly in Python, Java, Ruby, .NET.",
};

function formatValues(v: unknown): string {
  if (Array.isArray(v)) return v.join(", ");
  return String(v ?? "");
}

export function TargetingRulesTab({
  flagKey,
  apiUrl,
  token,
  environment,
  environments,
  onEnvironmentChange,
}: TargetingRulesTabProps) {
  const [rules, setRules] = useState<TargetingRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<string | null>(
    null,
  );
  const [submitting, setSubmitting] = useState(false);

  const [ruleType, setRuleType] = useState<(typeof RULE_TYPES)[number]>("USER");
  const [attribute, setAttribute] = useState("");
  const [operator, setOperator] = useState<(typeof OPERATORS)[number]>("IN");
  const [valuesInput, setValuesInput] = useState("");
  const [variation, setVariation] = useState("");
  const [priority, setPriority] = useState(0);

  const headers = {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
  const rulesUrl = `${apiUrl}/api/v1/flags/${flagKey}/environments/${environment}/rules`;

  const loadRules = () => {
    setLoading(true);
    setError(null);
    fetch(rulesUrl, { headers })
      .then((r) => {
        if (!r.ok) throw new Error(`failed to load rules (${r.status})`);
        return r.json();
      })
      .then((data: { targeting_rules?: TargetingRule[] }) => {
        // ORDER matches the backend's own evaluation order (priority ASC —
        // lower priority is evaluated first) so what's shown here is what
        // actually happens at eval time, not an arbitrary API order.
        const sorted = (data.targeting_rules ?? [])
          .slice()
          .sort((a, b) => a.priority - b.priority);
        setRules(sorted);
      })
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    loadRules();
    // A stale confirm/delete id from a different environment can never
    // actually collide (targeting_rules.id is a globally-unique server
    // UUID), but reset defensively anyway -- an environment switch mid
    // delete-confirmation is a real interaction now that this tab has its
    // own env switcher below, not a rare edge case.
    setConfirmingDeleteId(null);
    setDeletingId(null);
    // Switching environments must refetch -- rules are per-environment,
    // there is no cross-environment fallback.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flagKey, environment]);

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      // Comma-separated free text -> a JSON array of strings, matching
      // what IN/NOT_IN need directly. Single-value operators (EQ, LT,
      // SEMVER_GTE, etc.) are expected by every SDK's already-existing
      // evaluation logic to read values[0] -- this UI stays deliberately
      // thin here, forwarding whatever the backend actually validates
      // rather than re-implementing per-operator shape rules client-side.
      const values = valuesInput
        .split(",")
        .map((v) => v.trim())
        .filter((v) => v.length > 0);
      const res = await fetch(rulesUrl, {
        method: "POST",
        headers,
        body: JSON.stringify({
          rule_type: ruleType,
          attribute,
          operator,
          values,
          variation,
          priority,
        }),
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as {
          error?: string;
        };
        throw new Error(body.error ?? `failed to add rule (${res.status})`);
      }
      setAttribute("");
      setValuesInput("");
      setVariation("");
      setPriority(0);
      loadRules();
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to add rule");
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (id: string) => {
    if (confirmingDeleteId !== id) {
      setConfirmingDeleteId(id);
      return;
    }
    setConfirmingDeleteId(null);
    setDeletingId(id);
    setError(null);
    try {
      const res = await fetch(`${rulesUrl}/${id}`, {
        method: "DELETE",
        headers,
      });
      // DeleteTargetingRule (targeting_rules.go) responds 200 with
      // {deleted:true, id} on success, never 204 -- there is no case where
      // res.ok is true and this needs to distinguish further.
      if (!res.ok) {
        throw new Error(`failed to delete rule (${res.status})`);
      }
      setRules((prev) => prev.filter((r) => r.id !== id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to delete rule");
    } finally {
      setDeletingId(null);
    }
  };

  return (
    <div className="space-y-6">
      {/* This tab is reachable directly (e.g. a user opens a flag and
          clicks "Targeting Rules" without ever visiting the Overview tab's
          own env sub-tabs), so it needs its OWN visible environment
          control -- otherwise the active environment (defaulting to
          "production") is easy to misjudge, and a rule can silently get
          added to the wrong one. Shares the same activeEnv state as
          Overview's switcher via onEnvironmentChange, so the two stay in
          sync rather than tracking environment independently. */}
      <div className="flex items-center gap-2">
        <span className="text-xs text-gray-500">Environment:</span>
        {environments.map((env) => (
          <button
            key={env}
            onClick={() => onEnvironmentChange(env)}
            className={`px-3 py-1 rounded text-xs ${
              env === environment
                ? "bg-blue-600 text-white"
                : "bg-gray-800 text-gray-300 hover:bg-gray-700"
            }`}
          >
            {env}
          </button>
        ))}
      </div>

      <div className="text-sm text-gray-400">
        Rules for the{" "}
        <span className="font-mono text-green-400">{environment}</span>{" "}
        environment, evaluated in priority order (lowest first). There is no
        edit — delete a rule and add a new one to change it.
      </div>

      {error && (
        <div className="text-sm text-red-400 bg-red-950/40 border border-red-800 rounded px-3 py-2">
          {error}
        </div>
      )}

      {loading ? (
        <div className="text-gray-400">Loading targeting rules…</div>
      ) : rules.length === 0 ? (
        <div className="text-gray-500 text-sm py-6 text-center border border-gray-800 rounded">
          No targeting rules for this environment yet.
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm border-collapse">
            <thead>
              <tr className="border-b border-gray-800 text-left text-xs uppercase text-gray-500">
                <th className="py-2 px-3">Priority</th>
                <th className="py-2 px-3">Type</th>
                <th className="py-2 px-3">Attribute</th>
                <th className="py-2 px-3">Operator</th>
                <th className="py-2 px-3">Values</th>
                <th className="py-2 px-3">Variation</th>
                <th className="py-2 px-3"></th>
              </tr>
            </thead>
            <tbody>
              {rules.map((rule) => (
                <tr key={rule.id} className="border-b border-gray-900">
                  <td className="py-2 px-3 font-mono">{rule.priority}</td>
                  <td className="py-2 px-3">{rule.rule_type}</td>
                  <td className="py-2 px-3 font-mono text-blue-300">
                    {rule.attribute}
                  </td>
                  <td className="py-2 px-3 font-mono">{rule.operator}</td>
                  <td className="py-2 px-3 text-gray-300">
                    {formatValues(rule.values)}
                  </td>
                  <td className="py-2 px-3 font-mono text-green-400">
                    {rule.variation}
                  </td>
                  <td className="py-2 px-3 text-right">
                    <button
                      onClick={() => void handleDelete(rule.id)}
                      disabled={deletingId === rule.id}
                      className={`px-2 py-1 rounded text-xs ${
                        confirmingDeleteId === rule.id
                          ? "bg-red-700 text-white"
                          : "bg-gray-800 text-gray-300 hover:bg-gray-700"
                      }`}
                    >
                      {deletingId === rule.id
                        ? "Deleting…"
                        : confirmingDeleteId === rule.id
                          ? "Confirm delete?"
                          : "Delete"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <form
        onSubmit={(e) => void handleAdd(e)}
        className="border border-gray-800 rounded p-4 space-y-3"
      >
        <div className="text-sm font-semibold text-gray-300">
          Add targeting rule
        </div>
        <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
          <label className="text-xs text-gray-400 flex flex-col gap-1">
            Rule type
            <select
              value={ruleType}
              onChange={(e) =>
                setRuleType(e.target.value as (typeof RULE_TYPES)[number])
              }
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200"
            >
              {RULE_TYPES.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </label>
          <label className="text-xs text-gray-400 flex flex-col gap-1">
            Attribute
            <input
              value={attribute}
              onChange={(e) => setAttribute(e.target.value)}
              placeholder="e.g. country, user_id, plan"
              required
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200"
            />
          </label>
          <label className="text-xs text-gray-400 flex flex-col gap-1">
            Operator
            <select
              value={operator}
              onChange={(e) =>
                setOperator(e.target.value as (typeof OPERATORS)[number])
              }
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200"
            >
              {OPERATORS.map((op) => (
                <option key={op} value={op}>
                  {op in NON_FUNCTIONAL_OPERATOR_WARNINGS ? `${op} ⚠` : op}
                </option>
              ))}
            </select>
          </label>
          <label className="text-xs text-gray-400 flex flex-col gap-1 col-span-2">
            Values (comma-separated)
            <input
              value={valuesInput}
              onChange={(e) => setValuesInput(e.target.value)}
              placeholder="e.g. US, CA, UK"
              required
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200"
            />
          </label>
          {operator in NON_FUNCTIONAL_OPERATOR_WARNINGS && (
            <div className="col-span-2 md:col-span-3 text-xs text-amber-400 bg-amber-950/30 border border-amber-800 rounded px-3 py-2">
              ⚠ {NON_FUNCTIONAL_OPERATOR_WARNINGS[operator]}
            </div>
          )}
          <label className="text-xs text-gray-400 flex flex-col gap-1">
            Variation
            <input
              value={variation}
              onChange={(e) => setVariation(e.target.value)}
              placeholder="e.g. true, variant-a"
              required
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200"
            />
          </label>
          <label className="text-xs text-gray-400 flex flex-col gap-1">
            Priority (lower = first)
            <input
              type="number"
              value={priority}
              onChange={(e) => setPriority(Number(e.target.value))}
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200"
            />
          </label>
        </div>
        <button
          type="submit"
          disabled={submitting}
          className="px-4 py-2 bg-blue-600 hover:bg-blue-500 disabled:bg-gray-700 text-white rounded text-sm"
        >
          {submitting ? "Adding…" : "Add rule"}
        </button>
      </form>
    </div>
  );
}
