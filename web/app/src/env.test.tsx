import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AppEnv, ErrorResponse } from "@/api/generated/httpapi";
import { anAppListItem, appPageApi, appsListApi } from "@/test/app-fixtures";
import { deploymentsServer } from "@/test/deployment-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import { json } from "@/test/network";
import { renderApp } from "@/test/render";

const app = anApp({
  id: "app7",
  name: "shop",
  env_names: ["DATABASE_URL", "STRIPE_KEY"],
});
const envText = "DATABASE_URL=postgres://db/shop\nSTRIPE_KEY=sk_live_secret\n";

/** GET and PUT of app7's env vars; PUT answers `put` (by default, it stores them). */
function envApi(initial = envText, put?: (body: AppEnv) => Response) {
  let stored = initial;
  const sent: AppEnv[] = [];
  return {
    sent,
    api: {
      "GET /api/v1/apps/app7/env": () => json<AppEnv>({ env: stored }),
      "PUT /api/v1/apps/app7/env": async (req: Request) => {
        const body = (await req.json()) as AppEnv;
        sent.push(body);
        if (put) return put(body);
        stored = body.env;
        return json<AppEnv>({ env: stored });
      },
    },
  };
}

describe("an App's env vars on its page", () => {
  it("shows their names but never their values, and links to the editor", async () => {
    const { user, calls } = renderApp("/apps/app7", {
      api: { ...appPageApi(app, [aDeployment()]), ...envApi().api },
    });

    const env = await screen.findByRole("region", { name: "Env vars" });
    expect(within(env).getByText("DATABASE_URL")).toBeInTheDocument();
    expect(within(env).getByText("STRIPE_KEY")).toBeInTheDocument();
    // The App page never asks for the values.
    expect(calls.map((c) => c.path)).not.toContain("/api/v1/apps/app7/env");

    await user.click(within(env).getByRole("link", { name: "Edit env vars" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Env vars of shop" }),
    ).toBeVisible();
    expect(window.location.pathname).toBe("/apps/app7/env");
  });

  it("says so when the App has none", async () => {
    renderApp("/apps/app7", { api: appPageApi({ ...app, env_names: [] }, []) });

    const env = await screen.findByRole("region", { name: "Env vars" });
    expect(env).toHaveTextContent("No env vars.");
  });

  it("are not on the Apps list, nor asked for by it", async () => {
    const { calls } = renderApp("/apps", { api: appsListApi([anAppListItem(app)]) });

    const row = await screen.findByRole("row", { name: /shop/ });
    expect(row).not.toHaveTextContent("DATABASE_URL");
    expect(calls.map((c) => c.path)).toEqual(["/api/v1/apps"]);
  });
});

describe("the env editor", () => {
  it("loads the App's env vars as text", async () => {
    const env = envApi();
    renderApp("/apps/app7/env", { api: { ...appPageApi(app, []), ...env.api } });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Env vars of shop" }),
    ).toBeVisible();
    expect(await screen.findByLabelText("Env vars")).toHaveValue(envText);
  });

  it("shows a refused line's number beside the text and keeps what was typed", async () => {
    const env = envApi(envText, () =>
      json<ErrorResponse>(
        { error: 'line 3: "oops" is not KEY=value', field: "env" },
        { status: 400 },
      ),
    );
    const { user } = renderApp("/apps/app7/env", { api: { ...appPageApi(app, []), ...env.api } });
    const editor = await screen.findByLabelText("Env vars");

    await user.type(editor, "oops");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText('line 3: "oops" is not KEY=value')).toBeVisible();
    expect(editor).toHaveAttribute("aria-invalid", "true");
    expect(editor).toHaveAccessibleDescription(/line 3: "oops" is not KEY=value/);
    expect(editor).toHaveFocus();
    expect(editor).toHaveValue(envText + "oops");
    expect(env.sent).toEqual([{ env: envText + "oops" }]);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("saves, says it takes effect on the next Deployment and offers to deploy", async () => {
    // The server keeps the pairs, not the comments.
    const env = envApi(envText, () => json<AppEnv>({ env: "GREETING=hi\n" }));
    const server = deploymentsServer(app, [aDeployment({ app_id: "app7" })]);
    // The server adds a handler per Deployment it records, so pass its own api.
    const { user, calls } = renderApp("/apps/app7/env", {
      api: Object.assign(server.api, env.api),
    });
    const editor = await screen.findByLabelText("Env vars");

    await user.clear(editor);
    await user.type(editor, "# the shop{Enter}GREETING=hi{Enter}");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const saved = await screen.findByRole("status");
    expect(saved).toHaveTextContent(/Saved.*take effect on the next Deployment/);
    expect(env.sent).toEqual([{ env: "# the shop\nGREETING=hi\n" }]);
    // The editor shows what the App now holds: the comment isn't kept.
    expect(editor).toHaveValue("GREETING=hi\n");
    // Saving never deploys.
    expect(calls.filter((c) => c.path.endsWith("/deployments") && c.method === "POST")).toEqual([]);

    await user.click(within(saved).getByRole("button", { name: "Deploy" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "Deployment dep-1" }),
    ).toBeVisible();
    expect(window.location.pathname).toBe("/apps/app7/deployments/dep-1");
    expect(calls.filter((c) => c.method === "POST").map((c) => c.path)).toEqual([
      "/api/v1/apps/app7/deployments",
    ]);
  });

  it("shows App not found for an unknown App", async () => {
    const notFound = () => json<ErrorResponse>({ error: "app nope: not found" }, { status: 404 });
    renderApp("/apps/nope/env", {
      api: { "GET /api/v1/apps/nope": notFound, "GET /api/v1/apps/nope/env": notFound },
    });

    expect(await screen.findByRole("heading", { name: "App not found" })).toBeVisible();
  });
});
