type FlagState = 'DRAFT' | 'ACTIVE' | 'COMPLETE' | 'ARCHIVED';

interface Props {
  state: FlagState;
  isStale?: boolean;
  isOrphaned?: boolean;
}

export function FlagHealthBadge({ state, isStale, isOrphaned }: Props) {
  if (isOrphaned) {
    return (
      <span
        title="Owner no longer in org"
        className="text-xs px-1.5 py-0.5 rounded bg-red-900/40 text-red-400 border border-red-800"
      >
        ORPHANED
      </span>
    );
  }

  if (isStale) {
    return (
      <span
        title="At 100% rollout for 30+ days — candidate for cleanup"
        className="text-xs px-1.5 py-0.5 rounded bg-amber-900/40 text-amber-400 border border-amber-800"
      >
        STALE
      </span>
    );
  }

  const config: Record<FlagState, { bg: string; text: string }> = {
    ACTIVE:   { bg: 'bg-green-900/40 border-green-800', text: 'text-green-400' },
    DRAFT:    { bg: 'bg-gray-800/60 border-gray-700',   text: 'text-gray-400' },
    COMPLETE: { bg: 'bg-blue-900/40 border-blue-800',   text: 'text-blue-400' },
    ARCHIVED: { bg: 'bg-gray-800/40 border-gray-700',   text: 'text-gray-500' },
  };

  const c = config[state] ?? config['DRAFT'];
  return (
    <span className={`text-xs px-1.5 py-0.5 rounded border ${c.bg} ${c.text}`}>
      {state}
    </span>
  );
}
