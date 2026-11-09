import { useNavigate } from "react-router";
import { useCreateApp } from "@/api/app-mutations";
import { AppForm, newApp } from "@/apps/AppForm";
import { Page } from "@/shell/Page";

/** The create form. A created App opens on its own page. */
export function NewAppPage() {
  const create = useCreateApp();
  const navigate = useNavigate();

  return (
    <Page title="New App" up={{ to: "/apps", label: "All Apps" }}>
      <AppForm
        initial={newApp}
        submitLabel="Create App"
        cancelTo="/apps"
        onSubmit={(settings) =>
          create
            .mutateAsync(settings)
            .then((app) => navigate(`/apps/${encodeURIComponent(app.id)}`))
        }
        error={create.error}
        pending={create.isPending}
      />
    </Page>
  );
}
