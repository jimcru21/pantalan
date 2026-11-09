package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// listedApp is what a test reads from one row of GET /api/v1/apps.
type listedApp struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Domain           string `json:"domain"`
	Branch           string `json:"branch"`
	LatestDeployment *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Commit string `json:"commit"`
	} `json:"latest_deployment"`
}

// createApp creates an App named name, served at <name>.example.test,
// through the API, and returns its ID.
func (s *seam) createApp(name string) string {
	s.t.Helper()
	body := fmt.Sprintf(`{"name":%q,"repo_url":"https://example.test/acme/%s.git","branch":"main","domain":"%s.example.test","port":8080}`, name, name, name)
	got := s.do(http.MethodPost, "/api/v1/apps", body, withToken(adminToken))
	if got.status != http.StatusCreated {
		s.t.Fatalf("create App %s = %d %q, want 201", name, got.status, got.body)
	}
	return field(s.t, got, "id").(string)
}

// deploy starts a Deployment of ref (the App's branch when empty) through
// the API and returns its ID. It stays queued until runQueued.
func (s *seam) deploy(appID, ref string) string {
	s.t.Helper()
	body := ""
	if ref != "" {
		body = fmt.Sprintf(`{"ref":%q}`, ref)
	}
	got := s.do(http.MethodPost, "/api/v1/apps/"+appID+"/deployments", body, withToken(adminToken))
	if got.status != http.StatusAccepted {
		s.t.Fatalf("deploy %s = %d %q, want 202", appID, got.status, got.body)
	}
	return field(s.t, got, "id").(string)
}

// listApps reads GET /api/v1/apps, failing unless it is a 200 JSON list.
func (s *seam) listApps(opts ...option) []listedApp {
	s.t.Helper()
	got := s.get("/api/v1/apps", opts...)
	if got.status != http.StatusOK {
		s.t.Fatalf("GET /api/v1/apps = %d %q, want 200", got.status, got.body)
	}
	var apps []listedApp
	if err := json.Unmarshal([]byte(got.body), &apps); err != nil {
		s.t.Fatalf("GET /api/v1/apps body %q: %v", got.body, err)
	}
	return apps
}

func TestTheAppsListCarriesEachAppsLatestDeploymentStatus(t *testing.T) {
	s := newSeam(t)
	idle := s.createApp("idle")
	up := s.createApp("up")
	broken := s.createApp("broken")

	upLive := s.deploy(up, "")
	brokenLive := s.deploy(broken, "v1")
	s.runQueued()
	brokenFailed := s.deploy(broken, "v2")
	s.builder.fail("compile error")
	s.runQueued()

	apps := s.listApps(withSession(s.signedIn()))
	if len(apps) != 3 {
		t.Fatalf("GET /api/v1/apps listed %d Apps, want 3: %+v", len(apps), apps)
	}
	byID := map[string]listedApp{}
	for _, a := range apps {
		byID[a.ID] = a
	}

	if a := byID[idle]; a.LatestDeployment != nil {
		t.Errorf("an App with no Deployments has latest_deployment %+v, want none", *a.LatestDeployment)
	}
	if a := byID[up]; a.LatestDeployment == nil || a.LatestDeployment.ID != upLive || a.LatestDeployment.Status != "live" {
		t.Errorf("a live App's latest_deployment = %+v, want %s live", a.LatestDeployment, upLive)
	}
	a := byID[broken]
	if a.LatestDeployment == nil || a.LatestDeployment.ID != brokenFailed || a.LatestDeployment.Status != "failed" {
		t.Errorf("an App that failed after going live has latest_deployment %+v, want %s failed (not the live %s)",
			a.LatestDeployment, brokenFailed, brokenLive)
	}
	if a.LatestDeployment != nil && a.LatestDeployment.Commit != "commit-of-v2" {
		t.Errorf("the failed Deployment's commit = %q, want commit-of-v2", a.LatestDeployment.Commit)
	}
}

func TestAnAppIsReadByItsIDWithItsDeploymentsNewestFirst(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	// Two Apps with the same name: only the ID tells them apart.
	first := s.createApp("web")
	other := s.do(http.MethodPost, "/api/v1/apps",
		`{"name":"web","repo_url":"https://example.test/acme/other.git","domain":"other.example.test","port":8080}`,
		withToken(adminToken))
	if other.status != http.StatusCreated {
		t.Fatalf("create a second App named web = %d %q, want 201", other.status, other.body)
	}
	older := s.deploy(first, "v1")
	s.runQueued()
	newer := s.deploy(first, "v2")

	got := s.get("/api/v1/apps/"+first, withSession(session))
	if got.status != http.StatusOK || field(t, got, "id") != first || field(t, got, "domain") != "web.example.test" {
		t.Errorf("GET /api/v1/apps/%s = %d %q, want 200 with that App", first, got.status, got.body)
	}

	got = s.get("/api/v1/apps/"+first+"/deployments", withSession(session))
	var deps []struct{ ID, Status string }
	if err := json.Unmarshal([]byte(got.body), &deps); got.status != http.StatusOK || err != nil {
		t.Fatalf("GET deployments = %d %q, want 200 with a JSON list", got.status, got.body)
	}
	if len(deps) != 2 || deps[0].ID != newer || deps[0].Status != "queued" || deps[1].ID != older || deps[1].Status != "live" {
		t.Errorf("deployments = %+v, want %s queued then %s live", deps, newer, older)
	}
}

func TestAnUnknownAppIDIsNotFound(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	for _, path := range []string{"/api/v1/apps/nosuchapp", "/api/v1/apps/nosuchapp/deployments"} {
		got := s.get(path, withSession(session))
		if got.status != http.StatusNotFound {
			t.Errorf("GET %s = %d %q, want 404", path, got.status, got.body)
		}
		if msg, _ := field(t, got, "error").(string); msg == "" {
			t.Errorf("GET %s body = %q, want an {\"error\"} message", path, got.body)
		}
	}
}
