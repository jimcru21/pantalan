package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
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

	// events is the daemon's event buffer, oldest first: the last
	// eventBuffer events, which a stream's since= replays from.
	events      []bufferedEvent
	nextSeq     int
	eventBuffer int
	// eventsChanged is closed and replaced by emit, waking the streams.
	eventsChanged chan struct{}
	// streamsDone is closed and replaced to end every open stream.
	streamsDone chan struct{}
	// eventsSince is the since= of each /events request, in order; ""
	// when there was none.
	eventsSince []string
}

// engineEvent is one message of the /events stream.
type engineEvent struct {
	Type     string      `json:"Type"`
	Action   string      `json:"Action"`
	Actor    engineActor `json:"Actor"`
	Scope    string      `json:"scope"`
	Time     int64       `json:"time"`
	TimeNano int64       `json:"timeNano"`
}

type engineActor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

type bufferedEvent struct {
	seq int
	ev  engineEvent
}

// containerEvent is a container event at at; attrs are more attribute
// key, value pairs.
func containerEvent(at time.Time, action, id, name, image string, attrs ...string) engineEvent {
	a := map[string]string{"name": name, "image": image}
	for i := 0; i+1 < len(attrs); i += 2 {
		a[attrs[i]] = attrs[i+1]
	}
	return engineEvent{
		Type: "container", Action: action, Scope: "local",
		Actor: engineActor{ID: id, Attributes: a},
		Time:  at.Unix(), TimeNano: at.UnixNano(),
	}
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
	fe := &fakeEngine{
		t: t, socket: socket, state: defaultEngineState(), calls: map[string]int{},
		eventBuffer: 256, eventsChanged: make(chan struct{}), streamsDone: make(chan struct{}),
	}
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
	fe.dropEvents()
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
	case "/events":
		fe.serveEvents(w, r)
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

// emit puts events in the daemon's buffer, which keeps the last
// eventBuffer, and sends them down every open stream they match.
func (fe *fakeEngine) emit(events ...engineEvent) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	for _, ev := range events {
		fe.events = append(fe.events, bufferedEvent{seq: fe.nextSeq, ev: ev})
		fe.nextSeq++
	}
	if n := len(fe.events) - fe.eventBuffer; n > 0 {
		fe.events = append(fe.events[:0], fe.events[n:]...)
	}
	close(fe.eventsChanged)
	fe.eventsChanged = make(chan struct{})
}

// dropEvents ends every open /events stream; the Engine stays up.
func (fe *fakeEngine) dropEvents() {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	close(fe.streamsDone)
	fe.streamsDone = make(chan struct{})
}

// streams is the since= of each /events request so far.
func (fe *fakeEngine) streams() []string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return slices.Clone(fe.eventsSince)
}

// waitForStreams waits until n /events requests have come in and
// returns their since= values.
func (fe *fakeEngine) waitForStreams(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := fe.streams(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d /events requests, want %d", len(fe.streams()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// serveEvents streams events as NDJSON the way dockerd does: the buffered
// ones from since= (inclusive) first, then each one as it's emitted, with
// the type and event filters applied.
func (fe *fakeEngine) serveEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var filters map[string]map[string]bool
	if err := json.Unmarshal([]byte(q.Get("filters")), &filters); err != nil || len(filters["type"]) != 1 || !filters["type"]["container"] {
		http.Error(w, `{"message":"want filters type=container"}`, http.StatusBadRequest)
		return
	}
	match := func(ev engineEvent) bool {
		if len(filters["event"]) == 0 {
			return true
		}
		// dockerd matches health_status and exec_* by prefix.
		for want := range filters["event"] {
			if ev.Action == want || strings.HasPrefix(ev.Action, want+":") {
				return true
			}
		}
		return false
	}
	since := q.Get("since")
	var sinceNano int64
	if since != "" {
		sec, frac, _ := strings.Cut(since, ".")
		s, err1 := strconv.ParseInt(sec, 10, 64)
		ns, err2 := strconv.ParseInt((frac + "000000000")[:9], 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, `{"message":"bad since"}`, http.StatusBadRequest)
			return
		}
		sinceNano = s*1e9 + ns
	}

	fe.mu.Lock()
	fe.eventsSince = append(fe.eventsSince, since)
	var replay []engineEvent
	if since != "" {
		for _, b := range fe.events {
			if b.ev.TimeNano >= sinceNano && match(b.ev) {
				replay = append(replay, b.ev)
			}
		}
	}
	next := fe.nextSeq
	done := fe.streamsDone
	fe.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flusher := w.(http.Flusher)
	for _, ev := range replay {
		enc.Encode(ev)
	}
	flusher.Flush()
	for {
		fe.mu.Lock()
		var fresh []engineEvent
		for _, b := range fe.events {
			if b.seq >= next && match(b.ev) {
				fresh = append(fresh, b.ev)
			}
		}
		next = fe.nextSeq
		changed := fe.eventsChanged
		fe.mu.Unlock()
		for _, ev := range fresh {
			enc.Encode(ev)
		}
		flusher.Flush()
		select {
		case <-changed:
		case <-done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// serveWithEngine runs serve on the fixture host with fe as its Docker
// and signs in, once the first sample is on the Node overview.
func serveWithEngine(t *testing.T, fp *fakeProc) (*running, *browser, *fakeEngine) {
	t.Helper()
	world := fp.world(t)
	fe := newFakeEngine(t, world.DockerSocket)
	s, b := serveNodeOverview(t, fp, world)
	return s, b, fe
}
