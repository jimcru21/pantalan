import { useRef, useState, type ComponentProps, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router";
import type { App } from "@/api/generated/domain";
import type { AppSettings } from "@/api/generated/httpapi";
import {
  buttonClass,
  errorsFor,
  Field,
  focusFieldOf,
  FormAlert,
  inputClass,
  type FieldControl,
} from "@/components/form";

/** The settings the form edits, by their API names. */
const fields = ["name", "repo_url", "branch", "build_type", "domain", "port"] as const;
type Values = Record<(typeof fields)[number], string>;

/** A new App's starting values: branch main, a Dockerfile build. */
export const newApp: Values = {
  name: "",
  repo_url: "",
  branch: "main",
  build_type: "dockerfile",
  domain: "",
  port: "",
};

/** An App's settings as the form's starting values. */
export function valuesOf(app: App): Values {
  return {
    name: app.name,
    repo_url: app.repo_url,
    branch: app.branch,
    build_type: app.build_type,
    domain: app.domain,
    port: String(app.port),
  };
}

/**
 * The create and edit form for an App's settings. `onSubmit` sends them;
 * when it fails, `error` is shown beside the field it names (or above the
 * form) and every value typed stays.
 */
export function AppForm({
  initial,
  submitLabel,
  cancelTo,
  note,
  onSubmit,
  error,
  pending,
}: {
  initial: Values;
  submitLabel: string;
  cancelTo: string;
  note?: ReactNode;
  onSubmit: (settings: AppSettings) => Promise<unknown>;
  error: Error | null;
  pending: boolean;
}) {
  const [values, setValues] = useState(initial);
  const form = useRef<HTMLFormElement>(null);
  const { byField, other } = errorsFor(error, fields);

  function submit(event: FormEvent) {
    event.preventDefault();
    onSubmit(settingsOf(values)).catch((err: unknown) => focusFieldOf(form.current, err));
  }

  // A Field's control: a text input bound to one setting.
  const text =
    (name: keyof Values, extra: ComponentProps<"input"> = {}) =>
    (c: FieldControl) => (
      <input
        {...c}
        {...extra}
        name={name}
        className={inputClass}
        autoCapitalize="none"
        spellCheck={false}
        value={values[name]}
        onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))}
      />
    );

  return (
    <form
      ref={form}
      noValidate
      onSubmit={submit}
      className="grid max-w-2xl gap-[18px] rounded-lg border bg-card p-5 shadow-xs"
    >
      {other && <FormAlert>{other}</FormAlert>}
      <Field label="Name" error={byField.name}>
        {text("name", { autoComplete: "off" })}
      </Field>
      <Field
        label="Repository"
        error={byField.repo_url}
        hint="The git URL to clone, e.g. https://github.com/you/app.git."
      >
        {text("repo_url", { inputMode: "url", autoComplete: "off" })}
      </Field>
      <div className="grid gap-[18px] sm:grid-cols-2">
        <Field label="Branch" error={byField.branch} hint="Pushes to it deploy the App.">
          {text("branch")}
        </Field>
        <Field label="Build type" error={byField.build_type}>
          {(c) => (
            <select
              {...c}
              name="build_type"
              className={inputClass}
              value={values.build_type}
              onChange={(e) => setValues((v) => ({ ...v, build_type: e.target.value }))}
            >
              <option value="dockerfile">Dockerfile</option>
              <option value="nixpacks">Nixpacks</option>
            </select>
          )}
        </Field>
      </div>
      <div className="grid gap-[18px] sm:grid-cols-2">
        <Field
          label="Domain"
          error={byField.domain}
          hint="A bare hostname whose DNS points at this Node."
        >
          {text("domain")}
        </Field>
        <Field
          label="Port"
          error={byField.port}
          hint="The port the App listens on in its container."
        >
          {text("port", { inputMode: "numeric" })}
        </Field>
      </div>
      {note && <p className="m-0 text-[13px] text-muted-foreground">{note}</p>}
      <div className="flex gap-2.5">
        <button type="submit" disabled={pending} className={buttonClass.primary}>
          {submitLabel}
        </button>
        <Link to={cancelTo} className={buttonClass.secondary}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

// The values as the API's settings. A port that isn't a number is sent as
// 0, which the API refuses with a message beside the field.
function settingsOf(values: Values): AppSettings {
  const port = Number(values.port.trim());
  return {
    name: values.name,
    repo_url: values.repo_url,
    branch: values.branch,
    build_type: values.build_type,
    domain: values.domain,
    port: values.port.trim() !== "" && Number.isFinite(port) ? port : 0,
  };
}
