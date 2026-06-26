// workspace-dashboard/src/components/ui/Section.tsx
import type { ReactNode } from 'react';

interface SectionProps {
  eyebrow?: string;
  heading: string;
  children: ReactNode;
  className?: string;
}

export function Section({
  eyebrow,
  heading,
  children,
  className = '',
}: SectionProps) {
  return (
    <section className={`space-y-4 ${className}`}>
      <div className="space-y-2">
        {eyebrow && (
          <p className="text-xs font-semibold uppercase tracking-widest text-[var(--color-fg-muted)]">
            {eyebrow}
          </p>
        )}
        <h2 className="text-2xl font-semibold text-[var(--color-fg)]">
          {heading}
        </h2>
      </div>
      {children}
    </section>
  );
}
