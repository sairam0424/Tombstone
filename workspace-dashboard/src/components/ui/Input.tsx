// workspace-dashboard/src/components/ui/Input.tsx
import type { InputHTMLAttributes } from 'react';
import { useState } from 'react';

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  icon?: React.ReactNode;
  label?: string;
}

export function Input({ icon, label, className = '', ...props }: InputProps) {
  const [focused, setFocused] = useState(false);

  return (
    <div className="flex flex-col gap-1.5">
      {label && <label className="text-xs font-medium" style={{ color: 'var(--color-fg-muted)' }}>{label}</label>}
      <div
        className="flex items-center gap-2.5 px-3 rounded-lg transition-colors duration-150"
        style={{
          background: 'var(--color-bg-elevated)',
          border: `1px solid ${focused ? 'var(--color-accent)' : 'var(--color-border)'}`,
          boxShadow: focused ? 'var(--glow-accent)' : 'none',
          height: 38,
        }}
      >
        {icon && <span style={{ color: 'var(--color-fg-subtle)', flexShrink: 0 }}>{icon}</span>}
        <input
          className={`flex-1 bg-transparent text-sm outline-none placeholder-[var(--color-fg-subtle)] ${className}`}
          style={{ color: 'var(--color-fg)' }}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          {...props}
        />
      </div>
    </div>
  );
}
