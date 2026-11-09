package main

import (
	"net/http"
	"testing"
	"time"
)

// Which sessions expire is tested at the service seam
// (internal/service/admin_test.go); this checks that serve prunes them at
// startup and on the daily tick.
func TestServePrunesExpiredSessionsAtStartupAndOnTheDailyTick(t *testing.T) {
	clock := newFakeClock()
	env := serveEnv(t)
	world := testWorld(t)
	world.Clock = clock.Now
	ticks := make(chan time.Time)
	world.MaintenanceTicks = ticks
	s := startServeOn(t, env, world)
	pw := createServedAdmin(t, s, "alice")
	for i := range 2 {
		if got := newBrowser(t, s).login("alice", pw); got.StatusCode != http.StatusNoContent {
			t.Fatalf("login %d = %d, want 204", i+1, got.StatusCode)
		}
		clock.advance(90 * time.Minute) // past the idle hour
	}
	live := newBrowser(t, s)
	if got := live.login("alice", pw); got.StatusCode != http.StatusNoContent {
		t.Fatalf("third login = %d, want 204", got.StatusCode)
	}
	s.stop()
	s.wait(t)

	s = startServeOn(t, env, world)
	s.addr(t, "dashboard")
	if n := sessionRows(t, env); n != 1 {
		t.Errorf("sessions after the restart = %d, want 1 (the live one)", n)
	}

	clock.advance(time.Hour)
	ticks <- clock.Now()
	deadline := time.Now().Add(5 * time.Second)
	for sessionRows(t, env) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("sessions after the daily tick = %d, want 0", sessionRows(t, env))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sessionRows is how many sessions the database holds. Pruning has no
// other visible effect: an expired session is refused either way.
func sessionRows(t *testing.T, env []string) int {
	t.Helper()
	n, _ := signInCounts(t, env)
	return n
}
