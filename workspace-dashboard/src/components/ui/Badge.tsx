// workspace-dashboard/src/components/ui/Badge.tsx
type BadgeVariant = 'active' | 'draft' | 'complete' | 'archived' | 'risk-low' | 'risk-medium' | 'risk-high' | 'risk-blocked';

interface BadgeProps {
  variant: BadgeVariant;
  children: React.ReactNode;
  dot?: boolean;
}

export function Badge({ variant, children, dot }: BadgeProps) {
  return (
    <span className={`badge badge-${variant}`}>
      {dot && <span className={`status-dot status-dot-${variant === 'active' ? 'on' : 'off'}`} />}
      {children}
    </span>
  );
}
