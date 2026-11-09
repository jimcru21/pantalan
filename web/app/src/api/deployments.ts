// Deploying an App, and one Deployment by its App and ID.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { request } from "@/api/client";
import type { Deployment } from "@/api/generated/domain";
import { appKeys, isInProgress, retryUnlessAnswered, statusPollMs } from "@/api/apps";
import { appPaths } from "@/api/paths";

export const deploymentKey = (appId: string, id: string) =>
  [...appKeys.deployments(appId), id] as const;

/**
 * One of an App's Deployments, by ID. Polled every 2 s while it's queued,
 * building or starting; the polling stops once it's live, failed or
 * superseded. An unknown ID (or another App's Deployment) fails at once
 * with a 404 (isNotFound).
 */
export function useDeployment(appId: string, id: string) {
  return useQuery({
    queryKey: deploymentKey(appId, id),
    queryFn: () => request<Deployment>(appPaths.deployment(appId, id)),
    retry: retryUnlessAnswered,
    refetchInterval: (query) =>
      query.state.data && isInProgress(query.state.data.status) ? statusPollMs : false,
  });
}

/**
 * Deploys an App. `mutate({})` deploys the App's branch (its latest
 * commit); `mutate({ redeployOf: id })` deploys the exact commit an earlier
 * Deployment resolved instead, which is how an App rolls back (the earlier
 * Deployment stays as it was). Each call queues its own Deployment: two
 * quick clicks are two Deployments, run in turn by the server. The answer
 * goes into the App's Deployments at once, so neither click's Deployment
 * waits for (or is lost to) the other's refetch.
 */
export function useDeploy(appId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ redeployOf }: { redeployOf?: string }) =>
      request<Deployment>(
        redeployOf ? appPaths.redeploy(appId, redeployOf) : appPaths.deployments(appId),
        { method: "POST" },
      ),
    onSuccess: (dep) => {
      client.setQueryData<Deployment[]>(appKeys.deployments(appId), (old = []) =>
        newestFirst([dep, ...old.filter((d) => d.id !== dep.id)]),
      );
      void client.invalidateQueries({ queryKey: appKeys.deployments(appId) });
      void client.invalidateQueries({ queryKey: appKeys.list, exact: true });
    },
  });
}

// The server's order; its times have trimmed fractions, so compare them as
// times, not strings.
function newestFirst(deps: Deployment[]): Deployment[] {
  return deps.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
}
