// Deploying an App, and one Deployment by its App and ID.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, request } from "@/api/client";
import type { Deployment } from "@/api/generated/domain";
import { appKeys, isInProgress, statusPollMs } from "@/api/apps";

export const deploymentKey = (appID: string, id: string) =>
  [...appKeys.deployments(appID), id] as const;

/**
 * One of an App's Deployments, by ID. Polled every 2 s while it's queued,
 * building or starting; the polling stops once it's live, failed or
 * superseded. An unknown ID (or another App's Deployment) fails at once
 * with a 404 (isNotFound).
 */
export function useDeployment(appID: string, id: string) {
  return useQuery({
    queryKey: deploymentKey(appID, id),
    queryFn: () =>
      request<Deployment>(
        `/api/v1/apps/${encodeURIComponent(appID)}/deployments/${encodeURIComponent(id)}`,
      ),
    retry: (failures, err) => !(err instanceof ApiError && err.status < 500) && failures < 3,
    refetchInterval: (query) =>
      query.state.data && isInProgress(query.state.data.status) ? statusPollMs : false,
  });
}

/**
 * Deploys an App's branch (its latest commit). Each call queues its own
 * Deployment: two quick clicks are two Deployments, run in turn by the
 * server. The answer goes into the App's Deployments at once, so neither
 * click's Deployment waits for (or is lost to) the other's refetch.
 */
export function useDeploy(appID: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () =>
      request<Deployment>(`/api/v1/apps/${encodeURIComponent(appID)}/deployments`, {
        method: "POST",
      }),
    onSuccess: (dep) => {
      client.setQueryData<Deployment[]>(appKeys.deployments(appID), (old = []) =>
        newestFirst([dep, ...old.filter((d) => d.id !== dep.id)]),
      );
      void client.invalidateQueries({ queryKey: appKeys.deployments(appID) });
      void client.invalidateQueries({ queryKey: appKeys.list, exact: true });
    },
  });
}

// The server's order; its times have trimmed fractions, so compare them as
// times, not strings.
function newestFirst(deps: Deployment[]): Deployment[] {
  return deps.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
}
