package nodeinfo

import (
	"context"
	"net/url"
	"sync"
)

// MaxStatsInFlight is how many container stats requests run at once,
// across every caller.
const MaxStatsInFlight = 8

// ContainerStats is one container's CPU and memory from a one-shot stats
// request.
type ContainerStats struct {
	// CPUPercent is the container's CPU since the previous sample, as
	// docker stats counts it (100 % is one CPU). HasCPU is false on the
	// first sample of a container.
	CPUPercent float64
	HasCPU     bool
	// MemUsed is the usage minus inactive file, as docker stats counts
	// it; MemLimit is the container's limit (the host's memory without
	// one).
	MemUsed, MemLimit uint64
}

// cpuSample is the cumulative CPU counters of one stats sample, in ns,
// and the CPU % worked out from it, if any.
type cpuSample struct {
	container, system uint64
	percent           float64
	hasPercent        bool
}

// statsBody is the part of a stats response Pantalan reads.
type statsBody struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

// memUsed is usage minus inactive file: total_inactive_file on cgroup v1,
// inactive_file on v2, whichever is present and below the usage.
func (b statsBody) memUsed() uint64 {
	m := b.MemoryStats
	for _, key := range []string{"total_inactive_file", "inactive_file"} {
		if v, ok := m.Stats[key]; ok && v < m.Usage {
			return m.Usage - v
		}
	}
	return m.Usage
}

// Stats takes one-shot stats of each container in ids, through the
// apiVersion a Summary negotiated, at most MaxStatsInFlight at a time.
// A container whose stats fail is left out. Each container's CPU % is
// a delta against its previous sample, which Stats keeps; containers not
// in ids are forgotten.
func (d *Docker) Stats(ctx context.Context, apiVersion string, ids []string) map[string]ContainerStats {
	var mu sync.Mutex
	bodies := make(map[string]statsBody, len(ids))
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			select {
			case d.statsSlots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-d.statsSlots }()
			var b statsBody
			q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
			if err := d.get(ctx, apiVersion, "/containers/"+url.PathEscape(id)+"/stats", q, &b); err != nil {
				return
			}
			mu.Lock()
			bodies[id] = b
			mu.Unlock()
		})
	}
	wg.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]ContainerStats, len(bodies))
	for id, b := range bodies {
		s := ContainerStats{MemUsed: b.memUsed(), MemLimit: b.MemoryStats.Limit}
		cur := cpuSample{container: b.CPUStats.CPUUsage.TotalUsage, system: b.CPUStats.SystemCPUUsage}
		prev, ok := d.prevCPU[id]
		switch {
		case ok && cur.system == prev.system && prev.hasPercent:
			// The daemon hasn't sampled since (it does every second), as
			// when two Dashboards poll at once: the last figure stands.
			cur = prev
		case ok && cur.system > prev.system && cur.container >= prev.container:
			cpus := max(b.CPUStats.OnlineCPUs, 1)
			cur.percent = float64(cur.container-prev.container) / float64(cur.system-prev.system) * float64(cpus) * 100
			cur.hasPercent = true
		}
		s.CPUPercent, s.HasCPU = cur.percent, cur.hasPercent
		d.prevCPU[id] = cur
		out[id] = s
	}
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	for id := range d.prevCPU {
		if !keep[id] {
			delete(d.prevCPU, id)
		}
	}
	return out
}
