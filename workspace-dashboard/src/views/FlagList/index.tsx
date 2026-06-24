import { useState, useEffect, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { API_URL, SDK_TOKEN } from '../../config.js';

interface FlagItem {
  id: string;
  key: string;
  name: string;
  description: string;
  state: string;
  owner_id: string;
  flag_type: string;
}

interface EnvState {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
}

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

function RolloutBar({ pct, enabled }: { pct: number; enabled: boolean }) {
  const fillColor = !enabled
    ? '#1a1a1a'
    : pct === 100
    ? '#22c55e'
    : pct >= 50
    ? '#f59e0b'
    : '#3b82f6';

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 120 }}>
      <div style={{
        flex: 1,
        height: 4,
        background: '#1a1a1a',
        borderRadius: 2,
        overflow: 'hidden',
      }}>
        <div style={{
          width: `${pct}%`,
          height: '100%',
          background: fillColor,
          borderRadius: 2,
          transition: 'width 0.5s ease',
        }} />
      </div>
      <span style={{ fontSize: 11, color: '#6b7280', width: 32, textAlign: 'right' as const }}>
        {pct}%
      </span>
    </div>
  );
}

export default function FlagList() {
  injectPulseStyle();

  const [flags, setFlags] = useState<FlagItem[]>([]);
  const [envStates, setEnvStates] = useState<Record<string, EnvState>>({});
  const [env, setEnv] = useState<Env>('production');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [hovered, setHovered] = useState<string | null>(null);

  const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [fr, sr] = await Promise.all([
        fetch(`${API_URL}/api/v1/flags`, { headers: hdrs }).then(r => r.json()) as Promise<{ flags: FlagItem[] }>,
        fetch(`${API_URL}/api/v1/environments/snapshot?environment=${env}`, { headers: hdrs }).then(r => r.json()) as Promise<{ flags: EnvState[] }>,
      ]);
      setFlags(fr.flags ?? []);
      const m: Record<string, EnvState> = {};
      for (const s of sr.flags ?? []) m[s.flag_key] = s;
      setEnvStates(m);
    } catch (e) { console.error(e); }
    finally { setLoading(false); }
  }, [env]);

  useEffect(() => { void load(); }, [load]);

  const filtered = flags.filter(f =>
    !search || f.key.toLowerCase().includes(search.toLowerCase()) || f.name.toLowerCase().includes(search.toLowerCase())
  );

  const onCount = Object.values(envStates).filter(s => s.enabled).length;
  const offCount = flags.length - onCount;

  const STAT_CARDS = [
    { label: 'Total',    val: flags.length, color: '#60a5fa' },
    { label: 'Enabled',  val: onCount,       color: '#4ade80' },
    { label: 'Disabled', val: offCount,      color: '#6b7280' },
  ];

  const COL_HEADERS = ['Flag Key', 'Status', 'Rollout', 'Type', 'State', 'Owner', ''];

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
      </div>

      {/* ── Table ── */}
      <div style={{
        background: '#0c0c0c',
        border: '1px solid #1a1a1a',
        borderRadius: 14,
        overflow: 'hidden',
      }}>
        {loading ? (
          <div style={{ padding: 72, textAlign: 'center' as const, color: '#4b5563', fontSize: 14 }}>
            Loading flags…
          </div>
        ) : filtered.length === 0 ? (
          /* ── Empty state ── */
          <div style={{
            padding: '80px 40px',
            textAlign: 'center' as const,
            display: 'flex',
            flexDirection: 'column' as const,
            alignItems: 'center',
            gap: 14,
          }}>
            {/* Flag icon */}
            <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#374151" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z"/>
              <line x1="4" y1="22" x2="4" y2="15"/>
            </svg>
            <div style={{ fontSize: 16, fontWeight: 600, color: '#6b7280' }}>
              {search ? `No flags match "${search}"` : 'No flags yet. Create your first flag.'}
            </div>
            {!search && (
              <div style={{ fontSize: 13, color: '#374151' }}>
                Click "Create Flag" above to get started.
              </div>
            )}
          </div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse' as const, fontSize: 13 }}>
            <thead>
              <tr style={{ borderBottom: '1px solid #1a1a1a' }}>
                {COL_HEADERS.map((h, i) => (
                  <th key={`${h}-${i}`} style={{
                    textAlign: 'left' as const,
                    padding: '11px 16px',
                    fontSize: 11,
                    fontWeight: 600,
                    textTransform: 'uppercase' as const,
                    letterSpacing: '0.08em',
                    color: '#6b7280',
                    borderBottom: '1px solid #1a1a1a',
                    background: '#0c0c0c',
                    whiteSpace: 'nowrap' as const,
                  }}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {filtered.map(flag => {
                const es = envStates[flag.key];
                const sb = STATE_BADGE[flag.state] ?? STATE_BADGE['DRAFT'];
                const hov = hovered === flag.id;
                return (
                  <tr
                    key={flag.id}
                    style={{
                      borderBottom: '1px solid #111',
                      background: hov ? '#111' : 'transparent',
                      transition: 'background 150ms ease',
                    }}
                    onMouseEnter={() => setHovered(flag.id)}
                    onMouseLeave={() => setHovered(null)}
                  >
                    {/* Flag key + name */}
                    <td style={{ padding: '13px 16px', maxWidth: 240 }}>
                      <Link
                        to={`/flags/${flag.key}`}
                        style={{
                          fontFamily: "'JetBrains Mono','Fira Code','Cascadia Code',monospace",
                          fontSize: 12,
                          color: '#3b82f6',
                          textDecoration: 'none',
                          fontWeight: 500,
                          letterSpacing: '-0.01em',
                        }}
                        onMouseEnter={e => { (e.target as HTMLElement).style.textDecoration = 'underline'; }}
                        onMouseLeave={e => { (e.target as HTMLElement).style.textDecoration = 'none'; }}
                      >
                        {flag.key}
                      </Link>
                      {flag.name && flag.name !== flag.key && (
                        <div style={{
                          fontSize: 11,
                          color: '#6b7280',
                          marginTop: 3,
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap' as const,
                          maxWidth: 210,
                        }}>
                          {flag.name}
                        </div>
                      )}
                    </td>

                    {/* Status — pulsing dot */}
                    <td style={{ padding: '13px 16px' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
                        <div style={{
                          width: 7,
                          height: 7,
                          borderRadius: '50%',
                          background: es?.enabled ? '#4ade80' : '#374151',
                          animation: es?.enabled ? 'pulse-dot 2s ease-in-out infinite' : 'none',
                          flexShrink: 0,
                        }} />
                        <span style={{
                          fontSize: 12,
                          fontWeight: 600,
                          color: es?.enabled ? '#4ade80' : '#4b5563',
                          letterSpacing: '0.04em',
                        }}>
                          {es?.enabled ? 'ON' : 'OFF'}
                        </span>
                      </div>
                    </td>

                    {/* Rollout bar */}
                    <td style={{ padding: '13px 16px', minWidth: 150 }}>
                      <RolloutBar pct={es?.rollout_pct ?? 0} enabled={es?.enabled ?? false} />
                    </td>

                    {/* Type badge */}
                    <td style={{ padding: '13px 16px' }}>
                      <code style={{
                        fontSize: 11,
                        color: '#9ca3af',
                        background: '#111',
                        border: '1px solid #1a1a1a',
                        borderRadius: 4,
                        padding: '2px 8px',
                        fontFamily: "'JetBrains Mono','Fira Code',monospace",
                        letterSpacing: '-0.01em',
                      }}>
                        {flag.flag_type}
                      </code>
                    </td>

                    {/* State pill */}
                    <td style={{ padding: '13px 16px' }}>
                      <span style={{
                        fontSize: 11,
                        fontWeight: 600,
                        padding: '3px 10px',
                        borderRadius: 999,
                        background: sb.bg,
                        border: `1px solid ${sb.border}`,
                        color: sb.text,
                        letterSpacing: '0.04em',
                        textTransform: 'uppercase' as const,
                      }}>
                        {flag.state}
                      </span>
                    </td>

                    {/* Owner */}
                    <td style={{ padding: '13px 16px', maxWidth: 180 }}>
                      <span style={{
                        fontSize: 12,
                        color: '#6b7280',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap' as const,
                        display: 'block',
                      }}>
                        {flag.owner_id}
                      </span>
                    </td>

                    {/* View action — visible on row hover */}
                    <td style={{ padding: '13px 16px', textAlign: 'right' as const }}>
                      <Link
                        to={`/flags/${flag.key}`}
                        style={{
                          fontSize: 12,
                          fontWeight: 500,
                          color: hov ? '#60a5fa' : 'transparent',
                          textDecoration: 'none',
                          transition: 'color 150ms ease',
                          letterSpacing: '-0.01em',
                          whiteSpace: 'nowrap' as const,
                        }}
                      >
                        View &rarr;
                      </Link>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
