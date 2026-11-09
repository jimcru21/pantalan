package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// deployment is what a test reads from one Deployment in a response.
type deployment struct {
	ID         string  `json:"id"`
	AppID      string  `json:"app_id"`
	Ref        string  `json:"ref"`
	Commit     string  `json:"commit"`
	Image      string  `json:"image"`
	Status     string  `json:"status"`
	Error      string  `json:"error"`
	CreatedAt  string  `json:"created_at"`
	FinishedAt *string `json:"finished_at"`
}

func decodeDeployment(t *testing.T, what string, got response, status int) deployment {
	t.Helper()
	if got.status != status {
		t.Fatalf("%s = %d %q, want %d", what, got.status, got.body, status)
	}
	var d deployment
	if err := json.Unmarshal([]byte(got.body), &d); err != nil {
		t.Fatalf("%s body %q: %v", what, got.body, err)
	}
	return d
}

func TestDeployingAnAppAnswersWithAQueuedDeployment(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")

	t.Run("of the App's branch when no ref is given", func(t *testing.T) {
		got := s.do(http.MethodPost, "/api/v1/apps/"+app+"/deployments", "", withSession(session))
		d := decodeDeployment(t, "deploy with no body", got, http.StatusAccepted)
		if d.ID == "" || d.AppID != app || d.Ref != "main" || d.Status != "queued" ||
			d.Commit != "" || d.CreatedAt == "" || d.FinishedAt != nil {
			t.Errorf("deploy answered %+v, want a queued Deployment of main for %s, not finished", d, app)
		}
	})

	t.Run("of the ref given", func(t *testing.T) {
		got := s.do(http.MethodPost, "/api/v1/apps/"+app+"/deployments", `{"ref":"v2"}`, withSession(session))
		if d := decodeDeployment(t, "deploy v2", got, http.StatusAccepted); d.Ref != "v2" || d.Status != "queued" {
			t.Errorf("deploy v2 answered %+v, want a queued Deployment of v2", d)
		}
	})

	t.Run("not for an unknown App", func(t *testing.T) {
		got := s.do(http.MethodPost, "/api/v1/apps/nosuchapp/deployments", "", withSession(session))
		if got.status != http.StatusNotFound {
			t.Errorf("deploy an unknown App = %d %q, want 404", got.status, got.body)
		}
	})
}

func TestTwoQuickDeploysQueueTwoDeploymentsInOrder(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")

	first := s.deploy(app, "")
	second := s.deploy(app, "")
	if first == second {
		t.Fatalf("two deploys answered the same Deployment %s", first)
	}

	got := s.get("/api/v1/apps/"+app+"/deployments", withSession(session))
	var deps []deployment
	if err := json.Unmarshal([]byte(got.body), &deps); got.status != http.StatusOK || err != nil {
		t.Fatalf("GET deployments = %d %q, want 200 with a JSON list", got.status, got.body)
	}
	if len(deps) != 2 || deps[0].ID != second || deps[1].ID != first ||
		deps[0].Status != "queued" || deps[1].Status != "queued" {
		t.Fatalf("deployments = %+v, want %s then %s, both queued", deps, second, first)
	}

	// Run in turn, the later one ends live and the earlier one superseded.
	s.runQueued()
	got = s.get("/api/v1/apps/"+app+"/deployments/"+first, withSession(session))
	if d := decodeDeployment(t, "GET the first", got, http.StatusOK); d.Status != "superseded" {
		t.Errorf("the first Deployment is %q, want superseded", d.Status)
	}
	got = s.get("/api/v1/apps/"+app+"/deployments/"+second, withSession(session))
	if d := decodeDeployment(t, "GET the second", got, http.StatusOK); d.Status != "live" {
		t.Errorf("the second Deployment is %q, want live", d.Status)
	}
}

func TestADeploymentIsReadByItsAppAndID(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")
	other := s.createApp("other")
	id := s.deploy(app, "v1")
	path := "/api/v1/apps/" + app + "/deployments/" + id

	got := s.get(path, withSession(session))
	if d := decodeDeployment(t, "GET a queued Deployment", got, http.StatusOK); d.ID != id || d.AppID != app || d.Status != "queued" {
		t.Errorf("GET %s = %+v, want %s queued", path, d, id)
	}

	s.runQueued()
	got = s.get(path, withToken(adminToken))
	d := decodeDeployment(t, "GET a live Deployment", got, http.StatusOK)
	if d.Status != "live" || d.Ref != "v1" || d.Commit != "commit-of-v1" || d.Image == "" ||
		d.CreatedAt == "" || d.FinishedAt == nil {
		t.Errorf("GET %s = %+v, want it live at commit-of-v1 with an image and its timing", path, d)
	}

	failed := s.deploy(app, "v2")
	s.builder.fail("compile error")
	s.runQueued()
	got = s.get("/api/v1/apps/"+app+"/deployments/"+failed, withSession(session))
	if d := decodeDeployment(t, "GET a failed Deployment", got, http.StatusOK); d.Status != "failed" || d.Error == "" {
		t.Errorf("GET the failed Deployment = %+v, want failed with its error", d)
	}

	for _, missing := range []string{
		"/api/v1/apps/" + app + "/deployments/nosuchdeployment",
		"/api/v1/apps/" + other + "/deployments/" + id, // another App's Deployment
		"/api/v1/apps/nosuchapp/deployments/" + id,
	} {
		got := s.get(missing, withSession(session))
		if got.status != http.StatusNotFound {
			t.Errorf("GET %s = %d %q, want 404", missing, got.status, got.body)
		}
		if msg, _ := field(t, got, "error").(string); msg == "" {
			t.Errorf("GET %s body = %q, want an {\"error\"} message", missing, got.body)
		}
	}
}
