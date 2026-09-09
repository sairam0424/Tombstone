import { EventSource as NodeEventSourcePolyfill } from "eventsource";
import type {
  FlagEvent,
  PrerequisitesUpdateEvent,
  TargetingRule,
  TargetingRulesUpdateEvent,
  TombstoneClientConfig,
} from "./types.js";

// This package's own package.json has always declared `eventsource` as a
// dependency, but nothing in this file ever actually imported it -- calling
// connect() in a plain Node.js process (this SDK's own primary documented
// target platform) threw `ReferenceError: EventSource is not defined`
// unless the CONSUMING application happened to already polyfill a global
// itself. Only the test suite ever worked around this (client.test.ts/
// streaming.test.ts install their own FakeEventSource as a global before
// running, per that file's own comment: "previously threw ReferenceError:
// EventSource is not defined") -- found by actually connecting this SDK to
// a live backend end to end, something no existing test does. Prefers a
// real global EventSource when one exists (a real browser, or a Node/Bun/
// Deno runtime that already provides one) so a browser bundle keeps using
// the platform's own implementation; falls back to the declared npm
// dependency otherwise -- matching openConnection's own existing comment
// ("Use native EventSource in browser; use EventSource-compatible init in
// Node.js") and its `fetch` override below, which only the npm polyfill
// (not any native browser EventSource) has ever actually supported.
//
// Resolved INSIDE this function, called fresh on every connect()/reconnect
// -- NOT as a module-level `const` evaluated once at import time. A
// module-level const would freeze whatever `globalThis.EventSource` was at
// the moment this module was first imported, which for every test in this
// package is BEFORE that test file's own `globalThis.EventSource =
// FakeEventSource` assignment runs (ES module evaluation order: importing
// this file fully evaluates its top-level code before the importer's own
// subsequent top-level statements execute) -- every SSE test would silently
// open a REAL npm-polyfill connection to a real network address instead of
// the intended FakeEventSource, both hanging the whole mocha process on an
// unclosed handle and making every FakeEventSource.instances[0] lookup
// undefined. Found by actually running this package's own test suite after
// this file's other real-bug fixes, something that had not been done since
// those fixes landed.
function resolveEventSourceImpl(): typeof EventSource {
  return typeof EventSource !== "undefined"
    ? EventSource
    : (NodeEventSourcePolyfill as unknown as typeof EventSource);
}

// SSE client with automatic reconnect and exponential backoff.
// Handles: flag_updated, kill_switch, prerequisites_updated,
// targeting_rules_updated, heartbeat, connected events.
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
    private readonly onPrerequisitesEvent?: (
      event: PrerequisitesUpdateEvent,
    ) => void,
    private readonly onTargetingRulesEvent?: (
      event: TargetingRulesUpdateEvent,
    ) => void,
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

    // Use native EventSource in browser; use EventSource-compatible init in Node.js.
    //
    // The npm `eventsource` package's v3 constructor init (EventSourceInit)
    // does NOT accept a `headers` option at all -- only `withCredentials` and
    // `fetch` (confirmed against its own dist/index.cjs: `_fetch` is set from
    // `eventSourceInitDict?.fetch`, and getRequestOptions_fn builds its
    // request's headers as only `{Accept, Last-Event-ID}`, with no
    // Authorization). Passing `headers` here used to be silently ignored --
    // no Authorization header was EVER sent, so gateway's auth middleware
    // correctly 401'd every real SSE connection from this SDK, in any
    // deployment, ever. The `@ts-expect-error` comment this replaced dated
    // from `eventsource`'s older v1/v2 API, which DID take `headers` directly
    // -- v3's rewrite onto the Fetch API moved custom-header injection to a
    // caller-supplied `fetch` override instead. Found by actually connecting
    // this SDK to a live gateway end to end with a real bearer token, not the
    // unauthenticated raw curl check that exposed the separate gateway
    // Flush() bug. Native browser EventSource has no `fetch` option either
    // (and cannot send custom headers at all -- a platform limitation, not
    // fixable here); passing it is harmless there since the browser
    // constructor simply ignores properties it doesn't recognize.
    const EventSourceImpl = resolveEventSourceImpl();
    this.es = new EventSourceImpl(url, {
      // @ts-expect-error - fetch override is eventsource v3's Node-only mechanism for custom headers; not in the DOM EventSourceInit type
      fetch: (input: unknown, init: Record<string, unknown>) =>
        fetch(input as never, {
          ...init,
          headers: {
            ...(init?.headers as Record<string, string> | undefined),
            Authorization: `Bearer ${this.config.sdkKey}`,
          },
        }),
    });

    this.es.addEventListener("flag_updated", (e: MessageEvent) => {
      this.handleRawEvent(e.data as string);
    });

    this.es.addEventListener("kill_switch", (e: MessageEvent) => {
      this.handleRawEvent(e.data as string);
    });

    // services/flag-api/internal/api/v1/prerequisites.go's PrerequisitesEvent
    // -- a distinct payload shape (flag_key/environment/prerequisites/ts,
    // no enabled/rollout_pct/reason at all) from FlagEvent, so it gets its
    // own listener and handler rather than being routed through
    // handleRawEvent, which would otherwise coerce those missing keys into
    // FlagEvent's defaults (enabled=false, rolloutPct=0) for a flag that
    // was never actually disabled.
    this.es.addEventListener("prerequisites_updated", (e: MessageEvent) => {
      this.handlePrerequisitesRawEvent(e.data as string);
    });

    // services/flag-api/internal/api/v1/targeting_rules.go's
    // TargetingRulesEvent -- same reasoning as prerequisites_updated above:
    // a distinct payload shape (flag_key/environment/targeting_rules/ts,
    // no enabled/rollout_pct/reason), so it gets its own listener rather
    // than being routed through handleRawEvent.
    this.es.addEventListener("targeting_rules_updated", (e: MessageEvent) => {
      this.handleTargetingRulesRawEvent(e.data as string);
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

  private handlePrerequisitesRawEvent(data: string): void {
    try {
      const raw = JSON.parse(data) as Record<string, unknown>;
      const flagKey = String(raw["flag_key"] ?? "");
      if (!flagKey) return;

      const rawPrereqs = Array.isArray(raw["prerequisites"])
        ? raw["prerequisites"]
        : [];
      const event: PrerequisitesUpdateEvent = {
        flagKey,
        environment: String(raw["environment"] ?? ""),
        ts: Number(raw["ts"] ?? 0),
        prerequisites: rawPrereqs.map((p) => {
          const pr = p as Record<string, unknown>;
          return {
            flagKey: String(pr["flag_key"] ?? ""),
            requiredVariation: String(pr["required_variation"] ?? "true"),
            gate: pr["gate"] !== false,
          };
        }),
      };
      this.onPrerequisitesEvent?.(event);
    } catch {
      // malformed event — ignore
    }
  }

  private handleTargetingRulesRawEvent(data: string): void {
    try {
      const raw = JSON.parse(data) as Record<string, unknown>;
      const flagKey = String(raw["flag_key"] ?? "");
      if (!flagKey) return;

      const rawRules = Array.isArray(raw["targeting_rules"])
        ? raw["targeting_rules"]
        : [];
      const event: TargetingRulesUpdateEvent = {
        flagKey,
        environment: String(raw["environment"] ?? ""),
        ts: Number(raw["ts"] ?? 0),
        targetingRules: rawRules.map((r): TargetingRule => {
          const rule = r as Record<string, unknown>;
          return {
            id: String(rule["id"] ?? ""),
            ruleType:
              (rule["rule_type"] as TargetingRule["ruleType"]) ?? "CUSTOM",
            attribute: String(rule["attribute"] ?? ""),
            operator: rule["operator"] as TargetingRule["operator"],
            values: Array.isArray(rule["values"]) ? rule["values"] : [],
            variation: String(rule["variation"] ?? ""),
            // A non-numeric wire priority (e.g. "abc") would otherwise
            // coerce to NaN and feed directly into evaluation.ts's sort
            // comparator (a.priority - b.priority), whose result is NaN
            // whenever either operand is -- making this rule's relative
            // order among same-flag rules engine/version-dependent instead
            // of the deterministic, priority-ascending order this feature
            // promises. Mirrors the SAME NaN-rejection reasoning this PR's
            // own applyTargetingRulesEvent/applyPrerequisitesEvent already
            // apply to the EVENT-level ts field. Found by adversarial
            // review of PR #246.
            priority: Number.isFinite(Number(rule["priority"]))
              ? Number(rule["priority"])
              : 0,
          };
        }),
      };
      this.onTargetingRulesEvent?.(event);
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
