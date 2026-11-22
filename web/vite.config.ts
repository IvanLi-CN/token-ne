import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const appVersion = process.env.VITE_APP_VERSION || "0.0.0-dev";
const gitCommit = process.env.VITE_GIT_COMMIT || "unknown";
const builtAt = process.env.VITE_BUILD_TIMESTAMP || new Date().toISOString();

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  plugins: [
    react(),
    tailwindcss(),
    {
      name: "token-ne-version-artifact",
      closeBundle() {
        writeFileSync(
          resolve("dist/version.json"),
          `${JSON.stringify({ version: appVersion, commit: gitCommit, built_at: builtAt }, null, 2)}\n`,
        );
      },
    },
  ],
  server: {
    host: "127.0.0.1",
    port: 60080,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
  test: {
    setupFiles: ["./vitest.setup.ts"],
    testTimeout: 20_000,
    hookTimeout: 20_000,
  },
});
