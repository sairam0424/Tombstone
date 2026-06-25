// workspace-dashboard/src/components/ui/Button.tsx
import { motion } from 'motion/react';
import type { ReactNode, ButtonHTMLAttributes } from 'react';

type Variant = 'primary' | 'ghost' | 'danger' | 'outline';
type Size = 'sm' | 'md' | 'lg';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
  children?: ReactNode;
}

const VARIANT_STYLES: Record<Variant, string> = {
  primary: 'bg-[#38e1ff] text-[#07080d] font-semibold hover:bg-[#0fb8db]',
  ghost:   'bg-transparent text-[var(--color-fg-muted)] hover:bg-[color-mix(in_oklab,var(--color-fg)_4%,transparent)] hover:text-[var(--color-fg)]',
  danger:  'bg-[color-mix(in_oklab,var(--color-risk-high)_12%,transparent)] text-[var(--color-risk-high)] border border-[color-mix(in_oklab,var(--color-risk-high)_25%,transparent)] hover:bg-[color-mix(in_oklab,var(--color-risk-high)_20%,transparent)]',
  outline: 'bg-transparent text-[var(--color-fg)] border border-[var(--color-border)] hover:border-[var(--color-border-strong)] hover:bg-[var(--color-bg-elevated)]',
};
const SIZE_STYLES: Record<Size, string> = {
  sm: 'text-xs px-3 py-1.5 rounded-md gap-1.5',
  md: 'text-sm px-4 py-2 rounded-lg gap-2',
  lg: 'text-base px-5 py-2.5 rounded-lg gap-2',
};

export function Button({ variant = 'primary', size = 'md', loading, icon, children, className = '', disabled, ...props }: ButtonProps) {
  return (
    <motion.button
      whileTap={{ scale: 0.97 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      className={`inline-flex items-center justify-center font-medium transition-colors duration-150 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed ${VARIANT_STYLES[variant]} ${SIZE_STYLES[size]} ${className}`}
      disabled={disabled || loading}
      {...(props as object)}
    >
      {loading ? (
        <span className="w-4 h-4 border-2 border-current border-t-transparent rounded-full animate-spin" />
      ) : icon}
      {children}
    </motion.button>
  );
}
