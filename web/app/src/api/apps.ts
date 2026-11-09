// The Apps API (GET /api/v1/apps and an App's own routes), as TanStack
// queries. Apps are addressed by ID: names aren't unique.
import { useQuery } from "@tanstack/react-query";
import { ApiError, request } from "@/api/client";
import type { Deployment } from "@/api/generated/domain";
import type { App, AppListItem } from "@/api/generated/httpapi";
import { appPaths } from "@/api/paths";

/** Query keys, so mutations can invalidate what they change. */
export const appKeys = {
  list: ["apps"] as const,
  app: (appId: string) => ["apps", appId] as const,
  deployments: (appId: string) => ["apps", appId, "deployments"] as const,
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
    queryFn: () => request<AppListItem[]>(appPaths.apps),
    refetchInterval: 5_000,
    retry: false,
  });
}

/**
 * Retries only what may pass next time: a network error or a 5xx. A 4xx
 * (an unknown ID, a signed-out session) answers the same again. For the
 * `retry` of a query that reads by ID.
 */
export function retryUnlessAnswered(failures: number, err: Error): boolean {
  if (err instanceof ApiError && err.status < 500) return false;
  return failures < 3;
}

/** One App, by ID. An unknown ID fails at once with a 404 (isNotFound). */
export function useApp(appId: string) {
  return useQuery({
    queryKey: appKeys.app(appId),
    queryFn: () => request<App>(appPaths.app(appId)),
    retry: retryUnlessAnswered,
  });
}

/**
 * An App's Deployments, newest first. Polled every 2 s while any of them is
 * on its way (queued, building or starting), so status moves on screen;
 * once all are final the polling stops.
 */
export function useDeployments(appId: string) {
  return useQuery({
    queryKey: appKeys.deployments(appId),
    queryFn: () => request<Deployment[]>(appPaths.deployments(appId)),
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
