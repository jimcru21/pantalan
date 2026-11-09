// The smoke tests: Playwright against the built binary (`make build`), with
// its embedded frontend. See "Smoke tests" in docs/development.md.
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "e2e",
  // Starts the binary and stops it at the end.
  globalSetup: "./e2e/global-setup.ts",
  // One binary, one Admin: the tests sign in in turn.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // A flake here is a bug until shown otherwise.
  retries: 0,
  reporter: process.env.CI ? [["github"], ["list"]] : "list",
  use: {
    // Pantalan's Dashboard certificate is self-signed.
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
