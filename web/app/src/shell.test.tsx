import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderApp } from "@/test/render";
import { overviewApi } from "@/test/node-fixtures";

describe("the Dashboard shell", () => {
  it("shows the page the address bar names", async () => {
    renderApp("/apps");

    expect(await screen.findByRole("heading", { name: "Apps" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Dashboard" });
    expect(nav).toContainElement(screen.getByRole("link", { name: "Apps" }));
    expect(screen.getByRole("link", { name: "Apps" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute("aria-current");
  });

  it("moves between Overview and Apps without a reload, with back and forward", async () => {
    const { user } = renderApp("/", { api: overviewApi() });
    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument();
    const shell = screen.getByRole("navigation", { name: "Dashboard" });

    await user.click(screen.getByRole("link", { name: "Apps" }));
    expect(await screen.findByRole("heading", { name: "Apps" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps");
    // The same sidebar element: the page swapped its content, not reloaded.
    expect(screen.getByRole("navigation", { name: "Dashboard" })).toBe(shell);

    window.history.back();
    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");

    window.history.forward();
    expect(await screen.findByRole("heading", { name: "Apps" })).toBeInTheDocument();
  });

  it("remembers a collapsed sidebar across a reload", async () => {
    const { user, reload } = renderApp("/", { api: overviewApi() });
    const collapse = await screen.findByRole("button", { name: "Collapse sidebar" });
    expect(collapse).toHaveAttribute("aria-expanded", "true");

    await user.click(collapse);
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );

    reload();
    const expand = await screen.findByRole("button", { name: "Expand sidebar" });
    expect(expand).toHaveAttribute("aria-expanded", "false");

    await user.click(expand);
    reload();
    expect(await screen.findByRole("button", { name: "Collapse sidebar" })).toBeInTheDocument();
  });

  it("keeps the nav labels reachable while the sidebar is collapsed", async () => {
    const { user } = renderApp("/", { api: overviewApi() });
    await user.click(await screen.findByRole("button", { name: "Collapse sidebar" }));

    // Each link keeps its accessible name and shows it as a tooltip on focus.
    focusFromTop();
    await user.tab();
    const focused = document.activeElement;
    expect(focused).toHaveAccessibleName("Overview");
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Overview");
  });

  it("injects no <style> element, which the CSP would block", async () => {
    const { user } = renderApp("/", { api: overviewApi() });
    await user.click(await screen.findByRole("button", { name: "Collapse sidebar" }));
    focusFromTop();
    await user.tab();
    await screen.findByRole("tooltip");
    await user.click(screen.getByRole("link", { name: "Apps" }));
    await screen.findByRole("heading", { name: "Apps" });

    expect(document.querySelectorAll("style")).toHaveLength(0);
  });
});

// Moves focus back to the start of the page, so the next Tab lands on the
// first focusable element.
function focusFromTop() {
  (document.activeElement as HTMLElement | null)?.blur();
}
