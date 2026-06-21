import { useEffect, useState } from 'react';
import type { EvaluationContext, EvaluationResult } from '@tombstone/core';
import { useTombstoneContext } from './provider.js';

// Primary evaluation hook — synchronous, reactive on SSE flag updates.
// Returns the defaultValue immediately if the client is not ready.
export function useFlag<T = boolean>(
  flagKey: string,
  context: EvaluationContext,
  defaultValue: T,
): EvaluationResult<T> {
  const { client, ready } = useTombstoneContext();

  const evaluate = (): EvaluationResult<T> => {
    if (!client || !ready) {
      return { value: defaultValue, reason: 'ERROR', fromCache: false, flagKey };
    }
    return client.evaluate<T>(flagKey, context);
  };

  const [result, setResult] = useState<EvaluationResult<T>>(evaluate);

  useEffect(() => {
    setResult(evaluate());
    // Re-evaluate whenever client/ready state changes (e.g., after connect)
  }, [client, ready, flagKey]); // eslint-disable-line react-hooks/exhaustive-deps

  return result;
}

// Returns true when the client has connected and loaded the flag snapshot.
export function useTombstoneReady(): boolean {
  return useTombstoneContext().ready;
}

// Returns all flag keys currently in the local cache (useful for debugging).
export function useFlagKeys(): string[] {
  const { client, ready } = useTombstoneContext();
  if (!client || !ready) return [];
  return client.flagKeys();
}
