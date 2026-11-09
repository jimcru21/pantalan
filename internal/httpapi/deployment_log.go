package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/pantalan/pantalan/internal/domain"
)

const (
	// logReplay is how much of a Deployment Log a first connect replays:
	// its last 1 MiB, so a noisy build can't freeze the browser.
	logReplay = 1 << 20
	// logPoll is how often a stream looks for new output while its
	// Deployment runs.
	logPoll = 250 * time.Millisecond
	// logChunk is the most a stream reads, and sends, at once.
	logChunk = 64 << 10
)

// deploymentLog serves a Deployment's whole Deployment Log as plain text,
// cleaned as the stream is. A Deployment that hasn't started has an
// empty one.
func (h *handler) deploymentLog(w http.ResponseWriter, r *http.Request) {
	dep, err := h.deps.Deployer.Get(r.Context(), r.PathValue("id"), r.PathValue("deploymentID"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	f, err := h.deps.Deployer.OpenLog(r.Context(), dep.ID)
	if errors.Is(err, domain.ErrNotFound) {
		h.sendPlainLog(w, nil)
		return
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	defer h.closeLog(f)
	h.sendPlainLog(w, f)
}

// deploymentLogs is the older address of the plain-text Deployment Log,
// by the Deployment's ID alone. It is kept for scripts.
func (h *handler) deploymentLogs(w http.ResponseWriter, r *http.Request) {
	f, err := h.deps.Deployer.OpenLog(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	defer h.closeLog(f)
	h.sendPlainLog(w, f)
}

// sendPlainLog writes the log in f (none when nil) as plain text. nosniff
// keeps a browser from rendering output that looks like HTML.
func (h *handler) sendPlainLog(w http.ResponseWriter, f *os.File) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if f == nil {
		return
	}
	in := bufio.NewReaderSize(f, logChunk)
	for {
		line, err := in.ReadSlice('\n') // a longer line comes in pieces
		if len(line) > 0 {
			if _, werr := io.WriteString(w, cleanText(line)); werr != nil {
				h.deps.Log.Warn("send log", "err", werr)
				return
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			h.deps.Log.Warn("read log", "err", err)
			return
		}
	}
}

func (h *handler) closeLog(f *os.File) {
	if err := f.Close(); err != nil {
		h.deps.Log.Warn("close log", "err", err)
	}
}

// streamDeploymentLog streams a Deployment's Deployment Log as
// Server-Sent Events. Each chunk is a "message" event of whole lines
// whose id is the byte offset in the log after it, so a reconnect's
// Last-Event-ID resumes exactly where the last one stopped. A first
// connect replays the last logReplay bytes, preceded by an "omitted"
// event (data: how many bytes it left out) when it skipped any. The
// stream follows the log while the Deployment runs, then sends "done"
// (data: the final status) and ends.
func (h *handler) streamDeploymentLog(w http.ResponseWriter, r *http.Request) {
	appID, depID := r.PathValue("id"), r.PathValue("deploymentID")
	dep, err := h.deps.Deployer.Get(r.Context(), appID, depID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	resume := int64(-1) // a first connect
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		if resume, err = strconv.ParseInt(id, 10, 64); err != nil || resume < 0 {
			h.writeError(w, fmt.Errorf("%w: Last-Event-ID %q is not a byte offset of the log", domain.ErrInvalid, id))
			return
		}
	}

	ctx, cancel := h.streamContext(r)
	defer cancel()
	es, err := openEventStream(w)
	if err != nil {
		return // the client has gone
	}
	tail := &logTail{}
	defer func() {
		if tail.f != nil {
			h.closeLog(tail.f)
		}
	}()
	poll := time.NewTicker(logPoll)
	defer poll.Stop()
	for {
		// The Deployer closes the log before it records the end, so
		// once the end is seen, reading to EOF reads all of it.
		finished := dep.FinishedAt != nil
		if err := h.sendNewOutput(ctx, es, tail, dep.ID, resume, finished); err != nil {
			if ctx.Err() == nil {
				h.deps.Log.Warn("stream log", "deployment", dep.ID, "err", err)
			}
			return
		}
		if finished {
			_ = es.send("done", "", string(dep.Status))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
		if dep, err = h.deps.Deployer.Get(ctx, appID, depID); err != nil {
			return // the App was deleted, or the client has gone
		}
	}
}

// sendNewOutput sends what has been written to the log since the last
// call, opening it first if it exists by now. At the end of the
// Deployment it sends a last line that has no line end yet too.
func (h *handler) sendNewOutput(ctx context.Context, es *eventStream, tail *logTail, depID string, resume int64, finished bool) error {
	if tail.f == nil {
		f, err := h.deps.Deployer.OpenLog(ctx, depID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil // not started yet
		}
		if err != nil {
			return err
		}
		tail.f = f
		omitted, err := tail.start(resume)
		if err != nil {
			return err
		}
		if omitted > 0 {
			off := strconv.FormatInt(omitted, 10)
			if err := es.send("omitted", off, off); err != nil {
				return err
			}
		}
	}
	for {
		more, err := tail.read()
		if err != nil {
			return err
		}
		if chunk, end := tail.take(finished && !more); len(chunk) > 0 {
			if text := cleanText(chunk); text != "" {
				if err := es.send("", strconv.FormatInt(end, 10), text); err != nil {
					return err
				}
			}
		}
		if !more {
			return nil
		}
	}
}

// logTail reads a Deployment Log from an offset and hands it out in
// whole lines, so a chunk never ends inside an escape code or a
// character.
type logTail struct {
	f       *os.File
	offset  int64  // in the log, of pending's first byte
	pending []byte // read but not yet taken
	buf     []byte
}

// start places the tail at resume, or, for a first connect (resume < 0),
// at the start of the first line of the log's last logReplay bytes. It
// returns how many bytes that leaves out.
func (t *logTail) start(resume int64) (omitted int64, err error) {
	info, err := t.f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	switch {
	case resume >= 0:
		t.offset = min(resume, size)
	case size > logReplay:
		t.offset = size - logReplay
		head := make([]byte, logChunk)
		n, err := t.f.ReadAt(head, t.offset)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if i := bytes.IndexByte(head[:n], '\n'); i >= 0 {
			t.offset += int64(i) + 1
		}
		omitted = t.offset
	}
	_, err = t.f.Seek(t.offset, io.SeekStart)
	return omitted, err
}

// read reads up to logChunk more bytes. more reports that it read a
// whole logChunk, so there may be more already.
func (t *logTail) read() (more bool, err error) {
	if t.buf == nil {
		t.buf = make([]byte, logChunk)
	}
	n, err := t.f.Read(t.buf)
	t.pending = append(t.pending, t.buf[:n]...)
	if err == io.EOF {
		err = nil
	}
	return n == len(t.buf), err
}

// take returns the whole lines read so far, or everything when all is
// set, and the log offset after them. A line longer than logChunk comes
// in pieces, split between characters.
func (t *logTail) take(all bool) (chunk []byte, end int64) {
	n := len(t.pending)
	if !all {
		n = bytes.LastIndexByte(t.pending, '\n') + 1
		if n == 0 && len(t.pending) >= logChunk {
			n = len(t.pending)
			// Keep back a last character that isn't all there yet.
			for i := n - 1; i >= 0 && i >= n-utf8.UTFMax; i-- {
				if utf8.RuneStart(t.pending[i]) {
					if !utf8.FullRune(t.pending[i:]) {
						n = i
					}
					break
				}
			}
		}
	}
	chunk = bytes.Clone(t.pending[:n])
	t.pending = append(t.pending[:0], t.pending[n:]...)
	t.offset += int64(n)
	return chunk, t.offset
}
