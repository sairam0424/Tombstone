// workspace-dashboard/src/hooks/useFeatureFlags.ts
import { useQuery } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../config.js';

interface EnvFlagState {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
}

export function useFeatureFlags(): Record<string, boolean> {
  const { data } = useQuery({
    queryKey: ['feature-flags', 'production'],
    queryFn: async (): Promise<Record<string, boolean>> => {
      const r = await fetch(
        `${API_URL}/api/v1/environments/snapshot?environment=production`,
        { headers: { Authorization: `Bearer ${SDK_TOKEN}` } },
      );
      if (!r.ok) return {};
      const d = await r.json() as { flags?: EnvFlagState[] };
      const map: Record<string, boolean> = {};
      for (const f of (d.flags ?? [])) {
        if (f.flag_key.startsWith('feature-')) {
          map[f.flag_key] = f.enabled;
        }
      }
      return map;
    },
    staleTime: 60_000,
    gcTime: 300_000,
    retry: 1,
  });
  return data ?? {};
}

export function useFeatureFlag(key: string): boolean {
  const flags = useFeatureFlags();
  return flags[key] ?? false;
}
