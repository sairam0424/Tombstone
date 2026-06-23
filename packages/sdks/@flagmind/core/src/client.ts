import { EvaluationEngine } from './evaluation.js';
import { FlagCache } from './cache.js';
import { SSEStreamClient } from './streaming.js';
import type {
  TombstoneClientConfig,
  EvaluationContext,
  EvaluationResult,
  FlagSnapshot,
  FlagEvent,
  TargetingRule,
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

  /**
   * Fetches full snapshot, loads cache, opens SSE stream.
   * Must be called before evaluate().
   *
   * If the snapshot response includes targeting rules per flag they are stored
   * in the cache automatically.  For any flags that have no rules in the
   * snapshot, a best-effort fetch of GET /api/v1/flags/{key}/rules is made;
   * failures are silently ignored — empty rules means evaluate by rollout hash,
   * which is the unchanged v1 behaviour.
   */
  async connect(): Promise<void> {
    const apiUrl = this.config.apiUrl ?? 'http://localhost:8081';
    const url = `${apiUrl}/api/v1/environments/snapshot?environment=${encodeURIComponent(this.config.environment)}`;

    const resp = await fetch(url, {
      headers: { Authorization: `Bearer ${this.config.sdkKey}` },
    });

    if (resp.ok) {
      const snap = await resp.json() as FlagSnapshot;
      this.cache.loadSnapshot(snap);

      // Best-effort: for flags whose snapshot entry has no targeting rules,
      // try to fetch them from the flag-api individually.
      const rulesPromises = this.cache.keys()
        .filter(flagKey => {
          const entry = this.cache.get(flagKey);
          return !entry?.targetingRules || entry.targetingRules.length === 0;
        })
        .map(flagKey => this.fetchAndStoreRules(apiUrl, flagKey));

      // Fire-and-forget — we do NOT await; failures are swallowed inside
      // fetchAndStoreRules.  If they resolve before the first evaluate() call
      // the rules will be used; otherwise evaluation falls through to rollout.
      void Promise.allSettled(rulesPromises);
    }
    // Even if snapshot fails, we proceed — fallback to defaults on evaluation

    this.stream.connect();
    this.connected = true;
  }

  /**
   * Synchronous, <0.5ms in-memory evaluation.
   * Never throws — returns safe default on any error.
   *
   * Targeting rules stored in the cache are evaluated first (priority
   * ascending, lower number = higher priority).  If no rule matches, the
   * call falls through to the MurmurHash rollout hash.
   */
  evaluate<T = boolean>(flagKey: string, context: EvaluationContext): EvaluationResult<T> {
    const flagState = this.cache.get(flagKey);
    const defaultValue = this.resolveDefault<T>(flagKey);

    // Pull targeting rules from cache (defaults to [] if not present)
    const rules: TargetingRule[] = flagState?.targetingRules ?? [];

    return this.engine.evaluate<T>(
      flagState,
      rules,
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

  /**
   * Fetch targeting rules for a single flag from the flag-api.
   * Stores the result in the cache on success; silently ignores any error.
   */
  private async fetchAndStoreRules(apiUrl: string, flagKey: string): Promise<void> {
    try {
      const url = `${apiUrl}/api/v1/flags/${encodeURIComponent(flagKey)}/rules`;
      const resp = await fetch(url, {
        headers: { Authorization: `Bearer ${this.config.sdkKey}` },
      });
      if (!resp.ok) return;
      const rules = await resp.json() as TargetingRule[];
      if (Array.isArray(rules) && rules.length > 0) {
        this.cache.setTargetingRules(flagKey, rules);
      }
    } catch {
      // Fail silently — no rules = evaluate by rollout hash (v1 behaviour)
    }
  }
}
