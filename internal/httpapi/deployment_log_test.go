package httpapi_test

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// logPath is where a Deployment's Deployment Log is served: as plain
// text, and with "/stream" as Server-Sent Events.
func logPath(appID, deploymentID string) string {
	return "/api/v1/apps/" + appID + "/deployments/" + deploymentID + "/log"
}

// plainLog reads a Deployment's whole Deployment Log as plain text.
func (s *seam) plainLog(appID, deploymentID string) string {
	s.t.Helper()
	got := s.get(logPath(appID, deploymentID), withToken(adminToken))
	if got.status != http.StatusOK {
		s.t.Fatalf("GET %s = %d %q, want 200", logPath(appID, deploymentID), got.status, got.body)
	}
	return got.body
}

// logText reads a Deployment Log stream to its done event: the text of
// its chunks, the ID of the last chunk, and done's data.
func logText(t *testing.T, es *eventStream) (text, lastID, done string) {
	t.Helper()
	var b strings.Builder
	for {
		ev, ok := es.next()
		if !ok {
			t.Fatalf("the stream ended without done, after %q", b.String())
		}
		switch ev.event {
		case "message":
			b.WriteString(ev.data)
			lastID = ev.id
		case "done":
			return b.String(), lastID, ev.data
		}
	}
}

func TestAFinishedDeploymentsLogStreamsInFullThenDone(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	s.builder.writes("step 1/2: FROM alpine\nstep 2/2: COPY . .\n")
	dep := s.deploy(app, "")
	s.runQueued()
	whole := s.plainLog(app, dep)
	if !strings.Contains(whole, "step 2/2: COPY . .\n") || !strings.HasSuffix(whole, "==> live at web.example.test\n") {
		t.Fatalf("plain-text log = %q, want the build's output through go-live", whole)
	}

	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))
	text, lastID, done := logText(t, es)

	if text != whole {
		t.Errorf("streamed log = %q, want the whole log %q", text, whole)
	}
	if lastID != strconv.Itoa(len(whole)) {
		t.Errorf("last chunk's id = %q, want the byte offset after it, %d", lastID, len(whole))
	}
	if done != "live" {
		t.Errorf("done = %q, want the Deployment's status, live", done)
	}
	es.ended()
}

// textUntil reads chunks until the text read contains want, returning it
// and the last chunk's id.
func textUntil(t *testing.T, es *eventStream, want string) (text, lastID string) {
	t.Helper()
	var b strings.Builder
	for !strings.Contains(b.String(), want) {
		ev, ok := es.next()
		if !ok {
			t.Fatalf("the stream ended after %q, before %q", b.String(), want)
		}
		if ev.event != "message" {
			t.Fatalf("got %+v after %q, want the chunk with %q", ev, b.String(), want)
		}
		b.WriteString(ev.data)
		lastID = ev.id
	}
	return b.String(), lastID
}

func TestARunningDeploymentsLogStreamsAsItIsWritten(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	builds := s.builder.hold()
	dep := s.deploy(app, "")
	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken)) // still queued
	ran := s.runQueuedInBackground()
	log := builds.started(t)

	textUntil(t, es, "==> building")
	fmt.Fprint(log, "step 1/2: FROM alpine\n")
	textUntil(t, es, "step 1/2: FROM alpine\n")
	fmt.Fprint(log, "step 2/2: COPY ") // the rest of the line is still to come
	fmt.Fprint(log, ". .\n")
	if text, _ := textUntil(t, es, "\n"); text != "step 2/2: COPY . .\n" {
		t.Errorf("next chunk = %q, want the whole line once its end was written", text)
	}
	builds.finish()
	<-ran

	text, _, done := logText(t, es)
	if !strings.HasSuffix(text, "==> live at web.example.test\n") || done != "live" {
		t.Errorf("rest of the stream = %q then done %q, want go-live then done live", text, done)
	}
	es.ended()
}

func TestAReconnectResumesTheDeploymentLogWithNoRepeatedOrMissingBytes(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	builds := s.builder.hold()
	dep := s.deploy(app, "")
	ran := s.runQueuedInBackground()
	log := builds.started(t)
	fmt.Fprint(log, "line 1\nline 2\n")

	first := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))
	before, lastID := textUntil(t, first, "line 2\n")
	first.close() // the Wi-Fi drops
	fmt.Fprint(log, "line 3\nline 4\n")
	builds.finish()
	<-ran
	if lastID != strconv.Itoa(len(before)) {
		t.Errorf("id after %q = %q, want its byte offset %d", before, lastID, len(before))
	}

	second := s.stream(logPath(app, dep)+"/stream", withToken(adminToken), withHeader("Last-Event-ID", lastID))
	after, _, done := logText(t, second)

	if whole := s.plainLog(app, dep); before+after != whole {
		t.Errorf("before the drop %q + after the reconnect %q, want exactly the whole log %q", before, after, whole)
	}
	if done != "live" {
		t.Errorf("done = %q, want live", done)
	}
}

func TestALastEventIDThatIsNoOffsetIsRefused(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	dep := s.deploy(app, "")

	got := s.get(logPath(app, dep)+"/stream", withToken(adminToken), withHeader("Last-Event-ID", "abc"))

	if got.status != http.StatusBadRequest {
		t.Errorf("stream with Last-Event-ID abc = %d %q, want 400", got.status, got.body)
	}
}

func TestAFirstConnectReplaysTheLastMiBAfterAnOmissionEvent(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	var build strings.Builder
	for i := range 100_000 { // 1.9 MB
		fmt.Fprintf(&build, "build line %06d\n", i)
	}
	s.builder.writes(build.String())
	dep := s.deploy(app, "")
	s.runQueued()
	whole := s.plainLog(app, dep)

	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))
	omitted, ok := es.next()
	if !ok || omitted.event != "omitted" {
		t.Fatalf("first event = %+v, want omitted", omitted)
	}
	text, lastID, done := logText(t, es)

	skipped, err := strconv.Atoi(omitted.data)
	if err != nil || skipped+len(text) != len(whole) || whole[skipped:] != text {
		t.Errorf("omitted %q bytes then %d bytes, want the rest of the %d-byte log", omitted.data, len(text), len(whole))
	}
	if omitted.id != omitted.data {
		t.Errorf("omitted's id = %q, want the offset the replay starts at, %s", omitted.id, omitted.data)
	}
	if len(text) > 1<<20 || len(text) < 1<<20-len("build line 000000\n") {
		t.Errorf("replayed %d bytes, want the last 1 MiB, from a line start", len(text))
	}
	if !strings.HasPrefix(text, "build line ") || whole[skipped-1] != '\n' {
		t.Errorf("replay starts %q, want the start of a line", text[:20])
	}
	if lastID != strconv.Itoa(len(whole)) || done != "live" {
		t.Errorf("last id %q then done %q, want %d then live", lastID, done, len(whole))
	}
}

func TestAShortLogHasNoOmissionEvent(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	dep := s.deploy(app, "")
	s.runQueued()

	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))

	if ev, _ := es.next(); ev.event != "message" || !strings.HasPrefix(ev.data, "==> fetching") {
		t.Errorf("first event = %+v, want the log's first line", ev)
	}
}

func TestADeploymentLogStreamClosesWithinAMinuteOfItsSessionEnding(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")
	builds := s.builder.hold()
	dep := s.deploy(app, "")
	es := s.stream(logPath(app, dep)+"/stream", withSession(session))
	s.clock.waitForTimer(t)

	if err := s.admins.SignOut(t.Context(), session); err != nil { // as another tab, or reset-password, would
		t.Fatal(err)
	}
	s.clock.advance(59 * time.Second)
	// Still open: the next output arrives.
	s.runQueuedInBackground()
	fmt.Fprint(builds.started(t), "still streaming\n")
	textUntil(t, es, "still streaming\n")

	s.clock.advance(time.Second)

	es.ended()
	builds.finish()
	// And reconnecting is refused, which ends the browser's EventSource.
	if got := s.get(logPath(app, dep)+"/stream", withSession(session)); got.status != http.StatusUnauthorized {
		t.Errorf("reconnect after sign-out = %d, want 401", got.status)
	}
}

func TestADeploymentLogStreamDoesNotKeepItsSessionAlive(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn() // at the clock's start, so it idles out an hour later
	app := s.createApp("web")
	dep := s.deploy(app, "") // queued: the stream stays open, waiting
	es := s.stream(logPath(app, dep)+"/stream", withSession(session))

	// Half an hour in, the stream re-checks its session: still live.
	s.clock.waitForTimer(t)
	s.clock.advance(30 * time.Minute)
	// An hour after the last request, the session has idled out, unless
	// the re-check counted as activity.
	s.clock.waitForTimer(t)
	s.clock.advance(30 * time.Minute)

	es.ended()
	if got := s.get("/api/v1/apps", withSession(session)); got.status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/apps after an hour with only the stream open = %d, want 401", got.status)
	}
}

func TestAStreamOverTheAdminTokenHasNoSessionToEnd(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	builds := s.builder.hold()
	dep := s.deploy(app, "")
	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))

	s.clock.advance(24 * time.Hour)
	s.runQueuedInBackground()
	fmt.Fprint(builds.started(t), "still streaming\n")

	textUntil(t, es, "still streaming\n")
	builds.finish()
}

func TestAStreamEndsWhenTheServiceShutsDown(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	dep := s.deploy(app, "") // queued: the stream stays open, waiting
	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))

	close(s.stopping)

	es.ended() // so shutting down doesn't wait for it
}

func TestAFailedDeploymentsLogEndsWithDoneFailed(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	s.builder.fail("exit status 1")
	dep := s.deploy(app, "")
	s.runQueued()

	es := s.stream(logPath(app, dep)+"/stream", withToken(adminToken))
	text, _, done := logText(t, es)

	if !strings.HasSuffix(text, "==> deployment failed: build: exit status 1\n") {
		t.Errorf("streamed log = %q, want it to end saying why the Deployment failed", text)
	}
	if done != "failed" {
		t.Errorf("done = %q, want failed", done)
	}
	es.ended()
}

func TestTheDeploymentLogHasItsColourCodesStripped(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	s.builder.writes("\x1b[1;31merror\x1b[0m: \x1b]0;title\x07bad layer\r\n\x1b[2K\x1b[1Gdone\n")
	dep := s.deploy(app, "")
	s.runQueued()

	streamed, _, _ := logText(t, s.stream(logPath(app, dep)+"/stream", withToken(adminToken)))
	whole := s.plainLog(app, dep)

	for what, text := range map[string]string{"streamed log": streamed, "plain-text log": whole} {
		if !strings.Contains(text, "error: bad layer\ndone\n") || strings.ContainsAny(text, "\x1b\r") {
			t.Errorf("%s = %q, want the build's output without escape codes or carriage returns", what, text)
		}
	}
}

// The older address by Deployment ID alone is for scripts, which read
// the log exactly as the build wrote it, colour codes and carriage
// returns included, as they always have.
func TestTheLegacyLogsAddressServesTheLogAsWritten(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	raw := "\x1b[1;31merror\x1b[0m: bad layer\r\n\rprogress 50%\rprogress 100%\n"
	s.builder.writes(raw)
	dep := s.deploy(app, "")
	s.runQueued()

	got := s.get("/api/v1/deployments/"+dep+"/logs", withToken(adminToken))

	if got.status != http.StatusOK {
		t.Fatalf("GET legacy logs = %d %q, want 200", got.status, got.body)
	}
	if ct := got.header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	if !strings.Contains(got.body, "\n"+raw+"==> ") {
		t.Errorf("legacy logs = %q, want the build's output byte for byte (%q)", got.body, raw)
	}
}

func TestThePlainTextLogIsTheWholeLog(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")
	s.builder.writes(strings.Repeat("a noisy build line\n", 100_000)) // well over the stream's 1 MiB
	dep := s.deploy(app, "")
	s.runQueued()

	got := s.get(logPath(app, dep), withSession(session))

	if got.status != http.StatusOK {
		t.Fatalf("GET plain-text log = %d %q, want 200", got.status, got.body)
	}
	if ct := got.header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	if got.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff, so a browser never renders it as HTML", got.header.Get("X-Content-Type-Options"))
	}
	if !strings.HasPrefix(got.body, "==> fetching https://example.test/acme/web.git at main\n") ||
		strings.Count(got.body, "a noisy build line\n") != 100_000 ||
		!strings.HasSuffix(got.body, "==> live at web.example.test\n") {
		t.Errorf("plain-text log has %d bytes, want all of it, from the fetch through go-live", len(got.body))
	}
}

func TestADeploymentThatHasNotStartedHasAnEmptyLog(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("web")
	dep := s.deploy(app, "")

	if got := s.plainLog(app, dep); got != "" {
		t.Errorf("plain-text log of a queued Deployment = %q, want empty", got)
	}
}

func TestTheDeploymentLogIsOnlyServedUnderItsApp(t *testing.T) {
	s := newSeam(t)
	app, other := s.createApp("web"), s.createApp("api")
	dep := s.deploy(app, "")
	s.runQueued()

	for name, path := range map[string]string{
		"unknown App":              logPath("nosuchapp", dep),
		"unknown Deployment":       logPath(app, "nosuchdeployment"),
		"another App's Deployment": logPath(other, dep),
	} {
		t.Run(name, func(t *testing.T) {
			for _, p := range []string{path, path + "/stream"} {
				got := s.get(p, withToken(adminToken), withHeader("Accept", "text/event-stream"))
				if got.status != http.StatusNotFound || !strings.Contains(got.body, `"error"`) {
					t.Errorf("GET %s = %d %q, want 404 with a JSON error", p, got.status, got.body)
				}
			}
		})
	}
}

func TestTheDeploymentLogNeedsACredential(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("web")
	dep := s.deploy(app, "")
	s.runQueued()

	for _, p := range []string{logPath(app, dep), logPath(app, dep) + "/stream"} {
		if got := s.get(p); got.status != http.StatusUnauthorized {
			t.Errorf("GET %s with no credential = %d, want 401", p, got.status)
		}
		if got := s.get(p, withSession("not-a-session")); got.status != http.StatusUnauthorized {
			t.Errorf("GET %s with a dead session = %d, want 401", p, got.status)
		}
	}
	// The session cookie streams as the token does.
	if _, _, done := logText(t, s.stream(logPath(app, dep)+"/stream", withSession(session))); done != "live" {
		t.Errorf("done over the session cookie = %q, want live", done)
	}
}
