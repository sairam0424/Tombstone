import { useState, useEffect } from "react";
import { GATEWAY_URL, SDK_TOKEN } from "../config.js";

export interface SSEEvent {
  id: string;
  type: string;
  flagKey: string;
  environment: string;
  timestamp: string;
  payload: unknown;
}

const MAX_EVENTS = 50;

export function useSSE(env: string) {
  const [events, setEvents] = useState<SSEEvent[]>([]);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    const url = `${GATEWAY_URL}/api/v1/stream?environment=${env}&sdk_key=${SDK_TOKEN}`;
    const es = new EventSource(url);

    es.onopen = () => setConnected(true);
    // This comment used to claim "no auto-reconnect implemented by design,"
    // which was never true: onerror only clears `connected` here, it never
    // calls es.close(), so the browser's native EventSource DOES keep
    // auto-reconnecting on its own per the WHATWG spec (fixed by
    // adversarial review of Tombstone PR #211, which found this while
    // checking whether GW-2's new SSE id:/Last-Event-ID support has any
    // real client-visible effect yet -- it does, here: once gateway starts
    // sending id: lines, this EventSource's own browser-tracked
    // lastEventId updates silently, and the NEXT auto-reconnect will
    // automatically send it back as Last-Event-ID, with zero code change
    // needed in this file). ConnectionStatus shows connection health in the
    // meantime; a manual page refresh is only needed if auto-reconnect
    // itself is failing (e.g. gateway fully down), not for an ordinary
    // transient drop.
    es.onerror = () => setConnected(false);

    es.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data) as SSEEvent;
        setEvents((prev) => [data, ...prev].slice(0, MAX_EVENTS));
      } catch {
        /* ignore malformed */
      }
    };

    return () => {
      es.close();
      setConnected(false);
    };
  }, [env]);

  return { events, connected };
}
