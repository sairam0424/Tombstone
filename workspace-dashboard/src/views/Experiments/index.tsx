import { useState } from 'react';

interface ExperimentResult {
  experiment_id: string;
  flag_key: string;
  recommendation: 'SHIP' | 'NO_SHIP' | 'CONTINUE';
  relative_lift: number;
  is_significant: boolean;
  sample_sizes: { control: number; treatment: number };
  probability_beats_control?: number;
  p_value?: number;
}

const recConfig: Record<
  ExperimentResult['recommendation'],
  { color: string; label: string }
> = {
  SHIP:     { color: 'text-green-400 bg-green-900/30 border-green-700',   label: 'SHIP IT' },
  NO_SHIP:  { color: 'text-red-400 bg-red-900/30 border-red-700',         label: 'DO NOT SHIP' },
  CONTINUE: { color: 'text-amber-400 bg-amber-900/30 border-amber-700',   label: 'CONTINUE' },
};

interface FormState {
  flagKey: string;
  metricName: string;
  metricSql: string;
  eventTable: string;
  flagEventTable: string;
  warehouseType: string;
  warehouseDsn: string;
}

export default function Experiments() {
  const [form, setForm] = useState<FormState>({
    flagKey: '',
    metricName: 'conversion',
    metricSql: 'CASE WHEN converted THEN 1 ELSE 0 END',
    eventTable: 'user_events',
    flagEventTable: 'flag_evaluations',
    warehouseType: 'postgresql',
    warehouseDsn: '',
  });
  const [result, setResult] = useState<ExperimentResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const intelUrl =
    (import.meta as { env: Record<string, string> }).env['VITE_INTEL_URL'] ?? 'http://localhost:8083';

  const run = async () => {
    setRunning(true);
    setError(null);
    setResult(null);
    try {
      const res = await fetch(`${intelUrl}/api/v1/experiments/analyze`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          experiment_id: crypto.randomUUID(),
          flag_key: form.flagKey,
          metric_name: form.metricName,
          metric_sql: form.metricSql,
          event_table: form.eventTable,
          flag_event_table: form.flagEventTable,
          warehouse_type: form.warehouseType,
          warehouse_dsn: form.warehouseDsn,
          stat_method: 'bayesian',
          min_sample_size: 100,
        }),
      });
      if (!res.ok) {
        const err = await res.json() as { detail?: string };
        throw new Error(err.detail ?? `HTTP ${res.status}`);
      }
      setResult(await res.json() as ExperimentResult);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Analysis failed');
    } finally {
      setRunning(false);
    }
  };

  const textFields: { label: string; key: keyof FormState; placeholder: string }[] = [
    { label: 'Flag Key',          key: 'flagKey',         placeholder: 'payments.checkout.checkout-v2' },
    { label: 'Metric Name',       key: 'metricName',      placeholder: 'conversion' },
    { label: 'Event Table',       key: 'eventTable',      placeholder: 'user_events' },
    { label: 'Flag Event Table',  key: 'flagEventTable',  placeholder: 'flag_evaluations' },
  ];

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Experiments</h1>
        <p className="text-gray-400 text-sm mt-1">
          Warehouse-native analysis — your data never leaves your infrastructure
        </p>
      </div>

      <div className="bg-gray-900 rounded-lg border border-gray-700 p-5 mb-6">
        <h2 className="text-sm font-semibold text-white mb-4">Configure Analysis</h2>
        <div className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            {textFields.map(({ label, key, placeholder }) => (
              <div key={key}>
                <label className="text-xs text-gray-400 block mb-1">{label}</label>
                <input
                  type="text"
                  placeholder={placeholder}
                  value={form[key]}
                  onChange={e => setForm(f => ({ ...f, [key]: e.target.value }))}
                  className="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-white placeholder-gray-500"
                />
              </div>
            ))}
          </div>
          <div>
            <label className="text-xs text-gray-400 block mb-1">Metric SQL Expression</label>
            <input
              type="text"
              value={form.metricSql}
              onChange={e => setForm(f => ({ ...f, metricSql: e.target.value }))}
              className="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-white font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-gray-400 block mb-1">Warehouse Connection String</label>
            <input
              type="password"
              placeholder="Set VITE_WAREHOUSE_DSN in .env"
              value={form.warehouseDsn}
              onChange={e => setForm(f => ({ ...f, warehouseDsn: e.target.value }))}
              className="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-white"
            />
          </div>
        </div>
        <button
          onClick={() => void run()}
          disabled={running || !form.flagKey || !form.warehouseDsn}
          className="mt-4 px-5 py-2 rounded text-sm font-medium bg-blue-600 hover:bg-blue-500 text-white disabled:opacity-40 transition-colors"
        >
          {running ? 'Running analysis…' : 'Run Analysis'}
        </button>
      </div>

      {error && (
        <div className="bg-red-900/30 border border-red-700 rounded p-3 text-red-300 text-sm mb-4">
          {error}
        </div>
      )}

      {result && (() => {
        const cfg = recConfig[result.recommendation];
        return (
          <div className="bg-gray-900 rounded-lg border border-gray-700 p-5">
            <div className={`inline-flex items-center px-3 py-1.5 rounded border text-sm font-bold mb-4 ${cfg.color}`}>
              {cfg.label}
            </div>
            <div className="grid grid-cols-2 gap-4 text-sm">
              <div className="bg-black/20 rounded p-3">
                <div className="text-gray-400 text-xs mb-1">Relative Lift</div>
                <div className={`text-xl font-bold ${result.relative_lift >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                  {(result.relative_lift * 100).toFixed(2)}%
                </div>
              </div>
              <div className="bg-black/20 rounded p-3">
                <div className="text-gray-400 text-xs mb-1">Sample Sizes</div>
                <div className="text-white">
                  Control: {result.sample_sizes.control.toLocaleString()}
                  <br />
                  Treatment: {result.sample_sizes.treatment.toLocaleString()}
                </div>
              </div>
              {result.probability_beats_control !== undefined && (
                <div className="bg-black/20 rounded p-3">
                  <div className="text-gray-400 text-xs mb-1">P(Treatment &gt; Control)</div>
                  <div className="text-white text-xl font-bold">
                    {(result.probability_beats_control * 100).toFixed(1)}%
                  </div>
                </div>
              )}
              <div className="bg-black/20 rounded p-3">
                <div className="text-gray-400 text-xs mb-1">Statistically Significant</div>
                <div className={`text-xl font-bold ${result.is_significant ? 'text-green-400' : 'text-gray-500'}`}>
                  {result.is_significant ? 'Yes' : 'No'}
                </div>
              </div>
            </div>
          </div>
        );
      })()}
    </div>
  );
}
