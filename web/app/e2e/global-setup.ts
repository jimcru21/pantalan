// Starts the built binary for the suite and stops it afterwards: a fresh
// data dir, an Admin made with `pantalan admin create`, then `pantalan
// serve` on free loopback ports. The tests find it through the environment
// (server.ts). PANTALAN_BIN overrides the binary (default: make build's
// bin/pantalan).
import { execFile, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { request } from "@playwright/test";
import { serverEnv } from "./server";

const username = "alice";

export default async function globalSetup() {
  const bin =
    process.env.PANTALAN_BIN ?? fileURLToPath(new URL("../../../bin/pantalan", import.meta.url));
  if (!existsSync(bin)) throw new Error(`no binary at ${bin}: run make build first`);

  const dataDir = await mkdtemp(join(tmpdir(), "pantalan-smoke-"));
  const [dashboardPort, proxyPort] = [await freePort(), await freePort()];
  const env = {
    ...process.env,
    PANTALAN_DATA_DIR: dataDir,
    PANTALAN_DASHBOARD_ADDR: `127.0.0.1:${dashboardPort}`,
    PANTALAN_PROXY_ADDR: `127.0.0.1:${proxyPort}`,
    PANTALAN_ADMIN_TOKEN: randomBytes(16).toString("hex"),
    PANTALAN_WEBHOOK_SECRET: randomBytes(16).toString("hex"),
  };

  // The generated password is printed alone on stdout.
  const created = await promisify(execFile)(bin, ["admin", "create", "--username", username], {
    env,
  });
  const password = created.stdout.trim();

  let output = "";
  const serve = spawn(bin, ["serve"], { env, stdio: ["ignore", "pipe", "pipe"] });
  serve.stdout.on("data", (chunk) => (output += chunk));
  serve.stderr.on("data", (chunk) => (output += chunk));
  const exited = new Promise<number | null>((resolve) => serve.on("exit", resolve));

  const url = `https://127.0.0.1:${dashboardPort}`;
  const stop = async () => {
    serve.kill("SIGTERM");
    await exited;
    await rm(dataDir, { recursive: true, force: true });
  };
  try {
    await untilHealthy(url, exited);
  } catch (err) {
    await stop();
    throw new Error(`${(err as Error).message}\n--- pantalan serve output ---\n${output}`, {
      cause: err,
    });
  }

  Object.assign(
    process.env,
    serverEnv({ url, username, password, adminToken: env.PANTALAN_ADMIN_TOKEN }),
  );
  return stop;
}

/** A TCP port nothing listens on right now. */
function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolve(typeof address === "object" && address ? address.port : 0));
    });
  });
}

/** Waits for /healthz to answer 200, failing if serve exits first. */
async function untilHealthy(url: string, exited: Promise<number | null>) {
  let code: number | null | undefined;
  void exited.then((c) => (code = c));
  const api = await request.newContext({ baseURL: url, ignoreHTTPSErrors: true });
  try {
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      if (code !== undefined) throw new Error(`pantalan serve exited with ${code}`);
      const ok = await api
        .get("/healthz")
        .then((res) => res.ok())
        .catch(() => false);
      if (ok) return;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error(`pantalan serve didn't answer ${url}/healthz within 30 s`);
  } finally {
    await api.dispose();
  }
}
