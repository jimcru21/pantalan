import { useNode } from "@/api/node";
import { NodeTabs } from "@/overview/NodeTabs";
import { StatBand } from "@/overview/StatBand";
import { TopBar } from "@/overview/TopBar";

/**
 * The Node overview: the top bar naming the Node, the stat band, then the
 * tabs. Each part polls on its own, and only the open tab's data is
 * fetched.
 */
export function OverviewPage() {
  const node = useNode();
  return (
    <>
      <TopBar node={node.data} />
      <main className="grid min-w-0 content-start gap-5 p-6 max-sm:p-4">
        <h1 className="sr-only">Overview</h1>
        <StatBand node={node.data} />
        <NodeTabs />
      </main>
    </>
  );
}
