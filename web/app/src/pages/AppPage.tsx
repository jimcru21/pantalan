import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import type { Deployment } from "@/api/generated/domain";
import type { App } from "@/api/generated/httpapi";
import { isNotFound, useApp, useDeployments } from "@/api/apps";
import { AppLog } from "@/apps/AppLog";
import { AppNotFound } from "@/apps/AppNotFound";
import { DeleteAppButton } from "@/apps/DeleteAppButton";
import { DeployButton } from "@/apps/DeployButton";
import { DeploymentStatus } from "@/apps/DeploymentStatus";
import { DeploymentLink, Queue } from "@/apps/Queue";
import { buttonClass } from "@/components/form";
import { Alert, Facts, Panel, Table, Td } from "@/components/panel";
import { formatUTCDateTime } from "@/lib/format";
import { Page } from "@/shell/Page";

const toList = { to: "/apps", label: "All Apps" };

/**
 * An App's page, addressed by its ID (names aren't unique): its settings,
 * its live Deployment and its Deployment history, newest first.
 */
export function AppPage() {
  const { appId = "" } = useParams();
  const app = useApp(appId);
  const deployments = useDeployments(appId);

  if (isNotFound(app.error)) {
    return <AppNotFound appId={appId} />;
  }
  if (!app.data) {
    return (
      <Page title="App" up={toList}>
        {app.error && <ErrorCard error={app.error} />}
      </Page>
    );
  }

  return (
    <Page title={app.data.name} up={toList}>
      <Panel
        title="Settings"
        action={
          <span className="flex items-center gap-2">
            <Link to={`/apps/${encodeURIComponent(appId)}/edit`} className={buttonClass.small}>
              Edit
            </Link>
            <DeleteAppButton app={app.data} deployments={deployments.data?.length} />
          </span>
        }
      >
        <Facts facts={settings(app.data)} />
      </Panel>
      <EnvVars appId={appId} names={app.data.env_names} />
      {deployments.error ? (
        <ErrorCard error={deployments.error} />
      ) : (
        deployments.data && (
          <>
            <LiveDeployment
              deployment={deployments.data.find((d) => d.status === "live")}
              action={<DeployButton appId={appId} />}
            />
            <AppLog appId={appId} liveID={deployments.data.find((d) => d.status === "live")?.id} />
            <Queue deployments={deployments.data} />
            <History deployments={deployments.data} />
          </>
        )
      )}
    </Page>
  );
}

function settings(app: App): [string, ReactNode][] {
  return [
    ["Repository", <Mono>{app.repo_url}</Mono>],
    ["Branch", <Mono>{app.branch}</Mono>],
    ["Build type", app.build_type],
    [
      "Domain",
      <a href={`https://${app.domain}`} target="_blank" rel="noreferrer">
        {app.domain}
      </a>,
    ],
    ["Port", <Mono>{app.port}</Mono>],
    ["Created", `${formatUTCDateTime(app.created_at)} UTC`],
  ];
}

// The names of the App's env vars. Their values may be secrets, so they
// show only on the env editor.
function EnvVars({ appId, names }: { appId: string; names: string[] }) {
  return (
    <Panel
      title="Env vars"
      action={
        <Link to={`/apps/${encodeURIComponent(appId)}/env`} className={buttonClass.small}>
          Edit env vars
        </Link>
      }
    >
      {names.length > 0 ? (
        <ul className="m-0 flex list-none flex-wrap gap-2 px-4 py-3">
          {names.map((name) => (
            <li key={name} className="rounded-md bg-secondary px-2 py-0.5 font-mono text-[0.92em]">
              {name}
            </li>
          ))}
        </ul>
      ) : (
        <p className="m-0 px-4 py-3 text-muted-foreground">No env vars.</p>
      )}
    </Panel>
  );
}

// The Deployment serving the App's domain now, if any.
function LiveDeployment({ deployment, action }: { deployment?: Deployment; action: ReactNode }) {
  return (
    <Panel title="Live Deployment" action={action}>
      {deployment ? (
        <Facts
          facts={[
            ["Commit", <Commit sha={deployment.commit} />],
            ["Ref", <Mono>{deployment.ref}</Mono>],
            ["Image", <Mono>{deployment.image}</Mono>],
            [
              "Live since",
              deployment.finished_at ? `${formatUTCDateTime(deployment.finished_at)} UTC` : "–",
            ],
          ]}
        />
      ) : (
        <p className="m-0 px-4 py-3 text-muted-foreground">
          No live Deployment: nothing is serving this App's domain.
        </p>
      )}
    </Panel>
  );
}

function History({ deployments }: { deployments: Deployment[] }) {
  return (
    <Panel title="Deployments">
      <Table
        bare
        head={["Status", "Deployment", "Ref", "Commit", "Started (UTC)", "Finished (UTC)", "Error"]}
        empty={deployments.length === 0 && "Not deployed yet."}
      >
        {deployments.map((d) => (
          <tr key={d.id}>
            <Td>
              <DeploymentStatus status={d.status} />
            </Td>
            <Td mono>
              <DeploymentLink deployment={d} />
            </Td>
            <Td mono>{d.ref}</Td>
            <Td>
              <Commit sha={d.commit} />
            </Td>
            <Td>{formatUTCDateTime(d.created_at)}</Td>
            <Td>{d.finished_at ? formatUTCDateTime(d.finished_at) : "–"}</Td>
            <Td bad>
              <span className="whitespace-normal">{d.error}</span>
            </Td>
          </tr>
        ))}
      </Table>
    </Panel>
  );
}

// A commit as its first 7 characters, the full SHA on hover. Empty until
// the Deployment has fetched its ref.
function Commit({ sha }: { sha: string }) {
  if (!sha) return <span className="text-muted-foreground">–</span>;
  return (
    <span title={sha} className="font-mono text-[0.92em]">
      {sha.slice(0, 7)}
    </span>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-[0.92em]">{children}</span>;
}

function ErrorCard({ error }: { error: Error }) {
  return (
    <div className="rounded-lg border bg-card shadow-xs">
      <Alert>{error.message}</Alert>
    </div>
  );
}
