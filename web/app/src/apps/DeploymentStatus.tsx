import type { DeploymentStatus as Status } from "@/api/generated/domain";
import { cn } from "@/lib/utils";

const looks: Record<string, { label: string; className: string }> = {
  queued: { label: "Queued", className: "bg-warn-soft text-warn" },
  building: { label: "Building", className: "bg-warn-soft text-warn" },
  starting: { label: "Starting", className: "bg-warn-soft text-warn" },
  live: { label: "Live", className: "bg-ok-soft text-ok" },
  failed: { label: "Failed", className: "bg-destructive-soft text-destructive" },
  superseded: { label: "Superseded", className: "bg-secondary text-muted-foreground" },
};

/**
 * A Deployment's status as a pill: green when live, red when failed,
 * amber on the way there, grey once superseded. With no status, the App
 * has never been deployed.
 */
export function DeploymentStatus({ status }: { status?: Status }) {
  const look =
    status === undefined
      ? { label: "Not deployed", className: "bg-secondary text-muted-foreground" }
      : (looks[status] ?? { label: status, className: "bg-secondary text-muted-foreground" });
  return (
    <span
      className={cn(
        "inline-block rounded-full px-2 py-px text-[11px] font-semibold whitespace-nowrap",
        look.className,
      )}
    >
      {look.label}
    </span>
  );
}
