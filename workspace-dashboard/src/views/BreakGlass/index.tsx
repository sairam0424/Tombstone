import { useState, useEffect } from 'react';
import { API_URL, SDK_TOKEN } from '../../config.js';

interface BGToken {
  id: string;
  scope: string;
  createdBy: string;
  expiresAt: number;
  used: boolean;
  usedBy?: string;
  incidentRef?: string;
}

export default function BreakGlassView() {
  const [tokens, setTokens] = useState<BGToken[]>([]);
  const [creating, setCreating] = useState(false);
  const [newToken, setNewToken] = useState<string | null>(null);
  const [form, setForm] = useState({ scope: 'all-flags', expiresInHours: 4, incidentRef: '' });

  const apiUrl = API_URL;
  const token = SDK_TOKEN;
  const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };

  const loadTokens = () => {
    fetch(`${apiUrl}/api/v1/break-glass/tokens`, { headers })
      .then(r => r.json())
      .then((d: { tokens?: BGToken[] }) => setTokens(d.tokens ?? []))
      .catch(console.error);
  };

  useEffect(() => { loadTokens(); }, []);

  const create = async () => {
    setCreating(true);
    try {
      const res = await fetch(`${apiUrl}/api/v1/break-glass/tokens`, {
        method: 'POST', headers,
        body: JSON.stringify({
          scope: form.scope,
          expires_in_hours: form.expiresInHours,
          incident_ref: form.incidentRef,
        }),
      });
      const data = await res.json() as { token?: string };
      if (data.token) {
        setNewToken(data.token);
        loadTokens();
      }
    } finally {
      setCreating(false);
    }
  };

  const now = Math.floor(Date.now() / 1000);

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Break-Glass Tokens</h1>
        <p className="text-gray-400 text-sm mt-1">
          Pre-authorized emergency tokens for on-call engineers. No approval needed during an incident.
        </p>
      </div>

      {/* New token display (one-time) */}
      {newToken && (
        <div className="bg-amber-900/30 border border-amber-500 rounded-lg p-4 mb-6">
          <p className="text-amber-300 font-medium text-sm mb-2">
            Token created. Copy it now — it will not be shown again.
          </p>
          <code className="bg-black/40 text-amber-200 text-xs p-2 rounded block font-mono break-all">
            {newToken}
          </code>
          <button
            onClick={() => { void navigator.clipboard.writeText(newToken); }}
            className="mt-2 text-xs text-amber-400 hover:underline"
          >
            Copy to clipboard
          </button>
        </div>
      )}

      {/* Create form */}
      <div className="bg-gray-900 border border-gray-700 rounded-lg p-4 mb-6">
        <h2 className="text-sm font-semibold text-white mb-3">Create Token</h2>
        <div className="grid grid-cols-3 gap-3 mb-3">
          <div>
            <label className="text-xs text-gray-400 block mb-1">Scope</label>
            <select
              value={form.scope}
              onChange={e => setForm(f => ({ ...f, scope: e.target.value }))}
              className="w-full bg-gray-800 border border-gray-600 rounded px-2 py-1.5 text-sm text-white"
            >
              <option value="all-flags">All flags</option>
              <option value="payment-flags">Payment flags</option>
              <option value="auth-flags">Auth flags</option>
            </select>
          </div>
          <div>
            <label className="text-xs text-gray-400 block mb-1">Expires (hours)</label>
            <input
              type="number"
              min={1}
              max={24}
              value={form.expiresInHours}
              onChange={e => setForm(f => ({ ...f, expiresInHours: parseInt(e.target.value) }))}
              className="w-full bg-gray-800 border border-gray-600 rounded px-2 py-1.5 text-sm text-white"
            />
          </div>
          <div>
            <label className="text-xs text-gray-400 block mb-1">Incident ref (optional)</label>
            <input
              type="text"
              placeholder="INC-1234"
              value={form.incidentRef}
              onChange={e => setForm(f => ({ ...f, incidentRef: e.target.value }))}
              className="w-full bg-gray-800 border border-gray-600 rounded px-2 py-1.5 text-sm text-white"
            />
          </div>
        </div>
        <button
          onClick={() => void create()}
          disabled={creating}
          className="px-4 py-1.5 rounded text-sm font-medium bg-red-700 hover:bg-red-600 text-white disabled:opacity-50 transition-colors"
        >
          {creating ? 'Creating…' : 'Create Break-Glass Token'}
        </button>
      </div>

      {/* Token list */}
      <div className="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
        <div className="px-4 py-3 border-b border-gray-700 text-sm font-medium text-white">
          Recent Tokens
        </div>
        {tokens.length === 0 ? (
          <div className="p-6 text-center text-gray-500 text-sm">No tokens created yet</div>
        ) : (
          <div className="divide-y divide-gray-800">
            {tokens.map(t => (
              <div key={t.id} className="px-4 py-3 flex items-center justify-between">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-mono text-gray-300">{t.scope}</span>
                    {t.used && (
                      <span className="text-xs px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">USED</span>
                    )}
                    {!t.used && now > t.expiresAt && (
                      <span className="text-xs px-1.5 py-0.5 rounded bg-red-900/40 text-red-400">EXPIRED</span>
                    )}
                    {!t.used && now <= t.expiresAt && (
                      <span className="text-xs px-1.5 py-0.5 rounded bg-green-900/40 text-green-400">ACTIVE</span>
                    )}
                  </div>
                  <p className="text-xs text-gray-500 mt-0.5">
                    By {t.createdBy}
                    {t.incidentRef && ` · ${t.incidentRef}`}
                    {' · '}
                    Expires {new Date(t.expiresAt * 1000).toLocaleString()}
                  </p>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
