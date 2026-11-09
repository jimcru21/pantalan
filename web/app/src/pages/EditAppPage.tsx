import { useNavigate, useParams } from "react-router";
import { useUpdateApp } from "@/api/app-settings";
import { isNotFound, useApp } from "@/api/apps";
import { AppForm, valuesOf } from "@/apps/AppForm";
import { FormAlert } from "@/components/form";
import { NotFoundPage, Page } from "@/shell/Page";

/**
 * The edit form for an App's settings, starting from what it has now.
 * Saving starts no Deployment: the settings take effect on the next one.
 */
export function EditAppPage() {
  const { appId = "" } = useParams();
  const app = useApp(appId);
  const update = useUpdateApp(appId);
  const navigate = useNavigate();
  const appPath = `/apps/${encodeURIComponent(appId)}`;

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
      <Page title="Edit App" up={{ to: appPath, label: "Back to the App" }}>
        {app.error && <FormAlert className="max-w-2xl">{app.error.message}</FormAlert>}
      </Page>
    );
  }

  return (
    <Page title={`Edit ${app.data.name}`} up={{ to: appPath, label: app.data.name }}>
      <AppForm
        key={app.data.id}
        initial={valuesOf(app.data)}
        submitLabel="Save"
        cancelTo={appPath}
        note="Changes take effect on the next Deployment. Saving doesn't deploy."
        onSubmit={(settings) => update.mutateAsync(settings).then(() => navigate(appPath))}
        error={update.error}
        pending={update.isPending}
      />
    </Page>
  );
}
