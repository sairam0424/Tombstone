import { useState, useEffect, useRef } from 'react';
import { GATEWAY_URL, SDK_TOKEN } from '../config.js';

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
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    const url = `${GATEWAY_URL}/api/v1/stream?environment=${env}&sdk_key=${SDK_TOKEN}`;
    const es = new EventSource(url);
    esRef.current = es;

    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);

    es.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data) as SSEEvent;
        setEvents(prev => [data, ...prev].slice(0, MAX_EVENTS));
      } catch { /* ignore malformed */ }
    };

    return () => {
      es.close();
      esRef.current = null;
      setConnected(false);
    };
  }, [env]);

  return { events, connected };
}
