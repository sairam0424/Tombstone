import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { TargetingRulesTab } from "./TargetingRulesTab.js";

const RULES_URL =
  "http://localhost:8081/api/v1/flags/my-flag/environments/production/rules";
const fixtureAuthValue = "fixture-auth-value";

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

describe("TargetingRulesTab", () => {
  beforeEach(() => {
    global.fetch = vi.fn();
  });

  function renderTab() {
    return render(
      <TargetingRulesTab
        flagKey="my-flag"
        apiUrl="http://localhost:8081"
        token={fixtureAuthValue}
        environment="production"
        environments={["development", "staging", "production"]}
        onEnvironmentChange={() => {}}
      />,
    );
  }

  it("lists rules sorted by priority (lowest first, matching real evaluation order)", async () => {
    (global.fetch as any).mockResolvedValueOnce(
      jsonResponse({
        targeting_rules: [
          {
            id: "r2",
            flag_id: "f1",
            environment: "production",
            rule_type: "USER",
            attribute: "country",
            operator: "IN",
            values: ["US", "CA"],
            variation: "true",
            priority: 5,
            created_at: 0,
          },
          {
            id: "r1",
            flag_id: "f1",
            environment: "production",
            rule_type: "SEGMENT",
            attribute: "plan",
            operator: "EQ",
            values: ["enterprise"],
            variation: "true",
            priority: 1,
            created_at: 0,
          },
        ],
      }),
    );

    renderTab();

    await waitFor(() => {
      expect(screen.getByText("country")).toBeInTheDocument();
      expect(screen.getByText("plan")).toBeInTheDocument();
    });

    const rows = screen.getAllByRole("row").slice(1); // drop header row
    expect(rows[0]).toHaveTextContent("plan"); // priority 1, evaluated first
    expect(rows[1]).toHaveTextContent("country"); // priority 5
  });

  it("adding a rule POSTs comma-separated values as a real JSON array and refetches", async () => {
    (global.fetch as any)
      .mockResolvedValueOnce(jsonResponse({ targeting_rules: [] })) // initial load
      .mockResolvedValueOnce(
        jsonResponse(
          {
            id: "new-id",
            flag_id: "f1",
            environment: "production",
            rule_type: "USER",
            attribute: "country",
            operator: "IN",
            values: ["US", "CA"],
            variation: "true",
            priority: 0,
            created_at: 0,
          },
          201,
        ),
      ) // POST response
      .mockResolvedValueOnce(jsonResponse({ targeting_rules: [] })); // refetch after add

    renderTab();
    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(1));

    fireEvent.change(screen.getByPlaceholderText(/country, user_id, plan/i), {
      target: { value: "country" },
    });
    fireEvent.change(screen.getByPlaceholderText(/US, CA, UK/i), {
      target: { value: " US , CA " },
    });
    fireEvent.change(screen.getByPlaceholderText(/true, variant-a/i), {
      target: { value: "true" },
    });
    fireEvent.click(screen.getByRole("button", { name: /add rule/i }));

    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(3));

    const postCall = (global.fetch as any).mock.calls[1];
    expect(postCall[0]).toBe(RULES_URL);
    const body = JSON.parse(postCall[1].body);
    expect(body.attribute).toBe("country");
    expect(body.values).toEqual(["US", "CA"]);
    expect(body.variation).toBe("true");
  });

  it("shows the real backend error message when adding a rule fails", async () => {
    (global.fetch as any)
      .mockResolvedValueOnce(jsonResponse({ targeting_rules: [] }))
      .mockResolvedValueOnce(
        jsonResponse({ error: "operator is not a recognized operator" }, 400),
      );

    renderTab();
    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(1));

    fireEvent.change(screen.getByPlaceholderText(/country, user_id, plan/i), {
      target: { value: "country" },
    });
    fireEvent.change(screen.getByPlaceholderText(/US, CA, UK/i), {
      target: { value: "US" },
    });
    fireEvent.change(screen.getByPlaceholderText(/true, variant-a/i), {
      target: { value: "true" },
    });
    fireEvent.click(screen.getByRole("button", { name: /add rule/i }));

    await waitFor(() => {
      expect(
        screen.getByText("operator is not a recognized operator"),
      ).toBeInTheDocument();
    });
  });

  it("deleting a rule requires a second confirming click before calling DELETE", async () => {
    (global.fetch as any)
      .mockResolvedValueOnce(
        jsonResponse({
          targeting_rules: [
            {
              id: "r1",
              flag_id: "f1",
              environment: "production",
              rule_type: "USER",
              attribute: "country",
              operator: "IN",
              values: ["US"],
              variation: "true",
              priority: 0,
              created_at: 0,
            },
          ],
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ deleted: true, id: "r1" }));

    renderTab();
    await waitFor(() =>
      expect(screen.getByText("country")).toBeInTheDocument(),
    );

    const deleteButton = screen.getByRole("button", { name: /^delete$/i });
    fireEvent.click(deleteButton);
    expect(
      screen.getByRole("button", { name: /confirm delete\?/i }),
    ).toBeInTheDocument();
    expect(global.fetch).toHaveBeenCalledTimes(1); // still just the initial load

    fireEvent.click(screen.getByRole("button", { name: /confirm delete\?/i }));

    await waitFor(() => {
      expect(global.fetch).toHaveBeenCalledTimes(2);
      expect((global.fetch as any).mock.calls[1][0]).toBe(`${RULES_URL}/r1`);
      expect((global.fetch as any).mock.calls[1][1].method).toBe("DELETE");
    });
  });

  it("blocks submitting the add form with an empty Values field (adversarial-review finding: empty values silently produce wrong-semantics rules for EQ/NEQ/GT/GTE/LT/LTE)", async () => {
    (global.fetch as any).mockResolvedValueOnce(
      jsonResponse({ targeting_rules: [] }),
    );

    renderTab();
    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(1));

    fireEvent.change(screen.getByPlaceholderText(/country, user_id, plan/i), {
      target: { value: "country" },
    });
    fireEvent.change(screen.getByPlaceholderText(/true, variant-a/i), {
      target: { value: "true" },
    });
    // Values deliberately left blank.
    fireEvent.click(screen.getByRole("button", { name: /add rule/i }));

    // jsdom enforces the native `required` constraint on form submit, same
    // as a real browser -- no POST should ever fire.
    await new Promise((r) => setTimeout(r, 10));
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });

  it("warns when a non-functional operator is selected and clears the warning when switched away (adversarial-review finding: REGEX/SEMVER/DATE operators silently never match in one or more SDKs)", async () => {
    (global.fetch as any).mockResolvedValueOnce(
      jsonResponse({ targeting_rules: [] }),
    );

    renderTab();
    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(1));

    fireEvent.change(screen.getByRole("combobox", { name: /^operator$/i }), {
      target: { value: "REGEX" },
    });
    expect(
      screen.getByText(/not implemented in any sdk yet/i),
    ).toBeInTheDocument();

    fireEvent.change(screen.getByRole("combobox", { name: /^operator$/i }), {
      target: { value: "SEMVER_GTE" },
    });
    expect(
      screen.getByText(/not implemented in the typescript sdk/i),
    ).toBeInTheDocument();

    fireEvent.change(screen.getByRole("combobox", { name: /^operator$/i }), {
      target: { value: "IN" },
    });
    expect(screen.queryByText(/not implemented/i)).not.toBeInTheDocument();
  });

  it("clicking an environment pill calls onEnvironmentChange instead of managing its own env state (adversarial-review finding: the tab had no visible env control at all)", async () => {
    (global.fetch as any).mockResolvedValueOnce(
      jsonResponse({ targeting_rules: [] }),
    );
    const onEnvironmentChange = vi.fn();

    render(
      <TargetingRulesTab
        flagKey="my-flag"
        apiUrl="http://localhost:8081"
        token={fixtureAuthValue}
        environment="production"
        environments={["development", "staging", "production"]}
        onEnvironmentChange={onEnvironmentChange}
      />,
    );
    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole("button", { name: /^staging$/i }));
    expect(onEnvironmentChange).toHaveBeenCalledWith("staging");
  });
});
