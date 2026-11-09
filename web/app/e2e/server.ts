// The Pantalan binary under test, as global-setup.ts hands it to the tests:
// through environment variables, which the test workers inherit.

/** What the tests need to reach the running binary. */
export type Server = { url: string; username: string; password: string; adminToken: string };

const names = {
  url: "PANTALAN_SMOKE_URL",
  username: "PANTALAN_SMOKE_USERNAME",
  password: "PANTALAN_SMOKE_PASSWORD",
  adminToken: "PANTALAN_SMOKE_ADMIN_TOKEN",
} as const;

/** The environment variables that describe server. */
export function serverEnv(server: Server): Record<string, string> {
  return {
    [names.url]: server.url,
    [names.username]: server.username,
    [names.password]: server.password,
    [names.adminToken]: server.adminToken,
  };
}

/** The server global-setup.ts started, from the environment it set. */
export function serverFromEnv(): Server {
  const url = process.env[names.url];
  const username = process.env[names.username];
  const password = process.env[names.password];
  const adminToken = process.env[names.adminToken];
  if (!url || !username || !password || !adminToken) {
    throw new Error(
      "no Pantalan server: run the suite with `npx playwright test` (global-setup.ts)",
    );
  }
  return { url, username, password, adminToken };
}
