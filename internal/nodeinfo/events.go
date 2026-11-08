package nodeinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

const (
	// EventsLen is how many container events the ring keeps.
	EventsLen = 100
	// MinBackoff and MaxBackoff bound the wait before the event stream
	// reconnects: it doubles from the first to the second.
	MinBackoff = time.Second
	MaxBackoff = 30 * time.Second
)

// EventActions are the container events the stream asks Docker for.
// dockerd matches health_status by prefix, as its action is
// "health_status: healthy".
var EventActions = []string{"start", "stop", "die", "oom", "kill", "restart", "health_status", "destroy"}

// Event is one container event as GET /events reports it.
type Event struct {
	Action string // "die", "health_status: healthy", ...
	Actor  struct {
		ID         string
		Attributes map[string]string // name, image, exitCode, signal, labels
	}
	TimeNano int64 `json:"timeNano"`

	// GapBefore marks the first event after a reconnect whose gap the
	// stream couldn't backfill: events before it may be missing.
	GapBefore bool `json:"-"`
}

// Time is when it happened.
func (e Event) Time() time.Time { return time.Unix(0, e.TimeNano) }

// EventLog is the ring as the Events tab shows it.
type EventLog struct {
	Events []Event // newest first
	// Err is why the stream is down, while it is.
	Err error
}

// EventStream holds one /events stream open on Docker, filtered to
// container events at the source, and keeps the last EventsLen of them.
// It reconnects with backoff from MinBackoff to MaxBackoff and backfills
// the gap with since=<last event time>.
type EventStream struct {
	docker *Docker
	log    *slog.Logger

	mu   sync.Mutex
	ring []Event // oldest first
	err  error
	// since is where a reconnect backfills from: the last event's time,
	// or before there was one, when the stream last connected.
	since time.Time
	// replaying is true from a reconnect until the replay is past the
	// ring's last event: events the ring has are dropped till then.
	// backfilled is whether the replay held that last event, which
	// proves dockerd still buffered everything after it. A replay without
	// it (dockerd's buffer moved past it, or dockerd restarted) leaves a
	// gap, and the next event gets GapBefore.
	replaying, backfilled bool
}

func NewEventStream(docker *Docker, log *slog.Logger) *EventStream {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &EventStream{docker: docker, log: log}
}

// Recent is a copy of the ring, newest first.
func (s *EventStream) Recent() EventLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := slices.Clone(s.ring)
	slices.Reverse(events)
	return EventLog{Events: events, Err: s.err}
}

// Run keeps the stream open until ctx is done. after waits out a backoff
// delay; nil means time.After.
func (s *EventStream) Run(ctx context.Context, after func(time.Duration) <-chan time.Time) {
	if after == nil {
		after = time.After
	}
	backoff := MinBackoff
	for {
		connected, err := s.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = MinBackoff
		}
		s.setErr(err)
		select {
		case <-after(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(2*backoff, MaxBackoff)
	}
}

// stream reads one connection to /events until it ends. connected is
// whether Docker accepted it.
func (s *EventStream) stream(ctx context.Context) (connected bool, err error) {
	ver, _, err := s.docker.ping(ctx)
	if err != nil {
		return false, err
	}
	filters, _ := json.Marshal(map[string]map[string]bool{"type": {"container": true}, "event": actionSet()})
	q := url.Values{"filters": {string(filters)}}
	connectedAt := s.docker.now()
	s.mu.Lock()
	since := s.since
	s.mu.Unlock()
	if !since.IsZero() {
		// dockerd replays its buffered events from since on, inclusive.
		q.Set("since", fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond()))
	}
	resp, err := s.docker.do(ctx, "/v"+ver+"/events?"+q.Encode())
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, engineError("/events", resp)
	}
	s.mu.Lock()
	s.err = nil
	s.replaying, s.backfilled = len(s.ring) > 0, false
	if since.IsZero() {
		s.since = connectedAt
	}
	s.mu.Unlock()
	dec := json.NewDecoder(resp.Body)
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			return true, fmt.Errorf("docker: event stream ended: %w", err)
		}
		s.add(ev)
	}
}

func actionSet() map[string]bool {
	set := map[string]bool{}
	for _, a := range EventActions {
		set[a] = true
	}
	return set
}

func (s *EventStream) add(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replaying {
		last := s.ring[len(s.ring)-1]
		if ev.TimeNano <= last.TimeNano {
			switch {
			case same(ev, last):
				s.backfilled = true
			case !slices.ContainsFunc(s.ring, func(e Event) bool { return same(e, ev) }):
				s.insert(ev)
			}
			return
		}
		s.replaying = false
		ev.GapBefore = !s.backfilled
	}
	s.insert(ev)
}

// same reports whether a and b are the same event.
func same(a, b Event) bool {
	return a.TimeNano == b.TimeNano && a.Actor.ID == b.Actor.ID && a.Action == b.Action
}

// insert adds ev to the ring, making room by dropping the oldest event.
func (s *EventStream) insert(ev Event) {
	s.since = ev.Time()
	if len(s.ring) == EventsLen {
		s.ring = append(s.ring[:0], s.ring[1:]...)
	}
	s.ring = append(s.ring, ev)
}

func (s *EventStream) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && (s.err == nil || s.err.Error() != err.Error()) {
		s.log.Warn("docker event stream", "err", err)
	}
	s.err = err
}
