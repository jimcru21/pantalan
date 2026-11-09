import { NotFoundPage } from "@/shell/Page";

/**
 * What an App's pages (its page, its edit form, its env editor) show for
 * an ID no App has, e.g. one deleted since the link was made.
 */
export function AppNotFound({ appId }: { appId: string }) {
  return (
    <NotFoundPage title="App not found" back={{ to: "/apps", label: "Back to Apps" }}>
      No App has the ID <code className="font-mono text-[0.92em]">{appId}</code>. It may have been
      deleted.
    </NotFoundPage>
  );
}
