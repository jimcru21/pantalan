import type { ReactNode } from "react";

/** A page's frame inside the shell: its heading, then its content. */
export function Page({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <main className="grid min-w-0 content-start gap-5 p-6">
      <h1 className="text-xl font-semibold">{title}</h1>
      {children}
    </main>
  );
}
