// The smoke tests' `test`: its baseURL is the binary global-setup.ts
// started, `admin` is the Admin it created, and every test fails when the
// browser's console reports a Content Security Policy violation.
import { test as base, expect, request, type APIRequestContext } from "@playwright/test";
import { serverFromEnv } from "./server";

type Fixtures = {
  /** The Admin the binary was set up with. */
  admin: { username: string; password: string };
  /** The API as automation reaches it: with the Admin Token, outside the browser. */
  api: APIRequestContext;
  /**
   * The CSP violations the console has reported so far, on any page of the
   * test's browser context. The test fails if any are left at its end.
   */
  cspViolations: string[];
};

export const test = base.extend<Fixtures>({
  // eslint-disable-next-line no-empty-pattern -- Playwright reads the fixtures a function needs from its first parameter's destructuring.
  baseURL: async ({}, use) => {
    await use(serverFromEnv().url);
  },

  // eslint-disable-next-line no-empty-pattern
  admin: async ({}, use) => {
    const { username, password } = serverFromEnv();
    await use({ username, password });
  },

  // eslint-disable-next-line no-empty-pattern
  api: async ({}, use) => {
    const { url, adminToken } = serverFromEnv();
    const api = await request.newContext({
      baseURL: url,
      ignoreHTTPSErrors: true,
      extraHTTPHeaders: { Authorization: `Bearer ${adminToken}` },
    });
    await use(api);
    await api.dispose();
  },

  cspViolations: [
    async ({ context }, use) => {
      const violations: string[] = [];
      context.on("console", (message) => {
        if (/Content Security Policy/i.test(message.text())) violations.push(message.text());
      });
      await use(violations);
      expect(violations, "CSP violations logged to the console").toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
