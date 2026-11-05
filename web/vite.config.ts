import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { existsSync, readFileSync } from "node:fs";

export function legalDependencies() {
  const manifest = JSON.parse(
    readFileSync(
      new URL("../legal/dependencies.json", import.meta.url),
      "utf8",
    ),
  );
  const reviewed = new Set(
    manifest.components
      .filter((p: { ecosystem: string }) => p.ecosystem === "npm")
      .map((p: { name: string; version: string }) => `${p.name}@${p.version}`),
  );
  return {
    name: "reviewed-dependency-notices",
    generateBundle(
      _options: unknown,
      bundle: Record<
        string,
        { type: string; modules?: Record<string, unknown> }
      >,
    ) {
      for (const chunk of Object.values(bundle)) {
        if (chunk.type !== "chunk") continue;
        for (const id of Object.keys(chunk.modules ?? {})) {
          if (!id.includes("/node_modules/") || id.startsWith("\0")) continue;
          let dir = dirname(id.split("?")[0] ?? id);
          while (dir !== dirname(dir) && !existsSync(join(dir, "package.json")))
            dir = dirname(dir);
          const p = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
          if (!reviewed.has(`${p.name}@${p.version}`)) {
            throw new Error(
              `Missing reviewed distribution notice for ${p.name}@${p.version}`,
            );
          }
        }
      }
    },
  };
}

const managerURL = new URL(
  process.env.HAPROXY_DEV_MANAGER_URL ?? "https://localhost:18445",
);
if (
  managerURL.protocol !== "https:" ||
  !["localhost", "127.0.0.1", "[::1]"].includes(managerURL.hostname) ||
  managerURL.username ||
  managerURL.password ||
  managerURL.pathname !== "/" ||
  managerURL.search ||
  managerURL.hash
) {
  throw new Error("HAPROXY_DEV_MANAGER_URL must be a loopback HTTPS origin");
}
export default defineConfig({
  plugins: [react(), legalDependencies()],
  resolve: {
    alias: {
      "@ecraela/ui/styles.css": fileURLToPath(
        new URL("./ui/styles/index.css", import.meta.url),
      ),
      "@ecraela/ui/icons": fileURLToPath(
        new URL("./ui/icons.tsx", import.meta.url),
      ),
      "@ecraela/ui": fileURLToPath(new URL("./ui/index.ts", import.meta.url)),
    },
  },
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  server: {
    port: 5180,
    strictPort: true,
    proxy: {
      "/api/haproxy": { target: managerURL.origin, secure: false },
    },
  },
  test: { environment: "node", include: ["src/**/*.test.{ts,tsx}"] },
});
