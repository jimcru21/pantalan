// Creating an App, editing its settings and deleting it, as TanStack
// mutations. A
// refused one fails with an ApiError whose `field` names the setting it's
// about (see components/form.tsx for showing it beside that field).
import { type QueryClient, useMutation, useQueryClient } from "@tanstack/react-query";
import { request } from "@/api/client";
import { appKeys } from "@/api/apps";
import type { App, AppListItem, AppSettings, CreateAppRequest } from "@/api/generated/httpapi";

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
 * DELETE /api/v1/apps/{id}: removes the App, what it left on the Node and
 * its Deployments. Refused with a 409 while a Deployment of it runs. On
 * success the App is gone from the cached Apps list; the caller leaves
 * the App's page, then drops its queries with forgetApp.
 */
export function useDeleteApp(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () => request<void>(`/api/v1/apps/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      client.setQueryData<AppListItem[]>(appKeys.list, (apps) => apps?.filter((a) => a.id !== id));
      void client.invalidateQueries({ queryKey: appKeys.list, exact: true });
    },
  });
}

/** Drops every cached query of a deleted App (it, its Deployments, its env). */
export function forgetApp(client: QueryClient, id: string) {
  client.removeQueries({ queryKey: appKeys.app(id) });
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
