// @vitest-environment jsdom
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SourceModel } from "../../lib/api";
import { createQueryClient } from "../../lib/query-client";
import { PriceSourceCatalogSection } from "./catalog";

const timestamp = "2026-11-22T00:00:00Z";
const billing = {
  input_usd_per_mtok: 1,
  cached_input_usd_per_mtok: 0,
  output_usd_per_mtok: 2,
};

function sourceModel(overrides: Partial<SourceModel>): SourceModel {
  return {
    source_model_id: "acme/model-a",
    vendor: "Acme",
    released_at: timestamp,
    deprecated_at: null,
    billing,
    model_id: "acme/model-a",
    installed: false,
    synced: false,
    ...overrides,
  };
}

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Handler = (url: string, init: RequestInit) => Response | undefined;

function mock(handler?: Handler) {
  const fetch = vi.fn((url: string, init: RequestInit = {}) =>
    Promise.resolve(handler?.(url, init) ?? response({ data: [] })),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

function mount() {
  const client = createQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <PriceSourceCatalogSection />
    </QueryClientProvider>,
  );
}

function catalogHandler(
  rows: SourceModel[],
  calls: { install: string[][]; unsync: string[][] },
): Handler {
  return (url, init) => {
    if (url === "/api/price-sources") {
      return response({
        data: [{ id: "builtin", name: "内置价目表", enabled: true, last_synced_at: null }],
      });
    }
    if (url.startsWith("/api/price-sources/builtin/models/install") && init.method === "POST") {
      const body = JSON.parse(String(init.body)) as { source_model_ids: string[] };
      calls.install.push(body.source_model_ids);
      return response(
        {
          source_id: "builtin",
          data: body.source_model_ids.map((id) => ({
            source_model_id: id,
            model_id: id,
            outcome: "created",
          })),
        },
        201,
      );
    }
    if (url.startsWith("/api/price-sources/builtin/models/unsync") && init.method === "POST") {
      const body = JSON.parse(String(init.body)) as { source_model_ids: string[] };
      calls.unsync.push(body.source_model_ids);
      return response({ source_id: "builtin", cleared: body.source_model_ids.length });
    }
    if (url.startsWith("/api/price-sources/builtin/models")) {
      return response({
        source_id: "builtin",
        fetched_at: timestamp,
        vendors: ["Acme"],
        data: rows,
      });
    }
    return undefined;
  };
}

function toggle(switchElement: HTMLElement) {
  fireEvent.pointerDown(switchElement);
  fireEvent.pointerUp(window);
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("price source catalog switches", () => {
  it("renders a synced row off once its global model is deleted and installs on demand", async () => {
    const calls = { install: [] as string[][], unsync: [] as string[][] };
    const fetch = mock(
      catalogHandler(
        [
          sourceModel({ source_model_id: "acme/deleted", model_id: "acme/deleted", synced: true }),
          sourceModel({
            source_model_id: "acme/live",
            model_id: "acme/live",
            synced: true,
            installed: true,
          }),
        ],
        calls,
      ),
    );
    mount();

    const deleted = await screen.findByRole("switch", { name: /开始同步 acme\/deleted/ });
    expect(deleted.getAttribute("aria-checked")).toBe("false");
    expect(
      screen.getByRole("switch", { name: /停止同步 acme\/live/ }).getAttribute("aria-checked"),
    ).toBe("true");

    toggle(deleted);
    await waitFor(() => expect(calls.install).toEqual([["acme/deleted"]]));
    // The install summary stays visible even when the filter body is collapsed.
    const summary = await waitFor(() => {
      const node = Array.from(document.querySelectorAll('[role="status"]')).find((element) =>
        element.textContent?.includes("已创建"),
      );
      expect(node).toBeTruthy();
      return node;
    });
    expect(document.getElementById("price-filters")?.contains(summary as Node)).toBe(false);
    // Turning a stale mark back on recreates the model through install; the
    // catalog never deletes a global model on its own.
    expect(fetch.mock.calls.some(([, init]) => (init as RequestInit)?.method === "DELETE")).toBe(
      false,
    );
  });

  it("clears the mark without deleting the global model or asking for confirmation", async () => {
    const calls = { install: [] as string[][], unsync: [] as string[][] };
    const fetch = mock(
      catalogHandler(
        [
          sourceModel({
            source_model_id: "acme/live",
            model_id: "acme/live",
            synced: true,
            installed: true,
          }),
        ],
        calls,
      ),
    );
    mount();

    const live = await screen.findByRole("switch", { name: /停止同步 acme\/live/ });
    toggle(live);

    await waitFor(() => expect(calls.unsync).toEqual([["acme/live"]]));
    expect(screen.queryByRole("dialog")).toBeNull();
    const deletes = fetch.mock.calls.filter(
      ([, init]) => (init as RequestInit)?.method === "DELETE",
    );
    expect(deletes).toEqual([]);
  });
});
