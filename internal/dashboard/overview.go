package dashboard

import (
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
	// Level is the dot's CSS class: "ok" (green), "warn" (amber), "off"
	// (grey, no data yet); app.css also has "down" (red) for Docker.
	Level   string
	Reasons []string
}

// Tooltip is the reasons, one per line.
func (s status) Tooltip() string { return strings.Join(s.Reasons, "\n") }

// warn turns the dot amber for reason.
func (s *status) warn(reason string) {
	s.Level = "warn"
	s.Reasons = append(s.Reasons, reason)
}

// hostStatus is the status the host alone gives: amber when disk or
// memory runs under 10 % free, or when the latest read failed (readErr)
// and s is an older sample. The thresholds are fixed in v1. The Docker
// ticket adds red for Docker unreachable and its own amber reasons.
func hostStatus(s nodeinfo.Sample, readErr error) status {
	st := status{Level: "ok"}
	if readErr != nil {
		st.warn("Can't read the host, so this is older data: " + readErr.Error())
	}
	if underTenth(s.Disk.Free, s.Disk.Total) {
		st.warn("Disk " + s.Disk.Path + " has under 10% free")
	}
	if underTenth(s.Memory.Available, s.Memory.Total) {
		st.warn("Memory has under 10% available")
	}
	if len(st.Reasons) == 0 {
		st.Reasons = []string{"Healthy"}
	}
	return st
}

// underTenth reports part < 10 % of whole, in integers so that exactly
// 10 % is not under.
func underTenth(part, whole uint64) bool { return part*10 < whole }

// statBand is the row of tiles under the top bar. The Docker ticket adds
// its tiles next to the host ones.
type statBand struct {
	CPU    cpuTile
	Memory memoryTile
	Disk   diskTile
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

func (d *Dashboard) overviewData(r *http.Request) (any, error) {
	data := overviewData{Top: topBar{
		Status: status{Level: "off", Reasons: []string{"No host information yet"}},
		IP:     localIP(r),
		Arch:   runtime.GOARCH,
	}}
	if d.deps.Host == nil {
		return data, nil
	}
	snap := d.deps.Host.Snapshot()
	if snap.Err != nil {
		data.HostErr = snap.Err.Error()
	}
	latest, ok := snap.Latest()
	if !ok {
		return data, nil
	}
	data.HostOK = true
	data.Top.Status = hostStatus(latest, snap.Err)
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
	disk := latest.Disk
	data.Band.Disk = diskTile{
		Path:        disk.Path,
		Free:        formatBytes(disk.Free),
		Total:       formatBytes(disk.Total),
		UsedPercent: share(disk.Total-min(disk.Free, disk.Total), disk.Total),
	}
	return data, nil
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
