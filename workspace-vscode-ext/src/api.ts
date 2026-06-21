import * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Config helpers
// ---------------------------------------------------------------------------

export interface TombstoneConfig {
  apiUrl: string;
  apiToken: string;
  environment: string;
  intelligenceApiUrl: string;
}

export function getConfig(): TombstoneConfig {
  const cfg = vscode.workspace.getConfiguration("tombstone");
  return {
    apiUrl: cfg.get<string>("apiUrl", "http://localhost:8081").replace(/\/$/, ""),
    apiToken: cfg.get<string>("apiToken", ""),
    environment: cfg.get<string>("environment", "production"),
    intelligenceApiUrl: cfg.get<string>("intelligenceApiUrl", "http://localhost:8082").replace(/\/$/, ""),
  };
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

export interface FlagState {
  id: string;
  key: string;
  name: string;
  description: string;
  state: "active" | "inactive" | "killed" | "archived";
  owner_id: string;
  enabled: boolean;
  rollout_pct: number;
  flag_type: "release" | "experiment" | "kill_switch" | "operational";
}

export interface StaleFlag {
  flag_key: string;
  days_at_100_pct: number;
  stale_score: number;
  recommended_action: "remove" | "archive" | "review";
}

export interface NlpSearchResult {
  flag_key: string;
  name: string;
  score: number;
  snippet: string;
}

export interface CleanupPRResult {
  pr_title: string;
  pr_body: string;
  branch_name: string;
}

// ---------------------------------------------------------------------------
// Core fetch wrapper
// ---------------------------------------------------------------------------

export async function apiFetch(
  baseUrl: string,
  path: string,
  options: RequestInit = {}
): Promise<Response> {
  const { apiToken } = getConfig();

  const url = `${baseUrl}${path}`;
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(options.headers as Record<string, string> | undefined),
  };

  if (apiToken) {
    headers["Authorization"] = `Bearer ${apiToken}`;
  }

  const response = await fetch(url, { ...options, headers });

  if (!response.ok) {
    const body = await response.text().catch(() => "(no body)");
    throw new Error(
      `Tombstone API error ${response.status} ${response.statusText} — ${url}: ${body}`
    );
  }

  return response;
}

// ---------------------------------------------------------------------------
// Flag-API endpoints  (flag-api, default :8081)
// ---------------------------------------------------------------------------

export async function listFlags(): Promise<FlagState[]> {
  const { apiUrl } = getConfig();
  const response = await apiFetch(apiUrl, "/api/v1/flags");
  const data = await response.json() as { flags?: FlagState[] } | FlagState[];
  // Support both array response and wrapped {flags:[]} envelope
  if (Array.isArray(data)) {
    return data;
  }
  return (data as { flags?: FlagState[] }).flags ?? [];
}

export async function getFlag(key: string): Promise<FlagState> {
  const { apiUrl } = getConfig();
  const response = await apiFetch(apiUrl, `/api/v1/flags/${encodeURIComponent(key)}`);
  return response.json() as Promise<FlagState>;
}

export async function killSwitch(flagKey: string, reason: string): Promise<void> {
  const { apiUrl } = getConfig();
  await apiFetch(apiUrl, `/api/v1/flags/${encodeURIComponent(flagKey)}/kill`, {
    method: "POST",
    body: JSON.stringify({ reason }),
  });
}

// ---------------------------------------------------------------------------
// Intelligence service endpoints  (intelligence, default :8082)
// ---------------------------------------------------------------------------

export async function listStaleFlags(): Promise<StaleFlag[]> {
  const { intelligenceApiUrl } = getConfig();
  const response = await apiFetch(intelligenceApiUrl, "/api/v1/stale");
  const data = await response.json() as { stale_flags?: StaleFlag[] } | StaleFlag[];
  if (Array.isArray(data)) {
    return data;
  }
  return (data as { stale_flags?: StaleFlag[] }).stale_flags ?? [];
}

export async function searchFlagsNlp(query: string): Promise<NlpSearchResult[]> {
  const { intelligenceApiUrl } = getConfig();
  const response = await apiFetch(
    intelligenceApiUrl,
    `/api/v1/search?q=${encodeURIComponent(query)}`
  );
  const data = await response.json() as { results?: NlpSearchResult[] } | NlpSearchResult[];
  if (Array.isArray(data)) {
    return data;
  }
  return (data as { results?: NlpSearchResult[] }).results ?? [];
}

export async function generateCleanupPR(flagKey: string): Promise<CleanupPRResult> {
  const { intelligenceApiUrl } = getConfig();
  const response = await apiFetch(
    intelligenceApiUrl,
    "/api/v1/cleanup/generate-pr",
    {
      method: "POST",
      body: JSON.stringify({ flag_key: flagKey }),
    }
  );
  return response.json() as Promise<CleanupPRResult>;
}
