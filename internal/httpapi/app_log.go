package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
)

// streamAppLog streams an App's App Log as Server-Sent Events: the last
// lines of its live Deployment's container, then each line as the
// container writes it, stdout and stderr alike. Each "message" event is
// one line, whose id says where it is in the App Log, so a reconnect's
// Last-Event-ID goes on after it. The stream ends with "done" (data:
// "replaced") once a new Deployment has replaced that container. An App
// with no live Deployment answers "done" (data: "no-live-deployment") at
// once: nothing to follow, which isn't an empty log.
//
// Both endings are events, not HTTP errors: the browser's EventSource
// can't read a status, and the Dashboard takes a refused stream to mean
// the session has ended.
func (h *handler) streamAppLog(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("id")
	var after service.AppLogCursor
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		var ok bool
		if after, ok = parseAppLogCursor(id); !ok {
			h.writeError(w, fmt.Errorf("%w: Last-Event-ID %q is not a place in the App Log", domain.ErrInvalid, id))
			return
		}
	}
	if _, err := h.deps.Apps.Get(r.Context(), appID); err != nil {
		h.writeError(w, err)
		return
	}

	ctx, cancel := h.streamContext(r)
	defer cancel()
	es, err := openEventStream(w)
	if err != nil {
		return // the client has gone
	}
	err = h.deps.AppLogs.Follow(ctx, appID, after, func(l service.AppLogLine) error {
		text := cleanText([]byte(l.Text))
		if text == "" {
			return nil
		}
		return es.send(message, formatAppLogCursor(l.Cursor), text)
	})
	switch {
	case errors.Is(err, service.ErrReplaced):
		_ = es.send(StreamDone, "", string(AppLogReplaced))
	case errors.Is(err, service.ErrNoLiveDeployment):
		_ = es.send(StreamDone, "", string(AppLogNoLiveDeployment))
	case ctx.Err() != nil:
		// The client left, the session ended or the service is stopping.
	default:
		// The browser reconnects, from the last line it got.
		h.deps.Log.Warn("stream app log", "app", appID, "err", err)
	}
}

// An App Log cursor as an event id: "<Deployment ID>@<Unix nanoseconds>".
func formatAppLogCursor(c service.AppLogCursor) string {
	return c.Deployment + "@" + strconv.FormatInt(c.Time.UnixNano(), 10)
}

func parseAppLogCursor(id string) (service.AppLogCursor, bool) {
	dep, nanos, ok := strings.Cut(id, "@")
	n, err := strconv.ParseInt(nanos, 10, 64)
	if !ok || dep == "" || err != nil || n <= 0 {
		return service.AppLogCursor{}, false
	}
	return service.AppLogCursor{Deployment: dep, Time: time.Unix(0, n).UTC()}, true
}
