package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// redeploy asks for a new Deployment of an earlier Deployment's commit.
func (s *seam) redeploy(appID, deploymentID string, opts ...option) response {
	s.t.Helper()
	return s.do(http.MethodPost, "/api/v1/apps/"+appID+"/deployments/"+deploymentID+"/redeploy", "", opts...)
}

func TestRedeployingQueuesANewDeploymentOfTheEarlierCommitAndLeavesTheEarlierOneAlone(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")

	earlier := s.deploy(app, "v1")
	s.runQueued()
	s.deploy(app, "v2") // the bad release
	s.runQueued()
	earlierPath := "/api/v1/apps/" + app + "/deployments/" + earlier
	before := s.get(earlierPath, withSession(session))
	if d := decodeDeployment(t, "GET the earlier Deployment", before, http.StatusOK); d.Status != "superseded" || d.Commit != "commit-of-v1" {
		t.Fatalf("the earlier Deployment = %+v, want superseded at commit-of-v1", d)
	}

	got := s.redeploy(app, earlier, withSession(session))
	d := decodeDeployment(t, "redeploy", got, http.StatusAccepted)
	if d.ID == "" || d.ID == earlier || d.AppID != app || d.Ref != "commit-of-v1" ||
		d.Status != "queued" || d.Commit != "" || d.FinishedAt != nil {
		t.Fatalf("redeploy answered %+v, want a new queued Deployment of %s whose ref is commit-of-v1", d, app)
	}

	if after := s.get(earlierPath, withSession(session)); after.status != http.StatusOK || after.body != before.body {
		t.Errorf("after the redeploy the earlier Deployment = %d %q, want it unchanged: %q", after.status, after.body, before.body)
	}
	got = s.get("/api/v1/apps/"+app+"/deployments", withSession(session))
	var deps []deployment
	if err := json.Unmarshal([]byte(got.body), &deps); got.status != http.StatusOK || err != nil {
		t.Fatalf("GET deployments = %d %q, want 200 with a JSON list", got.status, got.body)
	}
	if len(deps) != 3 || deps[0].ID != d.ID || deps[2].ID != earlier {
		t.Errorf("history = %+v, want the redeploy newest, then the bad release, then the earlier Deployment", deps)
	}

	// Run, the redeploy puts the earlier commit live again.
	s.runQueued()
	got = s.get("/api/v1/apps/"+app+"/deployments/"+d.ID, withToken(adminToken))
	if live := decodeDeployment(t, "GET the redeploy", got, http.StatusOK); live.Status != "live" || live.Commit != "commit-of-v1" {
		t.Errorf("the redeploy = %+v, want live at commit-of-v1", live)
	}
	if after := s.get(earlierPath, withSession(session)); after.body != before.body {
		t.Errorf("once the redeploy is live the earlier Deployment = %q, want it unchanged: %q", after.body, before.body)
	}
}

func TestRedeployingWorksWithTheAdminToken(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	earlier := s.deploy(app, "v1")
	s.runQueued()

	got := s.redeploy(app, earlier, withToken(adminToken))
	if d := decodeDeployment(t, "redeploy with the token", got, http.StatusAccepted); d.Ref != "commit-of-v1" || d.Status != "queued" {
		t.Errorf("redeploy with the token = %+v, want a queued Deployment of commit-of-v1", d)
	}
}

func TestRedeployingADeploymentThatNeverResolvedACommitIsRefused(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")
	unfetched := s.deploy(app, unfetchableRef)
	s.runQueued()
	queued := s.deploy(app, "v1")

	for name, id := range map[string]string{"failed before fetching": unfetched, "still queued": queued} {
		t.Run(name, func(t *testing.T) {
			history := "/api/v1/apps/" + app + "/deployments"
			before := s.get(history, withSession(session))
			got := s.redeploy(app, id, withSession(session))
			if got.status != http.StatusConflict {
				t.Fatalf("redeploy = %d %q, want 409", got.status, got.body)
			}
			if msg, _ := field(t, got, "error").(string); !strings.Contains(msg, "never resolved a commit") {
				t.Errorf("redeploy error = %q, want it to say the Deployment never resolved a commit", msg)
			}
			if after := s.get(history, withSession(session)); after.body != before.body {
				t.Errorf("a refused redeploy changed the history from %q to %q", before.body, after.body)
			}
		})
	}
}

func TestRedeployingAnUnknownDeploymentIsNotFound(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")
	other := s.createApp("other")
	id := s.deploy(app, "v1")
	s.runQueued()

	for _, c := range []struct{ app, id string }{
		{app, "nosuchdeployment"},
		{other, id}, // another App's Deployment
		{"nosuchapp", id},
	} {
		got := s.redeploy(c.app, c.id, withSession(session))
		if got.status != http.StatusNotFound {
			t.Errorf("redeploy %s of %s = %d %q, want 404", c.id, c.app, got.status, got.body)
		}
		if msg, _ := field(t, got, "error").(string); msg == "" {
			t.Errorf("redeploy %s of %s body = %q, want an {\"error\"} message", c.id, c.app, got.body)
		}
	}
}
