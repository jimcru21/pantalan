import { useNavigate, useParams } from "react-router";
import { useUpdateApp } from "@/api/app-mutations";
import { isNotFound, useApp } from "@/api/apps";
import { AppForm, valuesOf } from "@/apps/AppForm";
import { AppNotFound } from "@/apps/AppNotFound";
import { FormAlert } from "@/components/form";
import { Page } from "@/shell/Page";

/**
 * The edit form for an App's settings, starting from what it has now.
 * Saving starts no Deployment: the settings take effect on the next one,
 * but a new domain, which serves the live Deployment at once.
 */
export function EditAppPage() {
  const { appId = "" } = useParams();
  const app = useApp(appId);
  const update = useUpdateApp(appId);
  const navigate = useNavigate();
  const appPath = `/apps/${encodeURIComponent(appId)}`;

  if (isNotFound(app.error)) {
    return <AppNotFound appId={appId} />;
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
        note="Changes take effect on the next Deployment, but a new domain serves the live one at once. Saving doesn't deploy."
        onSubmit={(settings) => update.mutateAsync(settings).then(() => navigate(appPath))}
        error={update.error}
        pending={update.isPending}
      />
    </Page>
  );
}
