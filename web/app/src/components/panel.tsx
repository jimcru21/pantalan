// The cards, tables and alerts the Dashboard's pages are built from.
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** An error message inside a card. */
export function Alert({ children }: { children: ReactNode }) {
  return (
    <p
      role="alert"
      className="mx-4 my-3 rounded-md bg-destructive-soft px-3 py-2.5 text-[13px] text-destructive"
    >
      {children}
    </p>
  );
}

/** A card with a titled header: a region named by its title. */
export function Panel({
  title,
  action,
  children,
}: {
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section
      aria-label={title}
      className="min-w-0 overflow-x-auto rounded-lg border bg-card shadow-xs"
    >
      <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
        <h2 className="m-0 text-sm font-semibold">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

/** Term and value pairs, e.g. an App's settings. */
export function Facts({ facts }: { facts: [string, ReactNode][] }) {
  return (
    <dl className="m-0 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-2 p-4">
      {facts.map(([term, value]) => (
        <div key={term} className="contents">
          <dt className="text-muted-foreground">{term}</dt>
          <dd className="m-0 [overflow-wrap:anywhere]">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

export type Column = string | { label: string; num: true };

/**
 * A table in its own card, or `bare` inside one. `empty` is the one row a
 * table with no rows shows; `label` names the table.
 */
export function Table({
  head,
  empty,
  bare,
  label,
  children,
}: {
  head: Column[];
  empty?: string | false;
  bare?: boolean;
  label?: string;
  children: ReactNode;
}) {
  const table = (
    <table aria-label={label} className="w-full border-collapse text-[13px]">
      <thead>
        <tr>
          {head.map((col) => {
            const { label, num } = typeof col === "string" ? { label: col, num: false } : col;
            return (
              <th
                key={label}
                className={cn(
                  "border-b bg-secondary px-4 py-2 text-left text-xs font-semibold whitespace-nowrap text-muted-foreground",
                  num && "text-right",
                )}
              >
                {label}
              </th>
            );
          })}
        </tr>
      </thead>
      <tbody className="[&>tr:last-child>td]:border-b-0">
        {empty ? (
          <tr>
            <Td colSpan={head.length} muted>
              {empty}
            </Td>
          </tr>
        ) : (
          children
        )}
      </tbody>
    </table>
  );
  if (bare) return table;
  return <div className="min-w-0 overflow-x-auto rounded-lg border bg-card shadow-xs">{table}</div>;
}

export function Td({
  num,
  mono,
  bad,
  muted,
  colSpan,
  children,
}: {
  num?: boolean;
  mono?: boolean;
  bad?: boolean;
  muted?: boolean;
  colSpan?: number;
  children?: ReactNode;
}) {
  return (
    <td
      colSpan={colSpan}
      className={cn(
        "border-b px-4 py-2 text-left align-top whitespace-nowrap",
        num && "text-right tabular-nums",
        mono && "font-mono text-[0.92em]",
        bad && "text-destructive",
        muted && "text-muted-foreground",
      )}
    >
      {children}
    </td>
  );
}
