import type { Tool } from "@modelcontextprotocol/sdk/types.js";

// ─── Tool Definitions ────────────────────────────────────────────────────────

export const getFlagTool: Tool = {
  name: "tombstone_get_flag",
  description:
    "Get the current state and metadata of a feature flag by its dot-notation key (e.g. payments.checkout.v2).",
  inputSchema: {
    type: "object",
    properties: {
      key: {
        type: "string",
        description: "Dot-notation flag key (e.g. payments.checkout.v2)",
      },
    },
    required: ["key"],
    additionalProperties: false,
  },
};

export const killSwitchTool: Tool = {
  name: "tombstone_kill_switch",
  description:
    "Immediately disable a feature flag via the kill-switch endpoint. Requires a reason of at least 10 characters explaining why the flag is being killed.",
  inputSchema: {
    type: "object",
    properties: {
      key: {
        type: "string",
        description: "Dot-notation flag key to disable",
      },
      reason: {
        type: "string",
        description: "Human-readable reason for killing the flag (min 10 chars)",
        minLength: 10,
      },
    },
    required: ["key", "reason"],
    additionalProperties: false,
  },
};

export const blastRadiusTool: Tool = {
  name: "tombstone_blast_radius",
  description:
    "Compute the blast radius (risk score, affected users, dependent services) before flipping a flag. Always call this before enabling or disabling high-impact flags.",
  inputSchema: {
    type: "object",
    properties: {
      key: {
        type: "string",
        description: "Dot-notation flag key to analyse",
      },
      targetState: {
        type: "boolean",
        description: "The state you intend to flip the flag to (true = enable, false = disable)",
      },
    },
    required: ["key", "targetState"],
    additionalProperties: false,
  },
};

export const listStaleFlagsTool: Tool = {
  name: "tombstone_list_stale_flags",
  description:
    "List feature flags that have not been updated or evaluated recently and are candidates for cleanup.",
  inputSchema: {
    type: "object",
    properties: {
      days: {
        type: "number",
        description: "Flags untouched for more than this many days are considered stale (default 30)",
        minimum: 1,
      },
      limit: {
        type: "number",
        description: "Maximum number of stale flags to return (default 20)",
        minimum: 1,
        maximum: 200,
      },
    },
    required: [],
    additionalProperties: false,
  },
};

export const createFlagTool: Tool = {
  name: "tombstone_create_flag",
  description:
    "Create a new feature flag. The key must use dot-notation (e.g. billing.new-invoices.enabled).",
  inputSchema: {
    type: "object",
    properties: {
      key: {
        type: "string",
        description: "Unique dot-notation key for the flag (e.g. billing.new-invoices.enabled)",
        pattern: "^[a-z0-9]+(?:\\.[a-z0-9_-]+)+$",
      },
      description: {
        type: "string",
        description: "Human-readable description of what this flag controls",
      },
      enabled: {
        type: "boolean",
        description: "Initial enabled state (default false)",
      },
      tags: {
        type: "array",
        items: { type: "string" },
        description: "Optional tags for grouping/searching flags",
      },
      owner: {
        type: "string",
        description: "Team or person responsible for this flag",
      },
    },
    required: ["key", "description"],
    additionalProperties: false,
  },
};

export const generateCleanupPRTool: Tool = {
  name: "tombstone_generate_cleanup_pr",
  description:
    "Generate a cleanup PR specification for a stale feature flag. Returns a branch name, PR title, and PR body with a checklist for removing the flag from the codebase. Use after tombstone_list_stale_flags to act on stale flags.",
  inputSchema: {
    type: "object",
    properties: {
      flag_key: {
        type: "string",
        description: "The flag key to generate a cleanup PR for (e.g. payments.checkout.v2)",
      },
      flag_name: {
        type: "string",
        description: "Human-readable name of the flag",
      },
      flag_description: {
        type: "string",
        description: "What the flag does (used to generate PR body)",
      },
      owner_id: {
        type: "string",
        description: "Owner email or team name",
      },
      days_at_100_pct: {
        type: "number",
        description: "Number of days the flag has been at 100% rollout",
        minimum: 0,
      },
      stale_score: {
        type: "number",
        description: "Stale score from 0.0 to 1.0 (higher = more stale)",
        minimum: 0,
        maximum: 1,
      },
    },
    required: ["flag_key"],
    additionalProperties: false,
  },
};

export const searchFlagsTool: Tool = {
  name: "tombstone_search_flags",
  description:
    "Natural-language search across all feature flags using the intelligence service. Returns flags ranked by relevance.",
  inputSchema: {
    type: "object",
    properties: {
      q: {
        type: "string",
        description: "Free-text query (e.g. 'payment flags disabled last week')",
      },
      limit: {
        type: "number",
        description: "Maximum results to return (default 10)",
        minimum: 1,
        maximum: 50,
      },
    },
    required: ["q"],
    additionalProperties: false,
  },
};

// ─── Handler functions ────────────────────────────────────────────────────────

async function apiFetch(
  url: string,
  apiToken: string,
  options: RequestInit = {}
): Promise<unknown> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Authorization: `Bearer ${apiToken}`,
    ...(options.headers as Record<string, string> | undefined),
  };

  const response = await fetch(url, { ...options, headers });

  const text = await response.text();
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    body = { raw: text };
  }

  if (!response.ok) {
    const message =
      typeof body === "object" && body !== null && "message" in body
        ? (body as Record<string, unknown>).message
        : text;
    throw new Error(`Tombstone API error ${response.status}: ${message}`);
  }

  return body;
}

export async function handleGetFlag(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const key = args.key as string;
  const url = `${apiUrl}/api/v1/flags/${encodeURIComponent(key)}`;
  return apiFetch(url, apiToken);
}

export async function handleKillSwitch(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const key = args.key as string;
  const reason = args.reason as string;

  if (reason.length < 10) {
    throw new Error("kill-switch reason must be at least 10 characters");
  }

  const url = `${apiUrl}/api/v1/flags/${encodeURIComponent(key)}/kill`;
  return apiFetch(url, apiToken, {
    method: "POST",
    body: JSON.stringify({ reason }),
  });
}

export async function handleBlastRadius(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const key = args.key as string;
  const targetState = args.targetState as boolean;

  const params = new URLSearchParams({
    key,
    targetState: String(targetState),
  });
  const url = `${apiUrl}/api/v1/blast-radius?${params.toString()}`;
  return apiFetch(url, apiToken);
}

export async function handleListStaleFlags(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const params = new URLSearchParams();
  if (args.days !== undefined) params.set("days", String(args.days));
  if (args.limit !== undefined) params.set("limit", String(args.limit));

  const query = params.toString();
  const url = `${apiUrl}/api/v1/stale${query ? `?${query}` : ""}`;
  return apiFetch(url, apiToken);
}

export async function handleCreateFlag(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const { key, description, enabled = false, tags, owner } = args as {
    key: string;
    description: string;
    enabled?: boolean;
    tags?: string[];
    owner?: string;
  };

  // Validate dot-notation key
  if (!/^[a-z0-9]+(?:\.[a-z0-9_-]+)+$/.test(key)) {
    throw new Error(
      "Flag key must use dot-notation (e.g. payments.checkout.v2). Only lowercase letters, digits, hyphens, and underscores are allowed in each segment."
    );
  }

  const url = `${apiUrl}/api/v1/flags`;
  const body: Record<string, unknown> = { key, description, enabled };
  if (tags !== undefined) body.tags = tags;
  if (owner !== undefined) body.owner = owner;

  return apiFetch(url, apiToken, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export async function handleSearchFlags(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const q = args.q as string;
  const limit = args.limit as number | undefined;

  const params = new URLSearchParams({ q });
  if (limit !== undefined) params.set("limit", String(limit));

  const url = `${apiUrl}/api/v1/search?${params.toString()}`;
  return apiFetch(url, apiToken);
}

export async function handleGenerateCleanupPR(
  args: Record<string, unknown>,
  apiUrl: string,
  _apiToken: string
): Promise<unknown> {
  const intelUrl = apiUrl.replace(":8081", ":8083").replace("8081", "8083");
  const body = {
    flag_key: args.flag_key as string,
    flag_name: (args.flag_name as string | undefined) ?? "",
    flag_description: (args.flag_description as string | undefined) ?? "",
    owner_id: (args.owner_id as string | undefined) ?? "",
    days_at_100_pct: (args.days_at_100_pct as number | undefined) ?? 30,
    stale_score: (args.stale_score as number | undefined) ?? 0.5,
  };
  return apiFetch(`${intelUrl}/api/v1/cleanup/generate-pr`, _apiToken, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export const openFeatureSetupTool: Tool = {
  name: "tombstone_openfeature_setup",
  description:
    "Get setup instructions for the Tombstone OpenFeature provider in TypeScript or Python. Returns copy-pasteable code snippets and required dependencies.",
  inputSchema: {
    type: "object",
    properties: {
      language: {
        type: "string",
        enum: ["typescript", "python"],
        description: "Target language for the setup instructions",
      },
    },
    required: ["language"],
    additionalProperties: false,
  },
};

// ─── OpenFeature setup handler ────────────────────────────────────────────────

export function handleOpenFeatureSetup(
  args: Record<string, unknown>
): unknown {
  const language = args.language as string;

  if (language === "typescript") {
    return {
      language: "typescript",
      package: "@tombstone/core",
      peer_dependency: "@openfeature/server-sdk (optional — interfaces are bundled inline)",
      instructions: `
// 1. Install
npm install @tombstone/core

// 2. Create the client and provider
import { TombstoneClient, TombstoneProvider } from '@tombstone/core';

const tombstoneClient = new TombstoneClient({
  sdkKey: process.env.TOMBSTONE_SDK_KEY!,
  environment: 'production',
  defaults: {
    'payments.new-flow': false,
  },
});

const provider = new TombstoneProvider(tombstoneClient);

// 3a. Use standalone (no OpenFeature SDK peer dep required)
await provider.initialize();

const details = await provider.resolveBooleanEvaluation(
  'payments.new-flow',
  false,
  { targetingKey: 'user-42' }
);
console.log(details.value, details.reason);

// 3b. Use with the official @openfeature/server-sdk
import { OpenFeature } from '@openfeature/server-sdk';

await OpenFeature.setProviderAndWait(provider as any);
const client = OpenFeature.getClient();
const enabled = await client.getBooleanValue('payments.new-flow', false, {
  targetingKey: 'user-42',
});
`.trim(),
      reason_mapping: {
        OFF: "DISABLED",
        FALLTHROUGH: "DEFAULT",
        TARGET_MATCH: "TARGETING_MATCH",
        RULE_MATCH: "TARGETING_MATCH",
        ERROR: "ERROR",
      },
    };
  }

  if (language === "python") {
    return {
      language: "python",
      package: "tombstone",
      peer_dependency: "openfeature-sdk (optional — interfaces are bundled inline)",
      instructions: `
# 1. Install
pip install tombstone
# or with openfeature-sdk peer dep:
pip install tombstone openfeature-sdk

# 2. Create the client and provider
from tombstone import TombstoneClient, TombstoneProvider
from tombstone.openfeature import OpenFeatureEvaluationContext

client = TombstoneClient(
    sdk_key="YOUR_SDK_KEY",
    environment="production",
    defaults={"payments.new-flow": False},
)
client.connect()

provider = TombstoneProvider(client)

# 3a. Use standalone (no openfeature-sdk peer dep required)
ctx = OpenFeatureEvaluationContext(targeting_key="user-42")
details = provider.resolve_boolean_details("payments.new-flow", False, ctx)
print(details.value, details.reason)

# 3b. Use with the official openfeature-sdk
from openfeature import api
from openfeature.evaluation_context import EvaluationContext as OFContext

api.set_provider(provider)
of_client = api.get_client()
of_ctx = OFContext(targeting_key="user-42")
enabled = of_client.get_boolean_value("payments.new-flow", False, of_ctx)
`.trim(),
      reason_mapping: {
        OFF: "DISABLED",
        FALLTHROUGH: "DEFAULT",
        TARGET_MATCH: "TARGETING_MATCH",
        RULE_MATCH: "TARGETING_MATCH",
        ERROR: "ERROR",
      },
    };
  }

  throw new Error(`Unsupported language: ${language}. Must be "typescript" or "python".`);
}

// ─── All tools array (for ListTools response) ────────────────────────────────

export const allTools: Tool[] = [
  getFlagTool,
  killSwitchTool,
  blastRadiusTool,
  listStaleFlagsTool,
  createFlagTool,
  searchFlagsTool,
  generateCleanupPRTool,
  openFeatureSetupTool,
];
