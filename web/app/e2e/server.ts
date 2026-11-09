// The Pantalan binary under test, as global-setup.ts hands it to the tests:
// through environment variables, which the test workers inherit.

/** What the tests need to reach the running binary. */
export type Server = { url: string; username: string; password: string };

const names = {
  url: "PANTALAN_SMOKE_URL",
  username: "PANTALAN_SMOKE_USERNAME",
  password: "PANTALAN_SMOKE_PASSWORD",
} as const;

/** The environment variables that describe server. */
export function serverEnv(server: Server): Record<string, string> {
  return {
    [names.url]: server.url,
    [names.username]: server.username,
    [names.password]: server.password,
  };
}

/** The server global-setup.ts started, from the environment it set. */
export function serverFromEnv(): Server {
  const url = process.env[names.url];
  const username = process.env[names.username];
  const password = process.env[names.password];
  if (!url || !username || !password) {
    throw new Error(
      "no Pantalan server: run the suite with `npx playwright test` (global-setup.ts)",
    );
  }
  return { url, username, password };
}
