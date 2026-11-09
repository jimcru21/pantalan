import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import type { App, Deployment } from "@/api/generated/domain";
import { isNotFound, useApp, useDeployments } from "@/api/apps";
import { DeploymentStatus } from "@/apps/DeploymentStatus";
import { buttonClass } from "@/components/form";
import { Alert, Facts, Panel, Table, Td } from "@/components/panel";
import { formatUTCDateTime } from "@/lib/format";
import { NotFoundPage, Page } from "@/shell/Page";

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
    return (
      <NotFoundPage title="App not found" back={{ to: "/apps", label: "Back to Apps" }}>
        No App has the ID <code className="font-mono text-[0.92em]">{appId}</code>. It may have been
        deleted.
      </NotFoundPage>
    );
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
          <Link to={`/apps/${encodeURIComponent(appId)}/edit`} className={buttonClass.small}>
            Edit
          </Link>
        }
      >
        <Facts facts={settings(app.data)} />
      </Panel>
      {deployments.error ? (
        <ErrorCard error={deployments.error} />
      ) : (
        deployments.data && (
          <>
            <LiveDeployment deployment={deployments.data.find((d) => d.status === "live")} />
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

// The Deployment serving the App's domain now, if any.
function LiveDeployment({ deployment }: { deployment?: Deployment }) {
  return (
    <Panel title="Live Deployment">
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
        head={["Status", "Ref", "Commit", "Started (UTC)", "Finished (UTC)", "Error"]}
        empty={deployments.length === 0 && "Not deployed yet."}
      >
        {deployments.map((d) => (
          <tr key={d.id}>
            <Td>
              <DeploymentStatus status={d.status} />
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
