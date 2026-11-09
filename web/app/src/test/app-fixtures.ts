// Fixtures and stubbed endpoints for the Apps pages, typed by the
// generated API types.
import type { App, Deployment } from "@/api/generated/domain";
import type { AppListItem } from "@/api/generated/httpapi";
import { aDeployment, anApp } from "./fixtures";
import { json, type Api } from "./network";

/** A row of GET /api/v1/apps: anApp, live by default. */
export function anAppListItem(fields: Partial<AppListItem> = {}): AppListItem {
  return {
    ...anApp(),
    latest_deployment: aDeployment(),
    ...fields,
  };
}

/** GET /api/v1/apps answering items. */
export function appsListApi(items: AppListItem[] = []): Api {
  return { "GET /api/v1/apps": () => json<AppListItem[]>(items) };
}

/** What an App's page fetches: the App and its Deployments, newest first. */
export function appPageApi(app: App = anApp(), deployments: Deployment[] = [aDeployment()]): Api {
  return {
    [`GET /api/v1/apps/${app.id}`]: () => json<App>(app),
    [`GET /api/v1/apps/${app.id}/deployments`]: () => json<Deployment[]>(deployments),
  };
}

/** What an App's page fetches for an ID no App has: the API's 404s. */
export function unknownAppApi(id: string): Api {
  const notFound = () => json({ error: `app ${id}: not found` }, { status: 404 });
  return {
    [`GET /api/v1/apps/${id}`]: notFound,
    [`GET /api/v1/apps/${id}/deployments`]: notFound,
  };
}
