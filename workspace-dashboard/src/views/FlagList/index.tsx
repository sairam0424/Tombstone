import { useRef, useDeferredValue, useEffect, useState, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { useVirtualizer } from '@tanstack/react-virtual';
import { useQueryState } from 'nuqs';
import { SkeletonRow } from '../../components/SkeletonRow.js';
import { useFlags, useEnvSnapshot, type FlagItem, type EnvState } from '../../hooks/useFlags.js';
import { FlagCreateModal } from '../../components/FlagCreateModal.js';
import { useDensity } from '../../hooks/useDensity.js';
import { DensityToggle } from '../../components/DensityToggle.js';

type Env = 'development' | 'staging' | 'production';

const ENV_PILL: Record<Env, { active: { bg: string; color: string; border: string }; idle: { bg: string; color: string; border: string } }> = {
  development: {
    active: { bg: '#1d4ed8', color: '#fff', border: '#1d4ed8' },
    idle:   { bg: 'transparent', color: '#6b7280', border: '#1a1a1a' },
  },
  staging: {
    active: { bg: '#b45309', color: '#fff', border: '#b45309' },
    idle:   { bg: 'transparent', color: '#6b7280', border: '#1a1a1a' },
  },
  production: {
    active: { bg: '#15803d', color: '#fff', border: '#15803d' },
    idle:   { bg: 'transparent', color: '#6b7280', border: '#1a1a1a' },
  },
};

const STATE_BADGE: Record<string, { text: string; bg: string; border: string }> = {
  ACTIVE:   { text: '#4ade80', bg: 'rgba(74,222,128,0.10)',  border: 'rgba(74,222,128,0.25)' },
  DRAFT:    { text: '#9ca3af', bg: 'rgba(156,163,175,0.08)', border: 'rgba(156,163,175,0.18)' },
  COMPLETE: { text: '#60a5fa', bg: 'rgba(96,165,250,0.10)',  border: 'rgba(96,165,250,0.25)' },
  ARCHIVED: { text: '#4b5563', bg: 'rgba(75,85,99,0.08)',    border: 'rgba(75,85,99,0.18)' },
};

// Pulsing green dot keyframes injected once
const PULSE_STYLE = `
@keyframes pulse-dot {
  0%, 100% { box-shadow: 0 0 0 0 rgba(74,222,128,0.6), 0 0 6px rgba(74,222,128,0.4); }
  50%       { box-shadow: 0 0 0 4px rgba(74,222,128,0), 0 0 10px rgba(74,222,128,0.5); }
}
`;

function injectPulseStyle() {
  if (typeof document !== 'undefined' && !document.getElementById('tombstone-pulse-style')) {
    const tag = document.createElement('style');
    tag.id = 'tombstone-pulse-style';
    tag.textContent = PULSE_STYLE;
    document.head.appendChild(tag);
  }
}


export default function FlagList() {
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const [env, setEnv] = useQueryState<Env>('env', {
    defaultValue: 'production',
    parse: (v) => ((['development', 'staging', 'production'] as const).includes(v as Env) ? (v as Env) : 'production'),
    serialize: (v) => v,
  });
  const [search, setSearch] = useQueryState('q', { defaultValue: '', shallow: true });

  // useDeferredValue: adaptive interruptible search — no fixed debounce delay (React 19)
  const deferredSearch = useDeferredValue(search);

  const { density, setDensity, rowHeight } = useDensity();

  const { data: flags = [], isLoading: flagsLoading } = useFlags();
  const { data: envStates = {}, isLoading: snapshotLoading } = useEnvSnapshot(env);
  const loading = flagsLoading || snapshotLoading;

  useEffect(() => { injectPulseStyle(); }, []);

  // Filter uses deferred value — won't block typing even with 5000+ flags
  const filtered = flags.filter((f: FlagItem) =>
    !deferredSearch ||
    f.key.toLowerCase().includes(deferredSearch.toLowerCase()) ||
    f.name.toLowerCase().includes(deferredSearch.toLowerCase())
  );

  const parentRef = useRef<HTMLDivElement>(null);

  // useCallback ensures the estimateSize ref is replaced when rowHeight changes.
  // TanStack Virtual v3 honours a new callback identity and re-estimates all rows,
  // so switching density correctly resizes the virtual slot heights.
  const estimateSize = useCallback(() => rowHeight, [rowHeight]);

  const virtualizer = useVirtualizer({
    count: loading ? 8 : filtered.length,
    getScrollElement: () => parentRef.current,
    estimateSize,
    overscan: 5,
  });

  const onCount = Object.values(envStates).filter((s: EnvState) => s.enabled).length;
  const offCount = flags.length - onCount;

  const STAT_CARDS = [
    { label: 'Total',    val: flags.length, color: '#60a5fa' },
    { label: 'Enabled',  val: onCount,       color: '#4ade80' },
    { label: 'Disabled', val: offCount,      color: '#6b7280' },
  ];

  return (
    <div style={{ padding: '32px 40px', maxWidth: 1320, margin: '0 auto', fontFamily: 'Inter, system-ui, sans-serif' }}>

      {/* ── Header ── */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        {/* Title + subtitle */}
        <div>
          <h1 style={{ fontSize: 28, fontWeight: 700, color: '#f9fafb', margin: '0 0 6px', letterSpacing: '-0.02em' }}>
            Feature Flags
          </h1>
          <p style={{ fontSize: 13, color: '#6b7280', margin: 0, lineHeight: 1.5 }}>
            Manage, monitor, and roll out flags across environments.&nbsp;
            <span style={{ color: '#9ca3af' }}>
              {flags.length} total &middot;&nbsp;
              <span style={{ color: ENV_PILL[env].active.bg === 'transparent' ? '#6b7280' : ENV_PILL[env].active.bg }}>
                {onCount} enabled in {env}
              </span>
            </span>
          </p>
        </div>

        {/* Stat cards + Create button */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {STAT_CARDS.map(s => (
            <div key={s.label} style={{
              background: '#111',
              border: '1px solid #1a1a1a',
              borderRadius: 12,
              padding: '12px 20px',
              textAlign: 'center' as const,
              minWidth: 80,
            }}>
              <div style={{ fontSize: 26, fontWeight: 700, color: s.color, lineHeight: 1, fontVariantNumeric: 'tabular-nums' }}>
                {s.val}
              </div>
              <div style={{ fontSize: 11, color: '#4b5563', marginTop: 4, textTransform: 'uppercase' as const, letterSpacing: '0.06em' }}>
                {s.label}
              </div>
            </div>
          ))}

          {/* Create Flag button */}
          <button
            onClick={() => setCreateOpen(true)}
            style={{
              marginLeft: 8,
              padding: '10px 18px',
              background: '#2563eb',
              border: 'none',
              borderRadius: 8,
              color: '#fff',
              fontSize: 13,
              fontWeight: 600,
              cursor: 'pointer',
              letterSpacing: '-0.01em',
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              transition: 'background 0.15s',
              whiteSpace: 'nowrap' as const,
            }}
            onMouseEnter={e => { (e.currentTarget as HTMLButtonElement).style.background = '#1d4ed8'; }}
            onMouseLeave={e => { (e.currentTarget as HTMLButtonElement).style.background = '#2563eb'; }}
          >
            <span style={{ fontSize: 16, lineHeight: 1, marginTop: -1 }}>+</span>
            Create Flag
          </button>
        </div>
      </div>

      {/* ── Controls ── */}
      <div style={{ display: 'flex', gap: 10, marginBottom: 20, flexWrap: 'wrap' as const, alignItems: 'center' }}>
        {/* Search */}
        <input
          type="text"
          placeholder="Search flags by key or name…"
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{
            flex: '1 1 280px',
            minWidth: 220,
            background: '#111',
            border: '1px solid #1a1a1a',
            borderRadius: 8,
            padding: '9px 14px',
            fontSize: 14,
            color: '#f9fafb',
            outline: 'none',
            transition: 'border-color 0.15s',
          }}
          onFocus={e => { (e.target as HTMLInputElement).style.borderColor = '#3b82f6'; }}
          onBlur={e => { (e.target as HTMLInputElement).style.borderColor = '#1a1a1a'; }}
        />

        {/* Environment pills */}
        <div style={{ display: 'flex', gap: 6 }}>
          {(['development', 'staging', 'production'] as Env[]).map(e => {
            const active = env === e;
            const styles = active ? ENV_PILL[e].active : ENV_PILL[e].idle;
            return (
              <button key={e} onClick={() => setEnv(e)} style={{
                padding: '8px 16px',
                borderRadius: 999,
                fontSize: 12,
                fontWeight: 600,
                cursor: 'pointer',
                border: `1px solid ${styles.border}`,
                background: styles.bg,
                color: styles.color,
                transition: 'all 0.15s',
                letterSpacing: '-0.01em',
                textTransform: 'capitalize' as const,
              }}>
                {e}
              </button>
            );
          })}
        </div>

        <DensityToggle density={density} onChange={setDensity} />
      </div>

      {/* ── Table ── */}
      <div
        ref={parentRef}
        style={{
          background: 'var(--color-bg-surface)',
          border: '1px solid var(--color-border)',
          borderRadius: 12,
          overflow: 'auto',
          maxHeight: 'calc(100vh - 280px)',
        }}
      >
        {/* Table header */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: '2fr 80px 140px 90px 90px 120px 60px',
          padding: '10px 16px',
          borderBottom: '1px solid var(--color-border)',
          position: 'sticky', top: 0, zIndex: 1,
          background: 'var(--color-bg-surface)',
        }}>
          {['Flag Key', 'Status', 'Rollout', 'Type', 'State', 'Owner', ''].map(h => (
            <div key={h} style={{ fontSize: 11, fontWeight: 600, textTransform: 'uppercase' as const, letterSpacing: '0.07em', color: 'var(--color-fg-subtle)' }}>{h}</div>
          ))}
        </div>

        {/* Virtual rows */}
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map(vRow => {
            if (loading) {
              return (
                <div key={vRow.key} style={{ position: 'absolute', top: vRow.start, left: 0, right: 0, height: vRow.size }}>
                  <SkeletonRow />
                </div>
              );
            }
            const flag = filtered[vRow.index];
            if (!flag) return null;
            const es = envStates[flag.key];
            const sb = STATE_BADGE[flag.state] ?? STATE_BADGE['DRAFT'];
            const pct = es?.rollout_pct ?? 0;
            const enabled = es?.enabled ?? false;
            const fillColor = !enabled ? 'var(--color-border)' : pct === 100 ? 'var(--color-risk-low)' : pct >= 50 ? 'var(--color-risk-medium)' : 'var(--color-accent)';

            // Derive vertical padding from rowHeight so density visually adapts.
            // vRow.size equals the estimated rowHeight (set by estimateSize above).
            // Using padding-top/bottom to fill the slot prevents content overflow
            // clipping in Condensed (32px) and gaps in Spacious (72px).
            const vPad = Math.max(0, Math.floor((vRow.size - 20) / 2));

            return (
              <div
                key={vRow.key}
                style={{
                  position: 'absolute', top: vRow.start, left: 0, right: 0, height: vRow.size,
                  display: 'grid',
                  gridTemplateColumns: '2fr 80px 140px 90px 90px 120px 60px',
                  alignItems: 'center',
                  padding: `${vPad}px 16px`,
                  borderBottom: '1px solid var(--color-border)',
                  cursor: 'pointer',
                  transition: 'background 0.1s',
                  boxSizing: 'border-box' as const,
                }}
                onMouseEnter={e => { (e.currentTarget as HTMLElement).style.background = 'var(--color-bg-elevated)'; }}
                onMouseLeave={e => { (e.currentTarget as HTMLElement).style.background = 'transparent'; }}
                onClick={() => navigate(`/flags/${flag.key}`)}
              >
                {/* Flag Key */}
                <div>
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--color-accent)', fontWeight: 500 }}>{flag.key}</div>
                  {flag.name && flag.name !== flag.key && (
                    <div style={{ fontSize: 11, color: 'var(--color-fg-subtle)', marginTop: 2 }}>{flag.name}</div>
                  )}
                </div>
                {/* Status */}
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span className={`status-dot status-dot-${enabled ? 'on' : 'off'}`} />
                  <span style={{ fontSize: 12, fontWeight: 500, color: enabled ? 'var(--color-risk-low)' : 'var(--color-fg-subtle)' }}>
                    {enabled ? 'ON' : 'OFF'}
                  </span>
                </div>
                {/* Rollout */}
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <div style={{ flex: 1, height: 4, background: 'var(--color-bg-overlay)', borderRadius: 2, overflow: 'hidden' }}>
                    <div style={{ width: `${pct}%`, height: '100%', background: fillColor, borderRadius: 2, transition: 'width 0.5s ease' }} />
                  </div>
                  <span style={{ fontSize: 11, color: 'var(--color-fg-subtle)', width: 30, textAlign: 'right' as const }}>{pct}%</span>
                </div>
                {/* Type */}
                <div><code style={{ fontSize: 11, color: 'var(--color-fg-muted)', background: 'var(--color-bg-elevated)', border: '1px solid var(--color-border)', borderRadius: 4, padding: '2px 6px' }}>{flag.flag_type}</code></div>
                {/* State */}
                <div><span className="badge" style={{ fontSize: 11, fontWeight: 500, padding: '2px 8px', borderRadius: 999, background: sb.bg, border: `1px solid ${sb.border}`, color: sb.text }}>{flag.state}</span></div>
                {/* Owner */}
                <div style={{ fontSize: 12, color: 'var(--color-fg-subtle)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' as const }}>{flag.owner_id}</div>
                {/* Action */}
                <div style={{ textAlign: 'right' as const, fontSize: 12, color: 'var(--color-fg-subtle)' }}>{'→'}</div>
              </div>
            );
          })}
        </div>

      </div>

      <FlagCreateModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  );
}
