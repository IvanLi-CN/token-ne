// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { createQueryClient } from "../../lib/query-client";
import RequestsPage from "./index";

const upstream = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE";
function response(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("submits a valid upstream UUID in canonical lowercase", async () => {
  const fetch = vi.fn((url: string) =>
    Promise.resolve(
      url.startsWith("/api/requests") ? response({ data: [], next_cursor: null }) : response({}),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false } });
  const router = createMemoryRouter([{ path: "/", Component: RequestsPage }], {
    initialEntries: ["/"],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  fireEvent.change(await screen.findByLabelText("上游"), { target: { value: upstream } });
  fireEvent.click(screen.getByRole("button", { name: "应用筛选" }));
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(([url]) => url.includes(`upstream=${upstream.toLowerCase()}`)),
    ).toBe(true),
  );
  expect(fetch.mock.calls.some(([url]) => url.includes(upstream))).toBe(false);
  expect(screen.queryByRole("alert")).toBeNull();
});
