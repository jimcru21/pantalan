package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The Node overview API's rules are tested at the HTTP seam
// (internal/httpapi); this checks only that serve wires the real Node
// information sources into it.
func TestServeWiresTheNodeSourcesIntoTheNodeOverviewAPI(t *testing.T) {
	fp := newFakeProc(t)
	_, b, _ := serveWithEngine(t, fp)

	got := b.get("/api/v1/node")
	if got.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/node = %d %q, want 200", got.StatusCode, got.body)
	}
	var node struct {
		Host struct {
			Hostname string `json:"hostname"`
		} `json:"host"`
		Docker struct {
			Reachable bool   `json:"reachable"`
			Version   string `json:"version"`
		} `json:"docker"`
	}
	if err := json.Unmarshal([]byte(got.body), &node); err != nil {
		t.Fatalf("GET /api/v1/node body %q: %v", got.body, err)
	}
	if node.Host.Hostname != "node-1" || !node.Docker.Reachable || node.Docker.Version != "28.5.1" {
		t.Errorf("GET /api/v1/node = %s, want the fixture host and the fake Engine", got.body)
	}

	for _, path := range []string{"/api/v1/node/containers", "/api/v1/node/images", "/api/v1/node/docker", "/api/v1/node/events"} {
		if got := b.get(path); got.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d %q, want 200", path, got.StatusCode, got.body)
		}
	}
}
