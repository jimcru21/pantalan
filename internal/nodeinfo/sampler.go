package nodeinfo

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// SampleInterval is how often the sampler reads the host.
	SampleInterval = 5 * time.Second
	// HistoryLen is how many samples the ring keeps: 10 minutes' worth.
	HistoryLen = 120
)

// Sample is one reading of the host as the sampler stored it.
type Sample struct {
	Reading
	// CPU is the busy share of all CPUs since the previous sample, 0–100.
	// HasCPU is false for the first sample, which has nothing to compare
	// with, and when the counters didn't move.
	CPUPercent float64
	HasCPU     bool
}

// Snapshot is the sampler's state: the newest sample and the ring.
type Snapshot struct {
	// History is the ring, oldest first; its last entry is the latest
	// sample. It is empty until a read succeeds.
	History []Sample
	// Err is why the latest read failed, if it did. History then still
	// holds the earlier samples.
	Err error
}

// Latest is the newest sample, if any.
func (s Snapshot) Latest() (Sample, bool) {
	if len(s.History) == 0 {
		return Sample{}, false
	}
	return s.History[len(s.History)-1], true
}

// Sampler reads the host in the background into a ring of HistoryLen
// samples, which feeds the sparklines and the CPU delta.
type Sampler struct {
	host Host
	log  *slog.Logger

	mu      sync.Mutex
	ring    []Sample // oldest first, at most HistoryLen
	prevCPU CPUTimes
	err     error
}

func NewSampler(host Host, log *slog.Logger) *Sampler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Sampler{host: host, log: log}
}

// Run samples once at once and then on every tick until ctx is done.
// ticks is nil in production, which means every SampleInterval.
func (s *Sampler) Run(ctx context.Context, ticks <-chan time.Time) {
	if ticks == nil {
		t := time.NewTicker(SampleInterval)
		defer t.Stop()
		ticks = t.C
	}
	s.sample()
	for {
		select {
		case <-ticks:
			s.sample()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Sampler) sample() {
	r, err := s.host.Read()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		// Logged once per failure, not on every tick of a lasting one.
		if s.err == nil || s.err.Error() != err.Error() {
			s.log.Warn("read host", "err", err)
		}
		s.err = err
		return
	}
	if s.err != nil {
		s.log.Info("read host recovered")
		s.err = nil
	}
	smp := Sample{Reading: r}
	if len(s.ring) > 0 && r.CPU.Total > s.prevCPU.Total && r.CPU.Busy >= s.prevCPU.Busy {
		smp.CPUPercent = 100 * float64(r.CPU.Busy-s.prevCPU.Busy) / float64(r.CPU.Total-s.prevCPU.Total)
		smp.HasCPU = true
	}
	s.prevCPU = r.CPU
	if len(s.ring) == HistoryLen {
		s.ring = append(s.ring[:0], s.ring[1:]...)
	}
	s.ring = append(s.ring, smp)
}

// Snapshot returns a copy of the sampler's state.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{History: append([]Sample(nil), s.ring...), Err: s.err}
}
