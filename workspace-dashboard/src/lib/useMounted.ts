import { useSyncExternalStore } from 'react';

/**
 * Returns true if the component is mounted on the client.
 * Safe for SSR: Returns false on server, true after hydration.
 *
 * Uses useSyncExternalStore to ensure consistency between server and client renders.
 */
export function useMounted(): boolean {
  return useSyncExternalStore(
    () => {
      // Subscribe: no-op, mounted state never changes after initial mount
      return () => {};
    },
    () => {
      // Server snapshot (SSR): always false
      return false;
    },
    () => {
      // Client snapshot: always true after hydration
      return true;
    },
  );
}
