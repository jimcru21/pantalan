package main

import (
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

// sample makes the sampler take one sample of the fixture as it is now,
// a minute after the previous one, and waits until the Dashboard shows it.
func (fp *fakeProc) sample(b *browser) {
	b.t.Helper()
	before := b.fragment("/fragments/topbar").body
	fp.set(func(h *hostState) { h.Uptime += time.Minute })
	select {
	case fp.ticks <- time.Now():
	case <-time.After(5 * time.Second):
		b.t.Fatal("the sampler took no tick")
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.fragment("/fragments/topbar").body == before {
		if time.Now().After(deadline) {
			b.t.Fatal("the top bar never showed the new sample")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// serveOverview runs serve on the fixture host, with a healthy fake
// Engine as its Docker, and signs in, once the sampler's first sample is
// on the Node overview.
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
	for !strings.Contains(b.get("/").body, html.EscapeString(fp.state.Hostname)) {
		if time.Now().After(deadline) {
			t.Fatalf("the Node overview never showed the first sample:\n%s", b.get("/").body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s, b
}

// fragment is a GET the way htmx makes it when it polls.
func (b *browser) fragment(path string) page {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, b.base+path, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	return b.do(req)
}

var (
	tags   = regexp.MustCompile(`<[^>]*>`)
	spaces = regexp.MustCompile(`\s+`)
)

// text is the visible text of an HTML document, whitespace collapsed.
func text(doc string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(tags.ReplaceAllString(doc, " ")), " "))
}

func TestTheTopBarShowsTheNodeFromProc(t *testing.T) {
	fp := newFakeProc(t)
	fp.set(func(h *hostState) {
		h.Hostname = "web-01"
		h.Kernel = "6.8.0-49-generic"
		h.Uptime = 12*24*time.Hour + 4*time.Hour + 17*time.Minute + 33*time.Second
		h.Load = [3]float64{0.42, 0.38, 0.31}
	})
	s, b := serveOverview(t, fp)
	ip, _, _ := strings.Cut(s.addr(t, "dashboard"), ":")

	for _, path := range []string{"/", "/fragments/topbar"} {
		got := b.fragment(path)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, got.StatusCode)
		}
		for _, want := range []string{"web-01", ip, "Linux 6.8.0-49-generic", runtime.GOARCH, "up 12d 4h 17m", "load 0.42 0.38 0.31"} {
			if !strings.Contains(text(got.body), want) {
				t.Errorf("GET %s lacks %q:\n%s", path, want, text(got.body))
			}
		}
	}
}

func TestTheStatBandShowsCPUMemoryAndDisk(t *testing.T) {
	fp := newFakeProc(t)
	fp.set(func(h *hostState) {
		h.CPUBusy, h.CPUIdle = 1000, 9000
		h.MemTotal, h.MemAvailable = 8<<20, 2<<20 // KiB: 6 of 8 GiB used
		h.DiskTotal, h.DiskFree = 100<<30, 25<<30
	})
	_, b := serveOverview(t, fp)

	// One sample has no CPU delta yet.
	if got := text(b.fragment("/fragments/band").body); !strings.Contains(got, "CPU –") {
		t.Errorf("band after one sample lacks %q:\n%s", "CPU –", got)
	}

	// 300 of the next 1000 ticks busy.
	fp.set(func(h *hostState) { h.CPUBusy, h.CPUIdle = 1300, 9700 })
	fp.sample(b)

	for _, path := range []string{"/", "/fragments/band"} {
		got := b.fragment(path)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, got.StatusCode)
		}
		for _, want := range []string{"CPU 30%", "Memory 75% 6.0 of 8.0 GiB", "Disk / 25.0 GiB free of 100.0 GiB"} {
			if !strings.Contains(text(got.body), want) {
				t.Errorf("GET %s lacks %q:\n%s", path, want, text(got.body))
			}
		}
	}
}

// sparkline is the series a tile's sparkline draws, as percentages: its
// SVG is 100 high and a point at y is a value of 100 − y.
func sparkline(t *testing.T, doc, label string) []float64 {
	t.Helper()
	re := regexp.MustCompile(`<svg [^>]*aria-label="` + regexp.QuoteMeta(label) + `"[^>]*>\s*<polyline [^>]*points="([^"]*)"`)
	m := re.FindStringSubmatch(doc)
	if m == nil {
		t.Fatalf("no sparkline labelled %q in:\n%s", label, doc)
	}
	var values []float64
	for _, pt := range strings.Fields(m[1]) {
		_, y, ok := strings.Cut(pt, ",")
		v, err := strconv.ParseFloat(y, 64)
		if !ok || err != nil {
			t.Fatalf("sparkline %q has a bad point %q", label, pt)
		}
		values = append(values, 100-v)
	}
	return values
}

func sameSeries(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 0.05 {
			return false
		}
	}
	return true
}

func TestSparklinesDrawTheLast120Samples(t *testing.T) {
	fp := newFakeProc(t)
	// Memory and CPU in steps of whole percents: 100 MiB of RAM, and 100
	// CPU ticks between samples.
	memUsed := func(k int) float64 { return float64(k * 7 % 100) }
	cpuBusy := func(k int) float64 { return float64(k % 100) }
	fp.set(func(h *hostState) { h.MemTotal, h.MemAvailable = 100<<10, 100<<10 })
	_, b := serveOverview(t, fp)

	// The first sample, then 124 more: 125 in all, five more than the ring.
	var wantMem, wantCPU []float64
	for k := 1; k <= 124; k++ {
		fp.set(func(h *hostState) {
			h.MemAvailable = uint64(100-memUsed(k)) << 10
			h.CPUBusy += uint64(cpuBusy(k))
			h.CPUIdle += 100 - uint64(cpuBusy(k))
		})
		fp.sample(b)
		wantMem = append(wantMem, memUsed(k))
		wantCPU = append(wantCPU, cpuBusy(k))
		if k == 3 {
			// Before the ring is full the lines are as long as the history;
			// CPU has no point for the first sample.
			band := b.fragment("/fragments/band").body
			if got, want := sparkline(t, band, "Memory, last 10 minutes"), append([]float64{0}, wantMem...); !sameSeries(got, want) {
				t.Errorf("memory sparkline after 4 samples = %v, want %v", got, want)
			}
			if got := sparkline(t, band, "CPU, last 10 minutes"); !sameSeries(got, wantCPU) {
				t.Errorf("CPU sparkline after 4 samples = %v, want %v", got, wantCPU)
			}
		}
	}

	for _, path := range []string{"/", "/fragments/band"} {
		doc := b.fragment(path).body
		if got, want := sparkline(t, doc, "Memory, last 10 minutes"), wantMem[len(wantMem)-120:]; !sameSeries(got, want) {
			t.Errorf("GET %s: memory sparkline has %d points %v, want the last 120 samples %v", path, len(got), got, want)
		}
		if got, want := sparkline(t, doc, "CPU, last 10 minutes"), wantCPU[len(wantCPU)-120:]; !sameSeries(got, want) {
			t.Errorf("GET %s: CPU sparkline has %d points %v, want the last 120 samples %v", path, len(got), got, want)
		}
	}
}

var polled = regexp.MustCompile(`<(\w+) [^>]*hx-get="([^"]+)" hx-trigger="([^"]+)" hx-swap="([^"]+)"`)

func TestEachPolledRegionIsItsOwnFragmentThatKeepsPolling(t *testing.T) {
	_, b := serveOverview(t, newFakeProc(t))

	regions := polled.FindAllStringSubmatch(b.get("/").body, -1)
	var paths []string
	for _, m := range regions {
		paths = append(paths, m[2])
	}
	// The open tab polls with the top bar and the band.
	if want := []string{"/fragments/topbar", "/fragments/band", "/fragments/containers"}; !slices.Equal(paths, want) {
		t.Fatalf("the Node overview polls %q, want %q", paths, want)
	}
	for _, m := range regions {
		tag, path, trigger, swap := m[1], m[2], m[3], m[4]
		if trigger != "every 5s" || swap != "outerHTML" {
			t.Errorf("%s polls with hx-trigger=%q hx-swap=%q, want every 5s and outerHTML", path, trigger, swap)
		}
		got := b.fragment(path)
		if got.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, got.StatusCode)
			continue
		}
		if cc := got.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, cc)
		}
		if csp := got.Header.Get("Content-Security-Policy"); csp != wantCSP {
			t.Errorf("GET %s CSP = %q, want %q", path, csp, wantCSP)
		}
		// The fragment is the region alone, and the region again, so
		// swapping it in keeps the poll going.
		if strings.Contains(got.body, "<html") || strings.Contains(got.body, `class="sidebar"`) {
			t.Errorf("GET %s returns more than the region:\n%s", path, got.body)
		}
		again := polled.FindAllStringSubmatch(got.body, -1)
		if len(again) != 1 || again[0][1] != tag || again[0][2] != path || again[0][3] != trigger || again[0][4] != swap {
			t.Errorf("GET %s does not return the polling <%s> itself:\n%s", path, tag, got.body)
		}
		if strings.Contains(got.body, "style=") || strings.Contains(got.body, "<script") || strings.Contains(got.body, "hx-on") {
			t.Errorf("GET %s has inline style, script or hx-on:\n%s", path, got.body)
		}
	}
}

func TestAFragmentWithoutALiveSessionGets401AndHXRedirectToLogin(t *testing.T) {
	clock := newFakeClock()
	fp := newFakeProc(t)
	world := fp.world(t)
	world.Clock = clock.Now
	s := startServeOn(t, serveEnv(t), world)
	expired := signedIn(t, s)
	anon := newBrowser(t, s)
	clock.advance(2 * time.Hour) // past the idle hour

	for name, b := range map[string]*browser{"no session": anon, "expired session": expired} {
		for _, path := range []string{"/fragments/topbar", "/fragments/band", "/fragments/containers", "/fragments/images", "/fragments/docker", "/fragments/events"} {
			got := b.fragment(path)
			if got.StatusCode != http.StatusUnauthorized || got.Header.Get("HX-Redirect") != "/login" {
				t.Errorf("%s: GET %s = %d HX-Redirect %q, want 401 to /login", name, path, got.StatusCode, got.Header.Get("HX-Redirect"))
			}
			if cc := got.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("%s: GET %s Cache-Control = %q, want no-store", name, path, cc)
			}
			if strings.Contains(got.body, "node-1") {
				t.Errorf("%s: GET %s leaks the Node's data:\n%s", name, path, got.body)
			}
		}
	}
}

var statusDot = regexp.MustCompile(`<span class="dot (\w+)"[^>]* title="([^"]*)"`)

func TestTheStatusDotTurnsAmberUnder10PercentFreeDiskOrMemory(t *testing.T) {
	fp := newFakeProc(t)
	_, b := serveOverview(t, fp)
	const (
		diskReason = "Disk / has under 10% free"
		memReason  = "Memory has under 10% available"
	)

	for _, c := range []struct {
		name               string
		diskFree, memAvail uint64 // of 100 GiB disk and 100 MiB RAM
		want               string
		reasons            []string
	}{
		{name: "both at 10%", diskFree: 10, memAvail: 10, want: "ok", reasons: []string{"Healthy"}},
		{name: "disk under", diskFree: 9, memAvail: 50, want: "warn", reasons: []string{diskReason}},
		{name: "memory under", diskFree: 50, memAvail: 9, want: "warn", reasons: []string{memReason}},
		{name: "both under", diskFree: 1, memAvail: 1, want: "warn", reasons: []string{diskReason, memReason}},
		{name: "recovered", diskFree: 50, memAvail: 50, want: "ok", reasons: []string{"Healthy"}},
	} {
		fp.set(func(h *hostState) {
			h.DiskTotal, h.DiskFree = 100<<30, c.diskFree<<30
			h.MemTotal, h.MemAvailable = 100<<10, c.memAvail<<10
		})
		fp.sample(b)
		for _, path := range []string{"/", "/fragments/topbar"} {
			m := statusDot.FindStringSubmatch(b.fragment(path).body)
			if m == nil {
				t.Fatalf("%s: GET %s has no status dot", c.name, path)
			}
			colour, tooltip := m[1], html.UnescapeString(m[2])
			if colour != c.want {
				t.Errorf("%s: GET %s status dot is %q, want %q (tooltip %q)", c.name, path, colour, c.want, tooltip)
			}
			if got := strings.Split(tooltip, "\n"); !slices.Equal(got, c.reasons) {
				t.Errorf("%s: GET %s tooltip lists %q, want %q", c.name, path, got, c.reasons)
			}
		}
	}
}

func TestAFailingHostReadTurnsTheDotAmberRatherThanShowStaleData(t *testing.T) {
	fp := newFakeProc(t)
	_, b := serveOverview(t, fp)

	fp.set(func(h *hostState) { h.Missing = "meminfo" })
	fp.sample(b)

	m := statusDot.FindStringSubmatch(b.fragment("/fragments/topbar").body)
	if m == nil || m[1] != "warn" || !strings.Contains(html.UnescapeString(m[2]), "Can't read the host") {
		t.Errorf("status dot after a failed read = %q, want amber with a can't-read reason", m)
	}
}

func TestSwapShowsOnlyWhenInUse(t *testing.T) {
	fp := newFakeProc(t)
	fp.set(func(h *hostState) { h.SwapTotal, h.SwapFree = 0, 0 })
	_, b := serveOverview(t, fp)

	for _, step := range []struct {
		name        string
		total, free uint64 // KiB
		want        string // "" means no swap line at all
	}{
		{name: "no swap", total: 0, free: 0},
		{name: "swap unused", total: 2 << 20, free: 2 << 20},
		{name: "swap in use", total: 2 << 20, free: 3 << 19, want: "Swap 0.5 of 2.0 GiB"},
	} {
		fp.set(func(h *hostState) { h.SwapTotal, h.SwapFree = step.total, step.free })
		fp.sample(b)
		got := text(b.fragment("/fragments/band").body)
		if step.want == "" && strings.Contains(got, "Swap") {
			t.Errorf("%s: band shows swap:\n%s", step.name, got)
		}
		if step.want != "" && !strings.Contains(got, step.want) {
			t.Errorf("%s: band lacks %q:\n%s", step.name, step.want, got)
		}
	}
}
