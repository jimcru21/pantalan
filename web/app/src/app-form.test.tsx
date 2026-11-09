import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { App, AppSettings, CreateAppRequest, ErrorResponse } from "@/api/generated/httpapi";
import { anAppListItem, appPageApi, appsListApi, unknownAppApi } from "@/test/app-fixtures";
import { anApp } from "@/test/fixtures";
import { json } from "@/test/network";
import { renderApp } from "@/test/render";

const created = anApp({
  id: "app9",
  name: "shop",
  repo_url: "https://github.com/example/shop.git",
  branch: "release",
  build_type: "nixpacks",
  domain: "shop.example.com",
  port: 3000,
});

// Fills the create form with created's settings.
async function fillIn(user: ReturnType<typeof renderApp>["user"]) {
  await user.type(await screen.findByLabelText("Name"), "shop");
  await user.type(screen.getByLabelText("Repository"), "https://github.com/example/shop.git");
  await user.clear(screen.getByLabelText("Branch"));
  await user.type(screen.getByLabelText("Branch"), "release");
  await user.selectOptions(screen.getByLabelText("Build type"), "Nixpacks");
  await user.type(screen.getByLabelText("Domain"), "shop.example.com");
  await user.type(screen.getByLabelText("Port"), "3000");
}

function expectTyped() {
  expect(screen.getByLabelText("Name")).toHaveValue("shop");
  expect(screen.getByLabelText("Repository")).toHaveValue("https://github.com/example/shop.git");
  expect(screen.getByLabelText("Branch")).toHaveValue("release");
  expect(screen.getByLabelText("Build type")).toHaveValue("nixpacks");
  expect(screen.getByLabelText("Domain")).toHaveValue("shop.example.com");
  expect(screen.getByLabelText("Port")).toHaveValue("3000");
}

const fieldError = (status: number, field: string, error: string) => () =>
  json<ErrorResponse>({ error, field }, { status });

describe("creating an App", () => {
  it("is reached from the Apps list", async () => {
    const { user } = renderApp("/apps", { api: appsListApi([anAppListItem()]) });

    await user.click(await screen.findByRole("link", { name: "New App" }));

    expect(await screen.findByRole("heading", { level: 1, name: "New App" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/new");
  });

  it("starts on branch main and a Dockerfile build", async () => {
    renderApp("/apps/new");

    expect(await screen.findByLabelText("Branch")).toHaveValue("main");
    expect(screen.getByLabelText("Build type")).toHaveValue("dockerfile");
  });

  it("creates the App from the form and lands on its page", async () => {
    const { user, calls } = renderApp("/apps/new", {
      api: {
        "POST /api/v1/apps": () => json<App>(created, { status: 201 }),
        ...appPageApi(created, []),
      },
    });

    await fillIn(user);
    await user.click(screen.getByRole("button", { name: "Create App" }));

    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app9");
    const post = calls.find((c) => c.method === "POST");
    expect(await post?.request.json()).toEqual({
      name: "shop",
      repo_url: "https://github.com/example/shop.git",
      branch: "release",
      build_type: "nixpacks",
      domain: "shop.example.com",
      port: 3000,
    } satisfies CreateAppRequest);
  });

  it("shows a validation error beside its field and keeps what was typed", async () => {
    let answer = fieldError(409, "domain", "domain shop.example.com is used by app blog");
    const { user } = renderApp("/apps/new", {
      api: { "POST /api/v1/apps": () => answer(), ...appPageApi(created, []) },
    });

    await fillIn(user);
    await user.click(screen.getByRole("button", { name: "Create App" }));

    const domain = screen.getByLabelText("Domain");
    expect(await screen.findByText("domain shop.example.com is used by app blog")).toBeVisible();
    expect(domain).toHaveAttribute("aria-invalid", "true");
    expect(domain).toHaveAccessibleDescription(/domain shop\.example\.com is used by app blog/);
    expect(domain).toHaveFocus();
    expect(screen.getByLabelText("Name")).not.toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Port")).not.toHaveAccessibleDescription(/used by app/);
    expectTyped();
    expect(window.location.pathname).toBe("/apps/new");

    answer = () => json<App>({ ...created, domain: "shop2.example.com" }, { status: 201 });
    await user.clear(domain);
    await user.type(domain, "shop2.example.com");
    await user.click(screen.getByRole("button", { name: "Create App" }));
    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
  });

  it("shows an error that names no field above the form, keeping what was typed", async () => {
    const { user } = renderApp("/apps/new", {
      api: {
        "POST /api/v1/apps": () =>
          json<ErrorResponse>({ error: "internal error" }, { status: 500 }),
      },
    });

    await fillIn(user);
    await user.click(screen.getByRole("button", { name: "Create App" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("internal error");
    expectTyped();
  });

  it("goes back to the Apps list on Cancel", async () => {
    const { user } = renderApp("/apps/new", { api: appsListApi([]) });

    await user.click(await screen.findByRole("link", { name: "Cancel" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Apps" })).toBeInTheDocument();
  });
});

describe("editing an App", () => {
  const app = anApp({
    id: "app7",
    name: "shop",
    repo_url: "https://github.com/example/shop.git",
    branch: "main",
    build_type: "dockerfile",
    domain: "shop.example.com",
    port: 3000,
    env_names: ["DATABASE_URL"],
  });

  it("is reached from the App's page", async () => {
    const { user } = renderApp("/apps/app7", { api: appPageApi(app, []) });

    const settings = await screen.findByRole("region", { name: "Settings" });
    await user.click(within(settings).getByRole("link", { name: "Edit" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Edit shop" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app7/edit");
  });

  it("starts from the App's settings, saves the changes and lands on its page", async () => {
    let current = app;
    let sent: unknown;
    const { user, calls } = renderApp("/apps/app7/edit", {
      api: {
        ...appPageApi(app, []),
        "GET /api/v1/apps/app7": () => json<App>(current),
        "PUT /api/v1/apps/app7": async (req) => {
          sent = await req.json();
          current = { ...app, ...(sent as AppSettings) } as App;
          return json<App>(current);
        },
      },
    });

    expect(await screen.findByLabelText("Name")).toHaveValue("shop");
    expect(screen.getByLabelText("Repository")).toHaveValue("https://github.com/example/shop.git");
    expect(screen.getByLabelText("Branch")).toHaveValue("main");
    expect(screen.getByLabelText("Build type")).toHaveValue("dockerfile");
    expect(screen.getByLabelText("Domain")).toHaveValue("shop.example.com");
    expect(screen.getByLabelText("Port")).toHaveValue("3000");
    expect(screen.getByText(/take effect on the next Deployment/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Branch"));
    await user.type(screen.getByLabelText("Branch"), "release");
    await user.clear(screen.getByLabelText("Port"));
    await user.type(screen.getByLabelText("Port"), "8080");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app7");
    const settings = screen.getByRole("region", { name: "Settings" });
    expect(settings).toHaveTextContent("release");
    expect(settings).toHaveTextContent("8080");
    // The settings only: never env vars, the ID or the creation time.
    expect(sent).toEqual({
      name: "shop",
      repo_url: "https://github.com/example/shop.git",
      branch: "release",
      build_type: "dockerfile",
      domain: "shop.example.com",
      port: 8080,
    } satisfies AppSettings);
    // Editing never starts a Deployment.
    expect(calls.filter((c) => c.method === "POST")).toEqual([]);
  });

  it("shows a validation error beside its field and keeps what was typed", async () => {
    const { user } = renderApp("/apps/app7/edit", {
      api: {
        ...appPageApi(app, []),
        "PUT /api/v1/apps/app7": fieldError(400, "port", "port must be between 1 and 65535"),
      },
    });

    const port = await screen.findByLabelText("Port");
    await user.clear(port);
    await user.type(port, "70000");
    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "store");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("port must be between 1 and 65535")).toBeVisible();
    expect(port).toHaveAttribute("aria-invalid", "true");
    expect(port).toHaveAccessibleDescription(/port must be between 1 and 65535/);
    expect(port).toHaveValue("70000");
    expect(screen.getByLabelText("Name")).toHaveValue("store");
    expect(window.location.pathname).toBe("/apps/app7/edit");
  });

  it("shows a not-found page for an unknown App ID", async () => {
    renderApp("/apps/nosuch/edit", { api: unknownAppApi("nosuch") });

    expect(await screen.findByRole("heading", { name: "App not found" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("goes back to the App's page on Cancel", async () => {
    const { user } = renderApp("/apps/app7/edit", { api: appPageApi(app, []) });

    await user.click(await screen.findByRole("link", { name: "Cancel" }));

    expect(await screen.findByRole("heading", { level: 1, name: "shop" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps/app7");
  });
});
