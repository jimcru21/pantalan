// The Node overview API (GET /api/v1/node and its tabs), as TanStack
// queries with the overview's polling cadence. An interval pauses while
// the browser tab is hidden, and a query that is due refetches as soon as
// the tab is shown again (TanStack's focus handling), so a background tab
// doesn't load the Node.
import { useQuery } from "@tanstack/react-query";
import { ApiError, request } from "@/api/client";
import type {
  Node,
  NodeContainer,
  NodeDockerInfo,
  NodeEventLog,
  NodeImage,
} from "@/api/generated/node";

/** True for the 503 the Docker reads answer while Docker is unreachable. */
export function isDockerUnreachable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 503;
}

// Polled queries don't retry: the next poll is the retry, and a failure
// (Docker going down) shows at once instead of after the backoff.
const polled = { retry: false } as const;

/** The top bar and the stat band, every 5 s. */
export function useNode() {
  return useQuery({
    ...polled,
    queryKey: ["node"],
    queryFn: () => request<Node>("/api/v1/node"),
    refetchInterval: 5_000,
  });
}

/** The Containers tab, every 5 s while it is open. */
export function useNodeContainers() {
  return useQuery({
    ...polled,
    queryKey: ["node", "containers"],
    queryFn: () => request<NodeContainer[]>("/api/v1/node/containers"),
    refetchInterval: 5_000,
  });
}

/** The Images tab, every 30 s while it is open. */
export function useNodeImages() {
  return useQuery({
    ...polled,
    queryKey: ["node", "images"],
    queryFn: () => request<NodeImage[]>("/api/v1/node/images"),
    refetchInterval: 30_000,
  });
}

/** The Events tab, every 5 s while it is open. */
export function useNodeEvents() {
  return useQuery({
    ...polled,
    queryKey: ["node", "events"],
    queryFn: () => request<NodeEventLog>("/api/v1/node/events"),
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
    queryFn: () => request<NodeDockerInfo>("/api/v1/node/docker"),
    refetchOnMount: "always",
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}
