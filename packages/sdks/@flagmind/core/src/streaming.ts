import type { FlagEvent, TombstoneClientConfig } from './types.js';

// SSE client with automatic reconnect and exponential backoff.
// Handles: flag_updated, kill_switch, heartbeat, connected events.
export class SSEStreamClient {
  private es: EventSource | null = null;
  private reconnectMs: number;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private stopped = false;

  constructor(
    private readonly config: TombstoneClientConfig,
    private readonly onEvent: (event: FlagEvent) => void,
  ) {
    this.reconnectMs = config.reconnectIntervalMs ?? 1000;
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
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }

  private openConnection(): void {
    const gatewayUrl = this.config.gatewayUrl ?? 'http://localhost:8080';
    const url = `${gatewayUrl}/api/v1/stream?environment=${encodeURIComponent(this.config.environment)}`;

    // Use native EventSource in browser; use EventSource-compatible init in Node.js
    // The Authorization header is passed as a custom header via the headers option.
    this.es = new EventSource(url, {
      // @ts-expect-error - Node.js EventSource supports headers, browser does not type it
      headers: { Authorization: `Bearer ${this.config.sdkKey}` },
    });

    this.es.addEventListener('flag_updated', (e: MessageEvent) => {
      this.handleRawEvent(e.data as string);
    });

    this.es.addEventListener('kill_switch', (e: MessageEvent) => {
      this.handleRawEvent(e.data as string);
    });

    this.es.addEventListener('connected', () => {
      this.reconnectMs = this.config.reconnectIntervalMs ?? 1000; // reset backoff
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
        flagKey:     String(raw['flag_key'] ?? ''),
        enabled:     Boolean(raw['enabled']),
        rolloutPct:  Number(raw['rollout_pct'] ?? 0),
        reason:      String(raw['reason'] ?? ''),
        ts:          Number(raw['ts'] ?? 0),
        environment: String(raw['environment'] ?? ''),
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
}
