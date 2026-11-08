package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// eventsAt is when the tests' events start.
var eventsAt = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// eventRows waits until the Events tab's newest row is newest and returns
// its rows.
func (b *browser) eventRows(newest string) []string {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := b.fragment("/fragments/events")
		if got.StatusCode != http.StatusOK {
			b.t.Fatalf("GET /fragments/events = %d:\n%s", got.StatusCode, got.body)
		}
		rows := tableRows(got.body)
		if len(rows) > 0 && rows[0] == newest {
			return rows
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("the Events tab never showed %q first:\n%s", newest, strings.Join(rows, "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheEventsTabShowsContainerEventsNewestFirstWithExitCodes(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.waitForStreams(t, 1)

	at := func(s int) time.Time { return eventsAt.Add(time.Duration(s) * time.Second) }
	fe.emit(
		containerEvent(at(0), "start", "c1", "db", "postgres:17"),
		containerEvent(at(1), "health_status: healthy", "c1", "db", "postgres:17"),
		// Not one the tab shows: the stream's filter leaves it out.
		containerEvent(at(2), "exec_start: sh -c pg_isready", "c1", "db", "postgres:17"),
		containerEvent(at(3), "kill", "c3", "migrate", "postgres:17", "signal", "15"),
		containerEvent(at(4), "die", "c3", "migrate", "postgres:17", "exitCode", "137"),
		containerEvent(at(5), "stop", "c3", "migrate", "postgres:17"),
		containerEvent(at(6), "oom", "c2", "cache", "redis:7"),
		containerEvent(at(7), "restart", "c2", "cache", "redis:7"),
		containerEvent(at(8), "destroy", "c3", "migrate", "postgres:17"),
	)

	// time (UTC), container, event, detail, image
	want := []string{
		"09:00:08 migrate destroy postgres:17",
		"09:00:07 cache restart redis:7",
		"09:00:06 cache oom redis:7",
		"09:00:05 migrate stop postgres:17",
		"09:00:04 migrate die exit code 137 postgres:17",
		"09:00:03 migrate kill signal 15 postgres:17",
		"09:00:01 db health_status healthy postgres:17",
		"09:00:00 db start postgres:17",
	}
	if rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("events tab rows =\n%q\nwant\n%q", rows, want)
	}

	got := b.fragment("/fragments/events")
	if open, _ := openTab(t, got.body); open != "events" {
		t.Errorf("the Events tab marks %q open", open)
	}
	if open, all := openTab(t, b.get("/").body); open != "containers" || !slices.Contains(all, "events") {
		t.Errorf("the page's tab bar has %q open of %q, want containers with events on offer", open, all)
	}
	regions := polled.FindAllStringSubmatch(got.body, -1)
	if len(regions) != 1 || regions[0][2] != "/fragments/events" || regions[0][3] != "every 5s" || regions[0][4] != "outerHTML" {
		t.Errorf("the Events tab polls %q, want itself every 5s", regions)
	}
}

// fakeBackoff stands in for the event stream's reconnect timer: the test
// sees each delay the stream asks for and fires it when it's ready.
type fakeBackoff struct {
	waits chan time.Duration
	fire  chan time.Time
}

func newFakeBackoff() *fakeBackoff {
	return &fakeBackoff{waits: make(chan time.Duration, 100), fire: make(chan time.Time)}
}

func (f *fakeBackoff) after(d time.Duration) <-chan time.Time {
	f.waits <- d
	return f.fire
}

// next waits for the stream to ask for a delay, fires it and returns it.
func (f *fakeBackoff) next(t *testing.T) time.Duration {
	t.Helper()
	var d time.Duration
	select {
	case d = <-f.waits:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream never waited to reconnect")
	}
	select {
	case f.fire <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream stopped waiting before its delay fired")
	}
	return d
}

// serveWithEvents is serveWithEngine with the event stream's reconnect
// timer under the test's control, once the stream is open.
func serveWithEvents(t *testing.T) (*browser, *fakeEngine, *fakeBackoff) {
	t.Helper()
	fp := newFakeProc(t)
	node := fp.node(t)
	backoff := newFakeBackoff()
	node.EventsBackoff = backoff.after
	fe := newFakeEngine(t, node.DockerSocket)
	_, b := serveNodeOverview(t, fp, node)
	fe.waitForStreams(t, 1)
	return b, fe, backoff
}

// since is how the stream asks dockerd for the events from at on.
func since(at time.Time) string { return fmt.Sprintf("%d.%09d", at.Unix(), at.Nanosecond()) }

func TestADroppedStreamReconnectsWithBackoffFrom1To30sAndBackfillsWithSince(t *testing.T) {
	b, fe, backoff := serveWithEvents(t)
	at := func(s int) time.Time { return eventsAt.Add(time.Duration(s)*time.Second + 250*time.Millisecond) }
	fe.emit(
		containerEvent(at(0), "start", "c1", "db", "postgres:17"),
		containerEvent(at(1), "start", "c2", "cache", "redis:7"),
	)
	b.eventRows("09:00:01 cache start redis:7")

	// The stream drops; what happens meanwhile is backfilled, and the
	// last event before the drop, which since= replays, shows once.
	fe.dropEvents()
	fe.emit(containerEvent(at(2), "die", "c2", "cache", "redis:7", "exitCode", "1"))
	if d := backoff.next(t); d != time.Second {
		t.Errorf("first reconnect after %v, want 1s", d)
	}
	if got := fe.waitForStreams(t, 2); got[1] != since(at(1)) {
		t.Errorf("the reconnect asked for since=%q, want %q (the last event's time)", got[1], since(at(1)))
	}
	want := []string{
		"09:00:02 cache die exit code 1 redis:7",
		"09:00:01 cache start redis:7",
		"09:00:00 db start postgres:17",
	}
	if rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after the reconnect the events are\n%q\nwant\n%q", rows, want)
	}

	// Docker goes down: each failed attempt doubles the wait, up to 30s.
	fe.close()
	for _, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		if d := backoff.next(t); d != want*time.Second {
			t.Errorf("reconnect after %v, want %v", d, want*time.Second)
		}
	}
	if got := b.text("/fragments/events"); !strings.Contains(got, "Not connected to Docker's event stream") || !strings.Contains(got, "09:00:02 cache die") {
		t.Errorf("while Docker is down the Events tab should say so and keep its events:\n%s", got)
	}
	if n := len(fe.streams()); n != 2 {
		t.Errorf("%d /events requests while Docker was down, want none", n-2)
	}

	// Back up: the next attempt connects, backfills, and the backoff
	// starts again from 1s.
	fe.open()
	fe.emit(containerEvent(at(3), "start", "c2", "cache", "redis:7"))
	backoff.next(t)
	if got := fe.waitForStreams(t, 3); got[2] != since(at(2)) {
		t.Errorf("the reconnect asked for since=%q, want %q", got[2], since(at(2)))
	}
	want = append([]string{"09:00:03 cache start redis:7"}, want...)
	if rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after Docker came back the events are\n%q\nwant\n%q", rows, want)
	}
	if got := b.text("/fragments/events"); strings.Contains(got, "Not connected") {
		t.Errorf("the Events tab still says the stream is down:\n%s", got)
	}
	fe.dropEvents()
	if d := backoff.next(t); d != time.Second {
		t.Errorf("reconnect after a stream that was up waited %v, want 1s", d)
	}
}

func TestAStreamThatDropsBeforeAnyEventBackfillsFromWhenItConnected(t *testing.T) {
	b, fe, backoff := serveWithEvents(t)
	fe.dropEvents()
	// The Node's clock is the real one here.
	missed := time.Now()
	fe.emit(containerEvent(missed, "start", "c1", "db", "postgres:17"))
	backoff.next(t)
	got := fe.waitForStreams(t, 2)
	if got[1] == "" || got[1] > since(missed) {
		t.Errorf("the reconnect asked for since=%q, want one before the missed event at %q", got[1], since(missed))
	}
	b.eventRows(missed.UTC().Format("15:04:05") + " db start postgres:17")
}

const reconnected = "Stream reconnected: earlier events may be missing"

func TestAGapTheDaemonsBufferNoLongerCoversShowsTheReconnectedMarker(t *testing.T) {
	b, fe, backoff := serveWithEvents(t)
	at := func(s int) time.Time { return eventsAt.Add(time.Duration(s) * time.Second) }
	fe.emit(
		containerEvent(at(0), "start", "c1", "db", "postgres:17"),
		containerEvent(at(1), "start", "c2", "cache", "redis:7"),
	)
	b.eventRows("09:00:01 cache start redis:7")

	// While the stream is down more happens than dockerd buffers, so
	// the replay from since= no longer starts with the last event seen.
	fe.dropEvents()
	fe.set(func(*engineState) { fe.eventBuffer = 3 })
	fe.emit(
		containerEvent(at(2), "stop", "c2", "cache", "redis:7"),
		containerEvent(at(3), "die", "c2", "cache", "redis:7", "exitCode", "0"),
		containerEvent(at(4), "start", "c2", "cache", "redis:7"),
		containerEvent(at(5), "kill", "c1", "db", "postgres:17", "signal", "9"),
		containerEvent(at(6), "die", "c1", "db", "postgres:17", "exitCode", "137"),
	)
	backoff.next(t)
	want := []string{
		"09:00:06 db die exit code 137 postgres:17",
		"09:00:05 db kill signal 9 postgres:17",
		"09:00:04 cache start redis:7",
		reconnected,
		"09:00:01 cache start redis:7",
		"09:00:00 db start postgres:17",
	}
	if rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after an unbackfillable gap the events are\n%q\nwant\n%q", rows, want)
	}

	// dockerd restarts, losing its buffer: the next event comes after a
	// gap nothing could replay.
	fe.close()
	fe.set(func(*engineState) { fe.events = nil })
	fe.open()
	backoff.next(t)
	fe.waitForStreams(t, 3)
	fe.emit(containerEvent(at(9), "start", "c1", "db", "postgres:17"))
	want = append([]string{"09:00:09 db start postgres:17", reconnected}, want...)
	if rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after the daemon restarted the events are\n%q\nwant\n%q", rows, want)
	}
}

func TestTheEventsTabKeepsOnlyTheLast100Events(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.waitForStreams(t, 1)

	var events []engineEvent
	for i := range 130 {
		events = append(events, containerEvent(eventsAt.Add(time.Duration(i)*time.Second), "restart", "c2", "cache", "redis:7"))
	}
	fe.emit(events...)

	rows := b.eventRows("09:02:09 cache restart redis:7")
	if len(rows) != 100 {
		t.Fatalf("the Events tab has %d rows, want 100", len(rows))
	}
	if oldest := "09:00:30 cache restart redis:7"; rows[99] != oldest {
		t.Errorf("the oldest row is %q, want %q", rows[99], oldest)
	}
}
