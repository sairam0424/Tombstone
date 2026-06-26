// workspace-dashboard/src/components/ui/Reveal.tsx
import { motion } from 'motion/react';
import type { ReactNode } from 'react';
import { useMounted } from '../../lib/useMounted.js';
import { useReducedMotion } from '../../lib/useReducedMotion.js';

interface RevealProps {
  children: ReactNode;
  delay?: number;
  className?: string;
}

export function Reveal({ children, delay = 0, className = '' }: RevealProps) {
  const isMounted = useMounted();
  const prefersReducedMotion = useReducedMotion();

  // Static fallback when not mounted or motion is reduced
  if (!isMounted || prefersReducedMotion) {
    return <div className={className}>{children}</div>;
  }

  return (
    <motion.div
      className={className}
      initial={{ opacity: 0, y: 16 }}
      whileInView={{ opacity: 1, y: 0 }}
      viewport={{ once: true, margin: '-80px' }}
      transition={{ duration: 0.5, delay, ease: [0.21, 0.47, 0.32, 0.98] }}
    >
      {children}
    </motion.div>
  );
}
