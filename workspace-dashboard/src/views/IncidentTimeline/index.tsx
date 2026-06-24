import { useState, useEffect, useCallback } from 'react';
import { formatDistanceToNow, fromUnixTime, format } from 'date-fns';
import { API_URL, SDK_TOKEN } from '../../config.js';

interface TimelineEntry {
  ts: number;
  type: 'flag_change' | 'metric_anomaly' | 'incident';
  title: string;
  description: string;
  actor?: string;
  severity: 'critical' | 'high' | 'medium' | 'low' | 'info' | 'warning';
  flagKey?: string;
  correlationScore?: number;
}

type WindowOption = '1h' | '6h' | '24h' | '7d';

const WINDOW_SECONDS: Record<WindowOption, number> = {
  '1h': 3600,
  '6h': 21600,
  '24h': 86400,
  '7d': 604800,
};

// Map legacy severity values to the new canonical set
function normalizeSeverity(raw: string): TimelineEntry['severity'] {
  if (raw === 'critical') return 'critical';
  if (raw === 'high') return 'high';
  if (raw === 'warning') return 'medium';  // legacy alias
  if (raw === 'medium') return 'medium';
  if (raw === 'low') return 'low';
  return 'low'; // info / unknown -> low (green)
}

const SEVERITY_BADGE: Record<TimelineEntry['severity'], { label: string; classes: string }> = {
  critical: { label: 'CRITICAL', classes: 'bg-red-500/20 text-red-400 border border-red-500/40' },
  high:     { label: 'HIGH',     classes: 'bg-orange-500/20 text-orange-400 border border-orange-500/40' },
  medium:   { label: 'MEDIUM',   classes: 'bg-amber-500/20 text-amber-400 border border-amber-500/40' },
  low:      { label: 'LOW',      classes: 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/40' },
  // legacy values kept so TypeScript is happy if they ever sneak through
  info:     { label: 'LOW',      classes: 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/40' },
  warning:  { label: 'MEDIUM',   classes: 'bg-amber-500/20 text-amber-400 border border-amber-500/40' },
};

const SEVERITY_DOT: Record<TimelineEntry['severity'], string> = {
  critical: 'bg-red-500 shadow-[0_0_6px_1px_rgba(239,68,68,0.6)]',
  high:     'bg-orange-500 shadow-[0_0_6px_1px_rgba(249,115,22,0.6)]',
  medium:   'bg-amber-500 shadow-[0_0_6px_1px_rgba(245,158,11,0.6)]',
  low:      'bg-emerald-500 shadow-[0_0_6px_1px_rgba(16,185,129,0.5)]',
  info:     'bg-emerald-500 shadow-[0_0_6px_1px_rgba(16,185,129,0.5)]',
  warning:  'bg-amber-500 shadow-[0_0_6px_1px_rgba(245,158,11,0.6)]',
};

const SEVERITY_CARD_LEFT: Record<TimelineEntry['severity'], string> = {
  critical: 'border-l-red-500',
  high:     'border-l-orange-500',
  medium:   'border-l-amber-500',
  low:      'border-l-emerald-500',
  info:     'border-l-emerald-500',
  warning:  'border-l-amber-500',
};

// Lightning bolt SVG icon for empty state
function LightningIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="w-12 h-12 text-gray-600"
    >
      <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
    </svg>
  );
}

export default function IncidentTimeline() {
  const [window, setWindow] = useState<WindowOption>('1h');
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
      const token = SDK_TOKEN;
      const apiUrl = API_URL;

      const res = await globalThis.fetch(
        `${apiUrl}/api/v1/audit?from=${from}&to=${now}&limit=100`,
        { headers: { Authorization: `Bearer ${token}` } }
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json() as { entries: Array<Record<string, unknown>> };

      const timeline: TimelineEntry[] = (data.entries ?? []).map((e) => {
        const eventType = e['event_type'] as string;
        const rawSeverity = eventType.includes('kill') ? 'critical' :
                            eventType.includes('anomaly') ? 'high' :
                            eventType.includes('updated') ? 'medium' : 'low';
        return {
          ts: e['created_at'] as number,
          type: eventType.includes('kill') ? 'incident' :
                eventType.includes('anomaly') ? 'metric_anomaly' : 'flag_change',
          title: `${eventType}: ${(e['flag_key'] as string) ?? 'unknown'}`,
          description: `Actor: ${(e['actor'] as string) ?? 'unknown'} | Env: ${(e['environment'] as string) ?? '-'}`,
          actor: e['actor'] as string,
          severity: normalizeSeverity(rawSeverity),
          flagKey: e['flag_key'] as string | undefined,
        };
      });

      setEntries(timeline.sort((a, b) => b.ts - a.ts));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load timeline');
    } finally {
      setLoading(false);
    }
  }, [window]);

  useEffect(() => { void fetchTimeline(); }, [fetchTimeline]);

  const WINDOW_LABELS: Record<WindowOption, string> = {
    '1h': '1h', '6h': '6h', '24h': '24h', '7d': '7d',
  };

  return (
    <div className="min-h-screen bg-gray-950 text-gray-100">
      <div className="max-w-4xl mx-auto px-6 py-8">

        {/* Page header */}
        <div className="mb-8">
          <div className="flex items-start justify-between gap-4 flex-wrap">
            <div>
              <h1 className="text-2xl font-bold tracking-tight text-white">
                What Changed?
              </h1>
              <p className="mt-1 text-sm text-gray-400 tracking-wide">
                Causal incident correlation
              </p>
            </div>

            {/* Controls */}
            <div className="flex items-center gap-2 flex-wrap">
              {/* Time range pills */}
              <div className="flex items-center bg-gray-900 border border-gray-800 rounded-lg p-1 gap-1">
                {(['1h', '6h', '24h', '7d'] as WindowOption[]).map((w) => (
                  <button
                    key={w}
                    onClick={() => setWindow(w)}
                    className={`px-3 py-1 rounded-md text-xs font-semibold tracking-wide transition-all duration-150 ${
                      window === w
                        ? 'bg-blue-600 text-white shadow-sm'
                        : 'text-gray-400 hover:text-gray-200 hover:bg-gray-800'
                    }`}
                  >
                    {WINDOW_LABELS[w]}
                  </button>
                ))}
              </div>

              {/* Refresh button */}
              <button
                onClick={() => void fetchTimeline()}
                disabled={loading}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold text-gray-400 bg-gray-900 border border-gray-800 hover:text-gray-200 hover:border-gray-700 transition-all duration-150 disabled:opacity-40"
              >
                <svg
                  xmlns="http://www.w3.org/2000/svg"
                  viewBox="0 0 20 20"
                  fill="currentColor"
                  className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`}
                >
                  <path
                    fillRule="evenodd"
                    d="M15.312 5.312a.75.75 0 011.06 0 8 8 0 11-11.31-.06.75.75 0 011.06 1.06 6.5 6.5 0 109.19.06zM10 3a.75.75 0 01.75.75v3.5a.75.75 0 01-1.5 0v-3.5A.75.75 0 0110 3z"
                    clipRule="evenodd"
                  />
                </svg>
                Refresh
              </button>
            </div>
          </div>

          {/* Divider */}
          <div className="mt-6 border-t border-gray-800" />
        </div>

        {/* Error banner */}
        {error && (
          <div className="flex items-start gap-3 bg-red-950/60 border border-red-800/60 rounded-xl px-4 py-3 mb-6">
            <svg
              xmlns="http://www.w3.org/2000/svg"
              viewBox="0 0 20 20"
              fill="currentColor"
              className="w-4 h-4 text-red-400 mt-0.5 shrink-0"
            >
              <path
                fillRule="evenodd"
                d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.28 7.22a.75.75 0 00-1.06 1.06L8.94 10l-1.72 1.72a.75.75 0 101.06 1.06L10 11.06l1.72 1.72a.75.75 0 101.06-1.06L11.06 10l1.72-1.72a.75.75 0 00-1.06-1.06L10 8.94 8.28 7.22z"
                clipRule="evenodd"
              />
            </svg>
            <span className="text-red-300 text-sm">{error}</span>
          </div>
        )}

        {/* Loading skeleton */}
        {loading && entries.length === 0 && (
          <div className="space-y-4">
            {[...Array(4)].map((_, i) => (
              <div key={i} className="flex gap-6 animate-pulse">
                <div className="flex flex-col items-center pt-1">
                  <div className="w-3 h-3 rounded-full bg-gray-800" />
                  <div className="w-px flex-1 bg-gray-800 mt-2" />
                </div>
                <div className="flex-1 pb-6">
                  <div className="h-20 bg-gray-900 rounded-xl border border-gray-800" />
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Empty state */}
        {!loading && entries.length === 0 && (
          <div className="flex flex-col items-center justify-center py-24 gap-4">
            <div className="p-4 rounded-full bg-gray-900 border border-gray-800">
              <LightningIcon />
            </div>
            <div className="text-center">
              <p className="text-gray-300 font-medium">No recent incidents</p>
              <p className="text-gray-600 text-sm mt-1">
                No events detected in the last {WINDOW_LABELS[window]}. Production is quiet.
              </p>
            </div>
          </div>
        )}

        {/* Timeline */}
        {entries.length > 0 && (
          <div className="relative">
            {/* Vertical rail */}
            <div className="absolute left-[5px] top-2 bottom-2 w-px bg-gray-800" />

            <div className="space-y-4 pl-8">
              {entries.map((entry, i) => {
                const badge = SEVERITY_BADGE[entry.severity];
                const dot = SEVERITY_DOT[entry.severity];
                const cardLeft = SEVERITY_CARD_LEFT[entry.severity];
                const isOpen = expanded === i;
                const entryDate = fromUnixTime(entry.ts);

                return (
                  <div key={i} className="relative">
                    {/* Timeline dot */}
                    <div
                      className={`absolute -left-[27px] top-[18px] w-[11px] h-[11px] rounded-full ${dot}`}
                    />

                    {/* Card */}
                    <div
                      onClick={() => setExpanded(isOpen ? null : i)}
                      className={`
                        group cursor-pointer rounded-xl
                        bg-gray-900 border border-gray-800
                        border-l-2 ${cardLeft}
                        hover:border-gray-700 hover:bg-gray-900/80
                        transition-all duration-150
                      `}
                    >
                      {/* Card header */}
                      <div className="flex items-start justify-between gap-4 px-5 py-4">
                        <div className="flex-1 min-w-0">
                          {/* Timestamp + flag key row */}
                          <div className="flex items-center gap-3 mb-1.5 flex-wrap">
                            <span className="font-mono text-xs text-[#6b7280] tabular-nums shrink-0">
                              {format(entryDate, 'HH:mm:ss')}
                            </span>
                            <span className="text-[#6b7280] text-xs">·</span>
                            <span className="text-gray-500 text-xs">
                              {formatDistanceToNow(entryDate, { addSuffix: true })}
                            </span>
                            {entry.flagKey && (
                              <>
                                <span className="text-[#6b7280] text-xs">·</span>
                                <span className="font-mono text-xs text-blue-400 bg-blue-950/40 px-1.5 py-0.5 rounded border border-blue-900/60">
                                  {entry.flagKey}
                                </span>
                              </>
                            )}
                          </div>

                          {/* Change description */}
                          <p className="text-sm text-gray-200 font-medium leading-snug">
                            {entry.title}
                          </p>
                        </div>

                        {/* Severity badge + expand chevron */}
                        <div className="flex items-center gap-3 shrink-0 mt-0.5">
                          <span className={`text-[10px] font-bold tracking-widest px-2 py-0.5 rounded-md ${badge.classes}`}>
                            {badge.label}
                          </span>
                          <svg
                            xmlns="http://www.w3.org/2000/svg"
                            viewBox="0 0 20 20"
                            fill="currentColor"
                            className={`w-4 h-4 text-gray-600 group-hover:text-gray-400 transition-transform duration-200 ${isOpen ? 'rotate-180' : ''}`}
                          >
                            <path
                              fillRule="evenodd"
                              d="M5.23 7.21a.75.75 0 011.06.02L10 11.168l3.71-3.938a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
                              clipRule="evenodd"
                            />
                          </svg>
                        </div>
                      </div>

                      {/* Expanded details */}
                      {isOpen && (
                        <div className="px-5 pb-4 border-t border-gray-800/60">
                          <p className="text-xs text-gray-400 mt-3 leading-relaxed">
                            {entry.description}
                          </p>
                          {entry.flagKey && (
                            <div className="mt-3">
                              <a
                                href={`/flags/${entry.flagKey}`}
                                className="inline-flex items-center gap-1 text-xs text-blue-400 hover:text-blue-300 hover:underline transition-colors"
                                onClick={(e) => e.stopPropagation()}
                              >
                                View flag: {entry.flagKey}
                                <svg
                                  xmlns="http://www.w3.org/2000/svg"
                                  viewBox="0 0 16 16"
                                  fill="currentColor"
                                  className="w-3 h-3"
                                >
                                  <path
                                    fillRule="evenodd"
                                    d="M6.22 4.22a.75.75 0 011.06 0l3.25 3.25a.75.75 0 010 1.06l-3.25 3.25a.75.75 0 01-1.06-1.06L8.94 8 6.22 5.28a.75.75 0 010-1.06z"
                                    clipRule="evenodd"
                                  />
                                </svg>
                              </a>
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
