package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake Docker Engine API: an HTTP server on the Node's Docker unix
// socket that answers the Engine API calls the Node overview makes from
// fixture data, and records what it was asked.

type engineContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Ports   []enginePort      `json:"Ports"`
	Health  *engineHealth     `json:"Health,omitempty"`
}

type enginePort struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

type engineHealth struct {
	Status string `json:"Status"`
}

// engineStats is what one-shot stats report for a container: cumulative
// CPU times in nanoseconds and memory in bytes.
type engineStats struct {
	CPUTotal, SystemCPU uint64
	OnlineCPUs          int
	MemUsage, MemLimit  uint64
	MemStats            map[string]uint64 // memory_stats.stats
}

type engineImage struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Size     int64    `json:"Size"`
	Created  int64    `json:"Created"`
}

type engineInfo struct {
	ServerVersion string   `json:"ServerVersion"`
	DockerRootDir string   `json:"DockerRootDir"`
	Driver        string   `json:"Driver"`
	CgroupVersion string   `json:"CgroupVersion"`
	CgroupDriver  string   `json:"CgroupDriver"`
	Warnings      []string `json:"Warnings"`
}

// engineState is the fixture the fake Engine serves.
type engineState struct {
	// APIVersion is the Api-Version /_ping reports.
	APIVersion string
	Info       engineInfo
	Containers []engineContainer
	Stats      map[string]engineStats // by container ID
	Images     []engineImage
	// DiskUsage is the /system/df body, as JSON.
	DiskUsage string
}

type fakeEngine struct {
	t      *testing.T
	socket string

	mu    sync.Mutex
	state engineState
	srv   *httptest.Server
	// calls counts requests by path without the version prefix.
	calls map[string]int
	// versions are the API version prefixes requests used, in order.
	versions []string
	// statsGate, when set, holds each stats request until it is closed.
	statsGate                  chan struct{}
	statsInFlight, maxInFlight int
}

func defaultEngineState() engineState {
	return engineState{
		APIVersion: "1.51",
		Info: engineInfo{
			ServerVersion: "28.5.1",
			DockerRootDir: "/var/lib/docker",
			Driver:        "overlay2",
			CgroupVersion: "2",
			CgroupDriver:  "systemd",
		},
		Containers: []engineContainer{
			{ID: "c1", Names: []string{"/db"}, Image: "postgres:17", ImageID: "sha256:pg", State: "running", Status: "Up 2 hours (healthy)", Health: &engineHealth{Status: "healthy"}},
			{ID: "c2", Names: []string{"/cache"}, Image: "redis:7", ImageID: "sha256:redis", State: "running", Status: "Up 2 hours"},
			{ID: "c3", Names: []string{"/migrate"}, Image: "postgres:17", ImageID: "sha256:pg", State: "exited", Status: "Exited (0) 2 hours ago"},
		},
		Stats: map[string]engineStats{},
		Images: []engineImage{
			{ID: "sha256:pg", RepoTags: []string{"postgres:17"}, Size: 450 << 20, Created: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).Unix()},
			{ID: "sha256:redis", RepoTags: []string{"redis:7"}, Size: 120 << 20, Created: time.Date(2026, 9, 15, 8, 30, 0, 0, time.UTC).Unix()},
		},
		DiskUsage: `{"ImageUsage":{"ActiveCount":2,"TotalCount":2,"TotalSize":0,"Reclaimable":0},
			"ContainerUsage":{"ActiveCount":2,"TotalCount":3,"TotalSize":0,"Reclaimable":0},
			"VolumeUsage":{"ActiveCount":0,"TotalCount":0,"TotalSize":0,"Reclaimable":0},
			"BuildCacheUsage":{"ActiveCount":0,"TotalCount":0,"TotalSize":0,"Reclaimable":0}}`,
	}
}

// newFakeEngine serves the default fixture on socket until the test ends.
func newFakeEngine(t *testing.T, socket string) *fakeEngine {
	t.Helper()
	fe := &fakeEngine{t: t, socket: socket, state: defaultEngineState(), calls: map[string]int{}}
	fe.open()
	t.Cleanup(fe.close)
	return fe
}

// set changes the fixture; the next request sees it.
func (fe *fakeEngine) set(change func(*engineState)) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	change(&fe.state)
}

// open starts serving on the socket.
func (fe *fakeEngine) open() {
	fe.t.Helper()
	os.Remove(fe.socket)
	ln, err := net.Listen("unix", fe.socket)
	if err != nil {
		fe.t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(fe.serve))
	srv.Listener = ln
	srv.Start()
	fe.mu.Lock()
	fe.srv = srv
	fe.mu.Unlock()
}

// close stops the Engine: the socket is gone, as when Docker is down.
func (fe *fakeEngine) close() {
	fe.mu.Lock()
	srv := fe.srv
	fe.srv = nil
	fe.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// isOpen reports whether the Engine is serving.
func (fe *fakeEngine) isOpen() bool {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.srv != nil
}

// callCount is how many requests reached path (without the version).
func (fe *fakeEngine) callCount(path string) int {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.calls[path]
}

var versionPrefix = regexp.MustCompile(`^/v(\d+\.\d+)(/.*)$`)

func (fe *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/_ping" {
		fe.mu.Lock()
		fe.calls[path]++
		v := fe.state.APIVersion
		fe.mu.Unlock()
		w.Header().Set("Api-Version", v)
		w.Write([]byte("OK"))
		return
	}
	m := versionPrefix.FindStringSubmatch(path)
	if m == nil {
		http.Error(w, `{"message":"page not found"}`, http.StatusNotFound)
		return
	}
	path = m[2]

	fe.mu.Lock()
	fe.calls[path]++
	fe.versions = append(fe.versions, m[1])
	s := fe.state
	fe.mu.Unlock()

	if id, ok := strings.CutPrefix(path, "/containers/"); ok && strings.HasSuffix(id, "/stats") {
		fe.serveStats(w, r, strings.TrimSuffix(id, "/stats"))
		return
	}
	switch path {
	case "/info":
		writeJSON(w, s.Info)
	case "/version":
		writeJSON(w, map[string]string{"Version": s.Info.ServerVersion, "ApiVersion": s.APIVersion, "MinAPIVersion": "1.24", "Os": "linux", "Arch": "amd64"})
	case "/containers/json":
		if r.URL.Query().Get("all") != "1" {
			http.Error(w, `{"message":"want all=1"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, s.Containers)
	case "/images/json":
		writeJSON(w, s.Images)
	case "/system/df":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(s.DiskUsage))
	default:
		http.Error(w, `{"message":"page not found"}`, http.StatusNotFound)
	}
}

func (fe *fakeEngine) serveStats(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	if q.Get("stream") != "false" || q.Get("one-shot") != "true" {
		http.Error(w, `{"message":"want one-shot stats"}`, http.StatusBadRequest)
		return
	}
	fe.mu.Lock()
	st, ok := fe.state.Stats[id]
	gate := fe.statsGate
	fe.statsInFlight++
	fe.maxInFlight = max(fe.maxInFlight, fe.statsInFlight)
	fe.mu.Unlock()
	defer func() {
		fe.mu.Lock()
		fe.statsInFlight--
		fe.mu.Unlock()
	}()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	if !ok {
		http.Error(w, `{"message":"No such container: `+id+`"}`, http.StatusNotFound)
		return
	}
	// One-shot stats leave precpu_stats empty.
	writeJSON(w, map[string]any{
		"id": id,
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": st.CPUTotal},
			"system_cpu_usage": st.SystemCPU,
			"online_cpus":      st.OnlineCPUs,
		},
		"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 0}},
		"memory_stats": map[string]any{"usage": st.MemUsage, "limit": st.MemLimit, "stats": st.MemStats},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// serveWithEngine runs serve on the fixture host with fe as its Docker
// and signs in, once the first sample is on the Node overview.
func serveWithEngine(t *testing.T, fp *fakeProc) (*running, *browser, *fakeEngine) {
	t.Helper()
	node := fp.node(t)
	fe := newFakeEngine(t, node.DockerSocket)
	s, b := serveNodeOverview(t, fp, node)
	return s, b, fe
}
