// The Apps API (GET /api/v1/apps and an App's own routes), as TanStack
// queries. Apps are addressed by ID: names aren't unique.
import { useQuery } from "@tanstack/react-query";
import { ApiError, request } from "@/api/client";
import type { App, Deployment } from "@/api/generated/domain";
import type { AppListItem } from "@/api/generated/httpapi";

/** Query keys, so mutations can invalidate what they change. */
export const appKeys = {
  list: ["apps"] as const,
  app: (id: string) => ["apps", id] as const,
  deployments: (id: string) => ["apps", id, "deployments"] as const,
};

/** True for the API's 404: no App (or Deployment) has that ID. */
export function isNotFound(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

/**
 * The Apps list with each App's latest Deployment, every 5 s so the Admin
 * can watch Deployments finish. The interval pauses while the browser tab
 * is hidden and catches up when it's shown again (TanStack's focus
 * handling). No retries: the next poll is the retry.
 */
export function useApps() {
  return useQuery({
    queryKey: appKeys.list,
    queryFn: () => request<AppListItem[]>("/api/v1/apps"),
    refetchInterval: 5_000,
    retry: false,
  });
}

// Retries only what may pass next time: a network error or a 5xx. A 4xx
// (an unknown ID, a signed-out session) answers the same again.
function retryUnlessAnswered(failures: number, err: Error): boolean {
  if (err instanceof ApiError && err.status < 500) return false;
  return failures < 3;
}

/** One App, by ID. An unknown ID fails at once with a 404 (isNotFound). */
export function useApp(id: string) {
  return useQuery({
    queryKey: appKeys.app(id),
    queryFn: () => request<App>(`/api/v1/apps/${encodeURIComponent(id)}`),
    retry: retryUnlessAnswered,
  });
}

/**
 * An App's Deployments, newest first. Polled every 2 s while any of them is
 * on its way (queued, building or starting), so status moves on screen;
 * once all are final the polling stops.
 */
export function useDeployments(appID: string) {
  return useQuery({
    queryKey: appKeys.deployments(appID),
    queryFn: () => request<Deployment[]>(`/api/v1/apps/${encodeURIComponent(appID)}/deployments`),
    retry: retryUnlessAnswered,
    refetchInterval: (query) =>
      query.state.data?.some((d) => isInProgress(d.status)) ? statusPollMs : false,
  });
}

/** How often a Deployment on its way is polled for its status. */
export const statusPollMs = 2_000;

/** True for a Deployment on its way: queued, building or starting. */
export function isInProgress(status: Deployment["status"]): boolean {
  return status === "queued" || status === "building" || status === "starting";
}
