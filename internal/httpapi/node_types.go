package httpapi

// The Node overview's response bodies. Values are raw: bytes, ratios
// (0.25 is 25 %), counts and RFC 3339 times; the browser formats them.
// This file holds only the wire types, so the TypeScript generator can
// take it whole.

// Node is GET /api/v1/node: the top bar and the stat band.
type Node struct {
	// IP is the Node address the request came in on.
	IP string `json:"ip"`
	// Arch is the architecture Pantalan runs on, e.g. "amd64".
	Arch   string     `json:"arch"`
	Status NodeStatus `json:"status"`
	// Host is missing until the host sampler has a reading.
	Host *NodeHostInfo `json:"host,omitempty"`
	// HostError is why the latest host read failed, if it did; Host
	// then holds the last good reading.
	HostError string     `json:"host_error,omitempty"`
	Docker    DockerTile `json:"docker"`
}

// NodeStatus is the status dot: its level and the reasons for it.
type NodeStatus struct {
	Level StatusLevel `json:"level"`
	// Reasons are one line each: what is wrong, or "Healthy".
	Reasons []string `json:"reasons"`
}

// StatusLevel is the status dot's colour.
type StatusLevel string

const (
	StatusOK   StatusLevel = "ok"   // green
	StatusWarn StatusLevel = "warn" // amber: a disk or memory under 10 % free, a container in trouble, a Docker warning, or a failed host read
	StatusDown StatusLevel = "down" // red: Docker is unreachable
	StatusOff  StatusLevel = "off"  // grey: no host reading yet
)

// NodeHostInfo is the host's latest reading and its recent history.
type NodeHostInfo struct {
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"` // kernel name and release
	UptimeSeconds int64      `json:"uptime_seconds"`
	Load          [3]float64 `json:"load"` // 1, 5 and 15 minute averages
	CPU           CPUTile    `json:"cpu"`
	Memory        MemoryTile `json:"memory"`
	// Disks are Docker's root dir and, when it is on another
	// filesystem, /; just / while Docker's is unknown.
	Disks []DiskTile `json:"disks"`
}

// CPUTile is the busy share of all CPUs.
type CPUTile struct {
	// Ratio is the latest, missing until there are two samples.
	Ratio *float64 `json:"ratio,omitempty"`
	// History is the sampler's ring, oldest first, of the samples that
	// have a CPU ratio: up to the last 10 minutes, one every 5 s.
	History []float64 `json:"history"`
}

// MemoryTile is memory in bytes. Used is Total − Available.
type MemoryTile struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
	Used      uint64 `json:"used"`
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
	// History is the used share of each sample in the ring, oldest
	// first.
	History []float64 `json:"history"`
}

// DiskTile is one filesystem, in bytes.
type DiskTile struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// DockerTile is the Containers and Docker version tiles.
type DockerTile struct {
	// Reachable is false while Docker can't be read; Error says why,
	// and the other fields are missing.
	Reachable  bool             `json:"reachable"`
	Error      string           `json:"error,omitempty"`
	Version    string           `json:"version,omitempty"`
	Containers *ContainerCounts `json:"containers,omitempty"`
}

// ContainerCounts counts every container, running or not.
type ContainerCounts struct {
	Running   int `json:"running"`
	Total     int `json:"total"`
	Unhealthy int `json:"unhealthy"`
}

// The tab endpoints that read Docker answer 503 with
// {"error": "Docker unreachable: <reason>"} while it can't be read.

// NodeContainer is one entry of GET /api/v1/node/containers, which lists
// every container, running or not.
type NodeContainer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Created string `json:"created"` // RFC 3339
	// State is Docker's: "running", "exited", "restarting", ...
	State string `json:"state"`
	// Health is "healthy", "unhealthy" or "starting"; missing for a
	// container without a health check.
	Health string `json:"health,omitempty"`
	// Status is Docker's own text: "Up 3 hours (healthy)".
	Status string `json:"status"`
	// Trouble is "restarting" or "unhealthy" for a container in trouble,
	// which also turns the status amber; missing otherwise.
	Trouble string `json:"trouble,omitempty"`
	// Ports leave out the IPv6 twin Docker lists next to a port published
	// on every IPv4 address.
	Ports []ContainerPort `json:"ports"`
	// App is the App the container belongs to, by its label; missing for
	// other containers.
	App *ContainerApp `json:"app,omitempty"`
	// Stats are a running container's CPU and memory; missing when it
	// isn't running or they couldn't be read.
	Stats *ContainerStats `json:"stats,omitempty"`
}

// ContainerPort is one of a container's ports; IP and PublicPort are
// missing when it isn't published.
type ContainerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	Type        string `json:"type"` // "tcp", "udp", ...
}

// ContainerApp is the App a container belongs to. Name is missing when no
// App has the ID any more.
type ContainerApp struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ContainerStats are one container's CPU and memory.
type ContainerStats struct {
	// CPU is its CPU since the previous read, as docker stats counts it:
	// 1 is one whole CPU. Missing on the first read of a container.
	CPU *float64 `json:"cpu,omitempty"`
	// MemoryUsed leaves out inactive file, as docker stats does;
	// MemoryLimit is its limit, or the host's memory without one.
	MemoryUsed  uint64 `json:"memory_used"`
	MemoryLimit uint64 `json:"memory_limit"`
}

// NodeImage is one entry of GET /api/v1/node/images.
type NodeImage struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"` // empty for a dangling image
	Size    int64    `json:"size"`
	Created string   `json:"created"` // RFC 3339
	// InUse is whether a container, running or not, uses it.
	InUse bool `json:"in_use"`
}

// NodeDockerInfo is GET /api/v1/node/docker: the engine and its disk
// usage.
type NodeDockerInfo struct {
	Version string `json:"version"`
	// APIVersion is the Engine API version Pantalan uses; the daemon
	// accepts MinAPIVersion to MaxAPIVersion.
	APIVersion    string   `json:"api_version"`
	MinAPIVersion string   `json:"min_api_version"`
	MaxAPIVersion string   `json:"max_api_version"`
	Driver        string   `json:"driver"` // storage driver
	CgroupVersion string   `json:"cgroup_version"`
	CgroupDriver  string   `json:"cgroup_driver"`
	RootDir       string   `json:"root_dir"`
	Warnings      []string `json:"warnings"`
	// DiskUsage is missing when it couldn't be read; DiskUsageError
	// says why.
	DiskUsage      *DockerDiskUsage `json:"disk_usage,omitempty"`
	DiskUsageError string           `json:"disk_usage_error,omitempty"`
}

// DockerDiskUsage is Docker's disk use by kind. It is shared between
// requests for 60 s, so At may be up to a minute old.
type DockerDiskUsage struct {
	Images     DockerDiskUse `json:"images"`
	Containers DockerDiskUse `json:"containers"`
	Volumes    DockerDiskUse `json:"volumes"`
	BuildCache DockerDiskUse `json:"build_cache"`
	At         string        `json:"at"` // RFC 3339: when Docker computed it
}

// DockerDiskUse is one kind's count, how many are in use, and bytes.
type DockerDiskUse struct {
	Total       int   `json:"total"`
	Active      int   `json:"active"`
	Size        int64 `json:"size"`
	Reclaimable int64 `json:"reclaimable"`
}

// NodeEventLog is GET /api/v1/node/events: Docker's recent container
// events. It answers while Docker is down, with the events from before.
type NodeEventLog struct {
	// Events are newest first.
	Events []NodeEvent `json:"events"`
	// StreamError is why the event stream is down, while it is.
	StreamError string `json:"stream_error,omitempty"`
}

// NodeEvent is one container event.
type NodeEvent struct {
	Time        string `json:"time"` // RFC 3339, with nanoseconds
	ContainerID string `json:"container_id"`
	// Container is its name, when Docker gave one.
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
	// Action is "start", "stop", "die", "oom", "kill", "restart",
	// "health_status" or "destroy".
	Action string `json:"action"`
	// Health is the new health status of a health_status event.
	Health string `json:"health,omitempty"`
	// ExitCode is a die event's.
	ExitCode *int `json:"exit_code,omitempty"`
	// Signal is a kill event's, as Docker reports it.
	Signal string `json:"signal,omitempty"`
	// GapBefore is true when events older than this one may be missing:
	// the stream reconnected and couldn't backfill.
	GapBefore bool `json:"gap_before"`
}
