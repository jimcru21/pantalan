// Smoke tests against the built binary, with its embedded frontend: the
// only tests where the embedding, the session cookie, the cross-origin
// check and the CSP work together. Every test fails on a CSP violation in
// the console (see fixtures.ts).
import { hostname } from "node:os";
import { expect, test } from "./fixtures";

test("signs in and sees the overview", async ({ page, admin }) => {
  await page.goto("/");
  await expect(page).toHaveURL("/login");

  await page.getByLabel("Username").fill(admin.username);
  await page.getByLabel("Password").fill(admin.password);
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page).toHaveURL("/");
  // The top bar shows the Node's hostname, read by the binary from the
  // same machine the browser runs on.
  await expect(page.getByRole("banner", { name: "Node" })).toContainText(hostname());
  await expect(page.getByRole("region", { name: "Node health" })).toBeVisible();
});

// The guard every test relies on: the binary's CSP refuses an inline
// <style> and the refusal reaches cspViolations, which fails a test.
test("a CSP violation in the console is caught", async ({ page, cspViolations }) => {
  await page.goto("/login");
  await page.evaluate(() => {
    const style = document.createElement("style");
    style.textContent = "body { color: red }";
    document.head.append(style);
  });

  await expect.poll(() => cspViolations).toEqual([expect.stringMatching(/style-src 'self'/)]);
  // Caught: clear it so this test itself passes.
  cspViolations.length = 0;
});
