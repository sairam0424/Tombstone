import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { API_URL, INTEL_URL, SDK_TOKEN } from '../../config.js';
import { EvaluationChart, type TimeSeriesPoint } from '../../components/charts/EvaluationChart.js';
import { Section, Reveal, SkeletonStatCard, EmptyState } from '../../components/ui/index.js';

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

interface ActivityEntry {
  flag_key: string;
  event_type: string;
  actor: string;
  created_at: number;
}

// ─── design tokens (mirrors FlagList / dashboard palette) ───────────────────

const T = {
  bg0:        '#010409',
  bg1:        '#0d1117',
  bg2:        '#161b22',
  border:     '#21262d',
  borderSub:  '#161b22',
  textPrimary: '#e6edf3',
  textMuted:  '#8b949e',
  textDim:    '#484f58',
  blue:       '#58a6ff',
  green:      '#3fb950',
  amber:      '#e3b341',
  red:        '#f85149',
  greenBg:    'rgba(63,185,80,0.10)',
  amberBg:    'rgba(227,179,65,0.10)',
  redBg:      'rgba(248,81,73,0.10)',
  blueBg:     'rgba(88,166,255,0.10)',
};

// ─── action badge styles ─────────────────────────────────────────────────────

const ACTION_STYLE: Record<string, { color: string; bg: string; border: string }> = {
  ARCHIVE:      { color: T.red,   bg: T.redBg,   border: 'rgba(248,81,73,0.25)' },
  NOTIFY_OWNER: { color: T.amber, bg: T.amberBg, border: 'rgba(227,179,65,0.25)' },
  REVIEW:       { color: T.blue,  bg: T.blueBg,  border: 'rgba(88,166,255,0.25)' },
};

// ─── helpers ─────────────────────────────────────────────────────────────────

function timeAgo(unixTs: number): string {
  const secs = Math.floor(Date.now() / 1000) - unixTs;
  if (secs < 60)  return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

// ─── sub-components ──────────────────────────────────────────────────────────

function SectionCard({ title, subtitle, children }: {
  title: string;
  subtitle?: string;
  children: React.ReactNode;
}) {
  return (
    <div style={{
      background: T.bg1,
      border: `1px solid ${T.border}`,
      borderRadius: 10,
      overflow: 'hidden',
    }}>
      <div style={{
        padding: '14px 20px',
        borderBottom: `1px solid ${T.border}`,
        display: 'flex',
        alignItems: 'baseline',
        gap: 10,
      }}>
        <h2 style={{ margin: 0, fontSize: 14, fontWeight: 600, color: T.textPrimary }}>{title}</h2>
        {subtitle && (
          <span style={{ fontSize: 12, color: T.textDim }}>{subtitle}</span>
        )}
      </div>
      {children}
    </div>
  );
}

function EmptyRow({ message }: { message: string }) {
  return (
    <div style={{
      padding: '48px 20px',
      textAlign: 'center',
      color: T.textDim,
      fontSize: 13,
    }}>
      {message}
    </div>
  );
}

function TH({ children }: { children?: React.ReactNode }) {
  return (
    <th style={{
      textAlign: 'left',
      padding: '9px 16px',
      fontSize: 11,
      fontWeight: 600,
      textTransform: 'uppercase',
      letterSpacing: '0.07em',
      color: T.textDim,
      whiteSpace: 'nowrap',
    }}>
      {children}
    </th>
  );
}

// ─── Circular-style Health Indicator ─────────────────────────────────────────

function HealthRing({ pct, color }: { pct: number; color: string }) {
  const r = 28;
  const circ = 2 * Math.PI * r;
  const dash = (pct / 100) * circ;

  return (
    <svg width={72} height={72} style={{ display: 'block', flexShrink: 0 }}>
      {/* track */}
      <circle cx={36} cy={36} r={r} fill="none" stroke={T.bg2} strokeWidth={6} />
      {/* progress — starts at 12 o'clock */}
      <circle
        cx={36} cy={36} r={r}
        fill="none"
        stroke={color}
        strokeWidth={6}
        strokeLinecap="round"
        strokeDasharray={`${dash} ${circ - dash}`}
        strokeDashoffset={circ * 0.25}
        style={{ transition: 'stroke-dasharray 0.6s ease' }}
      />
      <text
        x={36} y={36}
        dominantBaseline="middle"
        textAnchor="middle"
        fontSize={13}
        fontWeight={700}
        fill={color}
        fontFamily="'JetBrains Mono','Fira Code',monospace"
      >
        {pct}
      </text>
    </svg>
  );
}

// ─── Stat Card ───────────────────────────────────────────────────────────────

function StatCard({
  label, value, color, sub, ring,
}: {
  label: string;
  value: string | number;
  color: string;
  sub?: string;
  ring?: { pct: number; color: string };
}) {
  return (
    <div style={{
      background: T.bg1,
      border: `1px solid ${T.border}`,
      borderRadius: 10,
      padding: '18px 20px',
      display: 'flex',
      alignItems: 'center',
      gap: 16,
      flex: '1 1 0',
      minWidth: 0,
    }}>
      {ring && <HealthRing pct={ring.pct} color={ring.color} />}
      <div style={{ minWidth: 0 }}>
        <div style={{ fontSize: 11, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.07em', color: T.textDim, marginBottom: 6 }}>
          {label}
        </div>
        <div style={{ fontSize: ring ? 36 : 32, fontWeight: 700, color, lineHeight: 1, fontVariantNumeric: 'tabular-nums' }}>
          {value}
        </div>
        {sub && (
          <div style={{ fontSize: 12, color: T.textMuted, marginTop: 5 }}>{sub}</div>
        )}
      </div>
    </div>
  );
}

// ─── Stale Flags Table ────────────────────────────────────────────────────────

function StaleFlagsTable({ stale, hovered, setHovered }: {
  stale: StaleFlag[];
  hovered: string | null;
  setHovered: (k: string | null) => void;
}) {
  if (stale.length === 0) {
    return <EmptyRow message="No stale flags detected — flag hygiene looks good" />;
  }

  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: `1px solid ${T.border}` }}>
            <TH>Flag Key</TH>
            <TH>Owner</TH>
            <TH>Days at 100%</TH>
            <TH>Stale Score</TH>
            <TH>Action</TH>
          </tr>
        </thead>
        <tbody>
          {stale.map((f) => {
            const isHov = hovered === f.flag_key;
            const as = ACTION_STYLE[f.recommended_action] ?? ACTION_STYLE['REVIEW'];
            const scoreW = Math.round(f.stale_score * 100);
            const scoreColor = f.stale_score >= 0.8 ? T.red : f.stale_score >= 0.5 ? T.amber : T.textMuted;

            return (
              <tr
                key={f.flag_key}
                style={{
                  borderBottom: `1px solid ${T.borderSub}`,
                  background: isHov ? T.bg2 : 'transparent',
                  transition: 'background 0.1s',
                  cursor: 'default',
                }}
                onMouseEnter={() => setHovered(f.flag_key)}
                onMouseLeave={() => setHovered(null)}
              >
                {/* Flag key */}
                <td style={{ padding: '11px 16px' }}>
                  <a
                    href={`/flags/${f.flag_key}`}
                    style={{
                      fontFamily: "'JetBrains Mono','Fira Code',monospace",
                      fontSize: 12,
                      color: T.blue,
                      textDecoration: 'none',
                      fontWeight: 500,
                    }}
                    onMouseEnter={e => { (e.target as HTMLElement).style.textDecoration = 'underline'; }}
                    onMouseLeave={e => { (e.target as HTMLElement).style.textDecoration = 'none'; }}
                  >
                    {f.flag_key}
                  </a>
                </td>

                {/* Owner */}
                <td style={{ padding: '11px 16px' }}>
                  <code style={{
                    fontSize: 11,
                    color: T.textMuted,
                    background: T.bg2,
                    border: `1px solid ${T.border}`,
                    borderRadius: 4,
                    padding: '2px 7px',
                  }}>
                    {f.owner_id}
                  </code>
                </td>

                {/* Days */}
                <td style={{ padding: '11px 16px' }}>
                  <span style={{
                    fontSize: 13,
                    fontWeight: 600,
                    color: f.days_at_100_pct >= 60 ? T.red : f.days_at_100_pct >= 30 ? T.amber : T.textPrimary,
                    fontVariantNumeric: 'tabular-nums',
                  }}>
                    {f.days_at_100_pct}d
                  </span>
                </td>

                {/* Stale score bar */}
                <td style={{ padding: '11px 16px', minWidth: 150 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <div style={{
                      flex: 1,
                      height: 5,
                      background: T.bg2,
                      borderRadius: 3,
                      overflow: 'hidden',
                      border: `1px solid ${T.border}`,
                    }}>
                      <div style={{
                        width: `${scoreW}%`,
                        height: '100%',
                        background: scoreColor,
                        borderRadius: 3,
                        transition: 'width 0.4s ease',
                      }} />
                    </div>
                    <span style={{
                      fontSize: 11,
                      color: scoreColor,
                      fontWeight: 600,
                      minWidth: 32,
                      textAlign: 'right',
                      fontVariantNumeric: 'tabular-nums',
                    }}>
                      {scoreW}%
                    </span>
                  </div>
                </td>

                {/* Action badge */}
                <td style={{ padding: '11px 16px' }}>
                  <span style={{
                    fontSize: 11,
                    fontWeight: 600,
                    padding: '3px 10px',
                    borderRadius: 20,
                    background: as.bg,
                    border: `1px solid ${as.border}`,
                    color: as.color,
                    letterSpacing: '0.03em',
                  }}>
                    {f.recommended_action}
                  </span>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ─── Autonomous Rollout Table ─────────────────────────────────────────────────

function AutonomousTable({ recs, applyingKey, onApply }: {
  recs: AutonomousRecommendation[];
  applyingKey: string | null;
  onApply: (rec: AutonomousRecommendation) => void;
}) {
  const [hovered, setHovered] = useState<string | null>(null);

  if (recs.length === 0) {
    return <EmptyRow message="No flags currently in autonomous rollout mode" />;
  }

  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: `1px solid ${T.border}` }}>
            <TH>Flag Key</TH>
            <TH>Env</TH>
            <TH>Current</TH>
            <TH>Recommended</TH>
            <TH>Confidence</TH>
            <TH>Reason</TH>
            <TH></TH>
          </tr>
        </thead>
        <tbody>
          {recs.map(rec => {
            const rowKey = `${rec.flag_key}:${rec.environment}`;
            const isHov = hovered === rowKey;
            const isApplying = applyingKey === rowKey;
            const confPct = Math.round(rec.confidence * 100);
            const confColor = confPct >= 80 ? T.green : confPct >= 60 ? T.amber : T.red;
            const envColor = rec.environment === 'production' ? T.green
              : rec.environment === 'staging' ? T.amber : T.blue;

            return (
              <tr
                key={rowKey}
                style={{
                  borderBottom: `1px solid ${T.borderSub}`,
                  background: isHov ? T.bg2 : 'transparent',
                  transition: 'background 0.1s',
                }}
                onMouseEnter={() => setHovered(rowKey)}
                onMouseLeave={() => setHovered(null)}
              >
                {/* Flag key */}
                <td style={{ padding: '11px 16px' }}>
                  <a
                    href={`/flags/${rec.flag_key}`}
                    style={{
                      fontFamily: "'JetBrains Mono','Fira Code',monospace",
                      fontSize: 12,
                      color: T.blue,
                      textDecoration: 'none',
                      fontWeight: 500,
                    }}
                    onMouseEnter={e => { (e.target as HTMLElement).style.textDecoration = 'underline'; }}
                    onMouseLeave={e => { (e.target as HTMLElement).style.textDecoration = 'none'; }}
                  >
                    {rec.flag_key}
                  </a>
                </td>

                {/* Env */}
                <td style={{ padding: '11px 16px' }}>
                  <span style={{
                    fontSize: 11,
                    fontWeight: 600,
                    color: envColor,
                    background: `${envColor}18`,
                    border: `1px solid ${envColor}40`,
                    padding: '2px 9px',
                    borderRadius: 20,
                  }}>
                    {rec.environment}
                  </span>
                </td>

                {/* Current */}
                <td style={{ padding: '11px 16px' }}>
                  <span style={{ fontSize: 13, fontWeight: 600, color: T.textPrimary, fontVariantNumeric: 'tabular-nums' }}>
                    {rec.current_pct}%
                  </span>
                </td>

                {/* Recommended */}
                <td style={{ padding: '11px 16px' }}>
                  <span style={{ fontSize: 13, fontWeight: 700, color: T.blue, fontVariantNumeric: 'tabular-nums' }}>
                    {rec.recommended_pct}%
                  </span>
                </td>

                {/* Confidence */}
                <td style={{ padding: '11px 16px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
                    <div style={{
                      width: 42,
                      height: 4,
                      background: T.bg2,
                      borderRadius: 2,
                      overflow: 'hidden',
                      border: `1px solid ${T.border}`,
                    }}>
                      <div style={{
                        width: `${confPct}%`,
                        height: '100%',
                        background: confColor,
                        borderRadius: 2,
                      }} />
                    </div>
                    <span style={{ fontSize: 12, fontWeight: 600, color: confColor, fontVariantNumeric: 'tabular-nums' }}>
                      {confPct}%
                    </span>
                  </div>
                </td>

                {/* Reason */}
                <td style={{ padding: '11px 16px', maxWidth: 280 }}>
                  <span style={{
                    fontSize: 12,
                    color: T.textMuted,
                    display: '-webkit-box',
                    WebkitLineClamp: 2,
                    WebkitBoxOrient: 'vertical',
                    overflow: 'hidden',
                  } as React.CSSProperties}>
                    {rec.reason}
                  </span>
                </td>

                {/* Action */}
                <td style={{ padding: '11px 16px', textAlign: 'right' }}>
                  {rec.should_advance ? (
                    <button
                      onClick={() => onApply(rec)}
                      disabled={isApplying}
                      style={{
                        background: isApplying ? T.bg2 : '#1f6feb',
                        color: isApplying ? T.textDim : T.textPrimary,
                        border: `1px solid ${isApplying ? T.border : '#388bfd'}`,
                        borderRadius: 6,
                        padding: '5px 14px',
                        fontSize: 12,
                        fontWeight: 600,
                        cursor: isApplying ? 'not-allowed' : 'pointer',
                        transition: 'all 0.15s',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {isApplying ? 'Applying…' : 'Apply'}
                    </button>
                  ) : (
                    <span style={{ color: T.textDim, fontSize: 13 }}>—</span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ─── Recent Activity List ─────────────────────────────────────────────────────

function ActivityList({ entries }: { entries: ActivityEntry[] }) {
  if (entries.length === 0) {
    return <EmptyRow message="No recent activity" />;
  }

  const EVENT_COLOR: Record<string, string> = {
    flag_created:  T.green,
    flag_updated:  T.blue,
    flag_archived: T.red,
    flag_enabled:  T.green,
    flag_disabled: T.amber,
    kill_switch:   T.red,
  };

  return (
    <div style={{ padding: '4px 0' }}>
      {entries.map((entry, idx) => {
        const dotColor = EVENT_COLOR[entry.event_type] ?? T.textDim;
        return (
          <div
            key={idx}
            style={{
              display: 'flex',
              alignItems: 'flex-start',
              gap: 14,
              padding: '10px 20px',
              borderBottom: idx < entries.length - 1 ? `1px solid ${T.borderSub}` : 'none',
            }}
          >
            {/* dot + connecting line */}
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', paddingTop: 3 }}>
              <div style={{
                width: 8,
                height: 8,
                borderRadius: '50%',
                background: dotColor,
                boxShadow: `0 0 6px ${dotColor}80`,
                flexShrink: 0,
              }} />
              {idx < entries.length - 1 && (
                <div style={{ width: 1, flex: 1, background: T.border, minHeight: 16, marginTop: 4 }} />
              )}
            </div>

            {/* content */}
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
                <code style={{
                  fontFamily: "'JetBrains Mono','Fira Code',monospace",
                  fontSize: 12,
                  color: T.blue,
                  background: T.bg2,
                  border: `1px solid ${T.border}`,
                  borderRadius: 4,
                  padding: '1px 6px',
                }}>
                  {entry.flag_key}
                </code>
                <span style={{
                  fontSize: 12,
                  color: dotColor,
                  fontWeight: 500,
                }}>
                  {entry.event_type.replace(/_/g, ' ')}
                </span>
              </div>
              <div style={{ marginTop: 3, fontSize: 11, color: T.textDim }}>
                {entry.actor && (
                  <span style={{ marginRight: 8 }}>by {entry.actor}</span>
                )}
                <span>{timeAgo(entry.created_at)}</span>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ─── Main view ────────────────────────────────────────────────────────────────

export default function GovernanceDash() {
  const [applyingKey, setApplyingKey] = useState<string | null>(null);
  const [hoveredStale, setHoveredStale] = useState<string | null>(null);

  const isIntelAvailable = !INTEL_URL.includes('localhost');

  const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

  // ── TanStack Query data fetching ──────────────────────────────────────────

  const { data: healthSummary, isLoading: healthLoading } = useQuery({
    queryKey: ['governance', 'health'],
    queryFn: async () => {
      const r = await fetch(`${INTEL_URL}/api/v1/intelligence/health-summary`, { headers: hdrs });
      if (!r.ok) return { total_flags: 0, stale_flags: 0, health_score: 0 };
      return r.json() as Promise<{ total_flags: number; stale_flags: number; health_score: number }>;
    },
    refetchInterval: 60_000,
  });

  const { data: staleFlagsData = [], isLoading: staleLoading } = useQuery({
    queryKey: ['governance', 'stale'],
    queryFn: async () => {
      const r = await fetch(`${INTEL_URL}/api/v1/intelligence/stale-flags`, { headers: hdrs });
      if (!r.ok) return [] as StaleFlag[];
      const d = await r.json() as { flags?: StaleFlag[] };
      return d.flags ?? [];
    },
  });

  const { data: autonomousData } = useQuery({
    queryKey: ['governance', 'autonomous'],
    queryFn: async () => {
      const res = await fetch(`${INTEL_URL}/api/v1/rollout/recommendations`);
      if (!res.ok) return [] as AutonomousRecommendation[];
      const data = (await res.json()) as { recommendations?: AutonomousRecommendation[] };
      return data.recommendations ?? [];
    },
  });

  const { data: activityData = [] } = useQuery({
    queryKey: ['governance', 'activity'],
    queryFn: async () => {
      const now = Math.floor(Date.now() / 1000);
      const from = now - 7 * 86400;
      const res = await fetch(
        `${API_URL}/api/v1/audit?from=${from}&to=${now}&limit=20`,
        { headers: hdrs },
      );
      if (!res.ok) return [] as ActivityEntry[];
      const data = await res.json() as { entries?: Array<Record<string, unknown>> };
      return (data.entries ?? []).map(e => ({
        flag_key:   (e['flag_key'] as string) ?? (e['entity_id'] as string) ?? '—',
        event_type: (e['event_type'] as string) ?? 'flag_updated',
        actor:      (e['actor'] as string) ?? '',
        created_at: (e['created_at'] as number) ?? 0,
      })) as ActivityEntry[];
    },
  });

  const stale = staleFlagsData;
  const autonomousRecs = autonomousData ?? [];
  const activity = activityData;
  const loading = healthLoading || staleLoading;

  // Derive health from healthSummary or compute from stale data as fallback
  const health: HealthSummary | null = healthSummary
    ? healthSummary
    : null;

  const handleApplyRec = async (rec: AutonomousRecommendation) => {
    setApplyingKey(`${rec.flag_key}:${rec.environment}`);
    try {
      await fetch(`${INTEL_URL}/api/v1/rollout/update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          flag_key: rec.flag_key,
          environment: rec.environment,
          rollout_pct: rec.recommended_pct,
        }),
      });
    } catch {
      // silently ignore
    } finally {
      setApplyingKey(null);
    }
  };

  const healthPct = Math.round((health?.health_score ?? 1) * 100);
  const healthColor = healthPct >= 80 ? T.green : healthPct >= 60 ? T.amber : T.red;
  const activeFlags = (health?.total_flags ?? 0) - (health?.stale_flags ?? 0);

  // Demo time-series for health score trend (replace with real endpoint when available)
  const healthTrend: TimeSeriesPoint[] = Array.from({ length: 24 }, (_, i) => ({
    timestamp: Date.now() - (23 - i) * 3_600_000,
    value: Math.max(60, (health?.health_score ?? 0.8) * 100 + Math.sin(i) * 5),
  }));

  if (!isIntelAvailable) {
    return (
      <div style={{ padding: '32px 40px' }}>
        <EmptyState
          heading="Intelligence service offline"
          body="GovernanceDash requires the intelligence service. Deploy evaluator + intelligence to Northflank to enable this view."
        />
      </div>
    );
  }

  return (
    <div style={{
      padding: '28px 36px',
      maxWidth: 1280,
      margin: '0 auto',
      minHeight: '100vh',
      background: T.bg0,
    }}>

      {/* ── Page Header ─────────────────────────────────────────────────────── */}
      <Section
        label="GOVERNANCE"
        title="Flag Health"
        titleAs="h1"
        className="mb-7"
      >
        <p style={{ margin: 0, fontSize: 13, color: T.textMuted }}>
          SOC2 compliance &amp; flag health
        </p>
      </Section>

      {/* ── Loading ─────────────────────────────────────────────────────────── */}
      {loading && (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(4, 1fr)',
          gap: 12,
          marginBottom: 16,
        }}>
          <SkeletonStatCard />
          <SkeletonStatCard />
          <SkeletonStatCard />
          <SkeletonStatCard />
        </div>
      )}

      {!loading && !health && (
        <EmptyState
          heading="Governance data unavailable"
          body="Could not load flag health from the intelligence service."
        />
      )}

      {health && !loading && (
        <>
          {/* ── Metric Cards Row ───────────────────────────────────────────── */}
          <Reveal delay={0}>
            <div style={{
              display: 'grid',
              gridTemplateColumns: 'repeat(4, 1fr)',
              gap: 12,
              marginBottom: 16,
            }}>

              {/* Health Score — with ring */}
              <StatCard
                label="Health Score"
                value={`${healthPct}%`}
                color={healthColor}
                sub={healthPct >= 80 ? 'Passing all checks' : healthPct >= 60 ? 'Needs attention' : 'Action required'}
                ring={{ pct: healthPct, color: healthColor }}
              />

              {/* Stale Flags */}
              <StatCard
                label="Stale Flags"
                value={health.stale_flags}
                color={health.stale_flags > 0 ? T.amber : T.green}
                sub={health.stale_flags === 0 ? 'None detected' : `${health.stale_flags} flag${health.stale_flags !== 1 ? 's' : ''} need review`}
              />

              {/* Active Flags */}
              <StatCard
                label="Active Flags"
                value={activeFlags}
                color={T.blue}
                sub="Not stale"
              />

              {/* Total Flags */}
              <StatCard
                label="Total Flags"
                value={health.total_flags}
                color={T.textPrimary}
                sub="Across all projects"
              />
            </div>
          </Reveal>

          {/* ── Health Score Trend Chart ─────────────────────────────────────── */}
          <Reveal delay={0.08}>
            <div style={{
              background: T.bg1,
              border: `1px solid ${T.border}`,
              borderRadius: 10,
              marginBottom: 28,
              overflow: 'hidden',
            }}>
              <EvaluationChart
                data={healthTrend}
                title="Health Score (24h)"
                color={
                  healthPct >= 80 ? '#4ade80' :
                  healthPct >= 60 ? '#fbbf24' : '#f87171'
                }
                height={160}
                yLabel="%"
              />
            </div>
          </Reveal>

          {/* ── Main Content: Two-column layout ─────────────────────────────── */}
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 340px', gap: 16, alignItems: 'start' }}>

            {/* Left column */}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>

              {/* Stale Flags table */}
              <Reveal delay={0.16}>
                <SectionCard
                  title="Stale Flags"
                  subtitle={`${stale.length} flag${stale.length !== 1 ? 's' : ''} at 100% rollout for 30+ days`}
                >
                  <StaleFlagsTable
                    stale={stale}
                    hovered={hoveredStale}
                    setHovered={setHoveredStale}
                  />
                </SectionCard>
              </Reveal>

              {/* Autonomous Rollout Status */}
              <Reveal delay={0.24}>
                <SectionCard
                  title="Autonomous Rollout Status"
                  subtitle="Flags managed by the ML rollout engine"
                >
                  <AutonomousTable
                    recs={autonomousRecs}
                    applyingKey={applyingKey}
                    onApply={(rec) => void handleApplyRec(rec)}
                  />
                </SectionCard>
              </Reveal>
            </div>

            {/* Right column — Recent Activity */}
            <Reveal delay={0.24}>
              <SectionCard
                title="Recent Activity"
                subtitle="Last 7 days"
              >
                <ActivityList entries={activity} />
              </SectionCard>
            </Reveal>
          </div>
        </>
      )}
    </div>
  );
}
