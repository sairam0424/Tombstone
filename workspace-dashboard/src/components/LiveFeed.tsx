import { AnimatePresence, motion } from 'motion/react';
import { Wifi, WifiOff, Activity } from 'lucide-react';
import { useSSE } from '../hooks/useSSE.js';

interface Props { env: string; }

const EVENT_COLORS: Record<string, string> = {
  flag_updated:  'var(--color-accent)',
  flag_enabled:  'var(--color-risk-low)',
  flag_disabled: 'var(--color-risk-high)',
  rollout:       'var(--color-risk-medium)',
  approved:      'var(--color-risk-low)',
  rejected:      'var(--color-risk-high)',
};

function timeAgo(ts: string) {
  const diff = Date.now() - new Date(ts).getTime();
  if (diff < 60000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  return `${Math.floor(diff / 3600000)}h ago`;
}

export function LiveFeed({ env }: Props) {
  const { events, connected } = useSSE(env);

  return (
    <div style={{
      width: 280, flexShrink: 0,
      background: 'var(--color-bg-surface)',
      borderLeft: '1px solid var(--color-border)',
      display: 'flex', flexDirection: 'column',
      overflow: 'hidden',
    }}>
      {/* Header */}
      <div style={{
        padding: '12px 16px',
        borderBottom: '1px solid var(--color-border)',
        display: 'flex', alignItems: 'center', gap: 8,
      }}>
        <Activity size={14} color="var(--color-fg-subtle)" />
        <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--color-fg)', flex: 1 }}>Live Feed</span>
        <span style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 11, color: connected ? 'var(--color-risk-low)' : 'var(--color-fg-subtle)' }}>
          {connected ? <Wifi size={11} /> : <WifiOff size={11} />}
          {connected ? 'live' : 'offline'}
        </span>
      </div>

      {/* Events */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '8px 0' }}>
        {events.length === 0 ? (
          <div style={{ padding: '32px 16px', textAlign: 'center', color: 'var(--color-fg-subtle)', fontSize: 12 }}>
            {connected ? 'Waiting for events…' : 'Not connected'}
          </div>
        ) : (
          <AnimatePresence initial={false}>
            {events.map(ev => (
              <motion.div
                key={ev.id}
                initial={{ opacity: 0, x: 20, height: 0 }}
                animate={{ opacity: 1, x: 0, height: 'auto' }}
                exit={{ opacity: 0, height: 0 }}
                transition={{ type: 'spring', stiffness: 400, damping: 30 }}
                style={{ padding: '8px 16px', borderBottom: '1px solid var(--color-border)' }}
              >
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
                  <div style={{
                    width: 6, height: 6, borderRadius: '50%', marginTop: 5, flexShrink: 0,
                    background: EVENT_COLORS[ev.type] ?? 'var(--color-fg-subtle)',
                    boxShadow: `0 0 6px ${EVENT_COLORS[ev.type] ?? 'transparent'}`,
                  }} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <code style={{ fontSize: 11, color: 'var(--color-accent)', fontFamily: 'var(--font-mono)', display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {ev.flagKey}
                    </code>
                    <div style={{ fontSize: 11, color: 'var(--color-fg-subtle)', marginTop: 2 }}>
                      {ev.type.replace(/_/g, ' ')}
                    </div>
                  </div>
                  <div style={{ fontSize: 10, color: 'var(--color-fg-subtle)', flexShrink: 0, marginTop: 1 }}>
                    {timeAgo(ev.timestamp)}
                  </div>
                </div>
              </motion.div>
            ))}
          </AnimatePresence>
        )}
      </div>
    </div>
  );
}
