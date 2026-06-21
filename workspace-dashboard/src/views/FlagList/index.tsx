import { useState, useEffect, useCallback } from 'react';
import { Link } from 'react-router-dom';

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

const ENV_COLOR: Record<Env, { border: string; text: string; bg: string }> = {
  development: { border: '#388bfd', text: '#58a6ff', bg: 'rgba(56,139,253,0.1)' },
  staging:     { border: '#e3b341', text: '#e3b341', bg: 'rgba(227,179,65,0.1)' },
  production:  { border: '#3fb950', text: '#3fb950', bg: 'rgba(63,185,80,0.1)' },
};

const STATE_BADGE: Record<string, { text: string; bg: string; border: string }> = {
  ACTIVE:   { text: '#3fb950', bg: 'rgba(63,185,80,0.12)',   border: 'rgba(63,185,80,0.3)' },
  DRAFT:    { text: '#8b949e', bg: 'rgba(139,148,158,0.08)', border: 'rgba(139,148,158,0.2)' },
  COMPLETE: { text: '#58a6ff', bg: 'rgba(88,166,255,0.1)',   border: 'rgba(88,166,255,0.25)' },
  ARCHIVED: { text: '#484f58', bg: 'rgba(72,79,88,0.08)',    border: 'rgba(72,79,88,0.2)' },
};

function RolloutBar({ pct, enabled }: { pct: number; enabled: boolean }) {
  const c = !enabled ? '#21262d' : pct === 100 ? '#3fb950' : pct >= 50 ? '#e3b341' : '#58a6ff';
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 110 }}>
      <div style={{ flex: 1, height: 5, background: '#161b22', borderRadius: 3, overflow: 'hidden', border: '1px solid #21262d' }}>
        <div style={{ width: `${pct}%`, height: '100%', background: c, borderRadius: 3, transition: 'width 0.5s ease' }} />
      </div>
      <span style={{ fontSize: 11, color: '#8b949e', width: 30, textAlign: 'right' as const }}>{pct}%</span>
    </div>
  );
}

export default function FlagList() {
  const [flags, setFlags] = useState<FlagItem[]>([]);
  const [envStates, setEnvStates] = useState<Record<string, EnvState>>({});
  const [env, setEnv] = useState<Env>('production');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [hovered, setHovered] = useState<string | null>(null);

  const apiUrl = 'http://localhost:8081';
  const tok = 'sdk-dev-token-change-in-prod';
  const hdrs = { Authorization: `Bearer ${tok}` };

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [fr, sr] = await Promise.all([
        fetch(`${apiUrl}/api/v1/flags`, { headers: hdrs }).then(r => r.json()) as Promise<{ flags: FlagItem[] }>,
        fetch(`${apiUrl}/api/v1/environments/snapshot?environment=${env}`, { headers: hdrs }).then(r => r.json()) as Promise<{ flags: EnvState[] }>,
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

  return (
    <div style={{ padding: '28px 36px', maxWidth: 1280, margin: '0 auto' }}>

      {/* ── Header ── */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 24 }}>
        <div>
          <h1 style={{ fontSize: 24, fontWeight: 700, color: '#e6edf3', margin: '0 0 4px' }}>Feature Flags</h1>
          <p style={{ fontSize: 13, color: '#8b949e', margin: 0 }}>
            {flags.length} total · {onCount} enabled in <span style={{ color: ENV_COLOR[env].text }}>{env}</span>
          </p>
        </div>
        {/* Stat cards */}
        <div style={{ display: 'flex', gap: 10 }}>
          {[
            { label: 'Total',    val: flags.length,              color: '#58a6ff' },
            { label: 'Enabled',  val: onCount,                   color: '#3fb950' },
            { label: 'Disabled', val: flags.length - onCount,    color: '#484f58' },
          ].map(s => (
            <div key={s.label} style={{
              background: '#0d1117', border: '1px solid #21262d', borderRadius: 8,
              padding: '10px 18px', textAlign: 'center' as const, minWidth: 76,
              transition: 'border-color 0.15s, transform 0.15s',
            }}>
              <div style={{ fontSize: 24, fontWeight: 700, color: s.color, lineHeight: 1 }}>{s.val}</div>
              <div style={{ fontSize: 11, color: '#484f58', marginTop: 3 }}>{s.label}</div>
            </div>
          ))}
        </div>
      </div>

      {/* ── Controls ── */}
      <div style={{ display: 'flex', gap: 10, marginBottom: 18, flexWrap: 'wrap' as const }}>
        <input type="text" placeholder="Search by key or name…" value={search}
          onChange={e => setSearch(e.target.value)}
          style={{
            flex: '1 1 240px', minWidth: 200,
            background: '#161b22', border: '1px solid #21262d', borderRadius: 8,
            padding: '8px 14px', fontSize: 13, color: '#e6edf3', outline: 'none',
          }}
          onFocus={e => { (e.target as HTMLInputElement).style.borderColor = '#388bfd'; }}
          onBlur={e => { (e.target as HTMLInputElement).style.borderColor = '#21262d'; }}
        />
        <div style={{ display: 'flex', gap: 6 }}>
          {(['development', 'staging', 'production'] as Env[]).map(e => {
            const ec = ENV_COLOR[e];
            const active = env === e;
            return (
              <button key={e} onClick={() => setEnv(e)} style={{
                padding: '7px 14px', borderRadius: 6, fontSize: 12, fontWeight: 500,
                cursor: 'pointer', border: `1px solid ${active ? ec.border : '#21262d'}`,
                background: active ? ec.bg : 'transparent',
                color: active ? ec.text : '#8b949e',
                transition: 'all 0.15s',
              }}>
                {e}
              </button>
            );
          })}
        </div>
      </div>

      {/* ── Table ── */}
      <div style={{ background: '#0d1117', border: '1px solid #21262d', borderRadius: 10, overflow: 'hidden' }}>
        {loading ? (
          <div style={{ padding: 60, textAlign: 'center' as const, color: '#484f58', fontSize: 14 }}>Loading flags…</div>
        ) : filtered.length === 0 ? (
          <div style={{ padding: 60, textAlign: 'center' as const, color: '#484f58', fontSize: 14 }}>
            {search ? `No flags match "${search}"` : 'No flags yet.'}
          </div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse' as const, fontSize: 13 }}>
            <thead>
              <tr style={{ borderBottom: '1px solid #21262d' }}>
                {['Flag Key', 'Status', 'Rollout', 'Type', 'State', 'Owner', ''].map(h => (
                  <th key={h} style={{
                    textAlign: 'left' as const, padding: '10px 16px',
                    fontSize: 11, fontWeight: 600, textTransform: 'uppercase' as const,
                    letterSpacing: '0.07em', color: '#484f58',
                  }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {filtered.map(flag => {
                const es = envStates[flag.key];
                const sb = STATE_BADGE[flag.state] ?? STATE_BADGE['DRAFT'];
                const hov = hovered === flag.id;
                return (
                  <tr key={flag.id}
                    style={{ borderBottom: '1px solid #161b22', background: hov ? '#161b22' : 'transparent', transition: 'background 0.1s' }}
                    onMouseEnter={() => setHovered(flag.id)}
                    onMouseLeave={() => setHovered(null)}
                  >
                    {/* Key */}
                    <td style={{ padding: '12px 16px', maxWidth: 220 }}>
                      <Link to={`/flags/${flag.key}`} style={{
                        fontFamily: "'JetBrains Mono','Fira Code',monospace",
                        fontSize: 12, color: '#58a6ff', textDecoration: 'none', fontWeight: 500,
                      }}
                        onMouseEnter={e => { (e.target as HTMLElement).style.textDecoration = 'underline'; }}
                        onMouseLeave={e => { (e.target as HTMLElement).style.textDecoration = 'none'; }}
                      >
                        {flag.key}
                      </Link>
                      {flag.name && flag.name !== flag.key && (
                        <div style={{ fontSize: 11, color: '#8b949e', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' as const, maxWidth: 200 }}>
                          {flag.name}
                        </div>
                      )}
                    </td>
                    {/* Status */}
                    <td style={{ padding: '12px 16px' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <div style={{
                          width: 7, height: 7, borderRadius: '50%',
                          background: es?.enabled ? '#3fb950' : '#21262d',
                          boxShadow: es?.enabled ? '0 0 6px rgba(63,185,80,0.5)' : 'none',
                        }} />
                        <span style={{ fontSize: 12, fontWeight: 500, color: es?.enabled ? '#3fb950' : '#484f58' }}>
                          {es?.enabled ? 'ON' : 'OFF'}
                        </span>
                      </div>
                    </td>
                    {/* Rollout */}
                    <td style={{ padding: '12px 16px', minWidth: 140 }}>
                      <RolloutBar pct={es?.rollout_pct ?? 0} enabled={es?.enabled ?? false} />
                    </td>
                    {/* Type */}
                    <td style={{ padding: '12px 16px' }}>
                      <code style={{
                        fontSize: 11, color: '#8b949e',
                        background: '#161b22', border: '1px solid #21262d',
                        borderRadius: 4, padding: '2px 7px',
                      }}>{flag.flag_type}</code>
                    </td>
                    {/* State */}
                    <td style={{ padding: '12px 16px' }}>
                      <span style={{
                        fontSize: 11, fontWeight: 500, padding: '3px 9px', borderRadius: 20,
                        background: sb.bg, border: `1px solid ${sb.border}`, color: sb.text,
                      }}>{flag.state}</span>
                    </td>
                    {/* Owner */}
                    <td style={{ padding: '12px 16px', maxWidth: 160 }}>
                      <span style={{ fontSize: 12, color: '#8b949e', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' as const, display: 'block' }}>
                        {flag.owner_id}
                      </span>
                    </td>
                    {/* Action */}
                    <td style={{ padding: '12px 16px', textAlign: 'right' as const }}>
                      <Link to={`/flags/${flag.key}`} style={{
                        fontSize: 12, color: hov ? '#58a6ff' : '#484f58',
                        textDecoration: 'none', fontWeight: 500, transition: 'color 0.15s',
                      }}>
                        Details →
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
