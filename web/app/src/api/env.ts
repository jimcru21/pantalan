// An App's env vars as dotenv text (GET and PUT /api/v1/apps/{id}/env),
// the only place their values come from: App responses carry the names
// alone (env_names).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { request } from "@/api/client";
import { appKeys, retryUnlessAnswered } from "@/api/apps";
import type { AppEnv } from "@/api/generated/httpapi";
import { appPaths } from "@/api/paths";

export const envKey = (appId: string) => [...appKeys.app(appId), "env"] as const;

/** An App's env vars, values included. An unknown App fails with a 404. */
export function useEnv(appId: string) {
  return useQuery({
    queryKey: envKey(appId),
    queryFn: () => request<AppEnv>(appPaths.env(appId)),
    retry: retryUnlessAnswered,
  });
}

/**
 * Replaces an App's env vars with `text`. A bad line fails with an
 * ApiError on field "env" whose message starts "line N:". Resolves to the
 * env vars as stored (the pairs alone, sorted by name). It starts no
 * Deployment: they take effect on the next one.
 */
export function useSaveEnv(appId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (text: string) =>
      request<AppEnv>(appPaths.env(appId), { method: "PUT", json: { env: text } satisfies AppEnv }),
    onSuccess: (env) => {
      client.setQueryData(envKey(appId), env);
      // The App's env var names changed.
      void client.invalidateQueries({ queryKey: appKeys.app(appId), exact: true });
    },
  });
}
