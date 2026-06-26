import { useState } from 'react';
import { INTEL_URL } from '../../config.js';
import { EmptyState, Reveal } from '../../components/ui/index.js';

// ── Types ────────────────────────────────────────────────────────────────────

interface ExperimentResult {
  experiment_id: string;
  flag_key: string;
  recommendation: 'SHIP' | 'NO_SHIP' | 'CONTINUE';
  relative_lift: number;
  is_significant: boolean;
  sample_sizes: { control: number; treatment: number };
  probability_beats_control?: number;
  p_value?: number;
}

interface FormState {
  flagKey: string;
  metricName: string;
  metricSql: string;
  eventTable: string;
  flagEventTable: string;
  warehouseType: string;
  warehouseDsn: string;
}

// ── Static mock catalogue (shown when no live result exists) ─────────────────

type ExperimentStatus = 'RUNNING' | 'COMPLETE' | 'DRAFT';

interface CatalogueEntry {
  id: string;
  name: string;
  description: string;
  status: ExperimentStatus;
  sampleSize: number;
  confidenceInterval: [number, number];
  metricLift: number; // fractional, e.g. 0.032 = +3.2%
  flagKey: string;
}

const MOCK_EXPERIMENTS: CatalogueEntry[] = [
  {
    id: 'exp-001',
    name: 'Checkout v2 Redesign',
    description: 'New single-page checkout flow replacing the 3-step wizard.',
    status: 'RUNNING',
    sampleSize: 14_820,
    confidenceInterval: [0.018, 0.048],
    metricLift: 0.032,
    flagKey: 'payments.checkout.checkout-v2',
  },
  {
    id: 'exp-002',
    name: 'Search Autocomplete',
    description: 'Predictive query suggestions in the main search bar.',
    status: 'COMPLETE',
    sampleSize: 52_100,
    confidenceInterval: [0.071, 0.093],
    metricLift: 0.082,
    flagKey: 'search.autocomplete.v3',
  },
  {
    id: 'exp-003',
    name: 'Pricing Page Refresh',
    description: 'Updated tier cards with feature comparison table.',
    status: 'DRAFT',
    sampleSize: 0,
    confidenceInterval: [0, 0],
    metricLift: 0,
    flagKey: 'marketing.pricing.v4',
  },
  {
    id: 'exp-004',
    name: 'Onboarding Checklist',
    description: 'Guided setup checklist shown to new accounts on first login.',
    status: 'RUNNING',
    sampleSize: 3_405,
    confidenceInterval: [-0.009, 0.031],
    metricLift: -0.011,
    flagKey: 'onboarding.checklist.beta',
  },
];

// ── Design tokens ─────────────────────────────────────────────────────────────

const T = {
  bg:        '#0d1117',
  surface:   '#161b22',
  border:    '#21262d',
  borderFocus: '#388bfd',
  text:      '#e6edf3',
  muted:     '#8b949e',
  dim:       '#484f58',
  blue:      '#58a6ff',
  blueDeep:  '#388bfd',
  green:     '#3fb950',
  greenSoft: 'rgba(63,185,80,0.12)',
  greenBorder:'rgba(63,185,80,0.28)',
  red:       '#f85149',
  redSoft:   'rgba(248,81,73,0.12)',
  redBorder: 'rgba(248,81,73,0.28)',
  amber:     '#e3b341',
  amberSoft: 'rgba(227,179,65,0.12)',
  amberBorder:'rgba(227,179,65,0.28)',
  blueSoft:  'rgba(88,166,255,0.1)',
  blueBorder:'rgba(88,166,255,0.25)',
  greySoft:  'rgba(139,148,158,0.08)',
  greyBorder:'rgba(139,148,158,0.2)',
  radius:    10,
  radiusSm:  6,
};

// ── Status badge config ───────────────────────────────────────────────────────

const STATUS_CONFIG: Record<ExperimentStatus, { label: string; color: string; bg: string; border: string; dot: string }> = {
  RUNNING:  { label: 'Running',  color: T.blue,  bg: T.blueSoft,  border: T.blueBorder,  dot: T.blue  },
  COMPLETE: { label: 'Complete', color: T.green, bg: T.greenSoft, border: T.greenBorder, dot: T.green },
  DRAFT:    { label: 'Draft',    color: T.muted, bg: T.greySoft,  border: T.greyBorder,  dot: T.dim   },
};

// ── Recommendation config ─────────────────────────────────────────────────────

const REC_CONFIG: Record<ExperimentResult['recommendation'], { label: string; color: string; bg: string; border: string }> = {
  SHIP:     { label: 'Ship It',     color: T.green, bg: T.greenSoft,  border: T.greenBorder },
  NO_SHIP:  { label: 'Do Not Ship', color: T.red,   bg: T.redSoft,    border: T.redBorder   },
  CONTINUE: { label: 'Continue',    color: T.amber, bg: T.amberSoft,  border: T.amberBorder },
};

// ── Sub-components ────────────────────────────────────────────────────────────

function StatusBadge({ status }: { status: ExperimentStatus }) {
  const c = STATUS_CONFIG[status];
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 5,
      fontSize: 11, fontWeight: 600, padding: '3px 9px', borderRadius: 20,
      background: c.bg, border: `1px solid ${c.border}`, color: c.color,
      letterSpacing: '0.04em', textTransform: 'uppercase',
    }}>
      <span style={{
        width: 6, height: 6, borderRadius: '50%', background: c.dot,
        boxShadow: status === 'RUNNING' ? `0 0 6px ${c.dot}` : 'none',
      }} />
      {c.label}
    </span>
  );
}

function LiftBadge({ lift }: { lift: number }) {
  if (lift === 0) {
    return <span style={{ fontSize: 13, fontWeight: 600, color: T.dim }}>—</span>;
  }
  const positive = lift >= 0;
  return (
    <span style={{
      fontSize: 15, fontWeight: 700,
      color: positive ? T.green : T.red,
    }}>
      {positive ? '+' : ''}{(lift * 100).toFixed(1)}%
    </span>
  );
}

function ExperimentCard({ entry, onAnalyze }: { entry: CatalogueEntry; onAnalyze: (key: string) => void }) {
  const [hovered, setHovered] = useState(false);
  const ci = entry.confidenceInterval;
  const hasCi = ci[0] !== 0 || ci[1] !== 0;

  return (
    <div
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        background: T.surface,
        border: `1px solid ${hovered ? '#30363d' : T.border}`,
        borderRadius: T.radius,
        padding: '20px 22px',
        display: 'flex',
        flexDirection: 'column',
        gap: 14,
        transition: 'border-color 0.15s, box-shadow 0.15s',
        boxShadow: hovered ? '0 4px 24px rgba(0,0,0,0.3)' : 'none',
        cursor: 'default',
      }}
    >
      {/* Top row: name + status */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 10 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 14, fontWeight: 600, color: T.text, marginBottom: 4 }}>
            {entry.name}
          </div>
          <div style={{ fontSize: 12, color: T.muted, lineHeight: 1.5 }}>
            {entry.description}
          </div>
        </div>
        <StatusBadge status={entry.status} />
      </div>

      {/* Flag key */}
      <code style={{
        fontSize: 11, color: T.blue,
        background: T.bg, border: `1px solid ${T.border}`,
        borderRadius: 4, padding: '3px 8px',
        alignSelf: 'flex-start',
        fontFamily: "'JetBrains Mono','Fira Code',monospace",
      }}>
        {entry.flagKey}
      </code>

      {/* Stats row */}
      <div style={{
        display: 'grid', gridTemplateColumns: '1fr 1fr 1fr',
        gap: 1, background: T.border, borderRadius: T.radiusSm, overflow: 'hidden',
      }}>
        {[
          {
            label: 'Sample Size',
            value: entry.sampleSize > 0
              ? entry.sampleSize.toLocaleString()
              : <span style={{ color: T.dim }}>—</span>,
          },
          {
            label: '95% CI',
            value: hasCi
              ? `[${(ci[0] * 100).toFixed(1)}%, ${(ci[1] * 100).toFixed(1)}%]`
              : <span style={{ color: T.dim }}>—</span>,
          },
          {
            label: 'Metric Lift',
            value: <LiftBadge lift={entry.metricLift} />,
          },
        ].map(({ label, value }) => (
          <div key={label} style={{
            background: T.bg, padding: '10px 14px',
          }}>
            <div style={{ fontSize: 10, fontWeight: 600, color: T.dim, textTransform: 'uppercase', letterSpacing: '0.07em', marginBottom: 5 }}>
              {label}
            </div>
            <div style={{ fontSize: 13, fontWeight: 600, color: T.text }}>
              {value}
            </div>
          </div>
        ))}
      </div>

      {/* Footer: view results + run analysis */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: 2 }}>
        <button
          onClick={() => onAnalyze(entry.flagKey)}
          style={{
            fontSize: 12, fontWeight: 500,
            color: T.muted, background: 'transparent', border: 'none',
            cursor: 'pointer', padding: 0,
            transition: 'color 0.15s',
          }}
          onMouseEnter={e => { (e.currentTarget as HTMLElement).style.color = T.blue; }}
          onMouseLeave={e => { (e.currentTarget as HTMLElement).style.color = T.muted; }}
        >
          Run Analysis
        </button>
        <button
          style={{
            fontSize: 12, fontWeight: 500,
            color: hovered ? T.blue : T.muted,
            background: 'transparent', border: 'none',
            cursor: 'pointer', padding: 0,
            display: 'flex', alignItems: 'center', gap: 4,
            transition: 'color 0.15s',
          }}
          onMouseEnter={e => { (e.currentTarget as HTMLElement).style.color = T.blue; }}
          onMouseLeave={e => { (e.currentTarget as HTMLElement).style.color = hovered ? T.blue : T.muted; }}
        >
          View Results <span style={{ fontSize: 14 }}>→</span>
        </button>
      </div>
    </div>
  );
}

// ── Input helpers ─────────────────────────────────────────────────────────────

function Field({
  label, placeholder, value, onChange, type = 'text', mono = false,
}: {
  label: string;
  placeholder: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  mono?: boolean;
}) {
  const [focused, setFocused] = useState(false);
  return (
    <div>
      <label style={{ fontSize: 11, fontWeight: 500, color: T.muted, display: 'block', marginBottom: 5 }}>
        {label}
      </label>
      <input
        type={type}
        placeholder={placeholder}
        value={value}
        onChange={e => onChange(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        style={{
          width: '100%', boxSizing: 'border-box',
          background: T.bg,
          border: `1px solid ${focused ? T.borderFocus : T.border}`,
          borderRadius: T.radiusSm,
          padding: '8px 12px',
          fontSize: 13,
          color: T.text,
          outline: 'none',
          fontFamily: mono ? "'JetBrains Mono','Fira Code',monospace" : 'inherit',
          transition: 'border-color 0.15s',
        }}
      />
    </div>
  );
}

// ── Main view ─────────────────────────────────────────────────────────────────

export default function Experiments() {
  const [form, setForm] = useState<FormState>({
    flagKey: '',
    metricName: 'conversion',
    metricSql: 'CASE WHEN converted THEN 1 ELSE 0 END',
    eventTable: 'user_events',
    flagEventTable: 'flag_evaluations',
    warehouseType: 'postgresql',
    warehouseDsn: '',
  });
  const [result, setResult] = useState<ExperimentResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [panelOpen, setPanelOpen] = useState(false);

  const run = async () => {
    setRunning(true);
    setError(null);
    setResult(null);
    try {
      const res = await fetch(`${INTEL_URL}/api/v1/experiments/analyze`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          experiment_id: crypto.randomUUID(),
          flag_key: form.flagKey,
          metric_name: form.metricName,
          metric_sql: form.metricSql,
          event_table: form.eventTable,
          flag_event_table: form.flagEventTable,
          warehouse_type: form.warehouseType,
          warehouse_dsn: form.warehouseDsn,
          stat_method: 'bayesian',
          min_sample_size: 100,
        }),
      });
      if (!res.ok) {
        const err = await res.json() as { detail?: string };
        throw new Error(err.detail ?? `HTTP ${res.status}`);
      }
      setResult(await res.json() as ExperimentResult);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Analysis failed');
    } finally {
      setRunning(false);
    }
  };

  const handleAnalyze = (flagKey: string) => {
    setForm(f => ({ ...f, flagKey }));
    setPanelOpen(true);
    setResult(null);
    setError(null);
    window.scrollTo({ top: 0, behavior: 'smooth' });
  };

  const runningCount  = MOCK_EXPERIMENTS.filter(e => e.status === 'RUNNING').length;
  const completeCount = MOCK_EXPERIMENTS.filter(e => e.status === 'COMPLETE').length;
  const draftCount    = MOCK_EXPERIMENTS.filter(e => e.status === 'DRAFT').length;

  return (
    <div style={{ padding: '28px 36px', maxWidth: 1280, margin: '0 auto' }}>

      {/* ── Page header ───────────────────────────────────────────────── */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div>
          <h1 style={{ fontSize: 24, fontWeight: 700, color: T.text, margin: '0 0 5px' }}>
            Experiments
          </h1>
          <p style={{ fontSize: 13, color: T.muted, margin: 0 }}>
            A/B tests and statistical analysis
          </p>
        </div>

        {/* Stat pills */}
        <div style={{ display: 'flex', gap: 10 }}>
          {[
            { label: 'Running',  val: runningCount,  color: T.blue  },
            { label: 'Complete', val: completeCount, color: T.green },
            { label: 'Draft',    val: draftCount,    color: T.dim   },
          ].map(s => (
            <div key={s.label} style={{
              background: T.bg, border: `1px solid ${T.border}`, borderRadius: 8,
              padding: '10px 18px', textAlign: 'center',
            }}>
              <div style={{ fontSize: 22, fontWeight: 700, color: s.color, lineHeight: 1 }}>{s.val}</div>
              <div style={{ fontSize: 11, color: T.dim, marginTop: 3 }}>{s.label}</div>
            </div>
          ))}
        </div>
      </div>

      {/* ── Analysis panel (collapsible) ───────────────────────────────── */}
      <div style={{
        background: T.surface, border: `1px solid ${T.border}`,
        borderRadius: T.radius, marginBottom: 28, overflow: 'hidden',
      }}>
        {/* Panel header / toggle */}
        <button
          onClick={() => setPanelOpen(o => !o)}
          style={{
            width: '100%', display: 'flex', alignItems: 'center', justifyContent: 'space-between',
            padding: '14px 20px', background: 'transparent', border: 'none', cursor: 'pointer',
          }}
        >
          <span style={{ fontSize: 13, fontWeight: 600, color: T.text }}>
            Warehouse Analysis
          </span>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 11, color: T.muted }}>
              {panelOpen ? 'Collapse' : 'Expand'}
            </span>
            <span style={{ fontSize: 16, color: T.muted, transform: panelOpen ? 'rotate(180deg)' : 'none', transition: 'transform 0.2s' }}>
              ▾
            </span>
          </div>
        </button>

        {panelOpen && (
          <div style={{ padding: '0 20px 20px', borderTop: `1px solid ${T.border}` }}>
            <p style={{ fontSize: 12, color: T.muted, margin: '14px 0 16px' }}>
              Warehouse-native analysis — your data never leaves your infrastructure.
            </p>

            {/* Form grid */}
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 14 }}>
              <Field
                label="Flag Key"
                placeholder="payments.checkout.checkout-v2"
                value={form.flagKey}
                onChange={v => setForm(f => ({ ...f, flagKey: v }))}
              />
              <Field
                label="Metric Name"
                placeholder="conversion"
                value={form.metricName}
                onChange={v => setForm(f => ({ ...f, metricName: v }))}
              />
              <Field
                label="Event Table"
                placeholder="user_events"
                value={form.eventTable}
                onChange={v => setForm(f => ({ ...f, eventTable: v }))}
              />
              <Field
                label="Flag Event Table"
                placeholder="flag_evaluations"
                value={form.flagEventTable}
                onChange={v => setForm(f => ({ ...f, flagEventTable: v }))}
              />
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14, marginBottom: 16 }}>
              <Field
                label="Metric SQL Expression"
                placeholder="CASE WHEN converted THEN 1 ELSE 0 END"
                value={form.metricSql}
                onChange={v => setForm(f => ({ ...f, metricSql: v }))}
                mono
              />
              <Field
                label="Warehouse Connection String"
                placeholder="Set VITE_WAREHOUSE_DSN in .env"
                value={form.warehouseDsn}
                onChange={v => setForm(f => ({ ...f, warehouseDsn: v }))}
                type="password"
              />
            </div>

            <button
              onClick={() => void run()}
              disabled={running || !form.flagKey || !form.warehouseDsn}
              style={{
                padding: '9px 20px', borderRadius: T.radiusSm,
                fontSize: 13, fontWeight: 600, cursor: 'pointer',
                background: running || !form.flagKey || !form.warehouseDsn ? '#21262d' : T.blueDeep,
                color: running || !form.flagKey || !form.warehouseDsn ? T.dim : '#fff',
                border: 'none', transition: 'background 0.15s, opacity 0.15s',
                opacity: running ? 0.7 : 1,
              }}
            >
              {running ? 'Running analysis…' : 'Run Analysis'}
            </button>
          </div>
        )}
      </div>

      {/* ── Error banner ───────────────────────────────────────────────── */}
      {error && (
        <div style={{
          background: T.redSoft, border: `1px solid ${T.redBorder}`,
          borderRadius: T.radiusSm, padding: '12px 16px',
          color: T.red, fontSize: 13, marginBottom: 20,
          display: 'flex', alignItems: 'center', gap: 8,
        }}>
          <span style={{ fontSize: 16 }}>⚠</span> {error}
        </div>
      )}

      {/* ── Live result card ───────────────────────────────────────────── */}
      {result && (() => {
        const rec = REC_CONFIG[result.recommendation];
        return (
          <div style={{
            background: T.surface, border: `1px solid ${T.border}`,
            borderRadius: T.radius, padding: '20px 22px', marginBottom: 28,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
              <div>
                <div style={{ fontSize: 11, color: T.dim, textTransform: 'uppercase', letterSpacing: '0.07em', marginBottom: 5 }}>
                  Live Result · {result.flag_key}
                </div>
                <span style={{
                  display: 'inline-flex', alignItems: 'center',
                  fontSize: 12, fontWeight: 700, padding: '4px 12px', borderRadius: 20,
                  background: rec.bg, border: `1px solid ${rec.border}`, color: rec.color,
                  letterSpacing: '0.05em', textTransform: 'uppercase',
                }}>
                  {rec.label}
                </span>
              </div>
              <button
                onClick={() => setResult(null)}
                style={{
                  background: 'transparent', border: 'none', cursor: 'pointer',
                  color: T.dim, fontSize: 18, lineHeight: 1,
                  padding: '2px 6px', borderRadius: 4,
                  transition: 'color 0.15s',
                }}
                onMouseEnter={e => { (e.currentTarget as HTMLElement).style.color = T.text; }}
                onMouseLeave={e => { (e.currentTarget as HTMLElement).style.color = T.dim; }}
                title="Dismiss"
              >
                ×
              </button>
            </div>

            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 1,
              background: T.border, borderRadius: T.radiusSm, overflow: 'hidden',
            }}>
              {[
                {
                  label: 'Relative Lift',
                  value: (
                    <span style={{ color: result.relative_lift >= 0 ? T.green : T.red, fontSize: 22, fontWeight: 700 }}>
                      {result.relative_lift >= 0 ? '+' : ''}{(result.relative_lift * 100).toFixed(2)}%
                    </span>
                  ),
                },
                {
                  label: 'Sample Sizes',
                  value: (
                    <div style={{ fontSize: 13, color: T.text, lineHeight: 1.6 }}>
                      <span style={{ color: T.muted }}>Control: </span>{result.sample_sizes.control.toLocaleString()}
                      <br />
                      <span style={{ color: T.muted }}>Treatment: </span>{result.sample_sizes.treatment.toLocaleString()}
                    </div>
                  ),
                },
                ...(result.probability_beats_control !== undefined ? [{
                  label: 'P(Treatment > Control)',
                  value: (
                    <span style={{ fontSize: 22, fontWeight: 700, color: T.text }}>
                      {(result.probability_beats_control * 100).toFixed(1)}%
                    </span>
                  ),
                }] : []),
                {
                  label: 'Statistically Significant',
                  value: (
                    <span style={{ fontSize: 15, fontWeight: 700, color: result.is_significant ? T.green : T.dim }}>
                      {result.is_significant ? 'Yes' : 'No'}
                    </span>
                  ),
                },
              ].map(({ label, value }) => (
                <div key={label} style={{ background: T.bg, padding: '14px 18px' }}>
                  <div style={{ fontSize: 10, fontWeight: 600, color: T.dim, textTransform: 'uppercase', letterSpacing: '0.07em', marginBottom: 8 }}>
                    {label}
                  </div>
                  {value}
                </div>
              ))}
            </div>
          </div>
        );
      })()}

      {/* ── Section label ──────────────────────────────────────────────── */}
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16,
      }}>
        <h2 style={{ fontSize: 13, fontWeight: 600, color: T.muted, margin: 0, textTransform: 'uppercase', letterSpacing: '0.07em' }}>
          All Experiments
        </h2>
        <span style={{ fontSize: 12, color: T.dim }}>
          {MOCK_EXPERIMENTS.length} total
        </span>
      </div>

      {/* ── Experiment grid ────────────────────────────────────────────── */}
      {MOCK_EXPERIMENTS.length === 0 ? (
        <EmptyState
          icon={
            <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M9 3H5a2 2 0 0 0-2 2v4m6-6h10a2 2 0 0 1 2 2v4M9 3v11l-3 3a2 2 0 0 0 1.4 3.4h9.2A2 2 0 0 0 18 17l-3-3V3" />
            </svg>
          }
          heading="No experiments yet"
          body="Create your first A/B test to start measuring the impact of your flag changes."
        />
      ) : (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(440px, 1fr))',
          gap: 14,
        }}>
          {MOCK_EXPERIMENTS.map((entry, i) => (
            <Reveal key={entry.id} delay={i * 0.05}>
              <ExperimentCard entry={entry} onAnalyze={handleAnalyze} />
            </Reveal>
          ))}
        </div>
      )}
    </div>
  );
}
