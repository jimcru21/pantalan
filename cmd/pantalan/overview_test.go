package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/httpapi"
)

// hostState is what a fixture /proc root and the fake statfs report.
type hostState struct {
	Hostname string
	Kernel   string // /proc/sys/kernel/osrelease
	Uptime   time.Duration
	Load     [3]float64
	// CPU is the aggregate line of /proc/stat, in ticks.
	CPUBusy, CPUIdle uint64
	// Memory, as /proc/meminfo has it, in KiB.
	MemTotal, MemAvailable, SwapTotal, SwapFree uint64
	// Disk is what statfs reports for /, in bytes.
	DiskTotal, DiskFree uint64
	// DockerDisk is what statfs reports for the fake Engine's
	// DockerRootDir, /var/lib/docker: a filesystem of its own, unless
	// DockerOnRoot puts it on the one holding /.
	DockerDiskTotal, DockerDiskFree uint64
	DockerOnRoot                    bool
	// Missing is a file left out of the fixture, to break reads.
	Missing string
}

// fakeProc is the Node's host as the sampler sees it: a fixture /proc
// root, a fake statfs and the ticks that make the sampler read them.
type fakeProc struct {
	t     *testing.T
	root  string
	ticks chan time.Time

	mu    sync.Mutex // guards state, which the sampler's statfs reads
	state hostState
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	fp := &fakeProc{t: t, root: filepath.Join(t.TempDir(), "proc"), ticks: make(chan time.Time)}
	fp.state = hostState{
		Hostname:     "node-1",
		Kernel:       "6.8.0-49-generic",
		Uptime:       3 * time.Hour,
		Load:         [3]float64{0.10, 0.20, 0.30},
		CPUBusy:      1000,
		CPUIdle:      9000,
		MemTotal:     8 << 20, // 8 GiB
		MemAvailable: 4 << 20,
		DiskTotal:    100 << 30,
		DiskFree:     50 << 30,
		// Docker's data on a filesystem of its own.
		DockerDiskTotal: 200 << 30,
		DockerDiskFree:  150 << 30,
	}
	fp.write()
	return fp
}

// world is a test world whose Node's /proc and disks are this fixture.
func (fp *fakeProc) world(t *testing.T) World {
	world := testWorld(t)
	world.ProcRoot = fp.root
	world.Statfs = fp.statfs
	world.SampleTicks = fp.ticks
	return world
}

// set changes the fixture. The sampler sees it on its next sample.
func (fp *fakeProc) set(change func(*hostState)) {
	fp.mu.Lock()
	change(&fp.state)
	fp.mu.Unlock()
	fp.write()
}

func (fp *fakeProc) statfs(path string, buf *syscall.Statfs_t) error {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	const block = 4096
	fs := func(id int32, total, free uint64) syscall.Statfs_t {
		return syscall.Statfs_t{Bsize: block, Blocks: total / block, Bavail: free / block, Bfree: free / block, Fsid: syscall.Fsid{X__val: [2]int32{id, 0}}}
	}
	switch {
	case path == "/", path == "/var/lib/docker" && fp.state.DockerOnRoot:
		*buf = fs(1, fp.state.DiskTotal, fp.state.DiskFree)
	case path == "/var/lib/docker":
		*buf = fs(2, fp.state.DockerDiskTotal, fp.state.DockerDiskFree)
	default:
		return fmt.Errorf("statfs %s: not in the fixture", path)
	}
	return nil
}

func (fp *fakeProc) write() {
	fp.t.Helper()
	fp.mu.Lock()
	h := fp.state
	fp.mu.Unlock()
	files := map[string]string{
		"sys/kernel/hostname":  h.Hostname + "\n",
		"sys/kernel/ostype":    "Linux\n",
		"sys/kernel/osrelease": h.Kernel + "\n",
		"uptime":               fmt.Sprintf("%.2f 12345.67\n", h.Uptime.Seconds()),
		"loadavg":              fmt.Sprintf("%.2f %.2f %.2f 2/345 6789\n", h.Load[0], h.Load[1], h.Load[2]),
		// user nice system idle iowait irq softirq steal guest guest_nice
		"stat": fmt.Sprintf("cpu  %d 0 %d %d %d 0 0 0 0 0\ncpu0 1 0 1 1 0 0 0 0 0 0\nintr 1234\nctxt 5678\n",
			h.CPUBusy/2, h.CPUBusy-h.CPUBusy/2, h.CPUIdle-h.CPUIdle/10, h.CPUIdle/10),
		"meminfo": fmt.Sprintf("MemTotal:       %d kB\nMemFree:         1024 kB\nMemAvailable:   %d kB\nBuffers:          512 kB\n"+
			"Cached:          2048 kB\nSwapCached:         0 kB\nSwapTotal:      %d kB\nSwapFree:       %d kB\n",
			h.MemTotal, h.MemAvailable, h.SwapTotal, h.SwapFree),
	}
	for name, content := range files {
		path := filepath.Join(fp.root, name)
		if name == h.Missing {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				fp.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fp.t.Fatal(err)
		}
		// Atomically, so the sampler never reads half a file.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			fp.t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			fp.t.Fatal(err)
		}
	}
}

// getJSON is GET path decoded as JSON, failing the test unless it is a
// 200.
func getJSON[T any](b *browser, path string) T {
	b.t.Helper()
	got := b.get(path)
	if got.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s = %d, want 200:\n%s", path, got.StatusCode, got.body)
	}
	var v T
	if err := json.Unmarshal([]byte(got.body), &v); err != nil {
		b.t.Fatalf("GET %s: %v\n%s", path, err, got.body)
	}
	return v
}

// node is GET /api/v1/node: the top bar and the stat band.
func (b *browser) node() httpapi.Node {
	b.t.Helper()
	return getJSON[httpapi.Node](b, "/api/v1/node")
}

// asJSON is v as JSON, for failure messages.
func asJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(out)
}

// near reports whether two ratios are the same, give or take rounding.
func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// ratioIs reports whether a ratio the API may leave out is want.
func ratioIs(got *float64, want float64) bool { return got != nil && near(*got, want) }

func sameSeries(got, want []float64) bool {
	return slices.EqualFunc(got, want, near)
}

// hostReading is what a sample changes on the Node endpoint: the host's
// uptime, or the error of a failed read.
func hostReading(n httpapi.Node) string {
	if n.Host == nil {
		return n.HostError
	}
	return fmt.Sprint(n.Host.UptimeSeconds, " ", n.HostError)
}

// sample makes the sampler take one sample of the fixture as it is now,
// a minute after the previous one, and waits until the Node endpoint
// gives it.
func (fp *fakeProc) sample(b *browser) {
	b.t.Helper()
	before := hostReading(b.node())
	fp.set(func(h *hostState) { h.Uptime += time.Minute })
	select {
	case fp.ticks <- time.Now():
	case <-time.After(5 * time.Second):
		b.t.Fatal("the sampler took no tick")
	}
	deadline := time.Now().Add(5 * time.Second)
	for hostReading(b.node()) == before {
		if time.Now().After(deadline) {
			b.t.Fatal("the Node endpoint never gave the new sample")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// serveOverview runs serve on the fixture host, with a healthy fake
// Engine as its Docker, and signs in, once the sampler's first sample is
// on the Node endpoint.
func serveOverview(t *testing.T, fp *fakeProc) (*running, *browser) {
	t.Helper()
	s, b, _ := serveWithEngine(t, fp)
	return s, b
}

// serveNodeOverview is serveOverview on a Node the test made, whose
// Docker it set up (or left unreachable).
func serveNodeOverview(t *testing.T, fp *fakeProc, world World) (*running, *browser) {
	t.Helper()
	s := startServeOn(t, serveEnv(t), world)
	b := signedIn(t, s)
	deadline := time.Now().Add(5 * time.Second)
	for b.node().Host == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the Node endpoint never gave the first sample:\n%s", b.get("/api/v1/node").body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s, b
}

func TestTheNodeEndpointGivesTheHostFromProc(t *testing.T) {
	fp := newFakeProc(t)
	fp.set(func(h *hostState) {
		h.Hostname = "web-01"
		h.Kernel = "6.8.0-49-generic"
		h.Uptime = 12*24*time.Hour + 4*time.Hour + 17*time.Minute + 33*time.Second
		h.Load = [3]float64{0.42, 0.38, 0.31}
	})
	s, b := serveOverview(t, fp)
	ip, _, _ := strings.Cut(s.addr(t, "dashboard"), ":")

	n := b.node()
	if n.IP != ip || n.Arch != runtime.GOARCH {
		t.Errorf("ip, arch = %q, %q, want %q, %q", n.IP, n.Arch, ip, runtime.GOARCH)
	}
	h := n.Host
	if h.Hostname != "web-01" || h.OS != "Linux 6.8.0-49-generic" {
		t.Errorf("hostname, os = %q, %q, want %q, %q", h.Hostname, h.OS, "web-01", "Linux 6.8.0-49-generic")
	}
	if want := int64(12*24*3600 + 4*3600 + 17*60 + 33); h.UptimeSeconds != want {
		t.Errorf("uptime_seconds = %d, want %d", h.UptimeSeconds, want)
	}
	if want := [3]float64{0.42, 0.38, 0.31}; h.Load != want {
		t.Errorf("load = %v, want %v", h.Load, want)
	}
}

func TestTheNodeEndpointGivesCPUFromTheSecondSampleMemoryAndDisk(t *testing.T) {
	fp := newFakeProc(t)
	fp.set(func(h *hostState) {
		h.CPUBusy, h.CPUIdle = 1000, 9000
		h.MemTotal, h.MemAvailable = 8<<20, 2<<20 // KiB: 6 of 8 GiB used
		h.DiskTotal, h.DiskFree = 100<<30, 25<<30
	})
	_, b := serveOverview(t, fp)

	// One sample has no CPU delta yet.
	if cpu := b.node().Host.CPU; cpu.Ratio != nil || len(cpu.History) != 0 {
		t.Errorf("cpu after one sample = %s, want no ratio and no history", asJSON(cpu))
	}

	// 300 of the next 1000 ticks busy.
	fp.set(func(h *hostState) { h.CPUBusy, h.CPUIdle = 1300, 9700 })
	fp.sample(b)

	h := b.node().Host
	if !ratioIs(h.CPU.Ratio, 0.3) {
		t.Errorf("cpu = %s, want ratio 0.3", asJSON(h.CPU))
	}
	if want := (httpapi.MemoryTile{Total: 8 << 30, Available: 2 << 30, Used: 6 << 30}); h.Memory.Total != want.Total || h.Memory.Available != want.Available || h.Memory.Used != want.Used {
		t.Errorf("memory = %s, want total 8 GiB, available 2 GiB, used 6 GiB", asJSON(h.Memory))
	}
	// Docker's root dir is on a filesystem of its own, so / is there too.
	wantDisks := []httpapi.DiskTile{{Path: "/var/lib/docker", Total: 200 << 30, Free: 150 << 30}, {Path: "/", Total: 100 << 30, Free: 25 << 30}}
	if !slices.Equal(h.Disks, wantDisks) {
		t.Errorf("disks = %s, want %s", asJSON(h.Disks), asJSON(wantDisks))
	}
}

func TestTheNodeEndpointKeepsTheLast120SamplesAsHistory(t *testing.T) {
	fp := newFakeProc(t)
	// Memory and CPU in steps of whole percents: 100 MiB of RAM, and 100
	// CPU ticks between samples.
	memUsed := func(k int) float64 { return float64(k*7%100) / 100 }
	cpuBusy := func(k int) uint64 { return uint64(k % 100) }
	fp.set(func(h *hostState) { h.MemTotal, h.MemAvailable = 100<<10, 100<<10 })
	_, b := serveOverview(t, fp)

	// The first sample, then 124 more: 125 in all, five more than the ring.
	var wantMem, wantCPU []float64
	for k := 1; k <= 124; k++ {
		fp.set(func(h *hostState) {
			h.MemAvailable = uint64(100-k*7%100) << 10
			h.CPUBusy += cpuBusy(k)
			h.CPUIdle += 100 - cpuBusy(k)
		})
		fp.sample(b)
		wantMem = append(wantMem, memUsed(k))
		wantCPU = append(wantCPU, float64(cpuBusy(k))/100)
		if k == 3 {
			// Before the ring is full the history is as long as the
			// samples; CPU has none for the first sample.
			h := b.node().Host
			if got, want := h.Memory.History, append([]float64{0}, wantMem...); !sameSeries(got, want) {
				t.Errorf("memory history after 4 samples = %v, want %v", got, want)
			}
			if got := h.CPU.History; !sameSeries(got, wantCPU) {
				t.Errorf("CPU history after 4 samples = %v, want %v", got, wantCPU)
			}
		}
	}

	h := b.node().Host
	if got, want := h.Memory.History, wantMem[len(wantMem)-120:]; !sameSeries(got, want) {
		t.Errorf("memory history has %d samples %v, want the last 120 %v", len(got), got, want)
	}
	if got, want := h.CPU.History, wantCPU[len(wantCPU)-120:]; !sameSeries(got, want) {
		t.Errorf("CPU history has %d samples %v, want the last 120 %v", len(got), got, want)
	}
}

// nodePaths are the Node overview's endpoints.
var nodePaths = []string{"/api/v1/node", "/api/v1/node/containers", "/api/v1/node/images", "/api/v1/node/docker", "/api/v1/node/events"}

func TestTheNodeEndpointsAnswer401WithoutALiveSession(t *testing.T) {
	clock := newFakeClock()
	fp := newFakeProc(t)
	world := fp.world(t)
	world.Clock = clock.Now
	newFakeEngine(t, world.DockerSocket)
	s, expired := serveNodeOverview(t, fp, world)
	anon := newBrowser(t, s)
	clock.advance(2 * time.Hour) // past the idle hour

	for name, b := range map[string]*browser{"no session": anon, "expired session": expired} {
		for _, path := range nodePaths {
			got := b.get(path)
			if got.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s: GET %s = %d, want 401", name, path, got.StatusCode)
			}
			for _, secret := range []string{"node-1", "postgres", "28.5.1"} {
				if strings.Contains(got.body, secret) {
					t.Errorf("%s: GET %s leaks the Node's data:\n%s", name, path, got.body)
				}
			}
		}
	}
}

// The status rules are the HTTP seam's (internal/httpapi); this checks
// that the real sampler's readings reach them.
func TestTheSamplersReadingsDecideTheNodeStatus(t *testing.T) {
	fp := newFakeProc(t)
	_, b := serveOverview(t, fp)

	for _, c := range []struct {
		name               string
		diskFree, memAvail uint64 // of 100 GiB disk and 100 MiB RAM
		want               httpapi.StatusLevel
		reasons            []string
	}{
		{name: "both under 10% free", diskFree: 9, memAvail: 9, want: httpapi.StatusWarn,
			reasons: []string{"Disk / has under 10% free", "Memory has under 10% available"}},
		{name: "recovered", diskFree: 50, memAvail: 50, want: httpapi.StatusOK, reasons: []string{"Healthy"}},
	} {
		fp.set(func(h *hostState) {
			h.DiskTotal, h.DiskFree = 100<<30, c.diskFree<<30
			h.MemTotal, h.MemAvailable = 100<<10, c.memAvail<<10
		})
		fp.sample(b)
		if got := b.node().Status; got.Level != c.want || !slices.Equal(got.Reasons, c.reasons) {
			t.Errorf("%s: status = %s, want %q with %q", c.name, asJSON(got), c.want, c.reasons)
		}
	}
}

func TestAFailingHostReadKeepsTheLastReadingAndTurnsTheStatusAmber(t *testing.T) {
	fp := newFakeProc(t)
	_, b := serveOverview(t, fp)
	good := b.node().Host

	fp.set(func(h *hostState) { h.Missing = "meminfo" })
	fp.sample(b)

	n := b.node()
	if n.HostError == "" {
		t.Errorf("host_error after a failed read is empty")
	}
	if n.Host == nil || n.Host.UptimeSeconds != good.UptimeSeconds || n.Host.Memory.Total != good.Memory.Total {
		t.Errorf("host after a failed read = %s, want the last good reading %s", asJSON(n.Host), asJSON(good))
	}
	if n.Status.Level != httpapi.StatusWarn || len(n.Status.Reasons) != 1 || !strings.HasPrefix(n.Status.Reasons[0], "Can't read the Node") {
		t.Errorf("status after a failed read = %s, want amber with a can't-read reason", asJSON(n.Status))
	}

	// The next good read clears it.
	fp.set(func(h *hostState) { h.Missing = "" })
	fp.sample(b)
	if n := b.node(); n.HostError != "" || n.Status.Level != httpapi.StatusOK {
		t.Errorf("after the read recovered host_error = %q, status = %s, want none and ok", n.HostError, asJSON(n.Status))
	}
}

func TestTheNodeEndpointGivesSwapFromMeminfo(t *testing.T) {
	fp := newFakeProc(t)
	_, b := serveOverview(t, fp)

	for _, step := range []struct {
		name                string
		total, free         uint64 // KiB
		wantTotal, wantUsed uint64 // bytes
	}{
		{name: "no swap"},
		{name: "swap unused", total: 2 << 20, free: 2 << 20, wantTotal: 2 << 30},
		{name: "swap in use", total: 2 << 20, free: 3 << 19, wantTotal: 2 << 30, wantUsed: 1 << 29},
	} {
		fp.set(func(h *hostState) { h.SwapTotal, h.SwapFree = step.total, step.free })
		fp.sample(b)
		m := b.node().Host.Memory
		if m.SwapTotal != step.wantTotal || m.SwapUsed != step.wantUsed {
			t.Errorf("%s: swap_total, swap_used = %d, %d, want %d, %d", step.name, m.SwapTotal, m.SwapUsed, step.wantTotal, step.wantUsed)
		}
	}
}
