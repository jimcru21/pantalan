// Form building blocks, and the field-error pattern: the API names the
// body field an error is about (ApiError.field, from ErrorResponse), the
// form shows the message beside that field, marks it invalid and focuses
// it, and keeps every value typed. An error that names no field the form
// has goes in a FormAlert above it.
import { useId, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { cn } from "@/lib/utils";

const control =
  "w-full rounded-md border bg-card px-[11px] py-[9px] outline-none focus:border-primary focus:ring-3 focus:ring-accent aria-invalid:border-destructive aria-invalid:focus:ring-destructive-soft";

/** Classes for a text <input> (or <select>) inside a Field. */
export const inputClass = control;

export const buttonClass = {
  primary:
    "inline-flex cursor-pointer items-center justify-center rounded-md bg-primary px-3.5 py-2 font-semibold text-primary-foreground no-underline disabled:cursor-default disabled:opacity-70",
  secondary:
    "inline-flex cursor-pointer items-center justify-center rounded-md border bg-card px-3.5 py-2 font-medium text-foreground no-underline hover:bg-secondary",
  small:
    "inline-flex cursor-pointer items-center justify-center rounded-md border bg-card px-2.5 py-1 text-[13px] font-medium text-foreground no-underline hover:bg-secondary",
};

/** What a Field hands its control: spread it onto the <input> or <select>. */
export type FieldControl = {
  id: string;
  "aria-invalid"?: true;
  "aria-describedby"?: string;
};

/**
 * A labelled control with an optional hint and error message beside it.
 * The control is a render prop so it gets the ids that tie the label, the
 * hint and the error to it:
 *
 *   <Field label="Domain" error={errors.domain}>
 *     {(c) => <input {...c} name="domain" className={inputClass} ... />}
 *   </Field>
 */
export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: ReactNode;
  error?: string;
  children: (control: FieldControl) => ReactNode;
}) {
  const id = useId();
  const errorId = `${id}-error`;
  const hintId = `${id}-hint`;
  const describedBy = [error && errorId, hint && hintId].filter(Boolean).join(" ");
  return (
    <div className="grid content-start gap-1.5">
      <label htmlFor={id} className="text-[13px] font-medium">
        {label}
      </label>
      {children({
        id,
        "aria-invalid": error ? true : undefined,
        "aria-describedby": describedBy || undefined,
      })}
      {error && (
        <p id={errorId} className="m-0 text-[13px] text-destructive">
          {error}
        </p>
      )}
      {hint && (
        <p id={hintId} className="m-0 text-xs text-muted-foreground">
          {hint}
        </p>
      )}
    </div>
  );
}

/** A refusal that no one field explains, shown above the form. */
export function FormAlert({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      role="alert"
      className={cn(
        "rounded-md bg-destructive-soft px-3 py-2.5 text-[13px] text-destructive",
        className,
      )}
    >
      {children}
    </div>
  );
}

/**
 * Splits a failed submit's error between the form's fields: `byField`
 * holds the message of an error about one of `fields`, `other` the message
 * of any other error.
 */
export function errorsFor<F extends string>(
  error: unknown,
  fields: readonly F[],
): { byField: Partial<Record<F, string>>; other?: string } {
  if (!error) return { byField: {} };
  const field = fieldOf(error, fields);
  if (field)
    return { byField: { [field]: (error as Error).message } as Partial<Record<F, string>> };
  return { byField: {}, other: error instanceof Error ? error.message : String(error) };
}

/** Focuses the control (by its `name`) that a failed submit's error is about. */
export function focusFieldOf(form: HTMLFormElement | null, error: unknown): void {
  if (!(error instanceof ApiError) || !error.field || !form) return;
  const control = form.elements.namedItem(error.field);
  if (control instanceof HTMLElement) control.focus();
}

function fieldOf<F extends string>(error: unknown, fields: readonly F[]): F | undefined {
  if (!(error instanceof ApiError) || !error.field) return undefined;
  return fields.find((f) => f === error.field);
}
