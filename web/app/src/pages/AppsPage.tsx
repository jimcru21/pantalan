import { useQuery } from "@tanstack/react-query";
import type { App } from "@/api/generated/domain";
import { request } from "@/api/client";
import { Page } from "@/shell/Page";

/** The Apps list: for now only their names, until the Apps pages land. */
export function AppsPage() {
  const apps = useQuery({
    queryKey: ["apps"],
    queryFn: () => request<App[]>("/api/v1/apps"),
  });

  return (
    <Page title="Apps">
      {apps.data && (
        <ul className="grid gap-1">
          {apps.data.map((app) => (
            <li key={app.id}>{app.name}</li>
          ))}
        </ul>
      )}
    </Page>
  );
}
