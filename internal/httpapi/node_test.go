package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/nodeinfo"
)

const gib = 1 << 30

// sample is one host reading as the sampler stores it: 8 GiB of memory
// with availGiB available, and / on filesystem 1 with 100 GiB, 50 free.
func sample(cpu float64, hasCPU bool, availGiB uint64) nodeinfo.Sample {
	return nodeinfo.Sample{
		Reading: nodeinfo.Reading{
			Hostname: "node-1",
			OS:       "Linux 6.8.0-49-generic",
			Uptime:   26*time.Hour + 3*time.Minute + 30*time.Second,
			Load:     [3]float64{0.5, 0.25, 0.125},
			Memory:   nodeinfo.Memory{Total: 8 * gib, Available: availGiB * gib},
			Disk:     nodeinfo.Disk{Path: "/", Total: 100 * gib, Free: 50 * gib, FSID: fsid(1)},
		},
		CPUPercent: cpu,
		HasCPU:     hasCPU,
	}
}

// healthyNode is a Node with three host samples and a Docker whose data
// lives on a filesystem of its own, running one of its two containers.
func healthyNode(s *seam) {
	s.host.set(nodeinfo.Snapshot{History: []nodeinfo.Sample{
		sample(0, false, 4),
		sample(50, true, 4),
		sample(25, true, 2),
	}})
	s.docker.change(func(d *fakeDocker) {
		d.summary = nodeinfo.DockerSummary{
			APIVersion:       "1.47",
			DaemonAPIVersion: "1.47",
			Info: nodeinfo.DockerInfo{
				ServerVersion: "27.3.1",
				DockerRootDir: "/var/lib/docker",
				Driver:        "overlay2",
				CgroupVersion: "2",
				CgroupDriver:  "systemd",
			},
			Containers: []nodeinfo.Container{
				{ID: "c1", Names: []string{"/db"}, Image: "postgres:17", ImageID: "sha256:pg", State: "running", Status: "Up 3 hours"},
				{ID: "c2", Names: []string{"/old"}, Image: "busybox", ImageID: "sha256:bb", State: "exited", Status: "Exited (0) 2 days ago"},
			},
			RootDisk: nodeinfo.Disk{Path: "/var/lib/docker", Total: 200 * gib, Free: 150 * gib, FSID: fsid(2)},
		}
	})
}

// assertJSON fails unless got is status with a JSON body equal to want.
func assertJSON(t *testing.T, what string, got response, status int, want string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("%s = %d %q, want %d", what, got.status, got.body, status)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s Content-Type = %q, want application/json", what, ct)
	}
	var gotV, wantV any
	if err := json.Unmarshal([]byte(got.body), &gotV); err != nil {
		t.Fatalf("%s body %q is not JSON: %v", what, got.body, err)
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("want for %s is not JSON: %v", what, err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		gotPretty, _ := json.MarshalIndent(gotV, "", "  ")
		wantPretty, _ := json.MarshalIndent(wantV, "", "  ")
		t.Errorf("%s body:\n%s\nwant:\n%s", what, gotPretty, wantPretty)
	}
}

func TestTheNodeEndpointGivesHostFactsTheStatBandAndTheStatusAsRawValues(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)

	got := s.get("/api/v1/node", withToken(adminToken))

	assertJSON(t, "GET /api/v1/node", got, http.StatusOK, `{
		"ip": "127.0.0.1",
		"arch": "`+runtime.GOARCH+`",
		"status": {"level": "ok", "reasons": ["Healthy"]},
		"host": {
			"hostname": "node-1",
			"os": "Linux 6.8.0-49-generic",
			"uptime_seconds": 93810,
			"load": [0.5, 0.25, 0.125],
			"cpu": {"ratio": 0.25, "history": [0.5, 0.25]},
			"memory": {
				"total": 8589934592, "available": 2147483648, "used": 6442450944,
				"swap_total": 0, "swap_used": 0,
				"history": [0.5, 0.5, 0.75]
			},
			"disks": [
				{"path": "/var/lib/docker", "total": 214748364800, "free": 161061273600},
				{"path": "/", "total": 107374182400, "free": 53687091200}
			]
		},
		"docker": {
			"reachable": true,
			"version": "27.3.1",
			"containers": {"running": 1, "total": 2, "unhealthy": 0}
		}
	}`)
}

// field decodes the JSON body of got and returns the value at path, a
// dot-separated list of object keys.
func field(t *testing.T, got response, path string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(got.body), &v); err != nil {
		t.Fatalf("body %q is not JSON: %v", got.body, err)
	}
	for _, key := range strings.Split(path, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("body %s: %q is not under an object", got.body, key)
		}
		v = obj[key]
	}
	return v
}

// asJSON is v as compact JSON, for comparing a part of a body.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheStatusIsGreenAmberOrRedByTodaysRules(t *testing.T) {
	for name, tc := range map[string]struct {
		scene func(s *seam)
		want  string
	}{
		"green when nothing is wrong": {
			scene: func(*seam) {},
			want:  `{"level":"ok","reasons":["Healthy"]}`,
		},
		"amber when a disk has under a tenth free": {
			scene: func(s *seam) {
				s.docker.change(func(d *fakeDocker) { d.summary.RootDisk.Free = 20*gib - 1 })
			},
			want: `{"level":"warn","reasons":["Disk /var/lib/docker has under 10% free"]}`,
		},
		"green at exactly a tenth free": {
			scene: func(s *seam) {
				s.docker.change(func(d *fakeDocker) { d.summary.RootDisk.Free = 20 * gib })
			},
			want: `{"level":"ok","reasons":["Healthy"]}`,
		},
		"amber when memory has under a tenth available": {
			scene: func(s *seam) {
				smp := sample(25, true, 0)
				smp.Memory.Available = 800 << 20
				s.host.set(nodeinfo.Snapshot{History: []nodeinfo.Sample{smp}})
			},
			want: `{"level":"warn","reasons":["Memory has under 10% available"]}`,
		},
		"amber when the latest host read failed": {
			scene: func(s *seam) {
				snap := s.host.Snapshot()
				snap.Err = errors.New("read meminfo: no such file")
				s.host.set(snap)
			},
			want: `{"level":"warn","reasons":["Can't read the Node, so this is older data: read meminfo: no such file"]}`,
		},
		"amber for an unhealthy or restarting container": {
			scene: func(s *seam) {
				s.docker.change(func(d *fakeDocker) {
					d.summary.Containers = append(d.summary.Containers,
						nodeinfo.Container{ID: "c3", Names: []string{"/api"}, State: "running", Status: "Up 1 hour (unhealthy)"},
						nodeinfo.Container{ID: "c4", Names: []string{"/worker"}, State: "restarting", Status: "Restarting (1) 5 seconds ago"},
					)
				})
			},
			want: `{"level":"warn","reasons":["Container api is unhealthy","Container worker is restarting"]}`,
		},
		"amber for each warning Docker reports": {
			scene: func(s *seam) {
				s.docker.change(func(d *fakeDocker) { d.summary.Info.Warnings = []string{"No swap limit support"} })
			},
			want: `{"level":"warn","reasons":["Docker: No swap limit support"]}`,
		},
		"red, first, when Docker is unreachable": {
			scene: func(s *seam) {
				smp := sample(25, true, 0)
				smp.Memory.Available = 800 << 20
				s.host.set(nodeinfo.Snapshot{History: []nodeinfo.Sample{smp}})
				s.docker.change(func(d *fakeDocker) {
					d.down = errors.New("docker: dial unix /var/run/docker.sock: connect: no such file or directory")
				})
			},
			want: `{"level":"down","reasons":["Docker unreachable: docker: dial unix /var/run/docker.sock: connect: no such file or directory","Memory has under 10% available"]}`,
		},
		"grey before the host has a reading": {
			scene: func(s *seam) { s.host.set(nodeinfo.Snapshot{}) },
			want:  `{"level":"off","reasons":["No Node information yet"]}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			healthyNode(s)
			tc.scene(s)

			got := s.get("/api/v1/node", withToken(adminToken))
			if got.status != http.StatusOK {
				t.Fatalf("GET /api/v1/node = %d %q, want 200", got.status, got.body)
			}
			if status := asJSON(t, field(t, got, "status")); status != tc.want {
				t.Errorf("status = %s, want %s", status, tc.want)
			}
		})
	}
}

func TestWithDockerUnreachableTheNodeEndpointStillServesTheHost(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.docker.change(func(d *fakeDocker) { d.down = errors.New("docker: connection refused") })

	got := s.get("/api/v1/node", withToken(adminToken))

	if got.status != http.StatusOK {
		t.Fatalf("GET /api/v1/node = %d %q, want 200", got.status, got.body)
	}
	if hostname := field(t, got, "host.hostname"); hostname != "node-1" {
		t.Errorf("host.hostname = %v, want node-1", hostname)
	}
	if ratio := field(t, got, "host.cpu.ratio"); ratio != 0.25 {
		t.Errorf("host.cpu.ratio = %v, want 0.25", ratio)
	}
	// Docker's root dir is unknown, so the band shows just /.
	if disks := asJSON(t, field(t, got, "host.disks")); disks != `[{"free":53687091200,"path":"/","total":107374182400}]` {
		t.Errorf("host.disks = %s, want just /", disks)
	}
	if docker := asJSON(t, field(t, got, "docker")); docker != `{"error":"docker: connection refused","reachable":false}` {
		t.Errorf("docker = %s, want unreachable with its reason", docker)
	}
}

func TestTheBandShowsOnlyDockersRootDiskWhenItIsOnTheFilesystemOfSlash(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.docker.change(func(d *fakeDocker) { d.summary.RootDisk.FSID = fsid(1) })

	got := s.get("/api/v1/node", withToken(adminToken))

	if disks := asJSON(t, field(t, got, "host.disks")); disks != `[{"free":161061273600,"path":"/var/lib/docker","total":214748364800}]` {
		t.Errorf("host.disks = %s, want just Docker's root dir", disks)
	}
}

func TestBeforeTheFirstHostReadingTheNodeEndpointHasNoHost(t *testing.T) {
	s := newSeam(t)
	healthyNode(s)
	s.host.set(nodeinfo.Snapshot{Err: errors.New("read stat: permission denied")})

	got := s.get("/api/v1/node", withToken(adminToken))

	assertJSON(t, "GET /api/v1/node", got, http.StatusOK, `{
		"ip": "127.0.0.1",
		"arch": "`+runtime.GOARCH+`",
		"status": {"level": "off", "reasons": ["No Node information yet"]},
		"host_error": "read stat: permission denied",
		"docker": {"reachable": true, "version": "27.3.1", "containers": {"running": 1, "total": 2, "unhealthy": 0}}
	}`)
}
