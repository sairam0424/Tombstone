import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';

interface CriticalFlag {
  key: string;
  score: number;
  in_degree: number;
  out_degree: number;
  avg_edge_weight: number;
  blast_radius_tier: string;
}

interface CriticalFlagsPanelProps {
  apiUrl: string;
  token: string;
  limit?: number;
}

const tierColor: Record<string, string> = {
  BLOCKED: 'text-red-500',
  HIGH: 'text-orange-500',
  MEDIUM: 'text-yellow-500',
  LOW: 'text-green-500',
};

export function CriticalFlagsPanel({ apiUrl, token, limit = 20 }: CriticalFlagsPanelProps) {
  const [flags, setFlags] = useState<CriticalFlag[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    fetch(`${apiUrl}/api/v1/graph/critical-flags?limit=${limit}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then(r => r.json())
      .then(data => setFlags(data.flags || []))
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [apiUrl, token, limit]);

  if (loading) return <div className="text-gray-400">Loading critical flags...</div>;

  return (
    <div className="bg-gray-900 border border-gray-800 rounded p-4">
      <h3 className="text-lg font-semibold text-white mb-3">Critical Flags (Dependency Health)</h3>
      <div className="space-y-2">
        {flags.length === 0 && <div className="text-gray-400">No critical flags found.</div>}
        {flags.map(flag => (
          <Link
            key={flag.key}
            to={`/flags/${flag.key}`}
            className="flex items-center justify-between p-2 bg-gray-800 rounded hover:bg-gray-700 transition"
          >
            <div className="flex-1">
              <div className="font-mono text-sm text-white">{flag.key}</div>
              <div className="text-xs text-gray-400">
                {flag.in_degree} in / {flag.out_degree} out · avg weight {flag.avg_edge_weight.toFixed(2)}
              </div>
            </div>
            <div className="flex items-center gap-3">
              <span className={`text-xs font-semibold ${tierColor[flag.blast_radius_tier] || 'text-gray-400'}`}>
                {flag.blast_radius_tier}
              </span>
              <span className="text-sm font-mono text-gray-300">{flag.score.toFixed(2)}</span>
            </div>
          </Link>
        ))}
      </div>
    </div>
  );
}
