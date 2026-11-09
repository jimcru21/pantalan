// Vite builds the Dashboard SPA into web/dist (ADR 0003) and serves it with
// hot reload in development. The dev server is the compose stack's `web`
// service (or `npm run dev` beside `make run`); see "Develop with Docker"
// in docs/development.md.
import { existsSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

const env = process.env;

// The build goes to web/dist, which the binary embeds. Git keeps only its
// placeholder, so `go build` without Node still has something to embed;
// Vite's own emptyOutDir would delete it, so this empties the rest.
const outDir = fileURLToPath(new URL("../dist", import.meta.url));
const placeholder = ".gitkeep";

function emptyOutDirButThePlaceholder(): Plugin {
  return {
    name: "pantalan:empty-out-dir",
    apply: "build",
    buildStart() {
      if (!existsSync(outDir)) return;
      for (const entry of readdirSync(outDir)) {
        if (entry !== placeholder) rmSync(join(outDir, entry), { recursive: true, force: true });
      }
    },
  };
}

// Pantalan's own Dashboard certificate, so the dev server speaks HTTPS: the
// session cookie is `__Host-` and a remote browser only keeps it over HTTPS.
// The compose stack mounts the data volume's tls/; `make run` writes
// ./data/tls in the checkout.
const tlsDir =
  env.PANTALAN_DEV_TLS_DIR ?? fileURLToPath(new URL("../../data/tls", import.meta.url));
const https = existsSync(join(tlsDir, "cert.pem"))
  ? { cert: readFileSync(join(tlsDir, "cert.pem")), key: readFileSync(join(tlsDir, "key.pem")) }
  : undefined;

// The port the browser reaches the dev server on, when Docker publishes it
// on another one: hot reload's websocket must connect there.
const publishedPort = env.PANTALAN_DEV_WEB_PORT ? Number(env.PANTALAN_DEV_WEB_PORT) : undefined;

export default defineConfig({
  plugins: [react(), tailwindcss(), emptyOutDirButThePlaceholder()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir,
    emptyOutDir: false,
  },
  server: {
    host: env.PANTALAN_DEV_WEB_HOST ?? "localhost",
    port: 5173,
    strictPort: true,
    https,
    hmr: publishedPort ? { clientPort: publishedPort } : undefined,
    // Vite answers only Host headers it knows; IP addresses always pass.
    allowedHosts: env.PANTALAN_DEV_HOST ? [env.PANTALAN_DEV_HOST] : [],
    proxy: {
      // The browser's Host and Origin reach Pantalan unchanged (no
      // changeOrigin), so the API's cross-origin check sees a same-origin
      // request. secure: false accepts Pantalan's self-signed certificate.
      "/api": {
        target: env.PANTALAN_DEV_API ?? "https://localhost:8443",
        secure: false,
      },
    },
  },
  test: {
    // e2e/ holds the Playwright smoke tests (playwright.config.ts).
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
    unstubGlobals: true,
  },
});
