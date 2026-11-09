package nodeinfo

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
)

// fakeDockerd serves handle on a unix socket, answering /_ping with API
// version, and returns a Docker that reads it.
func fakeDockerd(t *testing.T, version string, handle http.HandlerFunc) *Docker {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", version)
			return
		}
		handle(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return NewDocker(socket, nil, nil)
}

// follow collects every line FollowLogs sends.
func follow(t *testing.T, d *Docker, start service.LogStart) ([]service.LogLine, error) {
	t.Helper()
	var lines []service.LogLine
	err := d.FollowLogs(t.Context(), "ctr-1", start, func(l service.LogLine) error {
		lines = append(lines, l)
		return nil
	})
	return lines, err
}

func at(t *testing.T, ts string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// testdata/logs-multiplexed.bin is what dockerd 29.9 (API 1.56) answered
// to containers/{id}/logs?follow=1&stdout=1&stderr=1&timestamps=1 for a
// busybox container that ran:
//
//	echo "listening on :8080"; echo "warning: disk is slow" >&2
//	printf "\033[31mred\033[0m text\n"
//	head -c 17000 /dev/zero | tr "\0" "x"; echo; echo bye >&2
//
// The 17000-character line came in two frames, as Docker logs a line
// longer than 16 KiB in pieces.
func TestFollowingLogsSplitsDockersStdoutAndStderrFramesApart(t *testing.T) {
	recorded, err := os.ReadFile("testdata/logs-multiplexed.bin")
	if err != nil {
		t.Fatal(err)
	}
	d := fakeDockerd(t, "1.56", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		w.Write(recorded)
	})

	lines, err := follow(t, d, service.LogStart{Tail: 200})
	if err != nil {
		t.Fatalf("FollowLogs = %v, want nil once the container has stopped", err)
	}
	want := []service.LogLine{
		{Time: at(t, "2026-11-09T13:17:18.682640283Z"), Stderr: true, Text: "warning: disk is slow\n"},
		{Time: at(t, "2026-11-09T13:17:18.682698371Z"), Text: "listening on :8080\n"},
		{Time: at(t, "2026-11-09T13:17:18.682701976Z"), Text: "\x1b[31mred\x1b[0m text\n"},
		{Time: at(t, "2026-11-09T13:17:18.707445971Z"), Stderr: true, Text: "bye\n"},
		{Time: at(t, "2026-11-09T13:17:18.707459843Z"), Text: strings.Repeat("x", 16384)},
		{Time: at(t, "2026-11-09T13:17:18.707459843Z"), Text: strings.Repeat("x", 17000-16384) + "\n"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(want), lines)
	}
	for i := range want {
		if !lines[i].Time.Equal(want[i].Time) || lines[i].Stderr != want[i].Stderr || lines[i].Text != want[i].Text {
			t.Errorf("line %d = %+v, want %+v", i, short(lines[i]), short(want[i]))
		}
	}
}

func short(l service.LogLine) service.LogLine {
	if len(l.Text) > 40 {
		l.Text = l.Text[:20] + "..." + l.Text[len(l.Text)-10:]
	}
	return l
}

func TestFollowingLogsAsksForTheTailOrEverythingSinceATime(t *testing.T) {
	queries := make(chan string, 2)
	d := fakeDockerd(t, "1.56", func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
	})

	if _, err := follow(t, d, service.LogStart{Tail: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := follow(t, d, service.LogStart{Since: at(t, "2026-11-09T13:17:18.000000042Z")}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/v1.56/containers/ctr-1/logs?follow=1&stderr=1&stdout=1&tail=200&timestamps=1",
		"/v1.56/containers/ctr-1/logs?follow=1&since=1794230238.000000042&stderr=1&stdout=1&timestamps=1",
	} {
		if got := <-queries; got != want {
			t.Errorf("asked dockerd for %s, want %s", got, want)
		}
	}
}

// A container started with a TTY has one raw stream, all of it stdout.
func TestFollowingLogsReadsARawStreamAsStdout(t *testing.T) {
	d := fakeDockerd(t, "1.56", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		w.Write([]byte("2026-11-09T13:17:18.682640283Z one\r\n2026-11-09T13:17:18.682698371Z two\r\n"))
	})
	lines, err := follow(t, d, service.LogStart{Tail: 200})
	if err != nil {
		t.Fatal(err)
	}
	want := []service.LogLine{
		{Time: at(t, "2026-11-09T13:17:18.682640283Z"), Text: "one\r\n"},
		{Time: at(t, "2026-11-09T13:17:18.682698371Z"), Text: "two\r\n"},
	}
	if len(lines) != 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Errorf("got %+v, want %+v", lines, want)
	}
}

func TestFollowingTheLogsOfAContainerThatIsGoneIsNotFound(t *testing.T) {
	answers := map[string]struct {
		status  int
		message string
	}{
		"no such container": {http.StatusNotFound, "No such container: ctr-1"},
		"being removed":     {http.StatusConflict, "can not get logs from container which is dead or marked for removal"},
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			d := fakeDockerd(t, "1.56", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(answer.status)
				fmt.Fprintf(w, `{"message":%q}`, answer.message)
			})
			if _, err := follow(t, d, service.LogStart{Tail: 200}); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("FollowLogs = %v, want domain.ErrNotFound", err)
			}
		})
	}
}

// A truncated frame is an error, not the container having stopped.
func TestFollowingLogsCutOffInsideAFrameIsAnError(t *testing.T) {
	d := fakeDockerd(t, "1.56", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		w.Write([]byte("\x01\x00\x00\x00\x00\x00\x00\x40" + "2026-11-09T13:17:18.682640283Z cut"))
	})
	if _, err := follow(t, d, service.LogStart{Tail: 200}); err == nil {
		t.Error("FollowLogs = nil for a stream cut off inside a frame, want an error")
	}
}
