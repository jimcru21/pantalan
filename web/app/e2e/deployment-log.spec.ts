// A real Deployment through the binary: git, docker build, the container's
// health check and go-live on this machine's Docker, watched in the
// browser as its Deployment Log streams to done; then the live container's
// App Log, read through the Engine API. Needs `git` and `docker`
// (with its socket) where the binary runs; make smoke-docker hands the
// container both.
import { execFile } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import type { App, Deployment } from "../src/api/generated/domain";
import { expect, test } from "./fixtures";

const run = promisify(execFile);

// The smoke App: a web server that answers on its port, so it goes live,
// after a line on stdout and one on stderr for its App Log.
const dockerfile = `FROM busybox:latest
RUN echo "building the smoke App"
CMD ["sh", "-c", "echo 'the smoke App on stdout'; echo 'the smoke App on stderr' >&2; exec httpd -f -p 8080 -h /tmp"]
`;

/** A git repository with the smoke App's Dockerfile committed on main. */
async function smokeRepo(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "pantalan-smoke-repo-"));
  await writeFile(join(dir, "Dockerfile"), dockerfile);
  const git = (...args: string[]) => run("git", ["-C", dir, ...args]);
  await git("init", "--quiet", "--initial-branch=main");
  await git("add", "Dockerfile");
  await git(
    "-c",
    "user.name=Pantalan smoke",
    "-c",
    "user.email=smoke@pantalan.invalid",
    "commit",
    "--quiet",
    "--message",
    "the smoke App",
  );
  return dir;
}

test.describe("the Deployment Log", () => {
  let repo = "";
  let deployed: Deployment | undefined;

  test.beforeAll(async () => {
    const docker = await run("docker", ["version"]).then(
      () => true,
      () => false,
    );
    // CI's runner has Docker: there, a missing one is a failure.
    test.skip(!docker && !process.env.CI, "needs docker (try make smoke-docker)");
    repo = await smokeRepo();
  });

  // Leave nothing behind on this machine's Docker.
  test.afterAll(async () => {
    if (deployed) {
      const ids = await run("docker", [
        "ps",
        "--all",
        "--quiet",
        "--filter",
        `label=pantalan.app=${deployed.app_id}`,
      ]);
      const containers = ids.stdout.split("\n").filter(Boolean);
      if (containers.length) await run("docker", ["rm", "--force", ...containers]);
      // Every Deployment's image, if its build got that far.
      const images = await run("docker", [
        "images",
        "--quiet",
        "--filter",
        `reference=pantalan/${deployed.app_id}`,
      ]);
      const built = [...new Set(images.stdout.split("\n").filter(Boolean))];
      if (built.length) await run("docker", ["rmi", "--force", ...built]);
    }
    if (repo) await rm(repo, { recursive: true, force: true });
  });

  test("streams to done as a Deployment goes live", async ({ page, admin, api }) => {
    test.setTimeout(180_000); // a first build may pull busybox
    const created = await api.post("/api/v1/apps", {
      data: {
        name: "smoke",
        repo_url: repo,
        branch: "main",
        domain: `smoke-${randomBytes(4).toString("hex")}.test`,
        port: 8080,
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    const app = (await created.json()) as App;
    const queued = await api.post(`/api/v1/apps/${app.id}/deployments`);
    expect(queued.status(), await queued.text()).toBe(202);
    deployed = (await queued.json()) as Deployment;

    // A deep link, signed out: sign in, and land on it.
    await page.goto(`/apps/${app.id}/deployments/${deployed.id}`);
    await expect(page).toHaveURL(/\/login\?next=/);
    await page.getByLabel("Username").fill(admin.username);
    await page.getByLabel("Password").fill(admin.password);
    await page.getByRole("button", { name: "Sign in" }).click();

    const log = page.getByRole("log", { name: "Deployment Log" });
    await expect(log).toContainText(`==> fetching ${repo} at main`);
    await expect(log).toContainText("building the smoke App", { timeout: 150_000 });
    await expect(log).toContainText(`==> live at ${app.domain}`, { timeout: 150_000 });
    await expect(log).toContainText("End of the log.");
    await expect(page.getByRole("region", { name: "Deployment", exact: true })).toContainText(
      "Live",
    );

    // The plain-text log is the same output, whole.
    const whole = await page.request.get(`/api/v1/apps/${app.id}/deployments/${deployed.id}/log`);
    expect(whole.headers()["content-type"]).toBe("text/plain; charset=utf-8");
    expect(await whole.text()).toContain(`==> live at ${app.domain}\n`);
  });

  test("then the App's page follows its App Log, to the next Deployment", async ({
    page,
    admin,
  }) => {
    test.skip(deployed === undefined, "the Deployment didn't go live");
    test.setTimeout(120_000);
    await page.goto(`/apps/${deployed?.app_id}`);
    await page.getByLabel("Username").fill(admin.username);
    await page.getByLabel("Password").fill(admin.password);
    await page.getByRole("button", { name: "Sign in" }).click();

    const log = page.getByRole("log", { name: "App Log" });
    await expect(log).toContainText("the smoke App on stdout");
    await expect(log).toContainText("the smoke App on stderr");

    // A new Deployment replaces the container the log follows.
    await page.getByRole("button", { name: "Deploy" }).click();
    const region = page.getByRole("region", { name: "App Log" });
    await expect(region).toContainText("A new Deployment replaced the container", {
      timeout: 100_000,
    });
    await page.getByRole("button", { name: "Follow the new Deployment" }).click();
    await expect(log).toContainText("the smoke App on stdout");
  });
});
