package httpapi_test

import (
	"maps"
	"net/http"
	"testing"
)

// Changing a live App's domain moves what the proxy serves: the new
// domain serves the live Deployment and the old one serves nothing.

// changeDomain PUTs the App's settings as createApp made them, with
// domain instead.
func (s *seam) changeDomain(appID, name, domain string) {
	s.t.Helper()
	body := `{"name":"` + name + `","repo_url":"https://example.test/acme/` + name + `.git","domain":"` + domain + `","port":8080}`
	if got := s.do(http.MethodPut, "/api/v1/apps/"+appID, body, withToken(adminToken)); got.status != http.StatusOK {
		s.t.Fatalf("PUT the App with domain %s = %d %q, want 200", domain, got.status, got.body)
	}
}

// upstreamOf is where the proxy reaches a Deployment's container.
func (s *seam) upstreamOf(appID, depID string) string {
	s.t.Helper()
	got := s.get("/api/v1/apps/"+appID+"/deployments/"+depID, withToken(adminToken))
	upstream, _ := field(s.t, got, "upstream").(string)
	if upstream == "" {
		s.t.Fatalf("Deployment %s has no upstream: %s", depID, got.body)
	}
	return upstream
}

func TestChangingALiveAppsDomainServesTheLiveDeploymentOnTheNewDomainAtOnce(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("shop")
	live := s.deploy(app, "")
	s.runQueued()

	s.changeDomain(app, "shop", "store.example.test")

	want := map[string]string{"store.example.test": s.upstreamOf(app, live)}
	if got := s.router.table(); !maps.Equal(got, want) {
		t.Errorf("routes after the domain change = %v, want %v: the old domain served nothing", got, want)
	}
}

func TestTheNextGoLiveAfterADomainChangeServesOnlyTheNewDomain(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("shop")
	s.deploy(app, "")
	s.runQueued()
	s.changeDomain(app, "shop", "store.example.test")

	next := s.deploy(app, "")
	s.runQueued()

	want := map[string]string{"store.example.test": s.upstreamOf(app, next)}
	if got := s.router.table(); !maps.Equal(got, want) {
		t.Errorf("routes after the next go-live = %v, want %v", got, want)
	}
}

// A Deployment that was already building when the domain changed goes
// live on the new domain, and the old one stops serving.
func TestADeploymentRunningThroughADomainChangeGoesLiveOnTheNewDomain(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("shop")
	s.deploy(app, "")
	s.runQueued()
	builds := s.builder.hold()
	next := s.deploy(app, "")
	ran := s.runQueuedInBackground()
	builds.started(t)

	s.changeDomain(app, "shop", "store.example.test")
	builds.finish()
	<-ran

	want := map[string]string{"store.example.test": s.upstreamOf(app, next)}
	if got := s.router.table(); !maps.Equal(got, want) {
		t.Errorf("routes after go-live = %v, want %v", got, want)
	}
}

func TestChangingTheDomainOfAnAppWithNothingLiveRoutesNothing(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("shop")

	s.changeDomain(app, "shop", "store.example.test")

	if got := s.router.table(); len(got) != 0 {
		t.Errorf("routes = %v, want none: nothing is live", got)
	}
}
