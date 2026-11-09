package main

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/httpapi"
)

// eventsAt is when the tests' events start.
var eventsAt = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// reconnected is the row the tests write for an event's gap_before: the
// stream reconnected and older events may be missing.
const reconnected = "Stream reconnected: earlier events may be missing"

// eventLog is GET /api/v1/node/events.
func (b *browser) eventLog() httpapi.NodeEventLog {
	b.t.Helper()
	return getJSON[httpapi.NodeEventLog](b, "/api/v1/node/events")
}

// eventRows is the log as the tests write it, newest first: each event's
// time (UTC), container, action, detail and image, with the reconnected
// row below an event that may have a gap before it.
func eventRows(t *testing.T, log httpapi.NodeEventLog) []string {
	t.Helper()
	var rows []string
	for _, e := range log.Events {
		at, err := time.Parse(time.RFC3339Nano, e.Time)
		if err != nil {
			t.Fatalf("event %s has a bad time: %v", asJSON(e), err)
		}
		row := []string{at.UTC().Format("15:04:05"), e.Container, e.Action}
		if e.Health != "" {
			row = append(row, e.Health)
		}
		if e.ExitCode != nil {
			row = append(row, "exit code "+strconv.Itoa(*e.ExitCode))
		}
		if e.Signal != "" {
			row = append(row, "signal "+e.Signal)
		}
		rows = append(rows, strings.Join(append(row, e.Image), " "))
		if e.GapBefore {
			rows = append(rows, reconnected)
		}
	}
	return rows
}

// eventRows waits until the event log's newest row is newest and returns
// the log and its rows.
func (b *browser) eventRows(newest string) (httpapi.NodeEventLog, []string) {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		log := b.eventLog()
		rows := eventRows(b.t, log)
		if len(rows) > 0 && rows[0] == newest {
			return log, rows
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("the event log never had %q first:\n%s", newest, strings.Join(rows, "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheEventLogListsContainerEventsNewestFirstWithExitCodes(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.waitForStreams(t, 1)

	at := func(s int) time.Time { return eventsAt.Add(time.Duration(s) * time.Second) }
	fe.emit(
		containerEvent(at(0), "start", "c1", "db", "postgres:17"),
		containerEvent(at(1), "health_status: healthy", "c1", "db", "postgres:17"),
		// Not one the log keeps: the stream's filter leaves it out.
		containerEvent(at(2), "exec_start: sh -c pg_isready", "c1", "db", "postgres:17"),
		containerEvent(at(3), "kill", "c3", "migrate", "postgres:17", "signal", "15"),
		containerEvent(at(4).Add(123456789), "die", "c3", "migrate", "postgres:17", "exitCode", "137"),
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
	log, rows := b.eventRows(want[0])
	if !slices.Equal(rows, want) {
		t.Errorf("event log rows =\n%q\nwant\n%q", rows, want)
	}
	code := 137
	wantDie := httpapi.NodeEvent{Time: "2026-11-08T09:00:04.123456789Z", ContainerID: "c3", Container: "migrate", Image: "postgres:17", Action: "die", ExitCode: &code}
	if got := log.Events[4]; !reflect.DeepEqual(got, wantDie) {
		t.Errorf("the die event = %s, want %s", asJSON(got), asJSON(wantDie))
	}
	if log.StreamError != "" {
		t.Errorf("stream_error with the stream up = %q, want none", log.StreamError)
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
	world := fp.world(t)
	backoff := newFakeBackoff()
	world.EventsBackoff = backoff.after
	fe := newFakeEngine(t, world.DockerSocket)
	_, b := serveNodeOverview(t, fp, world)
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
	if _, rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after the reconnect the events are\n%q\nwant\n%q", rows, want)
	}

	// Docker goes down: each failed attempt doubles the wait, up to 30s.
	fe.close()
	for _, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		if d := backoff.next(t); d != want*time.Second {
			t.Errorf("reconnect after %v, want %v", d, want*time.Second)
		}
	}
	if log := b.eventLog(); log.StreamError == "" || !slices.Equal(eventRows(t, log), want) {
		t.Errorf("while Docker is down the event log should say so and keep its events: %s", asJSON(log))
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
	if _, rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after Docker came back the events are\n%q\nwant\n%q", rows, want)
	}
	if log := b.eventLog(); log.StreamError != "" {
		t.Errorf("the event log still says the stream is down: %q", log.StreamError)
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

func TestAGapTheDaemonsBufferNoLongerCoversIsMarked(t *testing.T) {
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
	if _, rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
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
	if _, rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after the daemon restarted the events are\n%q\nwant\n%q", rows, want)
	}
}

func TestADropBeforeAnyEventIsMarkedAsAGap(t *testing.T) {
	b, fe, backoff := serveWithEvents(t)

	// dockerd restarts before any event reached the log, losing its
	// buffer: with no event to check the replay against, nothing proves
	// the gap was backfilled.
	fe.close()
	fe.emit(containerEvent(time.Now(), "start", "c9", "lost", "busybox"))
	fe.set(func(*engineState) { fe.events = nil })
	fe.open()
	backoff.next(t)
	fe.waitForStreams(t, 2)
	next := time.Now()
	fe.emit(containerEvent(next, "start", "c1", "db", "postgres:17"))

	want := []string{next.UTC().Format("15:04:05") + " db start postgres:17", reconnected}
	if _, rows := b.eventRows(want[0]); !slices.Equal(rows, want) {
		t.Errorf("after a drop before any event the events are\n%q\nwant\n%q", rows, want)
	}
}

func TestTheEventLogKeepsOnlyTheLast100Events(t *testing.T) {
	fp := newFakeProc(t)
	_, b, fe := serveWithEngine(t, fp)
	fe.waitForStreams(t, 1)

	var events []engineEvent
	for i := range 130 {
		events = append(events, containerEvent(eventsAt.Add(time.Duration(i)*time.Second), "restart", "c2", "cache", "redis:7"))
	}
	fe.emit(events...)

	_, rows := b.eventRows("09:02:09 cache restart redis:7")
	if len(rows) != 100 {
		t.Fatalf("the event log has %d events, want 100", len(rows))
	}
	if oldest := "09:00:30 cache restart redis:7"; rows[99] != oldest {
		t.Errorf("the oldest event is %q, want %q", rows[99], oldest)
	}
}
