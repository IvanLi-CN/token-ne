import { existsSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { demoRouteDocuments } from "./build/demo-route-documents";

const appVersion = process.env.VITE_APP_VERSION || "0.0.0-dev";
const gitCommit = process.env.VITE_GIT_COMMIT || "unknown";
const builtAt = process.env.VITE_BUILD_TIMESTAMP || new Date().toISOString();

/** Production bundle must never pull in the mock runtime (REQ-DEMO-009). */
const demoModulePattern = /(^|[/\\])src[/\\]demo[/\\]/;
const demoLeak = (ids: string[]) =>
  `Production build reached demo modules: ${ids.join(", ")}. web/src/demo must not be imported by web/src/main.tsx.`;

/**
 * The demo differs from production only by its mode: `index.html` is the single
 * entry document and `demo` swaps the module it loads. `index.html` points at a
 * placeholder so the substitution cannot drift from what it replaces.
 */
const entryMarker = "/src/__entry__.tsx";

/** Rewrite the single entry script of `index.html` to this build's module. */
const entrySwap = (entry: string, title: string, robots: string) => ({
  name: "token-ne-entry-swap",
  transformIndexHtml: {
    order: "pre" as const,
    handler(html: string, ctx: { path: string }) {
      const swapped = html
        .replace(entryMarker, entry)
        .replace("<title>token-ne</title>", `<title>${title}</title>`)
        .replace("__robots__", robots);
      // `index.html` is the only document whose swap the build depends on; the
      // visual-test fixtures go through here too and own their own entry.
      if (ctx.path !== "/index.html") return swapped;
      if (!swapped.includes(`src="${entry}"`))
        throw new Error(`web/index.html loads no entry to swap for ${entry}.`);
      if (!swapped.includes(`content="${robots}"`))
        throw new Error("web/index.html has no __robots__ placeholder to rewrite.");
      return swapped;
    },
  },
});

export default defineConfig(({ mode }) => {
  const demo = mode === "demo";
  let versionOutDir = "";
  return {
    base: "/",
    define: {
      __APP_VERSION__: JSON.stringify(appVersion),
    },
    plugins: [
      react(),
      tailwindcss(),
      entrySwap(
        demo ? "/src/demo/entry.tsx" : "/src/main.tsx",
        demo ? "token-ne demo" : "token-ne",
        demo ? "noindex" : "index, follow",
      ),
      // The demo is the only build that gets routed documents: production is
      // embedded in the control plane, which serves its own SPA fallback.
      ...(demo ? [demoRouteDocuments()] : []),
      {
        name: "token-ne-version-artifact",
        configResolved(config) {
          // Read the configured outDir so `build:demo` writes into dist-demo and
          // never into the production artifacts.
          versionOutDir = resolve(config.root, config.build.outDir);
        },
        closeBundle() {
          // A failed build never creates the output directory; writing the
          // artifact then would mask the real error with an ENOENT.
          if (!versionOutDir || !existsSync(versionOutDir)) return;
          writeFileSync(
            resolve(versionOutDir, "version.json"),
            `${JSON.stringify({ version: appVersion, commit: gitCommit, built_at: builtAt }, null, 2)}\n`,
          );
        },
      },
      {
        name: "token-ne-demo-isolation",
        apply: "build",
        buildEnd(error) {
          if (demo || error) return;
          // The module graph catches tree-shaken imports that never reach a
          // chunk; the bundle check below catches anything actually emitted.
          const leaked = [...this.getModuleIds()].filter((id) => demoModulePattern.test(id));
          if (leaked.length > 0) this.error(demoLeak(leaked));
        },
        generateBundle(_options, bundle) {
          if (demo) return;
          const leaked = Object.values(bundle).flatMap((output) =>
            output.type === "chunk"
              ? output.moduleIds.filter((id) => demoModulePattern.test(id))
              : [],
          );
          if (leaked.length > 0) this.error(demoLeak(leaked));
        },
      },
    ],
    server: {
      host: "127.0.0.1",
      port: 60080,
      strictPort: true,
      // The SPA calls the management API on its own origin. Point this at the
      // control plane so `bun run dev` works without a reverse proxy.
      proxy: {
        "/api": process.env.TOKEN_NE_API ?? "http://127.0.0.1:8080",
        "/v1": process.env.TOKEN_NE_API ?? "http://127.0.0.1:8080",
      },
    },
    build: {
      outDir: demo ? "dist-demo" : "dist",
      sourcemap: true,
    },
    test: {
      setupFiles: ["./vitest.setup.ts"],
      testTimeout: 20_000,
      hookTimeout: 20_000,
    },
  };
});
