package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A test's view of a Server-Sent Events stream: the events as the
// browser's EventSource would dispatch them.

// sseEvent is one dispatched event. event is "message" when the server
// named none.
type sseEvent struct {
	event, id, data string
}

type eventStream struct {
	t      *testing.T
	header http.Header
	events chan sseEvent // closed when the server ends the stream
	cancel context.CancelFunc
}

// stream opens path as an event stream with the options' credentials
// and headers, failing the test unless the server answers 200
// text/event-stream. The test may close it; it is closed at cleanup.
func (s *seam) stream(path string, opts ...option) *eventStream {
	s.t.Helper()
	ctx, cancel := context.WithCancel(s.t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.srv.URL+path, nil)
	if err != nil {
		s.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	for _, opt := range opts {
		opt(req)
	}
	res, err := s.client.Do(req)
	if err != nil {
		cancel()
		s.t.Fatalf("GET %s: %v", path, err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		res.Body.Close()
		cancel()
		s.t.Fatalf("GET %s = %d %s, want 200 text/event-stream", path, res.StatusCode, res.Header.Get("Content-Type"))
	}
	es := &eventStream{t: s.t, header: res.Header, events: make(chan sseEvent, 1024), cancel: cancel}
	go es.read(res)
	s.t.Cleanup(es.close)
	return es
}

// read parses the stream as the EventSource spec does (only "\n" line
// ends, which is all the server writes) until it ends.
func (es *eventStream) read(res *http.Response) {
	defer close(es.events)
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var ev sseEvent
	var data []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if data != nil {
				if ev.event == "" {
					ev.event = "message"
				}
				ev.data = strings.Join(data, "\n")
				es.events <- ev
			}
			ev, data = sseEvent{id: ev.id}, nil // the last ID carries over
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			ev.event = value
		case "id":
			ev.id = value
		case "data":
			data = append(data, value)
		}
	}
}

// next is the next event, or false once the server has ended the stream.
func (es *eventStream) next() (sseEvent, bool) {
	es.t.Helper()
	select {
	case ev, ok := <-es.events:
		return ev, ok
	case <-time.After(5 * time.Second):
		es.t.Fatal("no event and no end of stream within 5s")
		return sseEvent{}, false
	}
}

// ended fails the test unless the server ends the stream, with no more
// events, within 5 s.
func (es *eventStream) ended() {
	es.t.Helper()
	if ev, ok := es.next(); ok {
		es.t.Fatalf("got event %+v, want the stream to end", ev)
	}
}

func (es *eventStream) close() { es.cancel() }
