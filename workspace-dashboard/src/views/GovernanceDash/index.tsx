import { useState, useEffect } from 'react';

interface StaleFlag {
  flag_key: string;
  owner_id: string;
  days_at_100_pct: number;
  stale_score: number;
  recommended_action: 'REVIEW' | 'NOTIFY_OWNER' | 'ARCHIVE';
}

interface AutonomousRecommendation {
  flag_key: string;
  environment: string;
  current_pct: number;
  recommended_pct: number;
  confidence: number;
  reason: string;
  should_advance: boolean;
}

interface HealthSummary {
  total_flags: number;
  stale_flags: number;
  health_score: number;
}

const actionColor: Record<string, string> = {
  ARCHIVE: 'text-red-400 bg-red-900/30',
  NOTIFY_OWNER: 'text-amber-400 bg-amber-900/30',
  REVIEW: 'text-blue-400 bg-blue-900/30',
};

export default function GovernanceDash() {
  const [stale, setStale] = useState<StaleFlag[]>([]);
  const [health, setHealth] = useState<HealthSummary | null>(null);
  const [loading, setLoading] = useState(true);
  const [autonomousRecs, setAutonomousRecs] = useState<AutonomousRecommendation[]>([]);
  const [applyingKey, setApplyingKey] = useState<string | null>(null);

  const intelUrl = (import.meta.env['VITE_INTEL_URL'] as string | undefined) ?? 'http://localhost:8083';

  const fetchAutonomousRecs = async () => {
    try {
      const res = await globalThis.fetch(`${intelUrl}/api/v1/rollout/recommendations`);
      if (!res.ok) return;
      const data = (await res.json()) as { recommendations?: AutonomousRecommendation[] };
      setAutonomousRecs(data.recommendations ?? []);
    } catch {
      // silently ignore
    }
  };

  useEffect(() => {
    const apiUrl = (import.meta.env['VITE_API_URL'] as string | undefined) ?? 'http://localhost:8081';
    const token = (import.meta.env['VITE_SDK_TOKEN'] as string | undefined) ?? 'sdk-dev-token-change-in-prod';
    const headers = { Authorization: `Bearer ${token}` };

    Promise.all([
      globalThis.fetch(`${intelUrl}/api/v1/stale`, { headers }).then(r => r.json()),
      globalThis.fetch(`${apiUrl}/api/v1/flags?project_id=00000000-0000-0000-0000-000000000001`, { headers }).then(r => r.json()),
    ]).then(([staleData, flagsData]) => {
      const staleFlags = (staleData as { stale_flags?: StaleFlag[] }).stale_flags ?? [];
      const allFlags = (flagsData as { flags?: unknown[]; total?: number }).flags ?? [];
      const total = allFlags.length;
      const staleCount = staleFlags.length;
      setStale(staleFlags);
      setHealth({
        total_flags: total,
        stale_flags: staleCount,
        health_score: total > 0 ? Math.max(0, 1 - staleCount / total) : 1,
      });
    }).catch(console.error).finally(() => setLoading(false));

    void fetchAutonomousRecs();
  }, []);

  const handleApplyRec = async (rec: AutonomousRecommendation) => {
    setApplyingKey(`${rec.flag_key}:${rec.environment}`);
    try {
      await globalThis.fetch(`${intelUrl}/api/v1/rollout/update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          flag_key: rec.flag_key,
          environment: rec.environment,
          rollout_pct: rec.recommended_pct,
        }),
      });
      await fetchAutonomousRecs();
    } catch {
      // silently ignore
    } finally {
      setApplyingKey(null);
    }
  };

  const healthPct = Math.round((health?.health_score ?? 1) * 100);
  const healthColor = healthPct >= 80 ? 'text-green-400' : healthPct >= 60 ? 'text-amber-400' : 'text-red-400';

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <h1 className="text-2xl font-bold text-white mb-6">Governance</h1>

      {loading && <div className="text-gray-500">Loading…</div>}

      {health && !loading && (
        <>
          {/* Health score cards */}
          <div className="grid grid-cols-3 gap-4 mb-8">
            <div className="bg-gray-900 rounded-lg p-4 border border-gray-700">
              <div className="text-gray-400 text-sm">Health Score</div>
              <div className={`text-4xl font-bold mt-1 ${healthColor}`}>{healthPct}%</div>
            </div>
            <div className="bg-gray-900 rounded-lg p-4 border border-gray-700">
              <div className="text-gray-400 text-sm">Total Flags</div>
              <div className="text-4xl font-bold mt-1 text-white">{health.total_flags}</div>
            </div>
            <div className="bg-gray-900 rounded-lg p-4 border border-gray-700">
              <div className="text-gray-400 text-sm">Stale Flags</div>
              <div className={`text-4xl font-bold mt-1 ${health.stale_flags > 0 ? 'text-amber-400' : 'text-green-400'}`}>
                {health.stale_flags}
              </div>
            </div>
          </div>

          {/* Staleness leaderboard */}
          <div className="bg-gray-900 rounded-lg border border-gray-700">
            <div className="p-4 border-b border-gray-700">
              <h2 className="font-semibold text-white">Stale Flag Leaderboard</h2>
              <p className="text-gray-400 text-xs mt-1">Flags at 100% rollout for 30+ days — candidates for cleanup</p>
            </div>

            {stale.length === 0 ? (
              <div className="p-6 text-gray-500 text-center text-sm">No stale flags detected ✓</div>
            ) : (
              <div className="divide-y divide-gray-800">
                {stale.map((f) => (
                  <div key={f.flag_key} className="p-4 flex items-center justify-between hover:bg-gray-800/50">
                    <div>
                      <a href={`/flags/${f.flag_key}`} className="text-sm font-mono text-blue-400 hover:underline">
                        {f.flag_key}
                      </a>
                      <div className="text-xs text-gray-500 mt-0.5">
                        Owner: {f.owner_id} · {f.days_at_100_pct}d at 100%
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <div className="w-24 bg-gray-700 rounded-full h-1.5">
                        <div
                          className="bg-amber-500 h-1.5 rounded-full"
                          style={{ width: `${Math.round(f.stale_score * 100)}%` }}
                        />
                      </div>
                      <span className={`text-xs px-2 py-0.5 rounded font-medium ${actionColor[f.recommended_action]}`}>
                        {f.recommended_action}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Autonomous Rollout Status */}
          <div className="bg-gray-900 rounded-lg border border-gray-700 mt-6">
            <div className="p-4 border-b border-gray-700">
              <h2 className="font-semibold text-white">Autonomous Rollout Status</h2>
              <p className="text-gray-400 text-xs mt-1">Flags currently managed by the autonomous rollout engine</p>
            </div>

            {autonomousRecs.length === 0 ? (
              <div className="p-6 text-gray-500 text-center text-sm">None in autonomous mode</div>
            ) : (
              <div className="overflow-x-auto">
                <table style={{ width: '100%', borderCollapse: 'collapse' as const }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid #21262d' }}>
                      {['Flag Key', 'Env', 'Current %', 'Recommended %', 'Confidence', ''].map(h => (
                        <th key={h} style={{
                          padding: '10px 16px',
                          textAlign: 'left' as const,
                          color: '#8b949e',
                          fontSize: '11px',
                          fontWeight: 600,
                          letterSpacing: '0.05em',
                          textTransform: 'uppercase' as const,
                        }}>{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {autonomousRecs.map(rec => {
                      const rowKey = `${rec.flag_key}:${rec.environment}`;
                      const isApplying = applyingKey === rowKey;
                      const confPct = Math.round(rec.confidence * 100);
                      const confColor = confPct >= 80 ? '#3fb950' : confPct >= 60 ? '#d29922' : '#f85149';
                      return (
                        <tr key={rowKey} style={{ borderBottom: '1px solid #161b22' }}>
                          <td style={{ padding: '10px 16px' }}>
                            <a href={`/flags/${rec.flag_key}`} style={{ color: '#58a6ff', fontFamily: 'monospace', fontSize: '13px', textDecoration: 'none' }}>
                              {rec.flag_key}
                            </a>
                          </td>
                          <td style={{ padding: '10px 16px', color: '#8b949e', fontSize: '12px' }}>{rec.environment}</td>
                          <td style={{ padding: '10px 16px', color: '#e6edf3', fontSize: '13px', fontWeight: 600 }}>{rec.current_pct}%</td>
                          <td style={{ padding: '10px 16px', color: '#58a6ff', fontSize: '13px', fontWeight: 600 }}>{rec.recommended_pct}%</td>
                          <td style={{ padding: '10px 16px' }}>
                            <span style={{ color: confColor, fontSize: '13px', fontWeight: 600 }}>{confPct}%</span>
                          </td>
                          <td style={{ padding: '10px 16px' }}>
                            {rec.should_advance ? (
                              <button
                                onClick={() => void handleApplyRec(rec)}
                                disabled={isApplying}
                                style={{
                                  background: '#1f6feb',
                                  color: '#e6edf3',
                                  border: 'none',
                                  borderRadius: '5px',
                                  padding: '5px 12px',
                                  fontSize: '12px',
                                  fontWeight: 600,
                                  cursor: isApplying ? 'not-allowed' : 'pointer',
                                  opacity: isApplying ? 0.5 : 1,
                                }}
                              >
                                {isApplying ? 'Applying…' : 'Apply'}
                              </button>
                            ) : (
                              <span style={{ color: '#6e7681', fontSize: '12px' }}>—</span>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}
