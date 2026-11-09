import { useId, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { forgetApp, useDeleteApp } from "@/api/app-settings";
import type { App } from "@/api/generated/httpapi";
import { buttonClass, Field, FormAlert, inputClass } from "@/components/form";
import { cn } from "@/lib/utils";

/**
 * The App page's Delete: a modal dialog that says what deleting removes
 * and makes the Admin type the App's domain, exactly, before its button
 * works. Success lands on the Apps list. A refusal (a Deployment of the
 * App still running, say) shows in the dialog, which stays open.
 *
 * It is a native <dialog> opened with showModal(): the browser keeps
 * focus inside it, makes the page behind it inert, closes it on Escape
 * and gives it the dialog role, and it injects no <style>, which the
 * CSP would block (Radix Dialog does).
 */
export function DeleteAppButton({ app, deployments }: { app: App; deployments?: number }) {
  const del = useDeleteApp(app.id);
  const client = useQueryClient();
  const navigate = useNavigate();
  const dialog = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  const confirm = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState("");
  const titleId = useId();
  const descriptionId = useId();
  const matches = typed === app.domain;

  const open = () => {
    setTyped("");
    del.reset();
    dialog.current?.showModal();
    confirm.current?.focus();
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!matches || del.isPending) return;
    del.mutate(undefined, {
      onSuccess: () => {
        dialog.current?.close();
        void navigate("/apps", { replace: true });
        forgetApp(client, app.id);
      },
    });
  };

  return (
    <>
      <button ref={opener} type="button" onClick={open} className={buttonClass.small}>
        Delete
      </button>
      <dialog
        ref={dialog}
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        onClose={() => opener.current?.focus()}
        className="m-auto w-[min(32rem,calc(100vw-2rem))] rounded-lg border bg-card p-0 text-foreground shadow-lg backdrop:bg-black/40"
      >
        <form onSubmit={submit} className="grid gap-4 p-5">
          <h2 id={titleId} className="m-0 text-base font-semibold">
            Delete {app.name}?
          </h2>
          <div id={descriptionId} className="grid gap-2 text-[13px]">
            <p className="m-0">Deleting the App removes, from this Node:</p>
            <ul className="m-0 grid list-disc gap-1 pl-5">
              <li>its container, so the App stops running,</li>
              <li>its images,</li>
              <li>
                its route, so <span className="font-mono">{app.domain}</span> is no longer served,
              </li>
              <li>its Deployment Logs,</li>
              <li>
                its Deployments
                {deployments !== undefined && ` (${deployments} in its history)`}, and its settings
                and env vars.
              </li>
            </ul>
            <p className="m-0 font-medium">This can't be undone.</p>
          </div>
          {del.error && (
            <FormAlert>
              Couldn't delete {app.name}: {del.error.message}
            </FormAlert>
          )}
          <Field label={`Type ${app.domain} to confirm`}>
            {(c) => (
              <input
                {...c}
                ref={confirm}
                name="confirm"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                className={`${inputClass} font-mono`}
              />
            )}
          </Field>
          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={() => dialog.current?.close()}
              className={buttonClass.secondary}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!matches || del.isPending}
              className={cn(buttonClass.primary, "bg-destructive")}
            >
              {del.isPending ? "Deleting…" : "Delete App"}
            </button>
          </div>
        </form>
      </dialog>
    </>
  );
}
