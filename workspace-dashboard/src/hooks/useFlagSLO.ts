import { useState, useEffect, useCallback } from 'react';

export type CircuitState = 'CLOSED' | 'OPEN' | 'HALF_OPEN';

export interface SLOHistoryPoint {
  ts: string;
  error_rate: number;
  circuit_state: CircuitState;
}

export interface FlagSLOData {
  flag_key: string;
  window: string;
  error_rate: number;
  p99_latency_ms: number;
  evaluation_count: number;
  circuit_trips: number;
  slo_budget_remaining: number;
  circuit_state: CircuitState;
  history: SLOHistoryPoint[];
}

export type SLOWindow = '7d' | '30d' | '90d';

export function useFlagSLO(flagKey: string, window: SLOWindow = '7d') {
  const [data, setData] = useState<FlagSLOData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const evaluatorUrl =
    (import.meta as unknown as { env: Record<string, string> }).env['VITE_EVALUATOR_URL'] ??
    'http://localhost:8082';
  const token =
    (import.meta as unknown as { env: Record<string, string> }).env['VITE_SDK_TOKEN'] ??
    'sdk-dev-token-change-in-prod';

  const fetchSLO = useCallback(async () => {
    if (!flagKey) return;
    setLoading(true);
    setError(null);
    try {
      const url = `${evaluatorUrl}/api/v1/flags/${encodeURIComponent(flagKey)}/slo?window=${window}`;
      const res = await globalThis.fetch(url, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const json = await res.json() as FlagSLOData;
      setData(json);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch SLO data');
    } finally {
      setLoading(false);
    }
  }, [flagKey, window, evaluatorUrl, token]);

  useEffect(() => {
    void fetchSLO();
  }, [fetchSLO]);

  return { data, loading, error, refetch: fetchSLO };
}
