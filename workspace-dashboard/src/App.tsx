import { Routes, Route, NavLink, useLocation } from 'react-router-dom';
import { useState, useCallback } from 'react';
import { Activity } from 'lucide-react';
import { CommandPalette } from './components/CommandPalette.js';
import { LiveFeed } from './components/LiveFeed.js';
import { useKeyboard } from './hooks/useKeyboard.js';
import FlagList from './views/FlagList/index.js';
import FlagDetail from './views/FlagDetail/index.js';
import SLOView from './views/SLOView/index.js';
import IncidentTimeline from './views/IncidentTimeline/index.js';
import GovernanceDash from './views/GovernanceDash/index.js';
import ApprovalQueue from './views/ApprovalQueue/index.js';
import BreakGlassView from './views/BreakGlass/index.js';
import Experiments from './views/Experiments/index.js';
import DependencyGraph from './views/DependencyGraph/index.js';
import Marketplace from './views/Marketplace/index.js';

// ── SVG Icons ──────────────────────────────────────────────────────────────

const IconFlag = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z" />
    <line x1="4" y1="22" x2="4" y2="15" />
  </svg>
);

const IconLightning = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
  </svg>
);

const IconCheckCircle = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14" />
    <polyline points="22 4 12 14.01 9 11.01" />
  </svg>
);

const IconShield = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
  </svg>
);

const IconShare = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="18" cy="5" r="3" />
    <circle cx="6" cy="12" r="3" />
    <circle cx="18" cy="19" r="3" />
    <line x1="8.59" y1="13.51" x2="15.42" y2="17.49" />
    <line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
  </svg>
);

const IconBarChart = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <line x1="18" y1="20" x2="18" y2="10" />
    <line x1="12" y1="20" x2="12" y2="4" />
    <line x1="6" y1="20" x2="6" y2="14" />
    <line x1="2" y1="20" x2="22" y2="20" />
  </svg>
);

const IconBeaker = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M9 3h6v8l3.5 5.5A2 2 0 0 1 16.83 19H7.17a2 2 0 0 1-1.67-3L9 11V3z" />
    <line x1="9" y1="9" x2="15" y2="9" />
  </svg>
);

const IconPuzzle = () => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M20.59 13.41l-7.17 7.17a2 2 0 0 1-2.83 0L2 12V2h10l8.59 8.59a2 2 0 0 1 0 2.82z" />
    <line x1="7" y1="7" x2="7.01" y2="7" />
  </svg>
);

const IconChevronDown = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <polyline points="6 9 12 15 18 9" />
  </svg>
);

const IconPlus = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
    <line x1="12" y1="5" x2="12" y2="19" />
    <line x1="5" y1="12" x2="19" y2="12" />
  </svg>
);

// ── Nav config ─────────────────────────────────────────────────────────────

interface NavItem { to: string; label: string; icon: React.ReactNode; end?: boolean; }

const nav: { heading: string; items: NavItem[] }[] = [
  {
    heading: 'FLAGS',
    items: [{ to: '/', label: 'All Flags', icon: <IconFlag />, end: true }],
  },
  {
    heading: 'OPERATIONS',
    items: [
      { to: '/incident',    label: 'What Changed?', icon: <IconLightning /> },
      { to: '/approvals',   label: 'Approvals',     icon: <IconCheckCircle /> },
      { to: '/break-glass', label: 'Break-Glass',   icon: <IconShield /> },
    ],
  },
  {
    heading: 'INTELLIGENCE',
    items: [
      { to: '/graph',       label: 'Causal Graph',  icon: <IconShare /> },
      { to: '/governance',  label: 'Governance',    icon: <IconBarChart /> },
      { to: '/experiments', label: 'Experiments',   icon: <IconBeaker /> },
      { to: '/marketplace', label: 'Marketplace',   icon: <IconPuzzle /> },
    ],
  },
];

// Map route → page name for breadcrumb
const PAGE_NAMES: Record<string, string> = {
  '/':            'All Flags',
  '/incident':    'What Changed?',
  '/approvals':   'Approvals',
  '/break-glass': 'Break-Glass',
  '/graph':       'Causal Graph',
  '/governance':  'Governance',
  '/experiments': 'Experiments',
  '/marketplace': 'Marketplace',
};

const ENVIRONMENTS = ['development', 'staging', 'production'] as const;
type Env = typeof ENVIRONMENTS[number];

const ENV_COLORS: Record<Env, { bg: string; text: string; dot: string }> = {
  development: { bg: '#1a2e1a', text: '#4ade80', dot: '#4ade80' },
  staging:     { bg: '#2e2a1a', text: '#fbbf24', dot: '#fbbf24' },
  production:  { bg: '#1a1f2e', text: '#3b82f6', dot: '#3b82f6' },
};

// ── Component ──────────────────────────────────────────────────────────────

export default function App() {
  const location = useLocation();
  const [env, setEnv] = useState<Env>('production');
  const [envOpen, setEnvOpen] = useState(false);
  const [cmdOpen, setCmdOpen] = useState(false);
  const [showFeed, setShowFeed] = useState(false);
  const [flags] = useState<{ key: string; name: string; state: string }[]>([]);

  const openCmd = useCallback(() => setCmdOpen(true), []);
  const showShortcuts = useCallback(() => console.log('TODO: show shortcut map'), []);

  useKeyboard({ 'cmd+k': openCmd, 'escape': () => { setCmdOpen(false); setEnvOpen(false); }, '?': showShortcuts });

  const pageName = (() => {
    if (location.pathname.startsWith('/flags/')) return 'Flag Detail';
    return PAGE_NAMES[location.pathname] ?? 'Dashboard';
  })();

  const envStyle = ENV_COLORS[env];

  return (
    <div
      style={{
        display: 'flex',
        width: '100%',
        height: '100vh',
        overflow: 'hidden',
        background: '#0a0a0a',
        color: '#e5e7eb',
        fontFamily: '"Inter", system-ui, -apple-system, sans-serif',
      }}
    >
      {/* ── Sidebar ─────────────────────────────────────────────────────── */}
      <aside
        style={{
          width: '220px',
          flexShrink: 0,
          display: 'flex',
          flexDirection: 'column',
          background: '#111111',
          borderRight: '1px solid #1a1a1a',
          overflowY: 'auto',
        }}
      >
        {/* Logo */}
        <div
          style={{
            padding: '20px 16px 18px',
            borderBottom: '1px solid #1a1a1a',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
            <div
              style={{
                width: '32px',
                height: '32px',
                borderRadius: '6px',
                background: '#3b82f6',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: '13px',
                fontWeight: '700',
                color: '#ffffff',
                flexShrink: 0,
                letterSpacing: '-0.5px',
              }}
            >
              TS
            </div>
            <div>
              <div style={{ fontSize: '14px', fontWeight: '700', color: '#f9fafb', lineHeight: '1.2' }}>
                Tombstone
              </div>
              <div style={{ fontSize: '11px', color: '#4b5563', marginTop: '2px', lineHeight: '1' }}>
                Production Intelligence
              </div>
            </div>
          </div>
        </div>

        {/* Nav */}
        <nav style={{ flex: 1, padding: '12px 8px', display: 'flex', flexDirection: 'column', gap: '20px' }}>
          {nav.map(section => (
            <div key={section.heading}>
              <p
                style={{
                  padding: '0 10px',
                  marginBottom: '4px',
                  fontSize: '10px',
                  fontWeight: '600',
                  letterSpacing: '0.08em',
                  color: '#374151',
                  textTransform: 'uppercase',
                }}
              >
                {section.heading}
              </p>
              <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: '1px' }}>
                {section.items.map(item => (
                  <li key={item.to}>
                    <NavLink
                      to={item.to}
                      end={item.end}
                      style={({ isActive }) => ({
                        display: 'flex',
                        alignItems: 'center',
                        gap: '9px',
                        padding: '7px 10px',
                        borderRadius: '6px',
                        fontSize: '13px',
                        fontWeight: '500',
                        textDecoration: 'none',
                        transition: 'background 0.1s, color 0.1s',
                        borderLeft: isActive ? '2px solid #3b82f6' : '2px solid transparent',
                        paddingLeft: isActive ? '8px' : '10px',
                        background: isActive ? 'rgba(59,130,246,0.08)' : 'transparent',
                        color: isActive ? '#3b82f6' : '#6b7280',
                      })}
                      onMouseEnter={e => {
                        const el = e.currentTarget as HTMLAnchorElement;
                        if (!el.dataset.active) {
                          el.style.background = '#1f2937';
                          el.style.color = '#d1d5db';
                        }
                      }}
                      onMouseLeave={e => {
                        const el = e.currentTarget as HTMLAnchorElement;
                        if (!el.dataset.active) {
                          el.style.background = 'transparent';
                          el.style.color = '#6b7280';
                        }
                      }}
                    >
                      {({ isActive }) => (
                        <>
                          <span
                            style={{
                              flexShrink: 0,
                              width: '15px',
                              height: '15px',
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'center',
                              color: isActive ? '#3b82f6' : '#4b5563',
                            }}
                          >
                            {item.icon}
                          </span>
                          {item.label}
                        </>
                      )}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>

        {/* Footer */}
        <div
          style={{
            padding: '12px 16px',
            borderTop: '1px solid #1a1a1a',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '11px', color: '#4b5563' }}>
            <div
              style={{
                width: '6px',
                height: '6px',
                borderRadius: '50%',
                background: '#22c55e',
                flexShrink: 0,
                boxShadow: '0 0 4px #22c55e',
              }}
            />
            <span style={{ color: '#6b7280' }}>All systems operational</span>
          </div>
          <div style={{ marginTop: '4px', fontSize: '11px', color: '#374151' }}>
            v2.0.1
          </div>
        </div>
      </aside>

      {/* ── Main area ────────────────────────────────────────────────────── */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>

        {/* Top header */}
        <header
          style={{
            height: '48px',
            flexShrink: 0,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            padding: '0 20px',
            background: '#111111',
            borderBottom: '1px solid #1a1a1a',
          }}
        >
          {/* Breadcrumb */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '13px' }}>
            <span style={{ color: '#4b5563', fontWeight: '500' }}>Tombstone</span>
            <span style={{ color: '#2d3748', fontSize: '16px', lineHeight: '1', marginTop: '-1px' }}>/</span>
            <span style={{ color: '#d1d5db', fontWeight: '500' }}>{pageName}</span>
          </div>

          {/* Right controls */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            {/* Environment selector */}
            <div style={{ position: 'relative' }}>
              <button
                onClick={() => setEnvOpen(o => !o)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '6px',
                  padding: '4px 10px 4px 8px',
                  borderRadius: '20px',
                  background: envStyle.bg,
                  border: `1px solid ${envStyle.dot}22`,
                  color: envStyle.text,
                  fontSize: '12px',
                  fontWeight: '500',
                  cursor: 'pointer',
                  outline: 'none',
                  letterSpacing: '0.01em',
                }}
              >
                <div
                  style={{
                    width: '6px',
                    height: '6px',
                    borderRadius: '50%',
                    background: envStyle.dot,
                    flexShrink: 0,
                    boxShadow: `0 0 4px ${envStyle.dot}`,
                  }}
                />
                {env}
                <span style={{ opacity: 0.7 }}>
                  <IconChevronDown />
                </span>
              </button>

              {envOpen && (
                <div
                  style={{
                    position: 'absolute',
                    top: 'calc(100% + 6px)',
                    right: 0,
                    background: '#1a1a1a',
                    border: '1px solid #2a2a2a',
                    borderRadius: '8px',
                    overflow: 'hidden',
                    zIndex: 100,
                    minWidth: '140px',
                    boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
                  }}
                >
                  {ENVIRONMENTS.map(e => {
                    const c = ENV_COLORS[e];
                    return (
                      <button
                        key={e}
                        onClick={() => { setEnv(e); setEnvOpen(false); }}
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          gap: '8px',
                          width: '100%',
                          padding: '8px 12px',
                          background: e === env ? '#242424' : 'transparent',
                          border: 'none',
                          color: e === env ? c.text : '#9ca3af',
                          fontSize: '13px',
                          fontWeight: '500',
                          cursor: 'pointer',
                          textAlign: 'left',
                          transition: 'background 0.1s',
                        }}
                        onMouseEnter={ev => { (ev.currentTarget as HTMLButtonElement).style.background = '#242424'; }}
                        onMouseLeave={ev => { (ev.currentTarget as HTMLButtonElement).style.background = e === env ? '#242424' : 'transparent'; }}
                      >
                        <div
                          style={{
                            width: '6px',
                            height: '6px',
                            borderRadius: '50%',
                            background: c.dot,
                            flexShrink: 0,
                          }}
                        />
                        {e}
                      </button>
                    );
                  })}
                </div>
              )}
            </div>

            {/* Live feed toggle */}
            <button
              onClick={() => setShowFeed(v => !v)}
              title="Toggle live feed"
              style={{
                width: 32, height: 32, borderRadius: 8,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                background: showFeed ? 'color-mix(in oklab, var(--color-accent) 10%, transparent)' : 'var(--color-bg-elevated, #1a1a1a)',
                border: `1px solid ${showFeed ? 'var(--color-accent, #3b82f6)' : 'var(--color-border, #2a2a2a)'}`,
                color: showFeed ? 'var(--color-accent, #3b82f6)' : 'var(--color-fg-muted, #6b7280)',
                cursor: 'pointer',
              }}
            >
              <Activity size={14} />
            </button>

            {/* Search / Cmd+K button */}
            <button
              onClick={() => setCmdOpen(true)}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '5px 12px', borderRadius: 8, fontSize: 12,
                background: 'var(--color-bg-elevated, #1a1a1a)',
                border: '1px solid var(--color-border, #2a2a2a)',
                color: 'var(--color-fg-muted, #6b7280)',
                cursor: 'pointer',
              }}
            >
              <span>Search…</span>
              <kbd style={{ fontSize: 10, padding: '1px 4px', borderRadius: 3, background: 'var(--color-bg-surface, #111111)', border: '1px solid var(--color-border, #2a2a2a)' }}>⌘K</kbd>
            </button>

            {/* New Flag button */}
            <button
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '5px',
                padding: '5px 12px',
                borderRadius: '6px',
                background: '#3b82f6',
                border: 'none',
                color: '#ffffff',
                fontSize: '13px',
                fontWeight: '600',
                cursor: 'pointer',
                outline: 'none',
                transition: 'background 0.1s',
                letterSpacing: '0.01em',
              }}
              onMouseEnter={e => { (e.currentTarget as HTMLButtonElement).style.background = '#2563eb'; }}
              onMouseLeave={e => { (e.currentTarget as HTMLButtonElement).style.background = '#3b82f6'; }}
            >
              <IconPlus />
              New Flag
            </button>
          </div>
        </header>

        {/* Page content */}
        <div style={{ display: 'flex', flex: 1, overflow: 'hidden' }}>
          <main style={{ flex: 1, overflowY: 'auto', viewTransitionName: 'view-body' } as React.CSSProperties}>
            <Routes>
              <Route path="/"                    element={<FlagList />} />
              <Route path="/flags/:key"          element={<FlagDetail />} />
              <Route path="/flags/:key/slo"      element={<SLOView />} />
              <Route path="/incident"            element={<IncidentTimeline />} />
              <Route path="/graph"               element={<DependencyGraph />} />
              <Route path="/governance"          element={<GovernanceDash />} />
              <Route path="/approvals"           element={<ApprovalQueue />} />
              <Route path="/break-glass"         element={<BreakGlassView />} />
              <Route path="/experiments"         element={<Experiments />} />
              <Route path="/marketplace"         element={<Marketplace />} />
            </Routes>
          </main>
          {showFeed && <LiveFeed env={env} />}
        </div>
      </div>

      {/* Click-away overlay for env dropdown */}
      {envOpen && (
        <div
          onClick={() => setEnvOpen(false)}
          style={{
            position: 'fixed',
            inset: 0,
            zIndex: 99,
          }}
        />
      )}

      {/* Command Palette */}
      <CommandPalette open={cmdOpen} onClose={() => setCmdOpen(false)} flags={flags} />
    </div>
  );
}
