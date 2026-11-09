// The overview's tabs. Radix mounts only the open tab's panel, and each
// panel owns its query, so only the open tab is fetched and polled.
import { useState, type ReactNode } from "react";
import type { ContainerPort, NodeContainer, NodeEvent } from "@/api/generated/httpapi";
import {
  isDockerUnreachable,
  useNodeContainers,
  useNodeDocker,
  useNodeEvents,
  useNodeImages,
} from "@/api/node";
import { Alert, Facts, Panel, Table, Td } from "@/components/panel";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  formatBytes,
  formatCount,
  formatLocalTime,
  formatPercentTenths,
  formatUTCDate,
  formatUTCTime,
} from "@/lib/format";

const tabs = [
  { value: "containers", label: "Containers", panel: ContainersTab },
  { value: "images", label: "Images", panel: ImagesTab },
  { value: "docker", label: "Docker", panel: DockerTab },
  { value: "events", label: "Events", panel: EventsTab },
];

export function NodeTabs() {
  const [open, setOpen] = useState("containers");
  return (
    <Tabs value={open} onValueChange={setOpen} className="min-w-0 gap-3">
      <TabsList
        variant="line"
        aria-label="Node overview tabs"
        className="h-auto w-full justify-start gap-1 overflow-x-auto border-b p-0"
      >
        {tabs.map(({ value, label }) => (
          <TabsTrigger
            key={value}
            value={value}
            className="flex-none rounded-none border-0 border-b-2 border-transparent px-3 py-2 text-muted-foreground after:hidden data-[state=active]:border-primary data-[state=active]:text-foreground dark:data-[state=active]:border-primary"
          >
            {label}
          </TabsTrigger>
        ))}
      </TabsList>
      {tabs.map(({ value, panel: Panel }) => (
        <TabsContent key={value} value={value} className="grid min-w-0 gap-3">
          <Panel />
        </TabsContent>
      ))}
    </Tabs>
  );
}

function ContainersTab() {
  const { data, error } = useNodeContainers();
  if (error) return <LoadError error={error} />;
  if (!data) return null;
  return (
    <Table
      head={[
        "Name",
        "Image",
        "State",
        "Ports",
        "Uptime",
        { label: "CPU", num: true },
        { label: "Memory", num: true },
      ]}
      empty={data.length === 0 && "No containers on this Node."}
    >
      {data.map((c) => {
        const running = c.state === "running";
        return (
          <tr key={c.id}>
            <Td>
              <b>{c.name}</b>
              {c.app && (
                <span
                  title="Pantalan App"
                  className="ml-1.5 inline-block rounded-full bg-accent px-[7px] py-px align-[1px] text-[11px] font-semibold text-primary"
                >
                  {c.app.name ?? c.app.id}
                </span>
              )}
            </Td>
            <Td mono>{c.image}</Td>
            <Td bad={c.trouble !== undefined}>
              {running ? ["running", c.health].filter(Boolean).join(" · ") : c.status}
            </Td>
            <Td mono>{formatPorts(c.ports)}</Td>
            <Td>{running ? uptime(c.status) : "–"}</Td>
            <Td num>{c.stats?.cpu === undefined ? "–" : formatPercentTenths(c.stats.cpu)}</Td>
            <Td num>{memory(c)}</Td>
          </tr>
        );
      })}
    </Table>
  );
}

function memory(c: NodeContainer): string {
  if (!c.stats) return "–";
  return `${formatBytes(c.stats.memory_used)} / ${formatBytes(c.stats.memory_limit)}`;
}

// How long Docker says a running container has been up: its status "Up 3
// hours (healthy)" without the "Up" and the health.
function uptime(status: string): string {
  const up = status.replace(/^Up /, "").replace(/ \([^)]*\)$/, "");
  return up || "–";
}

// "127.0.0.1:49153→8080/tcp" when published, "5432/tcp" when not. The
// API has already left out the IPv6 twin of a port published on every
// IPv4 address.
function formatPorts(ports: ContainerPort[]): string {
  const out = ports.map((p) => {
    if (p.public_port === undefined) return `${p.private_port}/${p.type}`;
    const ip = p.ip?.includes(":") ? `[${p.ip}]` : (p.ip ?? "");
    return `${ip}:${p.public_port}→${p.private_port}/${p.type}`;
  });
  return out.length ? [...new Set(out)].join(", ") : "–";
}

function ImagesTab() {
  const { data, error } = useNodeImages();
  if (error) return <LoadError error={error} />;
  if (!data) return null;
  return (
    <Table
      head={["Repository:tag", { label: "Size", num: true }, "Created", "In use"]}
      empty={data.length === 0 && "No images on this Node."}
    >
      {data.map((img) => (
        <tr key={img.id}>
          <Td mono>{img.tags.length ? img.tags.join(", ") : "<none>:<none>"}</Td>
          <Td num>{formatBytes(img.size)}</Td>
          <Td>{formatUTCDate(img.created)}</Td>
          <Td>{img.in_use ? "In use" : <span className="text-muted-foreground">Unused</span>}</Td>
        </tr>
      ))}
    </Table>
  );
}

function DockerTab() {
  const { data, error, refetch, isFetching } = useNodeDocker();
  if (error && !isFetching) return <LoadError error={error} onRetry={() => void refetch()} />;
  if (!data) return null;
  const facts: [string, ReactNode][] = [
    ["Version", data.version],
    [
      "API version",
      <>
        {data.api_version}{" "}
        <span className="text-muted-foreground">
          daemon accepts {data.min_api_version}–{data.max_api_version}
        </span>
      </>,
    ],
    ["Storage driver", data.driver],
    ["Cgroup", `v${data.cgroup_version}${data.cgroup_driver ? ` (${data.cgroup_driver})` : ""}`],
    ["Root dir", <span className="font-mono text-[0.92em]">{data.root_dir}</span>],
    [
      "Warnings",
      data.warnings.length ? (
        <ul className="list-disc pl-[18px] text-warn">
          {data.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      ) : (
        "None"
      ),
    ],
  ];
  const du = data.disk_usage;
  return (
    <>
      <Panel
        title="Engine"
        action={
          <button
            type="button"
            onClick={() => void refetch()}
            disabled={isFetching}
            className="cursor-pointer rounded-md border px-2.5 py-[5px] font-medium hover:bg-secondary disabled:cursor-default disabled:opacity-60"
          >
            Refresh
          </button>
        }
      >
        <Facts facts={facts} />
      </Panel>
      <Panel
        title="Disk usage"
        action={du && <span className="text-muted-foreground">as of {formatLocalTime(du.at)}</span>}
      >
        {data.disk_usage_error ? (
          <Alert>{data.disk_usage_error}</Alert>
        ) : (
          du && (
            <Table
              bare
              head={[
                "Kind",
                { label: "Total", num: true },
                { label: "Active", num: true },
                { label: "Size", num: true },
                { label: "Reclaimable", num: true },
              ]}
            >
              {(
                [
                  ["Images", du.images],
                  ["Containers", du.containers],
                  ["Volumes", du.volumes],
                  ["Build cache", du.build_cache],
                ] as const
              ).map(([kind, use]) => (
                <tr key={kind}>
                  <Td>{kind}</Td>
                  <Td num>{formatCount(use.total)}</Td>
                  <Td num>{formatCount(use.active)}</Td>
                  <Td num>{formatBytes(use.size)}</Td>
                  <Td num>{formatBytes(use.reclaimable)}</Td>
                </tr>
              ))}
            </Table>
          )
        )}
      </Panel>
    </>
  );
}

function EventsTab() {
  const { data, error } = useNodeEvents();
  if (error) return <LoadError error={error} />;
  if (!data) return null;
  return (
    <div className="min-w-0 overflow-x-auto rounded-lg border bg-card shadow-xs">
      {data.stream_error && (
        <Alert>Not connected to Docker's event stream, retrying: {data.stream_error}</Alert>
      )}
      <Table
        bare
        head={["Time (UTC)", "Container", "Event", "Detail", "Image"]}
        empty={data.events.length === 0 && "No container events since Pantalan started."}
      >
        {data.events.flatMap((ev, i) => {
          const detail = eventDetail(ev);
          const row = (
            <tr key={i}>
              <Td mono>{formatUTCTime(ev.time)}</Td>
              <Td>
                <b>{ev.container ?? ev.container_id}</b>
              </Td>
              <Td bad={["die", "oom", "kill"].includes(ev.action) || ev.health === "unhealthy"}>
                {ev.action}
              </Td>
              <Td>{detail}</Td>
              <Td mono>{ev.image}</Td>
            </tr>
          );
          if (!ev.gap_before) return [row];
          return [
            row,
            <tr key={`${i}-gap`}>
              <td
                colSpan={5}
                className="bg-warn-soft px-4 py-2 text-center text-xs font-semibold text-warn"
              >
                Stream reconnected: earlier events may be missing
              </td>
            </tr>,
          ];
        })}
      </Table>
    </div>
  );
}

function eventDetail(ev: NodeEvent): string {
  if (ev.health) return ev.health;
  if (ev.exit_code !== undefined) return `exit code ${ev.exit_code}`;
  if (ev.signal) return `signal ${ev.signal}`;
  return "";
}

// The tab's error: the "Docker unreachable" card for Docker's 503, the
// message otherwise. Polling carries on, so the tab recovers on its own.
function LoadError({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  if (!isDockerUnreachable(error)) {
    return (
      <div className="rounded-lg border bg-card shadow-xs">
        <Alert>{error.message}</Alert>
      </div>
    );
  }
  return (
    <div
      role="alert"
      className="grid gap-1.5 rounded-lg border bg-card px-4 py-8 text-center shadow-xs"
    >
      <p className="m-0 font-bold">Docker unreachable</p>
      <p className="m-0 text-muted-foreground">{error.message}</p>
      <p className="m-0 text-muted-foreground">
        Check the daemon on the Node with{" "}
        <code className="font-mono text-[0.92em]">systemctl status docker</code>.
      </p>
      {onRetry && (
        <p className="m-0 mt-1.5">
          <button
            type="button"
            onClick={onRetry}
            className="cursor-pointer rounded-md border px-2.5 py-[5px] font-medium hover:bg-secondary"
          >
            Refresh
          </button>
        </p>
      )}
    </div>
  );
}
