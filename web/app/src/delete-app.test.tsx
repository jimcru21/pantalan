import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AppListItem, ErrorResponse } from "@/api/generated/httpapi";
import { anAppListItem, appPageApi } from "@/test/app-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import { json, status, type Api } from "@/test/network";
import { renderApp } from "@/test/render";

const app = anApp({ id: "app7", name: "shop", domain: "shop.example.com" });
const other = anAppListItem({ id: "app8", name: "blog", domain: "blog.example.com" });

// The App page of app, and a DELETE that succeeds: afterwards the Apps
// list no longer has it.
function deletableApi(): Api {
  let deleted = false;
  return {
    ...appPageApi(app, [aDeployment({ app_id: app.id })]),
    [`DELETE /api/v1/apps/${app.id}`]: () => {
      deleted = true;
      return status(204);
    },
    "GET /api/v1/apps": () => json<AppListItem[]>(deleted ? [other] : [anAppListItem(app), other]),
  };
}

async function openDialog(user: ReturnType<typeof renderApp>["user"]) {
  await user.click(await screen.findByRole("button", { name: "Delete" }));
  return screen.getByRole("dialog", { name: "Delete shop?" });
}

describe("deleting an App", () => {
  it("opens a dialog from the App page that says what will be removed", async () => {
    const { user } = renderApp(`/apps/${app.id}`, { api: deletableApi() });

    const dialog = await openDialog(user);

    expect(dialog).toHaveAccessibleDescription(/container/);
    expect(dialog).toHaveAccessibleDescription(/images/);
    expect(dialog).toHaveAccessibleDescription(/route/);
    expect(dialog).toHaveAccessibleDescription(/shop\.example\.com/);
    expect(dialog).toHaveAccessibleDescription(/Deployment Logs/);
    expect(dialog).toHaveAccessibleDescription(/Deployments/);
    expect(dialog).toHaveAccessibleDescription(/can't be undone/);
  });

  it("keeps the delete button disabled until the domain is typed exactly", async () => {
    const { user, calls } = renderApp(`/apps/${app.id}`, { api: deletableApi() });
    const dialog = await openDialog(user);
    const confirm = within(dialog).getByLabelText("Type shop.example.com to confirm");
    const button = within(dialog).getByRole("button", { name: "Delete App" });

    expect(button).toBeDisabled();
    await user.type(confirm, "shop.example");
    expect(button).toBeDisabled();
    await user.type(confirm, ".co");
    expect(button).toBeDisabled();
    await user.clear(confirm);
    await user.type(confirm, "SHOP.example.com");
    expect(button).toBeDisabled();
    await user.clear(confirm);
    await user.type(confirm, "shop.example.com ");
    expect(button).toBeDisabled();
    await user.clear(confirm);
    await user.type(confirm, "blog.example.com");
    expect(button).toBeDisabled();

    // Enter in the field doesn't delete either while it doesn't match.
    await user.type(confirm, "{Enter}");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    await user.clear(confirm);
    await user.type(confirm, "shop.example.com");
    expect(button).toBeEnabled();
  });

  it("deletes the App and returns to the Apps list, which no longer has it", async () => {
    const { user, calls } = renderApp(`/apps/${app.id}`, { api: deletableApi() });
    const dialog = await openDialog(user);

    await user.type(within(dialog).getByLabelText(/to confirm/), "shop.example.com");
    await user.click(within(dialog).getByRole("button", { name: "Delete App" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Apps" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps");
    expect(calls.filter((c) => c.method === "DELETE").map((c) => c.path)).toEqual([
      `/api/v1/apps/${app.id}`,
    ]);
    const table = await screen.findByRole("table", { name: "Apps" });
    expect(await within(table).findByRole("row", { name: /blog/ })).toBeInTheDocument();
    expect(within(table).queryByRole("row", { name: /shop/ })).not.toBeInTheDocument();
  });

  it("shows a refusal in the dialog and keeps it open with what was typed", async () => {
    const { user } = renderApp(`/apps/${app.id}`, {
      api: {
        ...deletableApi(),
        [`DELETE /api/v1/apps/${app.id}`]: () =>
          json<ErrorResponse>(
            {
              error:
                "conflict: Deployment dep2 of shop is building; delete the App once it has finished",
            },
            { status: 409 },
          ),
      },
    });
    const dialog = await openDialog(user);
    const confirm = within(dialog).getByLabelText(/to confirm/);

    await user.type(confirm, "shop.example.com");
    await user.click(within(dialog).getByRole("button", { name: "Delete App" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Deployment dep2 of shop is building",
    );
    expect(screen.getByRole("dialog", { name: "Delete shop?" })).toBeInTheDocument();
    expect(confirm).toHaveValue("shop.example.com");
    expect(window.location.pathname).toBe(`/apps/${app.id}`);
  });

  describe("the dialog with a keyboard", () => {
    it("moves focus to the confirmation field when it opens", async () => {
      const { user } = renderApp(`/apps/${app.id}`, { api: deletableApi() });

      const dialog = await openDialog(user);

      expect(within(dialog).getByLabelText(/to confirm/)).toHaveFocus();
    });

    it("deletes with Enter once the domain is typed", async () => {
      const { user, calls } = renderApp(`/apps/${app.id}`, { api: deletableApi() });
      await openDialog(user);

      await user.keyboard("shop.example.com{Enter}");

      expect(await screen.findByRole("heading", { level: 1, name: "Apps" })).toBeInTheDocument();
      expect(calls.some((c) => c.method === "DELETE")).toBe(true);
    });

    it("closes on Cancel, and on Escape, deleting nothing and focusing Delete again", async () => {
      const { user, calls } = renderApp(`/apps/${app.id}`, { api: deletableApi() });

      const dialog = await openDialog(user);
      await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Delete" })).toHaveFocus();

      await user.keyboard("{Enter}");
      expect(screen.getByRole("dialog", { name: "Delete shop?" })).toBeInTheDocument();
      await user.keyboard("shop{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Delete" })).toHaveFocus();

      expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    });

    it("starts empty each time it opens", async () => {
      const { user } = renderApp(`/apps/${app.id}`, { api: deletableApi() });

      let dialog = await openDialog(user);
      await user.type(within(dialog).getByLabelText(/to confirm/), "shop.example.com");
      await user.keyboard("{Escape}");
      dialog = await openDialog(user);

      expect(within(dialog).getByLabelText(/to confirm/)).toHaveValue("");
      expect(within(dialog).getByRole("button", { name: "Delete App" })).toBeDisabled();
    });
  });
});
