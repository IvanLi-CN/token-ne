import { existsSync, renameSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const appVersion = process.env.VITE_APP_VERSION || "0.0.0-dev";
const gitCommit = process.env.VITE_GIT_COMMIT || "unknown";
const builtAt = process.env.VITE_BUILD_TIMESTAMP || new Date().toISOString();

/** Production bundle must never pull in the mock runtime (REQ-DEMO-009). */
const demoModulePattern = /(^|[/\\])src[/\\]demo[/\\]/;
const demoLeak = (ids: string[]) =>
  `Production build reached demo modules: ${ids.join(", ")}. web/src/demo must not be imported by web/src/main.tsx.`;

export default defineConfig(({ mode }) => {
  const demo = mode === "demo";
  let versionOutDir = "";
  let demoOutDir = "";
  return {
    base: "/",
    define: {
      __APP_VERSION__: JSON.stringify(appVersion),
    },
    plugins: [
      react(),
      tailwindcss(),
      {
        name: "token-ne-version-artifact",
        configResolved(config) {
          // Read the configured outDir so `build:demo` writes into dist-demo and
          // never into the production artifacts.
          versionOutDir = resolve(config.root, config.build.outDir);
        },
        closeBundle() {
          if (!versionOutDir) return;
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
      {
        // The demo is a site of its own: emit `index.html` so any static host
        // serves `/` without a rule, leaving only the ordinary SPA rewrite.
        name: "token-ne-demo-index",
        apply: "build",
        configResolved(config) {
          demoOutDir = resolve(config.root, config.build.outDir);
        },
        closeBundle() {
          if (!demo || !demoOutDir) return;
          const emitted = resolve(demoOutDir, "demo.html");
          if (!existsSync(emitted))
            this.error(
              `demo build emitted no demo.html in ${demoOutDir}; dist-demo would have no entry document.`,
            );
          renameSync(emitted, resolve(demoOutDir, "index.html"));
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
    build: demo
      ? {
          outDir: "dist-demo",
          sourcemap: true,
          rollupOptions: { input: { demo: resolve("demo.html") } },
        }
      : {
          outDir: "dist",
          sourcemap: true,
        },
    test: {
      setupFiles: ["./vitest.setup.ts"],
      testTimeout: 20_000,
      hookTimeout: 20_000,
    },
  };
});
