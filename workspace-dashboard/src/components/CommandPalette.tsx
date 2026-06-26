// workspace-dashboard/src/components/CommandPalette.tsx
import { Command } from 'cmdk';
import { useNavigate } from 'react-router-dom';
import { Flag, Zap, CheckCircle, Shield, BarChart2, FlaskConical, GitBranch, X } from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';

interface FlagItem { key: string; name: string; state: string; }

interface Props {
  open: boolean;
  onClose: () => void;
  flags: FlagItem[];
}

const NAV_ITEMS = [
  { label: 'All Flags',     href: '/',            icon: Flag },
  { label: 'What Changed?', href: '/incident',     icon: Zap },
  { label: 'Approvals',     href: '/approvals',    icon: CheckCircle },
  { label: 'Break-Glass',   href: '/break-glass',  icon: Shield },
  { label: 'Governance',    href: '/governance',   icon: BarChart2 },
  { label: 'Experiments',   href: '/experiments',  icon: FlaskConical },
  { label: 'Causal Graph',  href: '/graph',        icon: GitBranch },
];

export function CommandPalette({ open, onClose, flags }: Props) {
  const navigate = useNavigate();

  const run = (fn: () => void) => { fn(); onClose(); };

  return (
    <AnimatePresence>
      {open && (
        <>
          {/* Backdrop */}
          <motion.div
            className="fixed inset-0 z-50"
            style={{ background: 'color-mix(in oklab, #000 60%, transparent)' }}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
            onClick={onClose}
          />
          {/* Palette */}
          <motion.div
            className="fixed z-50 left-1/2 -translate-x-1/2"
            style={{ top: '15vh', width: 600, maxWidth: 'calc(100vw - 32px)' }}
            initial={{ opacity: 0, scale: 0.96, y: -8 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.96, y: -8 }}
            transition={{ type: 'spring', stiffness: 500, damping: 35 }}
          >
            <Command
              className="rounded-xl overflow-hidden"
              style={{
                background: 'var(--color-bg-elevated)',
                border: '1px solid var(--color-border-strong)',
                boxShadow: 'var(--glow-accent), 0 24px 48px rgba(0,0,0,0.6)',
              }}
            >
              <div style={{ borderBottom: '1px solid var(--color-border)', padding: '12px 16px', display: 'flex', alignItems: 'center', gap: 8 }}>
                <Command.Input
                  placeholder="Search flags, navigate, take actions…"
                  style={{
                    flex: 1, background: 'transparent', border: 'none', outline: 'none',
                    fontSize: 15, color: 'var(--color-fg)', caretColor: 'var(--color-accent)',
                  }}
                  autoFocus
                />
                <button onClick={onClose} style={{ color: 'var(--color-fg-subtle)', cursor: 'pointer', border: 'none', background: 'none' }}>
                  <X size={16} />
                </button>
              </div>

              <Command.List style={{ maxHeight: 400, overflowY: 'auto', padding: '8px 0' }}>
                <Command.Empty style={{ padding: '32px 16px', textAlign: 'center', color: 'var(--color-fg-subtle)', fontSize: 13 }}>
                  No results.
                </Command.Empty>

                <Command.Group heading="Navigate" style={{ padding: '0 8px 8px' }}>
                  {NAV_ITEMS.map(item => {
                    const Icon = item.icon;
                    return (
                      <Command.Item
                        key={item.href}
                        value={item.label}
                        onSelect={() => run(() => navigate(item.href))}
                        style={{
                          display: 'flex', alignItems: 'center', gap: 10, padding: '8px 10px',
                          borderRadius: 8, cursor: 'pointer', fontSize: 13, color: 'var(--color-fg)',
                        }}
                      >
                        <Icon size={15} color="var(--color-fg-subtle)" />
                        {item.label}
                      </Command.Item>
                    );
                  })}
                </Command.Group>

                {flags.length > 0 && (
                  <Command.Group heading="Flags" style={{ padding: '0 8px 8px' }}>
                    {flags.slice(0, 20).map(flag => (
                      <Command.Item
                        key={flag.key}
                        value={`${flag.key} ${flag.name}`}
                        onSelect={() => run(() => navigate(`/flags/${flag.key}`))}
                        style={{
                          display: 'flex', alignItems: 'center', gap: 10, padding: '8px 10px',
                          borderRadius: 8, cursor: 'pointer', fontSize: 13, color: 'var(--color-fg)',
                        }}
                      >
                        <Flag size={13} color="var(--color-accent)" />
                        <code style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--color-accent)' }}>
                          {flag.key}
                        </code>
                        {flag.name !== flag.key && (
                          <span style={{ color: 'var(--color-fg-subtle)', fontSize: 12 }}>{flag.name}</span>
                        )}
                      </Command.Item>
                    ))}
                  </Command.Group>
                )}

              </Command.List>

              <div style={{ borderTop: '1px solid var(--color-border)', padding: '8px 16px', display: 'flex', gap: 16 }}>
                {[['↵', 'Select'], ['↑↓', 'Navigate'], ['esc', 'Close']].map(([key, label]) => (
                  <span key={key} style={{ fontSize: 11, color: 'var(--color-fg-subtle)', display: 'flex', alignItems: 'center', gap: 4 }}>
                    <kbd style={{ padding: '1px 5px', borderRadius: 3, background: 'var(--color-bg-surface)', border: '1px solid var(--color-border)' }}>{key}</kbd>
                    {label}
                  </span>
                ))}
              </div>
            </Command>
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
}
