import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import FlagDetail from "./index.js";

// FlagDetail pre-checks the kill switch against the evaluator's real
// blast-radius endpoint (EVAL-3) before disabling a flag — previously the
// button fired the kill unconditionally with zero visibility into blast
// radius. This suite covers: the check happening, the badge replacing the
// button on a successful check, confirm/cancel wiring the badge back to the
// real kill call, and the fail-open path when the evaluator is unreachable.

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

const ENV_STATE = {
  flag_id: "f1",
  flag_key: "my-flag",
  environment: "production",
  enabled: true,
  rollout_pct: 100,
  safe_default: "false",
  updated_at: 0,
};

const BLAST_RESULT = {
  risk_score: "HIGH",
  traffic_pct_affected: 42,
  recent_evaluation_count: 1000,
  dependent_flags_count: 2,
  affected_services: ["checkout"],
  historical_error_rate: 0.01,
  confidence: "HIGH",
};

function mockFetchImpl(url: string): Response {
  const body: unknown = url.includes("/api/v1/blast-radius")
    ? { flag_key: "my-flag", environment: "production", result: BLAST_RESULT }
    : url.includes("/api/v1/environments/snapshot")
      ? { flags: [ENV_STATE] }
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

  it("fetches blast radius and shows the badge instead of killing immediately", async () => {
    renderFlagDetail();

    const killButton = await screen.findByRole("button", {
      name: /Kill Switch/i,
    });
    fireEvent.click(killButton);

    // Badge appears with the real blast-radius data, not a direct kill.
    await waitFor(() => {
      expect(screen.getByText("Blast Radius Analysis")).toBeInTheDocument();
      expect(screen.getByText("HIGH RISK")).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: /Kill Switch/i }),
    ).not.toBeInTheDocument();

    const killCalls = (global.fetch as any).mock.calls.filter((c: any[]) =>
      String(c[0]).includes("/kill"),
    );
    expect(killCalls.length).toBe(0);
  });

  it("confirming the badge fires the real kill call", async () => {
    renderFlagDetail();

    fireEvent.click(
      await screen.findByRole("button", { name: /Kill Switch/i }),
    );
    await waitFor(() =>
      expect(screen.getByText("Blast Radius Analysis")).toBeInTheDocument(),
    );

    fireEvent.click(screen.getByRole("button", { name: /^Proceed$/i }));

    await waitFor(() => {
      const killCall = (global.fetch as any).mock.calls.find((c: any[]) =>
        String(c[0]).includes("/kill"),
      );
      expect(killCall).toBeTruthy();
      expect(JSON.parse(killCall[1].body).reason).toBe(
        "manual kill switch from dashboard",
      );
    });
  });

  it("cancelling the badge does not kill and restores the button", async () => {
    renderFlagDetail();

    fireEvent.click(
      await screen.findByRole("button", { name: /Kill Switch/i }),
    );
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

    fireEvent.click(
      await screen.findByRole("button", { name: /Kill Switch/i }),
    );

    await waitFor(() => {
      const killCall = (global.fetch as any).mock.calls.find((c: any[]) =>
        String(c[0]).includes("/kill"),
      );
      expect(killCall).toBeTruthy();
    });
    expect(screen.queryByText("Blast Radius Analysis")).not.toBeInTheDocument();
  });
});
