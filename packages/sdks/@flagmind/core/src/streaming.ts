import type { FlagEvent, TombstoneClientConfig } from "./types.js";

// SSE client with automatic reconnect and exponential backoff.
// Handles: flag_updated, kill_switch, heartbeat, connected events.
export class SSEStreamClient {
  private es: EventSource | null = null;
  private reconnectMs: number;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  // Debounce timer for coalescing bursts of "lag" events into one refetch.
  // Null when no refetch is pending. Cleared on disconnect() to stay cancel-safe.
  private lagRefetchTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly lagRefetchDebounceMs: number;
  private stopped = false;
  // True once the very first "connected" event has been observed. Used to
  // distinguish the initial connection (snapshot already fetched by
  // TombstoneClient.connect()) from every SUBSEQUENT reconnect, which must
  // trigger onReconnect regardless of whether the gap went through a STALE
  // provider state.
  private hasConnectedOnce = false;

  constructor(
    private readonly config: TombstoneClientConfig,
    private readonly onEvent: (event: FlagEvent) => void,
    private readonly onReconnect?: () => void,
  ) {
    this.reconnectMs = config.reconnectIntervalMs ?? 1000;
    this.lagRefetchDebounceMs = config.lagRefetchDebounceMs ?? 500;
  }

  connect(): void {
    this.stopped = false;
    this.openConnection();
  }

  disconnect(): void {
    this.stopped = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.lagRefetchTimer) {
      clearTimeout(this.lagRefetchTimer);
      this.lagRefetchTimer = null;
    }
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }

  private openConnection(): void {
    const gatewayUrl = this.config.gatewayUrl ?? "http://localhost:8080";
    const url = `${gatewayUrl}/api/v1/stream?environment=${encodeURIComponent(this.config.environment)}`;

    // Use native EventSource in browser; use EventSource-compatible init in Node.js
    // The Authorization header is passed as a custom header via the headers option.
    this.es = new EventSource(url, {
      // @ts-expect-error - Node.js EventSource supports headers, browser does not type it
      headers: { Authorization: `Bearer ${this.config.sdkKey}` },
    });

    this.es.addEventListener("flag_updated", (e: MessageEvent) => {
      this.handleRawEvent(e.data as string);
    });

    this.es.addEventListener("kill_switch", (e: MessageEvent) => {
      this.handleRawEvent(e.data as string);
    });

    // The gateway emits a "lag" frame right BEFORE it drops a real flag-update
    // event, whenever this client's send buffer is full (we fell behind — see
    // services/gateway/internal/hub/hub.go). The dropped update would otherwise
    // leave the cache silently stale until the next event or a full reconnect.
    // Recover by triggering the SAME full-snapshot refetch that reconnect uses
    // (onReconnect), debounced so a burst of lag frames collapses into one.
    this.es.addEventListener("lag", () => {
      this.scheduleLagRefetch();
    });

    this.es.addEventListener("connected", () => {
      this.reconnectMs = this.config.reconnectIntervalMs ?? 1000; // reset backoff

      // The gateway sends "connected" on the initial connection AND on every
      // reconnect. Fire onReconnect for every occurrence AFTER the first —
      // the initial connect() already fetched a snapshot before opening the
      // stream, so re-fetching here would be redundant (but harmless).
      if (this.hasConnectedOnce) {
        this.onReconnect?.();
      }
      this.hasConnectedOnce = true;
    });

    this.es.onerror = () => {
      this.es?.close();
      this.es = null;
      if (!this.stopped) {
        this.scheduleReconnect();
      }
    };
  }

  private handleRawEvent(data: string): void {
    try {
      const raw = JSON.parse(data) as Record<string, unknown>;
      const event: FlagEvent = {
        flagKey: String(raw["flag_key"] ?? ""),
        enabled: Boolean(raw["enabled"]),
        rolloutPct: Number(raw["rollout_pct"] ?? 0),
        reason: String(raw["reason"] ?? ""),
        ts: Number(raw["ts"] ?? 0),
        environment: String(raw["environment"] ?? ""),
      };
      if (event.flagKey) {
        this.onEvent(event);
      }
    } catch {
      // malformed event — ignore
    }
  }

  private scheduleReconnect(): void {
    const maxMs = this.config.maxReconnectMs ?? 30_000;
    this.reconnectTimer = setTimeout(() => {
      if (!this.stopped) {
        this.openConnection();
      }
    }, this.reconnectMs);
    this.reconnectMs = Math.min(this.reconnectMs * 2, maxMs);
  }

  // Debounced full-snapshot refetch, triggered by "lag" events. Each lag frame
  // resets the timer, so a burst arriving within the debounce window coalesces
  // into a SINGLE onReconnect() call this.lagRefetchDebounceMs after the last
  // frame — reusing the exact snapshot-refetch path connect()/reconnect use.
  private scheduleLagRefetch(): void {
    if (this.stopped) {
      return;
    }
    if (this.lagRefetchTimer) {
      clearTimeout(this.lagRefetchTimer);
    }
    this.lagRefetchTimer = setTimeout(() => {
      this.lagRefetchTimer = null;
      if (!this.stopped) {
        this.onReconnect?.();
      }
    }, this.lagRefetchDebounceMs);
  }
}
