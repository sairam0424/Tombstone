import { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ReferenceLine,
  ResponsiveContainer,
  RadialBarChart,
  RadialBar,
  PolarAngleAxis,
} from 'recharts';
import { useFlagSLO, type SLOWindow, type SLOHistoryPoint } from '../../hooks/useFlagSLO.js';
import { ENABLE_EVALUATOR } from '../../config.js';
import { EmptyState } from '../../components/ui/index.js';

// ── Circuit state badge ───────────────────────────────────────────────────────

const circuitColor: Record<string, { bg: string; text: string; dot: string }> = {
  CLOSED:    { bg: 'bg-green-900/40',  text: 'text-green-400',  dot: '#22c55e' },
  OPEN:      { bg: 'bg-red-900/40',    text: 'text-red-400',    dot: '#ef4444' },
  HALF_OPEN: { bg: 'bg-amber-900/40',  text: 'text-amber-400',  dot: '#f59e0b' },
};

function CircuitStateBadge({ state }: { state: string }) {
  const c = circuitColor[state] ?? circuitColor['CLOSED']!;
  return (
    <span className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-xs font-semibold ${c.bg} ${c.text}`}>
      <span className="w-1.5 h-1.5 rounded-full" style={{ background: c.dot }} />
      {state}
    </span>
  );
}

// ── Gauge (radial bar for SLO budget remaining) ───────────────────────────────

function SLOGauge({ value }: { value: number }) {
  const pct = Math.round(value * 100);
  const color = pct > 50 ? '#22c55e' : pct > 20 ? '#f59e0b' : '#ef4444';
  const gaugeData = [{ name: 'budget', value: pct, fill: color }];

  return (
    <div className="flex flex-col items-center">
      <div className="relative w-32 h-32">
        <RadialBarChart
          width={128}
          height={128}
          cx={64}
          cy={64}
          innerRadius={44}
          outerRadius={60}
          startAngle={225}
          endAngle={-45}
          data={gaugeData}
        >
          <PolarAngleAxis type="number" domain={[0, 100]} tick={false} />
          <RadialBar
            dataKey="value"
            cornerRadius={6}
            background={{ fill: '#1f2937' }}
          />
        </RadialBarChart>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="text-xl font-bold" style={{ color }}>{pct}%</span>
          <span className="text-xs text-gray-500">budget</span>
        </div>
      </div>
    </div>
  );
}

// ── Error rate sparkline with circuit trip markers ────────────────────────────

function ErrorRateChart({ history }: { history: SLOHistoryPoint[] }) {
  // Find hourly indices where circuit opened (transition CLOSED → OPEN)
  const tripIndices: number[] = [];
  for (let i = 1; i < history.length; i++) {
    const prev = history[i - 1];
    const curr = history[i];
    if (prev && curr && prev.circuit_state !== 'OPEN' && curr.circuit_state === 'OPEN') {
      tripIndices.push(i);
    }
  }

  const chartData = history.map((p, idx) => ({
    idx,
    ts: new Date(p.ts).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' }),
    error_rate: +(p.error_rate * 100).toFixed(3),
  }));

  // Show every N-th tick to avoid crowding
  const tickEvery = Math.max(1, Math.floor(history.length / 12));

  return (
    <ResponsiveContainer width="100%" height={220}>
      <LineChart data={chartData} margin={{ top: 8, right: 12, bottom: 8, left: 0 }}>
        <CartesianGrid strokeDasharray="3 3" stroke="#1f2937" />
        <XAxis
          dataKey="idx"
          tickFormatter={(v: number) => (v % tickEvery === 0 ? (chartData[v]?.ts ?? '') : '')}
          tick={{ fontSize: 10, fill: '#6b7280' }}
          axisLine={{ stroke: '#374151' }}
          tickLine={false}
        />
        <YAxis
          tickFormatter={(v: number) => `${v}%`}
          tick={{ fontSize: 10, fill: '#6b7280' }}
          axisLine={false}
          tickLine={false}
          width={36}
        />
        <Tooltip
          contentStyle={{ background: '#0d1117', border: '1px solid #21262d', borderRadius: 6, fontSize: 12 }}
          labelFormatter={(v: unknown) => chartData[v as number]?.ts ?? ''}
          formatter={(v: unknown) => [`${String(v)}%`, 'Error Rate'] as [string, string]}
        />
        {/* SLO threshold line at 5% */}
        <ReferenceLine y={5} stroke="#ef4444" strokeDasharray="4 2" label={{ value: 'SLO 5%', fill: '#ef4444', fontSize: 10, position: 'right' }} />
        {/* Circuit trip vertical markers */}
        {tripIndices.map(idx => (
          <ReferenceLine key={idx} x={idx} stroke="#f59e0b" strokeDasharray="3 3" label={{ value: '⚡', position: 'top', fontSize: 10 }} />
        ))}
        <Line
          type="monotone"
          dataKey="error_rate"
          stroke="#58a6ff"
          strokeWidth={1.5}
          dot={false}
          activeDot={{ r: 3, fill: '#58a6ff' }}
        />
      </LineChart>
    </ResponsiveContainer>
  );
}

// ── Main SLOView component ────────────────────────────────────────────────────

const WINDOWS: { label: string; value: SLOWindow }[] = [
  { label: '7 days',  value: '7d' },
  { label: '30 days', value: '30d' },
  { label: '90 days', value: '90d' },
];

export default function SLOView() {
  const { key } = useParams<{ key: string }>();
  const [selectedWindow, setSelectedWindow] = useState<SLOWindow>('7d');
  const isEvalAvailable = ENABLE_EVALUATOR;
  const { data, loading, error } = useFlagSLO(key ?? '', selectedWindow);

  if (!isEvalAvailable) {
    return (
      <div style={{ padding: '32px 40px' }}>
        <EmptyState
          heading="Evaluator service offline"
          body="Enable the 'feature-evaluator-service' flag in Tombstone when the evaluator is deployed."
        />
      </div>
    );
  }

  if (!key) return <div className="p-8 text-red-400">Missing flag key in URL.</div>;

  return (
    <div className="p-6 max-w-5xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <Link to={`/flags/${key}`} className="text-gray-500 hover:text-white text-sm">
          ← {key}
        </Link>
        <div className="flex items-center justify-between mt-2">
          <div>
            <h1 className="text-xl font-bold text-white font-mono">{key}</h1>
            <p className="text-gray-400 text-sm mt-0.5">SLO Dashboard</p>
          </div>
          {data && <CircuitStateBadge state={data.circuit_state} />}
        </div>
      </div>

      {/* Window selector */}
      <div className="flex gap-2 mb-6">
        {WINDOWS.map(w => (
          <button
            key={w.value}
            onClick={() => setSelectedWindow(w.value)}
            className={`px-3 py-1 rounded text-xs font-medium border transition-colors ${
              selectedWindow === w.value
                ? 'border-blue-500 text-blue-400 bg-blue-900/20'
                : 'border-gray-700 text-gray-500 hover:border-gray-500'
            }`}
          >
            {w.label}
          </button>
        ))}
      </div>

      {loading && <div className="text-gray-500 py-8 text-center">Loading SLO data…</div>}
      {error && <div className="text-red-400 py-8 text-center">{error}</div>}

      {data && (
        <>
          {/* Summary stats row */}
          <div className="grid grid-cols-2 gap-4 mb-6 sm:grid-cols-4">
            <StatCard label="Error Rate" value={`${(data.error_rate * 100).toFixed(3)}%`} warn={data.error_rate >= 0.05} />
            <StatCard label="Evaluations" value={data.evaluation_count.toLocaleString()} />
            <StatCard label="p99 Latency" value={data.p99_latency_ms > 0 ? `${data.p99_latency_ms} ms` : '—'} />
            <StatCard label="Circuit Trips" value={String(data.circuit_trips)} warn={data.circuit_trips > 0} />
          </div>

          {/* Budget gauge + sparkline */}
          <div className="grid grid-cols-1 gap-4 mb-6 lg:grid-cols-4">
            {/* Gauge */}
            <div className="bg-gray-900 rounded-lg border border-gray-700 p-5 flex flex-col items-center justify-center lg:col-span-1">
              <p className="text-xs text-gray-500 mb-2 uppercase tracking-wide">SLO Budget</p>
              <SLOGauge value={data.slo_budget_remaining} />
              <p className="text-xs text-gray-500 mt-2">
                {data.slo_budget_remaining < 0.2 ? (
                  <span className="text-red-400 font-semibold">Critical — budget nearly exhausted</span>
                ) : data.slo_budget_remaining < 0.5 ? (
                  <span className="text-amber-400">Degraded — review error rate</span>
                ) : (
                  <span className="text-green-400">Healthy</span>
                )}
              </p>
            </div>

            {/* Error rate sparkline */}
            <div className="bg-gray-900 rounded-lg border border-gray-700 p-5 lg:col-span-3">
              <p className="text-xs text-gray-500 mb-3 uppercase tracking-wide">
                Error Rate Over Time
                {data.circuit_trips > 0 && (
                  <span className="ml-2 text-amber-400">({data.circuit_trips} circuit trip{data.circuit_trips !== 1 ? 's' : ''} — ⚡ markers)</span>
                )}
              </p>
              <ErrorRateChart history={data.history} />
            </div>
          </div>

          {/* Circuit trip history table */}
          {data.circuit_trips > 0 && (
            <div className="bg-gray-900 rounded-lg border border-gray-700">
              <div className="px-5 py-3 border-b border-gray-700 text-sm font-medium text-white">
                Circuit Trip Timeline
                <span className="text-gray-500 font-normal ml-2">({selectedWindow} window)</span>
              </div>
              <div className="divide-y divide-gray-800 text-xs max-h-64 overflow-y-auto">
                {data.history
                  .filter((p, i) => i > 0 && data.history[i - 1]?.circuit_state !== 'OPEN' && p.circuit_state === 'OPEN')
                  .map(p => (
                    <div key={p.ts} className="px-5 py-3 flex items-center gap-4">
                      <span className="text-amber-400">⚡</span>
                      <span className="text-gray-300">{new Date(p.ts).toLocaleString()}</span>
                      <span className="text-gray-500">Error rate at trip:</span>
                      <span className={`font-mono ${p.error_rate >= 0.05 ? 'text-red-400' : 'text-amber-300'}`}>
                        {(p.error_rate * 100).toFixed(3)}%
                      </span>
                      <CircuitStateBadge state={p.circuit_state} />
                    </div>
                  ))}
                {data.history.filter((p, i) => i > 0 && data.history[i - 1]?.circuit_state !== 'OPEN' && p.circuit_state === 'OPEN').length === 0 && (
                  <div className="px-5 py-4 text-gray-500 text-center">No trip transitions in history window.</div>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ── Shared mini stat card ─────────────────────────────────────────────────────

function StatCard({ label, value, warn = false }: { label: string; value: string; warn?: boolean }) {
  return (
    <div className="bg-gray-900 rounded-lg border border-gray-700 p-4">
      <div className="text-gray-400 text-xs mb-1">{label}</div>
      <div className={`text-lg font-bold font-mono ${warn ? 'text-red-400' : 'text-white'}`}>{value}</div>
    </div>
  );
}
