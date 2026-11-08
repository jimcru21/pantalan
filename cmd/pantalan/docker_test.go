package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// text is GET path's visible text, failing the test unless it is a 200.
func (b *browser) text(path string) string {
	b.t.Helper()
	got := b.fragment(path)
	if got.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s = %d, want 200:\n%s", path, got.StatusCode, got.body)
	}
	return text(got.body)
}

func TestTheStatBandShowsContainersDockerAndDockersRootDisk(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)

	for _, path := range []string{"/", "/fragments/band"} {
		got := b.text(path)
		for _, want := range []string{
			"Containers 2/3 running",
			"Docker 28.5.1",
			"Disk /var/lib/docker 150.0 GiB free of 200.0 GiB",
			// / is on another filesystem, so it keeps its tile.
			"Disk / 50.0 GiB free of 100.0 GiB",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("GET %s lacks %q:\n%s", path, want, got)
			}
		}
		if strings.Contains(got, "unhealthy") {
			t.Errorf("GET %s counts unhealthy containers when there are none:\n%s", path, got)
		}
	}

	fe.set(func(s *engineState) {
		s.Containers[1].Status = "Up 2 hours (unhealthy)"
		s.Containers[1].Health = &engineHealth{Status: "unhealthy"}
	})
	if got, want := b.text("/fragments/band"), "Containers 2/3 running 1 unhealthy"; !strings.Contains(got, want) {
		t.Errorf("band lacks %q:\n%s", want, got)
	}

	// Docker's data on the filesystem holding /: one tile for both.
	fp.set(func(h *hostState) { h.DockerOnRoot = true })
	got := b.text("/fragments/band")
	if want := "Disk /var/lib/docker 50.0 GiB free of 100.0 GiB"; !strings.Contains(got, want) {
		t.Errorf("band lacks %q:\n%s", want, got)
	}
	if strings.Contains(got, "Disk / ") {
		t.Errorf("band shows / as well as the Docker root dir on the same filesystem:\n%s", got)
	}

	if n := fe.callCount("/system/df"); n != 0 {
		t.Errorf("the band called system/df %d times, want never", n)
	}
}

var tableRow = regexp.MustCompile(`(?s)<tr[ >].*?</tr>`)

// tableRows is the visible text of each body row of the tables in doc.
func tableRows(doc string) []string {
	var rows []string
	for _, tr := range tableRow.FindAllString(doc, -1) {
		if !strings.Contains(tr, "<th") {
			rows = append(rows, text(tr))
		}
	}
	return rows
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

var badge = regexp.MustCompile(`<span class="badge"[^>]*>([^<]*)</span>`)

func TestTheContainersTabListsEveryContainerAndBadgesPantalanApps(t *testing.T) {
	fp := newFakeProc(t)
	s, b, fe := serveWithEngine(t, fp)
	appID := createApp(t, s, "shop")
	fe.set(func(st *engineState) {
		st.Containers = []engineContainer{
			{ID: "a1", Names: []string{"/pantalan-" + appID + "-d1"}, Image: "pantalan/" + appID + ":d1", State: "running", Status: "Up 3 hours",
				Labels: map[string]string{"pantalan.app": appID, "pantalan.deployment": "d1"},
				Ports:  []enginePort{{IP: "127.0.0.1", PrivatePort: 8080, PublicPort: 49153, Type: "tcp"}}},
			{ID: "c1", Names: []string{"/db"}, Image: "postgres:17", State: "running", Status: "Up 2 days (healthy)", Health: &engineHealth{Status: "healthy"},
				Ports: []enginePort{{PrivatePort: 5432, Type: "tcp"}}},
			{ID: "c2", Names: []string{"/proxy"}, Image: "nginx:1.27", State: "running", Status: "Up 5 minutes",
				Ports: []enginePort{
					{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8081, Type: "tcp"},
					{IP: "::", PrivatePort: 80, PublicPort: 8081, Type: "tcp"},
					{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 5353, Type: "udp"},
				}},
			{ID: "c3", Names: []string{"/migrate"}, Image: "postgres:17", State: "exited", Status: "Exited (1) 2 hours ago"},
			{ID: "c4", Names: []string{"/legacy"}, Labels: map[string]string{"pantalan.app": "gone"}, Image: "pantalan/gone:d0", State: "created", Status: "Created"},
		}
	})

	want := []string{
		// name, app badge, image, state/health, ports, uptime
		"pantalan-" + appID + "-d1 shop pantalan/" + appID + ":d1 running 127.0.0.1:49153→8080/tcp 3 hours",
		"db postgres:17 running · healthy 5432/tcp 2 days",
		// The IPv6 twin of a published port is left out.
		"proxy nginx:1.27 running 0.0.0.0:8081→80/tcp, 0.0.0.0:5353→53/udp 5 minutes",
		"migrate postgres:17 Exited (1) 2 hours ago – –",
		// An app the store doesn't have is badged with its label.
		"legacy gone pantalan/gone:d0 Created – –",
	}
	for _, path := range []string{"/", "/fragments/containers"} {
		got := b.fragment(path)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, got.StatusCode)
		}
		rows := tableRows(got.body)
		if len(rows) != len(want) {
			t.Fatalf("GET %s has %d container rows, want %d:\n%s", path, len(rows), len(want), strings.Join(rows, "\n"))
		}
		for i := range want {
			if !strings.HasPrefix(rows[i], want[i]) {
				t.Errorf("GET %s row %d = %q, want it to start %q", path, i, rows[i], want[i])
			}
		}
		badges := badge.FindAllStringSubmatch(got.body, -1)
		if len(badges) != 2 || badges[0][1] != "shop" || badges[1][1] != "gone" {
			t.Errorf("GET %s badges = %q, want shop and gone", path, badges)
		}
	}
}

func TestContainerCPUIsADeltaFromTheFirstViewAndMemoryLeavesOutInactiveFile(t *testing.T) {
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

	cpuAndMemory := func(path string) []string {
		t.Helper()
		rows := tableRows(b.fragment(path).body)
		if len(rows) != 3 {
			t.Fatalf("GET %s has %d rows, want 3: %q", path, len(rows), rows)
		}
		return rows
	}
	first := cpuAndMemory("/")
	for i, want := range []string{"– 256.0 MiB / 1.0 GiB", "– 64.0 MiB / 8.0 GiB", "– –"} {
		if !strings.HasSuffix(first[i], want) {
			t.Errorf("first view, row %d = %q, want it to end %q", i, first[i], want)
		}
	}

	fe.set(func(st *engineState) {
		c1, c2 := st.Stats["c1"], st.Stats["c2"]
		// Of the next 40 s of all CPUs' time, c1 used 2 s on 4 CPUs and
		// c2 0.5 s on 2.
		c1.CPUTotal, c1.SystemCPU = c1.CPUTotal+2*sec, c1.SystemCPU+40*sec
		c2.CPUTotal, c2.SystemCPU = c2.CPUTotal+sec/2, c2.SystemCPU+40*sec
		st.Stats["c1"], st.Stats["c2"] = c1, c2
	})
	next := cpuAndMemory("/fragments/containers")
	for i, want := range []string{"20.0% 256.0 MiB / 1.0 GiB", "2.5% 64.0 MiB / 8.0 GiB", "– –"} {
		if !strings.HasSuffix(next[i], want) {
			t.Errorf("next view, row %d = %q, want it to end %q", i, next[i], want)
		}
	}
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
	go func() { done <- b.fragment("/fragments/containers") }()
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
	if rows := tableRows(got.body); len(rows) != 20 || !strings.HasSuffix(rows[19], "1.0 MiB / 1.0 GiB") {
		t.Errorf("containers tab after the stats = %q, want 20 rows with memory", rows)
	}
}

var tabButton = regexp.MustCompile(`<button [^>]*role="tab"[^>]*aria-selected="(true|false)"[^>]*hx-get="/fragments/(\w+)"[^>]*>`)

// openTab is the tab a fragment's tab bar marks selected, and the tabs
// it offers.
func openTab(t *testing.T, doc string) (string, []string) {
	t.Helper()
	var selected string
	var all []string
	for _, m := range tabButton.FindAllStringSubmatch(doc, -1) {
		all = append(all, m[2])
		if m[1] == "true" {
			selected = m[2]
		}
	}
	return selected, all
}

func TestTheImagesTabListsImagesWithSizeCreatedAndUseAndPollsEvery30s(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.set(func(st *engineState) {
		st.Images = append(st.Images,
			engineImage{ID: "sha256:old", RepoTags: []string{"shop:1", "shop:latest"}, Size: 2 << 30, Created: time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC).Unix()},
			engineImage{ID: "sha256:dangling", Size: 512 << 10, Created: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()},
		)
	})

	// The tab bar of the page's open tab offers it.
	if open, all := openTab(t, b.get("/").body); open != "containers" || !slices.Contains(all, "images") {
		t.Fatalf("the page's tab bar has %q open of %q, want containers with images on offer", open, all)
	}

	got := b.fragment("/fragments/images")
	if got.StatusCode != http.StatusOK {
		t.Fatalf("GET /fragments/images = %d", got.StatusCode)
	}
	if open, _ := openTab(t, got.body); open != "images" {
		t.Errorf("the images tab marks %q open", open)
	}
	want := []string{
		"postgres:17 450.0 MiB 2026-10-01 In use",
		"redis:7 120.0 MiB 2026-09-15 In use",
		"shop:1, shop:latest 2.0 GiB 2025-01-02 Unused",
		"<none>:<none> 512.0 KiB 2026-11-01 Unused",
	}
	if rows := tableRows(got.body); !slices.Equal(rows, want) {
		t.Errorf("images tab rows =\n%q\nwant\n%q", rows, want)
	}
	regions := polled.FindAllStringSubmatch(got.body, -1)
	if len(regions) != 1 || regions[0][2] != "/fragments/images" || regions[0][3] != "every 30s" || regions[0][4] != "outerHTML" {
		t.Errorf("the images tab polls %q, want itself every 30s", regions)
	}
}

func TestTheStatusDotIsRedWhenDockerIsUnreachableAndAmberForUnhealthyRestartingOrWarnings(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)

	for _, c := range []struct {
		name    string
		engine  func(*engineState)
		host    func(*hostState)
		down    bool // the fake socket closed
		want    string
		reasons []string // a reason ending in "…" is a prefix
	}{
		{name: "healthy", want: "ok", reasons: []string{"Healthy"}},
		{name: "unhealthy container", want: "warn", reasons: []string{"Container cache is unhealthy"}, engine: func(s *engineState) {
			s.Containers[1].Status, s.Containers[1].Health = "Up 2 hours (unhealthy)", &engineHealth{Status: "unhealthy"}
		}},
		{name: "unhealthy before API 1.52, from the status", want: "warn", reasons: []string{"Container cache is unhealthy"}, engine: func(s *engineState) {
			s.Containers[1].Status = "Up 2 hours (unhealthy)"
		}},
		{name: "restarting container", want: "warn", reasons: []string{"Container cache is restarting"}, engine: func(s *engineState) {
			s.Containers[1].State, s.Containers[1].Status = "restarting", "Restarting (1) 3 seconds ago"
		}},
		{name: "Docker warnings", want: "warn", reasons: []string{"Docker: WARNING: No swap limit support"}, engine: func(s *engineState) {
			s.Info.Warnings = []string{"WARNING: No swap limit support"}
		}},
		{name: "Docker's disk under 10% free", want: "warn", reasons: []string{"Disk /var/lib/docker has under 10% free"}, host: func(h *hostState) {
			h.DockerDiskFree = h.DockerDiskTotal / 20
		}},
		{name: "Docker unreachable", down: true, want: "down", reasons: []string{"Docker unreachable: …"}},
		{name: "red beats amber", down: true, want: "down", reasons: []string{"Docker unreachable: …", "Memory has under 10% available"}, host: func(h *hostState) {
			h.MemAvailable = h.MemTotal / 20
		}},
		{name: "recovered", want: "ok", reasons: []string{"Healthy"}},
	} {
		fe.set(func(s *engineState) {
			*s = defaultEngineState()
			if c.engine != nil {
				c.engine(s)
			}
		})
		fp.set(func(h *hostState) {
			h.MemTotal, h.MemAvailable = 8<<20, 4<<20
			h.DockerDiskTotal, h.DockerDiskFree = 200<<30, 150<<30
			if c.host != nil {
				c.host(h)
			}
		})
		if c.down {
			fe.close()
		} else if !fe.isOpen() {
			fe.open()
		}
		fp.sample(b)

		for _, path := range []string{"/", "/fragments/topbar"} {
			m := statusDot.FindStringSubmatch(b.fragment(path).body)
			if m == nil {
				t.Fatalf("%s: GET %s has no status dot", c.name, path)
			}
			colour, reasons := m[1], strings.Split(html.UnescapeString(m[2]), "\n")
			if colour != c.want {
				t.Errorf("%s: GET %s status dot is %q, want %q (tooltip %q)", c.name, path, colour, c.want, reasons)
			}
			match := len(reasons) == len(c.reasons)
			for i := 0; match && i < len(reasons); i++ {
				if prefix, ok := strings.CutSuffix(c.reasons[i], "…"); ok {
					match = strings.HasPrefix(reasons[i], prefix)
				} else {
					match = reasons[i] == c.reasons[i]
				}
			}
			if !match {
				t.Errorf("%s: GET %s tooltip lists %q, want %q", c.name, path, reasons, c.reasons)
			}
		}
	}
}

func TestWithDockerDownTheHostTilesStillRenderAndDockerShowsUnreachable(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.close()

	fp.set(func(h *hostState) { h.CPUBusy, h.CPUIdle = h.CPUBusy+250, h.CPUIdle+750 })
	fp.sample(b) // the host keeps updating

	band := b.text("/fragments/band")
	for _, want := range []string{"CPU 25%", "Memory 50% 4.0 of 8.0 GiB", "Disk / 50.0 GiB free of 100.0 GiB", "Containers Docker unreachable", "Docker Docker unreachable"} {
		if !strings.Contains(band, want) {
			t.Errorf("band with Docker down lacks %q:\n%s", want, band)
		}
	}
	if strings.Contains(band, "/var/lib/docker") {
		t.Errorf("band with Docker down shows Docker's root dir:\n%s", band)
	}

	for _, path := range []string{"/", "/fragments/containers", "/fragments/images", "/fragments/docker"} {
		got := b.fragment(path)
		if got.StatusCode != http.StatusOK {
			t.Fatalf("GET %s with Docker down = %d, want 200", path, got.StatusCode)
		}
		cards := strings.Count(got.body, `class="panel empty" role="alert"`)
		if cards != 1 || !strings.Contains(text(got.body), "Docker unreachable") || !strings.Contains(got.body, "<code>systemctl status docker</code>") {
			t.Errorf("GET %s with Docker down has %d error cards, want one with the systemctl hint:\n%s", path, cards, text(got.body))
		}
		if _, all := openTab(t, got.body); len(all) != 4 {
			t.Errorf("GET %s with Docker down offers tabs %q, want all four", path, all)
		}
	}

	// Back up: the open tab's next poll shows the containers again.
	fe.open()
	if rows := tableRows(b.fragment("/fragments/containers").body); len(rows) != 3 {
		t.Errorf("containers tab after Docker came back has rows %q, want the 3 containers", rows)
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

func TestTheAPIVersionIsWhatPingReportsCappedAtTheNewestPantalanKnows(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	visit := func() string {
		t.Helper()
		for _, path := range []string{"/", "/fragments/topbar", "/fragments/band", "/fragments/containers", "/fragments/images"} {
			b.fragment(path)
		}
		return b.text("/fragments/docker")
	}
	fe.set(func(st *engineState) {
		st.Stats["c1"] = engineStats{CPUTotal: 1, SystemCPU: 1, OnlineCPUs: 1, MemUsage: 1 << 20, MemLimit: 1 << 30}
	})
	fe.usedVersions()

	// A daemon newer than Pantalan.
	fe.set(func(st *engineState) { st.APIVersion = "1.99" })
	doc := visit()
	if got := fe.usedVersions(); !slices.Equal(got, []string{"1.56"}) {
		t.Errorf("with a daemon at API 1.99, requests used versions %q, want only the cap 1.56", got)
	}
	if want := "API version 1.56 daemon accepts 1.24–1.99"; !strings.Contains(doc, want) {
		t.Errorf("Docker tab lacks %q:\n%s", want, doc)
	}

	// An older daemon: its own version, as the daemon is upgraded or
	// downgraded under a running Pantalan.
	fe.set(func(st *engineState) { st.APIVersion = "1.48" })
	doc = visit()
	if got := fe.usedVersions(); !slices.Equal(got, []string{"1.48"}) {
		t.Errorf("with a daemon at API 1.48, requests used versions %q, want 1.48", got)
	}
	if want := "API version 1.48 daemon accepts 1.24–1.48"; !strings.Contains(doc, want) {
		t.Errorf("Docker tab lacks %q:\n%s", want, doc)
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

	// As docker system df counts: an image's reclaimable space is the
	// layers no container's image needs, net of shared layers.
	want := []string{
		"Images 2 1 1.0 GiB 524.0 MiB",
		"Containers 2 1 6.0 MiB 2.0 MiB",
		"Volumes 2 1 1.5 GiB 512.0 MiB",
		"Build cache 2 1 150.0 MiB 100.0 MiB",
	}
	if rows := tableRows(b.fragment("/fragments/docker").body); !slices.Equal(rows, want) {
		t.Errorf("Docker tab disk usage =\n%q\nwant\n%q", rows, want)
	}
}

// serveWithEngineAt is serveWithEngine with the Node's clock under the
// test's control.
func serveWithEngineAt(t *testing.T, fp *fakeProc, clock *fakeClock) (*running, *browser, *fakeEngine) {
	t.Helper()
	node := fp.node(t)
	node.Clock = clock.Now
	fe := newFakeEngine(t, node.DockerSocket)
	s, b := serveNodeOverview(t, fp, node)
	return s, b, fe
}

func TestTheDockerTabShowsTheEngineAndItsDiskUsageAndLoadsOnlyWhenOpened(t *testing.T) {
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

	if open, all := openTab(t, b.get("/").body); open != "containers" || !slices.Contains(all, "docker") {
		t.Fatalf("the page's tab bar has %q open of %q, want containers with docker on offer", open, all)
	}
	if n := fe.callCount("/system/df"); n != 0 {
		t.Errorf("the page called system/df %d times before the Docker tab was opened", n)
	}

	got := b.fragment("/fragments/docker")
	if got.StatusCode != http.StatusOK {
		t.Fatalf("GET /fragments/docker = %d", got.StatusCode)
	}
	if open, _ := openTab(t, got.body); open != "docker" {
		t.Errorf("the Docker tab marks %q open", open)
	}
	doc := text(got.body)
	for _, want := range []string{
		"Version 28.5.1",
		"API version 1.51",
		"Storage driver overlay2",
		"Cgroup v2 (systemd)",
		"Root dir /var/lib/docker",
		"WARNING: No swap limit support",
		"WARNING: bridge-nf-call-iptables is disabled",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("Docker tab lacks %q:\n%s", want, doc)
		}
	}
	// kind, total, active, size, reclaimable
	wantRows := []string{
		"Images 3 2 1.5 GiB 512.0 MiB",
		"Containers 3 2 10.0 MiB 1.0 MiB",
		"Volumes 2 1 3.0 GiB 1.0 GiB",
		"Build cache 5 0 800.0 MiB 800.0 MiB",
	}
	if rows := tableRows(got.body); !slices.Equal(rows, wantRows) {
		t.Errorf("Docker tab disk usage =\n%q\nwant\n%q", rows, wantRows)
	}

	// It doesn't poll; a Refresh button reloads it.
	if regions := polled.FindAllStringSubmatch(got.body, -1); len(regions) != 0 {
		t.Errorf("the Docker tab polls: %q", regions)
	}
	if !regexp.MustCompile(`<button [^>]*hx-get="/fragments/docker"[^>]*>\s*Refresh\s*</button>`).MatchString(got.body) {
		t.Errorf("the Docker tab has no Refresh button:\n%s", got.body)
	}

	fe.set(func(st *engineState) { st.Info.Warnings = nil })
	if doc := b.text("/fragments/docker"); !strings.Contains(doc, "Warnings None") {
		t.Errorf("Docker tab without warnings lacks %q:\n%s", "Warnings None", doc)
	}
}

func TestSystemDFIsCalledAtMostOnceA60SecondsAcrossRequests(t *testing.T) {
	fp := newFakeProc(t)
	clock := newFakeClock()
	_, b, fe := serveWithEngineAt(t, fp, clock)

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { b.fragment("/fragments/docker") })
	}
	wg.Wait()
	b.fragment("/fragments/band")
	b.fragment("/")
	if n := fe.callCount("/system/df"); n != 1 {
		t.Fatalf("5 Docker tabs at once made %d system/df calls, want 1", n)
	}

	clock.advance(59 * time.Second)
	b.fragment("/fragments/docker")
	if n := fe.callCount("/system/df"); n != 1 {
		t.Errorf("a Docker tab 59 s later made %d system/df calls in all, want still 1", n)
	}
	clock.advance(2 * time.Second)
	b.fragment("/fragments/docker")
	b.fragment("/fragments/docker")
	if n := fe.callCount("/system/df"); n != 2 {
		t.Errorf("Docker tabs 61 s later made %d system/df calls in all, want 2", n)
	}
}
