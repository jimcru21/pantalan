package main

import (
	"net/http"
	"testing"
	"time"
)

// sessionRows is how many sessions the database holds. Pruning has no
// other visible effect: an expired session is refused either way.
func sessionRows(t *testing.T, env []string) int {
	t.Helper()
	n, _ := signInCounts(t, env)
	return n
}

// expiredAndLive signs in twice, 90 minutes apart, then lets 30 more
// minutes pass: the first session is past its idle hour, the second isn't.
func expiredAndLive(t *testing.T, s *running, clock *fakeClock) (expired, live *browser) {
	t.Helper()
	pw := createServedAdmin(t, s, "alice")
	expired, live = newBrowser(t, s), newBrowser(t, s)
	if got := expired.login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("first login = %d, want 303", got.StatusCode)
	}
	clock.advance(90 * time.Minute)
	if got := live.login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("second login = %d, want 303", got.StatusCode)
	}
	clock.advance(30 * time.Minute)
	return expired, live
}

func TestExpiredSessionsArePrunedAtStartup(t *testing.T) {
	clock := newFakeClock()
	env := serveEnv(t)
	s := startServeAt(t, env, clock)
	_, live := expiredAndLive(t, s, clock)
	s.stop()
	s.wait(t)
	if n := sessionRows(t, env); n != 2 {
		t.Fatalf("sessions before the restart = %d, want 2", n)
	}

	s = startServeAt(t, env, clock)
	s.addr(t, "dashboard")

	if n := sessionRows(t, env); n != 1 {
		t.Errorf("sessions after the restart = %d, want 1 (the live one)", n)
	}
	live.base = "https://" + s.addr(t, "dashboard")
	if !live.signedInStill() {
		t.Error("pruning at startup ended the live session")
	}
}

func TestTheDailyTickPrunesExpiredSessions(t *testing.T) {
	clock := newFakeClock()
	env := serveEnv(t)
	node := testNode(t)
	node.Clock = clock.Now
	ticks := make(chan time.Time)
	node.MaintenanceTicks = ticks
	s := startServeOn(t, env, node)
	_, live := expiredAndLive(t, s, clock)

	ticks <- clock.Now()

	deadline := time.Now().Add(5 * time.Second)
	for sessionRows(t, env) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("sessions after the daily tick = %d, want 1 (the live one)", sessionRows(t, env))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !live.signedInStill() {
		t.Error("the daily tick ended the live session")
	}
}

func TestTheTwentyFirstLoginEvictsTheOldestSession(t *testing.T) {
	clock := newFakeClock()
	s := startServeAt(t, serveEnv(t), clock)
	pw := createServedAdmin(t, s, "alice")

	browsers := make([]*browser, 21)
	for i := range browsers {
		clock.advance(time.Second)
		browsers[i] = newBrowser(t, s)
		if got := browsers[i].login("alice", pw); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("login %d = %d, want 303", i+1, got.StatusCode)
		}
	}

	if browsers[0].signedInStill() {
		t.Error("the oldest session survived the 21st login")
	}
	for i, b := range browsers[1:] {
		if !b.signedInStill() {
			t.Errorf("session %d of the newest 20 was ended", i+2)
		}
	}
}
