package httpapi_test

import (
	"context"
	"sync"
	"syscall"

	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// The Node information sources the overview reads through: the host
// sampler, Docker's Engine API and Docker's event ring. Each holds what
// a test set on it and answers with that.

// fakeHost is the host sampler; set its snapshot with set.
type fakeHost struct {
	mu   sync.Mutex
	snap nodeinfo.Snapshot
}

func (h *fakeHost) set(snap nodeinfo.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snap = snap
}

func (h *fakeHost) Snapshot() nodeinfo.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snap
}

// fakeDocker is Docker's Engine API. While down is set, every call
// fails with it, as an unreachable daemon does. DiskUsage answers with
// diskUsage stamped with the seam clock's time, and counts its calls.
type fakeDocker struct {
	clock *fakeClock

	mu        sync.Mutex
	down      error
	summary   nodeinfo.DockerSummary
	stats     map[string]nodeinfo.ContainerStats
	images    []nodeinfo.Image
	version   nodeinfo.DockerVersion
	diskUsage nodeinfo.DiskUsage
	dfErr     error
	dfCalls   int
}

func (d *fakeDocker) change(f func(*fakeDocker)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

func (d *fakeDocker) Summary(context.Context) (nodeinfo.DockerSummary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down != nil {
		return nodeinfo.DockerSummary{}, d.down
	}
	return d.summary, nil
}

func (d *fakeDocker) Stats(_ context.Context, _ string, ids []string) map[string]nodeinfo.ContainerStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]nodeinfo.ContainerStats{}
	for _, id := range ids {
		if s, ok := d.stats[id]; ok {
			out[id] = s
		}
	}
	return out
}

func (d *fakeDocker) Images(context.Context, string) ([]nodeinfo.Image, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down != nil {
		return nil, d.down
	}
	return d.images, nil
}

func (d *fakeDocker) Version(context.Context, string) (nodeinfo.DockerVersion, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down != nil {
		return nodeinfo.DockerVersion{}, d.down
	}
	return d.version, nil
}

func (d *fakeDocker) DiskUsage(context.Context, string) (nodeinfo.DiskUsage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dfCalls++
	if d.down != nil {
		return nodeinfo.DiskUsage{}, d.down
	}
	if d.dfErr != nil {
		return nodeinfo.DiskUsage{}, d.dfErr
	}
	du := d.diskUsage
	du.At = d.clock.Now()
	return du, nil
}

func (d *fakeDocker) diskUsageCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dfCalls
}

// fakeEvents is Docker's recent container events.
type fakeEvents struct {
	mu  sync.Mutex
	log nodeinfo.EventLog
}

func (e *fakeEvents) set(log nodeinfo.EventLog) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = log
}

func (e *fakeEvents) Recent() nodeinfo.EventLog {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.log
}

// fsid is a statfs filesystem ID; disks with the same one are the same
// filesystem.
func fsid(n int32) syscall.Fsid { return syscall.Fsid{X__val: [2]int32{n, 0}} }
