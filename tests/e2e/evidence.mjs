// Real control-plane visual evidence for the Models, Upstreams and Prices
// surfaces. Seeds data through the management API, drives the built SPA and
// captures the phase-two states that matter.
import { chromium } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const BASE = process.env.BASE_URL;
const MOCK = process.env.MOCK_URL;
const OUT = process.env.EVIDENCE_DIR;
const USER = process.env.ADMIN_USER;
const PASS = process.env.ADMIN_PASSWORD;
if (!BASE || !MOCK || !OUT || !USER || !PASS) throw new Error("missing env");
mkdirSync(OUT, { recursive: true });

const shots = [];
function shot(name, target) {
  const path = join(OUT, name);
  shots.push(name);
  return target.screenshot({ path });
}

async function mockCatalog(ids) {
  const response = await fetch(`${MOCK}/__mock/models`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ models: ids }),
  });
  if (!response.ok) throw new Error(`mock catalog: ${response.status}`);
}

const browser = await chromium.launch();
const context = await browser.newContext({ baseURL: BASE, viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const pageErrors = [];
page.on("pageerror", (error) => pageErrors.push(String(error)));

const call = async (method, path, data) => {
  const response = await context.request.fetch(path, {
    method,
    data,
    headers: { Origin: BASE, "content-type": "application/json" },
  });
  return response;
};
const expectOk = async (method, path, data, statuses = [200, 201, 202]) => {
  const response = await call(method, path, data);
  if (!statuses.includes(response.status())) {
    throw new Error(`${method} ${path}: ${response.status()} ${await response.text()}`);
  }
  return response.status() === 204 ? undefined : response.json();
};

async function login() {
  const response = await context.request.post("/api/auth/login", {
    data: { username: USER, password: PASS },
    headers: { Origin: BASE },
  });
  if (response.status() !== 200) throw new Error(`login: ${response.status()}`);
}

async function seed() {
  await mockCatalog(["mock-model", "mock-retired"]);
  const upstream = await expectOk("POST", "/api/upstreams", {
    name: "mock",
    kind: "api_key",
    base_url: MOCK,
    auth_style: "bearer",
    enabled: true,
    models: [],
    api_key: "sk-mock",
  });
  await expectOk("POST", `/api/upstreams/${upstream.id}/models/sync`, undefined);
  await mockCatalog(["mock-model"]);
  await expectOk("POST", `/api/upstreams/${upstream.id}/models/sync`, undefined);
  for (const [id, input, output] of [
    ["gpt-4o-mini", 0.9, 4.5],
    // Deliberately different from the builtin quote so the normalized match
    // needs review instead of being ignored.
    ["gpt-4o", 2.0, 9.0],
  ]) {
    await expectOk("POST", "/api/models", {
      model_id: id,
      enabled: true,
      billing: {
        input_usd_per_mtok: input,
        cached_input_usd_per_mtok: input / 2,
        output_usd_per_mtok: output,
      },
    });
  }
  await expectOk("POST", `/api/upstreams/${upstream.id}/models/import`, {
    upstream_model_id: "mock-model",
    mode: "bind_existing",
    model_id: "gpt-4o-mini",
  });
  const current = await expectOk("GET", `/api/upstreams/${upstream.id}`);
  await expectOk("PUT", `/api/upstreams/${upstream.id}`, {
    name: current.name,
    kind: current.kind,
    base_url: current.base_url,
    auth_style: current.auth_style,
    enabled: current.enabled,
    capabilities_revision: current.capabilities_revision,
    models: [
      {
        model_id: "gpt-4o-mini",
        upstream_model_id: "mock-model",
        enabled: true,
      },
    ],
  });
  await expectOk("POST", "/api/model-routes", {
    pattern: "gpt-*",
    match_type: "glob",
    target_model_id: "gpt-4o-mini",
    enabled: true,
  });
  await expectOk("POST", "/api/model-routes", {
    pattern: "gpt-4*",
    match_type: "glob",
    target_model_id: "gpt-4o",
    enabled: true,
  });
}

async function newRun() {
  const [response] = await Promise.all([
    page.waitForResponse(
      (r) => r.url().includes("/api/price-sources/builtin/sync") && r.request().method() === "POST",
    ),
    page.getByRole("button", { name: "获取报价" }).first().click(),
  ]);
  const run = await response.json();
  await page.getByText("可审核", { exact: true }).first().waitFor();
  return run;
}
const quoteId = (run, model, status) =>
  run.items.find((q) => q.model_id === model && q.upstream_id === null && (!status || q.status === status))
    ?.id;
const applyButton = () => page.getByRole("button", { name: /应用选中报价/ });

async function main() {
  await login();
  await seed();

  await page.goto(`${BASE}/models`);
  await page.getByRole("heading", { name: "模型路由（全局）" }).waitFor();
  // Order is the whole contract: moving one rule submits the scope's complete id
  // list in one request, and the stored positions follow it.
  const [reordered] = await Promise.all([
    page.waitForResponse(
      (r) => r.url().includes("/api/model-routes/reorder") && r.request().method() === "POST",
    ),
    page.getByRole("button", { name: "上移 gpt-4*" }).click(),
  ]);
  if (reordered.status() !== 200) throw new Error(`reorder: ${reordered.status()}`);
  const routeOrder = (await expectOk("GET", "/api/model-routes")).data.map((route) => route.pattern);
  if (routeOrder[0] !== "gpt-4*") throw new Error(`route order not stored: ${routeOrder.join(",")}`);
  await shot("models-route-order.png", page.locator('[aria-label="模型路由（全局）"]'));

  await page.goto(`${BASE}/upstreams`);
  await page.getByRole("button", { name: "目录与策略" }).first().click();
  const catalogDrawer = page.getByRole("dialog");
  await catalogDrawer.getByText("已过期").first().waitFor();
  await shot("upstreams-stale-catalog.png", catalogDrawer);
  await catalogDrawer.getByRole("button", { name: "关闭" }).click();

  await page.goto(`${BASE}/prices`);
  await page.getByRole("heading", { name: "受控价格源" }).waitFor();

  // stale_price_quote: the target price changes between quote generation and apply.
  let run = await newRun();
  await page.waitForSelector('[aria-label^="选择报价 "]:checked');
  await expectOk("PUT", "/api/models/gpt-4o-mini", {
    enabled: true,
    billing: {
      input_usd_per_mtok: 1.1,
      cached_input_usd_per_mtok: 0.55,
      output_usd_per_mtok: 5.5,
    },
  });
  await applyButton().click();
  await page.getByRole("button", { name: "应用报价" }).click();
  await page.getByText("报价基线已过期（stale_price_quote）").waitFor();
  await shot("prices-stale-price-quote.png", page.locator('[aria-label="报价批次"]'));

  // quote_review_required: a normalized suggestion is selected without review.
  run = await newRun();
  const reviewQuote = quoteId(run, "gpt-4o", "needs_review");
  if (!reviewQuote) throw new Error("no normalized quote for gpt-4o");
  await page.locator(`[aria-label="选择报价 ${reviewQuote}"]`).check();
  await applyButton().click();
  await page.getByText("非安全报价未逐项审核（quote_review_required）").waitFor();
  await shot("prices-quote-review-required.png", page.locator('[aria-label="报价批次"]'));

  // price_profile_locked: a locked profile requires the separate confirmation
  // before the mutation is sent, so the UI never emits the request.
  const model = await expectOk("GET", "/api/models/gpt-4o-mini");
  await expectOk("PUT", "/api/models/gpt-4o-mini", {
    enabled: true,
    billing: model.billing,
    billing_locked: true,
  });
  run = await newRun();
  const lockedQuote = quoteId(run, "gpt-4o-mini");
  if (!lockedQuote) throw new Error("no locked quote for gpt-4o-mini");
  await page.locator(`[aria-label="选择报价 ${lockedQuote}"]`).check();
  await page.locator(`[aria-label="已审核 ${lockedQuote}"]`).check();
  await applyButton().click();
  const confirm = page.getByRole("dialog");
  await confirm.getByText("覆盖已锁定档案（force_locked）").waitFor();
  const disabled = await confirm.getByRole("button", { name: "应用报价" }).isDisabled();
  if (!disabled) throw new Error("locked confirmation is not gated");
  await shot("prices-price-profile-locked-gate.png", confirm);
  await page.keyboard.press("Escape");

  // quote_not_applicable: the target model disappears before apply.
  run = await newRun();
  const orphanQuote = quoteId(run, "gpt-4o");
  if (!orphanQuote) throw new Error("no quote for gpt-4o");
  await page.locator(`[aria-label="选择报价 ${orphanQuote}"]`).check();
  await page.locator(`[aria-label="已审核 ${orphanQuote}"]`).check();
  await expectOk("DELETE", "/api/models/gpt-4o", undefined, [204]);
  await applyButton().click();
  await page.getByRole("button", { name: "应用报价" }).click();
  await page.getByText("报价不适用（quote_not_applicable）").waitFor();
  await shot("prices-quote-not-applicable.png", page.locator('[aria-label="报价批次"]'));

  if (pageErrors.length) throw new Error(`page errors: ${pageErrors.join("; ")}`);
  writeFileSync(
    join(OUT, "REPORT.md"),
    [
      "# T9 真实控制面视觉证据",
      "",
      "数据由控制面 API 创建，前端为 `bun run build` 产物，由同一控制面二进制提供。",
      "",
      ...shots.map((name) => `- ${name}`),
      "",
      "`price_profile_locked` 以确认门禁呈现：锁定档案必须在确认框中单独勾选 `force_locked` 才能发送，",
      "因此正常界面不会产生该 409；`stale_price_quote` 与 `quote_not_applicable` 为真实后端拒绝。",
      "`quote_review_required` 由前端使用同一拒绝码在发送前拦截。",
      "",
    ].join("\\n"),
  );
  console.log(shots.map((name) => join(OUT, name)).join("\\n"));
}

try {
  await main();
} finally {
  await browser.close();
}
