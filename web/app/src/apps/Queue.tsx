import { Link } from "react-router";
import { isInProgress } from "@/api/apps";
import type { Deployment } from "@/api/generated/domain";
import { DeploymentStatus } from "@/apps/DeploymentStatus";
import { Panel, Table, Td } from "@/components/panel";
import { formatUTCDateTime } from "@/lib/format";

/**
 * An App's Deployments still on their way, in the order the server runs
 * them (oldest first): one at a time per App, so two quick deploys wait in
 * turn rather than race.
 */
export function Queue({ deployments }: { deployments: Deployment[] }) {
  const queue = deployments.filter((d) => isInProgress(d.status)).reverse();
  return (
    <Panel title="Queue">
      <Table
        bare
        head={["#", "Deployment", "Ref", "Status", "Queued (UTC)"]}
        empty={queue.length === 0 && "Nothing queued."}
      >
        {queue.map((d, i) => (
          <tr key={d.id}>
            <Td num>{i + 1}</Td>
            <Td mono>
              <DeploymentLink deployment={d} />
            </Td>
            <Td mono>{d.ref}</Td>
            <Td>
              <DeploymentStatus status={d.status} />
            </Td>
            <Td>{formatUTCDateTime(d.created_at)}</Td>
          </tr>
        ))}
      </Table>
    </Panel>
  );
}

/** A link to a Deployment's page, named by its ID. */
export function DeploymentLink({ deployment }: { deployment: Deployment }) {
  return (
    <Link
      to={`/apps/${encodeURIComponent(deployment.app_id)}/deployments/${encodeURIComponent(deployment.id)}`}
    >
      {deployment.id}
    </Link>
  );
}
