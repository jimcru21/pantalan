import { act, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { deploymentsServer } from "@/test/deployment-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import { json } from "@/test/network";
import { renderApp } from "@/test/render";

const app = anApp({ id: "app7", name: "shop", branch: "release", domain: "shop.example.com" });
const good = aDeployment({
  id: "dep-good",
  app_id: "app7",
  ref: "release",
  commit: "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  image: "pantalan/app7:dep-good",
  status: "superseded",
  created_at: "2026-11-08T10:00:00Z",
  finished_at: "2026-11-08T10:01:00Z",
});
const bad = aDeployment({
  id: "dep-bad",
  app_id: "app7",
  ref: "release",
  commit: "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  image: "pantalan/app7:dep-bad",
  status: "live",
  created_at: "2026-11-09T09:00:00Z",
  finished_at: "2026-11-09T09:01:00Z",
});

const redeployButton = () => screen.findByRole("button", { name: "Redeploy this commit" });

describe("Redeploy this commit", () => {
  it("starts a new Deployment of the earlier commit and lands on its page", async () => {
    const server = deploymentsServer(app, [good, bad]);
    const { user, calls } = renderApp("/apps/app7/deployments/dep-good", { api: server.api });

    await user.click(await redeployButton());

    const posts = calls.filter((c) => c.method === "POST");
    expect(posts.map((c) => c.path)).toEqual(["/api/v1/apps/app7/deployments/dep-good/redeploy"]);
    await expect(posts[0]?.request.text()).resolves.toBe("");
    expect(
      await screen.findByRole("heading", { level: 1, name: "Deployment dep-1" }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app7/deployments/dep-1");
    const facts = await screen.findByRole("region", { name: "Deployment" });
    expect(facts).toHaveTextContent("StatusQueued");
    expect(facts).toHaveTextContent("Ref1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");

    // The earlier Deployment is still in the history, as it was.
    window.history.back();
    expect(
      await screen.findByRole("heading", { level: 1, name: "Deployment dep-good" }),
    ).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Deployment" })).toHaveTextContent(
      "Superseded",
    );
  });

  it("puts the redeploy in the App's history, newest first, beside the Deployment it rolls back", async () => {
    const server = deploymentsServer(app, [good, bad]);
    const { user } = renderApp("/apps/app7/deployments/dep-good", { api: server.api });

    await user.click(await redeployButton());
    await screen.findByRole("heading", { level: 1, name: "Deployment dep-1" });
    await user.click(screen.getByRole("link", { name: "shop" }));

    const history = await screen.findByRole("region", { name: "Deployments" });
    await within(history).findByRole("link", { name: "dep-1" });
    const rows = within(history).getAllByRole("row");
    expect(rows).toHaveLength(4);
    expect(rows[1]).toHaveTextContent(/dep-1.*Queued|Queued.*dep-1/);
    expect(rows[2]).toHaveTextContent(/dep-bad.*Live|Live.*dep-bad/);
    expect(rows[3]).toHaveTextContent(/dep-good.*Superseded|Superseded.*dep-good/);
  });

  it("watches the new Deployment go live on its page", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const server = deploymentsServer(app, [good, bad]);
    const { user } = renderApp("/apps/app7/deployments/dep-good", { api: server.api });

    await user.click(await redeployButton());
    await screen.findByRole("heading", { level: 1, name: "Deployment dep-1" });
    expect(await screen.findByRole("region", { name: "Deployment" })).toHaveTextContent(
      "StatusQueued",
    );

    server.set("dep-1", {
      status: "live",
      commit: good.commit,
      image: "pantalan/app7:dep-1",
      finished_at: "2026-11-09T12:01:01Z",
    });
    server.set("dep-bad", { status: "superseded" });
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(screen.getByRole("region", { name: "Deployment" })).toHaveTextContent("Live");
  });

  it("is offered on the live Deployment and on a failed one that resolved its commit", async () => {
    const failed = aDeployment({ ...bad, id: "dep-failed", status: "failed", error: "build" });
    const server = deploymentsServer(app, [good, bad, failed]);
    const { reload } = renderApp("/apps/app7/deployments/dep-bad", { api: server.api });
    expect(await redeployButton()).toBeInTheDocument();

    window.history.replaceState(null, "", "/apps/app7/deployments/dep-failed");
    reload();
    expect(await redeployButton()).toBeInTheDocument();
  });

  it("isn't offered for a Deployment that never resolved a commit", async () => {
    const unfetched = aDeployment({
      ...bad,
      id: "dep-nofetch",
      ref: "no-such-branch",
      commit: "",
      image: "",
      status: "failed",
      error: "fetch source: couldn't find remote ref no-such-branch",
    });
    renderApp("/apps/app7/deployments/dep-nofetch", {
      api: deploymentsServer(app, [good, unfetched]).api,
    });

    const facts = await screen.findByRole("region", { name: "Deployment" });
    await within(facts).findByText(/couldn't find remote ref/);
    expect(screen.queryByRole("button", { name: "Redeploy this commit" })).not.toBeInTheDocument();
  });

  it("says why when the server refuses, and stays on the earlier Deployment", async () => {
    const { user } = renderApp("/apps/app7/deployments/dep-good", {
      api: {
        ...deploymentsServer(app, [good, bad]).api,
        "POST /api/v1/apps/app7/deployments/dep-good/redeploy": () =>
          json({ error: "deploy queue is full" }, { status: 503 }),
      },
    });

    await user.click(await redeployButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Redeploy failed: deploy queue is full",
    );
    expect(window.location.pathname).toBe("/apps/app7/deployments/dep-good");
  });
});
