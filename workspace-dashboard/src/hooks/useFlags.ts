import { useQuery } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../config.js';

type Env = 'development' | 'staging' | 'production';

export interface FlagItem {
  id: string;
  key: string;
  name: string;
  description: string;
  state: string;
  owner_id: string;
  flag_type: string;
}

export interface EnvState {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
}

const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

export function useFlags() {
  return useQuery({
    queryKey: ['flags'],
    queryFn: async (): Promise<FlagItem[]> => {
      const r = await fetch(`${API_URL}/api/v1/flags`, { headers: hdrs });
      if (!r.ok) throw new Error(`Flags fetch failed: ${r.status}`);
      const d = await r.json() as { flags?: FlagItem[] };
      return d.flags ?? [];
    },
  });
}

export function useEnvSnapshot(env: Env) {
  return useQuery({
    queryKey: ['snapshot', env],
    queryFn: async (): Promise<Record<string, EnvState>> => {
      const r = await fetch(
        `${API_URL}/api/v1/environments/snapshot?environment=${env}`,
        { headers: hdrs },
      );
      if (!r.ok) throw new Error(`Snapshot fetch failed: ${r.status}`);
      const d = await r.json() as { flags?: EnvState[] };
      const map: Record<string, EnvState> = {};
      for (const s of (d.flags ?? [])) map[s.flag_key] = s;
      return map;
    },
  });
}
