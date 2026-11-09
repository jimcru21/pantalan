import { act, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { deploymentsServer, unknownDeploymentApi } from "@/test/deployment-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import { json } from "@/test/network";
import { renderApp } from "@/test/render";

const app = anApp({ id: "app7", name: "shop", branch: "release", domain: "shop.example.com" });
const live = aDeployment({
  id: "dep-live",
  app_id: "app7",
  ref: "v1",
  commit: "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  image: "pantalan/app7:dep-live",
  status: "live",
});

// The rows of a region's table, header first.
function rows(region: string) {
  return within(screen.getByRole("region", { name: region })).getAllByRole("row");
}

describe("the Deploy button", () => {
  it("deploys the branch's latest commit and shows the queued Deployment", async () => {
    const server = deploymentsServer(app, [live]);
    const { user, calls } = renderApp("/apps/app7", { api: server.api });

    await user.click(await screen.findByRole("button", { name: "Deploy" }));

    const [deploy, ...more] = calls.filter((c) => c.method === "POST");
    expect(more).toHaveLength(0);
    expect(deploy?.path).toBe("/api/v1/apps/app7/deployments");
    await expect(deploy?.request.text()).resolves.toBe("");
    // Newest first, under the header.
    await within(screen.getByRole("region", { name: "Deployments" })).findByText("Queued");
    expect(rows("Deployments")[1]).toHaveTextContent("release");
    expect(rows("Deployments")[2]).toHaveTextContent("Live");
  });

  it("queues two Deployments for two quick clicks, shown in order", async () => {
    const server = deploymentsServer(app, [live]);
    const { user, calls } = renderApp("/apps/app7", { api: server.api });
    const deploy = await screen.findByRole("button", { name: "Deploy" });

    // The first click's answer arrives after the second's.
    const releaseFirst = server.holdNextDeploy();
    await user.click(deploy);
    await user.click(deploy);
    await within(screen.getByRole("region", { name: "Queue" })).findByText("dep-2");
    releaseFirst();

    expect(calls.filter((c) => c.method === "POST")).toHaveLength(2);
    // The queue runs oldest first: dep-1, then dep-2.
    await within(screen.getByRole("region", { name: "Queue" })).findByText("dep-1");
    const queue = rows("Queue");
    expect(queue).toHaveLength(3);
    expect(queue[1]).toHaveTextContent(/1.*dep-1.*Queued/);
    expect(queue[2]).toHaveTextContent(/2.*dep-2.*Queued/);
    // The history stays newest first.
    const history = rows("Deployments");
    expect(history).toHaveLength(4);
    expect(history[1]).toHaveTextContent("dep-2");
    expect(history[2]).toHaveTextContent("dep-1");
    expect(history[3]).toHaveTextContent("Live");
  });

  it("moves the queued Deployment's status on screen every 2 s until it is final", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const server = deploymentsServer(app, [live]);
    const { user, calls } = renderApp("/apps/app7", { api: server.api });
    const listed = () => calls.filter((c) => c.path === "/api/v1/apps/app7/deployments").length;

    await user.click(await screen.findByRole("button", { name: "Deploy" }));
    await within(screen.getByRole("region", { name: "Queue" })).findByText("dep-1");

    server.set("dep-1", { status: "building", commit: "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(rows("Queue")[1]).toHaveTextContent("Building");

    server.set("dep-1", { status: "live", finished_at: "2026-11-09T12:01:00Z" });
    server.set("dep-live", { status: "superseded" });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(screen.getByRole("region", { name: "Queue" })).toHaveTextContent("Nothing queued.");
    expect(rows("Deployments")[1]).toHaveTextContent(/dep-1.*Live|Live.*dep-1/);
    expect(screen.getByRole("region", { name: "Live Deployment" })).toHaveTextContent("2222222");

    // Every Deployment is final: no more polling.
    const settled = listed();
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(listed()).toBe(settled);
  });

  it("says why when the server refuses the deploy", async () => {
    const { user } = renderApp("/apps/app7", {
      api: {
        ...deploymentsServer(app, [live]).api,
        "POST /api/v1/apps/app7/deployments": () =>
          json({ error: "deploy queue is full" }, { status: 503 }),
      },
    });

    await user.click(await screen.findByRole("button", { name: "Deploy" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Deploy failed: deploy queue is full",
    );
  });

  it("says nothing is queued when every Deployment has finished", async () => {
    renderApp("/apps/app7", { api: deploymentsServer(app, [live]).api });

    expect(await screen.findByRole("region", { name: "Queue" })).toHaveTextContent(
      "Nothing queued.",
    );
  });
});

describe("a Deployment's page", () => {
  const failed = aDeployment({
    id: "dep-bad",
    app_id: "app7",
    ref: "v2",
    commit: "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    image: "pantalan/app7:dep-bad",
    status: "failed",
    error: "build: exit status 1",
    created_at: "2026-11-09T12:00:00Z",
    finished_at: "2026-11-09T12:01:30Z",
  });

  it("works from the address bar and shows its ref, commit, image, timing, status and error", async () => {
    renderApp("/apps/app7/deployments/dep-bad", { api: deploymentsServer(app, [failed]).api });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Deployment dep-bad" }),
    ).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "shop" })).toHaveAttribute("href", "/apps/app7");
    const facts = screen.getByRole("region", { name: "Deployment" });
    for (const value of [
      "Failed",
      "v2",
      "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "pantalan/app7:dep-bad",
      "Nov 9, 2026, 12:00:00 UTC",
      "Nov 9, 2026, 12:01:30 UTC",
      "1m 30s",
      "build: exit status 1",
    ]) {
      expect(facts).toHaveTextContent(value);
    }
  });

  it("moves its status from queued to live every 2 s and stops polling there", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const queued = aDeployment({
      id: "dep-new",
      app_id: "app7",
      ref: "release",
      commit: "",
      image: "",
      status: "queued",
      finished_at: undefined,
    });
    const server = deploymentsServer(app, [live, queued]);
    const { calls } = renderApp("/apps/app7/deployments/dep-new", { api: server.api });
    const read = () =>
      calls.filter((c) => c.path === "/api/v1/apps/app7/deployments/dep-new").length;
    const status = () => screen.getByRole("region", { name: "Deployment" });
    await screen.findByRole("region", { name: "Deployment" });
    expect(status()).toHaveTextContent("Queued");

    server.set("dep-new", {
      status: "building",
      commit: "3333333ccccccccccccccccccccccccccccccccc",
    });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(status()).toHaveTextContent("Building");
    expect(status()).toHaveTextContent("3333333ccccccccccccccccccccccccccccccccc");

    server.set("dep-new", { status: "starting", image: "pantalan/app7:dep-new" });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(status()).toHaveTextContent("Starting");

    server.set("dep-new", { status: "live", finished_at: "2026-11-01T09:01:04Z" });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(status()).toHaveTextContent("Live");
    expect(status()).toHaveTextContent("Took4s");

    const settled = read();
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(read()).toBe(settled);
  });

  it("stops polling at failed and says the live Deployment is still serving", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const building = {
      ...failed,
      status: "building" as const,
      error: undefined,
      finished_at: undefined,
    };
    const server = deploymentsServer(app, [live, building]);
    const { calls } = renderApp("/apps/app7/deployments/dep-bad", { api: server.api });
    const read = () =>
      calls.filter((c) => c.path === "/api/v1/apps/app7/deployments/dep-bad").length;
    const page = await screen.findByRole("region", { name: "Deployment" });
    expect(page).toHaveTextContent("Building");
    expect(page).not.toHaveTextContent("still live");

    server.set("dep-bad", {
      status: "failed",
      error: "health check: no answer on port 8080",
      finished_at: "2026-11-09T12:01:30Z",
    });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(page).toHaveTextContent("Failed");
    expect(page).toHaveTextContent("health check: no answer on port 8080");
    expect(
      await within(page).findByText(/still live and serving shop\.example\.com/),
    ).toHaveTextContent("Deployment dep-live (v1 at 1111111)");
    expect(within(page).getByRole("link", { name: "dep-live" })).toHaveAttribute(
      "href",
      "/apps/app7/deployments/dep-live",
    );

    const settled = read();
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(read()).toBe(settled);
  });

  it("doesn't claim anything is serving when the App has no live Deployment", async () => {
    renderApp("/apps/app7/deployments/dep-bad", { api: deploymentsServer(app, [failed]).api });

    const page = await screen.findByRole("region", { name: "Deployment" });
    await screen.findByRole("link", { name: "shop" });
    expect(page).toHaveTextContent("Failed");
    expect(page).not.toHaveTextContent(/still|serving/);
  });

  it("is reached from the App's Deployment history", async () => {
    const { user } = renderApp("/apps/app7", { api: deploymentsServer(app, [live, failed]).api });

    const history = await screen.findByRole("region", { name: "Deployments" });
    await user.click(within(history).getByRole("link", { name: "dep-bad" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "Deployment dep-bad" }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app7/deployments/dep-bad");
    window.history.back();
    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
  });

  it("shows a not-found page for a Deployment the App doesn't have", async () => {
    renderApp("/apps/app7/deployments/nosuch", {
      api: { ...deploymentsServer(app, [live]).api, ...unknownDeploymentApi("app7", "nosuch") },
    });

    expect(
      await screen.findByRole("heading", { name: "Deployment not found" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/No Deployment of this App/)).toHaveTextContent("nosuch");
    expect(screen.getByRole("link", { name: "Back to the App" })).toHaveAttribute(
      "href",
      "/apps/app7",
    );
  });
});
