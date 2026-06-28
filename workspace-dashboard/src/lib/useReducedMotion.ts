import { useEffect, useState } from 'react';

/**
 * SSR-safe hook to detect prefers-reduced-motion media query.
 * Returns true if the user prefers reduced motion, false otherwise.
 *
 * Safe for SSR: Returns false on server, then hydrates to correct value on client.
 */
export function useReducedMotion(): boolean {
  const [reducedMotion, setReducedMotion] = useState(false);

  useEffect(() => {
    // Check initial state
    const mediaQuery = window.matchMedia('(prefers-reduced-motion: reduce)');
    setReducedMotion(mediaQuery.matches);

    // Listen for changes
    const handleChange = (e: MediaQueryListEvent): void => {
      setReducedMotion(e.matches);
    };

    mediaQuery.addEventListener('change', handleChange);
    return () => {
      mediaQuery.removeEventListener('change', handleChange);
    };
  }, []);

  return reducedMotion;
}
