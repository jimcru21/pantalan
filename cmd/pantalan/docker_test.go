package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/httpapi"
)

// The Node overview's Docker endpoints, end to end: the real Engine
// client against the fake Engine.

// containers is GET /api/v1/node/containers.
func (b *browser) containers() []httpapi.NodeContainer {
	b.t.Helper()
	return getJSON[[]httpapi.NodeContainer](b, "/api/v1/node/containers")
}

// byID is containers by their ID.
func byID(cs []httpapi.NodeContainer) map[string]httpapi.NodeContainer {
	out := map[string]httpapi.NodeContainer{}
	for _, c := range cs {
		out[c.ID] = c
	}
	return out
}

func TestTheNodeEndpointCountsContainersAndGivesDockersRootDisk(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)

	n := b.node()
	if want := (httpapi.ContainerCounts{Running: 2, Total: 3}); !n.Docker.Reachable || n.Docker.Version != "28.5.1" || n.Docker.Containers == nil || *n.Docker.Containers != want {
		t.Errorf("docker = %s, want reachable 28.5.1 with %s", asJSON(n.Docker), asJSON(want))
	}
	// / is on another filesystem, so it keeps its tile.
	wantDisks := []httpapi.DiskTile{{Path: "/var/lib/docker", Total: 200 << 30, Free: 150 << 30}, {Path: "/", Total: 100 << 30, Free: 50 << 30}}
	if !slices.Equal(n.Host.Disks, wantDisks) {
		t.Errorf("disks = %s, want %s", asJSON(n.Host.Disks), asJSON(wantDisks))
	}

	for _, c := range []struct {
		name          string
		makeUnhealthy func(*engineContainer)
	}{
		{name: "from its health", makeUnhealthy: func(c *engineContainer) { c.Health = &engineHealth{Status: "unhealthy"} }},
		// Before API 1.52 the list has no health; its status says it.
		{name: "from its status", makeUnhealthy: func(c *engineContainer) { c.Health, c.Status = nil, "Up 2 hours (unhealthy)" }},
	} {
		fe.set(func(s *engineState) {
			*s = defaultEngineState()
			c.makeUnhealthy(&s.Containers[1])
		})
		if got := b.node().Docker.Containers; got == nil || got.Unhealthy != 1 {
			t.Errorf("unhealthy %s: containers = %s, want 1 unhealthy", c.name, asJSON(got))
		}
	}

	// Docker's data on the filesystem holding /: one tile for both.
	fp.set(func(h *hostState) { h.DockerOnRoot = true })
	wantDisks = []httpapi.DiskTile{{Path: "/var/lib/docker", Total: 100 << 30, Free: 50 << 30}}
	if got := b.node().Host.Disks; !slices.Equal(got, wantDisks) {
		t.Errorf("disks with Docker's root dir on / = %s, want %s", asJSON(got), asJSON(wantDisks))
	}

	if n := fe.callCount("/system/df"); n != 0 {
		t.Errorf("the Node endpoint called system/df %d times, want never", n)
	}
}

// createApp creates an app through /api/v1 and returns its ID.
func createApp(t *testing.T, s *running, name string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"repo_url":"/nonexistent/%s.git","domain":"%s.example.test","port":8080}`, name, name, name)
	code, got := apiCall(t, s, http.MethodPost, "/api/v1/apps", body)
	if code != http.StatusCreated {
		t.Fatalf("create app = %d %s", code, got)
	}
	var app struct{ ID string }
	if err := json.Unmarshal(got, &app); err != nil {
		t.Fatal(err)
	}
	return app.ID
}

func TestTheContainersEndpointListsEveryContainerWithItsApp(t *testing.T) {
	fp := newFakeProc(t)
	s, b, fe := serveWithEngine(t, fp)
	appID := createApp(t, s, "shop")
	created := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)
	fe.set(func(st *engineState) {
		st.Containers = []engineContainer{
			{ID: "a1", Names: []string{"/pantalan-" + appID + "-d1"}, Image: "pantalan/" + appID + ":d1", Created: created.Unix(), State: "running", Status: "Up 3 hours",
				Labels: map[string]string{"pantalan.app": appID, "pantalan.deployment": "d1"},
				Ports:  []enginePort{{IP: "127.0.0.1", PrivatePort: 8080, PublicPort: 49153, Type: "tcp"}}},
			{ID: "c1", Names: []string{"/db"}, Image: "postgres:17", Created: created.Unix(), State: "running", Status: "Up 2 days (healthy)", Health: &engineHealth{Status: "healthy"},
				Ports: []enginePort{{PrivatePort: 5432, Type: "tcp"}}},
			{ID: "c2", Names: []string{"/proxy"}, Image: "nginx:1.27", Created: created.Unix(), State: "running", Status: "Up 5 minutes",
				Ports: []enginePort{
					{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8081, Type: "tcp"},
					{IP: "::", PrivatePort: 80, PublicPort: 8081, Type: "tcp"},
					{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 5353, Type: "udp"},
				}},
			{ID: "c3", Names: []string{"/migrate"}, Image: "postgres:17", Created: created.Unix(), State: "exited", Status: "Exited (1) 2 hours ago"},
			{ID: "c4", Names: []string{"/legacy"}, Labels: map[string]string{"pantalan.app": "gone"}, Image: "pantalan/gone:d0", Created: created.Unix(), State: "created", Status: "Created"},
		}
	})

	at := "2026-11-06T09:30:00Z"
	want := []httpapi.NodeContainer{
		{ID: "a1", Name: "pantalan-" + appID + "-d1", Image: "pantalan/" + appID + ":d1", Created: at, State: "running", Status: "Up 3 hours",
			Ports: []httpapi.ContainerPort{{IP: "127.0.0.1", PrivatePort: 8080, PublicPort: 49153, Type: "tcp"}},
			App:   &httpapi.ContainerApp{ID: appID, Name: "shop"}},
		{ID: "c1", Name: "db", Image: "postgres:17", Created: at, State: "running", Health: "healthy", Status: "Up 2 days (healthy)",
			Ports: []httpapi.ContainerPort{{PrivatePort: 5432, Type: "tcp"}}},
		// The IPv6 twin of a published port is left out.
		{ID: "c2", Name: "proxy", Image: "nginx:1.27", Created: at, State: "running", Status: "Up 5 minutes",
			Ports: []httpapi.ContainerPort{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8081, Type: "tcp"}, {IP: "0.0.0.0", PrivatePort: 53, PublicPort: 5353, Type: "udp"}}},
		{ID: "c3", Name: "migrate", Image: "postgres:17", Created: at, State: "exited", Status: "Exited (1) 2 hours ago", Ports: []httpapi.ContainerPort{}},
		// An app the store doesn't have keeps its label's ID.
		{ID: "c4", Name: "legacy", Image: "pantalan/gone:d0", Created: at, State: "created", Status: "Created", Ports: []httpapi.ContainerPort{},
			App: &httpapi.ContainerApp{ID: "gone"}},
	}
	if got := b.containers(); !reflect.DeepEqual(got, want) {
		t.Errorf("containers =\n%s\nwant\n%s", asJSON(got), asJSON(want))
	}
}

func TestContainerCPUIsADeltaFromTheFirstReadAndMemoryLeavesOutInactiveFile(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	const sec = uint64(time.Second)
	fe.set(func(st *engineState) {
		st.Stats = map[string]engineStats{
			// cgroup v2: 300 MiB used, 44 MiB of it inactive file.
			"c1": {CPUTotal: 1 * sec, SystemCPU: 100 * sec, OnlineCPUs: 4, MemUsage: 300 << 20, MemLimit: 1 << 30,
				MemStats: map[string]uint64{"inactive_file": 44 << 20}},
			// cgroup v1 reports total_inactive_file, which wins.
			"c2": {CPUTotal: 5 * sec, SystemCPU: 100 * sec, OnlineCPUs: 2, MemUsage: 80 << 20, MemLimit: 8 << 30,
				MemStats: map[string]uint64{"total_inactive_file": 16 << 20, "inactive_file": 1 << 20}},
		}
	})

	check := func(read string, wantCPU map[string]float64) {
		t.Helper()
		got := byID(b.containers())
		for id, wantMem := range map[string][2]uint64{"c1": {256 << 20, 1 << 30}, "c2": {64 << 20, 8 << 30}} {
			st := got[id].Stats
			if st == nil {
				t.Errorf("%s read: %s has no stats", read, id)
				continue
			}
			if st.MemoryUsed != wantMem[0] || st.MemoryLimit != wantMem[1] {
				t.Errorf("%s read: %s memory = %d of %d, want %d of %d", read, id, st.MemoryUsed, st.MemoryLimit, wantMem[0], wantMem[1])
			}
			if want, ok := wantCPU[id]; ok != (st.CPU != nil) || ok && !near(*st.CPU, want) {
				t.Errorf("%s read: %s stats = %s, want cpu %v (missing: %v)", read, id, asJSON(st), want, !ok)
			}
		}
		if st := got["c3"].Stats; st != nil {
			t.Errorf("%s read: the exited container has stats %s", read, asJSON(st))
		}
	}
	// The first read of a container has no delta.
	check("first", nil)

	fe.set(func(st *engineState) {
		c1, c2 := st.Stats["c1"], st.Stats["c2"]
		// Of the next 40 s of all CPUs' time, c1 used 2 s on 4 CPUs and
		// c2 0.5 s on 2.
		c1.CPUTotal, c1.SystemCPU = c1.CPUTotal+2*sec, c1.SystemCPU+40*sec
		c2.CPUTotal, c2.SystemCPU = c2.CPUTotal+sec/2, c2.SystemCPU+40*sec
		st.Stats["c1"], st.Stats["c2"] = c1, c2
	})
	check("next", map[string]float64{"c1": 0.2, "c2": 0.025})

	if n := fe.callCount("/containers/c3/stats"); n != 0 {
		t.Errorf("stats of the exited container were asked for %d times, want never", n)
	}
}

func TestNoMoreThan8StatsRequestsRunAtOnce(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	gate := make(chan struct{})
	fe.set(func(st *engineState) {
		st.Containers = nil
		for i := range 20 {
			id := fmt.Sprintf("c%02d", i)
			st.Containers = append(st.Containers, engineContainer{ID: id, Names: []string{"/" + id}, Image: "busybox", State: "running", Status: "Up 1 minute"})
			st.Stats[id] = engineStats{CPUTotal: 1, SystemCPU: 1, OnlineCPUs: 1, MemUsage: 1 << 20, MemLimit: 1 << 30}
		}
	})
	fe.mu.Lock()
	fe.statsGate = gate
	fe.mu.Unlock()

	done := make(chan page)
	go func() { done <- b.get("/api/v1/node/containers") }()
	inFlight := func() int {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		return fe.statsInFlight
	}
	deadline := time.Now().Add(5 * time.Second)
	for inFlight() < 8 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d stats requests ever ran at once, want 8 in parallel", inFlight())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // time for a 9th to start, if it would
	close(gate)
	got := <-done

	fe.mu.Lock()
	maxInFlight := fe.maxInFlight
	fe.mu.Unlock()
	if maxInFlight != 8 {
		t.Errorf("at most %d stats requests ran at once, want 8", maxInFlight)
	}
	for i := range 20 {
		if n := fe.callCount(fmt.Sprintf("/containers/c%02d/stats", i)); n != 1 {
			t.Errorf("container c%02d had %d stats requests, want 1", i, n)
		}
	}
	var cs []httpapi.NodeContainer
	if err := json.Unmarshal([]byte(got.body), &cs); err != nil || got.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/node/containers = %d %v:\n%s", got.StatusCode, err, got.body)
	}
	withStats := 0
	for _, c := range cs {
		if c.Stats != nil && c.Stats.MemoryUsed == 1<<20 {
			withStats++
		}
	}
	if len(cs) != 20 || withStats != 20 {
		t.Errorf("containers after the stats = %d, %d of them with memory, want 20 with memory", len(cs), withStats)
	}
}

func TestTheImagesEndpointListsImagesWithSizeCreatedAndUse(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.set(func(st *engineState) {
		st.Images = append(st.Images,
			engineImage{ID: "sha256:old", RepoTags: []string{"shop:1", "shop:latest"}, Size: 2 << 30, Created: time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC).Unix()},
			engineImage{ID: "sha256:dangling", Size: 512 << 10, Created: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()},
		)
	})

	// In use is by any container, running or not.
	want := []httpapi.NodeImage{
		{ID: "sha256:pg", Tags: []string{"postgres:17"}, Size: 450 << 20, Created: "2026-10-01T12:00:00Z", InUse: true},
		{ID: "sha256:redis", Tags: []string{"redis:7"}, Size: 120 << 20, Created: "2026-09-15T08:30:00Z", InUse: true},
		{ID: "sha256:old", Tags: []string{"shop:1", "shop:latest"}, Size: 2 << 30, Created: "2025-01-02T03:04:00Z"},
		{ID: "sha256:dangling", Tags: []string{}, Size: 512 << 10, Created: "2026-11-01T00:00:00Z"},
	}
	if got := getJSON[[]httpapi.NodeImage](b, "/api/v1/node/images"); !reflect.DeepEqual(got, want) {
		t.Errorf("images =\n%s\nwant\n%s", asJSON(got), asJSON(want))
	}
}

func TestWithDockerDownTheNodeEndpointKeepsTheHostAndTheDockerTabsAnswer503(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.close()

	fp.set(func(h *hostState) { h.CPUBusy, h.CPUIdle = h.CPUBusy+250, h.CPUIdle+750 })
	fp.sample(b) // the host keeps updating

	n := b.node()
	if !ratioIs(n.Host.CPU.Ratio, 0.25) || n.Host.Memory.Used != 4<<30 {
		t.Errorf("host with Docker down = %s, want cpu 0.25 and 4 GiB of memory used", asJSON(n.Host))
	}
	// Docker's root dir is unknown, so just /.
	if want := []httpapi.DiskTile{{Path: "/", Total: 100 << 30, Free: 50 << 30}}; !slices.Equal(n.Host.Disks, want) {
		t.Errorf("disks with Docker down = %s, want %s", asJSON(n.Host.Disks), asJSON(want))
	}
	if d := n.Docker; d.Reachable || d.Error == "" || d.Containers != nil || d.Version != "" {
		t.Errorf("docker while it is down = %s, want unreachable with an error", asJSON(d))
	}
	if n.Status.Level != httpapi.StatusDown || len(n.Status.Reasons) == 0 || !strings.HasPrefix(n.Status.Reasons[0], "Docker unreachable: ") {
		t.Errorf("status with Docker down = %s, want down, first because Docker is unreachable", asJSON(n.Status))
	}

	for _, path := range []string{"/api/v1/node/containers", "/api/v1/node/images", "/api/v1/node/docker"} {
		got := b.get(path)
		var body struct{ Error string }
		if err := json.Unmarshal([]byte(got.body), &body); got.StatusCode != http.StatusServiceUnavailable || err != nil || !strings.HasPrefix(body.Error, "Docker unreachable: ") {
			t.Errorf("GET %s with Docker down = %d %s, want 503 with a Docker unreachable error", path, got.StatusCode, got.body)
		}
	}
	if got := b.get("/api/v1/node/events"); got.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/node/events with Docker down = %d, want 200", got.StatusCode)
	}

	// Back up: the next read has the containers again.
	fe.open()
	if cs := b.containers(); len(cs) != 3 {
		t.Errorf("containers after Docker came back = %s, want the 3 containers", asJSON(cs))
	}
}

// usedVersions are the API versions the Engine's versioned requests
// used since the last call.
func (fe *fakeEngine) usedVersions() []string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	v := slices.Compact(slices.Sorted(slices.Values(fe.versions)))
	fe.versions = nil
	return v
}

// dockerInfo is GET /api/v1/node/docker.
func (b *browser) dockerInfo() httpapi.NodeDockerInfo {
	b.t.Helper()
	return getJSON[httpapi.NodeDockerInfo](b, "/api/v1/node/docker")
}

func TestTheAPIVersionIsWhatPingReportsCappedAtTheNewestPantalanKnows(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	visit := func() httpapi.NodeDockerInfo {
		t.Helper()
		b.node()
		b.containers()
		getJSON[[]httpapi.NodeImage](b, "/api/v1/node/images")
		return b.dockerInfo()
	}
	fe.set(func(st *engineState) {
		st.Stats["c1"] = engineStats{CPUTotal: 1, SystemCPU: 1, OnlineCPUs: 1, MemUsage: 1 << 20, MemLimit: 1 << 30}
	})
	fe.usedVersions()

	for _, c := range []struct {
		name, daemon string
		want         string
	}{
		{name: "a daemon newer than Pantalan", daemon: "1.99", want: "1.56"},
		// Its own version, as the daemon is upgraded or downgraded under
		// a running Pantalan.
		{name: "an older daemon", daemon: "1.48", want: "1.48"},
	} {
		fe.set(func(st *engineState) { st.APIVersion = c.daemon })
		info := visit()
		if got := fe.usedVersions(); !slices.Equal(got, []string{c.want}) {
			t.Errorf("with %s at API %s, requests used versions %q, want only %s", c.name, c.daemon, got, c.want)
		}
		if info.APIVersion != c.want || info.MinAPIVersion != "1.24" || info.MaxAPIVersion != c.daemon {
			t.Errorf("with %s, api versions = %s, %s–%s, want %s, 1.24–%s", c.name, info.APIVersion, info.MinAPIVersion, info.MaxAPIVersion, c.want, c.daemon)
		}
	}
}

func TestAnOlderDaemonsSystemDFIsWorkedOutFromItsItemLists(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	// Docker 28 (API 1.48–1.51) has no per-kind summaries in system/df.
	fe.set(func(st *engineState) {
		st.APIVersion = "1.48"
		st.DiskUsage = `{"LayersSize":1073741824,
			"Images":[{"Size":629145600,"SharedSize":104857600,"Containers":2},{"Size":314572800,"SharedSize":-1,"Containers":0}],
			"Containers":[{"SizeRw":4194304,"State":"running"},{"SizeRw":2097152,"State":"exited"}],
			"Volumes":[{"UsageData":{"Size":1073741824,"RefCount":1}},{"UsageData":{"Size":536870912,"RefCount":0}}],
			"BuildCache":[{"Size":104857600,"InUse":false,"Shared":false},{"Size":52428800,"InUse":true,"Shared":false}]}`
	})

	du := b.dockerInfo().DiskUsage
	if du == nil {
		t.Fatal("the Docker endpoint has no disk usage")
	}
	// As docker system df counts: an image's reclaimable space is the
	// layers no container's image needs, net of shared layers.
	want := httpapi.DockerDiskUsage{
		Images:     httpapi.DockerDiskUse{Total: 2, Active: 1, Size: 1 << 30, Reclaimable: 524 << 20},
		Containers: httpapi.DockerDiskUse{Total: 2, Active: 1, Size: 6 << 20, Reclaimable: 2 << 20},
		Volumes:    httpapi.DockerDiskUse{Total: 2, Active: 1, Size: 1536 << 20, Reclaimable: 512 << 20},
		BuildCache: httpapi.DockerDiskUse{Total: 2, Active: 1, Size: 150 << 20, Reclaimable: 100 << 20},
		At:         du.At,
	}
	if *du != want {
		t.Errorf("disk usage =\n%s\nwant\n%s", asJSON(du), asJSON(want))
	}
}

func TestTheDockerEndpointGivesTheEngineAndItsDiskUsage(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.set(func(st *engineState) {
		st.Info.Warnings = []string{"WARNING: No swap limit support", "WARNING: bridge-nf-call-iptables is disabled"}
		st.DiskUsage = `{
			"ImageUsage":{"ActiveCount":2,"TotalCount":3,"TotalSize":1610612736,"Reclaimable":536870912},
			"ContainerUsage":{"ActiveCount":2,"TotalCount":3,"TotalSize":10485760,"Reclaimable":1048576},
			"VolumeUsage":{"ActiveCount":1,"TotalCount":2,"TotalSize":3221225472,"Reclaimable":1073741824},
			"BuildCacheUsage":{"ActiveCount":0,"TotalCount":5,"TotalSize":838860800,"Reclaimable":838860800}}`
	})

	got := b.dockerInfo()
	if got.DiskUsage == nil {
		t.Fatalf("the Docker endpoint has no disk usage: %s", asJSON(got))
	}
	if _, err := time.Parse(time.RFC3339, got.DiskUsage.At); err != nil {
		t.Errorf("disk usage at = %q, want an RFC 3339 time", got.DiskUsage.At)
	}
	want := httpapi.NodeDockerInfo{
		Version:       "28.5.1",
		APIVersion:    "1.51",
		MinAPIVersion: "1.24",
		MaxAPIVersion: "1.51",
		Driver:        "overlay2",
		CgroupVersion: "2",
		CgroupDriver:  "systemd",
		RootDir:       "/var/lib/docker",
		Warnings:      []string{"WARNING: No swap limit support", "WARNING: bridge-nf-call-iptables is disabled"},
		DiskUsage: &httpapi.DockerDiskUsage{
			Images:     httpapi.DockerDiskUse{Total: 3, Active: 2, Size: 1536 << 20, Reclaimable: 512 << 20},
			Containers: httpapi.DockerDiskUse{Total: 3, Active: 2, Size: 10 << 20, Reclaimable: 1 << 20},
			Volumes:    httpapi.DockerDiskUse{Total: 2, Active: 1, Size: 3 << 30, Reclaimable: 1 << 30},
			BuildCache: httpapi.DockerDiskUse{Total: 5, Active: 0, Size: 800 << 20, Reclaimable: 800 << 20},
			At:         got.DiskUsage.At,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET /api/v1/node/docker =\n%s\nwant\n%s", asJSON(got), asJSON(want))
	}
}

// serveWithEngineAt is serveWithEngine with the Node's clock under the
// test's control.
func serveWithEngineAt(t *testing.T, fp *fakeProc, clock *fakeClock) (*running, *browser, *fakeEngine) {
	t.Helper()
	world := fp.world(t)
	world.Clock = clock.Now
	fe := newFakeEngine(t, world.DockerSocket)
	s, b := serveNodeOverview(t, fp, world)
	return s, b, fe
}

func TestSystemDFIsCalledAtMostOnceA60SecondsAcrossRequests(t *testing.T) {
	fp := newFakeProc(t)
	clock := newFakeClock()
	_, b, fe := serveWithEngineAt(t, fp, clock)
	start := clock.Now()

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { b.get("/api/v1/node/docker") })
	}
	wg.Wait()
	b.node()
	b.containers()
	if n := fe.callCount("/system/df"); n != 1 {
		t.Fatalf("5 Docker tabs at once made %d system/df calls, want 1", n)
	}

	clock.advance(59 * time.Second)
	if at := b.dockerInfo().DiskUsage.At; at != start.Format(time.RFC3339) {
		t.Errorf("a Docker tab 59 s later has disk usage at %s, want the shared one from %s", at, start.Format(time.RFC3339))
	}
	if n := fe.callCount("/system/df"); n != 1 {
		t.Errorf("a Docker tab 59 s later made %d system/df calls in all, want still 1", n)
	}
	clock.advance(2 * time.Second)
	b.dockerInfo()
	if at, want := b.dockerInfo().DiskUsage.At, clock.Now().Format(time.RFC3339); at != want {
		t.Errorf("a Docker tab 61 s later has disk usage at %s, want a fresh one at %s", at, want)
	}
	if n := fe.callCount("/system/df"); n != 2 {
		t.Errorf("Docker tabs 61 s later made %d system/df calls in all, want 2", n)
	}
}
