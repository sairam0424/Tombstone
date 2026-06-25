import { motion, AnimatePresence } from 'motion/react';
import { useSSE } from '../hooks/useSSE.js';

interface ConnectionStatusProps {
  env: string;
}

export function ConnectionStatus({ env }: ConnectionStatusProps) {
  const { connected } = useSSE(env);

  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={connected ? 'live' : 'offline'}
        initial={{ opacity: 0, scale: 0.85, y: -4 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        exit={{ opacity: 0, scale: 0.85, y: 4 }}
        transition={{ type: 'spring', stiffness: 400, damping: 28, mass: 0.6 }}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: '6px',
          padding: '3px 10px 3px 8px',
          borderRadius: '20px',
          fontSize: '12px',
          fontWeight: '600',
          letterSpacing: '0.02em',
          background: connected ? 'rgba(34,197,94,0.10)' : 'rgba(239,68,68,0.10)',
          border: `1px solid ${connected ? 'rgba(34,197,94,0.25)' : 'rgba(239,68,68,0.25)'}`,
          color: connected ? '#4ade80' : '#f87171',
          userSelect: 'none',
          flexShrink: 0,
        }}
      >
        {/* Ripple dot container */}
        <span style={{ position: 'relative', width: '8px', height: '8px', flexShrink: 0 }}>
          {/* Outer ripple — only visible when live */}
          {connected && (
            <motion.span
              style={{
                position: 'absolute',
                inset: 0,
                borderRadius: '50%',
                background: '#22c55e',
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
              background: connected ? '#22c55e' : '#ef4444',
              boxShadow: connected ? '0 0 6px #22c55e' : undefined,
            }}
          />
        </span>

        {connected ? 'Live' : 'Offline'}
      </motion.div>
    </AnimatePresence>
  );
}
