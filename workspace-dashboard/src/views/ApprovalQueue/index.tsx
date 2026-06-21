import { useState, useEffect } from 'react';

interface ChangeRequest {
  id: string;
  flagKey: string;
  environment: string;
  requestedBy: string;
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'APPLIED';
  changeDescription: string;
  approvedBy: string[];
  createdAt: number;
}

export default function ApprovalQueue() {
  const [requests, setRequests] = useState<ChangeRequest[]>([]);
  const [loading, setLoading] = useState(true);
  const [acting, setActing] = useState<string | null>(null);

  const apiUrl = (import.meta as { env: Record<string, string> }).env['VITE_API_URL'] ?? 'http://localhost:8081';
  const token = (import.meta as { env: Record<string, string> }).env['VITE_SDK_TOKEN'] ?? 'sdk-dev-token-change-in-prod';
  const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };

  useEffect(() => {
    setLoading(true);
    fetch(`${apiUrl}/api/v1/change-requests?status=PENDING`, { headers })
      .then(r => r.json())
      .then((d: { requests?: ChangeRequest[] }) => setRequests(d.requests ?? []))
      .catch(console.error)
      .finally(() => setLoading(false));
  }, []);

  const approve = async (id: string) => {
    setActing(id);
    await fetch(`${apiUrl}/api/v1/change-requests/${id}/approve`, {
      method: 'POST', headers,
      body: JSON.stringify({ approved_by: 'current-user@example.com' }),
    });
    setRequests(prev => prev.filter(r => r.id !== id));
    setActing(null);
  };

  const reject = async (id: string) => {
    const reason = window.prompt('Rejection reason:');
    if (!reason) return;
    setActing(id);
    await fetch(`${apiUrl}/api/v1/change-requests/${id}/reject`, {
      method: 'POST', headers,
      body: JSON.stringify({ rejected_by: 'current-user@example.com', reason }),
    });
    setRequests(prev => prev.filter(r => r.id !== id));
    setActing(null);
  };

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Approval Queue</h1>
        <p className="text-gray-400 text-sm mt-1">
          Production flag changes require approval from a second team member
        </p>
      </div>

      {loading && <div className="text-gray-500">Loading…</div>}

      {!loading && requests.length === 0 && (
        <div className="bg-gray-900 rounded-lg border border-gray-700 p-8 text-center text-gray-500">
          No pending approvals ✓
        </div>
      )}

      <div className="space-y-3">
        {requests.map(req => (
          <div key={req.id} className="bg-gray-900 rounded-lg border border-amber-800/50 p-4">
            <div className="flex items-start justify-between">
              <div>
                <div className="flex items-center gap-2 mb-1">
                  <span className="font-mono text-blue-400 text-sm">{req.flagKey}</span>
                  <span className="text-xs px-2 py-0.5 rounded bg-amber-900/40 text-amber-400 border border-amber-800">
                    {req.environment}
                  </span>
                </div>
                <p className="text-gray-300 text-sm">{req.changeDescription}</p>
                <p className="text-gray-500 text-xs mt-1">
                  Requested by {req.requestedBy}
                </p>
              </div>
              <div className="flex gap-2 shrink-0 ml-4">
                <button
                  onClick={() => void approve(req.id)}
                  disabled={acting === req.id}
                  className="px-4 py-1.5 rounded text-sm font-medium bg-green-700 hover:bg-green-600 text-white disabled:opacity-50 transition-colors"
                >
                  {acting === req.id ? '…' : 'Approve'}
                </button>
                <button
                  onClick={() => void reject(req.id)}
                  disabled={acting === req.id}
                  className="px-4 py-1.5 rounded text-sm font-medium bg-red-900/60 hover:bg-red-800 text-red-300 disabled:opacity-50 transition-colors"
                >
                  Reject
                </button>
              </div>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
