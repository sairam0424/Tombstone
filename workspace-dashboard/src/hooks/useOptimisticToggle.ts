import { useOptimistic, useTransition } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { API_URL, SDK_TOKEN } from '../config.js';

type Env = 'development' | 'staging' | 'production';

interface ToggleState {
  enabled: boolean;
  rolloutPct: number;
}

async function patchFlagEnv(flagKey: string, env: Env, enabled: boolean): Promise<void> {
  const res = await fetch(`${API_URL}/api/v1/flags/${flagKey}/environments/${env}`, {
    method: 'PATCH',
    headers: {
      Authorization: `Bearer ${SDK_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) throw new Error(`Toggle failed: ${res.status}`);
}

export function useOptimisticToggle(
  flagKey: string,
  env: Env,
  initialState: ToggleState,
) {
  const queryClient = useQueryClient();
  const [isPending, startTransition] = useTransition();

  // optimisticState shows immediately; reverts to server state on commit/error
  const [optimisticState, updateOptimistic] = useOptimistic(
    initialState,
    (_current, next: ToggleState) => next,
  );

  const mutation = useMutation({
    mutationFn: ({ enabled }: { enabled: boolean }) =>
      patchFlagEnv(flagKey, env, enabled),
    onSuccess: () => {
      // Invalidate snapshot query so the list re-fetches server truth
      queryClient.invalidateQueries({
        queryKey: ['snapshot', env],
      });
      toast.success(`Flag ${optimisticState.enabled ? 'enabled' : 'disabled'}`, {
        description: flagKey,
      });
    },
    onError: (err) => {
      toast.error('Toggle failed', { description: String(err) });
      // TanStack Query automatically rolls back via invalidation
      queryClient.invalidateQueries({ queryKey: ['snapshot', env] });
    },
  });

  const toggle = () => {
    const next = { enabled: !optimisticState.enabled, rolloutPct: optimisticState.rolloutPct };
    // Correct React 19 pattern: wrap both optimistic update AND async action in startTransition
    startTransition(async () => {
      updateOptimistic(next);
      // Post-await setState loses Transition context — mutation handles its own state
      await mutation.mutateAsync({ enabled: next.enabled });
    });
  };

  return {
    enabled: optimisticState.enabled,
    rolloutPct: optimisticState.rolloutPct,
    toggle,
    isPending: isPending || mutation.isPending,
  };
}
