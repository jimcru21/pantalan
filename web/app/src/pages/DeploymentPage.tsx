import { useEffect, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { App, Deployment } from "@/api/generated/domain";
import { DeploymentLogDeleted } from "@/api/generated/httpapi";
import { appKeys, isInProgress, isNotFound, useApp, useDeployments } from "@/api/apps";
import { useDeployment } from "@/api/deployments";
import { useLogStream } from "@/api/streams";
import { DeploymentStatus } from "@/apps/DeploymentStatus";
import { RedeployButton } from "@/apps/RedeployButton";
import { Alert, Facts, Panel } from "@/components/panel";
import { formatDuration, formatUTCDateTime } from "@/lib/format";
import { LogView } from "@/logs/LogView";
import { NotFoundPage, Page } from "@/shell/Page";

/**
 * A Deployment's page, nested under its App: what was deployed (ref,
 * commit, image), when, and how it ended. Its status moves on screen while
 * it's on its way. A failed Deployment says whether the App's live
 * Deployment is still serving.
 */
export function DeploymentPage() {
  const { appId = "", deploymentId = "" } = useParams();
  const app = useApp(appId);
  const deployment = useDeployment(appId, deploymentId);
  const deployments = useDeployments(appId);
  const finished = deployment.data !== undefined && !isInProgress(deployment.data.status);

  // Once it's final the App's other Deployments may have moved too (the
  // one it replaced is superseded): read them again.
  const client = useQueryClient();
  useEffect(() => {
    if (finished)
      void client.invalidateQueries({ queryKey: appKeys.deployments(appId), exact: true });
  }, [finished, client, appId]);

  const up = { to: `/apps/${encodeURIComponent(appId)}`, label: app.data?.name ?? "App" };
  if (isNotFound(deployment.error)) {
    return (
      <NotFoundPage
        title="Deployment not found"
        back={
          isNotFound(app.error)
            ? { to: "/apps", label: "Back to Apps" }
            : { ...up, label: "Back to the App" }
        }
      >
        No Deployment of this App has the ID{" "}
        <code className="font-mono text-[0.92em]">{deploymentId}</code>.
      </NotFoundPage>
    );
  }

  const title = `Deployment ${deploymentId}`;
  if (!deployment.data) {
    return (
      <Page title={title} up={up}>
        {deployment.error && (
          <div className="rounded-lg border bg-card shadow-xs">
            <Alert>{deployment.error.message}</Alert>
          </div>
        )}
      </Page>
    );
  }

  const live = deployments.data?.find((d) => d.status === "live");
  return (
    <Page title={title} up={up}>
      <Panel
        title="Deployment"
        action={
          // Only a resolved commit can be deployed again.
          deployment.data.commit && <RedeployButton appID={appId} deploymentID={deploymentId} />
        }
      >
        <Facts facts={facts(deployment.data)} />
        {deployment.data.status === "failed" && live && app.data && (
          <StillServing live={live} app={app.data} />
        )}
      </Panel>
      <DeploymentLog appID={appId} deploymentID={deploymentId} />
    </Page>
  );
}

// The Deployment's own output, streamed live while it runs. The stream
// keeps going while the Admin is on another page, until it's done.
function DeploymentLog({ appID, deploymentID }: { appID: string; deploymentID: string }) {
  const log = `/api/v1/apps/${encodeURIComponent(appID)}/deployments/${encodeURIComponent(deploymentID)}/log`;
  const stream = useLogStream(`${log}/stream`, { keepRunning: true });
  return (
    <Panel
      title="Deployment Log"
      action={
        <a href={log} target="_blank" rel="noreferrer" className="text-[13px]">
          Full log as plain text
        </a>
      }
    >
      <LogView
        stream={stream}
        label="Deployment Log"
        end={
          stream.done === DeploymentLogDeleted
            ? "The Deployment was deleted with its App."
            : undefined
        }
      />
    </Panel>
  );
}

function facts(d: Deployment): [string, ReactNode][] {
  const rows: [string, ReactNode][] = [
    ["Status", <DeploymentStatus status={d.status} />],
    ["Ref", <Mono>{d.ref}</Mono>],
    ["Commit", d.commit ? <Mono>{d.commit}</Mono> : <Pending />],
    ["Image", d.image ? <Mono>{d.image}</Mono> : <Pending />],
    ["Queued", utc(d.created_at)],
    ["Finished", d.finished_at ? utc(d.finished_at) : <Pending />],
  ];
  if (d.finished_at) {
    const seconds = (Date.parse(d.finished_at) - Date.parse(d.created_at)) / 1000;
    rows.push(["Took", formatDuration(seconds)]);
  }
  if (d.error) rows.push(["Error", <span className="text-destructive">{d.error}</span>]);
  return rows;
}

// A failed Deployment never takes the App down: the live one keeps serving.
function StillServing({ live, app }: { live: Deployment; app: App }) {
  return (
    <p className="m-0 border-t px-4 py-3">
      The App is still up: Deployment{" "}
      <Link to={`/apps/${encodeURIComponent(app.id)}/deployments/${encodeURIComponent(live.id)}`}>
        {live.id}
      </Link>{" "}
      ({live.ref}
      {live.commit && <> at {live.commit.slice(0, 7)}</>}) is still live and serving {app.domain}.
    </p>
  );
}

function utc(rfc3339: string) {
  return `${formatUTCDateTime(rfc3339)} UTC`;
}

function Pending() {
  return <span className="text-muted-foreground">–</span>;
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-[0.92em]">{children}</span>;
}
