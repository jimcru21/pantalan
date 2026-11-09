// The Node overview API (GET /api/v1/node and its tabs), as TanStack
// queries with the overview's polling cadence. An interval pauses while
// the browser tab is hidden, and a query that is due refetches as soon as
// the tab is shown again (TanStack's focus handling), so a background tab
// doesn't load the Node.
import { useQuery } from "@tanstack/react-query";
import type {
  Node,
  NodeContainer,
  NodeDockerInfo,
  NodeEventLog,
  NodeImage,
} from "@/api/generated/node";

/** An answer other than 2xx: its status and the API's {"error"} message. */
export class NodeApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "NodeApiError";
    this.status = status;
  }
}

/** True for the 503 the Docker reads answer while Docker is unreachable. */
export function isDockerUnreachable(err: unknown): boolean {
  return err instanceof NodeApiError && err.status === 503;
}

// A GET behind sign-in. Kept minimal on purpose: the shared API client
// (with the 401 -> /login redirect) replaces it.
async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: unknown };
      if (typeof body.error === "string") message = body.error;
    } catch {
      // not JSON
    }
    throw new NodeApiError(res.status, message);
  }
  return (await res.json()) as T;
}

// Polled queries don't retry: the next poll is the retry, and a failure
// (Docker going down) shows at once instead of after the backoff.
const polled = { retry: false } as const;

/** The top bar and the stat band, every 5 s. */
export function useNode() {
  return useQuery({
    ...polled,
    queryKey: ["node"],
    queryFn: () => get<Node>("/api/v1/node"),
    refetchInterval: 5_000,
  });
}

/** The Containers tab, every 5 s while it is open. */
export function useNodeContainers() {
  return useQuery({
    ...polled,
    queryKey: ["node", "containers"],
    queryFn: () => get<NodeContainer[]>("/api/v1/node/containers"),
    refetchInterval: 5_000,
  });
}

/** The Images tab, every 30 s while it is open. */
export function useNodeImages() {
  return useQuery({
    ...polled,
    queryKey: ["node", "images"],
    queryFn: () => get<NodeImage[]>("/api/v1/node/images"),
    refetchInterval: 30_000,
  });
}

/** The Events tab, every 5 s while it is open. */
export function useNodeEvents() {
  return useQuery({
    ...polled,
    queryKey: ["node", "events"],
    queryFn: () => get<NodeEventLog>("/api/v1/node/events"),
    refetchInterval: 5_000,
  });
}

/**
 * The Docker tab: system/df is slow, so it loads only when the Admin asks,
 * by opening the tab or with refetch() (Refresh). It never polls and
 * doesn't refetch when the browser tab is shown again.
 */
export function useNodeDocker() {
  return useQuery({
    ...polled,
    queryKey: ["node", "docker"],
    queryFn: () => get<NodeDockerInfo>("/api/v1/node/docker"),
    refetchOnMount: "always",
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}
