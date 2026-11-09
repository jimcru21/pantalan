package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// appLogPath is the App Log stream of the App with ID app.
func appLogPath(app string) string { return "/api/v1/apps/" + app + "/log/stream" }

// liveApp creates an App and puts a Deployment of it live, returning the
// App's ID and its live container's.
func (s *seam) liveApp(name string) (app, container string) {
	s.t.Helper()
	app = s.createApp(name)
	s.deploy(app, "")
	s.runQueued()
	return app, s.liveContainer(app)
}

// liveContainer is the container of the App's live Deployment, as the
// API reports it.
func (s *seam) liveContainer(app string) string {
	s.t.Helper()
	got := s.get("/api/v1/apps/"+app+"/deployments", withToken(adminToken))
	if got.status != http.StatusOK {
		s.t.Fatalf("list Deployments of %s = %d %q", app, got.status, got.body)
	}
	var deps []struct {
		Status      string `json:"status"`
		ContainerID string `json:"container_id"`
	}
	if err := json.Unmarshal([]byte(got.body), &deps); err != nil {
		s.t.Fatalf("Deployments of %s %q: %v", app, got.body, err)
	}
	for _, d := range deps {
		if d.Status == "live" {
			return d.ContainerID
		}
	}
	s.t.Fatalf("App %s has no live Deployment: %s", app, got.body)
	return ""
}

func TestAnAppLogStartsFromItsLast200LinesAndFollowsNewOutput(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	for i := 1; i <= 250; i++ {
		s.runtime.print(ctr, fmt.Sprintf("line %d\n", i))
	}

	es := s.stream(appLogPath(app), withToken(adminToken))
	text, _ := textUntil(t, es, "line 250\n")
	var want strings.Builder
	for i := 51; i <= 250; i++ {
		fmt.Fprintf(&want, "line %d\n", i)
	}
	if text != want.String() {
		t.Errorf("first look at the App Log = %q..., want lines 51 to 250", text[:min(len(text), 40)])
	}

	s.runtime.print(ctr, "line 251\n")
	if text, _ := textUntil(t, es, "line 251\n"); text != "line 251\n" {
		t.Errorf("then = %q, want the new line", text)
	}
}

func TestAnAppLogShowsStdoutAndStderrBothWithoutColourCodes(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	s.runtime.print(ctr, "listening on :8080\n")
	s.runtime.printErr(ctr, "\x1b[31mwarning:\x1b[0m disk is slow\n")

	es := s.stream(appLogPath(app), withToken(adminToken))
	text, _ := textUntil(t, es, "disk is slow\n")
	s.runtime.printErr(ctr, "panic: oh no\r\n")
	more, _ := textUntil(t, es, "oh no\n")

	if want := "listening on :8080\nwarning: disk is slow\npanic: oh no\n"; text+more != want {
		t.Errorf("App Log = %q, want %q", text+more, want)
	}
}

func TestAnAppLogEndsWithDoneWhenANewDeploymentReplacesItsContainer(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	s.runtime.print(ctr, "serving v1\n")
	es := s.stream(appLogPath(app), withSession(s.signedIn()))
	textUntil(t, es, "serving v1\n")

	s.deploy(app, "")
	s.runQueued() // the new Deployment goes live, the old container goes

	if ev, ok := es.next(); !ok || ev.event != "done" || ev.data != "replaced" {
		t.Fatalf("after the new Deployment went live: %+v (open: %v), want done: replaced", ev, ok)
	}
	es.ended()

	// Following again follows the new container.
	next := s.liveContainer(app)
	s.runtime.print(next, "serving v2\n")
	textUntil(t, s.stream(appLogPath(app), withToken(adminToken)), "serving v2\n")
}

func TestAnAppWithNoLiveDeploymentSaysSoRatherThanAnEmptyLog(t *testing.T) {
	cases := map[string]func(s *seam, app string){
		"never deployed": func(*seam, string) {},
		"deploying":      func(s *seam, app string) { s.deploy(app, "") },
		"failed": func(s *seam, app string) {
			s.builder.fail("no Dockerfile")
			s.deploy(app, "")
			s.runQueued()
		},
	}
	for name, scene := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			app := s.createApp("web")
			scene(s, app)

			es := s.stream(appLogPath(app), withSession(s.signedIn()))
			if ev, ok := es.next(); !ok || ev.event != "done" || ev.data != "no-live-deployment" {
				t.Fatalf("first event %+v (open: %v), want done: no-live-deployment", ev, ok)
			}
			es.ended()
		})
	}
}

func TestDeletingAnAppEndsItsAppLog(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	s.runtime.print(ctr, "serving\n")
	es := s.stream(appLogPath(app), withToken(adminToken))
	textUntil(t, es, "serving\n")

	if got := s.do(http.MethodDelete, "/api/v1/apps/"+app, "", withToken(adminToken)); got.status != http.StatusNoContent {
		t.Fatalf("delete the App = %d %q, want 204", got.status, got.body)
	}

	if ev, ok := es.next(); !ok || ev.event != "done" || ev.data != "no-live-deployment" {
		t.Fatalf("after the App was deleted: %+v (open: %v), want done: no-live-deployment", ev, ok)
	}
	es.ended()
}

func TestAnAppLogStreamClosesWithinAMinuteOfItsSessionEnding(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app, ctr := s.liveApp("web")
	es := s.stream(appLogPath(app), withSession(session))
	s.clock.waitForTimer(t)

	if err := s.admins.SignOut(t.Context(), session); err != nil { // as another tab, or reset-password, would
		t.Fatal(err)
	}
	s.clock.advance(59 * time.Second)
	s.runtime.print(ctr, "still streaming\n")
	textUntil(t, es, "still streaming\n")

	s.clock.advance(time.Second)

	es.ended()
	if got := s.get(appLogPath(app), withSession(session)); got.status != http.StatusUnauthorized {
		t.Errorf("reconnect after sign-out = %d, want 401", got.status)
	}
}

func TestAReconnectedAppLogGoesOnAfterTheLastLineItGot(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	s.runtime.print(ctr, "one\n")
	s.runtime.print(ctr, "two\n")
	es := s.stream(appLogPath(app), withToken(adminToken))
	_, lastID := textUntil(t, es, "two\n")
	es.close() // the connection drops

	s.runtime.print(ctr, "three\n")
	again := s.stream(appLogPath(app), withToken(adminToken), withHeader("Last-Event-ID", lastID))
	s.runtime.print(ctr, "four\n")

	if text, _ := textUntil(t, again, "four\n"); text != "three\nfour\n" {
		t.Errorf("after reconnecting = %q, want only what came after two", text)
	}
}

func TestAnAppLogReconnectingAfterItsContainerWasReplacedIsDone(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	s.runtime.print(ctr, "serving v1\n")
	es := s.stream(appLogPath(app), withToken(adminToken))
	_, lastID := textUntil(t, es, "serving v1\n")
	es.close()

	s.deploy(app, "")
	s.runQueued()
	s.runtime.print(s.liveContainer(app), "serving v2\n")

	again := s.stream(appLogPath(app), withToken(adminToken), withHeader("Last-Event-ID", lastID))
	if ev, ok := again.next(); !ok || ev.event != "done" || ev.data != "replaced" {
		t.Fatalf("reconnect after the container was replaced: %+v (open: %v), want done: replaced", ev, ok)
	}
}

func TestAnAppLogFollowsItsContainerThroughARestart(t *testing.T) {
	s := newSeam(t)
	app, ctr := s.liveApp("web")
	s.runtime.print(ctr, "starting\n")
	es := s.stream(appLogPath(app), withToken(adminToken))
	textUntil(t, es, "starting\n")

	s.runtime.print(ctr, "panic: crashed\n")
	s.runtime.restart(ctr) // Docker restarts it: same container, same Deployment
	s.runtime.print(ctr, "starting again\n")

	if text, _ := textUntil(t, es, "starting again\n"); text != "panic: crashed\nstarting again\n" {
		t.Errorf("through the restart = %q, want each line once", text)
	}
}

func TestAnAppLogRefusesALastEventIDThatIsNoPlaceInIt(t *testing.T) {
	s := newSeam(t)
	app, _ := s.liveApp("web")
	got := s.get(appLogPath(app), withToken(adminToken), withHeader("Last-Event-ID", "12345"))
	if got.status != http.StatusBadRequest {
		t.Errorf("Last-Event-ID 12345 = %d %q, want 400", got.status, got.body)
	}
}

func TestTheAppLogOfAnUnknownAppIsNotFound(t *testing.T) {
	s := newSeam(t)
	got := s.get(appLogPath("no-such-app"), withToken(adminToken))
	if got.status != http.StatusNotFound {
		t.Errorf("App Log of an unknown App = %d %q, want 404", got.status, got.body)
	}
}
