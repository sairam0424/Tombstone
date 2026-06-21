import { EvaluationEngine } from './evaluation.js';
import { FlagCache } from './cache.js';
import { SSEStreamClient } from './streaming.js';
import type {
  TombstoneClientConfig,
  EvaluationContext,
  EvaluationResult,
  FlagSnapshot,
  FlagEvent,
} from './types.js';

export class TombstoneClient {
  private readonly cache: FlagCache;
  private readonly engine: EvaluationEngine;
  private readonly stream: SSEStreamClient;
  private connected = false;

  constructor(private readonly config: TombstoneClientConfig) {
    this.cache = new FlagCache();
    this.engine = new EvaluationEngine();
    this.stream = new SSEStreamClient(config, (event: FlagEvent) => {
      this.cache.applyEvent(event);
    });
  }

  // Fetches full snapshot, loads cache, opens SSE stream.
  // Must be called before evaluate().
  async connect(): Promise<void> {
    const apiUrl = this.config.apiUrl ?? 'http://localhost:8081';
    const url = `${apiUrl}/api/v1/environments/snapshot?environment=${encodeURIComponent(this.config.environment)}`;

    const resp = await fetch(url, {
      headers: { Authorization: `Bearer ${this.config.sdkKey}` },
    });

    if (resp.ok) {
      const snap = await resp.json() as FlagSnapshot;
      this.cache.loadSnapshot(snap);
    }
    // Even if snapshot fails, we proceed — fallback to defaults on evaluation

    this.stream.connect();
    this.connected = true;
  }

  // Synchronous, <0.5ms in-memory evaluation.
  // Never throws — returns safe default on any error.
  evaluate<T = boolean>(flagKey: string, context: EvaluationContext): EvaluationResult<T> {
    const flagState = this.cache.get(flagKey);
    const defaultValue = this.resolveDefault<T>(flagKey);

    return this.engine.evaluate<T>(
      flagState,
      [], // targeting rules loaded in future iteration (Phase 2)
      context,
      defaultValue,
      flagKey,
    );
  }

  isConnected(): boolean {
    return this.connected;
  }

  disconnect(): void {
    this.stream.disconnect();
    this.connected = false;
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
}
