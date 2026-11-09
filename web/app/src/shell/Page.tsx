import type { ReactNode } from "react";
import { Link } from "react-router";

/**
 * A page's frame inside the shell: its heading, then its content. `up`
 * links to the page above this one (e.g. the Apps list from an App);
 * `actions` sit at the heading's end (e.g. a "New App" link).
 */
export function Page({
  title,
  up,
  actions,
  children,
}: {
  title: string;
  up?: { to: string; label: string };
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <main className="grid min-w-0 content-start gap-5 p-6">
      <div className="grid gap-1">
        {up && (
          <Link to={up.to} className="text-[13px] text-muted-foreground hover:text-foreground">
            <span aria-hidden>← </span>
            {up.label}
          </Link>
        )}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-xl font-semibold">{title}</h1>
          {actions && <div className="flex gap-2">{actions}</div>}
        </div>
      </div>
      {children}
    </main>
  );
}

/** The page for an address that names nothing: a heading, why, and a way back. */
export function NotFoundPage({
  title,
  back,
  children,
}: {
  title: string;
  back: { to: string; label: string };
  children: ReactNode;
}) {
  return (
    <Page title={title}>
      <div className="grid gap-2 rounded-lg border bg-card px-4 py-8 text-center shadow-xs">
        <p className="m-0 text-muted-foreground">{children}</p>
        <p className="m-0">
          <Link to={back.to} className="font-medium">
            {back.label}
          </Link>
        </p>
      </div>
    </Page>
  );
}
