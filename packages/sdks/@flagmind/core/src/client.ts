import { EvaluationEngine } from "./evaluation.js";
import { FlagCache } from "./cache.js";
import { SSEStreamClient } from "./streaming.js";
import type {
  TombstoneClientConfig,
  EvaluationContext,
  EvaluationResult,
  FlagSnapshot,
  FlagEvent,
  TargetingRule,
  TelemetryEvent,
} from "./types.js";

// EVAL-2: cap the in-memory telemetry buffer so a sustained evaluator
// outage (or a caller who never awaits disconnect()) cannot grow it
// without bound -- matches this codebase's existing DLQ_MAX-style capping
// convention (e.g. services/gateway/internal/hub/dlq.go). Telemetry is
// best-effort observability data, not a delivery guarantee: dropping the
// OLDEST events once full keeps the buffer bounded while still flushing
// whatever fits, rather than blocking evaluate() or throwing.
const TELEMETRY_BUFFER_MAX = 1000;
const DEFAULT_TELEMETRY_FLUSH_INTERVAL_MS = 10_000;

export class TombstoneClient {
  private readonly cache: FlagCache;
  private readonly engine: EvaluationEngine;
  private readonly stream: SSEStreamClient;
  private connected = false;
  private readonly telemetryBuffer: TelemetryEvent[] = [];
  private telemetryFlushTimer: ReturnType<typeof setInterval> | undefined;

  constructor(private readonly config: TombstoneClientConfig) {
    this.cache = new FlagCache();
    this.engine = new EvaluationEngine();
    this.stream = new SSEStreamClient(
      config,
      (event: FlagEvent) => {
        this.cache.applyEvent(event);
      },
      () => {
        // Every SSE reconnect (not only ones that went through a STALE
        // period) may have missed flag-api's Redis publish for events that
        // occurred while disconnected — see the dual-write gap documented in
        // services/flag-api/internal/api/v1/flags.go. A fresh full-snapshot
        // refetch re-syncs the cache regardless of how briefly we were down.
        // Fire-and-forget: fetchSnapshot() already swallows its own errors.
        void this.fetchSnapshot();
      },
    );
  }

  /**
   * Fetches full snapshot, loads cache, opens SSE stream.
   * Must be called before evaluate().
   */
  async connect(): Promise<void> {
    await this.fetchSnapshot();
    this.stream.connect();
    this.connected = true;

    // EVAL-2: opt-in only -- no telemetryUrl means no timer, no network
    // calls, no behavior change for any existing caller.
    if (this.config.telemetryUrl) {
      const intervalMs =
        this.config.telemetryFlushIntervalMs ??
        DEFAULT_TELEMETRY_FLUSH_INTERVAL_MS;
      this.telemetryFlushTimer = setInterval(() => {
        void this.flushTelemetry();
      }, intervalMs);
    }
  }

  /**
   * Fetches the full flag snapshot for this client's environment and loads it
   * into the cache. Called on initial connect() AND on every subsequent SSE
   * reconnect (via the onReconnect callback passed to SSEStreamClient), so
   * the in-memory cache re-syncs after any connectivity gap — not just the
   * first load.
   *
   * If the snapshot response includes targeting rules per flag they are stored
   * in the cache automatically.  For any flags that have no rules in the
   * snapshot, a best-effort fetch of GET /api/v1/flags/{key}/rules is made;
   * failures are silently ignored — empty rules means evaluate by rollout hash,
   * which is the unchanged v1 behaviour.
   *
   * Never throws — a failed fetch (e.g. flag-api still unreachable) leaves
   * the existing cache untouched so evaluation keeps serving last-known-good
   * values.
   */
  private async fetchSnapshot(): Promise<void> {
    const apiUrl = this.config.apiUrl ?? "http://localhost:8081";
    const url = `${apiUrl}/api/v1/environments/snapshot?environment=${encodeURIComponent(this.config.environment)}`;

    try {
      const resp = await fetch(url, {
        headers: { Authorization: `Bearer ${this.config.sdkKey}` },
      });

      if (resp.ok) {
        const snap = (await resp.json()) as FlagSnapshot;
        this.cache.loadSnapshot(snap);

        // Best-effort: for flags whose snapshot entry has no targeting rules,
        // try to fetch them from the flag-api individually.
        const rulesPromises = this.cache
          .keys()
          .filter((flagKey) => {
            const entry = this.cache.get(flagKey);
            return !entry?.targetingRules || entry.targetingRules.length === 0;
          })
          .map((flagKey) => this.fetchAndStoreRules(apiUrl, flagKey));

        // Fire-and-forget — we do NOT await; failures are swallowed inside
        // fetchAndStoreRules.  If they resolve before the first evaluate() call
        // the rules will be used; otherwise evaluation falls through to rollout.
        void Promise.allSettled(rulesPromises);
      }
      // Even if snapshot fails, we proceed — fallback to defaults/last-known-good on evaluation
    } catch {
      // Network error — keep serving from whatever the cache already has.
    }
  }

  /**
   * Synchronous, <0.5ms in-memory evaluation.
   * Never throws — returns safe default on any error.
   *
   * Targeting rules stored in the cache are evaluated first (priority
   * ascending, lower number = higher priority).  If no rule matches, the
   * call falls through to the MurmurHash rollout hash.
   */
  evaluate<T = boolean>(
    flagKey: string,
    context: EvaluationContext,
  ): EvaluationResult<T> {
    const flagState = this.cache.get(flagKey);
    const defaultValue = this.resolveDefault<T>(flagKey);

    // Pull targeting rules from cache (defaults to [] if not present)
    const rules: TargetingRule[] = flagState?.targetingRules ?? [];

    const result = this.engine.evaluate<T>(
      flagState,
      rules,
      context,
      defaultValue,
      flagKey,
    );

    this.recordTelemetry(flagKey, result);

    return result;
  }

  isConnected(): boolean {
    return this.connected;
  }

  disconnect(): void {
    this.stream.disconnect();
    this.connected = false;

    if (this.telemetryFlushTimer !== undefined) {
      clearInterval(this.telemetryFlushTimer);
      this.telemetryFlushTimer = undefined;
      // Final flush -- fire-and-forget, matches fetchSnapshot's/
      // flushTelemetry's own error-swallowing convention. A caller who
      // wants a hard guarantee everything was sent before the process
      // exits should await client.flush() (public wrapper below)
      // themselves; disconnect() itself stays synchronous like today.
      void this.flushTelemetry();
    }
  }

  /**
   * Public wrapper so a caller that wants delivery confirmation before
   * shutting down (e.g. inside a signal handler) can await it directly,
   * instead of relying on disconnect()'s fire-and-forget final flush.
   */
  async flush(): Promise<void> {
    await this.flushTelemetry();
  }

  flagKeys(): string[] {
    return this.cache.keys();
  }

  private resolveDefault<T>(flagKey: string): T {
    const d = this.config.defaults[flagKey];
    if (d !== undefined) return d as T;
    // Type-safe fallback chain: boolean → false, string → '', number → 0
    return false as unknown as T;
  }

  /**
   * Fetch targeting rules for a single flag from the flag-api.
   * Stores the result in the cache on success; silently ignores any error.
   */
  private async fetchAndStoreRules(
    apiUrl: string,
    flagKey: string,
  ): Promise<void> {
    try {
      const url = `${apiUrl}/api/v1/flags/${encodeURIComponent(flagKey)}/rules`;
      const resp = await fetch(url, {
        headers: { Authorization: `Bearer ${this.config.sdkKey}` },
      });
      if (!resp.ok) return;
      const rules = (await resp.json()) as TargetingRule[];
      if (Array.isArray(rules) && rules.length > 0) {
        this.cache.setTargetingRules(flagKey, rules);
      }
    } catch {
      // Fail silently — no rules = evaluate by rollout hash (v1 behaviour)
    }
  }

  /**
   * EVAL-2: buffers a telemetry event for the given evaluate() outcome.
   * No-op entirely when telemetryUrl is unset — matches this class's
   * other opt-in network calls (connect()'s snapshot fetch is the
   * exception; that one is required, telemetry is not). isError mirrors
   * the SDK's own EvaluationReason enum's "ERROR" case, matching the
   * boolean IsError field services/evaluator/internal/telemetry/
   * aggregator.go's TelemetryEvent expects.
   */
  private recordTelemetry<T>(
    flagKey: string,
    result: EvaluationResult<T>,
  ): void {
    if (!this.config.telemetryUrl) return;

    const sampleRate = this.config.telemetrySampleRate ?? 1.0;
    if (sampleRate < 1.0 && Math.random() >= sampleRate) return;

    if (this.telemetryBuffer.length >= TELEMETRY_BUFFER_MAX) {
      this.telemetryBuffer.shift(); // drop oldest — best-effort, bounded
    }
    this.telemetryBuffer.push({
      flag_key: flagKey,
      environment: this.config.environment,
      is_error: result.reason === "ERROR",
      ts: new Date().toISOString(),
    });
  }

  /**
   * Sends the buffered telemetry batch to telemetryUrl and clears the
   * buffer. Never throws — a failed POST (network error, evaluator down)
   * drops the batch rather than blocking or re-queuing; telemetry is
   * best-effort observability data, not a delivery guarantee, matching
   * fetchSnapshot's/fetchAndStoreRules's own fail-silent convention.
   * The buffer is cleared BEFORE the request starts (not after it
   * resolves) so a slow/hanging request cannot cause the next interval
   * tick to pile a second batch on top of the first.
   */
  private async flushTelemetry(): Promise<void> {
    if (!this.config.telemetryUrl || this.telemetryBuffer.length === 0) return;

    const batch = this.telemetryBuffer.splice(0, this.telemetryBuffer.length);
    try {
      await fetch(`${this.config.telemetryUrl}/api/v1/telemetry`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(batch),
      });
    } catch {
      // Network error — drop the batch, matching this class's other
      // fail-silent telemetry/rules fetches.
    }
  }
}
