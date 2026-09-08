import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import FlagDetail from "./index.js";

// FlagDetail pre-checks the kill switch against the evaluator's real
// blast-radius endpoint (EVAL-3) before disabling a flag — previously the
// button fired the kill unconditionally with zero visibility into blast
// radius. This suite covers: the check happening (with the CURRENT
// rollout_pct, not 0 — an earlier version of this fix passed 0, which the
// evaluator reads as "0% of traffic sees the new configuration", always
// true for a kill, making the BLOCKED/HIGH traffic tiers unreachable —
// found by adversarial review), the badge replacing the button on a
// successful check, confirm/cancel wiring the badge back to the real kill
// call including the BLOCKED+justification path, the fail-open path when
// the evaluator is unreachable (and that an explicit Cancel click, unlike
// switching tabs, prevents that fail-open kill from firing), and that kill
// state is scoped per environment rather than shared globally.

const FLAG = {
  id: "f1",
  key: "my-flag",
  name: "My Flag",
  description: "",
  flag_type: "boolean",
  state: "ACTIVE",
  owner_id: "team-x",
  created_at: 0,
  updated_at: 0,
};

function envState(rolloutPct = 100) {
  return {
    flag_id: "f1",
    flag_key: "my-flag",
    environment: "production",
    enabled: true,
    rollout_pct: rolloutPct,
    safe_default: "false",
    updated_at: 0,
  };
}

// rollout_pct=100 is what requestKillSwitch now sends (the flag's CURRENT
// rollout, not 0) — traffic_pct_affected reflects that, matching the real
// evaluator's TrafficPctAffected = the passed rollout_pct.
const HIGH_RESULT = {
  risk_score: "HIGH",
  traffic_pct_affected: 100,
  recent_evaluation_count: 1000,
  dependent_flags_count: 2,
  affected_services: ["checkout"],
  historical_error_rate: 0.01,
  confidence: "HIGH",
};

const BLOCKED_RESULT = {
  risk_score: "BLOCKED",
  traffic_pct_affected: 100,
  recent_evaluation_count: 5000,
  dependent_flags_count: 3,
  affected_services: ["checkout", "billing"],
  historical_error_rate: 0.08,
  confidence: "HIGH",
  justification_required:
    "High traffic + elevated error rate — type a justification to override.",
};

function mockFetchImpl(
  url: string,
  blastResult: unknown = HIGH_RESULT,
): Response {
  const body: unknown = url.includes("/api/v1/blast-radius")
    ? { flag_key: "my-flag", environment: "production", result: blastResult }
    : url.includes("/api/v1/environments/snapshot")
      ? { flags: [envState()] }
      : url.includes("/api/v1/audit")
        ? { entries: [] }
        : url.includes("/rollout/posterior/")
          ? {
              alpha: 1,
              beta: 1,
              total_observations: 0,
              autonomous_enabled: false,
              current_rollout_pct: 100,
            }
          : url.includes("/api/v1/flags/my-flag/kill")
            ? {}
            : url.includes("/api/v1/circuit/")
              ? { state: "CLOSED" }
              : FLAG;
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as Response;
}

function findKillButton(env: string) {
  return screen.findByRole("button", {
    name: new RegExp(`Kill Switch.*${env}`, "i"),
  });
}

function renderFlagDetail() {
  return render(
    <MemoryRouter initialEntries={["/flags/my-flag"]}>
      <Routes>
        <Route path="/flags/:key" element={<FlagDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("FlagDetail kill switch blast-radius pre-check", () => {
  beforeEach(() => {
    global.fetch = vi.fn((url: string) =>
      Promise.resolve(mockFetchImpl(url)),
    ) as unknown as typeof fetch;
  });

  it("queries blast-radius with this env's CURRENT rollout_pct (not 0) and shows the badge instead of killing immediately", async () => {
    renderFlagDetail();

    fireEvent.click(await findKillButton("production"));

    await waitFor(() => {
      expect(screen.getByText("Blast Radius Analysis")).toBeInTheDocument();
      expect(screen.getByText("HIGH RISK")).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: /Kill Switch/i }),
    ).not.toBeInTheDocument();

    const calls = (global.fetch as any).mock.calls as any[][];
    const blastCall = calls.find((c) =>
      String(c[0]).includes("/api/v1/blast-radius"),
    );
    expect(blastCall).toBeTruthy();
    const blastUrl = new URL(String(blastCall![0]), "http://x");
    expect(blastUrl.searchParams.get("flag_key")).toBe("my-flag");
    expect(blastUrl.searchParams.get("environment")).toBe("production");
    expect(blastUrl.searchParams.get("rollout_pct")).toBe("100");

    expect(calls.some((c) => String(c[0]).includes("/kill"))).toBe(false);
  });

  it("confirming the badge fires the real kill call for the right environment", async () => {
    renderFlagDetail();

    fireEvent.click(await findKillButton("production"));
    await waitFor(() =>
      expect(screen.getByText("Blast Radius Analysis")).toBeInTheDocument(),
    );

    fireEvent.click(screen.getByRole("button", { name: /^Proceed$/i }));

    await waitFor(() => {
      const killCall = (global.fetch as any).mock.calls.find((c: any[]) =>
        String(c[0]).includes("/kill"),
      );
      expect(killCall).toBeTruthy();
      const parsedBody = JSON.parse(killCall[1].body);
      expect(parsedBody.reason).toBe("manual kill switch from dashboard");
      expect(parsedBody.environment).toBe("production");
    });
  });

  it("BLOCKED risk requires a 10+ character justification, sent through to the kill reason as an override", async () => {
    global.fetch = vi.fn((url: string) =>
      Promise.resolve(mockFetchImpl(url, BLOCKED_RESULT)),
    ) as unknown as typeof fetch;

    renderFlagDetail();

    fireEvent.click(await findKillButton("production"));
    await waitFor(() =>
      expect(screen.getByText("BLOCKED")).toBeInTheDocument(),
    );

    const overrideButton = screen.getByRole("button", {
      name: /Override & Proceed/i,
    });
    expect(overrideButton).toBeDisabled();

    fireEvent.change(
      screen.getByPlaceholderText(/Type justification to proceed/i),
      { target: { value: "short" } },
    );
    expect(overrideButton).toBeDisabled();

    fireEvent.change(
      screen.getByPlaceholderText(/Type justification to proceed/i),
      { target: { value: "known safe, rolling back a bad experiment" } },
    );
    expect(overrideButton).not.toBeDisabled();

    fireEvent.click(overrideButton);

    await waitFor(() => {
      const killCall = (global.fetch as any).mock.calls.find((c: any[]) =>
        String(c[0]).includes("/kill"),
      );
      expect(killCall).toBeTruthy();
      const parsedBody = JSON.parse(killCall[1].body);
      expect(parsedBody.reason).toContain("override justification:");
      expect(parsedBody.reason).toContain(
        "known safe, rolling back a bad experiment",
      );
    });
  });

  it("cancelling the badge does not kill and restores the button", async () => {
    renderFlagDetail();

    fireEvent.click(await findKillButton("production"));
    await waitFor(() =>
      expect(screen.getByText("Blast Radius Analysis")).toBeInTheDocument(),
    );

    fireEvent.click(screen.getByRole("button", { name: /Cancel/i }));

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: /Kill Switch/i }),
      ).toBeInTheDocument();
    });
    const killCalls = (global.fetch as any).mock.calls.filter((c: any[]) =>
      String(c[0]).includes("/kill"),
    );
    expect(killCalls.length).toBe(0);
  });

  it("fails open — kills directly, without ever showing the badge, if the blast-radius check errors", async () => {
    global.fetch = vi.fn((url: string) => {
      if (url.includes("/api/v1/blast-radius")) {
        return Promise.resolve({
          ok: false,
          status: 503,
          json: async () => ({}),
        } as Response);
      }
      return Promise.resolve(mockFetchImpl(url));
    }) as unknown as typeof fetch;

    renderFlagDetail();

    fireEvent.click(await findKillButton("production"));

    await waitFor(() => {
      const killCall = (global.fetch as any).mock.calls.find((c: any[]) =>
        String(c[0]).includes("/kill"),
      );
      expect(killCall).toBeTruthy();
    });
    expect(screen.queryByText("Blast Radius Analysis")).not.toBeInTheDocument();
  });

  it("the Kill Switch button disables synchronously while checking, so a second click cannot start an overlapping request", async () => {
    // requestKillSwitch bumps a per-env generation ref specifically to
    // guard against a stale, late-resolving check's fail-open branch
    // firing after a newer request for the same env has already taken
    // over — defensive-in-depth for a same-tick double-dispatch a real
    // browser input device could produce. The button's `disabled`
    // attribute is the first, always-active line of defense; this test
    // verifies that line of defense actually holds under React's render
    // model (fireEvent.click flushes synchronously between calls, so a
    // second click lands on an already-disabled element and never
    // dispatches).
    let blastRadiusCallCount = 0;
    global.fetch = vi.fn((url: string) => {
      if (url.includes("/api/v1/blast-radius")) {
        blastRadiusCallCount += 1;
        return new Promise<Response>(() => {
          /* never resolves — button must stay disabled */
        });
      }
      return Promise.resolve(mockFetchImpl(url));
    }) as unknown as typeof fetch;

    renderFlagDetail();

    const button = await findKillButton("production");
    fireEvent.click(button);
    await waitFor(() => expect(button).toBeDisabled());
    fireEvent.click(button);

    expect(blastRadiusCallCount).toBe(1);
  });

  it("kill-flow state (checking/confirming) for one environment does not leak into another environment's button", async () => {
    let resolveBlastRadius: ((v: Response) => void) | undefined;
    global.fetch = vi.fn((url: string) => {
      if (url.includes("/api/v1/blast-radius")) {
        return new Promise<Response>((resolve) => {
          resolveBlastRadius = resolve;
        });
      }
      return Promise.resolve(mockFetchImpl(url));
    }) as unknown as typeof fetch;

    renderFlagDetail();

    fireEvent.click(await findKillButton("production"));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /Checking blast radius/i }),
      ).toBeInTheDocument(),
    );

    // Switch to staging while production's check is still pending.
    fireEvent.click(screen.getByRole("button", { name: /^staging$/i }));

    // Staging's own Kill Switch button must be idle, not stuck on
    // production's "Checking blast radius…" status.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: /Kill Switch.*staging/i }),
      ).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: /Checking blast radius/i }),
    ).not.toBeInTheDocument();

    // Production's own tab, now inactive, must surface that it still has a
    // check in flight — the only signal for a backgrounded env's pending
    // operation (found missing by adversarial review).
    expect(
      document.querySelector('[title="Kill switch: checking"]'),
    ).toBeInTheDocument();

    // Resolve production's check now; switching back must show the badge
    // that finished in the background, not a lost/reset state.
    resolveBlastRadius!(mockFetchImpl("/api/v1/blast-radius"));
    fireEvent.click(screen.getByRole("button", { name: /^production$/i }));

    await waitFor(() =>
      expect(screen.getByText("Blast Radius Analysis")).toBeInTheDocument(),
    );
  });
});
