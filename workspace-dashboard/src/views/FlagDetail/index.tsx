import { useState, useCallback } from 'react';
import { useParams, Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import * as AlertDialog from '@radix-ui/react-alert-dialog';
import { FlagHealthBadge } from '../../components/FlagHealthBadge.js';
import { CircuitBreakerStatus } from '../../components/CircuitBreakerStatus.js';
import { AutonomousRolloutToggle } from '../../components/AutonomousRolloutToggle.js';
import { useOptimisticToggle } from '../../hooks/useOptimisticToggle.js';
import { useEnvSnapshot } from '../../hooks/useFlags.js';
import { API_URL, SDK_TOKEN } from '../../config.js';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface AuditEntry {
  id: string;
  flag_key: string;
  environment: string;
  actor: string;
  event_type: string;
  prev_state: unknown;
  new_state: unknown;
  created_at: number;
}

interface FlagDetailType {
  id: string;
  key: string;
  name: string;
  description: string;
  flag_type: string;
  state: 'DRAFT' | 'ACTIVE' | 'COMPLETE' | 'ARCHIVED';
  owner_id: string;
  created_at: number;
  updated_at: number;
}

// Matches the shape coming from useEnvSnapshot
interface EnvStateRow {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
  safe_default?: string;
  updated_at?: number;
}

type Env = 'development' | 'staging' | 'production';
const ENVS: Env[] = ['development', 'staging', 'production'];

const envConfig: Record<Env, {
  label: string;
  accent: string;
  bar: string;
  badge: string;
  toggleOn: string;
  border: string;
}> = {
  development: {
    label: 'Development',
    accent: 'text-blue-400',
    bar: 'bg-blue-500',
    badge: 'bg-blue-900/40 text-blue-300 border border-blue-800',
    toggleOn: 'bg-blue-500',
    border: 'border-blue-800/50',
  },
  staging: {
    label: 'Staging',
    accent: 'text-amber-400',
    bar: 'bg-amber-500',
    badge: 'bg-amber-900/40 text-amber-300 border border-amber-800',
    toggleOn: 'bg-amber-500',
    border: 'border-amber-800/50',
  },
  production: {
    label: 'Production',
    accent: 'text-emerald-400',
    bar: 'bg-emerald-500',
    badge: 'bg-emerald-900/40 text-emerald-300 border border-emerald-800',
    toggleOn: 'bg-emerald-500',
    border: 'border-emerald-800/50',
  },
};

const flagTypeBadge: Record<string, string> = {
  boolean: 'bg-sky-900/50 text-sky-300 border border-sky-800',
  multivariate: 'bg-violet-900/50 text-violet-300 border border-violet-800',
  experiment: 'bg-pink-900/50 text-pink-300 border border-pink-800',
};

const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

// ---------------------------------------------------------------------------
// Small sub-components
// ---------------------------------------------------------------------------

function SectionCard({ children, className = '', id }: { children: React.ReactNode; className?: string; id?: string }) {
  return (
    <div
      id={id}
      className={`rounded-xl border p-6 ${className}`}
      style={{ background: '#111', borderColor: '#1a1a1a' }}
    >
      {children}
    </div>
  );
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <h2 className="text-sm font-semibold tracking-widest text-gray-400 uppercase">{children}</h2>
      <div className="mt-2 h-px" style={{ background: '#1a1a1a' }} />
    </div>
  );
}

function ToggleVisual({ enabled, envKey }: { enabled: boolean; envKey: Env }) {
  const cfg = envConfig[envKey];
  return (
    <div className="flex items-center gap-2">
      <div
        className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors ${
          enabled ? cfg.toggleOn : 'bg-gray-700'
        }`}
      >
        <span
          className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${
            enabled ? 'translate-x-4' : 'translate-x-1'
          }`}
        />
      </div>
      <span
        className={`text-xs font-semibold tracking-wide ${
          enabled ? cfg.accent : 'text-gray-500'
        }`}
      >
        {enabled ? 'ON' : 'OFF'}
      </span>
    </div>
  );
}

function RolloutBar({ pct, envKey }: { pct: number; envKey: Env }) {
  const cfg = envConfig[envKey];
  return (
    <div className="flex items-center gap-3">
      <div className="flex-1 rounded-full h-1.5" style={{ background: '#252525' }}>
        <div
          className={`h-1.5 rounded-full transition-all ${cfg.bar}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-white text-xs font-mono font-semibold w-9 text-right">{pct}%</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// QuickActions — context strip mounted between flag header and env section
// ---------------------------------------------------------------------------
interface QuickActionsProps {
  flagKey: string;
  activeEnv: Env;
  enabled: boolean;
  isPending: boolean;
  onToggle: () => void;
  onRollback: () => void;
}

function QuickActions({ flagKey, activeEnv, enabled, isPending, onToggle, onRollback }: QuickActionsProps) {
  const [copied, setCopied] = useState(false);

  const handleClone = useCallback(() => {
    navigator.clipboard.writeText(flagKey).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }).catch(() => null);
  }, [flagKey]);

  const handleAudit = useCallback(() => {
    const el = document.getElementById('audit-log');
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  }, []);

  const envCfg = envConfig[activeEnv];

  return (
    <div
      className="flex items-center gap-2 flex-wrap mb-8 px-4 py-3 rounded-xl border"
      style={{ background: '#0d0d0d', borderColor: '#1a1a1a' }}
    >
      {/* Rollback button with confirmation dialog */}
      <AlertDialog.Root>
        <AlertDialog.Trigger asChild>
          <button
            disabled={isPending}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            style={{
              background: 'color-mix(in oklab, #ef4444 10%, transparent)',
              border: '1px solid color-mix(in oklab, #ef4444 25%, transparent)',
              color: '#f87171',
            }}
          >
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <path d="M3 8a5 5 0 1 1 1.5 3.6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
              <path d="M3 11.5V8H6.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/>
            </svg>
            Rollback
          </button>
        </AlertDialog.Trigger>

        <AlertDialog.Portal>
          <AlertDialog.Overlay
            className="fixed inset-0 z-50"
            style={{ background: 'rgba(0,0,0,0.7)', backdropFilter: 'blur(2px)' }}
          />
          <AlertDialog.Content
            className="fixed z-50 top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 rounded-xl p-6 shadow-2xl w-full max-w-md"
            style={{ background: '#111', border: '1px solid #2a2a2a' }}
          >
            <AlertDialog.Title className="text-base font-semibold text-white mb-2">
              Roll back <span className="text-red-400 font-mono">{flagKey}</span>?
            </AlertDialog.Title>
            <AlertDialog.Description className="text-sm text-gray-400 mb-6 leading-relaxed">
              This will immediately disable the flag in{' '}
              <span className={`font-semibold ${envCfg.accent}`}>{envCfg.label}</span>{' '}
              using the kill switch. All targeting rules will be preserved and the flag can be re-enabled at any time.
            </AlertDialog.Description>
            <div className="flex items-center justify-end gap-3">
              <AlertDialog.Cancel asChild>
                <button
                  className="px-4 py-2 rounded-lg text-sm font-medium transition-colors"
                  style={{
                    background: '#1a1a1a',
                    border: '1px solid #2a2a2a',
                    color: '#9ca3af',
                  }}
                >
                  Cancel
                </button>
              </AlertDialog.Cancel>
              <AlertDialog.Action asChild>
                <button
                  onClick={onRollback}
                  className="px-4 py-2 rounded-lg text-sm font-semibold transition-colors"
                  style={{
                    background: '#7f1d1d',
                    border: '1px solid #991b1b',
                    color: '#fca5a5',
                  }}
                >
                  Confirm Rollback
                </button>
              </AlertDialog.Action>
            </div>
          </AlertDialog.Content>
        </AlertDialog.Portal>
      </AlertDialog.Root>

      {/* Toggle enabled/disabled */}
      <button
        onClick={onToggle}
        disabled={isPending}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        style={{
          background: enabled
            ? 'color-mix(in oklab, #f59e0b 10%, transparent)'
            : 'color-mix(in oklab, #22c55e 10%, transparent)',
          border: enabled
            ? '1px solid color-mix(in oklab, #f59e0b 25%, transparent)'
            : '1px solid color-mix(in oklab, #22c55e 25%, transparent)',
          color: enabled ? '#fbbf24' : '#4ade80',
        }}
      >
        {enabled ? (
          <>
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <rect x="2" y="5" width="12" height="8" rx="1.5" stroke="currentColor" strokeWidth="1.5"/>
              <path d="M5.5 5V3.5a2.5 2.5 0 0 1 5 0V5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
            </svg>
            Disable
          </>
        ) : (
          <>
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <rect x="2" y="7" width="12" height="7" rx="1.5" stroke="currentColor" strokeWidth="1.5"/>
              <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
            </svg>
            Enable
          </>
        )}
      </button>

      {/* Clone — copies flag key to clipboard */}
      <button
        onClick={handleClone}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold transition-colors"
        style={{
          background: 'color-mix(in oklab, #38bdf8 8%, transparent)',
          border: '1px solid color-mix(in oklab, #38bdf8 20%, transparent)',
          color: copied ? '#4ade80' : '#7dd3fc',
        }}
      >
        {copied ? (
          <>
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <path d="M3 8l3.5 3.5L13 4" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"/>
            </svg>
            Copied!
          </>
        ) : (
          <>
            <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <rect x="5" y="5" width="8" height="9" rx="1.5" stroke="currentColor" strokeWidth="1.5"/>
              <path d="M10 5V3.5A1.5 1.5 0 0 0 8.5 2H3.5A1.5 1.5 0 0 0 2 3.5v7A1.5 1.5 0 0 0 3.5 12H5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
            </svg>
            Clone Key
          </>
        )}
      </button>

      {/* Audit — smooth scroll to audit log section */}
      <button
        onClick={handleAudit}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold transition-colors"
        style={{
          background: 'color-mix(in oklab, #a78bfa 8%, transparent)',
          border: '1px solid color-mix(in oklab, #a78bfa 20%, transparent)',
          color: '#c4b5fd',
        }}
      >
        <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <path d="M2 4h12M2 8h8M2 12h5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
        </svg>
        Audit Log
      </button>

      {/* Env context pill — shows which environment the actions target */}
      <span
        className={`ml-auto inline-flex items-center gap-1 px-2 py-1 rounded-md text-xs font-medium ${envCfg.badge}`}
        title="Actions target this environment"
      >
        <span className="w-1.5 h-1.5 rounded-full" style={{ background: 'currentColor', opacity: 0.7 }} />
        {envCfg.label}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// EnvToggleButton — isolated per-env optimistic toggle
// ---------------------------------------------------------------------------

function EnvToggleButton({
  flagKey,
  env,
  currentEnvState,
}: {
  flagKey: string;
  env: Env;
  currentEnvState: EnvStateRow | undefined;
}) {
  const { enabled, toggle, isPending } = useOptimisticToggle(
    flagKey,
    env,
    {
      enabled: currentEnvState?.enabled ?? false,
      rolloutPct: currentEnvState?.rollout_pct ?? 0,
    },
  );

  return (
    <button
      onClick={toggle}
      disabled={isPending}
      style={{
        padding: '8px 16px',
        borderRadius: 8,
        border: 'none',
        background: enabled ? 'var(--color-risk-high, #7f1d1d)' : 'var(--color-accent, #3b82f6)',
        color: enabled ? '#fff' : '#07080d',
        fontSize: 13,
        fontWeight: 600,
        cursor: isPending ? 'not-allowed' : 'pointer',
        opacity: isPending ? 0.7 : 1,
        transition: 'all 0.15s ease',
      }}
    >
      {isPending ? '…' : enabled ? 'Disable' : 'Enable'}
    </button>
  );
}

// ---------------------------------------------------------------------------
// ActiveEnvQuickActions — bridges useOptimisticToggle into QuickActions
// ---------------------------------------------------------------------------

function ActiveEnvQuickActions({
  flagKey,
  activeEnv,
  currentEnvState,
}: {
  flagKey: string;
  activeEnv: Env;
  currentEnvState: EnvStateRow | undefined;
}) {
  const { enabled, toggle, isPending } = useOptimisticToggle(
    flagKey,
    activeEnv,
    {
      enabled: currentEnvState?.enabled ?? false,
      rolloutPct: currentEnvState?.rollout_pct ?? 0,
    },
  );

  const [rollbackPending, setRollbackPending] = useState(false);

  const handleRollback = useCallback(async () => {
    if (!flagKey || rollbackPending) return;
    setRollbackPending(true);
    try {
      await fetch(`${API_URL}/api/v1/flags/${flagKey}/environments/${activeEnv}`, {
        method: 'PATCH',
        headers: { ...hdrs, 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: false }),
      });
    } catch {
      // silently ignore — state will reconcile on next snapshot refresh
    } finally {
      setRollbackPending(false);
    }
  }, [flagKey, activeEnv, rollbackPending]);

  return (
    <QuickActions
      flagKey={flagKey}
      activeEnv={activeEnv}
      enabled={enabled}
      isPending={isPending || rollbackPending}
      onToggle={toggle}
      onRollback={() => { void handleRollback(); }}
    />
  );
}

// ---------------------------------------------------------------------------
// Main view
// ---------------------------------------------------------------------------

export default function FlagDetail() {
  const { key: flagKey } = useParams<{ key: string }>();
  const [activeEnv, setActiveEnv] = useState<Env>('production');

  // ── Flag detail query ───────────────────────────────────────────────────
  const {
    data: flag,
    isLoading: flagLoading,
    error: flagError,
  } = useQuery({
    queryKey: ['flag', flagKey],
    queryFn: async (): Promise<FlagDetailType> => {
      const r = await fetch(`${API_URL}/api/v1/flags/${flagKey}`, { headers: hdrs });
      if (!r.ok) throw new Error('Flag not found');
      return r.json() as Promise<FlagDetailType>;
    },
    enabled: !!flagKey,
  });

  // ── Audit log query ─────────────────────────────────────────────────────
  const { data: auditData } = useQuery({
    queryKey: ['audit', flagKey],
    queryFn: async (): Promise<AuditEntry[]> => {
      const r = await fetch(
        `${API_URL}/api/v1/audit?flag_key=${flagKey}&limit=20`,
        { headers: hdrs },
      );
      if (!r.ok) return [];
      const d = await r.json() as { entries?: AuditEntry[] };
      return d.entries ?? [];
    },
    enabled: !!flagKey,
  });
  const audit = auditData ?? [];

  // ── Per-env snapshot queries (one per env, re-uses shared cache key) ────
  const { data: devSnap }  = useEnvSnapshot('development');
  const { data: stgSnap }  = useEnvSnapshot('staging');
  const { data: prodSnap } = useEnvSnapshot('production');

  const snapByEnv: Record<Env, Record<string, EnvStateRow>> = {
    development: (devSnap  ?? {}) as Record<string, EnvStateRow>,
    staging:     (stgSnap  ?? {}) as Record<string, EnvStateRow>,
    production:  (prodSnap ?? {}) as Record<string, EnvStateRow>,
  };

  // ── Derived per-env state ───────────────────────────────────────────────
  const envStates: Record<Env, EnvStateRow | undefined> = {
    development: flagKey ? snapByEnv.development[flagKey] : undefined,
    staging:     flagKey ? snapByEnv.staging[flagKey]     : undefined,
    production:  flagKey ? snapByEnv.production[flagKey]  : undefined,
  };

  const activeEnvState = envStates[activeEnv];

  // ── Loading / error states ──────────────────────────────────────────────
  if (flagLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center" style={{ background: '#0a0a0a' }}>
        <div className="flex items-center gap-3 text-gray-500">
          <div className="w-4 h-4 rounded-full border-2 border-blue-500 border-t-transparent animate-spin" />
          <span className="text-sm">Loading flag…</span>
        </div>
      </div>
    );
  }

  if (flagError || !flag) {
    return (
      <div className="min-h-screen p-8" style={{ background: '#0a0a0a' }}>
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 text-sm transition-colors hover:text-gray-300"
          style={{ color: '#6b7280' }}
        >
          ← All Flags
        </Link>
        <p className="text-red-400 mt-6 text-sm">
          {flagError ? String(flagError) : 'Flag not found'}
        </p>
      </div>
    );
  }

  const typeBadgeClass = flagTypeBadge[flag.flag_type] ?? 'bg-gray-800 text-gray-300 border border-gray-700';

  return (
    <div className="min-h-screen px-6 py-8" style={{ background: '#0a0a0a' }}>
      <div className="max-w-7xl mx-auto">

        {/* Back link */}
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 text-sm mb-6 transition-colors hover:text-gray-300"
          style={{ color: '#6b7280' }}
        >
          ← All Flags
        </Link>

        {/* Flag header */}
        <div className="flex items-start justify-between mb-8">
          <div className="flex-1 min-w-0 mr-6">
            <h1 className="text-3xl font-bold font-mono tracking-tight text-blue-400 truncate">
              {flag.key}
            </h1>
            <p className="text-gray-300 mt-1.5 text-base">{flag.name}</p>
            {flag.description && (
              <p className="text-gray-500 text-sm mt-1">{flag.description}</p>
            )}
          </div>
          <div className="flex items-center gap-3 shrink-0">
            <Link
              to={`/flags/${flag.key}/slo`}
              className="px-3 py-1.5 rounded-lg text-xs font-medium border border-blue-800 text-blue-400 hover:bg-blue-900/20 transition-colors"
            >
              View SLO →
            </Link>
            <FlagHealthBadge state={flag.state} />
          </div>
        </div>

        {/* Quick-actions context strip */}
        <ActiveEnvQuickActions
          flagKey={flag.key}
          activeEnv={activeEnv}
          currentEnvState={activeEnvState}
        />

        {/* Two-column layout */}
        <div className="flex gap-6 items-start flex-col xl:flex-row">

          {/* LEFT — main content (2/3) */}
          <div className="flex-1 min-w-0 space-y-6">

            {/* Overview section */}
            <SectionCard>
              <SectionTitle>Overview</SectionTitle>
              <div className="grid grid-cols-2 sm:grid-cols-3 gap-4 text-sm">
                <div>
                  <div className="text-gray-500 text-xs mb-1">Flag Type</div>
                  <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${typeBadgeClass}`}>
                    {flag.flag_type}
                  </span>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Owner</div>
                  <div className="text-gray-300 font-mono text-xs truncate">{flag.owner_id}</div>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Created</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.created_at * 1000).toLocaleDateString()}</div>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Last Modified</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.updated_at * 1000).toLocaleDateString()}</div>
                </div>
                <div>
                  <div className="text-gray-500 text-xs mb-1">Flag ID</div>
                  <div className="text-gray-500 font-mono text-xs truncate">{flag.id}</div>
                </div>
              </div>
            </SectionCard>

            {/* Environments section */}
            <SectionCard>
              <SectionTitle>Environments</SectionTitle>
              <div className="space-y-3">
                {ENVS.map(env => {
                  const cfg = envConfig[env];
                  const state = envStates[env];
                  return (
                    <div
                      key={env}
                      className={`rounded-lg border p-4 transition-colors cursor-pointer ${
                        activeEnv === env
                          ? `${cfg.border} bg-white/[0.03]`
                          : 'border-transparent hover:border-gray-800'
                      }`}
                      style={activeEnv !== env ? { borderColor: '#181818', background: '#0d0d0d' } : {}}
                      onClick={() => setActiveEnv(env)}
                    >
                      <div className="flex items-center justify-between mb-3">
                        <div className="flex items-center gap-2.5">
                          <span className={`px-2 py-0.5 rounded text-xs font-semibold ${cfg.badge}`}>
                            {cfg.label}
                          </span>
                          {state && <ToggleVisual enabled={state.enabled} envKey={env} />}
                        </div>
                        <div className="flex items-center gap-3">
                          {state?.updated_at != null && (
                            <span className="text-gray-500 text-xs">
                              Updated {new Date(state.updated_at * 1000).toLocaleDateString()}
                            </span>
                          )}
                          {/* Per-env optimistic toggle button */}
                          {flagKey && (
                            <div onClick={e => e.stopPropagation()}>
                              <EnvToggleButton
                                flagKey={flagKey}
                                env={env}
                                currentEnvState={state}
                              />
                            </div>
                          )}
                        </div>
                      </div>

                      {state ? (
                        <div className="space-y-2">
                          <div>
                            <div className="flex items-center justify-between mb-1">
                              <span className="text-gray-500 text-xs">Rollout</span>
                            </div>
                            <RolloutBar pct={state.rollout_pct} envKey={env} />
                          </div>
                          {state.safe_default != null && (
                            <div className="flex items-center gap-4 mt-2 text-xs">
                              <span className="text-gray-500">
                                Safe default: <code className="text-amber-300 ml-1">{state.safe_default}</code>
                              </span>
                            </div>
                          )}
                        </div>
                      ) : (
                        <p className="text-gray-600 text-xs">No state for this environment yet.</p>
                      )}
                    </div>
                  );
                })}
              </div>
            </SectionCard>

            {/* Targeting Rules section (active env detail) */}
            <SectionCard>
              <SectionTitle>Targeting Rules</SectionTitle>

              <div className="flex items-center justify-between mb-4">
                <div className="flex items-center gap-2 text-sm text-gray-300">
                  <span className={`px-2 py-0.5 rounded text-xs font-semibold ${envConfig[activeEnv].badge}`}>
                    {envConfig[activeEnv].label}
                  </span>
                  <span className="text-gray-500">environment selected</span>
                </div>
                <CircuitBreakerStatus flagKey={flag.key} />
              </div>

              {activeEnvState ? (
                <div className="space-y-4">
                  <div className="grid grid-cols-2 gap-3 text-sm">
                    <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                      <div className="text-gray-500 text-xs mb-1.5">Status</div>
                      <ToggleVisual enabled={activeEnvState.enabled} envKey={activeEnv} />
                    </div>
                    <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                      <div className="text-gray-500 text-xs mb-1.5">Rollout %</div>
                      <RolloutBar pct={activeEnvState.rollout_pct} envKey={activeEnv} />
                    </div>
                    {activeEnvState.safe_default != null && (
                      <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                        <div className="text-gray-500 text-xs mb-1.5">Safe Default</div>
                        <code className="text-amber-300 text-sm">{activeEnvState.safe_default}</code>
                      </div>
                    )}
                    {activeEnvState.updated_at != null && (
                      <div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
                        <div className="text-gray-500 text-xs mb-1.5">Last Updated</div>
                        <div className="text-gray-300 text-xs">
                          {new Date(activeEnvState.updated_at * 1000).toLocaleString()}
                        </div>
                      </div>
                    )}
                  </div>

                  {/* Primary optimistic toggle for active env */}
                  <div className="pt-4 border-t" style={{ borderColor: '#1a1a1a' }}>
                    <div className="flex items-center gap-4">
                      {flagKey && (
                        <EnvToggleButton
                          flagKey={flagKey}
                          env={activeEnv}
                          currentEnvState={activeEnvState}
                        />
                      )}
                      <p className="text-gray-600 text-xs">
                        Optimistic toggle — updates instantly, reverts automatically on API error.
                      </p>
                    </div>
                  </div>

                  {/* Autonomous rollout */}
                  {activeEnvState.enabled && (
                    <AutonomousRolloutToggle
                      flagKey={flag.key}
                      environment={activeEnv}
                      currentRolloutPct={activeEnvState.rollout_pct}
                    />
                  )}
                </div>
              ) : (
                <p className="text-gray-600 text-sm">No targeting rules configured for {activeEnv} yet.</p>
              )}
            </SectionCard>

          </div>

          {/* RIGHT — metadata sidebar (1/3) */}
          <div className="w-full xl:w-80 shrink-0 space-y-6">

            {/* Metadata card */}
            <SectionCard>
              <SectionTitle>Metadata</SectionTitle>
              <div className="space-y-4 text-sm">
                <div>
                  <div className="text-gray-500 text-xs mb-1.5">Flag Type</div>
                  <span className={`inline-block px-2.5 py-1 rounded-lg text-xs font-medium ${typeBadgeClass}`}>
                    {flag.flag_type}
                  </span>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Owner</div>
                  <div className="text-gray-300 font-mono text-xs break-all">{flag.owner_id}</div>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Created</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.created_at * 1000).toLocaleString()}</div>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Last Modified</div>
                  <div className="text-gray-300 text-xs">{new Date(flag.updated_at * 1000).toLocaleString()}</div>
                </div>
                <div className="h-px" style={{ background: '#1a1a1a' }} />
                <div>
                  <div className="text-gray-500 text-xs mb-1">Flag ID</div>
                  <div className="text-gray-500 font-mono text-xs break-all">{flag.id}</div>
                </div>
              </div>
            </SectionCard>

            {/* Quick env summary */}
            <SectionCard>
              <SectionTitle>Environment Summary</SectionTitle>
              <div className="space-y-3">
                {ENVS.map(env => {
                  const cfg = envConfig[env];
                  const state = envStates[env];
                  return (
                    <div key={env} className="flex items-center justify-between">
                      <span className={`text-xs font-medium ${cfg.accent}`}>{cfg.label}</span>
                      <div className="flex items-center gap-2">
                        {state ? (
                          <>
                            <span className={`text-xs font-semibold ${state.enabled ? cfg.accent : 'text-gray-600'}`}>
                              {state.enabled ? 'ON' : 'OFF'}
                            </span>
                            <span className="text-gray-600 text-xs font-mono">{state.rollout_pct}%</span>
                          </>
                        ) : (
                          <span className="text-gray-700 text-xs">—</span>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </SectionCard>

            {/* Audit log */}
            <SectionCard id="audit-log">
              <SectionTitle>Audit Log</SectionTitle>
              <div className="text-gray-500 text-xs mb-3">Last 20 entries</div>
              {audit.length === 0 ? (
                <p className="text-gray-600 text-xs text-center py-4">No audit entries yet.</p>
              ) : (
                <div className="space-y-2">
                  {audit.map(entry => (
                    <div
                      key={entry.id}
                      className="rounded-lg p-3 text-xs"
                      style={{ background: '#0d0d0d', border: '1px solid #181818' }}
                    >
                      <div className="flex items-center justify-between mb-1">
                        <span className={`px-1.5 py-0.5 rounded text-xs font-medium ${
                          entry.event_type.includes('kill')
                            ? 'bg-red-950 text-red-400 border border-red-900'
                            : entry.event_type.includes('create')
                            ? 'bg-emerald-950 text-emerald-400 border border-emerald-900'
                            : 'bg-gray-900 text-gray-400 border border-gray-800'
                        }`}>
                          {entry.event_type}
                        </span>
                        <span className={`text-xs font-medium ${envConfig[entry.environment as Env]?.accent ?? 'text-gray-400'}`}>
                          {entry.environment}
                        </span>
                      </div>
                      <div className="flex items-center justify-between text-gray-600 mt-1">
                        <span>{entry.actor}</span>
                        <span>{new Date(entry.created_at * 1000).toLocaleTimeString()}</span>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </SectionCard>

          </div>
        </div>
      </div>
    </div>
  );
}
