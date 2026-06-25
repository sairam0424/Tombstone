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

// ─── SEVERITY CONFIG ──────────────────────────────────────────────────────────
// Uses CSS custom properties + color-mix(in oklab, ...) as mandated by the spec.
// All sizes are in CSS px (not Tailwind size classes).
// bgColor: card background tint  (color-mix over transparent per global constraint)
// dotColor/borderColor/textColor: semantic token references
// dotSize: diameter in px per spec: critical=10, high=8, medium=6, info=4
const SEVERITY_CONFIG: Record<
  TimelineEntry['severity'],
  {
    dotSize: number;      // px diameter
    dotColor: string;     // CSS color expression
    dotGlow: string;      // CSS box-shadow expression ('' = none)
    textColor: string;    // CSS color expression
    bgColor: string;      // color-mix(in oklab, ...) background tint
    borderLeftColor: string;
    badgeLabel: string;
    badgeBg: string;
    badgeColor: string;
    badgeBorder: string;
    opacity: number;      // 0–1
  }
> = {
  critical: {
    dotSize: 10,
    dotColor: 'var(--color-risk-high)',
    dotGlow: '0 0 8px 2px color-mix(in oklab, var(--color-risk-high) 60%, transparent)',
    textColor: 'var(--color-risk-high)',
    bgColor: 'color-mix(in oklab, var(--color-risk-high) 12%, transparent)',
    borderLeftColor: 'var(--color-risk-high)',
    badgeLabel: 'CRITICAL',
    badgeBg: 'color-mix(in oklab, var(--color-risk-high) 18%, transparent)',
    badgeColor: 'var(--color-risk-high)',
    badgeBorder: 'color-mix(in oklab, var(--color-risk-high) 40%, transparent)',
    opacity: 1,
  },
  high: {
    dotSize: 8,
    dotColor: 'var(--color-action-warning)',
    dotGlow: '0 0 7px 1px color-mix(in oklab, var(--color-action-warning) 55%, transparent)',
    textColor: 'var(--color-action-warning)',
    bgColor: 'color-mix(in oklab, var(--color-action-warning) 10%, transparent)',
    borderLeftColor: 'var(--color-action-warning)',
    badgeLabel: 'HIGH',
    badgeBg: 'color-mix(in oklab, var(--color-action-warning) 18%, transparent)',
    badgeColor: 'var(--color-action-warning)',
    badgeBorder: 'color-mix(in oklab, var(--color-action-warning) 40%, transparent)',
    opacity: 1,
  },
  medium: {
    dotSize: 6,
    dotColor: 'var(--color-risk-medium)',
    dotGlow: '0 0 5px 1px color-mix(in oklab, var(--color-risk-medium) 45%, transparent)',
    textColor: 'var(--color-risk-medium)',
    bgColor: '',
    borderLeftColor: 'var(--color-risk-medium)',
    badgeLabel: 'MEDIUM',
    badgeBg: 'color-mix(in oklab, var(--color-risk-medium) 18%, transparent)',
    badgeColor: 'var(--color-risk-medium)',
    badgeBorder: 'color-mix(in oklab, var(--color-risk-medium) 40%, transparent)',
    opacity: 1,
  },
  low: {
    dotSize: 6,
    dotColor: 'var(--color-fg-subtle)',
    dotGlow: '',
    textColor: 'var(--color-fg-subtle)',
    bgColor: '',
    borderLeftColor: 'var(--color-border)',
    badgeLabel: 'LOW',
    badgeBg: 'color-mix(in oklab, var(--color-fg-subtle) 8%, transparent)',
    badgeColor: 'var(--color-fg-subtle)',
    badgeBorder: 'color-mix(in oklab, var(--color-fg-subtle) 15%, transparent)',
    opacity: 0.65,
  },
  info: {
    dotSize: 4,
    dotColor: 'var(--color-fg-subtle)',
    dotGlow: '',
    textColor: 'var(--color-fg-subtle)',
    bgColor: '',
    borderLeftColor: 'var(--color-border)',
    badgeLabel: 'INFO',
    badgeBg: 'color-mix(in oklab, var(--color-fg-subtle) 6%, transparent)',
    badgeColor: 'var(--color-fg-subtle)',
    badgeBorder: 'color-mix(in oklab, var(--color-fg-subtle) 12%, transparent)',
    opacity: 0.65,
  },
  warning: {
    dotSize: 6,
    dotColor: 'var(--color-risk-medium)',
    dotGlow: '0 0 5px 1px color-mix(in oklab, var(--color-risk-medium) 45%, transparent)',
    textColor: 'var(--color-risk-medium)',
    bgColor: '',
    borderLeftColor: 'var(--color-risk-medium)',
    badgeLabel: 'MEDIUM',
    badgeBg: 'color-mix(in oklab, var(--color-risk-medium) 18%, transparent)',
    badgeColor: 'var(--color-risk-medium)',
    badgeBorder: 'color-mix(in oklab, var(--color-risk-medium) 40%, transparent)',
    opacity: 1,
  },
};

// ─── PRIMARY event types ────────────────────────────────────────────────────
// Exactly as spec: only flag_change + incident.
const PRIMARY_TYPES = new Set<TimelineEntry['type']>([
  'flag_change',
  'incident',
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
      style={{ width: 48, height: 48, color: 'var(--color-fg-subtle)' }}
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
      style={{
        width: 16,
        height: 16,
        color: 'var(--color-fg-subtle)',
        transform: open ? 'rotate(180deg)' : 'rotate(0deg)',
        // Reduced-motion: transition is applied via CSS class below
      }}
      className="chevron-icon"
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
      style={{ width: 12, height: 12 }}
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
      style={{ width: 12, height: 12 }}
    >
      <path d="M3 8a5 5 0 1 0 1.6-3.7M3 4v4h4" />
    </svg>
  );
}

// ─── Quick-action button strip ───────────────────────────────────────────────
function QuickActions({ flagKey, severity }: { flagKey: string; severity: TimelineEntry['severity'] }) {
  const isCritical = severity === 'critical';

  const viewFlagStyle: React.CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 6,
    padding: '4px 10px',
    borderRadius: 6,
    fontSize: 12,
    fontWeight: 600,
    color: 'var(--color-accent)',
    background: 'color-mix(in oklab, var(--color-accent) 10%, transparent)',
    border: '1px solid color-mix(in oklab, var(--color-accent) 25%, transparent)',
    cursor: 'pointer',
    textDecoration: 'none',
  };

  const rollbackStyle: React.CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 6,
    padding: '4px 10px',
    borderRadius: 6,
    fontSize: 12,
    fontWeight: 600,
    cursor: 'pointer',
    background: isCritical
      ? 'color-mix(in oklab, var(--color-risk-high) 14%, transparent)'
      : 'color-mix(in oklab, var(--color-action-warning) 10%, transparent)',
    color: isCritical ? 'var(--color-risk-high)' : 'var(--color-action-warning)',
    border: isCritical
      ? '1px solid color-mix(in oklab, var(--color-risk-high) 35%, transparent)'
      : '1px solid color-mix(in oklab, var(--color-action-warning) 30%, transparent)',
  };

  return (
    <div
      style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 12 }}
      onClick={(e) => e.stopPropagation()}
    >
      {/* View Flag — navigates to /flags/${flagKey} */}
      <a
        href={`/flags/${flagKey}`}
        style={viewFlagStyle}
      >
        <ExternalLinkIcon />
        View Flag
      </a>

      {/* Rollback — navigates to /flags/${flagKey}?action=rollback */}
      <a
        href={`/flags/${flagKey}?action=rollback`}
        style={rollbackStyle}
      >
        <RollbackIcon />
        Rollback
      </a>
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
    <div style={{ minHeight: '100vh', background: 'var(--color-bg-base)', color: 'var(--color-fg)' }}>
      <div style={{ maxWidth: 896, margin: '0 auto', padding: '32px 24px' }}>

        {/* Page header */}
        <div style={{ marginBottom: 32 }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
            <div>
              <h1 style={{ fontSize: 24, fontWeight: 700, letterSpacing: '-0.025em', color: 'var(--color-fg)', margin: 0 }}>
                What Changed?
              </h1>
              <p style={{ marginTop: 4, fontSize: 14, color: 'var(--color-fg-muted)', letterSpacing: '0.025em' }}>
                Causal incident correlation
              </p>
            </div>

            {/* Controls */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              {/* Time range pills */}
              <div style={{
                display: 'flex',
                alignItems: 'center',
                background: 'var(--color-bg-surface)',
                border: '1px solid var(--color-border)',
                borderRadius: 10,
                padding: 4,
                gap: 4,
              }}>
                {(['1h', '6h', '24h', '7d'] as WindowOption[]).map((w) => (
                  <button
                    key={w}
                    onClick={() => setWindow(w)}
                    style={{
                      padding: '4px 12px',
                      borderRadius: 6,
                      fontSize: 12,
                      fontWeight: 600,
                      letterSpacing: '0.05em',
                      border: 'none',
                      cursor: 'pointer',
                      background: window === w ? 'var(--color-accent)' : 'transparent',
                      color: window === w ? 'var(--color-bg-base)' : 'var(--color-fg-muted)',
                    }}
                  >
                    {WINDOW_LABELS[w]}
                  </button>
                ))}
              </div>

              {/* Refresh button */}
              <button
                onClick={() => void fetchTimeline()}
                disabled={loading}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  padding: '6px 12px',
                  borderRadius: 8,
                  fontSize: 12,
                  fontWeight: 600,
                  color: 'var(--color-fg-muted)',
                  background: 'var(--color-bg-surface)',
                  border: '1px solid var(--color-border)',
                  cursor: loading ? 'not-allowed' : 'pointer',
                  opacity: loading ? 0.4 : 1,
                }}
              >
                <svg
                  xmlns="http://www.w3.org/2000/svg"
                  viewBox="0 0 20 20"
                  fill="currentColor"
                  style={{ width: 14, height: 14 }}
                  className={loading ? 'animate-spin' : ''}
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
          <div style={{ marginTop: 24, borderTop: '1px solid var(--color-border)' }} />
        </div>

        {/* Error banner */}
        {error && (
          <div style={{
            display: 'flex',
            alignItems: 'flex-start',
            gap: 12,
            background: 'color-mix(in oklab, var(--color-risk-high) 8%, transparent)',
            border: '1px solid color-mix(in oklab, var(--color-risk-high) 30%, transparent)',
            borderRadius: 12,
            padding: '12px 16px',
            marginBottom: 24,
          }}>
            <svg
              xmlns="http://www.w3.org/2000/svg"
              viewBox="0 0 20 20"
              fill="currentColor"
              style={{ width: 16, height: 16, color: 'var(--color-risk-high)', marginTop: 2, flexShrink: 0 }}
            >
              <path
                fillRule="evenodd"
                d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.28 7.22a.75.75 0 00-1.06 1.06L8.94 10l-1.72 1.72a.75.75 0 101.06 1.06L10 11.06l1.72 1.72a.75.75 0 101.06-1.06L11.06 10l1.72-1.72a.75.75 0 00-1.06-1.06L10 8.94 8.28 7.22z"
                clipRule="evenodd"
              />
            </svg>
            <span style={{ fontSize: 14, color: 'var(--color-risk-high)' }}>{error}</span>
          </div>
        )}

        {/* Loading skeleton */}
        {loading && entries.length === 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {[...Array(4)].map((_, i) => (
              <div key={i} style={{ display: 'flex', gap: 24 }} className="animate-pulse">
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', paddingTop: 4 }}>
                  <div style={{ width: 12, height: 12, borderRadius: '50%', background: 'var(--color-bg-elevated)' }} />
                  <div style={{ width: 1, flex: 1, background: 'var(--color-bg-elevated)', marginTop: 8 }} />
                </div>
                <div style={{ flex: 1, paddingBottom: 24 }}>
                  <div style={{ height: 80, background: 'var(--color-bg-surface)', borderRadius: 12, border: '1px solid var(--color-border)' }} />
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Empty state */}
        {!loading && entries.length === 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '96px 0', gap: 16 }}>
            <div style={{
              padding: 16,
              borderRadius: '50%',
              background: 'var(--color-bg-surface)',
              border: '1px solid var(--color-border)',
            }}>
              <LightningIcon />
            </div>
            <div style={{ textAlign: 'center' }}>
              <p style={{ color: 'var(--color-fg)', fontWeight: 500 }}>No recent incidents</p>
              <p style={{ color: 'var(--color-fg-subtle)', fontSize: 14, marginTop: 4 }}>
                No events detected in the last {WINDOW_LABELS[window]}. Production is quiet.
              </p>
            </div>
          </div>
        )}

        {/* Timeline */}
        {entries.length > 0 && (
          <div style={{ position: 'relative' }}>
            {/* Vertical rail */}
            <div style={{
              position: 'absolute',
              left: 5,
              top: 8,
              bottom: 8,
              width: 1,
              background: 'var(--color-border)',
            }} />

            <div style={{ display: 'flex', flexDirection: 'column', gap: 12, paddingLeft: 32 }}>
              {entries.map((entry, i) => {
                const cfg = SEVERITY_CONFIG[entry.severity];
                const primary = isPrimary(entry);
                const showActions = hasQuickActions(entry);
                const isOpen = expanded === i;
                const entryDate = fromUnixTime(entry.ts);

                // Dot offset: center the dot on the rail (rail is at left:5, half-width)
                const dotHalfSize = cfg.dotSize / 2;
                const dotLeft = 5 - dotHalfSize - 32; // relative to the pl-32 container

                return (
                  <div
                    key={`${entry.ts}-${entry.type}-${entry.flagKey ?? i}`}
                    style={{ position: 'relative', opacity: cfg.opacity }}
                    className="timeline-entry"
                  >
                    {/* Timeline dot — primary: larger + glow; secondary: small + grey */}
                    <div
                      style={{
                        position: 'absolute',
                        left: dotLeft,
                        top: 18,
                        width: cfg.dotSize,
                        height: cfg.dotSize,
                        borderRadius: '50%',
                        background: cfg.dotColor,
                        boxShadow: cfg.dotGlow || undefined,
                      }}
                    />

                    {/* Card */}
                    <div
                      onClick={() => setExpanded(isOpen ? null : i)}
                      style={{
                        cursor: 'pointer',
                        borderRadius: 12,
                        border: `1px solid var(--color-border)`,
                        borderLeft: `2px solid ${cfg.borderLeftColor}`,
                        background: primary && cfg.bgColor ? cfg.bgColor : 'color-mix(in oklab, var(--color-bg-surface) 50%, transparent)',
                      }}
                      className="timeline-card"
                    >
                      {/* Card header */}
                      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16, padding: '16px 20px' }}>
                        <div style={{ flex: 1, minWidth: 0 }}>
                          {/* Timestamp + flag key row */}
                          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 6, flexWrap: 'wrap' }}>
                            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--color-fg-subtle)', fontVariantNumeric: 'tabular-nums', flexShrink: 0 }}>
                              {format(entryDate, 'HH:mm:ss')}
                            </span>
                            <span style={{ color: 'var(--color-fg-subtle)', fontSize: 12 }}>·</span>
                            <span style={{ color: 'var(--color-fg-subtle)', fontSize: 12 }}>
                              {formatDistanceToNow(entryDate, { addSuffix: true })}
                            </span>
                            {entry.flagKey && (
                              <>
                                <span style={{ color: 'var(--color-fg-subtle)', fontSize: 12 }}>·</span>
                                <span style={{
                                  fontFamily: 'var(--font-mono)',
                                  fontSize: 12,
                                  color: 'var(--color-accent)',
                                  background: 'color-mix(in oklab, var(--color-accent) 10%, transparent)',
                                  padding: '2px 6px',
                                  borderRadius: 4,
                                  border: '1px solid color-mix(in oklab, var(--color-accent) 20%, transparent)',
                                }}>
                                  {entry.flagKey}
                                </span>
                              </>
                            )}
                          </div>

                          {/* Change description — bold + colored for primary, muted for secondary */}
                          <p style={{
                            fontSize: 14,
                            lineHeight: 1.4,
                            margin: 0,
                            fontWeight: primary ? 600 : 400,
                            color: primary ? cfg.textColor : 'var(--color-fg-subtle)',
                          }}>
                            {entry.title}
                          </p>

                          {/* Quick-action buttons — inline on critical/high flag_change events */}
                          {showActions && (
                            <QuickActions flagKey={entry.flagKey!} severity={entry.severity} />
                          )}
                        </div>

                        {/* Severity badge + expand chevron */}
                        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0, marginTop: 2 }}>
                          <span style={{
                            fontSize: 10,
                            fontWeight: 700,
                            letterSpacing: '0.1em',
                            padding: '2px 8px',
                            borderRadius: 6,
                            background: cfg.badgeBg,
                            color: cfg.badgeColor,
                            border: `1px solid ${cfg.badgeBorder}`,
                          }}>
                            {cfg.badgeLabel}
                          </span>
                          <ChevronIcon open={isOpen} />
                        </div>
                      </div>

                      {/* Expanded details */}
                      {isOpen && (
                        <div style={{ padding: '0 20px 16px', borderTop: '1px solid color-mix(in oklab, var(--color-border) 60%, transparent)' }}>
                          <p style={{ fontSize: 12, color: 'var(--color-fg-muted)', marginTop: 12, lineHeight: 1.6 }}>
                            {entry.description}
                          </p>
                          {entry.flagKey && (
                            <div style={{ marginTop: 12 }}>
                              <a
                                href={`/flags/${entry.flagKey}`}
                                style={{
                                  display: 'inline-flex',
                                  alignItems: 'center',
                                  gap: 4,
                                  fontSize: 12,
                                  color: 'var(--color-accent)',
                                  textDecoration: 'none',
                                }}
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

      {/* ── Reduced-motion guards for all CSS transitions in this view ── */}
      <style>{`
        @media (prefers-reduced-motion: reduce) {
          .timeline-card { transition: none !important; }
          .timeline-entry { transition: none !important; }
          .chevron-icon { transition: none !important; }
          .animate-spin { animation: none !important; }
          .animate-pulse { animation: none !important; }
        }
        @media (prefers-reduced-motion: no-preference) {
          .timeline-card {
            transition: border-color 150ms ease, background 150ms ease, filter 150ms ease;
          }
          .timeline-card:hover {
            border-color: var(--color-border-strong);
            filter: brightness(1.08);
          }
          .chevron-icon {
            transition: transform 200ms ease;
          }
        }
      `}</style>
    </div>
  );
}
