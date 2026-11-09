// Creating an App and editing its settings, as TanStack mutations. A
// refused one fails with an ApiError whose `field` names the setting it's
// about (see components/form.tsx for showing it beside that field).
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { request } from "@/api/client";
import { appKeys } from "@/api/apps";
import type { App, AppSettings, CreateAppRequest } from "@/api/generated/httpapi";

/** POST /api/v1/apps. Resolves to the new App, already in the cache. */
export function useCreateApp() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (req: CreateAppRequest) =>
      request<App>("/api/v1/apps", { method: "POST", json: req }),
    onSuccess: (app) => {
      client.setQueryData(appKeys.app(app.id), app);
      void client.invalidateQueries({ queryKey: appKeys.list, exact: true });
    },
  });
}

/**
 * PUT /api/v1/apps/{id}: replaces the App's settings (never its env vars)
 * and resolves to the App, already in the cache. It starts no Deployment.
 */
export function useUpdateApp(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (settings: AppSettings) =>
      request<App>(`/api/v1/apps/${encodeURIComponent(id)}`, { method: "PUT", json: settings }),
    onSuccess: (app) => {
      client.setQueryData(appKeys.app(app.id), app);
      void client.invalidateQueries({ queryKey: appKeys.list, exact: true });
    },
  });
}
