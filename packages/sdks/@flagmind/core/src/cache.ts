import type { FlagEnvironmentState, FlagEvent, FlagSnapshot, TargetingRule } from './types.js';

// Three-tier immutable flag cache: memory → (Redis in relay mode) → defaults
// IMMUTABILITY RULE: all updates create new objects, never mutate in-place.
export class FlagCache {
  private memory: Map<string, FlagEnvironmentState> = new Map();
  private snapshot: FlagSnapshot | null = null;

  // Load a full snapshot into memory (called on SDK init)
  loadSnapshot(snapshot: FlagSnapshot): void {
    const next = new Map<string, FlagEnvironmentState>();
    for (const flag of snapshot.flags) {
      next.set(flag.flagKey, {
        ...flag,
        // Ensure targetingRules is always an array (default [])
        targetingRules: Array.isArray(flag.targetingRules) ? [...flag.targetingRules] : [],
      });
    }
    this.memory = next;
    this.snapshot = { ...snapshot, flags: [...snapshot.flags] };
  }

  // Apply a delta SSE event — atomic immutable update.
  // Only patches the event fields (enabled, rolloutPct, updatedAt).
  // All other fields including targetingRules are PRESERVED via spread.
  applyEvent(event: FlagEvent): void {
    const existing = this.memory.get(event.flagKey);
    if (!existing) return;
    // Create new object — never mutate existing.
    // Spread first so that targetingRules and other fields survive unchanged.
    const updated: FlagEnvironmentState = {
      ...existing,
      enabled: event.enabled,
      rolloutPct: event.rolloutPct,
      updatedAt: event.ts,
    };
    const next = new Map(this.memory);
    next.set(event.flagKey, updated);
    this.memory = next;
  }

  // Patch targeting rules for a flag (immutable — creates a new entry).
  setTargetingRules(flagKey: string, rules: TargetingRule[]): void {
    const existing = this.memory.get(flagKey);
    if (!existing) return;
    const updated: FlagEnvironmentState = {
      ...existing,
      targetingRules: [...rules],
    };
    const next = new Map(this.memory);
    next.set(flagKey, updated);
    this.memory = next;
  }

  get(flagKey: string): FlagEnvironmentState | undefined {
    return this.memory.get(flagKey);
  }

  getHash(): string | null {
    return this.snapshot?.hash ?? null;
  }

  keys(): string[] {
    return Array.from(this.memory.keys());
  }

  size(): number {
    return this.memory.size;
  }
}
