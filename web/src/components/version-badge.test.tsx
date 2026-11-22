// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { createQueryClient } from "../lib/query-client";
import { VersionBadge } from "./version-badge";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows the runtime version returned by the control plane", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            version: "1.2.3",
            commit: "abcdef123456",
            built_at: "2026-11-22T10:00:00Z",
          }),
          { headers: { "Content-Type": "application/json" } },
        ),
    ),
  );
  render(
    <QueryClientProvider client={createQueryClient()}>
      <VersionBadge />
    </QueryClientProvider>,
  );
  expect(await screen.findByText("版本 1.2.3 · abcdef1")).toBeTruthy();
});
