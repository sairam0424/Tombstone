import { useState, useEffect, useCallback } from 'react';
import type { FlagListItem } from '../types.js';
import { API_URL, GATEWAY_URL, SDK_TOKEN } from '../config.js';

export function useFlags(environment: string, projectId?: string) {
  const [flags, setFlags] = useState<FlagListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const apiUrl = API_URL;
  const gatewayUrl = GATEWAY_URL;
  const token = SDK_TOKEN;

  const fetchFlags = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const url = new URL(`${apiUrl}/api/v1/flags`);
      if (projectId) url.searchParams.set('project_id', projectId);
      url.searchParams.set('environment', environment);

      const res = await globalThis.fetch(url.toString(), {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);

      const data = await res.json() as { flags?: Array<Record<string, unknown>> };
      const now = Math.floor(Date.now() / 1000);
      const thirtyDaysAgo = now - 30 * 86400;

      const items: FlagListItem[] = (data.flags ?? []).map(f => ({
        id:          String(f['id'] ?? ''),
        key:         String(f['key'] ?? ''),
        name:        String(f['name'] ?? ''),
        description: String(f['description'] ?? ''),
        state:       (f['state'] ?? 'DRAFT') as FlagListItem['state'],
        ownerId:     String(f['owner_id'] ?? ''),
        enabled:     Boolean(f['enabled']),
        rolloutPct:  Number(f['rollout_pct'] ?? 0),
        environment,
        createdAt:   Number(f['created_at'] ?? 0),
        updatedAt:   Number(f['updated_at'] ?? 0),
        isStale:     Number(f['updated_at'] ?? 0) < thirtyDaysAgo && Number(f['rollout_pct'] ?? 0) === 100,
        isOrphaned:  false,
      }));

      setFlags(items);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch flags');
    } finally {
      setLoading(false);
    }
  }, [environment, projectId, apiUrl, token]);

  useEffect(() => {
    void fetchFlags();

    // SSE listener for real-time updates
    const es = new EventSource(
      `${gatewayUrl}/api/v1/stream?environment=${environment}`
    );
    es.addEventListener('flag_updated', () => void fetchFlags());
    es.addEventListener('kill_switch', () => void fetchFlags());

    // Fallback polling every 30s
    const poll = setInterval(() => void fetchFlags(), 30_000);

    return () => {
      es.close();
      clearInterval(poll);
    };
  }, [fetchFlags, environment, gatewayUrl]);

  return { flags, loading, error, refetch: fetchFlags };
}
