import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { isNotFound, useApp } from "@/api/apps";
import { useEnv, useSaveEnv } from "@/api/env";
import { DeployButton } from "@/apps/DeployButton";
import {
  buttonClass,
  errorsFor,
  Field,
  focusFieldOf,
  FormAlert,
  inputClass,
} from "@/components/form";
import { NotFoundPage, Page } from "@/shell/Page";

/**
 * The env editor: an App's env vars as one block of KEY=value lines, the
 * only page that shows their values. Saving starts no Deployment; it says
 * so and offers one right there.
 */
export function EnvPage() {
  const { appId = "" } = useParams();
  const app = useApp(appId);
  const env = useEnv(appId);
  const appPath = `/apps/${encodeURIComponent(appId)}`;

  if (isNotFound(app.error) || isNotFound(env.error)) {
    return (
      <NotFoundPage title="App not found" back={{ to: "/apps", label: "Back to Apps" }}>
        No App has the ID <code className="font-mono text-[0.92em]">{appId}</code>. It may have been
        deleted.
      </NotFoundPage>
    );
  }
  const error = app.error ?? env.error;
  if (!app.data || !env.data) {
    return (
      <Page title="Env vars" up={{ to: appPath, label: "Back to the App" }}>
        {error && <FormAlert className="max-w-2xl">{error.message}</FormAlert>}
      </Page>
    );
  }

  return (
    <Page title={`Env vars of ${app.data.name}`} up={{ to: appPath, label: app.data.name }}>
      <EnvEditor appID={appId} initial={env.data.env} cancelTo={appPath} />
    </Page>
  );
}

function EnvEditor({
  appID,
  initial,
  cancelTo,
}: {
  appID: string;
  initial: string;
  cancelTo: string;
}) {
  const [text, setText] = useState(initial);
  const [saved, setSaved] = useState(false);
  const save = useSaveEnv(appID);
  const navigate = useNavigate();
  const form = useRef<HTMLFormElement>(null);
  const { byField, other } = errorsFor(save.error, ["env"] as const);

  function submit(event: FormEvent) {
    event.preventDefault();
    setSaved(false);
    save
      .mutateAsync(text)
      .then((stored) => {
        setText(stored.env); // what the App holds now: comments aren't kept
        setSaved(true);
      })
      .catch((err: unknown) => focusFieldOf(form.current, err));
  }

  return (
    <form
      ref={form}
      noValidate
      onSubmit={submit}
      className="grid max-w-2xl gap-[18px] rounded-lg border bg-card p-5 shadow-xs"
    >
      {other && <FormAlert>{other}</FormAlert>}
      <Field
        label="Env vars"
        error={byField.env}
        hint="One KEY=value per line, as in a .env file. Blank lines and # comments are ignored."
      >
        {(c) => (
          <textarea
            {...c}
            name="env"
            rows={14}
            className={`${inputClass} font-mono text-[13px]`}
            autoCapitalize="none"
            autoComplete="off"
            spellCheck={false}
            wrap="off"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setSaved(false);
            }}
          />
        )}
      </Field>
      {saved && (
        <div
          role="status"
          className="flex flex-wrap items-center justify-between gap-3 rounded-md bg-ok-soft px-3 py-2.5 text-[13px]"
        >
          <span>Saved. The env vars take effect on the next Deployment.</span>
          <DeployButton
            appID={appID}
            onDeployed={(dep) =>
              void navigate(
                `/apps/${encodeURIComponent(appID)}/deployments/${encodeURIComponent(dep.id)}`,
              )
            }
          />
        </div>
      )}
      <div className="flex gap-2.5">
        <button type="submit" disabled={save.isPending} className={buttonClass.primary}>
          Save
        </button>
        <Link to={cancelTo} className={buttonClass.secondary}>
          Back to the App
        </Link>
      </div>
    </form>
  );
}
