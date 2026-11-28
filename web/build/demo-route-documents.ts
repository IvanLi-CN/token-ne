import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Plugin } from "vite";
import { deployedRoutePaths } from "../src/app/route-paths";

/**
 * Static hosts serve one document per path. GitHub Pages has no rewrite rule,
 * so a deep link only reaches the demo when a copy of `index.html` sits at that
 * path. This writes one copy per route the production router answers, plus
 * `404.html` for everything else.
 *
 * The copies keep the emitted document verbatim: its asset URLs are absolute
 * from `base`, so they resolve from any depth without a per-directory fixup.
 */
export function demoRouteDocuments(): Plugin {
  let outDir = "";
  return {
    name: "token-ne-demo-route-documents",
    apply: "build",
    configResolved(config) {
      outDir = config.build.outDir;
    },
    closeBundle() {
      const document = readFileSync(join(outDir, "index.html"));
      for (const path of deployedRoutePaths) {
        if (path === "/") continue;
        const directory = join(outDir, path);
        mkdirSync(directory, { recursive: true });
        writeFileSync(join(directory, "index.html"), document);
      }
      writeFileSync(join(outDir, "404.html"), document);
    },
  };
}
