import { useState, useEffect } from 'react';
import { useParams, Link } from 'react-router-dom';
import { FlagHealthBadge } from '../../components/FlagHealthBadge.js';
import { CircuitBreakerStatus } from '../../components/CircuitBreakerStatus.js';
import { AutonomousRolloutToggle } from '../../components/AutonomousRolloutToggle.js';


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

const envColor: Record<Env, string> = {
  development: 'border-blue-600 text-blue-400',
  staging: 'border-amber-600 text-amber-400',
  production: 'border-green-600 text-green-400',
};

export default function FlagDetail() {
  const { key } = useParams<{ key: string }>();
  const [flag, setFlag] = useState<Flag | null>(null);
  const [envStates, setEnvStates] = useState<Record<string, FlagEnvState>>({});
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [activeEnv, setActiveEnv] = useState<Env>('production');
  const [loading, setLoading] = useState(true);
  const [killing, setKilling] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const apiUrl = (import.meta as unknown as { env: Record<string, string> }).env['VITE_API_URL'] ?? 'http://localhost:8081';
  const tok = (import.meta as unknown as { env: Record<string, string> }).env['VITE_SDK_TOKEN'] ?? 'sdk-dev-token-change-in-prod';
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

    // Fetch snapshot per env for rollout state
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
      // Refresh env state
      const snap = await fetch(`${apiUrl}/api/v1/environments/snapshot?environment=${env}`, { headers }).then(r => r.json()) as { flags: FlagEnvState[] };
      const match = snap.flags?.find(f => f.flag_key === key);
      if (match) setEnvStates(prev => ({ ...prev, [env]: match }));
    } finally {
      setKilling(false);
    }
  };

  if (loading) return <div className="p-8 text-gray-500">Loading…</div>;
  if (error || !flag) return (
    <div className="p-8">
      <Link to="/" className="text-blue-400 hover:underline text-sm">← Back to flags</Link>
      <p className="text-red-400 mt-4">{error ?? 'Flag not found'}</p>
    </div>
  );

  const envState = envStates[activeEnv];

  return (
    <div className="p-6 max-w-5xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <Link to="/" className="text-gray-500 hover:text-white text-sm">← All Flags</Link>
        <div className="flex items-start justify-between mt-2">
          <div>
            <h1 className="text-2xl font-bold text-white font-mono">{flag.key}</h1>
            <p className="text-gray-400 mt-1">{flag.name}</p>
            {flag.description && <p className="text-gray-500 text-sm mt-1">{flag.description}</p>}
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
          <span>Type: <span className="text-gray-300">{flag.flag_type}</span></span>
          <span>Owner: <span className="text-gray-300">{flag.owner_id}</span></span>
          <span>Created: <span className="text-gray-300">{new Date(flag.created_at * 1000).toLocaleDateString()}</span></span>
        </div>
      </div>

      {/* Environment tabs */}
      <div className="flex gap-2 mb-4">
        {ENVS.map(env => (
          <button key={env}
            onClick={() => setActiveEnv(env)}
            className={`px-4 py-1.5 rounded border text-xs font-medium transition-colors ${
              activeEnv === env
                ? envColor[env] + ' bg-white/5'
                : 'border-gray-700 text-gray-500 hover:border-gray-500'
            }`}
          >
            {env}
          </button>
        ))}
      </div>

      {/* Environment state card */}
      <div className="bg-gray-900 rounded-lg border border-gray-700 p-5 mb-6">
        <div className="flex items-center justify-between mb-4">
          <h2 className="font-semibold text-white capitalize">{activeEnv} Environment</h2>
          <CircuitBreakerStatus flagKey={flag.key} />
        </div>

        {envState ? (
          <div className="grid grid-cols-2 gap-4">
            <div className="bg-black/20 rounded p-3">
              <div className="text-gray-400 text-xs mb-1">Status</div>
              <div className={`text-lg font-bold ${envState.enabled ? 'text-green-400' : 'text-gray-500'}`}>
                {envState.enabled ? 'ENABLED' : 'DISABLED'}
              </div>
            </div>
            <div className="bg-black/20 rounded p-3">
              <div className="text-gray-400 text-xs mb-1">Rollout %</div>
              <div className="flex items-center gap-3">
                <div className="flex-1 bg-gray-700 rounded-full h-2">
                  <div className="bg-blue-500 h-2 rounded-full" style={{ width: `${envState.rollout_pct}%` }} />
                </div>
                <span className="text-white text-sm font-medium w-10 text-right">{envState.rollout_pct}%</span>
              </div>
            </div>
            <div className="bg-black/20 rounded p-3">
              <div className="text-gray-400 text-xs mb-1">Safe Default</div>
              <code className="text-amber-300 text-sm">{envState.safe_default}</code>
            </div>
            <div className="bg-black/20 rounded p-3">
              <div className="text-gray-400 text-xs mb-1">Last Updated</div>
              <div className="text-white text-sm">{new Date(envState.updated_at * 1000).toLocaleString()}</div>
            </div>
          </div>
        ) : (
          <p className="text-gray-500 text-sm">No state for this environment yet.</p>
        )}

        {/* Kill switch */}
        {envState?.enabled && (
          <div className="mt-4 pt-4 border-t border-gray-700">
            <button
              onClick={() => void killSwitch(activeEnv)}
              disabled={killing}
              className="px-4 py-2 rounded bg-red-800 hover:bg-red-700 text-red-200 text-sm font-medium disabled:opacity-50 transition-colors"
            >
              {killing ? 'Disabling…' : `Kill Switch — Disable in ${activeEnv}`}
            </button>
            <p className="text-gray-600 text-xs mt-1">Instantly disables flag. All targeting rules preserved for re-enable.</p>
          </div>
        )}

        {/* Autonomous rollout */}
        {envState && !envState.enabled ? null : (
          <AutonomousRolloutToggle
            flagKey={flag.key}
            environment={activeEnv}
            currentRolloutPct={envState?.rollout_pct ?? 0}
          />
        )}
      </div>

      {/* Audit log */}
      <div className="bg-gray-900 rounded-lg border border-gray-700">
        <div className="px-5 py-3 border-b border-gray-700 text-sm font-medium text-white">
          Audit Log
          <span className="text-gray-500 font-normal ml-2">(last 20 entries)</span>
        </div>
        {audit.length === 0 ? (
          <div className="p-6 text-center text-gray-500 text-sm">No audit entries yet.</div>
        ) : (
          <div className="divide-y divide-gray-800 text-xs">
            {audit.map(entry => (
              <div key={entry.id} className="px-5 py-3 flex items-start gap-4">
                <div className="text-gray-600 w-36 shrink-0">
                  {new Date(entry.created_at * 1000).toLocaleTimeString()}
                </div>
                <div className="flex-1">
                  <span className={`px-1.5 py-0.5 rounded text-xs mr-2 ${
                    entry.event_type.includes('kill') ? 'bg-red-900/50 text-red-400' :
                    entry.event_type.includes('create') ? 'bg-green-900/50 text-green-400' :
                    'bg-gray-800 text-gray-400'
                  }`}>{entry.event_type}</span>
                  <span className="text-gray-400">{entry.environment}</span>
                </div>
                <div className="text-gray-500 shrink-0">{entry.actor}</div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
