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

    // gateway (hub.go's sseFrame/rawFrame) never sends an unnamed/default
    // `message` event -- every real frame carries an explicit `event:` name
    // (connected/flag_updated/kill_switch/prerequisites_updated/
    // targeting_rules_updated/heartbeat/lag), which per the SSE spec only
    // fires a NAMED listener, never `onmessage`. This hook used to attach
    // only `onmessage`, so LiveFeed had never rendered a single real event,
    // ever, in this dashboard's history -- found by actually connecting a
    // real browser to this live gateway end to end, something no existing
    // test does (this file has no test coverage at all). The raw wire
    // payload's own field names (flag_key/rollout_pct/ts, see hub.go's
    // sseFrame) also never matched this hook's SSEEvent shape
    // (flagKey/timestamp/type/payload/id) even for the one event type
    // (flag_updated) LiveFeed was designed to color-code -- mapped
    // explicitly below instead of assuming the wire shape.
    const toSSEEvent = (eventType: string, raw: string): SSEEvent | null => {
      try {
        const data = JSON.parse(raw) as Record<string, unknown>;
        const ts = Number(data["ts"]);
        return {
          id: `${eventType}-${data["flag_key"] ?? ""}-${Number.isFinite(ts) ? ts : Date.now()}-${Math.random().toString(36).slice(2)}`,
          type: eventType,
          flagKey: String(data["flag_key"] ?? ""),
          environment: String(data["environment"] ?? env),
          timestamp: new Date(
            Number.isFinite(ts) ? ts * 1000 : Date.now(),
          ).toISOString(),
          payload: data,
        };
      } catch {
        return null; // malformed event — ignore
      }
    };

    const handleNamedEvent = (eventType: string) => (e: MessageEvent) => {
      const parsed = toSSEEvent(eventType, e.data as string);
      if (parsed) {
        setEvents((prev) => [parsed, ...prev].slice(0, MAX_EVENTS));
      }
    };

    const listenedTypes = [
      "flag_updated",
      "kill_switch",
      "prerequisites_updated",
      "targeting_rules_updated",
    ];
    for (const eventType of listenedTypes) {
      es.addEventListener(eventType, handleNamedEvent(eventType));
    }

    return () => {
      es.close();
      setConnected(false);
    };
  }, [env]);

  return { events, connected };
}
