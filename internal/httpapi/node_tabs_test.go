package httpapi_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// The Node overview's tabs: one endpoint each.

func TestTheContainersEndpointListsEveryContainerWithItsAppAndStats(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	app, err := s.apps.Create(t.Context(), domain.App{
		Name: "shop", RepoURL: "https://example.test/acme/shop.git", Domain: "shop.example.test", Port: 8080,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Unix()
	s.docker.change(func(d *fakeDocker) {
		d.summary.Containers = []nodeinfo.Container{
			{
				ID: "c1", Names: []string{"/pantalan-shop-1"}, Image: "pantalan/shop:abc", Created: created,
				State: "running", Status: "Up 3 hours (healthy)", Health: &struct{ Status string }{"healthy"},
				Labels: map[string]string{domain.AppLabel: app.ID},
				Ports: []nodeinfo.Port{
					{IP: "127.0.0.1", PrivatePort: 8080, PublicPort: 49153, Type: "tcp"},
				},
			},
			{
				ID: "c2", Names: []string{"/db"}, Image: "postgres:17", Created: created,
				State: "running", Status: "Up 2 days",
				Ports: []nodeinfo.Port{
					{IP: "0.0.0.0", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"},
					{IP: "::", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"},
					{PrivatePort: 5433, Type: "tcp"},
				},
			},
			{
				ID: "c3", Names: []string{"/pantalan-gone-1"}, Image: "pantalan/gone:def", Created: created,
				State: "restarting", Status: "Restarting (1) 5 seconds ago",
				Labels: map[string]string{domain.AppLabel: "no-such-app"},
			},
		}
		d.stats = map[string]nodeinfo.ContainerStats{
			"c1": {CPUPercent: 12.5, HasCPU: true, MemUsed: 256 << 20, MemLimit: 1 << 30},
			"c2": {MemUsed: 64 << 20, MemLimit: 8 << 30},
		}
	})

	got := s.get("/api/v1/node/containers", withToken(adminToken))

	assertJSON(t, "GET /api/v1/node/containers", got, http.StatusOK, `[
		{
			"id": "c1", "name": "pantalan-shop-1", "image": "pantalan/shop:abc",
			"created": "2026-01-01T12:00:00Z",
			"state": "running", "health": "healthy", "status": "Up 3 hours (healthy)",
			"ports": [{"ip": "127.0.0.1", "private_port": 8080, "public_port": 49153, "type": "tcp"}],
			"app": {"id": "`+app.ID+`", "name": "shop"},
			"stats": {"cpu": 0.125, "memory_used": 268435456, "memory_limit": 1073741824}
		},
		{
			"id": "c2", "name": "db", "image": "postgres:17",
			"created": "2026-01-01T12:00:00Z",
			"state": "running", "status": "Up 2 days",
			"ports": [
				{"ip": "0.0.0.0", "private_port": 5432, "public_port": 5432, "type": "tcp"},
				{"private_port": 5433, "type": "tcp"}
			],
			"stats": {"memory_used": 67108864, "memory_limit": 8589934592}
		},
		{
			"id": "c3", "name": "pantalan-gone-1", "image": "pantalan/gone:def",
			"created": "2026-01-01T12:00:00Z",
			"state": "restarting", "status": "Restarting (1) 5 seconds ago", "trouble": "restarting",
			"ports": [],
			"app": {"id": "no-such-app"}
		}
	]`)
}

func TestTheImagesEndpointListsTagsSizeCreationAndUse(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.docker.change(func(d *fakeDocker) {
		d.images = []nodeinfo.Image{
			{ID: "sha256:pg", RepoTags: []string{"postgres:17", "postgres:latest"}, Size: 450 << 20, Created: time.Date(2025, 12, 1, 8, 30, 0, 0, time.UTC).Unix()},
			{ID: "sha256:dangling", Size: 12 << 20, Created: time.Date(2025, 11, 2, 0, 0, 0, 0, time.UTC).Unix()},
		}
	})

	got := s.get("/api/v1/node/images", withToken(adminToken))

	assertJSON(t, "GET /api/v1/node/images", got, http.StatusOK, `[
		{"id": "sha256:pg", "tags": ["postgres:17", "postgres:latest"], "size": 471859200, "created": "2025-12-01T08:30:00Z", "in_use": true},
		{"id": "sha256:dangling", "tags": [], "size": 12582912, "created": "2025-11-02T00:00:00Z", "in_use": false}
	]`)
}

func TestTheDockerEndpointGivesTheEngineItsWarningsAndDiskUsage(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.docker.change(func(d *fakeDocker) {
		d.summary.APIVersion = "1.47"
		d.summary.DaemonAPIVersion = "1.48"
		d.summary.Info.Warnings = []string{"No swap limit support"}
		d.version = nodeinfo.DockerVersion{Version: "27.3.1", APIVersion: "1.48", MinAPIVersion: "1.24"}
		d.diskUsage = nodeinfo.DiskUsage{
			Images:     nodeinfo.DiskUse{Total: 5, Active: 2, Size: 2 << 30, Reclaimable: 1 << 30},
			Containers: nodeinfo.DiskUse{Total: 3, Active: 1, Size: 10 << 20, Reclaimable: 4 << 20},
			Volumes:    nodeinfo.DiskUse{Total: 1, Active: 1, Size: 100 << 20},
			BuildCache: nodeinfo.DiskUse{Total: 7, Size: 300 << 20, Reclaimable: 300 << 20},
		}
	})

	got := s.get("/api/v1/node/docker", withToken(adminToken))

	assertJSON(t, "GET /api/v1/node/docker", got, http.StatusOK, `{
		"version": "27.3.1",
		"api_version": "1.47",
		"min_api_version": "1.24",
		"max_api_version": "1.48",
		"driver": "overlay2",
		"cgroup_version": "2",
		"cgroup_driver": "systemd",
		"root_dir": "/var/lib/docker",
		"warnings": ["No swap limit support"],
		"disk_usage": {
			"images":      {"total": 5, "active": 2, "size": 2147483648, "reclaimable": 1073741824},
			"containers":  {"total": 3, "active": 1, "size": 10485760, "reclaimable": 4194304},
			"volumes":     {"total": 1, "active": 1, "size": 104857600, "reclaimable": 0},
			"build_cache": {"total": 7, "active": 0, "size": 314572800, "reclaimable": 314572800},
			"at": "2026-01-02T03:04:05Z"
		}
	}`)
}

func TestTheDockerEndpointStillAnswersWhenDiskUsageFails(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.docker.change(func(d *fakeDocker) {
		d.version = nodeinfo.DockerVersion{Version: "27.3.1", MinAPIVersion: "1.24"}
		d.dfErr = errors.New("docker: /system/df: context deadline exceeded")
	})

	got := s.get("/api/v1/node/docker", withToken(adminToken))

	if got.status != http.StatusOK {
		t.Fatalf("GET /api/v1/node/docker = %d %q, want 200", got.status, got.body)
	}
	if v := field(t, got, "version"); v != "27.3.1" {
		t.Errorf("version = %v, want 27.3.1", v)
	}
	if v := field(t, got, "disk_usage"); v != nil {
		t.Errorf("disk_usage = %v, want none", v)
	}
	if v := field(t, got, "disk_usage_error"); v != "docker: /system/df: context deadline exceeded" {
		t.Errorf("disk_usage_error = %v, want the reason", v)
	}
}

func TestDiskUsageIsSharedBetweenRequestsFor60Seconds(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	at := func() any { return field(t, s.get("/api/v1/node/docker", withToken(adminToken)), "disk_usage.at") }

	first := at()
	s.clock.advance(59 * time.Second)
	if again := at(); again != first || s.docker.diskUsageCalls() != 1 {
		t.Fatalf("59 s later: disk usage at %v after %d system/df calls, want the shared one from %v after 1", again, s.docker.diskUsageCalls(), first)
	}
	s.clock.advance(time.Second)
	if fresh := at(); fresh != "2026-01-02T03:05:05Z" || s.docker.diskUsageCalls() != 2 {
		t.Errorf("60 s later: disk usage at %v after %d system/df calls, want a fresh one after 2", fresh, s.docker.diskUsageCalls())
	}
}

func TestAFailedDiskUsageIsNotShared(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.docker.change(func(d *fakeDocker) { d.dfErr = errors.New("docker: /system/df: timeout") })
	s.get("/api/v1/node/docker", withToken(adminToken))
	s.docker.change(func(d *fakeDocker) { d.dfErr = nil })

	got := s.get("/api/v1/node/docker", withToken(adminToken))

	if v := field(t, got, "disk_usage.at"); v != "2026-01-02T03:04:05Z" {
		t.Errorf("disk_usage.at after a failed call = %v, want a fresh one", v)
	}
}

func TestTheEventsEndpointListsRecentEventsNewestFirstWithGapMarkers(t *testing.T) {
	s := newSeam(t)
	at := func(sec int) int64 { return time.Date(2026, 1, 2, 3, 0, sec, 500, time.UTC).UnixNano() }
	event := func(nano int64, action, id string, attrs map[string]string, gap bool) nodeinfo.Event {
		ev := nodeinfo.Event{Action: action, TimeNano: nano, GapBefore: gap}
		ev.Actor.ID = id
		ev.Actor.Attributes = attrs
		return ev
	}
	s.events.set(nodeinfo.EventLog{Events: []nodeinfo.Event{
		event(at(4), "health_status: unhealthy", "c1", map[string]string{"name": "web", "image": "pantalan/web:abc"}, false),
		event(at(3), "die", "c2", map[string]string{"name": "db", "image": "postgres:17", "exitCode": "137"}, false),
		event(at(2), "kill", "c2", map[string]string{"name": "db", "image": "postgres:17", "signal": "9"}, true),
		event(at(1), "start", "c3", map[string]string{"image": "busybox"}, false),
	}})

	got := s.get("/api/v1/node/events", withToken(adminToken))

	assertJSON(t, "GET /api/v1/node/events", got, http.StatusOK, `{"events": [
		{"time": "2026-01-02T03:00:04.0000005Z", "container_id": "c1", "container": "web", "image": "pantalan/web:abc", "action": "health_status", "health": "unhealthy", "gap_before": false},
		{"time": "2026-01-02T03:00:03.0000005Z", "container_id": "c2", "container": "db", "image": "postgres:17", "action": "die", "exit_code": 137, "gap_before": false},
		{"time": "2026-01-02T03:00:02.0000005Z", "container_id": "c2", "container": "db", "image": "postgres:17", "action": "kill", "signal": "9", "gap_before": true},
		{"time": "2026-01-02T03:00:01.0000005Z", "container_id": "c3", "image": "busybox", "action": "start", "gap_before": false}
	]}`)
}

func TestWithDockerUnreachableTheDockerTabsSaySoAndEventsAreKept(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	ev := nodeinfo.Event{Action: "die", TimeNano: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC).UnixNano()}
	ev.Actor.ID = "c1"
	ev.Actor.Attributes = map[string]string{"name": "db", "exitCode": "1"}
	s.events.set(nodeinfo.EventLog{Events: []nodeinfo.Event{ev}, Err: errors.New("docker: event stream ended: EOF")})
	s.docker.change(func(d *fakeDocker) {
		d.down = errors.New("docker: dial unix /var/run/docker.sock: connect: connection refused")
	})

	for _, path := range []string{"/api/v1/node/containers", "/api/v1/node/images", "/api/v1/node/docker"} {
		got := s.get(path, withToken(adminToken))
		assertJSON(t, "GET "+path, got, http.StatusServiceUnavailable,
			`{"error": "Docker unreachable: docker: dial unix /var/run/docker.sock: connect: connection refused"}`)
	}
	got := s.get("/api/v1/node/events", withToken(adminToken))
	assertJSON(t, "GET /api/v1/node/events", got, http.StatusOK, `{
		"events": [{"time": "2026-01-02T03:00:00Z", "container_id": "c1", "container": "db", "action": "die", "exit_code": 1, "gap_before": false}],
		"stream_error": "docker: event stream ended: EOF"
	}`)

	// Once Docker is back, the tabs answer again.
	s.docker.change(func(d *fakeDocker) { d.down = nil })
	if got := s.get("/api/v1/node/containers", withToken(adminToken)); got.status != http.StatusOK {
		t.Errorf("GET /api/v1/node/containers with Docker back = %d %q, want 200", got.status, got.body)
	}
}

var nodeEndpoints = []string{
	"/api/v1/node",
	"/api/v1/node/containers",
	"/api/v1/node/images",
	"/api/v1/node/docker",
	"/api/v1/node/events",
}

func TestEveryNodeEndpointTakesTheAdminTokenOrTheSessionCookieAndNothingElse(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	session := s.signedIn()

	for _, path := range nodeEndpoints {
		for name, tc := range map[string]struct {
			opts []option
			want int
		}{
			"the Admin Token":   {[]option{withToken(adminToken)}, http.StatusOK},
			"a session cookie":  {[]option{withSession(session)}, http.StatusOK},
			"no credential":     {nil, http.StatusUnauthorized},
			"a wrong token":     {[]option{withToken("nope")}, http.StatusUnauthorized},
			"an unknown cookie": {[]option{withSession("NOSUCHSESSION")}, http.StatusUnauthorized},
		} {
			if got := s.get(path, tc.opts...); got.status != tc.want {
				t.Errorf("GET %s with %s = %d %q, want %d", path, name, got.status, got.body, tc.want)
			}
		}
	}
}
