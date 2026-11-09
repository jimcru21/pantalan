// A stubbed server for one App's Deployments: deploy, list, read one, and
// a hand on each Deployment's status so a test can move it along.
import type { Deployment, DeploymentStatus } from "@/api/generated/domain";
import type { App } from "@/api/generated/httpapi";
import { anApp } from "./fixtures";
import { json, type Api } from "./network";

export type DeploymentsServer = {
  /** The handlers: GET the App, GET/POST its Deployments, GET and redeploy one Deployment. */
  api: Api;
  /** The App's Deployments, oldest first (the order they were deployed). */
  deployments: Deployment[];
  /** Moves a Deployment on, as the Deployer would. */
  set(id: string, fields: Partial<Deployment> & { status: DeploymentStatus }): void;
  /**
   * Holds the answer to the next deploy until the returned function is
   * called (the Deployment is recorded at once, as the server does).
   */
  holdNextDeploy(): () => void;
};

/**
 * The Deployments of `app`, starting with `existing` (oldest first). A
 * deploy records a queued Deployment `dep-<n>` of the ref asked for, or
 * the App's branch.
 */
export function deploymentsServer(
  app: App = anApp(),
  existing: Deployment[] = [],
): DeploymentsServer {
  const deployments = [...existing];
  const base = `/api/v1/apps/${app.id}`;
  const held: Promise<void>[] = [];
  let deploys = 0;
  const api: Api = {
    [`GET ${base}`]: () => json<App>(app),
    [`GET ${base}/deployments`]: () => json<Deployment[]>([...deployments].reverse()),
    [`POST ${base}/deployments`]: async (req) => {
      const text = await req.text();
      const ref = (text && (JSON.parse(text) as { ref?: string }).ref) || app.branch;
      return deploy(ref);
    },
  };
  for (const dep of existing) addRead(dep.id);

  async function deploy(ref: string) {
    deploys++;
    const dep: Deployment = {
      id: `dep-${deploys}`,
      app_id: app.id,
      ref,
      commit: "",
      image: "",
      status: "queued",
      created_at: new Date(Date.UTC(2026, 10, 9, 12, 0, deploys)).toISOString(),
    };
    deployments.push(dep);
    addRead(dep.id);
    await held.shift();
    return json<Deployment>(dep, { status: 202 });
  }

  // Reading a Deployment, and redeploying its commit (refused, as the
  // server does, when it never resolved one).
  function addRead(id: string) {
    const find = () => deployments.find((d) => d.id === id);
    api[`GET ${base}/deployments/${id}`] = () => {
      const dep = find();
      return dep ? json<Deployment>(dep) : json({ error: "not found" }, { status: 404 });
    };
    api[`POST ${base}/deployments/${id}/redeploy`] = () => {
      const dep = find();
      if (!dep) return json({ error: "not found" }, { status: 404 });
      if (!dep.commit)
        return json(
          {
            error: `conflict: Deployment ${id} never resolved a commit, so there is no commit to redeploy`,
          },
          { status: 409 },
        );
      return deploy(dep.commit);
    };
  }

  return {
    api,
    deployments,
    set(id, fields) {
      const i = deployments.findIndex((d) => d.id === id);
      const dep = deployments[i];
      if (!dep) throw new Error(`no Deployment ${id}`);
      deployments[i] = { ...dep, ...fields };
    },
    holdNextDeploy() {
      let release = () => {};
      held.push(new Promise<void>((resolve) => (release = resolve)));
      return release;
    },
  };
}

/** What a Deployment's page gets for an ID the App has no Deployment by: the API's 404. */
export function unknownDeploymentApi(appID: string, id: string): Api {
  return {
    [`GET /api/v1/apps/${appID}/deployments/${id}`]: () =>
      json({ error: `deployment ${id} of app ${appID}: not found` }, { status: 404 }),
  };
}
