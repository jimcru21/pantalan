import { Link } from "react-router";
import { useApps } from "@/api/apps";
import { DeploymentStatus } from "@/apps/DeploymentStatus";
import { buttonClass } from "@/components/form";
import { Alert, Table, Td } from "@/components/panel";
import { Page } from "@/shell/Page";

/**
 * The Apps list: every App with its domain, branch and the status of its
 * latest Deployment, refreshed every 5 s.
 */
export function AppsPage() {
  const { data, error } = useApps();

  return (
    <Page
      title="Apps"
      actions={
        <Link to="/apps/new" className={buttonClass.primary}>
          New App
        </Link>
      }
    >
      {error && !data && (
        <div className="rounded-lg border bg-card shadow-xs">
          <Alert>{error.message}</Alert>
        </div>
      )}
      {data && (
        <Table
          label="Apps"
          head={["Name", "Domain", "Branch", "Status"]}
          empty={data.length === 0 && "No Apps yet."}
        >
          {data.map((app) => (
            <tr key={app.id}>
              <Td>
                <Link to={`/apps/${encodeURIComponent(app.id)}`} className="font-semibold">
                  {app.name}
                </Link>
              </Td>
              <Td mono>{app.domain}</Td>
              <Td mono>{app.branch}</Td>
              <Td>
                <DeploymentStatus status={app.latest_deployment?.status} />
              </Td>
            </tr>
          ))}
        </Table>
      )}
    </Page>
  );
}
