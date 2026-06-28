import { useEffect, useCallback } from 'react';

type ShortcutMap = Record<string, () => void>;

export function useKeyboard(shortcuts: ShortcutMap) {
  const handler = useCallback((e: KeyboardEvent) => {
    const active = document.activeElement;
    const isInput = active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement;

    for (const [combo, fn] of Object.entries(shortcuts)) {
      const parts = combo.toLowerCase().split('+');
      const key = parts[parts.length - 1];
      const needsMeta = parts.includes('cmd') || parts.includes('meta');
      const needsCtrl = parts.includes('ctrl');
      const needsShift = parts.includes('shift');
      const needsAlt = parts.includes('alt');

      const keyMatch = e.key.toLowerCase() === key || e.code.toLowerCase() === `key${key}`;
      const metaMatch = !needsMeta || (e.metaKey || e.ctrlKey);
      const ctrlMatch = !needsCtrl || e.ctrlKey;
      const shiftMatch = !needsShift || e.shiftKey;
      const altMatch = !needsAlt || e.altKey;

      // Skip letter shortcuts when typing in inputs (allow Cmd+K always)
      if (isInput && !needsMeta && !needsCtrl && key.length === 1) continue;

      if (keyMatch && metaMatch && ctrlMatch && shiftMatch && altMatch) {
        e.preventDefault();
        fn();
        break;
      }
    }
  }, [shortcuts]);

  useEffect(() => {
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [handler]);
}
