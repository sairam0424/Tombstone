import { useState, useCallback } from 'react';

export type Density = 'condensed' | 'normal' | 'spacious';

const ROW_HEIGHTS: Record<Density, number> = {
  condensed: 32,
  normal:    52,
  spacious:  72,
};

const STORAGE_KEY = 'tombstone-density';

function readStored(): Density {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === 'condensed' || v === 'normal' || v === 'spacious') return v;
  } catch { /* SSR/private browsing */ }
  return 'normal';
}

export function useDensity() {
  const [density, setDensityState] = useState<Density>(readStored);

  const setDensity = useCallback((d: Density) => {
    setDensityState(d);
    try { localStorage.setItem(STORAGE_KEY, d); } catch { /* ignore */ }
  }, []);

  return { density, setDensity, rowHeight: ROW_HEIGHTS[density] };
}
