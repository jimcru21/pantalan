package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// The Node overview reads the Node straight from package nodeinfo, not
// through the service layer: it is read-only, holds no rules about Apps,
// and nothing else needs it (ADR 0003). These are the small interfaces it
// reads through; cmd/pantalan wires the nodeinfo implementations in.

// NodeHost is the Node's host health; nodeinfo.Sampler provides it.
type NodeHost interface {
	Snapshot() nodeinfo.Snapshot
}

// NodeDocker is the Node's Docker, read through the Engine API;
// nodeinfo.Docker provides it.
type NodeDocker interface {
	// Summary is an error when Docker is unreachable.
	Summary(ctx context.Context) (nodeinfo.DockerSummary, error)
	// Stats are the running containers' CPU and memory, by ID, through
	// the API version a Summary negotiated.
	Stats(ctx context.Context, apiVersion string, ids []string) map[string]nodeinfo.ContainerStats
	Images(ctx context.Context, apiVersion string) ([]nodeinfo.Image, error)
	Version(ctx context.Context, apiVersion string) (nodeinfo.DockerVersion, error)
	// DiskUsage is system/df, which is slow; the handler shares its
	// answer between requests for diskUsageTTL.
	DiskUsage(ctx context.Context, apiVersion string) (nodeinfo.DiskUsage, error)
}

// NodeEvents is Docker's recent container events; nodeinfo.EventStream
// provides it.
type NodeEvents interface {
	Recent() nodeinfo.EventLog
}

// nodeRead is what one request reads of the Node: the sampler's snapshot
// and a Docker summary.
type nodeRead struct {
	host   nodeinfo.Snapshot
	docker nodeinfo.DockerSummary
	// dockerErr is why Docker couldn't be read: it is unreachable.
	dockerErr error
}

func (h *handler) readNode(ctx context.Context) nodeRead {
	var n nodeRead
	if h.deps.Host != nil {
		n.host = h.deps.Host.Snapshot()
	}
	if h.deps.Docker == nil {
		n.dockerErr = errors.New("no Docker configured")
	} else {
		n.docker, n.dockerErr = h.deps.Docker.Summary(ctx)
	}
	return n
}

// disks are the filesystems the band shows: Docker's root dir, plus /
// when that is another filesystem; just / when Docker's is unknown.
func (n nodeRead) disks(root nodeinfo.Disk) []nodeinfo.Disk {
	if n.dockerErr != nil || n.docker.RootDiskErr != nil {
		return []nodeinfo.Disk{root}
	}
	if n.docker.RootDisk.SameFilesystem(root) {
		return []nodeinfo.Disk{n.docker.RootDisk}
	}
	return []nodeinfo.Disk{n.docker.RootDisk, root}
}

func (h *handler) node(w http.ResponseWriter, r *http.Request) {
	n := h.readNode(r.Context())
	out := Node{
		IP:     localIP(r),
		Arch:   runtime.GOARCH,
		Status: NodeStatus{Level: StatusOff, Reasons: []string{"No Node information yet"}},
		Docker: dockerTile(n),
	}
	snap := n.host
	if snap.Err != nil {
		out.HostError = snap.Err.Error()
	}
	latest, ok := snap.Latest()
	if !ok {
		h.writeJSON(w, http.StatusOK, out)
		return
	}
	disks := n.disks(latest.Disk)
	out.Status = hostStatus(latest, snap.Err, disks)
	out.Status.addDocker(n)
	out.Status.healthy()

	host := &NodeHostInfo{
		Hostname:      latest.Hostname,
		OS:            latest.OS,
		UptimeSeconds: int64(latest.Uptime / time.Second),
		Load:          latest.Load,
		CPU:           CPUTile{History: []float64{}},
		Disks:         []DiskTile{},
	}
	if latest.HasCPU {
		r := latest.CPUPercent / 100
		host.CPU.Ratio = &r
	}
	mem := latest.Memory
	host.Memory = MemoryTile{
		Total:     mem.Total,
		Available: mem.Available,
		Used:      mem.Used(),
		SwapTotal: mem.SwapTotal,
		SwapUsed:  mem.SwapUsed(),
		History:   []float64{},
	}
	for _, s := range snap.History {
		if s.HasCPU {
			host.CPU.History = append(host.CPU.History, s.CPUPercent/100)
		}
		host.Memory.History = append(host.Memory.History, ratio(s.Memory.Used(), s.Memory.Total))
	}
	for _, d := range disks {
		host.Disks = append(host.Disks, DiskTile{Path: d.Path, Total: d.Total, Free: d.Free})
	}
	out.Host = host
	h.writeJSON(w, http.StatusOK, out)
}

func dockerTile(n nodeRead) DockerTile {
	if n.dockerErr != nil {
		return DockerTile{Error: n.dockerErr.Error()}
	}
	counts := &ContainerCounts{Total: len(n.docker.Containers)}
	for _, c := range n.docker.Containers {
		if c.Running() {
			counts.Running++
		}
		if c.HealthStatus() == "unhealthy" {
			counts.Unhealthy++
		}
	}
	return DockerTile{Reachable: true, Version: n.docker.Info.ServerVersion, Containers: counts}
}

// The status rules: the level and its reasons are decided here, not in
// the browser.

// warn turns the status amber for reason, unless it is red.
func (s *NodeStatus) warn(reason string) {
	if s.Level != StatusDown {
		s.Level = StatusWarn
	}
	s.Reasons = append(s.Reasons, reason)
}

// hostStatus is the status the host gives: amber when a disk the band
// shows or memory runs under 10 % free, or when the latest read failed
// (readErr) and s is an older sample. The thresholds are fixed in v1.
func hostStatus(s nodeinfo.Sample, readErr error, disks []nodeinfo.Disk) NodeStatus {
	st := NodeStatus{Level: StatusOK}
	if readErr != nil {
		st.warn("Can't read the Node, so this is older data: " + readErr.Error())
	}
	for _, disk := range disks {
		if underTenth(disk.Free, disk.Total) {
			st.warn("Disk " + disk.Path + " has under 10% free")
		}
	}
	if underTenth(s.Memory.Available, s.Memory.Total) {
		st.warn("Memory has under 10% available")
	}
	return st
}

// addDocker adds Docker's reasons: red, first, when it is unreachable,
// which beats amber; amber for a container that is unhealthy or
// restarting and for each warning Docker reports.
func (s *NodeStatus) addDocker(n nodeRead) {
	if n.dockerErr != nil {
		s.Level = StatusDown
		s.Reasons = append([]string{"Docker unreachable: " + n.dockerErr.Error()}, s.Reasons...)
		return
	}
	for _, c := range n.docker.Containers {
		if trouble := containerTrouble(c); trouble != "" {
			s.warn("Container " + c.Name() + " is " + trouble)
		}
	}
	for _, w := range n.docker.Info.Warnings {
		s.warn("Docker: " + w)
	}
}

// healthy fills in the reason for a status nothing is wrong with.
func (s *NodeStatus) healthy() {
	if len(s.Reasons) == 0 {
		s.Reasons = []string{"Healthy"}
	}
}

// containerTrouble is what is wrong with c, "restarting" or "unhealthy",
// or "" when nothing is.
func containerTrouble(c nodeinfo.Container) string {
	switch {
	case c.State == "restarting":
		return "restarting"
	case c.HealthStatus() == "unhealthy":
		return "unhealthy"
	}
	return ""
}

// underTenth reports part < 10 % of whole, in integers so that exactly
// 10 % is not under.
func underTenth(part, whole uint64) bool { return part*10 < whole }

// ratio is part as a share of whole, 0 when whole is.
func ratio(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// localIP is the Node address this request came in on.
func localIP(r *http.Request) string {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
