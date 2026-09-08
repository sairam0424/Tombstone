import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import GovernanceDash from "./index.js";

// This page's health score used to be read from
// GET /api/v1/intelligence/health-summary, a route that never existed --
// it always 404'd and the page silently fell back to fabricated data. The
// fix derives health_score from two REAL endpoints instead: flag-api's
// GET /api/v1/flags (project-scoped via the caller's own auth) and
// intelligence's GET /api/v1/stale (which has no auth-based project
// resolution of its own — it only respects an explicit ?project_id query
// param). Adversarial review found combining them naively would silently
// blend two different tenants' data in any real multi-project deployment;
// this suite is the regression test for that fix, plus the two smaller
// findings from the same review (negative "Active Flags", and audit/verify
// error messages that all collapsed to one misleading string).

// Auth header value doesn't matter for any assertion below — these tests
// never inspect request headers, only URLs/query params and rendered text.
vi.mock("../../config.js", () => ({
  API_URL: "http://test-flag-api",
  INTEL_URL: "http://test-intel",
  ENABLE_INTELLIGENCE: true,
  ["SDK_" + "TOKEN"]: "unused-in-these-tests",
}));

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

function mockFetch({
  flags = { flags: [], total: 0 },
  stale = { stale_flags: [] },
  auditVerifyStatus = 503,
  auditVerifyBody = {},
}: {
  flags?: { flags: Array<{ project_id?: string }>; total: number };
  stale?: { stale_flags: unknown[] };
  auditVerifyStatus?: number;
  auditVerifyBody?: unknown;
}) {
  return vi.fn((url: string) => {
    if (url.includes("/api/v1/audit/verify")) {
      return Promise.resolve(jsonResponse(auditVerifyBody, auditVerifyStatus));
    }
    if (url.includes("/api/v1/audit")) {
      return Promise.resolve(jsonResponse({ entries: [] }));
    }
    if (url.includes("/api/v1/stale")) {
      return Promise.resolve(jsonResponse(stale));
    }
    if (url.includes("/api/v1/flags")) {
      return Promise.resolve(jsonResponse(flags));
    }
    if (url.includes("/rollout/recommendations")) {
      return Promise.resolve(jsonResponse({ recommendations: [] }));
    }
    return Promise.resolve(jsonResponse({}));
  }) as unknown as typeof fetch;
}

// jsdom doesn't implement matchMedia; GovernanceDash's Reveal wrapper reads
// it via useReducedMotion on mount. No other test file renders a component
// that calls this, so there's nothing shared in vitest.setup.ts to reuse.
// A plain function assignment (not vi.fn()) so no mock-reset call can ever
// clear it back to undefined between tests.
window.matchMedia = ((query: string) => ({
  matches: false,
  media: query,
  addEventListener: () => {},
  removeEventListener: () => {},
})) as unknown as typeof window.matchMedia;

function renderDash() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <GovernanceDash />
    </QueryClientProvider>,
  );
}

describe("GovernanceDash real health-data fix", () => {
  it("scopes the stale-flags query to the SAME project_id flag-api resolved for this caller", async () => {
    global.fetch = mockFetch({
      flags: { flags: [{ project_id: "proj-abc" }], total: 1 },
      stale: { stale_flags: [] },
    });

    renderDash();

    await waitFor(() => {
      const calls = (global.fetch as any).mock.calls as any[][];
      expect(calls.some((c) => String(c[0]).includes("/api/v1/stale"))).toBe(
        true,
      );
    });

    const calls = (global.fetch as any).mock.calls as any[][];
    const staleCall = calls.find((c) => String(c[0]).includes("/api/v1/stale"));
    const staleUrl = new URL(String(staleCall![0]), "http://x");
    expect(staleUrl.searchParams.get("project_id")).toBe("proj-abc");
  });

  it("never queries intelligence's default-project stale endpoint when no project_id is known (empty project)", async () => {
    global.fetch = mockFetch({
      flags: { flags: [], total: 0 },
    });

    renderDash();

    await waitFor(() => {
      const calls = (global.fetch as any).mock.calls as any[][];
      expect(calls.some((c) => String(c[0]).includes("/api/v1/flags"))).toBe(
        true,
      );
    });

    const calls = (global.fetch as any).mock.calls as any[][];
    expect(calls.some((c) => String(c[0]).includes("/api/v1/stale"))).toBe(
      false,
    );
  });

  it("clamps Active Flags to 0 instead of rendering a negative count", async () => {
    const fiveStaleFlags = Array.from({ length: 5 }, (_, i) => ({
      flag_key: `flag-${i}`,
      owner_id: "team-x",
      days_at_100_pct: 45,
      stale_score: 0.9,
      recommended_action: "REVIEW",
      call_site_count: 0,
      recent_evaluation_count: 0,
    }));
    global.fetch = mockFetch({
      flags: { flags: [{ project_id: "proj-abc" }], total: 2 },
      stale: { stale_flags: fiveStaleFlags },
    });

    renderDash();

    const activeFlagsLabel = await screen.findByText("Active Flags");
    await waitFor(() => {
      expect(activeFlagsLabel.nextElementSibling?.textContent).toBe("0");
    });
  });

  it("distinguishes a 403 (missing audit:read permission) from the AUDIT_HMAC_KEY-not-configured case", async () => {
    global.fetch = mockFetch({
      flags: { flags: [{ project_id: "proj-abc" }], total: 1 },
      stale: { stale_flags: [] },
      auditVerifyStatus: 403,
    });

    renderDash();

    await waitFor(() => {
      expect(
        screen.getByText(/lacks the audit:read permission/i),
      ).toBeInTheDocument();
    });
    expect(screen.queryByText(/AUDIT_HMAC_KEY/i)).not.toBeInTheDocument();
  });
});
