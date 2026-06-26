// workspace-dashboard/src/components/ui/EmptyState.tsx
import type { ReactNode } from 'react';
import { cn } from '../../lib/utils.js';

interface EmptyStateProps {
  icon?: ReactNode;
  heading: string;
  body?: string;
  action?: ReactNode;
  className?: string;
}

export function EmptyState({
  icon,
  heading,
  body,
  action,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        'card-surface flex flex-col items-center justify-center gap-6 py-16 px-6 text-center',
        className,
      )}
    >
      {icon && (
        <div className="flex items-center justify-center w-16 h-16 rounded-lg bg-[color-mix(in_oklab,var(--color-accent)_8%,transparent)]">
          {icon}
        </div>
      )}

      <div className="space-y-2">
        <h3 className="text-lg font-semibold text-[var(--color-fg)]">{heading}</h3>
        {body && (
          <p className="text-sm text-[var(--color-fg-muted)] max-w-xs">{body}</p>
        )}
      </div>

      {action && <div>{action}</div>}
    </div>
  );
}
