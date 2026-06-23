import { evaluate } from './evaluation.js';
import type { EvaluationContext, EvaluationResult, FlagEnvironmentState, FlagSnapshot } from './types.js';

// Cloudflare KV namespace interface (matches @cloudflare/workers-types KVNamespace)
interface KVNamespace {
  get(key: string, type: 'json'): Promise<unknown>;
  get(key: string, type?: 'text'): Promise<string | null>;
  put(key: string, value: string, options?: { expirationTtl?: number }): Promise<void>;
}

const KV_KEY = (env: string): string => `tombstone:snapshot:${env}`;

async function loadFromKV(kv: KVNamespace, environment: string): Promise<FlagSnapshot | null> {
  const cached = await kv.get(KV_KEY(environment), 'json');
  if (!cached) return null;
  return normalizeSnapshot(cached);
}

async function loadFromOrigin(apiUrl: string, environment: string, token: string): Promise<FlagSnapshot> {
  const resp = await fetch(`${apiUrl}/api/v1/environments/snapshot?environment=${environment}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!resp.ok) throw new Error(`snapshot fetch failed: ${resp.status}`);
  return normalizeSnapshot(await resp.json() as unknown);
}

/**
 * Configuration for EdgeFlagClient.
 *
 * Supports two operational modes:
 * 1. KV-first (recommended): supply `kv` + `apiUrl` + `token` — reads from KV,
 *    falls back to origin on miss, then writes result back to KV with `kvTtlSeconds` TTL.
 * 2. Origin-only: supply `apiUrl` + `token` only — every evaluate() fetches from origin.
 * 3. KV-only (legacy): supply `kv` only — original behaviour, no origin fallback.
 */
export interface EdgeClientConfig {
  /** Tombstone flag-api or gateway base URL, e.g. "https://flags.example.com" */
  apiUrl?: string;
  /** Bearer token for Tombstone API authentication */
  token?: string;
  /** Target environment name, e.g. "production" */
  environment: string;
  /** Cloudflare KV binding. When provided, snapshots are cached here. */
  kv?: KVNamespace;
  /** KV write TTL in seconds (default 30). Ignored when `kv` is not set. */
  kvTtlSeconds?: number;
  /** Default flag values returned when a flag is not found */
  defaults?: Record<string, unknown>;
  /** In-process cache TTL in milliseconds (default 10 000). Re-reads KV this often. */
  cacheTtlMs?: number;
}

/**
 * EdgeFlagClient — Tombstone SDK for Cloudflare Workers.
 *
 * Evaluation hierarchy:
 * 1. In-process memory cache (valid for `cacheTtlMs`)
 * 2. Cloudflare KV (if `kv` binding provided)
 * 3. Tombstone origin HTTP API (if `apiUrl` + `token` provided)
 *
 * On a KV miss with origin configured, the fresh snapshot is written back to KV
 * with `kvTtlSeconds` TTL so subsequent requests hit KV.
 *
 * Usage in a Cloudflare Worker:
 * ```typescript
 * export default {
 *   async fetch(request: Request, env: {
 *     TOMBSTONE_KV: KVNamespace;
 *     TOMBSTONE_API_URL: string;
 *     TOMBSTONE_TOKEN: string;
 *   }) {
 *     const client = new EdgeFlagClient({
 *       kv: env.TOMBSTONE_KV,
 *       apiUrl: env.TOMBSTONE_API_URL,
 *       token: env.TOMBSTONE_TOKEN,
 *       environment: 'production',
 *     });
 *     const result = await client.evaluate('checkout-v2', { userId: hashedUserId });
 *     if (result.value) { // new checkout }
 *   }
 * }
 * ```
 */
export class EdgeFlagClient {
  private readonly kv: KVNamespace | undefined;
  private readonly apiUrl: string | undefined;
  private readonly token: string | undefined;
  private readonly environment: string;
  private readonly defaults: Record<string, unknown>;
  private readonly kvTtlSeconds: number;
  private cachedSnapshot: FlagSnapshot | null = null;
  private snapshotCachedAt = 0;
  private readonly cacheTtlMs: number;

  constructor(config: EdgeClientConfig) {
    this.kv = config.kv;
    this.apiUrl = config.apiUrl;
    this.token = config.token;
    this.environment = config.environment;
    this.defaults = config.defaults ?? {};
    this.kvTtlSeconds = config.kvTtlSeconds ?? 30;
    this.cacheTtlMs = config.cacheTtlMs ?? 10_000;
  }

  /**
   * Evaluate a flag. Reads snapshot from KV (cached for cacheTtlMs).
   * Returns the default value if flag is not found or all sources are unavailable.
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

  /**
   * Returns the full flag snapshot.
   *
   * Resolution order:
   * 1. In-process memory cache (valid for cacheTtlMs)
   * 2. KV (if `kv` binding provided)
   * 3. Origin HTTP API (if `apiUrl` + `token` provided); writes result to KV on success
   */
  async getSnapshot(): Promise<FlagSnapshot | null> {
    const now = Date.now();
    if (this.cachedSnapshot && (now - this.snapshotCachedAt) < this.cacheTtlMs) {
      return this.cachedSnapshot;
    }

    // Step 1: try KV
    if (this.kv) {
      try {
        const fromKV = await loadFromKV(this.kv, this.environment);
        if (fromKV) {
          this.cachedSnapshot = fromKV;
          this.snapshotCachedAt = now;
          return this.cachedSnapshot;
        }
      } catch {
        // KV unavailable — fall through to origin
      }
    }

    // Step 2: KV miss or no KV binding — fetch from origin
    if (this.apiUrl && this.token) {
      try {
        const fromOrigin = await loadFromOrigin(this.apiUrl, this.environment, this.token);
        this.cachedSnapshot = fromOrigin;
        this.snapshotCachedAt = now;

        // Write back to KV with TTL so subsequent requests hit KV
        if (this.kv) {
          try {
            await this.kv.put(
              KV_KEY(this.environment),
              JSON.stringify(fromOrigin),
              { expirationTtl: this.kvTtlSeconds },
            );
          } catch {
            // Non-fatal: KV write failure just means next request also hits origin
          }
        }

        return this.cachedSnapshot;
      } catch {
        // Origin unavailable — serve from stale in-process cache if available
      }
    }

    return this.cachedSnapshot;
  }

  /** Force refresh the snapshot from KV/origin on the next evaluate() call. */
  invalidateCache(): void {
    this.snapshotCachedAt = 0;
  }
}

/**
 * Scheduled sync worker — call this from a Cloudflare Cron Trigger to keep KV warm.
 *
 * Fetches a fresh snapshot from the Tombstone origin and writes it to KV with a 30s TTL,
 * so Worker fetch handlers read from sub-1ms KV instead of hitting origin per request.
 *
 * wrangler.toml example:
 * ```toml
 * [[triggers]]
 * crons = ["*\/30 * * * * *"]   # every 30 seconds
 * ```
 *
 * Worker entry point example:
 * ```typescript
 * import { syncSnapshotToKV } from '@tombstone/edge';
 * export default {
 *   async scheduled(_event: ScheduledEvent, env: Env, _ctx: ExecutionContext) {
 *     await syncSnapshotToKV(env);
 *   }
 * }
 * ```
 */
export async function syncSnapshotToKV(env: {
  TOMBSTONE_API_URL: string;
  TOMBSTONE_TOKEN: string;
  TOMBSTONE_KV: KVNamespace;
  TOMBSTONE_ENVIRONMENT: string;
}): Promise<void> {
  const snapshot = await loadFromOrigin(env.TOMBSTONE_API_URL, env.TOMBSTONE_ENVIRONMENT, env.TOMBSTONE_TOKEN);
  await env.TOMBSTONE_KV.put(
    KV_KEY(env.TOMBSTONE_ENVIRONMENT),
    JSON.stringify(snapshot),
    { expirationTtl: 30 },
  );
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
