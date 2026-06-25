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
