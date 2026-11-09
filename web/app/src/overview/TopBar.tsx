import type { Node, NodeStatus } from "@/api/generated/node";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { formatLoad, formatUptime } from "@/lib/format";
import { cn } from "@/lib/utils";

/** The slim bar naming the Node, with its status dot. */
export function TopBar({ node }: { node: Node | undefined }) {
  const host = node?.host;
  return (
    <header
      aria-label="Node"
      className="flex min-h-14 flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b bg-card px-6 py-2 max-sm:px-4"
    >
      {node && (
        <>
          <div className="flex min-w-0 items-center gap-2.5">
            <StatusDot status={node.status} />
            {host ? (
              <>
                <b className="whitespace-nowrap">{host.hostname}</b>
                <span className="truncate font-mono text-[0.92em] text-muted-foreground">
                  {node.ip} · {host.os} · {node.arch}
                </span>
              </>
            ) : (
              <b>Node information unavailable</b>
            )}
          </div>
          {host && (
            <div className="flex gap-3 text-xs whitespace-nowrap text-muted-foreground">
              <span>up {formatUptime(host.uptime_seconds)}</span>
              <span>load {host.load.map(formatLoad).join(" ")}</span>
            </div>
          )}
        </>
      )}
    </header>
  );
}

const dotLevel: Record<string, string> = {
  ok: "bg-ok ring-ok-soft",
  warn: "bg-warn ring-warn-soft",
  down: "bg-destructive ring-destructive-soft",
};

// Green, amber, red or grey; the reasons are its name and its tooltip, one
// per line. A button so a keyboard can reach the tooltip.
function StatusDot({ status }: { status: NodeStatus }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={`Status: ${status.reasons.join("; ")}`}
          className={cn(
            "size-2.5 flex-none cursor-help rounded-full ring-3",
            dotLevel[status.level] ?? "bg-muted-foreground ring-transparent",
          )}
        />
      </TooltipTrigger>
      <TooltipContent side="bottom" align="start" className="whitespace-pre-line">
        {status.reasons.join("\n")}
      </TooltipContent>
    </Tooltip>
  );
}
