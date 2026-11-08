package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// Host is the Node's host health; nodeinfo.Sampler provides it.
type Host interface {
	Snapshot() nodeinfo.Snapshot
}

// Docker is the Node's Docker, read through the Engine API;
// nodeinfo.Docker provides it.
type Docker interface {
	// Summary is an error when Docker is unreachable.
	Summary(ctx context.Context) (nodeinfo.DockerSummary, error)
	// Stats are the running containers' CPU and memory, by ID, through
	// the API version a Summary negotiated.
	Stats(ctx context.Context, apiVersion string, ids []string) map[string]nodeinfo.ContainerStats
	Images(ctx context.Context, apiVersion string) ([]nodeinfo.Image, error)
	Version(ctx context.Context, apiVersion string) (nodeinfo.DockerVersion, error)
	// DiskUsage is system/df, which is slow: only the Docker tab asks,
	// and the answer is shared for nodeinfo.DiskUsageTTL.
	DiskUsage(ctx context.Context, apiVersion string) (nodeinfo.DiskUsage, error)
}

// overviewData is the dot of the Node overview and of each of its
// fragments, so a fragment renders exactly as it does inside the page.
type overviewData struct {
	// HostOK is false until the sampler has a reading; the host fields
	// are empty then and HostErr may say why.
	HostOK  bool
	HostErr string
	Top     topBar
	Band    statBand
}

// status is the top bar's dot: its colour and the reasons for it, which
// its tooltip lists.
type status struct {
	Level   level
	Reasons []string
}

// level is the status dot's colour, and its CSS class.
type level string

const (
	levelOK   level = "ok"   // green
	levelWarn level = "warn" // amber
	levelDown level = "down" // red: Docker unreachable
	levelOff  level = "off"  // grey: no Node data yet
)

// Tooltip is the reasons, one per line.
func (s status) Tooltip() string { return strings.Join(s.Reasons, "\n") }

// warn turns the dot amber for reason, unless it is red.
func (s *status) warn(reason string) {
	if s.Level != levelDown {
		s.Level = levelWarn
	}
	s.Reasons = append(s.Reasons, reason)
}

// containerTrouble is what is wrong with c, "restarting" or "unhealthy",
// or "" when nothing is. A container in trouble turns the dot amber and
// its row red.
func containerTrouble(c nodeinfo.Container) string {
	switch {
	case c.State == "restarting":
		return "restarting"
	case c.HealthStatus() == "unhealthy":
		return "unhealthy"
	}
	return ""
}

// hostStatus is the status the host gives: amber when a disk the band
// shows or memory runs under 10 % free, or when the latest read failed
// (readErr) and s is an older sample. The thresholds are fixed in v1.
func hostStatus(s nodeinfo.Sample, readErr error, disks []nodeinfo.Disk) status {
	st := status{Level: levelOK}
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
func (s *status) addDocker(n node) {
	if n.dockerErr != nil {
		s.Level = levelDown
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

// healthy fills in the reason for a dot nothing is wrong with.
func (s *status) healthy() {
	if len(s.Reasons) == 0 {
		s.Reasons = []string{"Healthy"}
	}
}

// underTenth reports part < 10 % of whole, in integers so that exactly
// 10 % is not under.
func underTenth(part, whole uint64) bool { return part*10 < whole }

// statBand is the row of tiles under the top bar: the host's, then
// Docker's.
type statBand struct {
	CPU    cpuTile
	Memory memoryTile
	// Disks are Docker's root dir and, when it is on another filesystem,
	// /; just / while Docker is unreachable.
	Disks  []diskTile
	Docker dockerTiles
}

// dockerTiles are the Containers and Docker tiles.
type dockerTiles struct {
	// OK is false while Docker is unreachable; the tiles say so then.
	OK                        bool
	Running, Total, Unhealthy int
	Version                   string
}

type cpuTile struct {
	Percent string // "30%", or "–" before there are two samples
	Spark   sparkline
}

type memoryTile struct {
	Percent string // used share of MemTotal, "75%"
	Used    string // "6.0 of 8.0 GiB"
	Swap    string // "0.5 of 2.0 GiB", or "" when no swap is in use
	Spark   sparkline
}

// sparkline is a percentage series drawn as an SVG polyline in a
// sparkWidth × 100 box: one x step per ring slot, newest at the right
// edge, y = 100 − value.
type sparkline struct {
	Label  string // what it shows, for screen readers: "CPU, last 10 minutes"
	Points string
	Width  int // of the viewBox: x runs from 0 to HistoryLen−1
}

func newSparkline(label string, values []float64) sparkline {
	pts := make([]string, len(values))
	offset := nodeinfo.HistoryLen - len(values)
	for i, v := range values {
		pts[i] = fmt.Sprintf("%d,%.1f", offset+i, 100-min(max(v, 0), 100))
	}
	return sparkline{Label: label, Points: strings.Join(pts, " "), Width: nodeinfo.HistoryLen - 1}
}

type diskTile struct {
	Path        string
	Free, Total string // "25.0 GiB"
	UsedPercent float64
}

// topBar is the slim bar naming the Node.
type topBar struct {
	Status   status
	Hostname string
	IP       string // the address the browser reached
	OS, Arch string
	Uptime   string // "12d 4h 17m"
	Load     string // "0.42 0.38 0.31"
}

// node is what one request reads of the Node: the sampler's snapshot and
// a Docker summary.
type node struct {
	host   nodeinfo.Snapshot
	docker nodeinfo.DockerSummary
	// dockerErr is why Docker couldn't be read: it is unreachable.
	dockerErr error
}

func (d *Dashboard) readNode(ctx context.Context) node {
	var n node
	if d.deps.Host != nil {
		n.host = d.deps.Host.Snapshot()
	}
	if d.deps.Docker == nil {
		n.dockerErr = errors.New("no Docker configured")
	} else {
		n.docker, n.dockerErr = d.deps.Docker.Summary(ctx)
	}
	return n
}

// disks are the filesystems the band shows: Docker's root dir, plus /
// when that is another filesystem; just / when Docker's is unknown.
func (n node) disks(root nodeinfo.Disk) []nodeinfo.Disk {
	if n.dockerErr != nil || n.docker.RootDiskErr != nil {
		return []nodeinfo.Disk{root}
	}
	if n.docker.RootDisk.SameFilesystem(root) {
		return []nodeinfo.Disk{n.docker.RootDisk}
	}
	return []nodeinfo.Disk{n.docker.RootDisk, root}
}

// overviewPage is the Node overview's first load: the polled regions,
// with the Containers tab open.
type overviewPage struct {
	overviewData
	Containers containersTab
}

func (d *Dashboard) overviewPage(r *http.Request) (any, error) {
	n := d.readNode(r.Context())
	return overviewPage{overviewData: d.overview(r, n), Containers: d.containersTab(r.Context(), n)}, nil
}

// overviewData is the dot of the top bar and band fragments.
func (d *Dashboard) overviewData(r *http.Request) (any, error) {
	return d.overview(r, d.readNode(r.Context())), nil
}

func (d *Dashboard) overview(r *http.Request, n node) overviewData {
	data := overviewData{Top: topBar{
		Status: status{Level: levelOff, Reasons: []string{"No Node information yet"}},
		IP:     localIP(r),
		Arch:   runtime.GOARCH,
	}}
	data.Band.Docker = dockerBand(n)
	snap := n.host
	if snap.Err != nil {
		data.HostErr = snap.Err.Error()
	}
	latest, ok := snap.Latest()
	if !ok {
		return data
	}
	data.HostOK = true
	disks := n.disks(latest.Disk)
	data.Top.Status = hostStatus(latest, snap.Err, disks)
	data.Top.Status.addDocker(n)
	data.Top.Status.healthy()
	data.Top.Hostname = latest.Hostname
	data.Top.OS = latest.OS
	data.Top.Uptime = formatUptime(latest.Uptime)
	data.Top.Load = fmt.Sprintf("%.2f %.2f %.2f", latest.Load[0], latest.Load[1], latest.Load[2])

	data.Band.CPU.Percent = "–"
	if latest.HasCPU {
		data.Band.CPU.Percent = formatPercent(latest.CPUPercent)
	}
	var cpuHistory, memHistory []float64
	for _, s := range snap.History {
		if s.HasCPU {
			cpuHistory = append(cpuHistory, s.CPUPercent)
		}
		memHistory = append(memHistory, share(s.Memory.Used(), s.Memory.Total))
	}
	data.Band.CPU.Spark = newSparkline("CPU, last 10 minutes", cpuHistory)
	mem := latest.Memory
	data.Band.Memory = memoryTile{
		Percent: formatPercent(share(mem.Used(), mem.Total)),
		Used:    formatBytesOf(mem.Used(), mem.Total),
		Spark:   newSparkline("Memory, last 10 minutes", memHistory),
	}
	if used := mem.SwapUsed(); used > 0 {
		data.Band.Memory.Swap = formatBytesOf(used, mem.SwapTotal)
	}
	for _, disk := range disks {
		data.Band.Disks = append(data.Band.Disks, diskTile{
			Path:        disk.Path,
			Free:        formatBytes(disk.Free),
			Total:       formatBytes(disk.Total),
			UsedPercent: share(disk.Total-min(disk.Free, disk.Total), disk.Total),
		})
	}
	return data
}

func dockerBand(n node) dockerTiles {
	if n.dockerErr != nil {
		return dockerTiles{}
	}
	t := dockerTiles{OK: true, Total: len(n.docker.Containers), Version: n.docker.Info.ServerVersion}
	for _, c := range n.docker.Containers {
		if c.Running() {
			t.Running++
		}
		if c.HealthStatus() == "unhealthy" {
			t.Unhealthy++
		}
	}
	return t
}

// share is part as a percentage of whole, 0 when whole is.
func share(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

func formatPercent(p float64) string { return fmt.Sprintf("%.0f%%", p) }

var byteUnits = []struct {
	name string
	size uint64
}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}}

// unitFor is the largest binary unit n is at least one of.
func unitFor(n uint64) (string, uint64) {
	for _, u := range byteUnits {
		if n >= u.size {
			return u.name, u.size
		}
	}
	return "B", 1
}

// formatBytes is n in its own unit with one decimal: "25.0 GiB".
func formatBytes(n uint64) string {
	name, size := unitFor(n)
	if size == 1 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(size), name)
}

// formatBytesOf is part and whole in whole's unit: "6.0 of 8.0 GiB".
func formatBytesOf(part, whole uint64) string {
	name, size := unitFor(whole)
	return fmt.Sprintf("%.1f of %.1f %s", float64(part)/float64(size), float64(whole)/float64(size), name)
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

// formatUptime is days, hours and minutes, leaving out leading zeros:
// "12d 4h 17m", "4h 0m", "3m".
func formatUptime(d time.Duration) string {
	m := int64(d / time.Minute)
	days, hours, mins := m/(24*60), m/60%24, m%60
	var b strings.Builder
	if days > 0 {
		fmt.Fprintf(&b, "%dd ", days)
	}
	if days > 0 || hours > 0 {
		fmt.Fprintf(&b, "%dh ", hours)
	}
	fmt.Fprintf(&b, "%dm", mins)
	return b.String()
}
