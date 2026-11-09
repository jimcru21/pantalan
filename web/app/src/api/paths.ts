// Every address under /api/v1/apps the SPA calls, built in one place with
// its IDs escaped. Pages and components take them from here, never
// building one themselves.

const apps = "/api/v1/apps";
const app = (appId: string) => `${apps}/${encodeURIComponent(appId)}`;
const deployments = (appId: string) => `${app(appId)}/deployments`;
const deployment = (appId: string, deploymentId: string) =>
  `${deployments(appId)}/${encodeURIComponent(deploymentId)}`;
const deploymentLog = (appId: string, deploymentId: string) =>
  `${deployment(appId, deploymentId)}/log`;

export const appPaths = {
  /** GET the Apps list, POST a new App. */
  apps,
  /** GET, PUT (settings) and DELETE one App. */
  app,
  /** GET and PUT the App's env vars as dotenv text. */
  env: (appId: string) => `${app(appId)}/env`,
  /** GET the App's Deployments, POST to deploy. */
  deployments,
  /** GET one Deployment of the App. */
  deployment,
  /** POST: a new Deployment of that Deployment's commit. */
  redeploy: (appId: string, deploymentId: string) => `${deployment(appId, deploymentId)}/redeploy`,
  /** The whole Deployment Log as plain text. */
  deploymentLog,
  /** The Deployment Log as Server-Sent Events. */
  deploymentLogStream: (appId: string, deploymentId: string) =>
    `${deploymentLog(appId, deploymentId)}/stream`,
  /** The App Log as Server-Sent Events. */
  appLogStream: (appId: string) => `${app(appId)}/log/stream`,
};
