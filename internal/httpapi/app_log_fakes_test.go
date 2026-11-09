package httpapi_test

import (
	"context"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
)

// fakeRuntime is also the App Log source: what its containers write to
// stdout and stderr, followed as Docker's container logs are. The state
// is guarded by the runtime's mutex.

// fakeOutput is every container's output, by container ID.
type fakeOutput struct {
	lines   map[string][]fakeLine
	stopped map[string]int // how many times each container has stopped
	written int            // lines written by all containers, for their times
	changes chan struct{}  // closed, and replaced, on every change
}

// fakeLine is a line, and which run of its container wrote it.
type fakeLine struct {
	service.LogLine
	run int
}

// changed wakes every follower. The runtime's mutex must be held.
func (o *fakeOutput) changed() {
	if o.changes != nil {
		close(o.changes)
	}
	o.changes = make(chan struct{})
}

// print has the container with ID id write text to stdout.
func (r *fakeRuntime) print(id, text string) { r.write(id, false, text) }

// printErr has the container with ID id write text to stderr.
func (r *fakeRuntime) printErr(id, text string) { r.write(id, true, text) }

func (r *fakeRuntime) write(id string, stderr bool, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := &r.output
	if o.lines == nil {
		o.lines = map[string][]fakeLine{}
	}
	o.written++
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(o.written) * time.Millisecond)
	line := service.LogLine{Time: at, Stderr: stderr, Text: text}
	o.lines[id] = append(o.lines[id], fakeLine{LogLine: line, run: o.stopped[id]})
	o.changed()
}

// restart stops the container with ID id, as a crash does, and Docker
// starts it again: whoever follows its output sees the output end.
func (r *fakeRuntime) restart(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.output.stopped == nil {
		r.output.stopped = map[string]int{}
	}
	r.output.stopped[id]++
	r.output.changed()
}

// FollowLogs follows the container's output as Docker's
// containers/{id}/logs?follow=1 does: from start, until the container
// stops or is removed.
func (r *fakeRuntime) FollowLogs(ctx context.Context, id string, start service.LogStart, send func(service.LogLine) error) error {
	r.mu.Lock()
	if _, ok := r.containers[id]; !ok {
		r.mu.Unlock()
		return fmt.Errorf("no such container: %s: %w", id, domain.ErrNotFound)
	}
	stops := r.output.stopped[id]
	lines := r.output.lines[id]
	next := max(0, len(lines)-start.Tail)
	if !start.Since.IsZero() {
		next = len(lines)
		for i, l := range lines {
			if !l.Time.Before(start.Since) {
				next = i
				break
			}
		}
	}
	for {
		lines := r.output.lines[id]
		var pending []service.LogLine
		for _, l := range lines[next:] {
			if l.run <= stops { // a follow ends with the run it followed
				pending = append(pending, l.LogLine)
			}
		}
		next = len(lines)
		_, exists := r.containers[id]
		running := exists && r.output.stopped[id] == stops
		if r.output.changes == nil {
			r.output.changed()
		}
		changes := r.output.changes
		r.mu.Unlock()

		for _, l := range pending {
			if err := send(l); err != nil {
				return err
			}
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
		}
		r.mu.Lock()
	}
}
