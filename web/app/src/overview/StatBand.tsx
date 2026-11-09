import type { ReactNode } from "react";
import type { DiskTile, Node } from "@/api/generated/httpapi";
import { formatBytes, formatBytesOf, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Sparkline } from "./Sparkline";

/** The row of tiles under the top bar: the host's, then Docker's. */
export function StatBand({ node }: { node: Node | undefined }) {
  const host = node?.host;
  return (
    <section
      aria-label="Node health"
      className="grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] overflow-hidden rounded-lg border bg-card shadow-xs max-sm:grid-cols-2"
    >
      {node && host && (
        <>
          <Tile label="CPU">
            <Value>{host.cpu.ratio === undefined ? "–" : formatPercent(host.cpu.ratio)}</Value>
            <Sparkline label="CPU, last 10 minutes" values={host.cpu.history} />
          </Tile>
          <Tile label="Memory">
            <Value>{formatPercent(share(host.memory.used, host.memory.total))}</Value>
            <Sub>{formatBytesOf(host.memory.used, host.memory.total)}</Sub>
            {host.memory.swap_used > 0 && (
              <Sub>Swap {formatBytesOf(host.memory.swap_used, host.memory.swap_total)}</Sub>
            )}
            <Sparkline label="Memory, last 10 minutes" values={host.memory.history} />
          </Tile>
          {host.disks.map((disk) => (
            <Disk key={disk.path} disk={disk} />
          ))}
        </>
      )}
      {node && !host && (
        <Tile label="Node" className="col-span-full">
          <Sub>Unavailable{node.host_error && `: ${node.host_error}`}</Sub>
        </Tile>
      )}
      {node && (
        <>
          <Tile label="Containers">
            {node.docker.reachable && node.docker.containers ? (
              <>
                <Value>
                  {node.docker.containers.running}/{node.docker.containers.total}{" "}
                  <small className="text-[13px] font-medium text-muted-foreground">running</small>
                </Value>
                {node.docker.containers.unhealthy > 0 && (
                  <Sub bad>{node.docker.containers.unhealthy} unhealthy</Sub>
                )}
              </>
            ) : (
              <Sub bad>Docker unreachable</Sub>
            )}
          </Tile>
          <Tile label="Docker">
            {node.docker.reachable ? (
              <Value>{node.docker.version}</Value>
            ) : (
              <Sub bad>Docker unreachable</Sub>
            )}
          </Tile>
        </>
      )}
    </section>
  );
}

function Disk({ disk }: { disk: DiskTile }) {
  const used = share(disk.total - Math.min(disk.free, disk.total), disk.total);
  const percent = Math.round(used * 1000) / 10;
  return (
    <Tile label={`Disk ${disk.path}`}>
      <Value>
        {formatBytes(disk.free)}{" "}
        <small className="text-[13px] font-medium text-muted-foreground">free</small>
      </Value>
      <Sub>of {formatBytes(disk.total)}</Sub>
      <div
        role="meter"
        aria-label={`Disk ${disk.path} used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        aria-valuetext={formatPercent(used)}
        className="mt-1.5 h-2 overflow-hidden rounded-full bg-muted"
      >
        <div
          className={cn("h-full rounded-full", used >= 0.9 ? "bg-destructive" : "bg-ok")}
          style={{ width: `${percent}%` }}
        />
      </div>
    </Tile>
  );
}

// One tile. Each draws a divider on its right and bottom; the band clips
// the ones on its outer edge, so any number of tiles wraps cleanly.
function Tile({
  label,
  className,
  children,
}: {
  label: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className={cn(
        "grid min-w-0 content-start gap-1 px-[18px] py-4 shadow-[1px_0_0_var(--border),0_1px_0_var(--border)]",
        className,
      )}
    >
      <span className="text-xs text-muted-foreground" aria-hidden>
        {label}
      </span>
      {children}
    </div>
  );
}

function Value({ children }: { children: ReactNode }) {
  return <span className="text-2xl/[1.1] font-bold tabular-nums">{children}</span>;
}

function Sub({ bad, children }: { bad?: boolean; children: ReactNode }) {
  return (
    <span
      className={cn("text-xs tabular-nums", bad ? "text-destructive" : "text-muted-foreground")}
    >
      {children}
    </span>
  );
}

// part as a share of whole, 0 when whole is.
function share(part: number, whole: number): number {
  return whole === 0 ? 0 : part / whole;
}
