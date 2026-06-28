// workspace-dashboard/src/components/ui/Card.tsx
import { motion } from 'motion/react';
import type { ReactNode } from 'react';

interface CardProps {
  children: ReactNode;
  className?: string;
  hover?: boolean;
  glow?: boolean;
  onClick?: () => void;
}

export function Card({ children, className = '', hover = false, glow = false, onClick }: CardProps) {
  const base = 'card-surface p-5';
  const glowStyle = glow ? { boxShadow: 'var(--glow-accent)' } : {};

  if (!hover) {
    return <div className={`${base} ${className}`} style={glowStyle} onClick={onClick}>{children}</div>;
  }
  return (
    <motion.div
      className={`${base} ${className} cursor-pointer`}
      style={glowStyle}
      whileHover={{ scale: 1.015, borderColor: 'var(--color-border-strong)' }}
      transition={{ type: 'spring', stiffness: 400, damping: 30 }}
      onClick={onClick}
    >
      {children}
    </motion.div>
  );
}
