package nodeinfo

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A dockerd that accepts /events but never answers must not hold the
// stream forever: it gives up after its header timeout and reconnects.
func TestAnEventStreamThatNeverAnswersTimesOutAndReconnects(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	hung := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", "1.48")
			return
		}
		select { // never answers /events
		case <-hung:
		case <-r.Context().Done():
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() {
		close(hung)
		srv.Close()
	})

	s := NewEventStream(NewDocker(socket, nil, nil), nil)
	s.headerTimeout = 50 * time.Millisecond
	waited := make(chan time.Duration, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(t.Context(), func(d time.Duration) <-chan time.Time {
			waited <- d
			return make(chan time.Time) // stay in the backoff
		})
	}()

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream still waits on a dockerd that never answered /events")
	}
	if err := s.Recent().Err; err == nil || !strings.Contains(err.Error(), "/events") {
		t.Errorf("Recent().Err = %v, want it to say /events didn't answer", err)
	}
}
