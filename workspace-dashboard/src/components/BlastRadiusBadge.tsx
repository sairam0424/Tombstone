import { useState } from 'react';

interface BlastRadiusResult {
  risk_score: 'LOW' | 'MEDIUM' | 'HIGH' | 'BLOCKED';
  traffic_pct_affected: number;
  dependent_flags_count: number;
  affected_services: string[];
  historical_error_rate_delta: number;
  justification_required?: string;
}

interface Props {
  result: BlastRadiusResult;
  onConfirm: (justification?: string) => void;
  onCancel: () => void;
}

const riskConfig = {
  LOW:     { color: 'border-green-500 bg-green-900/20',   badge: 'bg-green-500 text-white',   label: 'LOW RISK' },
  MEDIUM:  { color: 'border-amber-500 bg-amber-900/20',   badge: 'bg-amber-500 text-white',   label: 'MEDIUM RISK' },
  HIGH:    { color: 'border-red-500 bg-red-900/20',       badge: 'bg-red-500 text-white',     label: 'HIGH RISK' },
  BLOCKED: { color: 'border-purple-500 bg-purple-900/20', badge: 'bg-purple-600 text-white',  label: 'BLOCKED' },
};

export function BlastRadiusBadge({ result, onConfirm, onCancel }: Props) {
  const [justification, setJustification] = useState('');
  const cfg = riskConfig[result.risk_score];
  const needsJustification = result.risk_score === 'BLOCKED';

  return (
    <div className={`rounded-lg border p-4 ${cfg.color}`}>
      <div className="flex items-center justify-between mb-3">
        <span className="font-semibold text-white text-sm">Blast Radius Analysis</span>
        <span className={`text-xs px-2 py-0.5 rounded font-bold ${cfg.badge}`}>
          {cfg.label}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-2 text-xs mb-3">
        <div className="bg-black/20 rounded p-2">
          <div className="text-gray-400">Traffic affected</div>
          <div className="text-white font-medium mt-0.5">{result.traffic_pct_affected.toFixed(0)}%</div>
        </div>
        <div className="bg-black/20 rounded p-2">
          <div className="text-gray-400">Dependent flags</div>
          <div className="text-white font-medium mt-0.5">{result.dependent_flags_count}</div>
        </div>
        <div className="bg-black/20 rounded p-2">
          <div className="text-gray-400">Affected services</div>
          <div className="text-white font-medium mt-0.5">{result.affected_services.length || '—'}</div>
        </div>
        <div className="bg-black/20 rounded p-2">
          <div className="text-gray-400">Hist. error delta</div>
          <div className={`font-medium mt-0.5 ${result.historical_error_rate_delta > 0.03 ? 'text-red-300' : 'text-white'}`}>
            {(result.historical_error_rate_delta * 100).toFixed(1)}%
          </div>
        </div>
      </div>

      {needsJustification && (
        <div className="mb-3">
          <p className="text-amber-300 text-xs mb-1">{result.justification_required}</p>
          <input
            type="text"
            placeholder="Type justification to proceed…"
            value={justification}
            onChange={(e) => setJustification(e.target.value)}
            className="w-full bg-black/30 border border-purple-500/50 rounded px-3 py-1.5 text-xs text-white placeholder-gray-500 focus:outline-none focus:border-purple-400"
          />
        </div>
      )}

      <div className="flex gap-2">
        <button
          onClick={() => onConfirm(needsJustification ? justification : undefined)}
          disabled={needsJustification && justification.trim().length < 10}
          className="flex-1 py-1.5 rounded text-xs font-medium bg-white/10 hover:bg-white/20 text-white disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          {result.risk_score === 'BLOCKED' ? 'Override & Proceed' : 'Proceed'}
        </button>
        <button
          onClick={onCancel}
          className="flex-1 py-1.5 rounded text-xs font-medium bg-gray-700 hover:bg-gray-600 text-gray-300 transition-colors"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}
