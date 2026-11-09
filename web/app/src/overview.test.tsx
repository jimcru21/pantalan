import { act, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  Node,
  NodeContainer,
  NodeDockerInfo,
  NodeEventLog,
  NodeImage,
} from "@/api/generated/httpapi";
import type { LoginInfo } from "@/api/generated/httpapi";
import { aLoginInfo } from "@/test/fixtures";
import { json, status } from "@/test/network";
import {
  aContainer,
  aDockerInfo,
  aNode,
  anEvent,
  anEventLog,
  anImage,
  aNodeWithDockerDown,
  dockerUnreachable,
  overviewApi,
} from "@/test/node-fixtures";
import { renderApp } from "@/test/render";
import { setHidden } from "@/test/visibility";

describe("the Node overview", () => {
  it("names the Node in the top bar", async () => {
    renderApp("/", { api: overviewApi() });

    const top = await screen.findByRole("banner", { name: "Node" });
    expect(await within(top).findByText("node-1")).toBeInTheDocument();
    expect(top).toHaveTextContent("203.0.113.10 · Linux 6.12.38 · amd64");
    expect(top).toHaveTextContent("up 12d 4h 17m");
    expect(top).toHaveTextContent("load 0.42 0.38 0.31");
    expect(within(top).getByRole("button", { name: "Status: Healthy" })).toBeInTheDocument();
  });

  it("lists the status dot's reasons in its tooltip", async () => {
    const reasons = ["Disk / has under 10% free", "Container web is unhealthy"];
    renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node": () => json<Node>(aNode({ status: { level: "warn", reasons } })),
      },
    });

    const dot = await screen.findByRole("button", { name: `Status: ${reasons.join("; ")}` });
    act(() => dot.focus());
    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent(reasons[0]!);
    expect(tooltip).toHaveTextContent(reasons[1]!);
  });

  it("shows the value at the hovered point of a sparkline", async () => {
    const node = aNode();
    node.host!.cpu.history = [0.1, 0.42, 0.87];
    renderApp("/", { api: { ...overviewApi(), "GET /api/v1/node": () => json<Node>(node) } });

    const cpu = await screen.findByRole("group", { name: "CPU" });
    const chart = await within(cpu).findByRole("application", { name: "CPU, last 10 minutes" });
    const width = Number(chart.getAttribute("width"));

    // The newest sample is at the right edge, the one before it a slot to its left.
    hover(chart, width);
    expect(await within(cpu).findByText("87%")).toBeInTheDocument();
    hover(chart, width * (118 / 119));
    expect(await within(cpu).findByText("42%")).toBeInTheDocument();
    expect(within(cpu).queryByText("87%")).not.toBeInTheDocument();
    // Recharts draws without a <style> element, which the CSP would block.
    expect(document.querySelectorAll("style")).toHaveLength(0);
  });

  it("shows the Node's health as tiles", async () => {
    renderApp("/", { api: overviewApi() });

    const band = await screen.findByRole("region", { name: "Node health" });
    const tile = (name: string) => within(band).getByRole("group", { name });
    expect(await within(band).findByRole("group", { name: "CPU" })).toHaveTextContent("30%");
    expect(tile("Memory")).toHaveTextContent("75%");
    expect(tile("Memory")).toHaveTextContent("6.0 of 8.0 GiB");
    expect(tile("Memory")).not.toHaveTextContent("Swap");
    expect(tile("Disk /var/lib/docker")).toHaveTextContent("25.0 GiB free");
    expect(tile("Disk /var/lib/docker")).toHaveTextContent("of 100.0 GiB");
    expect(within(tile("Disk /var/lib/docker")).getByRole("meter")).toHaveAttribute(
      "aria-valuenow",
      "75",
    );
    expect(tile("Disk /")).toHaveTextContent("30.0 GiB free");
    expect(tile("Containers")).toHaveTextContent("3/4 running");
    expect(tile("Docker")).toHaveTextContent("29.1.3");
  });

  it("shows swap in use and unhealthy containers", async () => {
    const node = aNode();
    node.host!.memory.swap_used = 2 ** 29;
    node.docker.containers = { running: 2, total: 2, unhealthy: 1 };
    renderApp("/", { api: { ...overviewApi(), "GET /api/v1/node": () => json<Node>(node) } });

    const band = await screen.findByRole("region", { name: "Node health" });
    expect(await within(band).findByRole("group", { name: "Memory" })).toHaveTextContent(
      "Swap 0.5 of 2.0 GiB",
    );
    expect(within(band).getByRole("group", { name: "Containers" })).toHaveTextContent(
      "1 unhealthy",
    );
  });

  it("says so before the Node has a reading", async () => {
    renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node": () =>
          json<Node>(
            aNode({
              host: undefined,
              host_error: "read /proc/meminfo: permission denied",
              status: { level: "off", reasons: ["No Node information yet"] },
            }),
          ),
      },
    });

    const top = await screen.findByRole("banner", { name: "Node" });
    expect(await within(top).findByText("Node information unavailable")).toBeInTheDocument();
    expect(within(top).getByRole("button", { name: "Status: No Node information yet" }));
    const band = screen.getByRole("region", { name: "Node health" });
    expect(within(band).getByRole("group", { name: "Node" })).toHaveTextContent(
      "Unavailable: read /proc/meminfo: permission denied",
    );
    expect(within(band).getByRole("group", { name: "Docker" })).toHaveTextContent("29.1.3");
  });
});

describe("the overview's session", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("sends a tab whose session expired to sign-in at its next poll", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let signedIn = true;
    renderApp("/", {
      api: {
        "GET /api/v1/node": () => (signedIn ? json<Node>(aNode()) : status(401)),
        "GET /api/v1/node/containers": () =>
          signedIn ? json<NodeContainer[]>([aContainer()]) : status(401),
        "GET /api/v1/login-info": () => json<LoginInfo>(aLoginInfo()),
      },
    });
    await screen.findByRole("row", { name: /postgres/ });

    signedIn = false;
    await act(() => vi.advanceTimersByTimeAsync(5_000));

    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/login");
  });
});

describe("the overview while Docker is down", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("keeps the host tiles updating, shows the Docker card, and recovers on its own", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let dockerUp = false;
    let cpu = 0.2;
    const node = () => {
      const n = dockerUp ? aNode() : aNodeWithDockerDown();
      n.host!.cpu.ratio = cpu;
      return n;
    };
    renderApp("/", {
      api: {
        "GET /api/v1/node": () => json<Node>(node()),
        "GET /api/v1/node/containers": () =>
          dockerUp
            ? json<NodeContainer[]>([aContainer()])
            : dockerUnreachable("connection refused"),
      },
    });

    const panel = await screen.findByRole("tabpanel", { name: "Containers" });
    const card = await within(panel).findByRole("alert");
    expect(card).toHaveTextContent("Docker unreachable");
    expect(card).toHaveTextContent("Docker unreachable: connection refused");
    expect(card).toHaveTextContent("Check the daemon on the Node with systemctl status docker.");
    const band = screen.getByRole("region", { name: "Node health" });
    expect(within(band).getByRole("group", { name: "CPU" })).toHaveTextContent("20%");
    expect(within(band).getByRole("group", { name: "Containers" })).toHaveTextContent(
      "Docker unreachable",
    );
    expect(within(band).getByRole("group", { name: "Docker" })).toHaveTextContent(
      "Docker unreachable",
    );
    expect(screen.getByRole("button", { name: /^Status: Docker unreachable/ })).toBeInTheDocument();

    // The host keeps updating while Docker stays down.
    cpu = 0.55;
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(within(band).getByRole("group", { name: "CPU" })).toHaveTextContent("55%");
    expect(within(panel).getByRole("alert")).toHaveTextContent("Docker unreachable");

    // Docker comes back: the next poll brings it all back, with no reload.
    dockerUp = true;
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(await within(panel).findByRole("row", { name: /postgres/ })).toBeInTheDocument();
    expect(within(panel).queryByRole("alert")).not.toBeInTheDocument();
    expect(within(band).getByRole("group", { name: "Docker" })).toHaveTextContent("29.1.3");
    expect(screen.getByRole("button", { name: "Status: Healthy" })).toBeInTheDocument();
  });

  it("shows the Docker card on every Docker tab, and keeps the events from before", async () => {
    const { user } = renderApp("/", {
      api: {
        "GET /api/v1/node": () => json<Node>(aNodeWithDockerDown()),
        "GET /api/v1/node/containers": () => dockerUnreachable(),
        "GET /api/v1/node/images": () => dockerUnreachable(),
        "GET /api/v1/node/docker": () => dockerUnreachable(),
        "GET /api/v1/node/events": () =>
          json<NodeEventLog>(anEventLog({ stream_error: "connection refused" })),
      },
    });

    for (const name of ["Images", "Docker"]) {
      await user.click(await screen.findByRole("tab", { name }));
      const panel = await screen.findByRole("tabpanel", { name });
      expect(await within(panel).findByRole("alert")).toHaveTextContent(
        "Check the daemon on the Node with systemctl status docker.",
      );
    }

    await user.click(screen.getByRole("tab", { name: "Events" }));
    const events = await screen.findByRole("tabpanel", { name: "Events" });
    expect(await within(events).findByRole("row", { name: /postgres/ })).toBeInTheDocument();
    expect(within(events).getByRole("alert")).toHaveTextContent("connection refused");
  });
});

describe("the overview's tabs", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("opens on Containers, badging each App's container with the App's name", async () => {
    renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/containers": () =>
          json<NodeContainer[]>([
            aContainer({
              name: "pantalan-app1-dep7",
              image: "pantalan/hello:0123456",
              health: "healthy",
              status: "Up 3 hours (healthy)",
              id: "c1",
              ports: [
                { ip: "127.0.0.1", private_port: 8080, public_port: 49153, type: "tcp" },
                { private_port: 9000, type: "udp" },
              ],
              app: { id: "app1", name: "hello" },
            }),
            aContainer({ id: "c2", name: "orphan", image: "busybox", app: { id: "gone1" } }),
            aContainer({
              id: "c3",
              name: "postgres",
              state: "exited",
              status: "Exited (0) 2 days ago",
              ports: [],
              stats: undefined,
            }),
          ]),
      },
    });

    const tab = screen.getByRole("tab", { name: "Containers" });
    expect(tab).toHaveAttribute("aria-selected", "true");
    const panel = await screen.findByRole("tabpanel", { name: "Containers" });
    const app = await within(panel).findByRole("row", { name: /pantalan-app1-dep7/ });
    expect(within(app).getByText("hello")).toHaveAccessibleDescription("Pantalan App");
    expect(app).toHaveTextContent("pantalan/hello:0123456");
    expect(app).toHaveTextContent("running · healthy");
    expect(app).toHaveTextContent("127.0.0.1:49153→8080/tcp, 9000/udp");
    expect(app).toHaveTextContent("3 hours");
    expect(app).toHaveTextContent("12.5%");
    expect(app).toHaveTextContent("256.0 MiB / 8.0 GiB");
    // An App that no longer exists is named by its ID.
    expect(within(panel).getByRole("row", { name: /orphan/ })).toHaveTextContent("gone1");
    const stopped = within(panel).getByRole("row", { name: /postgres/ });
    expect(stopped).toHaveTextContent("Exited (0) 2 days ago");
    expect(stopped).not.toHaveTextContent("%");
  });

  it("says when the Node has no containers", async () => {
    renderApp("/", {
      api: { ...overviewApi(), "GET /api/v1/node/containers": () => json<NodeContainer[]>([]) },
    });

    expect(await screen.findByText("No containers on this Node.")).toBeInTheDocument();
  });

  it("fetches and polls only the open tab", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { user, calls } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/images": () => json<NodeImage[]>([anImage()]),
        "GET /api/v1/node/events": () => json<NodeEventLog>(anEventLog()),
      },
    });
    const fetched = (path: string) => calls.filter((c) => c.path === path).length;

    await screen.findByRole("row", { name: /postgres/ });
    expect(fetched("/api/v1/node/containers")).toBe(1);
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(fetched("/api/v1/node/containers")).toBe(2);
    expect(fetched("/api/v1/node")).toBe(2);

    await user.click(screen.getByRole("tab", { name: "Images" }));
    await screen.findByRole("tabpanel", { name: "Images" });
    await act(() => vi.advanceTimersByTimeAsync(25_000));
    expect(fetched("/api/v1/node/images")).toBe(1);
    expect(fetched("/api/v1/node/containers")).toBe(2);
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(fetched("/api/v1/node/images")).toBe(2);

    await user.click(screen.getByRole("tab", { name: "Events" }));
    await screen.findByRole("tabpanel", { name: "Events" });
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(fetched("/api/v1/node/images")).toBe(2);
    expect(fetched("/api/v1/node/events")).toBeGreaterThanOrEqual(6);
    expect(fetched("/api/v1/node/containers")).toBe(2);
    expect(fetched("/api/v1/node/docker")).toBe(0);
  });

  it("loads the Docker tab only when asked: on opening it and on Refresh", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { user, calls } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/docker": () =>
          json<NodeDockerInfo>(aDockerInfo({ warnings: ["No swap limit support"] })),
      },
    });
    const fetched = () => calls.filter((c) => c.path === "/api/v1/node/docker").length;
    await screen.findByRole("row", { name: /postgres/ });
    expect(fetched()).toBe(0);

    await user.click(screen.getByRole("tab", { name: "Docker" }));
    const engine = await screen.findByRole("region", { name: "Engine" });
    expect(engine).toHaveTextContent("Version29.1.3");
    expect(engine).toHaveTextContent("API version1.47 daemon accepts 1.24–1.52");
    expect(engine).toHaveTextContent("Storage driveroverlay2");
    expect(engine).toHaveTextContent("Cgroupv2 (systemd)");
    expect(engine).toHaveTextContent("Root dir/var/lib/docker");
    expect(within(engine).getByRole("listitem")).toHaveTextContent("No swap limit support");
    const usage = screen.getByRole("region", { name: "Disk usage" });
    expect(within(usage).getByRole("row", { name: /Build cache/ })).toHaveTextContent(
      "Build cache422.0 GiB1.0 GiB",
    );
    expect(fetched()).toBe(1);

    // It doesn't poll, nor refetch when the browser tab comes back.
    await act(() => vi.advanceTimersByTimeAsync(120_000));
    await act(async () => setHidden(true));
    await act(async () => setHidden(false));
    expect(fetched()).toBe(1);

    await user.click(within(engine).getByRole("button", { name: "Refresh" }));
    await vi.waitFor(() => expect(fetched()).toBe(2));
  });

  it("shows the Docker tab's facts when its disk usage can't be read", async () => {
    const { user } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/docker": () =>
          json<NodeDockerInfo>(
            aDockerInfo({ disk_usage: undefined, disk_usage_error: "system/df: timeout" }),
          ),
      },
    });
    await user.click(await screen.findByRole("tab", { name: "Docker" }));

    expect(await screen.findByRole("region", { name: "Engine" })).toHaveTextContent("None");
    expect(
      within(screen.getByRole("region", { name: "Disk usage" })).getByRole("alert"),
    ).toHaveTextContent("system/df: timeout");
  });

  it("lists the images and whether each is in use", async () => {
    const { user } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/images": () =>
          json<NodeImage[]>([
            anImage({ id: "i1", tags: ["postgres:17", "postgres:latest"] }),
            anImage({ id: "i2", tags: [], size: 512, in_use: false }),
          ]),
      },
    });
    await user.click(await screen.findByRole("tab", { name: "Images" }));

    const panel = await screen.findByRole("tabpanel", { name: "Images" });
    const used = await within(panel).findByRole("row", { name: /postgres:17/ });
    expect(used).toHaveTextContent("postgres:17, postgres:latest");
    expect(used).toHaveTextContent("450.0 MiB");
    expect(used).toHaveTextContent("Oct 20, 2026");
    expect(used).toHaveTextContent("In use");
    const dangling = within(panel).getByRole("row", { name: /<none>:<none>/ });
    expect(dangling).toHaveTextContent("512 B");
    expect(dangling).toHaveTextContent("Unused");
  });

  it("lists events newest first, marking where some may be missing", async () => {
    const { user } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/events": () =>
          json<NodeEventLog>({
            stream_error: "connection reset",
            events: [
              anEvent({ time: "2026-11-09T10:15:30.5Z", action: "die", exit_code: 137 }),
              anEvent({
                time: "2026-11-09T10:15:29Z",
                action: "kill",
                signal: "SIGKILL",
                gap_before: true,
              }),
              anEvent({
                time: "2026-11-09T09:00:00Z",
                container: undefined,
                container_id: "deadbeef",
                action: "health_status",
                health: "unhealthy",
              }),
            ],
          }),
      },
    });
    await user.click(await screen.findByRole("tab", { name: "Events" }));

    const panel = await screen.findByRole("tabpanel", { name: "Events" });
    expect(await within(panel).findByRole("alert")).toHaveTextContent(
      "Not connected to Docker's event stream, retrying: connection reset",
    );
    const rows = within(panel).getAllByRole("row").slice(1); // after the header
    expect(rows.map((r) => r.textContent)).toEqual([
      "10:15:30postgresdieexit code 137postgres:17",
      "10:15:29postgreskillsignal SIGKILLpostgres:17",
      "Stream reconnected: earlier events may be missing",
      "09:00:00deadbeefhealth_statusunhealthypostgres:17",
    ]);
  });

  it("says when there have been no events", async () => {
    const { user } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/events": () => json<NodeEventLog>({ events: [] }),
      },
    });
    await user.click(await screen.findByRole("tab", { name: "Events" }));

    expect(
      await screen.findByText("No container events since Pantalan started."),
    ).toBeInTheDocument();
  });

  it("pauses polling while the browser tab is hidden and catches up on return", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { calls } = renderApp("/", { api: overviewApi() });
    const fetched = (path: string) => calls.filter((c) => c.path === path).length;
    await screen.findByRole("row", { name: /postgres/ });

    await act(async () => setHidden(true));
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(fetched("/api/v1/node")).toBe(1);
    expect(fetched("/api/v1/node/containers")).toBe(1);

    await act(async () => setHidden(false));
    await vi.waitFor(() => {
      expect(fetched("/api/v1/node")).toBe(2);
      expect(fetched("/api/v1/node/containers")).toBe(2);
    });
  });

  it("can be moved between with the keyboard", async () => {
    const { user } = renderApp("/", {
      api: {
        ...overviewApi(),
        "GET /api/v1/node/images": () => json<NodeImage[]>([anImage()]),
      },
    });
    await screen.findByRole("row", { name: /postgres/ });

    act(() => screen.getByRole("tab", { name: "Containers" }).focus());
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Images" })).toHaveFocus();
    expect(await screen.findByRole("tabpanel", { name: "Images" })).toBeInTheDocument();
  });
});

// Moves the pointer over a chart to x pixels from its left edge. jsdom
// lays nothing out, so the chart's own box starts at 0.
function hover(chart: Element, x: number) {
  fireEvent.mouseMove(chart, { clientX: x, clientY: 10 });
}
