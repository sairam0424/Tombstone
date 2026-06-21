import { useState, useEffect, useCallback } from 'react';
import { formatDistanceToNow, fromUnixTime, format } from 'date-fns';

interface TimelineEntry {
  ts: number;
  type: 'flag_change' | 'metric_anomaly' | 'incident';
  title: string;
  description: string;
  actor?: string;
  severity: 'info' | 'warning' | 'critical';
  flagKey?: string;
  correlationScore?: number;
}

type WindowOption = '15m' | '30m' | '1h' | '6h';

const WINDOW_SECONDS: Record<WindowOption, number> = {
  '15m': 900, '30m': 1800, '1h': 3600, '6h': 21600,
};

const severityColor: Record<string, string> = {
  info: 'border-blue-500 bg-blue-500/10',
  warning: 'border-amber-500 bg-amber-500/10',
  critical: 'border-red-500 bg-red-500/10',
};

const typeIcon: Record<string, string> = {
  flag_change: '⚑',
  metric_anomaly: '⚡',
  incident: '🚨',
};

export default function IncidentTimeline() {
  const [window, setWindow] = useState<WindowOption>('30m');
  const [entries, setEntries] = useState<TimelineEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);

  const fetchTimeline = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const now = Math.floor(Date.now() / 1000);
      const from = now - WINDOW_SECONDS[window];
      const token = import.meta.env['VITE_SDK_TOKEN'] ?? 'sdk-dev-token-change-in-prod';
      const apiUrl = import.meta.env['VITE_API_URL'] ?? 'http://localhost:8081';

      const res = await globalThis.fetch(
        `${apiUrl}/api/v1/audit?from=${from}&to=${now}&limit=100`,
        { headers: { Authorization: `Bearer ${token}` } }
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json() as { entries: Array<Record<string, unknown>> };

      const timeline: TimelineEntry[] = (data.entries ?? []).map((e) => ({
        ts: e['created_at'] as number,
        type: (e['event_type'] as string).includes('kill') ? 'incident' :
              (e['event_type'] as string).includes('anomaly') ? 'metric_anomaly' : 'flag_change',
        title: `${e['event_type'] as string}: ${(e['flag_key'] as string) ?? 'unknown'}`,
        description: `Actor: ${(e['actor'] as string) ?? 'unknown'} | Env: ${(e['environment'] as string) ?? '-'}`,
        actor: e['actor'] as string,
        severity: (e['event_type'] as string).includes('kill') ? 'critical' :
                  (e['event_type'] as string).includes('updated') ? 'warning' : 'info',
        flagKey: e['flag_key'] as string | undefined,
      }));

      setEntries(timeline.sort((a, b) => b.ts - a.ts));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load timeline');
    } finally {
      setLoading(false);
    }
  }, [window]);

  useEffect(() => { void fetchTimeline(); }, [fetchTimeline]);

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-white">What Changed?</h1>
          <p className="text-gray-400 text-sm mt-1">Flag changes, anomalies, and incidents — correlated on one timeline</p>
        </div>
        <div className="flex gap-2">
          {(['15m', '30m', '1h', '6h'] as WindowOption[]).map((w) => (
            <button
              key={w}
              onClick={() => setWindow(w)}
              className={`px-3 py-1.5 rounded text-sm font-medium transition-colors ${
                window === w
                  ? 'bg-blue-600 text-white'
                  : 'bg-gray-800 text-gray-400 hover:bg-gray-700'
              }`}
            >
              {w}
            </button>
          ))}
          <button
            onClick={() => void fetchTimeline()}
            className="px-3 py-1.5 rounded text-sm font-medium bg-gray-800 text-gray-400 hover:bg-gray-700"
          >
            ↻ Refresh
          </button>
        </div>
      </div>

      {error && (
        <div className="bg-red-900/30 border border-red-500 rounded p-3 mb-4 text-red-300 text-sm">
          {error}
        </div>
      )}

      {loading && (
        <div className="text-gray-500 text-center py-12">Loading timeline…</div>
      )}

      {!loading && entries.length === 0 && (
        <div className="text-gray-500 text-center py-12">
          No events in the last {window}. Production is quiet ✓
        </div>
      )}

      <div className="relative border-l-2 border-gray-700 ml-4">
        {entries.map((entry, i) => (
          <div
            key={i}
            className="ml-6 mb-4 relative cursor-pointer"
            onClick={() => setExpanded(expanded === i ? null : i)}
          >
            {/* Timeline dot */}
            <div className={`absolute -left-9 w-4 h-4 rounded-full border-2 mt-1 ${
              entry.severity === 'critical' ? 'border-red-500 bg-red-900' :
              entry.severity === 'warning'  ? 'border-amber-500 bg-amber-900' :
                                              'border-blue-500 bg-blue-900'
            }`} />

            <div className={`p-3 rounded border ${severityColor[entry.severity]}`}>
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-2">
                  <span className="text-base">{typeIcon[entry.type]}</span>
                  <span className="text-sm font-medium text-white">{entry.title}</span>
                </div>
                <span className="text-xs text-gray-500 shrink-0 ml-4">
                  {format(fromUnixTime(entry.ts), 'HH:mm:ss')}
                  {' · '}
                  {formatDistanceToNow(fromUnixTime(entry.ts), { addSuffix: true })}
                </span>
              </div>

              {expanded === i && (
                <div className="mt-2 text-xs text-gray-400 border-t border-white/10 pt-2">
                  {entry.description}
                  {entry.flagKey && (
                    <div className="mt-1">
                      <a
                        href={`/flags/${entry.flagKey}`}
                        className="text-blue-400 hover:underline"
                        onClick={(e) => e.stopPropagation()}
                      >
                        View flag: {entry.flagKey} →
                      </a>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
