import { Routes, Route, NavLink } from 'react-router-dom';
import FlagList from './views/FlagList/index.js';
import FlagDetail from './views/FlagDetail/index.js';
import IncidentTimeline from './views/IncidentTimeline/index.js';
import GovernanceDash from './views/GovernanceDash/index.js';
import ApprovalQueue from './views/ApprovalQueue/index.js';
import BreakGlassView from './views/BreakGlass/index.js';
import Experiments from './views/Experiments/index.js';
import DependencyGraph from './views/DependencyGraph/index.js';
import Marketplace from './views/Marketplace/index.js';

interface NavItem { to: string; label: string; icon: string; end?: boolean; }

const nav: { heading: string; items: NavItem[] }[] = [
  {
    heading: 'Flags',
    items: [{ to: '/', label: 'All Flags', icon: '⚑', end: true }],
  },
  {
    heading: 'Operations',
    items: [
      { to: '/incident',    label: 'What Changed?',  icon: '⚡' },
      { to: '/approvals',   label: 'Approvals',      icon: '✓' },
      { to: '/break-glass', label: 'Break-Glass',    icon: '🔑' },
    ],
  },
  {
    heading: 'Intelligence',
    items: [
      { to: '/graph',       label: 'Causal Graph',   icon: '⬡' },
      { to: '/governance',  label: 'Governance',     icon: '◉' },
      { to: '/experiments', label: 'Experiments',    icon: '⚗' },
      { to: '/marketplace', label: 'Marketplace',    icon: '◈' },
    ],
  },
];

export default function App() {
  return (
    <div className="flex h-screen overflow-hidden" style={{ background: '#080c14', color: '#e6edf3' }}>

      {/* ── Sidebar ────────────────────────────────────── */}
      <aside className="w-56 flex-shrink-0 flex flex-col sidebar-glow overflow-y-auto"
             style={{ background: '#0d1117', borderRight: '1px solid #21262d' }}>

        {/* Logo */}
        <div className="px-4 py-5" style={{ borderBottom: '1px solid #21262d' }}>
          <div className="flex items-center gap-2">
            <div className="w-7 h-7 rounded flex items-center justify-center text-xs font-bold"
                 style={{ background: 'linear-gradient(135deg,#58a6ff,#bc8cff)', color: '#080c14' }}>
              FM
            </div>
            <div>
              <div className="text-sm font-semibold" style={{ color: '#e6edf3' }}>Tombstone</div>
              <div className="text-xs" style={{ color: '#484f58' }}>Production Intelligence</div>
            </div>
          </div>
        </div>

        {/* Nav sections */}
        <nav className="flex-1 px-2 py-3 space-y-5">
          {nav.map(section => (
            <div key={section.heading}>
              <p className="px-3 mb-1 text-xs font-semibold uppercase tracking-wider"
                 style={{ color: '#484f58' }}>
                {section.heading}
              </p>
              <ul className="space-y-0.5">
                {section.items.map(item => (
                  <li key={item.to}>
                    <NavLink to={item.to} end={item.end}
                      className={({ isActive }) =>
                        `flex items-center gap-2.5 px-3 py-1.5 rounded text-sm transition-all duration-150 ${isActive ? 'nav-active' : 'nav-idle hover:bg-white/5'}`
                      }
                      style={({ isActive }) => ({
                        color: isActive ? '#58a6ff' : '#8b949e',
                      })}
                    >
                      <span className="text-base w-4 text-center leading-none">{item.icon}</span>
                      {item.label}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>

        {/* Footer */}
        <div className="px-4 py-3 text-xs" style={{ borderTop: '1px solid #21262d', color: '#484f58' }}>
          <div className="flex items-center gap-1.5">
            <div className="w-1.5 h-1.5 rounded-full" style={{ background: '#3fb950' }} />
            All systems operational
          </div>
          <div className="mt-1">v0.1.0 · Phase 5D</div>
        </div>
      </aside>

      {/* ── Main area ──────────────────────────────────── */}
      <div className="flex-1 flex flex-col overflow-hidden">

        {/* Top bar */}
        <header className="flex items-center justify-between px-6 h-12 flex-shrink-0"
                style={{ background: '#0d1117', borderBottom: '1px solid #21262d' }}>
          <div className="flex items-center gap-2 text-sm" style={{ color: '#8b949e' }}>
            <span>Tombstone</span>
            <span style={{ color: '#484f58' }}>/</span>
            <span style={{ color: '#e6edf3' }}>Dashboard</span>
          </div>
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-1.5 text-xs px-2.5 py-1 rounded"
                 style={{ background: '#161b22', border: '1px solid #21262d', color: '#3fb950' }}>
              <div className="w-1.5 h-1.5 rounded-full" style={{ background: '#3fb950' }} />
              Production
            </div>
          </div>
        </header>

        {/* Page content */}
        <main className="flex-1 overflow-y-auto">
          <Routes>
            <Route path="/"             element={<FlagList />} />
            <Route path="/flags/:key"   element={<FlagDetail />} />
            <Route path="/incident"     element={<IncidentTimeline />} />
            <Route path="/graph"        element={<DependencyGraph />} />
            <Route path="/governance"   element={<GovernanceDash />} />
            <Route path="/approvals"    element={<ApprovalQueue />} />
            <Route path="/break-glass"  element={<BreakGlassView />} />
            <Route path="/experiments"  element={<Experiments />} />
            <Route path="/marketplace"  element={<Marketplace />} />
          </Routes>
        </main>
      </div>
    </div>
  );
}
