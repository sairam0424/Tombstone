import { evaluate } from './evaluation.js';
import type { EvaluationContext, EvaluationResult, FlagEnvironmentState, FlagSnapshot } from './types.js';

// Cloudflare KV namespace interface (matches @cloudflare/workers-types KVNamespace)
interface KVNamespace {
  get(key: string, type: 'json'): Promise<unknown>;
  get(key: string, type?: 'text'): Promise<string | null>;
}

/**
 * EdgeFlagClient — Tombstone SDK for Cloudflare Workers.
 *
 * Reads flag snapshots from Cloudflare KV for sub-1ms evaluation.
 * No SSE connections — Workers don't support persistent connections.
 * Snapshots are written to KV by flag-api's EdgeSyncer on every flag change.
 *
 * Usage in a Cloudflare Worker:
 * ```typescript
 * // wrangler.toml: [[kv_namespaces]] binding = "TOMBSTONE_FLAGS" id = "your-namespace-id"
 * export default {
 *   async fetch(request: Request, env: { TOMBSTONE_FLAGS: KVNamespace }) {
 *     const client = new EdgeFlagClient(env.TOMBSTONE_FLAGS, 'production');
 *     const result = await client.evaluate('checkout-v2', { userId: hashedUserId });
 *     if (result.value) { // new checkout }
 *   }
 * }
 * ```
 */
export class EdgeFlagClient {
  private readonly kv: KVNamespace;
  private readonly environment: string;
  private readonly defaults: Record<string, unknown>;
  private cachedSnapshot: FlagSnapshot | null = null;
  private snapshotCachedAt = 0;
  private readonly cacheTtlMs: number;

  constructor(
    kv: KVNamespace,
    environment: string,
    defaults: Record<string, unknown> = {},
    cacheTtlMs = 10_000, // re-read KV every 10s (Workers KV has 60s eventual consistency)
  ) {
    this.kv = kv;
    this.environment = environment;
    this.defaults = defaults;
    this.cacheTtlMs = cacheTtlMs;
  }

  /**
   * Evaluate a flag. Reads snapshot from KV (cached for cacheTtlMs).
   * Returns the default value if flag is not found or KV is unavailable.
   */
  async evaluate<T = boolean>(flagKey: string, context: EvaluationContext): Promise<EvaluationResult<T>> {
    const snapshot = await this.getSnapshot();
    const flagState = snapshot?.flags.find(f => f.flagKey === flagKey);
    const defaultValue = (this.defaults[flagKey] ?? false) as T;
    return evaluate<T>(flagState, context, defaultValue, flagKey);
  }

  /** Convenience wrapper — returns the boolean value directly. */
  async isEnabled(flagKey: string, context: EvaluationContext): Promise<boolean> {
    const result = await this.evaluate<boolean>(flagKey, context);
    return result.value === true;
  }

  /** Returns the full flag snapshot from KV. */
  async getSnapshot(): Promise<FlagSnapshot | null> {
    const now = Date.now();
    if (this.cachedSnapshot && (now - this.snapshotCachedAt) < this.cacheTtlMs) {
      return this.cachedSnapshot;
    }
    try {
      // KV key format: snapshot:{environment} (written by flag-api EdgeSyncer)
      const raw = await this.kv.get(`snapshot:${this.environment}`, 'json') as FlagSnapshot | null;
      if (raw) {
        // Normalize field names (API returns snake_case, normalize to camelCase)
        this.cachedSnapshot = normalizeSnapshot(raw);
        this.snapshotCachedAt = now;
      }
    } catch {
      // KV unavailable — serve from in-memory cache or return null
    }
    return this.cachedSnapshot;
  }

  /** Force refresh the snapshot from KV on the next evaluate() call. */
  invalidateCache(): void {
    this.snapshotCachedAt = 0;
  }
}

function normalizeSnapshot(raw: unknown): FlagSnapshot {
  const r = raw as Record<string, unknown>;
  const flags = ((r['flags'] as unknown[]) ?? []).map(f => {
    const fl = f as Record<string, unknown>;
    return {
      flagKey: (fl['flag_key'] ?? fl['flagKey'] ?? '') as string,
      enabled: Boolean(fl['enabled']),
      rolloutPct: Number(fl['rollout_pct'] ?? fl['rolloutPct'] ?? 0),
      safeDefault: String(fl['safe_default'] ?? fl['safeDefault'] ?? 'false'),
      environment: String(fl['environment'] ?? ''),
    } satisfies FlagEnvironmentState;
  });
  return {
    environment: String(r['environment'] ?? ''),
    flags,
    hash: String(r['hash'] ?? ''),
    ts: Number(r['ts'] ?? 0),
  };
}
