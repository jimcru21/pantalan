import { act, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AppListItem } from "@/api/generated/httpapi";
import { anAppListItem, appPageApi, appsListApi, unknownAppApi } from "@/test/app-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import { json } from "@/test/network";
import { renderApp } from "@/test/render";
import { setHidden } from "@/test/visibility";

describe("the Apps list", () => {
  it("lists every App with its domain, branch and latest Deployment's status", async () => {
    renderApp("/apps", {
      api: appsListApi([
        anAppListItem({
          id: "a1",
          name: "idle",
          domain: "idle.example.com",
          latest_deployment: undefined,
        }),
        anAppListItem({
          id: "a2",
          name: "shop",
          domain: "shop.example.com",
          branch: "release",
          latest_deployment: aDeployment({ app_id: "a2", status: "live" }),
        }),
        anAppListItem({
          id: "a3",
          name: "blog",
          domain: "blog.example.com",
          latest_deployment: aDeployment({ app_id: "a3", status: "failed", error: "build failed" }),
        }),
      ]),
    });

    const table = await screen.findByRole("table", { name: "Apps" });
    const shop = within(table).getByRole("row", { name: /shop/ });
    expect(within(shop).getByRole("link", { name: "shop" })).toHaveAttribute("href", "/apps/a2");
    expect(shop).toHaveTextContent("shop.example.com");
    expect(shop).toHaveTextContent("release");
    expect(shop).toHaveTextContent("Live");
    expect(within(table).getByRole("row", { name: /idle/ })).toHaveTextContent("Not deployed");
    expect(within(table).getByRole("row", { name: /blog/ })).toHaveTextContent("Failed");
  });

  it("says so when there are no Apps", async () => {
    renderApp("/apps", { api: appsListApi([]) });

    expect(await screen.findByText("No Apps yet.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /example/ })).not.toBeInTheDocument();
  });

  it("updates the status on its own every 5 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let status = "building";
    renderApp("/apps", {
      api: {
        "GET /api/v1/apps": () =>
          json<AppListItem[]>([anAppListItem({ latest_deployment: aDeployment({ status }) })]),
      },
    });
    expect(await screen.findByRole("row", { name: /hello/ })).toHaveTextContent("Building");

    status = "live";
    await act(() => vi.advanceTimersByTimeAsync(4_000));
    expect(screen.getByRole("row", { name: /hello/ })).toHaveTextContent("Building");
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(screen.getByRole("row", { name: /hello/ })).toHaveTextContent("Live");
  });

  it("pauses while the browser tab is hidden and catches up on return", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { calls } = renderApp("/apps", { api: appsListApi([anAppListItem()]) });
    const fetched = () => calls.filter((c) => c.path === "/api/v1/apps").length;
    await screen.findByRole("row", { name: /hello/ });

    await act(async () => setHidden(true));
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(fetched()).toBe(1);

    await act(async () => setHidden(false));
    await vi.waitFor(() => expect(fetched()).toBe(2));
  });
});

describe("an App's page", () => {
  const app = anApp({
    id: "app7",
    name: "shop",
    repo_url: "https://github.com/example/shop.git",
    branch: "release",
    build_type: "nixpacks",
    domain: "shop.example.com",
    port: 3000,
  });
  const live = aDeployment({
    id: "dep1",
    app_id: "app7",
    ref: "v1",
    commit: "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    image: "pantalan/shop:1111111",
    status: "live",
  });
  const failed = aDeployment({
    id: "dep2",
    app_id: "app7",
    ref: "v2",
    commit: "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    status: "failed",
    error: "build failed: exit status 1",
  });
  const queued = aDeployment({
    id: "dep3",
    app_id: "app7",
    ref: "v3",
    commit: "",
    image: "",
    status: "queued",
    finished_at: undefined,
  });

  it("shows its settings, its live Deployment and its Deployment history", async () => {
    renderApp("/apps/app7", { api: appPageApi(app, [queued, failed, live]) });

    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
    const settings = screen.getByRole("region", { name: "Settings" });
    for (const value of [
      "https://github.com/example/shop.git",
      "release",
      "nixpacks",
      "shop.example.com",
      "3000",
    ]) {
      expect(settings).toHaveTextContent(value);
    }

    const current = screen.getByRole("region", { name: "Live Deployment" });
    expect(current).toHaveTextContent("1111111");
    expect(current).toHaveTextContent("v1");
    expect(current).toHaveTextContent("pantalan/shop:1111111");

    const history = within(screen.getByRole("region", { name: "Deployments" })).getAllByRole("row");
    // The header, then newest first.
    expect(history).toHaveLength(4);
    expect(history[1]).toHaveTextContent("Queued");
    expect(history[1]).toHaveTextContent("v3");
    expect(history[2]).toHaveTextContent("Failed");
    expect(history[2]).toHaveTextContent("2222222");
    expect(history[2]).toHaveTextContent("build failed: exit status 1");
    expect(history[3]).toHaveTextContent("Live");
    expect(history[3]).toHaveTextContent("1111111");
  });

  it("says when nothing is live and it has never been deployed", async () => {
    renderApp("/apps/app7", { api: appPageApi(app, []) });

    const current = await screen.findByRole("region", { name: "Live Deployment" });
    expect(current).toHaveTextContent("No live Deployment");
    expect(screen.getByRole("region", { name: "Deployments" })).toHaveTextContent(
      "Not deployed yet.",
    );
  });

  it("is reached from the list and left with back and forward", async () => {
    const { user } = renderApp("/apps", {
      api: { ...appsListApi([anAppListItem(app)]), ...appPageApi(app, [live]) },
    });

    await user.click(await screen.findByRole("link", { name: "shop" }));
    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app7");
    // Still under Apps in the sidebar.
    expect(screen.getByRole("link", { name: "Apps" })).toHaveAttribute("aria-current", "page");

    window.history.back();
    expect(await screen.findByRole("table", { name: "Apps" })).toBeInTheDocument();
    window.history.forward();
    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
  });

  it("shows a not-found page for an unknown App ID", async () => {
    renderApp("/apps/nosuch", { api: unknownAppApi("nosuch") });

    expect(await screen.findByRole("heading", { name: "App not found" })).toBeInTheDocument();
    expect(screen.getByText(/No App has the ID/)).toHaveTextContent("nosuch");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to Apps" })).toHaveAttribute("href", "/apps");
  });
});
