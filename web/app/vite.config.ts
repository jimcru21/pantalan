// Vite builds the Dashboard SPA into web/dist (ADR 0003) and serves it with
// hot reload in development. The dev server is the compose stack's `web`
// service (or `npm run dev` beside `make run`); see "Develop with Docker"
// in docs/development.md.
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const env = process.env;

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
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: "../dist",
    emptyOutDir: true,
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
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
    unstubGlobals: true,
  },
});
