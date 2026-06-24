import { useState, useEffect } from 'react';
import { useParams, Link } from 'react-router-dom';
import { FlagHealthBadge } from '../../components/FlagHealthBadge.js';
import { CircuitBreakerStatus } from '../../components/CircuitBreakerStatus.js';
import { AutonomousRolloutToggle } from '../../components/AutonomousRolloutToggle.js';
import { API_URL, SDK_TOKEN } from '../../config.js';


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
  state: 'DRAFT' | 'ACTIVE' | 'COMPLETE' | 'ARCHIVED';
  owner_id: string;
  created_at: number;
  updated_at: number;
}

type Env = 'development' | 'staging' | 'production';
const ENVS: Env[] = ['development', 'staging', 'production'];

const envConfig: Record<Env, {
  label: string;
  accent: string;
  bar: string;
  badge: string;
  toggleOn: string;
  border: string;
}> = {
  development: {
    label: 'Development',
    accent: 'text-blue-400',
    bar: 'bg-blue-500',
    badge: 'bg-blue-900/40 text-blue-300 border border-blue-800',
    toggleOn: 'bg-blue-500',
    border: 'border-blue-800/50',
  },
  staging: {
    label: 'Staging',
    accent: 'text-amber-400',
    bar: 'bg-amber-500',
    badge: 'bg-amber-900/40 text-amber-300 border border-amber-800',
    toggleOn: 'bg-amber-500',
    border: 'border-amber-800/50',
  },
  production: {
    label: 'Production',
    accent: 'text-emerald-400',
    bar: 'bg-emerald-500',
    badge: 'bg-emerald-900/40 text-emerald-300 border border-emerald-800',
    toggleOn: 'bg-emerald-500',
    border: 'border-emerald-800/50',
  },
};

const flagTypeBadge: Record<string, string> = {
  boolean: 'bg-sky-900/50 text-sky-300 border border-sky-800',
  multivariate: 'bg-violet-900/50 text-violet-300 border border-violet-800',
  experiment: 'bg-pink-900/50 text-pink-300 border border-pink-800',
};

function SectionCard({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return (
    <div
      className={`rounded-xl border p-6 ${className}`}
      style={{ background: '#111', borderColor: '#1a1a1a' }}
    >
      {children}
    </div>
  );
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <h2 className="text-sm font-semibold tracking-widest text-gray-400 uppercase">{children}</h2>
      <div className="mt-2 h-px" style={{ background: '#1a1a1a' }} />
    </div>
  );
}

function ToggleVisual({ enabled, envKey }: { enabled: boolean; envKey: Env }) {
  const cfg = envConfig[envKey];
  return (
    <div className="flex items-center gap-2">
      <div
        className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors ${
          enabled ? cfg.toggleOn : 'bg-gray-700'
        }`}
      >
        <span
          className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${
            enabled ? 'translate-x-4' : 'translate-x-1'
          }`}
        />
      </div>
      <span
        className={`text-xs font-semibold tracking-wide ${
          enabled ? cfg.accent : 'text-gray-500'
        }`}
      >
        {enabled ? 'ON' : 'OFF'}
      </span>
    </div>
  );
}

function RolloutBar({ pct, envKey }: { pct: number; envKey: Env }) {
  const cfg = envConfig[envKey];
  return (
    <div className="flex items-center gap-3">
      <div className="flex-1 rounded-full h-1.5" style={{ background: '#252525' }}>
        <div
          className={`h-1.5 rounded-full transition-all ${cfg.bar}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-white text-xs font-mono font-semibold w-9 text-right">{pct}%</span>
    </div>
  );
}

export default function FlagDetail() {
  const { key } = useParams<{ key: string }>();
  const [flag, setFlag] = useState<Flag | null>(null);
  const [envStates, setEnvStates] = useState<Record<string, FlagEnvState>>({});
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [activeEnv, setActiveEnv] = useState<Env>('production');
  const [loading, setLoading] = useState(true);
  const [killing, setKilling] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const apiUrl = API_URL;
  const tok = SDK_TOKEN;
  const headers = { Authorization: `Bearer ${tok}`, 'Content-Type': 'application/json' };

  useEffect(() => {
    if (!key) return;
    setLoading(true);
    Promise.all([
      fetch(`${apiUrl}/api/v1/flags/${key}`, { headers }).then(r => r.json()),
      ...ENVS.map(env =>
        fetch(`${apiUrl}/api/v1/flags/${key}?environment=${env}`, { headers })
          .then(r => r.ok ? r.json() : null).catch(() => null)
      ),
      fetch(`${apiUrl}/api/v1/audit?flag_key=${key}&limit=20`, { headers }).then(r => r.json()),
    ]).then(([f, ...rest]) => {
      setFlag(f as Flag);
      const auditData = rest.pop() as { entries: AuditEntry[] };
      setAudit(auditData?.entries ?? []);
    }).catch(e => setError(String(e))).finally(() => setLoading(false));

    ENVS.forEach(env => {
      fetch(`${apiUrl}/api/v1/environments/snapshot?environment=${env}`, { headers })
        .then(r => r.json())
        .then((snap: { flags: FlagEnvState[] }) => {
          const match = snap.flags?.find(f => f.flag_key === key);
          if (match) setEnvStates(prev => ({ ...prev, [env]: match }));
        })
        .catch(() => null);
    });
  }, [key]);

  const killSwitch = async (env: Env) => {
    if (!key) return;
    setKilling(true);
    try {
      await fetch(`${apiUrl}/api/v1/flags/${key}/kill`, {
        method: 'POST', headers,
        body: JSON.stringify({ environment: env, reason: 'manual kill switch from dashboard' }),
      });
      const snap = await fetch(`${apiUrl}/api/v1/environments/snapshot?environment=${env}`, { headers }).then(r => r.json()) as { flags: FlagEnvState[] };
      const match = snap.flags?.find(f => f.flag_key === key);
      if (match) setEnvStates(prev => ({ ...prev, [env]: match }));
    } finally {
      setKilling(false);
    }
  };

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center" style={{ background: '#0a0a0a' }}>
        <div className="flex items-center gap-3 text-gray-500">
          <div className="w-4 h-4 rounded-full border-2 border-blue-500 border-t-transparent animate-spin" />
          <span className="text-sm">Loading flag…</span>
        </div>
      </div>
    );
  }

  if (error || !flag) {
    return (
      <div className="min-h-screen p-8" style={{ background: '#0a0a0a' }}>
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 text-sm transition-colors hover:text-gray-300"
          style={{ color: '#6b7280' }}
        >
          ← All Flags
        </Link>
        <p className="text-red-400 mt-6 text-sm">{error ?? 'Flag not found'}</p>
      </div>
    );
  }

  const envState = envStates[activeEnv];
  const typeBadgeClass = flagTypeBadge[flag.flag_type] ?? 'bg-gray-800 text-gray-300 border border-gray-700';

  return (
    <div className="min-h-screen px-6 py-8" style={{ background: '#0a0a0a' }}>
      <div className="max-w-7xl mx-auto">

        {/* Back link */}
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 text-sm mb-6 transition-colors hover:text-gray-300"
          style={{ color: '#6b7280' }}
        >
          ← All Flags
        </Link>

        {/* Flag header */}
        <div className="flex items-start justify-between mb-8">
          <div className="flex-1 min-w-0 mr-6">
            <h1 className="text-3xl font-bold font-mono tracking-tight text-blue-400 truncate">
              {flag.key}
            </h1>
            <p className="text-gray-300 mt-1.5 text-base">{flag.name}</p>
            {flag.description && (
              <p className="text-gray-500 text-sm mt-1">{flag.description}</p>
            )}
          </div>
          <div className="flex items-center gap-3 shrink-0">
            <Link
              to={`/flags/${flag.key}/slo`}
              className="px-3 py-1.5 rounded-lg text-xs font-medium border border-blue-800 text-blue-400 hover:bg-blue-900/20 transition-colors"
            >
              View SLO →
            </Link>
            <FlagHealthBadge state={flag.state} />
          </div>
        </div>

        {/* Two-column layout */}
        <div className="flex gap-6 items-start flex-col xl:flex-row">

          {/* LEFT — main content (2/3) */}
          <div className="flex-1 min-w-0 space-y-6">

            {/* Overview section */}
            <SectionCard>
              <SectionTitle>Overview</SectionTitle>
              <div className="grid grid-cols-2 sm:grid-cols-3 gap-4 text-sm">
                <div>
                  <div className="text-gray-500 text-xs mb-1">Flag Type</div>
                  <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${typeBadgeClass}`}>
                    {flag.flag_type}
                  </span>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Owner</div>
                  <div className="text-gray-300 font-mono text-xs truncate">{flag.owner_id}</div>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Created</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.created_at * 1000).toLocaleDateString()}</div>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Last Modified</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.updated_at * 1000).toLocaleDateString()}</div>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Flag ID</div>
                  <div className="text-gray-500 font-mono text-xs truncate">{flag.id}</div>
                </div>
              </div>
            </SectionCard>

            {/* Environments section */}
            <SectionCard>
              <SectionTitle>Environments</SectionTitle>
              <div className="space-y-3">
                {ENVS.map(env => {
                  const cfg = envConfig[env];
                  const state = envStates[env];
                  return (
                    <div
                      key={env}
                      className={`rounded-lg border p-4 transition-colors cursor-pointer ${
                        activeEnv === env
                          ? `${cfg.border} bg-white/[0.03]`
                          : 'border-transparent hover:border-gray-800'
                      }`}
                      style={activeEnv !== env ? { borderColor: '#181818', background: '#0d0d0d' } : {}}
                      onClick={() => setActiveEnv(env)}
                    >
                      <div className="flex items-center justify-between mb-3">
                        <div className="flex items-center gap-2.5">
                          <span className={`px-2 py-0.5 rounded text-xs font-semibold ${cfg.badge}`}>
                            {cfg.label}
                          </span>
                          {state && <ToggleVisual enabled={state.enabled} envKey={env} />}
                        </div>
                        {state && (
                          <span className="text-gray-500 text-xs">
                            Updated {new Date(state.updated_at * 1000).toLocaleDateString()}
                          </span>
                        )}
                      </div>

                      {state ? (
                        <div className="space-y-2">
                          <div>
                            <div className="flex items-center justify-between mb-1">
                              <span className="text-gray-500 text-xs">Rollout</span>
                            </div>
                            <RolloutBar pct={state.rollout_pct} envKey={env} />
                          </div>
                          <div className="flex items-center gap-4 mt-2 text-xs">
                            <span className="text-gray-500">
                              Safe default: <code className="text-amber-300 ml-1">{state.safe_default}</code>
                            </span>
                          </div>
                        </div>
                      ) : (
                        <p className="text-gray-600 text-xs">No state for this environment yet.</p>
                      )}
                    </div>
                  );
                })}
              </div>
            </SectionCard>

            {/* Targeting Rules section (active env detail) */}
            <SectionCard>
              <SectionTitle>Targeting Rules</SectionTitle>

              <div className="flex items-center justify-between mb-4">
                <div className="flex items-center gap-2 text-sm text-gray-300">
                  <span className={`px-2 py-0.5 rounded text-xs font-semibold ${envConfig[activeEnv].badge}`}>
                    {envConfig[activeEnv].label}
                  </span>
                  <span className="text-gray-500">environment selected</span>
                </div>
                <CircuitBreakerStatus flagKey={flag.key} />
              </div>

              {envState ? (
                <div className="space-y-4">
                  <div
                    className="grid grid-cols-2 gap-3 text-sm"
                  >
                    <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                      <div className="text-gray-500 text-xs mb-1.5">Status</div>
                      <ToggleVisual enabled={envState.enabled} envKey={activeEnv} />
                    </div>
                    <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                      <div className="text-gray-500 text-xs mb-1.5">Rollout %</div>
                      <RolloutBar pct={envState.rollout_pct} envKey={activeEnv} />
                    </div>
                    <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                      <div className="text-gray-500 text-xs mb-1.5">Safe Default</div>
                      <code className="text-amber-300 text-sm">{envState.safe_default}</code>
                    </div>
                    <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                      <div className="text-gray-500 text-xs mb-1.5">Last Updated</div>
                      <div className="text-gray-300 text-xs">{new Date(envState.updated_at * 1000).toLocaleString()}</div>
                    </div>
                  </div>

                  {/* Kill switch */}
                  {envState.enabled && (
                    <div className="pt-4 border-t" style={{ borderColor: '#1a1a1a' }}>
                      <button
                        onClick={() => void killSwitch(activeEnv)}
                        disabled={killing}
                        className="px-4 py-2 rounded-lg bg-red-950 hover:bg-red-900 text-red-300 text-sm font-medium disabled:opacity-50 transition-colors border border-red-900"
                      >
                        {killing ? 'Disabling…' : `Kill Switch — Disable in ${activeEnv}`}
                      </button>
                      <p className="text-gray-600 text-xs mt-1.5">
                        Instantly disables flag. All targeting rules preserved for re-enable.
                      </p>
                    </div>
                  )}

                  {/* Autonomous rollout */}
                  {envState.enabled && (
                    <AutonomousRolloutToggle
                      flagKey={flag.key}
                      environment={activeEnv}
                      currentRolloutPct={envState.rollout_pct}
                    />
                  )}
                </div>
              ) : (
                <p className="text-gray-600 text-sm">No targeting rules configured for {activeEnv} yet.</p>
              )}
            </SectionCard>

          </div>

          {/* RIGHT — metadata sidebar (1/3) */}
          <div className="w-full xl:w-80 shrink-0 space-y-6">

            {/* Metadata card */}
            <SectionCard>
              <SectionTitle>Metadata</SectionTitle>
              <div className="space-y-4 text-sm">
                <div>
                  <div className="text-gray-500 text-xs mb-1.5">Flag Type</div>
                  <span className={`inline-block px-2.5 py-1 rounded-lg text-xs font-medium ${typeBadgeClass}`}>
                    {flag.flag_type}
                  </span>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Owner</div>
                  <div className="text-gray-300 font-mono text-xs break-all">{flag.owner_id}</div>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Created</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.created_at * 1000).toLocaleString()}</div>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Last Modified</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.updated_at * 1000).toLocaleString()}</div>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Flag ID</div>
                  <div className="text-gray-500 font-mono text-xs break-all">{flag.id}</div>
                </div>
              </div>
            </SectionCard>

            {/* Quick env summary */}
            <SectionCard>
              <SectionTitle>Environment Summary</SectionTitle>
              <div className="space-y-3">
                {ENVS.map(env => {
                  const cfg = envConfig[env];
                  const state = envStates[env];
                  return (
                    <div key={env} className="flex items-center justify-between">
                      <span className={`text-xs font-medium ${cfg.accent}`}>{cfg.label}</span>
                      <div className="flex items-center gap-2">
                        {state ? (
                          <>
                            <span className={`text-xs font-semibold ${state.enabled ? cfg.accent : 'text-gray-600'}`}>
                              {state.enabled ? 'ON' : 'OFF'}
                            </span>
                            <span className="text-gray-600 text-xs font-mono">{state.rollout_pct}%</span>
                          </>
                        ) : (
                          <span className="text-gray-700 text-xs">—</span>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </SectionCard>

            {/* Audit log */}
            <SectionCard>
              <SectionTitle>Audit Log</SectionTitle>
              <div className="text-gray-500 text-xs mb-3">Last 20 entries</div>
              {audit.length === 0 ? (
                <p className="text-gray-600 text-xs text-center py-4">No audit entries yet.</p>
              ) : (
                <div className="space-y-2">
                  {audit.map(entry => (
                    <div
                      key={entry.id}
                      className="rounded-lg p-3 text-xs"
                      style={{ background: '#0d0d0d', border: '1px solid #181818' }}
                    >
                      <div className="flex items-center justify-between mb-1">
                        <span className={`px-1.5 py-0.5 rounded text-xs font-medium ${
                          entry.event_type.includes('kill')
                            ? 'bg-red-950 text-red-400 border border-red-900'
                            : entry.event_type.includes('create')
                            ? 'bg-emerald-950 text-emerald-400 border border-emerald-900'
                            : 'bg-gray-900 text-gray-400 border border-gray-800'
                        }`}>
                          {entry.event_type}
                        </span>
                        <span className={`text-xs font-medium ${envConfig[entry.environment as Env]?.accent ?? 'text-gray-400'}`}>
                          {entry.environment}
                        </span>
                      </div>
                      <div className="flex items-center justify-between text-gray-600 mt-1">
                        <span>{entry.actor}</span>
                        <span>{new Date(entry.created_at * 1000).toLocaleTimeString()}</span>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </SectionCard>

          </div>
        </div>
      </div>
    </div>
  );
}
