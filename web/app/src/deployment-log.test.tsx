import { act, fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { deploymentsServer } from "@/test/deployment-fixtures";
import { aDeployment, anApp } from "@/test/fixtures";
import { json, type FakeEventSource } from "@/test/network";
import { renderApp } from "@/test/render";

const app = anApp({ id: "app7", name: "shop", domain: "shop.example.com" });
const building = aDeployment({
  id: "dep-9",
  app_id: "app7",
  status: "building",
  commit: "",
  image: "",
  finished_at: undefined,
});
const page = "/apps/app7/deployments/dep-9";
const logURL = "/api/v1/apps/app7/deployments/dep-9/log/stream";

// The Deployment page of `building`, and the log stream it opened.
async function openLog() {
  const server = deploymentsServer(app, [building]); // also what the App's page reads
  const view = renderApp(page, { api: server.api });
  const log = await screen.findByRole("log", { name: "Deployment Log" });
  const stream = view.eventSources.latest(logURL);
  if (!stream) throw new Error(`no stream opened on ${logURL}`);
  act(() => stream.open());
  return { ...view, server, log, stream };
}

// Sends one chunk of the log, as the server does: its id is the byte
// offset after it.
let offset = 0;
function send(stream: FakeEventSource, text: string) {
  offset += new TextEncoder().encode(text).length;
  act(() => stream.emit(text, { id: String(offset) }));
}

// Gives the log's scroll box the sizes a browser would: `lines` lines of
// 10 px in a 100 px box.
function sizeLog(log: HTMLElement) {
  Object.defineProperty(log, "clientHeight", { configurable: true, get: () => 100 });
  Object.defineProperty(log, "scrollHeight", {
    configurable: true,
    get: () => 10 * (log.textContent ?? "").split("\n").length,
  });
}

function scrollTo(log: HTMLElement, top: number) {
  log.scrollTop = top;
  fireEvent.scroll(log);
}

describe("the Deployment Log", () => {
  it("streams onto a running Deployment's page as it's written", async () => {
    const { log, stream } = await openLog();

    send(stream, "==> fetching https://github.com/example/hello.git at main\n");
    send(stream, "==> building pantalan/app7:dep-9\n");

    expect(log).toHaveTextContent(/fetching .* at main\s*==> building pantalan\/app7:dep-9/);
    send(stream, "step 1/2: FROM alpine\n");
    expect(log.textContent).toContain("==> building pantalan/app7:dep-9\nstep 1/2: FROM alpine\n");
    expect(stream.readyState).toBe(stream.OPEN);
  });

  it("closes the stream at done, with the whole log on screen", async () => {
    const { log, stream } = await openLog();

    send(stream, "==> deployment failed: build: exit status 1\n");
    act(() => stream.emit("failed", { type: "done" }));

    expect(stream.readyState).toBe(stream.CLOSED);
    expect(log).toHaveTextContent("==> deployment failed: build: exit status 1");
    expect(
      within(screen.getByRole("region", { name: "Deployment Log" })).getByText("End of the log."),
    ).toBeInTheDocument();
  });

  it("starts with an omission line when the server skipped earlier output", async () => {
    const { log, stream } = await openLog();

    act(() => stream.emit("2400000", { type: "omitted", id: "2400000" }));
    send(stream, "build line 131072\n");

    expect(within(log).getByText("Earlier output omitted")).toBeInTheDocument();
    expect(log).toHaveTextContent("build line 131072");
  });

  it("keeps only the last 1 MiB on screen, with the omission line", async () => {
    const { log, stream } = await openLog();
    const line = (i: number) => `build line ${String(i).padStart(6, "0")}\n`; // 18 bytes
    const lines = (from: number, to: number) =>
      Array.from({ length: to - from }, (_, i) => line(from + i)).join("");

    send(stream, lines(0, 40_000)); // 720 kB
    expect(within(log).queryByText("Earlier output omitted")).not.toBeInTheDocument();
    send(stream, lines(40_000, 80_000)); // 1.44 MB in all

    expect(within(log).getByText("Earlier output omitted")).toBeInTheDocument();
    const shown = log.textContent ?? "";
    expect(shown.length).toBeLessThanOrEqual((1 << 20) + "Earlier output omitted".length);
    expect(shown).not.toContain(line(0));
    expect(shown).toContain(line(79_999));
    expect(shown).toMatch(/omitted\s*build line \d{6}\n/); // from a line's start
  });

  it("stays pinned to the newest line unless the Admin has scrolled up", async () => {
    const { log, stream } = await openLog();
    sizeLog(log);

    send(stream, Array.from({ length: 30 }, (_, i) => `line ${i}\n`).join(""));
    expect(log.scrollTop).toBe(log.scrollHeight);

    // Scrolled up to read: new output doesn't pull the view down.
    scrollTo(log, 50);
    send(stream, "line 30\n");
    expect(log.scrollTop).toBe(50);

    // Back at the bottom: pinned again.
    scrollTo(log, log.scrollHeight - log.clientHeight);
    send(stream, "line 31\n");
    expect(log.scrollTop).toBe(log.scrollHeight);
  });

  it("links to the whole log as plain text", async () => {
    await openLog();

    const link = screen.getByRole("link", { name: "Full log as plain text" });

    expect(link).toHaveAttribute("href", "/api/v1/apps/app7/deployments/dep-9/log");
  });

  it("keeps streaming while the Admin is on another page, and shows it all on return", async () => {
    const { user, log, stream, eventSources } = await openLog();
    send(stream, "line 1\n");

    await user.click(screen.getByRole("link", { name: "shop" })); // up to the App
    await screen.findByRole("heading", { level: 1, name: "shop" });
    send(stream, "line 2\n"); // while away
    expect(stream.readyState).toBe(stream.OPEN);
    window.history.back();
    const back = await screen.findByRole("log", { name: "Deployment Log" });
    send(stream, "line 3\n");

    expect(back.textContent).toContain("line 1\nline 2\nline 3\n");
    expect(back.textContent?.match(/line 2/g)).toHaveLength(1);
    expect(eventSources.instances.filter((s) => s.url.endsWith(logURL))).toHaveLength(1);
    expect(log).not.toBeInTheDocument(); // it is a new view of the same stream
  });

  it("keeps what it has through a dropped connection while the browser reconnects", async () => {
    const { log, stream } = await openLog();
    send(stream, "line 1\n");

    act(() => stream.fail()); // the browser retries, with Last-Event-ID
    act(() => stream.open());
    send(stream, "line 2\n");

    expect(log.textContent).toContain("line 1\nline 2\n");
    expect(screen.queryByText(/Signed out/)).not.toBeInTheDocument();
  });

  // An EventSource can't read the status of a refused reconnect, so the
  // page asks the server whether the session is still live.
  it("ends for good, saying so, when a reconnect is refused as signed out", async () => {
    const { log, stream, server } = await openLog();
    send(stream, "line 1\n");
    server.api["GET /api/v1/sessions/current"] = () =>
      json({ error: "not signed in" }, { status: 401 });

    act(() => stream.fail({ closed: true })); // the reconnect got 401

    expect(await within(logRegion()).findByRole("status")).toHaveTextContent(
      "Signed out, reload to sign in.",
    );
    expect(log).toHaveTextContent("line 1"); // what it had stays
  });

  it("says the server refused the stream, not signed out, while the session is live", async () => {
    const { log, stream } = await openLog(); // the session check answers 204
    send(stream, "line 1\n");

    act(() => stream.fail({ closed: true })); // the reconnect got a 404, say

    expect(await within(logRegion()).findByRole("status")).toHaveTextContent(
      "The server refused this log stream. Reload to try again.",
    );
    expect(screen.queryByText(/Signed out/)).not.toBeInTheDocument();
    expect(log).toHaveTextContent("line 1");
  });

  it("says the server can't be reached when the session check fails too", async () => {
    const { stream, server } = await openLog();
    server.api["GET /api/v1/sessions/current"] = () => Promise.reject(new TypeError("offline"));

    act(() => stream.fail({ closed: true }));

    expect(await within(logRegion()).findByRole("status")).toHaveTextContent(
      "Can't reach Pantalan to check the session. Reload to try again.",
    );
  });

  it("says the Deployment was deleted when the server ends the log that way", async () => {
    const { log, stream } = await openLog();
    send(stream, "line 1\n");

    act(() => stream.emit("deleted", { type: "done" }));

    expect(log).toHaveTextContent("The Deployment was deleted with its App.");
    expect(log).not.toHaveTextContent("End of the log.");
    expect(stream.readyState).toBe(stream.CLOSED);
  });
});

function logRegion() {
  return screen.getByRole("region", { name: "Deployment Log" });
}
