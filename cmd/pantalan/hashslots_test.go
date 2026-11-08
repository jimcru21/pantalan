package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/password"
)

// gatedHasher is the real hasher with a gate in front of every Verify:
// each check waits until the test lets one through. It records how many
// hashes (Hash or Verify) ran at once.
type gatedHasher struct {
	inner    password.Hasher
	entered  chan struct{} // a Verify reached the gate
	pass     chan struct{} // lets one Verify through
	opened   chan struct{} // closed: every Verify goes through
	openOnce sync.Once

	mu            sync.Mutex
	running, peak int
}

func newGatedHasher() *gatedHasher {
	return &gatedHasher{entered: make(chan struct{}, 100), pass: make(chan struct{}), opened: make(chan struct{})}
}

func (g *gatedHasher) begin() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running++
	g.peak = max(g.peak, g.running)
}

func (g *gatedHasher) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running--
}

func (g *gatedHasher) Hash(pw string) (string, error) {
	g.begin()
	defer g.end()
	return g.inner.Hash(pw)
}

func (g *gatedHasher) Verify(pw, hash string) (bool, error) {
	g.begin()
	defer g.end()
	g.entered <- struct{}{}
	select {
	case <-g.pass:
	case <-g.opened:
	}
	return g.inner.Verify(pw, hash)
}

// openGate lets every check through from now on.
func (g *gatedHasher) openGate() { g.openOnce.Do(func() { close(g.opened) }) }

func (g *gatedHasher) NeedsRehash(hash string) bool { return g.inner.NeedsRehash(hash) }

func (g *gatedHasher) peakRunning() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

// waitEntered waits for n checks to reach the gate.
func (g *gatedHasher) waitEntered(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-g.entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d password checks started", i, n)
		}
	}
}

// letOneThrough lets one waiting check finish.
func (g *gatedHasher) letOneThrough(t *testing.T) {
	t.Helper()
	select {
	case g.pass <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatal("no password check was waiting at the gate")
	}
}

// serveGated runs serve with the gated hasher and the hash-slot timer
// under the test's control, with Admin alice created.
func serveGated(t *testing.T) (*running, *gatedHasher, *slotTimer, string) {
	t.Helper()
	world := testWorld(t)
	hasher := newGatedHasher()
	slotWait := newSlotTimer()
	world.Hasher = hasher
	world.HashSlotWait = slotWait.after
	s := startServeOn(t, serveEnv(t), world)
	// Registered after serve's cleanup, so it runs first: a test that
	// fails mid-way doesn't leave checks stuck at the gate.
	t.Cleanup(hasher.openGate)
	return s, hasher, slotWait, createServedAdmin(t, s, "alice")
}

// loginAsync submits a login from a fresh browser and delivers the
// response on the returned channel (a status of 0 if the request failed).
func loginAsync(t *testing.T, s *running, username, pw string) <-chan page {
	t.Helper()
	b := newBrowser(t, s)
	form := url.Values{"username": {username}, "password": {pw}}
	req, err := http.NewRequest(http.MethodPost, b.base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	done := make(chan page, 1)
	go func() {
		resp, err := b.client.Do(req)
		if err != nil {
			t.Errorf("POST /login as %s: %v", username, err)
			done <- page{Response: &http.Response{}}
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- page{Response: resp, body: string(body)}
	}()
	return done
}

// slotTimer stands in for the hash-slot timeout: the test sees each wait
// a queued sign-in starts and fires it when it's ready.
type slotTimer struct {
	waits chan time.Duration
	fire  chan time.Time
}

func newSlotTimer() *slotTimer {
	return &slotTimer{waits: make(chan time.Duration, 100), fire: make(chan time.Time)}
}

func (f *slotTimer) after(d time.Duration) <-chan time.Time {
	f.waits <- d
	return f.fire
}

// started waits for a sign-in to queue for a hash slot and returns how
// long it means to wait.
func (f *slotTimer) started(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.waits:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no login queued for a hash slot")
		return 0
	}
}

// expire ends the wait of the sign-in that queued.
func (f *slotTimer) expire(t *testing.T) {
	t.Helper()
	select {
	case f.fire <- time.Time{}:
	case <-time.After(10 * time.Second):
		t.Fatal("the queued login stopped waiting before its timeout fired")
	}
}

func TestAtMostTwoPasswordHashesRunAtOnceAndTheRestQueue(t *testing.T) {
	s, hasher, slotWait, pw := serveGated(t)

	first := loginAsync(t, s, "alice", wrongPassword)
	second := loginAsync(t, s, "mallory", pw) // unknown: checks the dummy hash
	hasher.waitEntered(t, 2)
	third := loginAsync(t, s, "alice", pw)

	// The third finds both slots taken and starts its 5 s wait for one.
	if d := slotWait.started(t); d != 5*time.Second {
		t.Errorf("a queued login waits %v for a hash slot, want 5s", d)
	}
	hasher.letOneThrough(t)
	hasher.waitEntered(t, 1) // the third gets the freed slot
	hasher.letOneThrough(t)
	hasher.letOneThrough(t)

	for name, c := range map[string]<-chan page{"first": first, "second": second} {
		if got := <-c; got.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s login = %d, want 401", name, got.StatusCode)
		}
	}
	if got := <-third; got.StatusCode != http.StatusSeeOther {
		t.Errorf("queued login with the right password = %d, want 303; body:\n%s", got.StatusCode, got.body)
	}
	if peak := hasher.peakRunning(); peak != 2 {
		t.Errorf("at most %d hashes ran at once, want 2", peak)
	}
}

func TestALoginThatWaitsOverFiveSecondsForAHashSlotGets503(t *testing.T) {
	s, hasher, slotWait, pw := serveGated(t)

	first := loginAsync(t, s, "alice", wrongPassword)
	second := loginAsync(t, s, "alice", wrongPassword)
	hasher.waitEntered(t, 2)
	third := loginAsync(t, s, "alice", pw)

	if d := slotWait.started(t); d != 5*time.Second {
		t.Errorf("a queued login waits %v for a hash slot, want 5s", d)
	}
	slotWait.expire(t)
	got := <-third
	if got.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("login after 5 s without a hash slot = %d, want 503; body:\n%s", got.StatusCode, got.body)
	}
	if len(got.Cookies()) != 0 {
		t.Errorf("the 503 sets cookies %q", got.Header.Values("Set-Cookie"))
	}

	hasher.openGate()
	<-first
	<-second
	// The refused login wasn't counted: after two more failures there
	// are four, so the next attempt is still checked, not throttled.
	b := newBrowser(t, s)
	failLogins(t, b, 2)
	if got := b.login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Errorf("login with the right password after 4 failures = %d, want 303; body:\n%s", got.StatusCode, got.body)
	}
}

func TestASignInCheckedAgainstTheOldPasswordGetsNoSessionWhenAResetLandsMeanwhile(t *testing.T) {
	s, hasher, _, pw := serveGated(t)

	login := loginAsync(t, s, "alice", pw)
	hasher.waitEntered(t, 1) // checking pw against the hash read before the reset
	reset := runCLI(t, t.Context(), s.env, "admin", "reset-password")
	if reset.code != 0 {
		t.Fatalf("admin reset-password exit code = %d (stderr %q)", reset.code, reset.stderr)
	}
	hasher.letOneThrough(t)

	if got := <-login; got.StatusCode != http.StatusUnauthorized {
		t.Errorf("login with the password reset while it was checked = %d, want 401; body:\n%s", got.StatusCode, got.body)
	}
	var sessions int
	if err := adminDB(t, s.env).QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Errorf("%d sessions stored after the reset, want 0", sessions)
	}
}
