// An App's env vars as dotenv text (GET and PUT /api/v1/apps/{id}/env),
// the only place their values come from: App responses carry the names
// alone (env_names).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, request } from "@/api/client";
import { appKeys } from "@/api/apps";
import type { AppEnv } from "@/api/generated/httpapi";

export const envKey = (appID: string) => [...appKeys.app(appID), "env"] as const;

const envPath = (appID: string) => `/api/v1/apps/${encodeURIComponent(appID)}/env`;

/** An App's env vars, values included. An unknown App fails with a 404. */
export function useEnv(appID: string) {
  return useQuery({
    queryKey: envKey(appID),
    queryFn: () => request<AppEnv>(envPath(appID)),
    retry: (failures, err) => !(err instanceof ApiError && err.status < 500) && failures < 3,
  });
}

/**
 * Replaces an App's env vars with `text`. A bad line fails with an
 * ApiError on field "env" whose message starts "line N:". Resolves to the
 * env vars as stored (the pairs alone, sorted by name). It starts no
 * Deployment: they take effect on the next one.
 */
export function useSaveEnv(appID: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (text: string) =>
      request<AppEnv>(envPath(appID), { method: "PUT", json: { env: text } satisfies AppEnv }),
    onSuccess: (env) => {
      client.setQueryData(envKey(appID), env);
      // The App's env var names changed.
      void client.invalidateQueries({ queryKey: appKeys.app(appID), exact: true });
    },
  });
}
