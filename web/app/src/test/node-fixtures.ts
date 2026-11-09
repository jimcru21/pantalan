// Node overview API fixtures, typed by the generated types (see
// fixtures.ts): GET /api/v1/node and its tabs' endpoints.
import type {
  NodeContainer,
  Node,
  NodeDockerInfo,
  NodeEvent,
  NodeEventLog,
  NodeImage,
} from "@/api/generated/node";
import { json, type Api } from "./network";

const GiB = 2 ** 30;

/** A healthy Node with a host reading and Docker up. */
export function aNode(fields: Partial<Node> = {}): Node {
  return {
    ip: "203.0.113.10",
    arch: "amd64",
    status: { level: "ok", reasons: ["Healthy"] },
    host: {
      hostname: "node-1",
      os: "Linux 6.12.38",
      uptime_seconds: ((12 * 24 + 4) * 60 + 17) * 60 + 42,
      load: [0.42, 0.38, 0.31],
      cpu: { ratio: 0.3, history: [0.1, 0.2, 0.3] },
      memory: {
        total: 8 * GiB,
        available: 2 * GiB,
        used: 6 * GiB,
        swap_total: 2 * GiB,
        swap_used: 0,
        history: [0.7, 0.75],
      },
      disks: [
        { path: "/var/lib/docker", total: 100 * GiB, free: 25 * GiB },
        { path: "/", total: 40 * GiB, free: 30 * GiB },
      ],
    },
    docker: {
      reachable: true,
      version: "29.1.3",
      containers: { running: 3, total: 4, unhealthy: 0 },
    },
    ...fields,
  };
}

/** The same Node while Docker can't be read. */
export function aNodeWithDockerDown(
  reason = "dial unix /var/run/docker.sock: connect: no such file",
): Node {
  return aNode({
    status: { level: "down", reasons: [`Docker unreachable: ${reason}`] },
    docker: { reachable: false, error: reason },
  });
}

export function aContainer(fields: Partial<NodeContainer> = {}): NodeContainer {
  return {
    id: "c0ffee",
    name: "postgres",
    image: "postgres:17",
    created: "2026-11-01T09:00:00Z",
    state: "running",
    status: "Up 3 hours",
    ports: [{ private_port: 5432, type: "tcp" }],
    stats: { cpu: 0.125, memory_used: 256 * 2 ** 20, memory_limit: 8 * GiB },
    ...fields,
  };
}

export function anImage(fields: Partial<NodeImage> = {}): NodeImage {
  return {
    id: "sha256:1",
    tags: ["postgres:17"],
    size: 450 * 2 ** 20,
    created: "2026-10-20T12:00:00Z",
    in_use: true,
    ...fields,
  };
}

export function aDockerInfo(fields: Partial<NodeDockerInfo> = {}): NodeDockerInfo {
  const use = { total: 4, active: 2, size: 2 * GiB, reclaimable: GiB };
  return {
    version: "29.1.3",
    api_version: "1.47",
    min_api_version: "1.24",
    max_api_version: "1.52",
    driver: "overlay2",
    cgroup_version: "2",
    cgroup_driver: "systemd",
    root_dir: "/var/lib/docker",
    warnings: [],
    disk_usage: {
      images: use,
      containers: use,
      volumes: use,
      build_cache: use,
      at: "2026-11-09T10:00:00Z",
    },
    ...fields,
  };
}

export function anEvent(fields: Partial<NodeEvent> = {}): NodeEvent {
  return {
    time: "2026-11-09T10:15:30.123456789Z",
    container_id: "c0ffee",
    container: "postgres",
    image: "postgres:17",
    action: "start",
    gap_before: false,
    ...fields,
  };
}

export function anEventLog(fields: Partial<NodeEventLog> = {}): NodeEventLog {
  return { events: [anEvent()], ...fields };
}

/**
 * The endpoints the overview polls when it opens on its default tab: a
 * healthy Node with one container. Spread it and override what a test
 * cares about.
 */
export function overviewApi(): Api {
  return {
    "GET /api/v1/node": () => json<Node>(aNode()),
    "GET /api/v1/node/containers": () => json<NodeContainer[]>([aContainer()]),
  };
}

/** Docker's 503 for the overview's Docker reads while it is unreachable. */
export function dockerUnreachable(
  reason = "dial unix /var/run/docker.sock: connect: no such file",
) {
  return json({ error: `Docker unreachable: ${reason}` }, { status: 503 });
}
