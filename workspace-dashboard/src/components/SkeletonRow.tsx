import { useReducedMotion } from '../lib/useReducedMotion.js';

interface SkeletonBoxProps {
  width?: string | number;
  height?: number;
  className?: string;
}

export function SkeletonBox({ width = '100%', height = 14, className = '' }: SkeletonBoxProps) {
  return (
    <div
      className={`skeleton-shimmer rounded ${className}`}
      style={{ width, height, borderRadius: 4 }}
    />
  );
}

export function SkeletonRow() {
  return (
    <div
      className="flex items-center gap-4 px-4"
      style={{ height: 52, borderBottom: '1px solid var(--color-border)' }}
    >
      <SkeletonBox width={180} height={13} />
      <SkeletonBox width={52} height={13} />
      <SkeletonBox width={120} height={6} />
      <SkeletonBox width={64} height={22} />
      <SkeletonBox width={64} height={22} />
      <SkeletonBox width={80} height={13} />
      <SkeletonBox width={48} height={13} />
    </div>
  );
}

export function SkeletonCard() {
  return (
    <div className="card-surface p-5 flex flex-col gap-3">
      <SkeletonBox width="60%" height={16} />
      <SkeletonBox width="90%" height={12} />
      <SkeletonBox width="40%" height={12} />
    </div>
  );
}

/**
 * Matches GovernanceDash stat card layout: [icon circle] [value] [label].
 * Ported from Anvilry SkeletonStatCard.
 */
export function SkeletonStatCard() {
  return (
    <div
      className="card-surface"
      style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px' }}
      aria-hidden="true"
    >
      <div
        className="skeleton-shimmer"
        style={{ width: 32, height: 32, borderRadius: '50%', flexShrink: 0 }}
      />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1 }}>
        <div className="skeleton-shimmer" style={{ height: 14, width: 40, borderRadius: 4 }} />
        <div className="skeleton-shimmer" style={{ height: 10, width: 80, borderRadius: 4 }} />
      </div>
    </div>
  );
}

/**
 * Full-viewport skeleton for view-switching fallbacks.
 * Pulsing cyan orb ring + skeleton lines. Ported from Anvilry.
 */
export function SkeletonViewTransition({ label }: { label?: string }) {
  const reduced = useReducedMotion();
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        height: 'calc(100vh - 60px)',
        gap: 24,
      }}
      role="status"
      aria-label={label ?? 'Loading…'}
    >
      {/* Orb ring — matches Anvilry accent aesthetics */}
      <div
        style={{
          width: 64, height: 64, borderRadius: '50%',
          border: '1px solid color-mix(in oklab, var(--color-accent) 30%, transparent)',
          background: 'color-mix(in oklab, var(--color-accent) 5%, transparent)',
          animation: reduced ? 'none' : 'pulse 2s ease-in-out infinite',
        }}
        aria-hidden="true"
      />
      <div style={{ width: 160, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div className="skeleton-shimmer" style={{ height: 10, width: '100%', borderRadius: 999 }} />
        <div className="skeleton-shimmer" style={{ height: 10, width: '75%', borderRadius: 999, margin: '0 auto' }} />
        <div className="skeleton-shimmer" style={{ height: 10, width: '50%', borderRadius: 999, margin: '0 auto' }} />
      </div>
      <span className="sr-only">{label ?? 'Loading…'}</span>
    </div>
  );
}
