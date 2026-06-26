import { motion, AnimatePresence, useReducedMotion } from 'motion/react';
import { useSSE } from '../hooks/useSSE.js';

interface Props {
  env: string;
}

export function ConnectionStatus({ env }: Props) {
  const { connected } = useSSE(env);
  const reduced = useReducedMotion();

  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={connected ? 'live' : 'offline'}
        initial={reduced ? { opacity: 1, scale: 1, y: 0 } : { opacity: 0, scale: 0.9, y: 0 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        exit={reduced ? { opacity: 1, scale: 1, y: 0 } : { opacity: 0, scale: 0.9, y: 0 }}
        transition={{ type: 'spring', stiffness: 500, damping: 35 }}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: '6px',
          padding: '3px 10px 3px 8px',
          borderRadius: 'var(--radius-pill)',
          fontSize: '12px',
          fontWeight: '600',
          letterSpacing: '0.02em',
          background: connected
            ? 'color-mix(in oklab, var(--color-risk-low) 12%, transparent)'
            : 'color-mix(in oklab, var(--color-action-danger) 12%, transparent)',
          border: connected
            ? '1px solid color-mix(in oklab, var(--color-risk-low) 30%, transparent)'
            : '1px solid color-mix(in oklab, var(--color-action-danger) 30%, transparent)',
          color: connected ? 'var(--color-risk-low)' : 'var(--color-action-danger)',
          userSelect: 'none',
          flexShrink: 0,
        }}
      >
        {/* Ripple dot container */}
        <span style={{ position: 'relative', width: '8px', height: '8px', flexShrink: 0 }}>
          {/* Outer ripple — only visible when live and motion not reduced */}
          {connected && !reduced && (
            <motion.span
              style={{
                position: 'absolute',
                inset: 0,
                borderRadius: '50%',
                background: 'var(--color-risk-low)',
                opacity: 0,
              }}
              animate={{ scale: [1, 2], opacity: [0.5, 0] }}
              transition={{ duration: 1.6, repeat: Infinity, ease: 'easeOut' }}
            />
          )}
          {/* Core dot */}
          <span
            style={{
              position: 'absolute',
              inset: 0,
              borderRadius: '50%',
              background: connected ? 'var(--color-risk-low)' : 'var(--color-action-danger)',
              boxShadow: connected
                ? '0 0 6px color-mix(in oklab, var(--color-risk-low) 60%, transparent)'
                : undefined,
            }}
          />
        </span>

        {connected ? 'Live' : 'Offline'}
      </motion.div>
    </AnimatePresence>
  );
}
