#!/usr/bin/env node
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  type CallToolResult,
} from "@modelcontextprotocol/sdk/types.js";

import {
  allTools,
  handleGetFlag,
  handleKillSwitch,
  handleBlastRadius,
  handleListStaleFlags,
  handleCreateFlag,
  handleSearchFlags,
  handleGenerateCleanupPR,
  handleOpenFeatureSetup,
  handleGetDependencyGraph,
} from "./tools/flags.js";

// ─── Configuration ────────────────────────────────────────────────────────────

const TOMBSTONE_API_URL = process.env.TOMBSTONE_API_URL?.replace(/\/$/, "");
const TOMBSTONE_TOKEN = process.env.TOMBSTONE_TOKEN ?? process.env.TOMBSTONE_API_TOKEN;

if (!TOMBSTONE_API_URL) {
  console.error("ERROR: TOMBSTONE_API_URL environment variable is required");
  process.exit(1);
}

if (!TOMBSTONE_TOKEN) {
  console.error(
    "ERROR: TOMBSTONE_TOKEN (or TOMBSTONE_API_TOKEN) environment variable is required"
  );
  process.exit(1);
}

// ─── Server setup ─────────────────────────────────────────────────────────────

const server = new Server(
  {
    name: "tombstone",
    version: "0.1.0",
  },
  {
    capabilities: {
      tools: {},
    },
  }
);

// ─── List tools ───────────────────────────────────────────────────────────────

server.setRequestHandler(ListToolsRequestSchema, async () => {
  return { tools: allTools };
});

// ─── Call tool dispatcher ─────────────────────────────────────────────────────

server.setRequestHandler(CallToolRequestSchema, async (request): Promise<CallToolResult> => {
  const { name, arguments: args = {} } = request.params;
  const apiUrl = TOMBSTONE_API_URL as string;
  const apiToken = TOMBSTONE_TOKEN as string;

  try {
    let result: unknown;

    switch (name) {
      case "tombstone_get_flag":
        result = await handleGetFlag(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_kill_switch":
        result = await handleKillSwitch(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_blast_radius":
        result = await handleBlastRadius(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_list_stale_flags":
        result = await handleListStaleFlags(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_create_flag":
        result = await handleCreateFlag(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_search_flags":
        result = await handleSearchFlags(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_generate_cleanup_pr":
        result = await handleGenerateCleanupPR(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      case "tombstone_openfeature_setup":
        result = handleOpenFeatureSetup(args as Record<string, unknown>);
        break;

      case "tombstone_get_dependency_graph":
        result = await handleGetDependencyGraph(args as Record<string, unknown>, apiUrl, apiToken);
        break;

      default:
        return {
          content: [
            {
              type: "text",
              text: `Unknown tool: ${name}`,
            },
          ],
          isError: true,
        };
    }

    return {
      content: [
        {
          type: "text",
          text: JSON.stringify(result, null, 2),
        },
      ],
    };
  } catch (error: unknown) {
    const message =
      error instanceof Error ? error.message : String(error);
    return {
      content: [
        {
          type: "text",
          text: `Error calling ${name}: ${message}`,
        },
      ],
      isError: true,
    };
  }
});

// ─── Transport ────────────────────────────────────────────────────────────────

const transport = new StdioServerTransport();

await server.connect(transport);
