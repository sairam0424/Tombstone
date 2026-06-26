// workspace-dashboard/src/components/ui/EmptyState.tsx
import { motion } from 'motion/react';
import type { ReactNode } from 'react';

interface EmptyStateProps {
  icon?: ReactNode;
  heading: string;
  body: string;
  action?: {
    label: string;
    onClick: () => void;
    icon?: ReactNode;
  };
  className?: string;
}

export function EmptyState({
  icon,
  heading,
  body,
  action,
  className = '',
}: EmptyStateProps) {
  return (
    <motion.div
      className={`card-surface flex flex-col items-center justify-center gap-6 py-16 px-6 text-center ${className}`}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.3 }}
    >
      {icon && (
        <motion.div
          className="flex items-center justify-center w-16 h-16 rounded-lg bg-[color-mix(in_oklab,var(--color-accent)_8%,transparent)]"
          initial={{ scale: 0 }}
          animate={{ scale: 1 }}
          transition={{ type: 'spring', stiffness: 400, damping: 30, delay: 0.1 }}
        >
          {icon}
        </motion.div>
      )}

      <div className="space-y-2">
        <h3 className="text-lg font-semibold text-[var(--color-fg)]">{heading}</h3>
        <p className="text-sm text-[var(--color-fg-muted)] max-w-xs">{body}</p>
      </div>

      {action && (
        <motion.button
          type="button"
          className="inline-flex items-center justify-center gap-2 px-4 py-2 text-sm font-medium text-[#07080d] bg-[#38e1ff] rounded-lg hover:bg-[#0fb8db] transition-colors duration-150 cursor-pointer"
          initial={{ opacity: 0, translateY: 4 }}
          animate={{ opacity: 1, translateY: 0 }}
          transition={{ type: 'spring', stiffness: 400, damping: 30, delay: 0.2 }}
          whileTap={{ scale: 0.97 }}
          onClick={action.onClick}
        >
          {action.icon}
          {action.label}
        </motion.button>
      )}
    </motion.div>
  );
}
