import { useState, useEffect, useCallback } from 'react';
import { formatDistanceToNow, fromUnixTime, format } from 'date-fns';
import { API_URL, SDK_TOKEN } from '../../config.js';

interface TimelineEntry {
  ts: number;
  type: 'flag_change' | 'metric_anomaly' | 'incident' | 'notification_delivery';
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
  if (raw === 'info') return 'info';
  return 'low'; // unknown -> low (green)
}

// ─── SEVERITY CONFIG (OKLCH token-aligned) ───────────────────────────────────
// dot: size class + glow;  text: semantic color class; bg: card background tint;
// badge: inline severity label; borderLeft: left accent stripe
const SEVERITY_CONFIG: Record<
  TimelineEntry['severity'],
  {
    dotSize: string;
    dotColor: string;
    dotGlow: string;
    text: string;
    bg: string;
    borderLeft: string;
    badgeLabel: string;
    badgeClasses: string;
    opacity: string;
  }
> = {
  critical: {
    dotSize: 'w-3.5 h-3.5',
    dotColor: 'bg-[oklch(65%_0.23_25)]',
    dotGlow: 'shadow-[0_0_8px_2px_oklch(65%_0.23_25_/_60%)]',
    text: 'text-[oklch(75%_0.18_25)]',
    bg: 'bg-[oklch(65%_0.23_25_/_6%)]',
    borderLeft: 'border-l-[oklch(65%_0.23_25)]',
    badgeLabel: 'CRITICAL',
    badgeClasses: 'bg-[oklch(65%_0.23_25_/_18%)] text-[oklch(75%_0.18_25)] border border-[oklch(65%_0.23_25_/_40%)]',
    opacity: 'opacity-100',
  },
  high: {
    dotSize: 'w-3 h-3',
    dotColor: 'bg-[oklch(70%_0.2_45)]',
    dotGlow: 'shadow-[0_0_7px_1px_oklch(70%_0.2_45_/_55%)]',
    text: 'text-[oklch(80%_0.16_50)]',
    bg: 'bg-[oklch(70%_0.2_45_/_5%)]',
    borderLeft: 'border-l-[oklch(70%_0.2_45)]',
    badgeLabel: 'HIGH',
    badgeClasses: 'bg-[oklch(70%_0.2_45_/_18%)] text-[oklch(80%_0.16_50)] border border-[oklch(70%_0.2_45_/_40%)]',
    opacity: 'opacity-100',
  },
  medium: {
    dotSize: 'w-2.5 h-2.5',
    dotColor: 'bg-[oklch(78%_0.17_85)]',
    dotGlow: 'shadow-[0_0_5px_1px_oklch(78%_0.17_85_/_45%)]',
    text: 'text-[oklch(78%_0.17_85)]',
    bg: '',
    borderLeft: 'border-l-[oklch(78%_0.17_85)]',
    badgeLabel: 'MEDIUM',
    badgeClasses: 'bg-amber-500/20 text-amber-400 border border-amber-500/40',
    opacity: 'opacity-100',
  },
  low: {
    dotSize: 'w-2 h-2',
    dotColor: 'bg-gray-500',
    dotGlow: '',
    text: 'text-gray-500',
    bg: '',
    borderLeft: 'border-l-gray-700',
    badgeLabel: 'LOW',
    badgeClasses: 'bg-gray-800 text-gray-500 border border-gray-700',
    opacity: 'opacity-65',
  },
  info: {
    dotSize: 'w-2 h-2',
    dotColor: 'bg-gray-600',
    dotGlow: '',
    text: 'text-gray-500',
    bg: '',
    borderLeft: 'border-l-gray-700',
    badgeLabel: 'INFO',
    badgeClasses: 'bg-gray-800 text-gray-600 border border-gray-700',
    opacity: 'opacity-65',
  },
  warning: {
    dotSize: 'w-2.5 h-2.5',
    dotColor: 'bg-[oklch(78%_0.17_85)]',
    dotGlow: 'shadow-[0_0_5px_1px_oklch(78%_0.17_85_/_45%)]',
    text: 'text-[oklch(78%_0.17_85)]',
    bg: '',
    borderLeft: 'border-l-[oklch(78%_0.17_85)]',
    badgeLabel: 'MEDIUM',
    badgeClasses: 'bg-amber-500/20 text-amber-400 border border-amber-500/40',
    opacity: 'opacity-100',
  },
};

// ─── PRIMARY event types ────────────────────────────────────────────────────
// These are state-changing / high-impact events that deserve full visual weight.
const PRIMARY_TYPES = new Set<TimelineEntry['type']>([
  'flag_change',
  'incident',
  'metric_anomaly',
]);

/** Determine if an entry should render at PRIMARY (full weight) or SECONDARY (dimmed). */
function isPrimary(entry: TimelineEntry): boolean {
  if (PRIMARY_TYPES.has(entry.type)) return true;
  if (entry.severity === 'critical' || entry.severity === 'high') return true;
  return false;
}

/** Determine if inline quick-actions should appear (View Flag + Rollback). */
function hasQuickActions(entry: TimelineEntry): boolean {
  return (
    entry.type === 'flag_change' &&
    (entry.severity === 'critical' || entry.severity === 'high') &&
    Boolean(entry.flagKey)
  );
}

// ─── Icons ───────────────────────────────────────────────────────────────────
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

function ChevronIcon({ open }: { open: boolean }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 20 20"
      fill="currentColor"
      className={`w-4 h-4 text-gray-600 group-hover:text-gray-400 transition-transform duration-200 ${open ? 'rotate-180' : ''}`}
    >
      <path
        fillRule="evenodd"
        d="M5.23 7.21a.75.75 0 011.06.02L10 11.168l3.71-3.938a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
        clipRule="evenodd"
      />
    </svg>
  );
}

function ExternalLinkIcon() {
  return (
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
  );
}

function RollbackIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="w-3 h-3"
    >
      <path d="M3 8a5 5 0 1 0 1.6-3.7M3 4v4h4" />
    </svg>
  );
}

// ─── Quick-action button strip ───────────────────────────────────────────────
function QuickActions({ flagKey, severity }: { flagKey: string; severity: TimelineEntry['severity'] }) {
  const isCritical = severity === 'critical';
  return (
    <div className="flex items-center gap-2 mt-3" onClick={(e) => e.stopPropagation()}>
      {/* View Flag */}
      <a
        href={`/flags/${flagKey}`}
        className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-semibold
          text-[oklch(82%_0.18_200)] bg-[oklch(82%_0.18_200_/_10%)] border border-[oklch(82%_0.18_200_/_25%)]
          hover:bg-[oklch(82%_0.18_200_/_18%)] hover:border-[oklch(82%_0.18_200_/_40%)]
          transition-all duration-150"
      >
        <ExternalLinkIcon />
        View Flag
      </a>

      {/* Rollback — more prominent for critical */}
      <button
        type="button"
        onClick={() => {
          // Rollback action — placeholder; wired to API in a future task
          console.warn('[Tombstone] Rollback requested for flag:', flagKey);
        }}
        className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-semibold
          transition-all duration-150
          ${isCritical
            ? 'text-[oklch(75%_0.18_25)] bg-[oklch(65%_0.23_25_/_14%)] border border-[oklch(65%_0.23_25_/_35%)] hover:bg-[oklch(65%_0.23_25_/_22%)] hover:border-[oklch(65%_0.23_25_/_55%)]'
            : 'text-[oklch(80%_0.16_50)] bg-[oklch(70%_0.2_45_/_10%)] border border-[oklch(70%_0.2_45_/_30%)] hover:bg-[oklch(70%_0.2_45_/_18%)] hover:border-[oklch(70%_0.2_45_/_50%)]'
          }`}
      >
        <RollbackIcon />
        Rollback
      </button>
    </div>
  );
}

// ─── Main component ───────────────────────────────────────────────────────────
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
                eventType.includes('anomaly') ? 'metric_anomaly' :
                eventType.includes('notification') ? 'notification_delivery' : 'flag_change',
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

            <div className="space-y-3 pl-8">
              {entries.map((entry, i) => {
                const cfg = SEVERITY_CONFIG[entry.severity];
                const primary = isPrimary(entry);
                const showActions = hasQuickActions(entry);
                const isOpen = expanded === i;
                const entryDate = fromUnixTime(entry.ts);

                // Dot offset: larger dots need to be nudged left to stay centered on the rail
                const dotOffset = primary ? '-left-[29px]' : '-left-[27px]';

                return (
                  <div key={i} className={`relative transition-opacity duration-150 ${cfg.opacity}`}>
                    {/* Timeline dot — primary: larger + glow; secondary: small + grey */}
                    <div
                      className={`
                        absolute ${dotOffset} top-[18px] rounded-full
                        ${cfg.dotSize} ${cfg.dotColor} ${cfg.dotGlow}
                      `}
                    />

                    {/* Card */}
                    <div
                      onClick={() => setExpanded(isOpen ? null : i)}
                      className={`
                        group cursor-pointer rounded-xl
                        border border-gray-800 border-l-2 ${cfg.borderLeft}
                        hover:border-gray-700
                        transition-all duration-150
                        ${primary
                          ? `${cfg.bg} hover:brightness-110`
                          : 'bg-gray-900/50 hover:bg-gray-900/70'
                        }
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

                          {/* Change description — bold + colored for primary, muted for secondary */}
                          <p
                            className={`text-sm leading-snug ${
                              primary
                                ? `font-semibold ${cfg.text}`
                                : 'font-normal text-gray-500'
                            }`}
                          >
                            {entry.title}
                          </p>

                          {/* Quick-action buttons — inline on critical/high flag_change events */}
                          {showActions && (
                            <QuickActions flagKey={entry.flagKey!} severity={entry.severity} />
                          )}
                        </div>

                        {/* Severity badge + expand chevron */}
                        <div className="flex items-center gap-3 shrink-0 mt-0.5">
                          <span className={`text-[10px] font-bold tracking-widest px-2 py-0.5 rounded-md ${cfg.badgeClasses}`}>
                            {cfg.badgeLabel}
                          </span>
                          <ChevronIcon open={isOpen} />
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
                                <ExternalLinkIcon />
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
