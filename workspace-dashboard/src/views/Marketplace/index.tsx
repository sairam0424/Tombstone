import { useState, useEffect } from 'react';
import { MARKETPLACE_URL } from '../../config.js';

interface Integration {
  id: string;
  name: string;
  description: string;
  category: string;
  installed: boolean;
  webhook_url?: string;
}

// Map API categories to display labels and filter pills
const FILTER_PILLS = [
  { key: 'all',                label: 'All' },
  { key: 'observability',      label: 'Monitoring' },
  { key: 'incident-management',label: 'Incident' },
  { key: 'notifications',      label: 'Analytics' },
  { key: 'project-management', label: 'CI/CD' },
];

// Icon background palette — cycles by first letter
const ICON_PALETTE: Array<{ bg: string; fg: string }> = [
  { bg: 'rgba(88,166,255,0.18)',  fg: '#58a6ff' },
  { bg: 'rgba(63,185,80,0.18)',   fg: '#3fb950' },
  { bg: 'rgba(210,153,255,0.18)', fg: '#d29bff' },
  { bg: 'rgba(255,166,88,0.18)',  fg: '#ffa658' },
  { bg: 'rgba(248,81,73,0.18)',   fg: '#f85149' },
  { bg: 'rgba(86,211,200,0.18)',  fg: '#56d3c8' },
];

function iconColor(name: string): { bg: string; fg: string } {
  const idx = (name.charCodeAt(0) || 0) % ICON_PALETTE.length;
  return ICON_PALETTE[idx];
}

type StatusKey = 'CONNECTED' | 'AVAILABLE' | 'ERROR';

function resolveStatus(integration: Integration): StatusKey {
  if (integration.installed) return 'CONNECTED';
  return 'AVAILABLE';
}

const STATUS_STYLES: Record<StatusKey, { dot: string; label: string; bg: string; border: string; color: string }> = {
  CONNECTED: {
    dot: '#3fb950',
    label: 'Connected',
    bg: 'rgba(63,185,80,0.12)',
    border: 'rgba(63,185,80,0.28)',
    color: '#3fb950',
  },
  AVAILABLE: {
    dot: '#58a6ff',
    label: 'Available',
    bg: 'rgba(88,166,255,0.10)',
    border: 'rgba(88,166,255,0.28)',
    color: '#58a6ff',
  },
  ERROR: {
    dot: '#f85149',
    label: 'Error',
    bg: 'rgba(248,81,73,0.10)',
    border: 'rgba(248,81,73,0.28)',
    color: '#f85149',
  },
};

export default function Marketplace() {
  const [integrations, setIntegrations] = useState<Integration[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeCategory, setActiveCategory] = useState('all');
  const [actionLoading, setActionLoading] = useState<string | null>(null);

  async function fetchIntegrations() {
    try {
      setLoading(true);
      setError(null);
      const res = await fetch(`${MARKETPLACE_URL}/api/v1/marketplace`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      setIntegrations(Array.isArray(data) ? data : (data.integrations ?? []));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load integrations');
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    fetchIntegrations();
  }, []);

  async function handleInstall(integration: Integration) {
    const webhookUrl = window.prompt(
      `Enter webhook URL for ${integration.name}:`,
      'https://'
    );
    if (!webhookUrl || webhookUrl === 'https://') return;

    setActionLoading(integration.id);
    try {
      const res = await fetch(`${MARKETPLACE_URL}/api/v1/marketplace/${integration.id}/install`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ webhook_url: webhookUrl }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      await fetchIntegrations();
    } catch (err) {
      alert(`Install failed: ${err instanceof Error ? err.message : 'Unknown error'}`);
    } finally {
      setActionLoading(null);
    }
  }

  async function handleUninstall(integration: Integration) {
    if (!window.confirm(`Uninstall ${integration.name}?`)) return;

    setActionLoading(integration.id);
    try {
      const res = await fetch(`${MARKETPLACE_URL}/api/v1/marketplace/${integration.id}`, {
        method: 'DELETE',
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      await fetchIntegrations();
    } catch (err) {
      alert(`Uninstall failed: ${err instanceof Error ? err.message : 'Unknown error'}`);
    } finally {
      setActionLoading(null);
    }
  }

  const filtered =
    activeCategory === 'all'
      ? integrations
      : integrations.filter(i => i.category === activeCategory);

  const connectedCount = integrations.filter(i => i.installed).length;

  return (
    <div
      style={{
        padding: '28px 32px',
        minHeight: '100%',
        background: '#070b12',
        color: '#e6edf3',
        fontFamily: 'inherit',
      }}
    >
      {/* ── Page header ── */}
      <div
        style={{
          display: 'flex',
          alignItems: 'flex-start',
          justifyContent: 'space-between',
          marginBottom: '28px',
          gap: '16px',
        }}
      >
        <div>
          <h1
            style={{
              fontSize: '22px',
              fontWeight: 700,
              color: '#f0f6fc',
              margin: '0 0 6px',
              letterSpacing: '-0.3px',
            }}
          >
            Marketplace
          </h1>
          <p style={{ fontSize: '13px', color: '#7d8590', margin: 0, lineHeight: 1.5 }}>
            Integrations and plugins
          </p>
        </div>

        {/* Connected counter badge */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: '6px',
            padding: '5px 14px',
            borderRadius: '20px',
            background: 'rgba(63,185,80,0.10)',
            border: '1px solid rgba(63,185,80,0.25)',
            fontSize: '12px',
            fontWeight: 500,
            color: '#3fb950',
            flexShrink: 0,
          }}
        >
          <span
            style={{
              width: '6px',
              height: '6px',
              borderRadius: '50%',
              background: '#3fb950',
              display: 'inline-block',
            }}
          />
          {connectedCount} connected
        </div>
      </div>

      {/* ── Divider ── */}
      <div
        style={{
          height: '1px',
          background: 'linear-gradient(90deg, #1c2333 0%, #21262d 60%, transparent 100%)',
          marginBottom: '24px',
        }}
      />

      {/* ── Category filter pills ── */}
      <div
        style={{
          display: 'flex',
          gap: '8px',
          marginBottom: '28px',
          flexWrap: 'wrap',
        }}
      >
        {FILTER_PILLS.map(pill => {
          const isActive = activeCategory === pill.key;
          return (
            <button
              key={pill.key}
              onClick={() => setActiveCategory(pill.key)}
              style={{
                padding: '5px 16px',
                borderRadius: '20px',
                border: `1px solid ${isActive ? 'rgba(88,166,255,0.55)' : '#1c2333'}`,
                background: isActive ? 'rgba(88,166,255,0.12)' : '#0d1117',
                color: isActive ? '#58a6ff' : '#7d8590',
                fontSize: '12px',
                fontWeight: isActive ? 600 : 400,
                cursor: 'pointer',
                transition: 'all 0.15s ease',
                letterSpacing: '0.01em',
              }}
            >
              {pill.label}
            </button>
          );
        })}
      </div>

      {/* ── Loading state ── */}
      {loading && (
        <div
          style={{
            textAlign: 'center',
            padding: '72px 0',
            color: '#484f58',
            fontSize: '14px',
          }}
        >
          <div
            style={{
              width: '24px',
              height: '24px',
              border: '2px solid #21262d',
              borderTopColor: '#58a6ff',
              borderRadius: '50%',
              margin: '0 auto 14px',
              animation: 'spin 0.8s linear infinite',
            }}
          />
          Loading integrations…
          <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
        </div>
      )}

      {/* ── Error state ── */}
      {error && !loading && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: '12px',
            padding: '14px 18px',
            borderRadius: '10px',
            background: 'rgba(248,81,73,0.08)',
            border: '1px solid rgba(248,81,73,0.25)',
            color: '#f85149',
            fontSize: '13px',
            marginBottom: '20px',
          }}
        >
          <span style={{ flex: 1 }}>
            <strong style={{ fontWeight: 600 }}>Error:</strong> {error}
          </span>
          <button
            onClick={fetchIntegrations}
            style={{
              padding: '4px 12px',
              borderRadius: '6px',
              border: '1px solid rgba(248,81,73,0.35)',
              background: 'rgba(248,81,73,0.08)',
              color: '#f85149',
              cursor: 'pointer',
              fontSize: '12px',
              fontWeight: 500,
              flexShrink: 0,
            }}
          >
            Retry
          </button>
        </div>
      )}

      {/* ── Empty state ── */}
      {!loading && !error && filtered.length === 0 && (
        <div
          style={{
            textAlign: 'center',
            padding: '72px 0',
            color: '#484f58',
            fontSize: '14px',
          }}
        >
          No integrations found in this category.
        </div>
      )}

      {/* ── Integration grid ── */}
      {!loading && filtered.length > 0 && (
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))',
            gap: '16px',
          }}
        >
          {filtered.map(integration => {
            const status = resolveStatus(integration);
            const statusStyle = STATUS_STYLES[status];
            const icon = iconColor(integration.name);
            const isActioning = actionLoading === integration.id;
            const isConnected = integration.installed;

            return (
              <div
                key={integration.id}
                style={{
                  background: '#0d1117',
                  border: `1px solid ${isConnected ? 'rgba(63,185,80,0.18)' : '#1c2333'}`,
                  borderRadius: '12px',
                  padding: '20px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: '14px',
                  transition: 'border-color 0.15s ease, box-shadow 0.15s ease',
                  boxShadow: isConnected
                    ? '0 0 0 1px rgba(63,185,80,0.08) inset'
                    : 'none',
                }}
              >
                {/* ── Card header: icon + name + status ── */}
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '12px',
                  }}
                >
                  {/* Icon placeholder */}
                  <div
                    style={{
                      width: '40px',
                      height: '40px',
                      borderRadius: '10px',
                      background: icon.bg,
                      border: `1px solid ${icon.fg}33`,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      fontSize: '16px',
                      fontWeight: 700,
                      color: icon.fg,
                      flexShrink: 0,
                      letterSpacing: '-0.5px',
                    }}
                  >
                    {integration.name.charAt(0).toUpperCase()}
                  </div>

                  {/* Name + category */}
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div
                      style={{
                        fontWeight: 600,
                        fontSize: '14px',
                        color: '#f0f6fc',
                        lineHeight: 1.3,
                        whiteSpace: 'nowrap',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                      }}
                    >
                      {integration.name}
                    </div>
                    <div
                      style={{
                        fontSize: '11px',
                        color: '#484f58',
                        marginTop: '3px',
                        textTransform: 'capitalize',
                      }}
                    >
                      {integration.category.replace(/-/g, ' ')}
                    </div>
                  </div>

                  {/* Status badge */}
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: '5px',
                      padding: '3px 9px',
                      borderRadius: '20px',
                      background: statusStyle.bg,
                      border: `1px solid ${statusStyle.border}`,
                      fontSize: '11px',
                      fontWeight: 500,
                      color: statusStyle.color,
                      flexShrink: 0,
                    }}
                  >
                    <span
                      style={{
                        width: '5px',
                        height: '5px',
                        borderRadius: '50%',
                        background: statusStyle.dot,
                        display: 'inline-block',
                        flexShrink: 0,
                      }}
                    />
                    {statusStyle.label}
                  </div>
                </div>

                {/* ── Separator ── */}
                <div
                  style={{
                    height: '1px',
                    background: '#1c2333',
                  }}
                />

                {/* ── Description ── */}
                <p
                  style={{
                    fontSize: '13px',
                    color: '#7d8590',
                    margin: 0,
                    lineHeight: 1.6,
                    flex: 1,
                  }}
                >
                  {integration.description}
                </p>

                {/* ── Webhook URL (when connected) ── */}
                {isConnected && integration.webhook_url && (
                  <div
                    style={{
                      padding: '7px 10px',
                      borderRadius: '7px',
                      background: '#0a0e16',
                      border: '1px solid #1c2333',
                      fontSize: '11px',
                      color: '#484f58',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                      fontFamily: 'ui-monospace, monospace',
                    }}
                    title={integration.webhook_url}
                  >
                    {integration.webhook_url}
                  </div>
                )}

                {/* ── Action button ── */}
                <button
                  disabled={isActioning}
                  onClick={() =>
                    isConnected ? handleUninstall(integration) : handleInstall(integration)
                  }
                  style={{
                    padding: '8px 16px',
                    borderRadius: '8px',
                    border: `1px solid ${
                      isConnected ? '#1c2333' : 'rgba(88,166,255,0.30)'
                    }`,
                    background: isConnected
                      ? '#161b22'
                      : 'rgba(88,166,255,0.10)',
                    color: isConnected ? '#e6edf3' : '#58a6ff',
                    fontSize: '13px',
                    fontWeight: 500,
                    cursor: isActioning ? 'not-allowed' : 'pointer',
                    opacity: isActioning ? 0.55 : 1,
                    transition: 'all 0.15s ease',
                    textAlign: 'center',
                    letterSpacing: '0.01em',
                  }}
                >
                  {isActioning
                    ? isConnected
                      ? 'Disconnecting…'
                      : 'Connecting…'
                    : isConnected
                    ? 'Configure'
                    : 'Connect'}
                </button>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
