import { act, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { anAppListItem, appsListApi } from "@/test/app-fixtures";
import { deploymentsServer } from "@/test/deployment-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import type { FakeEventSource } from "@/test/network";
import { renderApp } from "@/test/render";

const app = anApp({ id: "app7", name: "shop", domain: "shop.example.com" });
const live = aDeployment({ id: "dep-1", app_id: "app7", status: "live" });
const logURL = "/api/v1/apps/app7/log/stream";

afterEach(() => {
  vi.useRealTimers();
});

// The App's page, and the App Log stream it opened.
async function openAppLog(deployments = [live]) {
  const server = deploymentsServer(app, deployments);
  const view = renderApp("/apps/app7", {
    api: { ...appsListApi([anAppListItem(app)]), ...server.api },
  });
  const log = await screen.findByRole("log", { name: "App Log" });
  const stream = latest(view.eventSources);
  act(() => stream.open());
  return { ...view, server, log, stream };
}

function latest(eventSources: typeof FakeEventSource): FakeEventSource {
  const stream = eventSources.latest(logURL);
  if (!stream) throw new Error(`no stream opened on ${logURL}`);
  return stream;
}

// One line of the App Log, as the server sends it.
let written = 0;
function print(stream: FakeEventSource, line: string) {
  written++;
  act(() => stream.emit(line, { id: `dep-1@${1_794_230_238_000_000_000 + written}` }));
}

function region() {
  return screen.getByRole("region", { name: "App Log" });
}

describe("the App Log", () => {
  it("follows what the live container prints, stdout and stderr both", async () => {
    const { log, stream } = await openAppLog();

    print(stream, "listening on :8080\n"); // stdout
    print(stream, "warning: disk is slow\n"); // stderr
    print(stream, "GET / 200\n");

    expect(log.textContent).toContain("listening on :8080\nwarning: disk is slow\nGET / 200\n");
    expect(stream.readyState).toBe(stream.OPEN);
  });

  it("says when a new Deployment replaced the container, and follows the new one when asked", async () => {
    const { user, log, stream, eventSources } = await openAppLog();
    print(stream, "serving v1\n");

    act(() => stream.emit("replaced", { type: "done" }));

    expect(stream.readyState).toBe(stream.CLOSED);
    expect(
      within(region()).getByText(/A new Deployment replaced the container/),
    ).toBeInTheDocument();
    expect(log).toHaveTextContent("serving v1"); // what it had stays until then
    expect(within(region()).queryByText("End of the log.")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Follow the new Deployment" }));

    const next = latest(eventSources);
    expect(next).not.toBe(stream);
    act(() => next.open());
    act(() => next.emit("serving v2\n", { id: "dep-2@1794230239000000000" }));
    const shown = screen.getByRole("log", { name: "App Log" });
    expect(shown.textContent).toContain("serving v2\n");
    expect(shown.textContent).not.toContain("serving v1");
    expect(
      screen.queryByRole("button", { name: "Follow the new Deployment" }),
    ).not.toBeInTheDocument();
  });

  it("says so when the App has no live Deployment, rather than showing an empty log", async () => {
    const { stream } = await openAppLog([]);

    act(() => stream.emit("no-live-deployment", { type: "done" }));

    expect(stream.readyState).toBe(stream.CLOSED);
    expect(
      within(region()).getByText(/No live Deployment, so nothing to follow/),
    ).toBeInTheDocument();
    expect(within(region()).queryByText("No output yet.")).not.toBeInTheDocument();
    expect(within(region()).queryByText("End of the log.")).not.toBeInTheDocument();
  });

  it("starts following once a Deployment goes live", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { user, server, stream, eventSources } = await openAppLog([]);
    act(() => stream.emit("no-live-deployment", { type: "done" }));

    await user.click(screen.getByRole("button", { name: "Deploy" }));
    server.set("dep-1", { status: "live" });
    await act(() => vi.advanceTimersByTimeAsync(2_000)); // the page's status polling

    const next = latest(eventSources);
    expect(next).not.toBe(stream);
    act(() => next.open());
    act(() => next.emit("listening on :8080\n", { id: "dep-1@1794230239000000000" }));
    expect(screen.getByRole("log", { name: "App Log" })).toHaveTextContent("listening on :8080");
    expect(screen.queryByText(/No live Deployment, so nothing to follow/)).not.toBeInTheDocument();
  });

  it("ends for good, saying so, when a reconnect is refused as signed out", async () => {
    const { log, stream } = await openAppLog();
    print(stream, "GET / 200\n");

    act(() => stream.fail({ closed: true })); // the reconnect got 401

    expect(within(region()).getByRole("status")).toHaveTextContent(
      "Signed out, reload to sign in.",
    );
    expect(log).toHaveTextContent("GET / 200");
  });

  it("closes the stream when the Admin leaves the App's page", async () => {
    const { user, stream } = await openAppLog();

    await user.click(screen.getByRole("link", { name: "All Apps" }));
    await screen.findByRole("heading", { level: 1, name: "Apps" });

    expect(stream.readyState).toBe(stream.CLOSED);
  });
});
