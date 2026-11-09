package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/internal/sessioncookie"
)

// Both logs, the Deployment Log and the App Log, stream as Server-Sent
// Events. What they share lives here: the event writer, the text
// cleaning and the session re-check.

const (
	// sessionRecheck is how often an open stream checks that the
	// session it was opened with is still live.
	sessionRecheck = time.Minute
	// reconnectAfter is how long the browser waits before reconnecting a
	// dropped stream (the SSE retry field).
	reconnectAfter = 2 * time.Second
)

// eventStream writes Server-Sent Events to one response.
type eventStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// openEventStream answers 200 text/event-stream and sends the headers
// at once, so the browser's EventSource opens before the first event.
// Errors before this point are ordinary JSON answers, which the
// EventSource treats as a failure it won't retry.
func openEventStream(w http.ResponseWriter) (*eventStream, error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	es := &eventStream{w: w, rc: http.NewResponseController(w)}
	if _, err := fmt.Fprintf(w, "retry: %d\n\n", reconnectAfter.Milliseconds()); err != nil {
		return nil, err
	}
	return es, es.rc.Flush()
}

// send writes one event and flushes it. The event message is the unnamed
// one; an empty id leaves the browser's last event ID as it was.
// Each line of data goes on its own data: line, which the browser joins
// again with "\n", so data arrives exactly as given.
func (es *eventStream) send(event StreamEvent, id, data string) error {
	var b strings.Builder
	if event != message {
		fmt.Fprintf(&b, "event: %s\n", event)
	}
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	for line := range strings.SplitSeq(newlines.Replace(data), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	if _, err := es.w.Write([]byte(b.String())); err != nil {
		return err
	}
	return es.rc.Flush()
}

// ansiEscape matches the terminal escape sequences build tools and Apps
// colour their output with: CSI (colours, cursor moves), OSC (titles,
// links) ended by BEL or ST, the two-byte escapes, and any ESC left over.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]|\x1b")

// newlines turns every line end a terminal knows into "\n". SSE splits
// lines on "\r" too, so a bare "\r" (a progress bar redrawing its line)
// would otherwise break an event apart.
var newlines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// cleanText is log output as the Dashboard shows it: ANSI escape codes
// stripped (the Dashboard shows logs as plain text and doesn't render
// colours; spec #68, user story 62) and line ends made "\n". It works on any span of whole lines, since no escape spans one.
func cleanText(b []byte) string {
	s := string(b)
	if strings.IndexByte(s, '\x1b') >= 0 {
		s = ansiEscape.ReplaceAllString(s, "")
	}
	if strings.IndexByte(s, '\r') >= 0 {
		s = newlines.Replace(s)
	}
	return s
}

// streamContext is the context a stream runs in: it ends when the client
// goes away, when the service shuts down (Deps.Stopping) and, for a
// request let in by its session cookie, when that session has ended.
// The session is re-checked every sessionRecheck with
// Sessions.CheckSession, which doesn't count as activity: an open stream
// never keeps its session alive. A request let in by the Admin Token
// (see requireCredential) has no session to re-check.
func (h *handler) streamContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	if h.deps.Stopping != nil {
		go func() {
			select {
			case <-h.deps.Stopping:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	if r.Header.Get("Authorization") != "" {
		return ctx, cancel
	}
	token := sessioncookie.Token(r)
	tick := h.deps.After(sessionRecheck) // set before the stream opens
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick:
			}
			err := h.deps.Sessions.CheckSession(ctx, token)
			switch {
			case errors.Is(err, service.ErrNoSession):
				return
			case err != nil && ctx.Err() == nil:
				h.deps.Log.Warn("re-check a stream's session", "err", err) // try again next time
			}
			tick = h.deps.After(sessionRecheck)
		}
	}()
	return ctx, cancel
}
