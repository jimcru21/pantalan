package nodeinfo

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
)

// maxLogFrame bounds one frame of a container's output. Docker logs a
// line in pieces of at most 16 KiB, so a bigger frame means the stream
// isn't what it looks like.
const maxLogFrame = 1 << 20

// FollowLogs follows a container's output through the Engine API's
// containers/{id}/logs, with follow and timestamps, stdout and stderr
// both. It implements service.ContainerLogs, for the App Log.
//
// A container without a TTY (every App's) answers with a multiplexed
// stream: frames of an 8-byte header (the stream: 1 stdout, 2 stderr,
// 3 an error from dockerd; 4 bytes of zero; the payload's size, big
// endian) and the payload. Each payload is one log message, a line or a
// piece of one, after its timestamp and a space.
func (d *Docker) FollowLogs(ctx context.Context, containerID string, start service.LogStart, send func(service.LogLine) error) error {
	ver, _, err := d.ping(ctx)
	if err != nil {
		return err
	}
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if !start.Since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", start.Since.Unix(), start.Since.Nanosecond()))
	} else {
		q.Set("tail", strconv.Itoa(start.Tail))
	}
	path := "/containers/" + url.PathEscape(containerID) + "/logs"
	resp, err := d.do(ctx, "/v"+ver+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusConflict: // 409: dead, or being removed
		return fmt.Errorf("%w: %w", domain.ErrNotFound, engineError(path, resp))
	default:
		return engineError(path, resp)
	}
	// Before API 1.42 dockerd called both kinds raw-stream; an App's
	// container never has a TTY, so that one is multiplexed.
	raw := resp.Header.Get("Content-Type") == "application/vnd.docker.raw-stream" && atLeast(ver, 1, 42)
	if raw {
		err = d.readRawLogs(resp.Body, send)
	} else {
		err = d.readMultiplexedLogs(resp.Body, send)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (d *Docker) readMultiplexedLogs(r io.Reader, send func(service.LogLine) error) error {
	in := bufio.NewReader(r)
	var header [8]byte
	var payload []byte
	for {
		if _, err := io.ReadFull(in, header[:]); err != nil {
			if err == io.EOF {
				return nil // the container has stopped
			}
			return fmt.Errorf("docker: read container logs: %w", err)
		}
		size := binary.BigEndian.Uint32(header[4:])
		if size > maxLogFrame {
			return fmt.Errorf("docker: container logs: a frame of %d bytes", size)
		}
		if cap(payload) < int(size) {
			payload = make([]byte, size)
		}
		payload = payload[:size]
		if _, err := io.ReadFull(in, payload); err != nil {
			return fmt.Errorf("docker: read container logs: %w", noEOF(err))
		}
		switch header[0] {
		case 0, 1, 2: // stdin (only with a TTY), stdout, stderr
			if err := send(d.logLine(string(payload), header[0] == 2)); err != nil {
				return err
			}
		case 3:
			return fmt.Errorf("docker: container logs: %s", strings.TrimSpace(string(payload)))
		default:
			return fmt.Errorf("docker: container logs: a frame of stream %d", header[0])
		}
	}
}

// readRawLogs reads a TTY's one stream, a line at a time.
func (d *Docker) readRawLogs(r io.Reader, send func(service.LogLine) error) error {
	in := bufio.NewReader(r)
	for {
		line, err := in.ReadString('\n')
		if line != "" {
			if err := send(d.logLine(line, false)); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("docker: read container logs: %w", err)
		}
	}
}

// logLine splits a log message into its timestamp and its text. One
// without a timestamp is from now.
func (d *Docker) logLine(msg string, stderr bool) service.LogLine {
	if ts, text, ok := strings.Cut(msg, " "); ok {
		if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return service.LogLine{Time: at, Stderr: stderr, Text: text}
		}
	}
	return service.LogLine{Time: d.now(), Stderr: stderr, Text: msg}
}

// noEOF makes an EOF inside a frame what it is: unexpected.
func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// atLeast reports whether API version v is major.minor or newer.
func atLeast(v string, major, minor int) bool {
	maj, mnr, _ := parseAPIVersion(v)
	return maj > major || maj == major && mnr >= minor
}
