import { useState, useEffect } from 'react';
import { EVAL_URL } from '../config.js';

type CircuitState = 'CLOSED' | 'OPEN' | 'HALF_OPEN';

interface Props {
  flagKey: string;
}

const stateConfig: Record<CircuitState, { color: string; label: string; description: string }> = {
  CLOSED:    { color: 'text-green-400', label: 'Healthy',    description: 'Circuit closed — flag evaluating normally' },
  HALF_OPEN: { color: 'text-amber-400', label: 'Recovering', description: 'Circuit half-open — monitoring recovery' },
  OPEN:      { color: 'text-red-400',   label: 'Tripped',    description: 'Circuit open — flag auto-disabled (rollback executed)' },
};

export function CircuitBreakerStatus({ flagKey }: Props) {
  const [state, setState] = useState<CircuitState>('CLOSED');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    globalThis.fetch(`${EVAL_URL}/api/v1/circuit/${encodeURIComponent(flagKey)}`)
      .then(r => r.json())
      .then((data: { state?: string }) => {
        setState((data.state ?? 'CLOSED') as CircuitState);
      })
      .catch(() => setState('CLOSED'))
      .finally(() => setLoading(false));

    // Poll every 10 seconds (matches the aggregation window)
    const interval = setInterval(() => {
      globalThis.fetch(`${EVAL_URL}/api/v1/circuit/${encodeURIComponent(flagKey)}`)
        .then(r => r.json())
        .then((data: { state?: string }) => setState((data.state ?? 'CLOSED') as CircuitState))
        .catch(() => {/* keep last state */});
    }, 10_000);

    return () => clearInterval(interval);
  }, [flagKey]);

  if (loading) return <div className="text-gray-500 text-xs">Checking…</div>;

  const cfg = stateConfig[state];

  return (
    <div className="flex items-center gap-2">
      <div className={`w-2 h-2 rounded-full ${
        state === 'CLOSED'    ? 'bg-green-400' :
        state === 'HALF_OPEN' ? 'bg-amber-400' : 'bg-red-400'
      } ${state === 'OPEN' ? 'animate-pulse' : ''}`} />
      <div>
        <span className={`text-xs font-medium ${cfg.color}`}>{cfg.label}</span>
        <p className="text-xs text-gray-500">{cfg.description}</p>
      </div>
    </div>
  );
}
