import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../../config.js';
import { EmptyState } from '../../components/ui/index.js';
import { useCurrentUser } from '../../hooks/useCurrentUser.js';

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

function CheckCircleIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="40"
      height="40"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="12" cy="12" r="10" />
      <path d="M8 12l2.5 2.5L16 9" />
    </svg>
  );
}

function ClockIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      style={{ display: 'inline', verticalAlign: 'middle' }}
    >
      <circle cx="12" cy="12" r="10" />
      <path d="M12 6v6l4 2" />
    </svg>
  );
}

function UserIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      style={{ display: 'inline', verticalAlign: 'middle' }}
    >
      <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" />
      <circle cx="12" cy="7" r="4" />
    </svg>
  );
}

function formatTimestamp(ts: number): string {
  const date = new Date(ts);
  return date.toLocaleString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export default function ApprovalQueue() {
  const [acting, setActing] = useState<string | null>(null);
  const queryClient = useQueryClient();

  const { email: currentUserEmail } = useCurrentUser();
  const apiUrl = API_URL;
  const token = SDK_TOKEN;
  const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };

  const { data: requests = [], isLoading: loading } = useQuery({
    queryKey: ['change-requests', 'PENDING'],
    queryFn: async (): Promise<ChangeRequest[]> => {
      const r = await fetch(`${apiUrl}/api/v1/change-requests?status=PENDING`, { headers });
      if (!r.ok) return [];
      const d = await r.json() as { requests?: ChangeRequest[] };
      return d.requests ?? [];
    },
    refetchInterval: 30_000,
  });

  const approve = async (id: string) => {
    setActing(id);
    await fetch(`${apiUrl}/api/v1/change-requests/${id}/approve`, {
      method: 'POST', headers,
      body: JSON.stringify({ approved_by: currentUserEmail }),
    });
    void queryClient.invalidateQueries({ queryKey: ['change-requests', 'PENDING'] });
    setActing(null);
  };

  const reject = async (id: string) => {
    const reason = window.prompt('Rejection reason:');
    if (!reason) return;
    setActing(id);
    await fetch(`${apiUrl}/api/v1/change-requests/${id}/reject`, {
      method: 'POST', headers,
      body: JSON.stringify({ rejected_by: currentUserEmail, reason }),
    });
    void queryClient.invalidateQueries({ queryKey: ['change-requests', 'PENDING'] });
    setActing(null);
  };

  const pendingCount = requests.length;

  return (
    <div style={{ padding: '32px', maxWidth: '860px', margin: '0 auto' }}>

      {/* Page header */}
      <div style={{ marginBottom: '28px', display: 'flex', alignItems: 'center', gap: '12px' }}>
        <h1 style={{
          fontSize: '22px',
          fontWeight: 700,
          color: '#f9fafb',
          letterSpacing: '-0.01em',
          margin: 0,
        }}>
          Approval Queue
        </h1>
        {!loading && (
          <span style={{
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            minWidth: '24px',
            height: '22px',
            padding: '0 8px',
            borderRadius: '999px',
            background: pendingCount > 0 ? '#b45309' : '#1f2937',
            color: pendingCount > 0 ? '#fde68a' : '#6b7280',
            fontSize: '12px',
            fontWeight: 600,
            letterSpacing: '0.02em',
            border: pendingCount > 0 ? '1px solid #92400e' : '1px solid #374151',
          }}>
            {pendingCount}
          </span>
        )}
      </div>
      <p style={{ color: '#6b7280', fontSize: '13px', marginTop: '-20px', marginBottom: '28px' }}>
        Production flag changes require approval from a second team member.
      </p>

      {/* Loading state */}
      {loading && (
        <div style={{ color: '#4b5563', fontSize: '14px', padding: '12px 0' }}>
          Loading...
        </div>
      )}

      {/* Empty state */}
      {!loading && pendingCount === 0 && (
        <EmptyState
          icon={<CheckCircleIcon />}
          heading="No pending approvals"
          body="All flag changes have been reviewed. The queue is clear."
        />
      )}

      {/* Approval cards */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
        {requests.map(req => (
          <ApprovalCard
            key={req.id}
            req={req}
            acting={acting}
            onApprove={approve}
            onReject={reject}
          />
        ))}
      </div>
    </div>
  );
}

interface ApprovalCardProps {
  req: ChangeRequest;
  acting: string | null;
  onApprove: (id: string) => void;
  onReject: (id: string) => void;
}

function ApprovalCard({ req, acting, onApprove, onReject }: ApprovalCardProps) {
  const [hovered, setHovered] = useState(false);
  const isActing = acting === req.id;

  const cardStyle: React.CSSProperties = {
    background: '#111111',
    border: `1px solid ${hovered ? '#374151' : '#1a1a1a'}`,
    borderRadius: '16px',
    padding: '20px 24px',
    transition: 'border-color 0.18s ease',
    cursor: 'default',
  };

  return (
    <div
      style={cardStyle}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: '16px' }}>

        {/* Left: metadata */}
        <div style={{ flex: 1, minWidth: 0 }}>

          {/* Flag key + environment badge */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '8px', flexWrap: 'wrap' }}>
            <span style={{
              fontFamily: '"SF Mono", "Fira Code", "Fira Mono", monospace',
              fontSize: '13px',
              fontWeight: 600,
              color: '#60a5fa',
              letterSpacing: '0.01em',
            }}>
              {req.flagKey}
            </span>
            <span style={{
              fontSize: '11px',
              fontWeight: 500,
              padding: '2px 8px',
              borderRadius: '6px',
              background: 'rgba(180, 83, 9, 0.18)',
              color: '#fbbf24',
              border: '1px solid rgba(146, 64, 14, 0.5)',
              letterSpacing: '0.03em',
              textTransform: 'uppercase' as const,
            }}>
              {req.environment}
            </span>
          </div>

          {/* Change description */}
          <p style={{
            color: '#d1d5db',
            fontSize: '14px',
            lineHeight: '1.5',
            margin: '0 0 10px 0',
          }}>
            {req.changeDescription}
          </p>

          {/* Requester + timestamp */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '16px', flexWrap: 'wrap' }}>
            <span style={{ color: '#6b7280', fontSize: '12px', display: 'flex', alignItems: 'center', gap: '5px' }}>
              <UserIcon />
              {req.requestedBy}
            </span>
            {req.createdAt > 0 && (
              <span style={{ color: '#4b5563', fontSize: '12px', display: 'flex', alignItems: 'center', gap: '5px' }}>
                <ClockIcon />
                {formatTimestamp(req.createdAt)}
              </span>
            )}
          </div>
        </div>

        {/* Right: action buttons */}
        <div style={{ display: 'flex', gap: '8px', flexShrink: 0, alignItems: 'center', marginTop: '2px' }}>
          <ActionButton
            label={isActing ? '...' : 'Approve'}
            disabled={isActing}
            variant="approve"
            onClick={() => onApprove(req.id)}
          />
          <ActionButton
            label="Reject"
            disabled={isActing}
            variant="reject"
            onClick={() => onReject(req.id)}
          />
        </div>
      </div>
    </div>
  );
}

interface ActionButtonProps {
  label: string;
  disabled: boolean;
  variant: 'approve' | 'reject';
  onClick: () => void;
}

function ActionButton({ label, disabled, variant, onClick }: ActionButtonProps) {
  const [hovered, setHovered] = useState(false);

  const base: React.CSSProperties = {
    padding: '6px 16px',
    borderRadius: '8px',
    fontSize: '13px',
    fontWeight: 600,
    cursor: disabled ? 'not-allowed' : 'pointer',
    opacity: disabled ? 0.45 : 1,
    transition: 'background 0.15s ease, border-color 0.15s ease, color 0.15s ease',
    letterSpacing: '0.01em',
    background: 'transparent',
    outline: 'none',
  };

  const approveStyle: React.CSSProperties = {
    ...base,
    border: `1px solid ${hovered && !disabled ? '#4ade80' : '#166534'}`,
    color: hovered && !disabled ? '#4ade80' : '#22c55e',
  };

  const rejectStyle: React.CSSProperties = {
    ...base,
    border: `1px solid ${hovered && !disabled ? '#f87171' : '#7f1d1d'}`,
    color: hovered && !disabled ? '#f87171' : '#ef4444',
  };

  return (
    <button
      onClick={onClick}
      disabled={disabled}
      style={variant === 'approve' ? approveStyle : rejectStyle}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {label}
    </button>
  );
}
