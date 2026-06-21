import { useState, useEffect } from 'react';

interface Integration {
  id: string;
  name: string;
  description: string;
  category: string;
  installed: boolean;
  webhook_url?: string;
}

const CATEGORIES = ['all', 'notifications', 'observability', 'incident-management', 'project-management'];

const CATEGORY_COLORS: Record<string, { bg: string; color: string }> = {
  notifications:         { bg: '#1f2d3d', color: '#58a6ff' },
  observability:         { bg: '#1d2d22', color: '#3fb950' },
  'incident-management': { bg: '#2d1d1d', color: '#f85149' },
  'project-management':  { bg: '#2d261d', color: '#e3b341' },
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
      const res = await fetch('http://localhost:8086/api/v1/marketplace');
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
      const res = await fetch(`http://localhost:8086/api/v1/marketplace/${integration.id}/install`, {
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
      const res = await fetch(`http://localhost:8086/api/v1/marketplace/${integration.id}`, {
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

  const filtered = activeCategory === 'all'
    ? integrations
    : integrations.filter(i => i.category === activeCategory);

  const installedCount = integrations.filter(i => i.installed).length;

  return (
    <div style={{ padding: '24px', minHeight: '100%', background: '#080c14', color: '#e6edf3' }}>
      {/* Header */}
      <div style={{ marginBottom: '24px' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div>
            <h1 style={{ fontSize: '20px', fontWeight: 600, color: '#e6edf3', margin: 0 }}>
              Marketplace
            </h1>
            <p style={{ fontSize: '13px', color: '#8b949e', margin: '4px 0 0' }}>
              Connect Tombstone to your observability, alerting, and project-management stack
            </p>
          </div>
          <div style={{
            padding: '4px 12px',
            borderRadius: '20px',
            background: '#161b22',
            border: '1px solid #21262d',
            fontSize: '12px',
            color: '#3fb950',
          }}>
            {installedCount} installed
          </div>
        </div>
      </div>

      {/* Category filter */}
      <div style={{ display: 'flex', gap: '8px', marginBottom: '24px', flexWrap: 'wrap' }}>
        {CATEGORIES.map(cat => (
          <button
            key={cat}
            onClick={() => setActiveCategory(cat)}
            style={{
              padding: '5px 14px',
              borderRadius: '20px',
              border: `1px solid ${activeCategory === cat ? '#58a6ff' : '#21262d'}`,
              background: activeCategory === cat ? 'rgba(88,166,255,0.12)' : '#0d1117',
              color: activeCategory === cat ? '#58a6ff' : '#8b949e',
              fontSize: '12px',
              cursor: 'pointer',
              textTransform: 'capitalize',
              transition: 'all 0.15s',
            }}
          >
            {cat.replace(/-/g, ' ')}
          </button>
        ))}
      </div>

      {/* Loading state */}
      {loading && (
        <div style={{ textAlign: 'center', padding: '60px', color: '#8b949e', fontSize: '14px' }}>
          Loading integrations…
        </div>
      )}

      {/* Error state */}
      {error && !loading && (
        <div style={{
          padding: '16px',
          borderRadius: '8px',
          background: 'rgba(248,81,73,0.1)',
          border: '1px solid rgba(248,81,73,0.3)',
          color: '#f85149',
          fontSize: '13px',
          marginBottom: '16px',
        }}>
          <strong>Error:</strong> {error}
          <button
            onClick={fetchIntegrations}
            style={{
              marginLeft: '12px',
              padding: '3px 10px',
              borderRadius: '6px',
              border: '1px solid rgba(248,81,73,0.4)',
              background: 'transparent',
              color: '#f85149',
              cursor: 'pointer',
              fontSize: '12px',
            }}
          >
            Retry
          </button>
        </div>
      )}

      {/* Empty state */}
      {!loading && !error && filtered.length === 0 && (
        <div style={{ textAlign: 'center', padding: '60px', color: '#484f58', fontSize: '14px' }}>
          No integrations found in this category.
        </div>
      )}

      {/* Integration grid */}
      {!loading && filtered.length > 0 && (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))',
          gap: '16px',
        }}>
          {filtered.map(integration => {
            const catStyle = CATEGORY_COLORS[integration.category] ?? { bg: '#161b22', color: '#8b949e' };
            const isActioning = actionLoading === integration.id;

            return (
              <div
                key={integration.id}
                style={{
                  background: '#0d1117',
                  border: `1px solid ${integration.installed ? '#1f3a2a' : '#21262d'}`,
                  borderRadius: '10px',
                  padding: '18px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: '12px',
                  transition: 'border-color 0.15s',
                }}
              >
                {/* Top row: name + status badge */}
                <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: '8px' }}>
                  <div style={{ fontWeight: 600, fontSize: '14px', color: '#e6edf3', lineHeight: 1.3 }}>
                    {integration.name}
                  </div>
                  <div style={{
                    padding: '2px 8px',
                    borderRadius: '12px',
                    fontSize: '11px',
                    fontWeight: 500,
                    flexShrink: 0,
                    background: integration.installed ? 'rgba(63,185,80,0.15)' : 'rgba(139,148,158,0.12)',
                    color: integration.installed ? '#3fb950' : '#8b949e',
                    border: `1px solid ${integration.installed ? 'rgba(63,185,80,0.3)' : '#21262d'}`,
                  }}>
                    {integration.installed ? '● Installed' : '○ Available'}
                  </div>
                </div>

                {/* Category badge */}
                <div style={{
                  display: 'inline-flex',
                  alignSelf: 'flex-start',
                  padding: '2px 8px',
                  borderRadius: '4px',
                  fontSize: '11px',
                  background: catStyle.bg,
                  color: catStyle.color,
                  textTransform: 'capitalize',
                }}>
                  {integration.category.replace(/-/g, ' ')}
                </div>

                {/* Description */}
                <p style={{
                  fontSize: '13px',
                  color: '#8b949e',
                  margin: 0,
                  lineHeight: 1.5,
                  flex: 1,
                }}>
                  {integration.description}
                </p>

                {/* Webhook URL (if installed) */}
                {integration.installed && integration.webhook_url && (
                  <div style={{
                    padding: '6px 10px',
                    borderRadius: '6px',
                    background: '#161b22',
                    border: '1px solid #21262d',
                    fontSize: '11px',
                    color: '#484f58',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}>
                    {integration.webhook_url}
                  </div>
                )}

                {/* Action button */}
                <button
                  disabled={isActioning}
                  onClick={() => integration.installed ? handleUninstall(integration) : handleInstall(integration)}
                  style={{
                    padding: '7px 14px',
                    borderRadius: '6px',
                    border: `1px solid ${integration.installed ? 'rgba(248,81,73,0.4)' : '#30363d'}`,
                    background: integration.installed
                      ? 'rgba(248,81,73,0.08)'
                      : 'rgba(88,166,255,0.08)',
                    color: integration.installed ? '#f85149' : '#58a6ff',
                    fontSize: '13px',
                    cursor: isActioning ? 'not-allowed' : 'pointer',
                    opacity: isActioning ? 0.6 : 1,
                    transition: 'all 0.15s',
                    fontWeight: 500,
                  }}
                >
                  {isActioning
                    ? (integration.installed ? 'Uninstalling…' : 'Installing…')
                    : (integration.installed ? 'Uninstall' : 'Install')}
                </button>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
