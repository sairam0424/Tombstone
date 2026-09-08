import { useState, useEffect, useRef } from "react";
import { useParams, Link } from "react-router-dom";
import { FlagHealthBadge } from "../../components/FlagHealthBadge.js";
import { CircuitBreakerStatus } from "../../components/CircuitBreakerStatus.js";
import { AutonomousRolloutToggle } from "../../components/AutonomousRolloutToggle.js";
import { DependenciesTab } from "../../components/DependenciesTab.js";
import { TargetingRulesTab } from "../../components/TargetingRulesTab.js";
import { BlastRadiusBadge } from "../../components/BlastRadiusBadge.js";
import { EVAL_URL } from "../../config.js";

interface BlastRadiusResult {
  risk_score: "LOW" | "MEDIUM" | "HIGH" | "BLOCKED";
  traffic_pct_affected: number;
  recent_evaluation_count: number;
  dependent_flags_count: number;
  affected_services: string[];
  historical_error_rate: number;
  confidence: "HIGH" | "LOW";
  justification_required?: string;
}

const RISK_SCORES = new Set(["LOW", "MEDIUM", "HIGH", "BLOCKED"]);

// The evaluator's real response shape is out of this component's control —
// a malformed/partial result must never crash BlastRadiusBadge's render
// (which would take the whole confirm UI, and with it the ability to kill
// at all, down with it — directly contradicting the "must never be
// blocked" fail-open design this feature exists to preserve).
function isValidBlastRadiusResult(v: unknown): v is BlastRadiusResult {
  if (!v || typeof v !== "object") return false;
  const r = v as Record<string, unknown>;
  return (
    typeof r.risk_score === "string" &&
    RISK_SCORES.has(r.risk_score) &&
    typeof r.traffic_pct_affected === "number" &&
    typeof r.recent_evaluation_count === "number" &&
    typeof r.dependent_flags_count === "number" &&
    Array.isArray(r.affected_services) &&
    typeof r.historical_error_rate === "number" &&
    typeof r.confidence === "string"
  );
}

type KillFlowStatus = "idle" | "checking" | "confirming" | "killing" | "error";

interface KillFlowState {
  status: KillFlowStatus;
  blastRadius?: BlastRadiusResult;
  error?: string;
}

const IDLE_KILL_FLOW: KillFlowState = { status: "idle" };

interface FlagEnvState {
  flag_id: string;
  flag_key: string;
  environment: string;
  enabled: boolean;
  rollout_pct: number;
  safe_default: string;
  updated_at: number;
}

interface AuditEntry {
  id: string;
  flag_key: string;
  environment: string;
  actor: string;
  event_type: string;
  prev_state: unknown;
  new_state: unknown;
  created_at: number;
}

interface Flag {
  id: string;
  key: string;
  name: string;
  description: string;
  flag_type: string;
  state: "DRAFT" | "ACTIVE" | "COMPLETE" | "ARCHIVED";
  owner_id: string;
  created_at: number;
  updated_at: number;
}

type Env = "development" | "staging" | "production";
const ENVS: Env[] = ["development", "staging", "production"];

const envColor: Record<Env, string> = {
  development: "border-blue-600 text-blue-400",
  staging: "border-amber-600 text-amber-400",
  production: "border-green-600 text-green-400",
};

export default function FlagDetail() {
  const { key } = useParams<{ key: string }>();
  const [flag, setFlag] = useState<Flag | null>(null);
  const [envStates, setEnvStates] = useState<Record<string, FlagEnvState>>({});
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [activeEnv, setActiveEnv] = useState<Env>("production");
  const [activeTab, setActiveTab] = useState<
    "overview" | "dependencies" | "targeting-rules"
  >("overview");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Scoped per environment, not a single shared flag — otherwise a check/
  // kill in flight for one env incorrectly disables or mislabels every
  // other env's independent Kill Switch button (found by adversarial
  // review: this page lets an operator triage all 3 envs from one screen).
  const [killFlow, setKillFlow] = useState<Record<Env, KillFlowState>>({
    development: IDLE_KILL_FLOW,
    staging: IDLE_KILL_FLOW,
    production: IDLE_KILL_FLOW,
  });
  // Per-env generation counter. Bumped only by an explicit Cancel click.
  // requestKillSwitch's fail-open catch checks this before firing the kill
  // it would otherwise send unconditionally — without it, an operator who
  // explicitly clicks Cancel could still have the kill fire moments later
  // when the (now-irrelevant) blast-radius fetch finally errors out, with
  // no confirmation ever shown for that outcome (found by adversarial
  // review). Switching environment tabs deliberately does NOT bump this —
  // a check/kill already in flight for env A is real work in progress, not
  // something viewing env B's tab should silently cancel.
  const killGenRef = useRef<Record<Env, number>>({
    development: 0,
    staging: 0,
    production: 0,
  });

  const apiUrl =
    (import.meta as unknown as { env: Record<string, string> }).env[
      "VITE_API_URL"
    ] ?? "http://localhost:8081";
  const tok =
    (import.meta as unknown as { env: Record<string, string> }).env[
      "VITE_SDK_TOKEN"
    ] ?? "sdk-dev-token-change-in-prod";
  const headers = {
    Authorization: `Bearer ${tok}`,
    "Content-Type": "application/json",
  };

  useEffect(() => {
    if (!key) return;
    setLoading(true);
    Promise.all([
      fetch(`${apiUrl}/api/v1/flags/${key}`, { headers }).then((r) => r.json()),
      ...ENVS.map((env) =>
        fetch(`${apiUrl}/api/v1/flags/${key}?environment=${env}`, { headers })
          .then((r) => (r.ok ? r.json() : null))
          .catch(() => null),
      ),
      fetch(`${apiUrl}/api/v1/audit?flag_key=${key}&limit=20`, {
        headers,
      }).then((r) => r.json()),
    ])
      .then(([f, ...rest]) => {
        setFlag(f as Flag);
        const auditData = rest.pop() as { entries: AuditEntry[] };
        setAudit(auditData?.entries ?? []);
      })
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));

    // Fetch snapshot per env for rollout state
    ENVS.forEach((env) => {
      fetch(`${apiUrl}/api/v1/environments/snapshot?environment=${env}`, {
        headers,
      })
        .then((r) => r.json())
        .then((snap: { flags: FlagEnvState[] }) => {
          const match = snap.flags?.find((f) => f.flag_key === key);
          if (match) setEnvStates((prev) => ({ ...prev, [env]: match }));
        })
        .catch(() => null);
    });
  }, [key]);

  const setFlow = (env: Env, next: KillFlowState) =>
    setKillFlow((prev) => ({ ...prev, [env]: next }));

  // The actual kill call, shared by both the confirmed-via-badge path and
  // the fail-open path below. `gen` is the generation this call started
  // under — checked before writing the outcome back so a Cancel click that
  // fires while this request is in flight can't be silently overwritten by
  // a stale result once the request finally resolves.
  const runKill = async (env: Env, gen: number, justification?: string) => {
    if (!key) return;
    setFlow(env, { status: "killing" });
    try {
      const res = await fetch(`${apiUrl}/api/v1/flags/${key}/kill`, {
        method: "POST",
        headers,
        body: JSON.stringify({
          environment: env,
          reason: justification
            ? `manual kill switch from dashboard — override justification: ${justification}`
            : "manual kill switch from dashboard",
        }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      // Refresh env state
      const snap = (await fetch(
        `${apiUrl}/api/v1/environments/snapshot?environment=${env}`,
        { headers },
      ).then((r) => r.json())) as { flags: FlagEnvState[] };
      const match = snap.flags?.find((f) => f.flag_key === key);
      if (match) setEnvStates((prev) => ({ ...prev, [env]: match }));
      if (killGenRef.current[env] !== gen) return;
      setFlow(env, { status: "idle" });
    } catch (e) {
      if (killGenRef.current[env] !== gen) return;
      // Surfaced, not silent: a fire-and-forget kill call that fails with
      // zero visible error would leave an operator believing a disable
      // succeeded when it didn't (found by adversarial review).
      setFlow(env, { status: "error", error: String(e) });
    }
  };

  // EVAL-3's blast-radius data is now real (historical error rate, dependent
  // flags, affected services), but nothing in the dashboard surfaced it
  // before this — a user could disable a flag with zero visibility into the
  // very score this platform is built to compute. Pre-checks the kill
  // switch specifically against this env's CURRENT rollout_pct — the
  // traffic that would be cut off by disabling, not 0 (an earlier version
  // of this passed 0, which the evaluator reads as "0% of traffic sees the
  // NEW configuration" — always true for a kill, so the BLOCKED/HIGH
  // traffic-based tiers could never fire for the single highest-stakes
  // case, a flag at high rollout serving real traffic; found by
  // adversarial review).
  //
  // Deliberately fails OPEN, not closed: the kill switch is the "eliminates
  // 3am alarm calls" panic button (see CLAUDE.md's own thesis) — it must
  // never be blocked by the evaluator service being unreachable during an
  // actual incident. If the blast-radius check itself fails, skip straight
  // to the kill, matching CircuitBreakerStatus's existing fail-open
  // precedent for this exact service — unless the user has already clicked
  // Cancel in the meantime (killGenRef changed), in which case an operator
  // who explicitly backed out must never see the kill fire anyway.
  const requestKillSwitch = async (env: Env) => {
    if (!key) return;
    const gen = ++killGenRef.current[env];
    setFlow(env, { status: "checking" });
    const currentRolloutPct = envStates[env]?.rollout_pct ?? 100;
    try {
      const res = await fetch(
        `${EVAL_URL}/api/v1/blast-radius?flag_key=${encodeURIComponent(key)}&environment=${env}&rollout_pct=${currentRolloutPct}`,
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = (await res.json()) as { result?: unknown };
      if (!isValidBlastRadiusResult(data.result)) {
        throw new Error("malformed blast-radius response");
      }
      if (killGenRef.current[env] !== gen) return; // cancelled while in flight
      setFlow(env, { status: "confirming", blastRadius: data.result });
    } catch {
      if (killGenRef.current[env] !== gen) return; // cancelled — do not kill invisibly
      await runKill(env, gen);
    }
  };

  const confirmKill = (env: Env, justification?: string) => {
    if (envStates[env]?.enabled === false) {
      // Already disabled out-of-band (another operator, auto-rollback)
      // since the badge was shown — skip the redundant kill call rather
      // than fire on stale decision data.
      cancelKill(env);
      return;
    }
    void runKill(env, killGenRef.current[env], justification);
  };

  const cancelKill = (env: Env) => {
    killGenRef.current[env] += 1;
    setFlow(env, { status: "idle" });
  };

  if (loading) return <div className="p-8 text-gray-500">Loading…</div>;
  if (error || !flag)
    return (
      <div className="p-8">
        <Link to="/" className="text-blue-400 hover:underline text-sm">
          ← Back to flags
        </Link>
        <p className="text-red-400 mt-4">{error ?? "Flag not found"}</p>
      </div>
    );

  const envState = envStates[activeEnv];
  const activeEnvKillFlow = killFlow[activeEnv];

  return (
    <div className="p-6 max-w-5xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <Link to="/" className="text-gray-500 hover:text-white text-sm">
          ← All Flags
        </Link>
        <div className="flex items-start justify-between mt-2">
          <div>
            <h1 className="text-2xl font-bold text-white font-mono">
              {flag.key}
            </h1>
            <p className="text-gray-400 mt-1">{flag.name}</p>
            {flag.description && (
              <p className="text-gray-500 text-sm mt-1">{flag.description}</p>
            )}
          </div>
          <div className="flex items-center gap-3">
            <Link
              to={`/flags/${flag.key}/slo`}
              className="px-3 py-1 rounded text-xs font-medium border border-blue-700 text-blue-400 hover:bg-blue-900/20 transition-colors"
            >
              View SLO →
            </Link>
            <FlagHealthBadge state={flag.state} />
          </div>
        </div>
        <div className="flex gap-4 mt-3 text-xs text-gray-500">
          <span>
            Type: <span className="text-gray-300">{flag.flag_type}</span>
          </span>
          <span>
            Owner: <span className="text-gray-300">{flag.owner_id}</span>
          </span>
          <span>
            Created:{" "}
            <span className="text-gray-300">
              {new Date(flag.created_at * 1000).toLocaleDateString()}
            </span>
          </span>
        </div>
      </div>

      {/* Main tab navigation */}
      <div className="flex gap-2 mb-4">
        <button
          onClick={() => setActiveTab("overview")}
          className={`px-4 py-2 rounded ${activeTab === "overview" ? "bg-blue-600 text-white" : "bg-gray-800 text-gray-300"}`}
        >
          Overview
        </button>
        <button
          onClick={() => setActiveTab("dependencies")}
          className={`px-4 py-2 rounded ${activeTab === "dependencies" ? "bg-blue-600 text-white" : "bg-gray-800 text-gray-300"}`}
        >
          Dependencies
        </button>
        <button
          onClick={() => setActiveTab("targeting-rules")}
          className={`px-4 py-2 rounded ${activeTab === "targeting-rules" ? "bg-blue-600 text-white" : "bg-gray-800 text-gray-300"}`}
        >
          Targeting Rules
        </button>
      </div>

      {/* Overview tab content */}
      {activeTab === "overview" && (
        <div>
          {/* Environment tabs */}
          <div className="flex gap-2 mb-4">
            {ENVS.map((env) => (
              <button
                key={env}
                onClick={() => setActiveEnv(env)}
                className={`px-4 py-1.5 rounded border text-xs font-medium transition-colors ${
                  activeEnv === env
                    ? envColor[env] + " bg-white/5"
                    : "border-gray-700 text-gray-500 hover:border-gray-500"
                }`}
              >
                {env}
                {/* Kill-switch state is per-env and persists across tab
                    switches (switching tabs is not a cancel) — this dot
                    is the only signal that a check/kill is in flight or
                    ended in error for an env the user isn't currently
                    looking at (found by adversarial review: without it,
                    a fail-open kill can complete for a backgrounded env
                    with zero visible indication anywhere). */}
                {killFlow[env].status !== "idle" && env !== activeEnv && (
                  <span
                    title={`Kill switch: ${killFlow[env].status}`}
                    className={`ml-1.5 inline-block w-1.5 h-1.5 rounded-full ${
                      killFlow[env].status === "error"
                        ? "bg-red-500"
                        : "bg-amber-400 animate-pulse"
                    }`}
                  />
                )}
              </button>
            ))}
          </div>

          {/* Environment state card */}
          <div className="bg-gray-900 rounded-lg border border-gray-700 p-5 mb-6">
            <div className="flex items-center justify-between mb-4">
              <h2 className="font-semibold text-white capitalize">
                {activeEnv} Environment
              </h2>
              <CircuitBreakerStatus flagKey={flag.key} />
            </div>

            {envState ? (
              <div className="grid grid-cols-2 gap-4">
                <div className="bg-black/20 rounded p-3">
                  <div className="text-gray-400 text-xs mb-1">Status</div>
                  <div
                    className={`text-lg font-bold ${envState.enabled ? "text-green-400" : "text-gray-500"}`}
                  >
                    {envState.enabled ? "ENABLED" : "DISABLED"}
                  </div>
                </div>
                <div className="bg-black/20 rounded p-3">
                  <div className="text-gray-400 text-xs mb-1">Rollout %</div>
                  <div className="flex items-center gap-3">
                    <div className="flex-1 bg-gray-700 rounded-full h-2">
                      <div
                        className="bg-blue-500 h-2 rounded-full"
                        style={{ width: `${envState.rollout_pct}%` }}
                      />
                    </div>
                    <span className="text-white text-sm font-medium w-10 text-right">
                      {envState.rollout_pct}%
                    </span>
                  </div>
                </div>
                <div className="bg-black/20 rounded p-3">
                  <div className="text-gray-400 text-xs mb-1">Safe Default</div>
                  <code className="text-amber-300 text-sm">
                    {envState.safe_default}
                  </code>
                </div>
                <div className="bg-black/20 rounded p-3">
                  <div className="text-gray-400 text-xs mb-1">Last Updated</div>
                  <div className="text-white text-sm">
                    {new Date(envState.updated_at * 1000).toLocaleString()}
                  </div>
                </div>
              </div>
            ) : (
              <p className="text-gray-500 text-sm">
                No state for this environment yet.
              </p>
            )}

            {/* Kill switch */}
            {envState?.enabled && (
              <div className="mt-4 pt-4 border-t border-gray-700">
                {activeEnvKillFlow.status === "confirming" &&
                activeEnvKillFlow.blastRadius ? (
                  <BlastRadiusBadge
                    result={activeEnvKillFlow.blastRadius}
                    onConfirm={(justification) =>
                      confirmKill(activeEnv, justification)
                    }
                    onCancel={() => cancelKill(activeEnv)}
                  />
                ) : (
                  <>
                    <button
                      onClick={() => void requestKillSwitch(activeEnv)}
                      disabled={
                        activeEnvKillFlow.status !== "idle" &&
                        activeEnvKillFlow.status !== "error"
                      }
                      className="px-4 py-2 rounded bg-red-800 hover:bg-red-700 text-red-200 text-sm font-medium disabled:opacity-50 transition-colors"
                    >
                      {activeEnvKillFlow.status === "killing"
                        ? "Disabling…"
                        : activeEnvKillFlow.status === "checking"
                          ? "Checking blast radius…"
                          : `Kill Switch — Disable in ${activeEnv}`}
                    </button>
                    <p className="text-gray-600 text-xs mt-1">
                      Instantly disables flag. All targeting rules preserved for
                      re-enable.
                    </p>
                    {activeEnvKillFlow.status === "error" && (
                      <p className="text-red-400 text-xs mt-1">
                        Kill switch failed: {activeEnvKillFlow.error}. The flag
                        may still be enabled — try again.
                      </p>
                    )}
                  </>
                )}
              </div>
            )}

            {/* Autonomous rollout — hidden while a kill is pending/in flight for
                this env, so an operator can't apply a rollout-increasing
                recommendation at the same moment they're deciding whether to
                disable the flag entirely (found by adversarial review). */}
            {!envState ||
            !envState.enabled ||
            activeEnvKillFlow.status === "idle" ||
            activeEnvKillFlow.status === "error" ? (
              <AutonomousRolloutToggle
                flagKey={flag.key}
                environment={activeEnv}
                currentRolloutPct={envState?.rollout_pct ?? 0}
              />
            ) : null}
          </div>

          {/* Audit log */}
          <div className="bg-gray-900 rounded-lg border border-gray-700">
            <div className="px-5 py-3 border-b border-gray-700 text-sm font-medium text-white">
              Audit Log
              <span className="text-gray-500 font-normal ml-2">
                (last 20 entries)
              </span>
            </div>
            {audit.length === 0 ? (
              <div className="p-6 text-center text-gray-500 text-sm">
                No audit entries yet.
              </div>
            ) : (
              <div className="divide-y divide-gray-800 text-xs">
                {audit.map((entry) => (
                  <div
                    key={entry.id}
                    className="px-5 py-3 flex items-start gap-4"
                  >
                    <div className="text-gray-600 w-36 shrink-0">
                      {new Date(entry.created_at * 1000).toLocaleTimeString()}
                    </div>
                    <div className="flex-1">
                      <span
                        className={`px-1.5 py-0.5 rounded text-xs mr-2 ${
                          entry.event_type.includes("kill")
                            ? "bg-red-900/50 text-red-400"
                            : entry.event_type.includes("create")
                              ? "bg-green-900/50 text-green-400"
                              : "bg-gray-800 text-gray-400"
                        }`}
                      >
                        {entry.event_type}
                      </span>
                      <span className="text-gray-400">{entry.environment}</span>
                    </div>
                    <div className="text-gray-500 shrink-0">{entry.actor}</div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Dependencies tab content */}
      {activeTab === "dependencies" && key && (
        <DependenciesTab flagKey={key} apiUrl={apiUrl} token={tok} />
      )}

      {/* Targeting Rules tab content */}
      {activeTab === "targeting-rules" && key && (
        <TargetingRulesTab
          flagKey={key}
          apiUrl={apiUrl}
          token={tok}
          environment={activeEnv}
          environments={ENVS}
          onEnvironmentChange={setActiveEnv}
        />
      )}
    </div>
  );
}
