import { useEffect, useRef } from 'react';
import { motion, MotionConfig, useReducedMotion } from 'motion/react';
import type { Density } from '../hooks/useDensity.js';

interface Props {
  density: Density;
  onChange: (d: Density) => void;
}

const OPTIONS: { value: Density; label: string; key: string; title: string }[] = [
  { value: 'condensed', label: 'C', key: 'c', title: 'Condensed (C)' },
  { value: 'normal',    label: 'N', key: 'n', title: 'Normal (N)' },
  { value: 'spacious',  label: 'S', key: 's', title: 'Spacious (S)' },
];

export function DensityToggle({ density, onChange }: Props) {
  const reduced = useReducedMotion();

  // Stabilise onChange via ref so the keydown listener never needs to re-register
  const onChangeRef = useRef(onChange);
  useEffect(() => { onChangeRef.current = onChange; });

  // Keyboard shortcuts: C / N / S (only when not focused in input / contenteditable)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const active = document.activeElement;
      const isInput =
        active instanceof HTMLInputElement ||
        active instanceof HTMLTextAreaElement ||
        (active instanceof HTMLElement && active.isContentEditable);
      if (isInput) return;
      const opt = OPTIONS.find(o => o.key === e.key.toLowerCase());
      if (opt) onChangeRef.current(opt.value);
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, []); // stable — onChange updates via ref above

  return (
    <MotionConfig reducedMotion="user">
    <div
      role="group"
      aria-label="Row density"
      style={{
        display: 'flex',
        gap: 2,
        padding: '3px',
        background: 'var(--color-bg-elevated)',
        border: '1px solid var(--color-border)',
        borderRadius: 8,
      }}
    >
      {OPTIONS.map(opt => {
        const active = density === opt.value;
        return (
          <motion.button
            key={opt.value}
            title={opt.title}
            aria-pressed={active}
            onClick={() => onChange(opt.value)}
            whileTap={reduced ? undefined : { scale: 0.93 }}
            transition={{ type: 'spring', stiffness: 600, damping: 35 }}
            style={{
              width: 28, height: 24,
              borderRadius: 5,
              border: 'none',
              background: active ? 'var(--color-accent-subtle)' : 'transparent',
              color: active ? 'var(--color-accent)' : 'var(--color-fg-subtle)',
              fontSize: 11,
              fontWeight: 600,
              cursor: 'pointer',
              letterSpacing: '0.04em',
              transition: 'background 0.12s, color 0.12s',
            }}
          >
            {opt.label}
          </motion.button>
        );
      })}
    </div>
    </MotionConfig>
  );
}
