import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { FlagCreateModal } from "./components/FlagCreateModal";

// Smoke test for TS-1. The dashboard build was broken by 12 packages that source
// code imported but package.json never declared (10 module imports + 2 CSS @import
// font packages). This guards that exact regression class:
//   (1) every runtime package the dashboard imports must resolve, and
//   (2) the form stack (radix-dialog + react-hook-form + zod + motion) must mount.

describe("dashboard dependency smoke test", () => {
  it("resolves every runtime package the dashboard imports", async () => {
    await Promise.all(
      [
        import("motion/react"),
        import("echarts"),
        import("echarts-for-react"),
        import("cmdk"),
        import("@radix-ui/react-dialog"),
        import("react-hook-form"),
        import("@hookform/resolvers/zod"),
        import("zod"),
        import("tailwind-merge"),
        import("@tanstack/react-query-devtools"),
      ].map((p) => expect(p).resolves.toBeDefined()),
    );
  });

  it("mounts the form modal (radix-dialog + react-hook-form + zod + motion) without throwing", () => {
    const queryClient = new QueryClient();
    render(
      <QueryClientProvider client={queryClient}>
        <FlagCreateModal open={true} onClose={() => {}} />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
