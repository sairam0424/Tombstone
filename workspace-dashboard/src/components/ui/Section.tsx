// workspace-dashboard/src/components/ui/Section.tsx
import type { ReactNode } from 'react';
import { cn } from '../../lib/utils.js';

interface SectionProps {
  id?: string;
  label?: string;
  title?: string;
  titleAs?: 'h1' | 'h2';
  children: ReactNode;
  className?: string;
}

export function Section({
  id,
  label,
  title,
  titleAs: TitleTag = 'h2',
  children,
  className,
}: SectionProps) {
  return (
    <section id={id} className={cn('space-y-4', className)}>
      {(label || title) && (
        <div className="space-y-2">
          {label && (
            <p className="text-xs font-semibold uppercase tracking-widest text-[var(--color-fg-muted)]">
              {label}
            </p>
          )}
          {title && (
            <TitleTag className="text-2xl font-semibold text-[var(--color-fg)]">
              {title}
            </TitleTag>
          )}
        </div>
      )}
      {children}
    </section>
  );
}
