import type { FlagEnvironmentState, FlagEvent, FlagSnapshot } from './types.js';

// Three-tier immutable flag cache: memory → (Redis in relay mode) → defaults
// IMMUTABILITY RULE: all updates create new objects, never mutate in-place.
export class FlagCache {
  private memory: Map<string, FlagEnvironmentState> = new Map();
  private snapshot: FlagSnapshot | null = null;

  // Load a full snapshot into memory (called on SDK init)
  loadSnapshot(snapshot: FlagSnapshot): void {
    const next = new Map<string, FlagEnvironmentState>();
    for (const flag of snapshot.flags) {
      next.set(flag.flagKey, { ...flag }); // spread = immutable copy
    }
    this.memory = next;
    this.snapshot = { ...snapshot, flags: [...snapshot.flags] };
  }

  // Apply a delta SSE event — atomic immutable update
  applyEvent(event: FlagEvent): void {
    const existing = this.memory.get(event.flagKey);
    if (!existing) return;
    // Create new object — never mutate existing
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
