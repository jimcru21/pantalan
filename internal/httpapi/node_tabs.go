package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// nodeEvents serves the event ring, which outlives Docker going down.
func (h *handler) nodeEvents(w http.ResponseWriter, _ *http.Request) {
	out := NodeEventLog{Events: []NodeEvent{}}
	if h.deps.Events == nil {
		h.writeJSON(w, http.StatusOK, out)
		return
	}
	log := h.deps.Events.Recent()
	if log.Err != nil {
		out.StreamError = log.Err.Error()
	}
	for _, ev := range log.Events {
		attrs := ev.Actor.Attributes
		e := NodeEvent{
			Time:        ev.Time().UTC().Format(time.RFC3339Nano),
			ContainerID: ev.Actor.ID,
			Container:   attrs["name"],
			Image:       attrs["image"],
			Action:      ev.Action,
			GapBefore:   ev.GapBefore,
		}
		if action, health, ok := strings.Cut(ev.Action, ": "); ok && action == "health_status" {
			e.Action, e.Health = action, health
		}
		switch e.Action {
		case "die":
			if code, err := strconv.Atoi(attrs["exitCode"]); err == nil {
				e.ExitCode = &code
			}
		case "kill":
			e.Signal = attrs["signal"]
		}
		out.Events = append(out.Events, e)
	}
	h.writeJSON(w, http.StatusOK, out)
}

// dockerUnreachable answers a tab that reads Docker while it can't be.
func (h *handler) dockerUnreachable(w http.ResponseWriter, err error) {
	h.writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "Docker unreachable: " + err.Error()})
}

func (h *handler) nodeContainers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := h.readNode(ctx)
	if n.dockerErr != nil {
		h.dockerUnreachable(w, n.dockerErr)
		return
	}
	names := h.appNames(ctx, n.docker.Containers)
	var running []string
	for _, c := range n.docker.Containers {
		if c.Running() {
			running = append(running, c.ID)
		}
	}
	stats := h.deps.Docker.Stats(ctx, n.docker.APIVersion, running)
	out := []NodeContainer{}
	for _, c := range n.docker.Containers {
		ctr := NodeContainer{
			ID:      c.ID,
			Name:    c.Name(),
			Image:   c.Image,
			Created: time.Unix(c.Created, 0).UTC().Format(time.RFC3339),
			State:   c.State,
			Health:  c.HealthStatus(),
			Status:  c.Status,
			Trouble: containerTrouble(c),
			Ports:   ports(c.Ports),
		}
		if id, ok := c.Labels[domain.AppLabel]; ok {
			ctr.App = &ContainerApp{ID: id, Name: names[id]}
		}
		if s, ok := stats[c.ID]; ok && c.Running() {
			ctr.Stats = &ContainerStats{MemoryUsed: s.MemUsed, MemoryLimit: s.MemLimit}
			if s.HasCPU {
				cpu := s.CPUPercent / 100
				ctr.Stats.CPU = &cpu
			}
		}
		out = append(out, ctr)
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *handler) nodeImages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := h.readNode(ctx)
	var images []nodeinfo.Image
	if n.dockerErr == nil {
		images, n.dockerErr = h.deps.Docker.Images(ctx, n.docker.APIVersion)
	}
	if n.dockerErr != nil {
		h.dockerUnreachable(w, n.dockerErr)
		return
	}
	used := map[string]bool{}
	for _, c := range n.docker.Containers {
		used[c.ImageID] = true
	}
	out := []NodeImage{}
	for _, img := range images {
		tags := img.RepoTags
		if tags == nil {
			tags = []string{}
		}
		out = append(out, NodeImage{
			ID:      img.ID,
			Tags:    tags,
			Size:    img.Size,
			Created: time.Unix(img.Created, 0).UTC().Format(time.RFC3339),
			InUse:   used[img.ID],
		})
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *handler) nodeDocker(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := h.readNode(ctx)
	var v nodeinfo.DockerVersion
	if n.dockerErr == nil {
		v, n.dockerErr = h.deps.Docker.Version(ctx, n.docker.APIVersion)
	}
	if n.dockerErr != nil {
		h.dockerUnreachable(w, n.dockerErr)
		return
	}
	info := n.docker.Info
	out := NodeDockerInfo{
		Version:       v.Version,
		APIVersion:    n.docker.APIVersion,
		MinAPIVersion: v.MinAPIVersion,
		MaxAPIVersion: n.docker.DaemonAPIVersion,
		Driver:        info.Driver,
		CgroupVersion: info.CgroupVersion,
		CgroupDriver:  info.CgroupDriver,
		RootDir:       info.DockerRootDir,
		Warnings:      info.Warnings,
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	du, err := h.diskUsage.get(ctx, h.deps.Docker, n.docker.APIVersion)
	if err != nil {
		out.DiskUsageError = err.Error()
	} else {
		use := func(u nodeinfo.DiskUse) DockerDiskUse {
			return DockerDiskUse{Total: u.Total, Active: u.Active, Size: u.Size, Reclaimable: u.Reclaimable}
		}
		out.DiskUsage = &DockerDiskUsage{
			Images:     use(du.Images),
			Containers: use(du.Containers),
			Volumes:    use(du.Volumes),
			BuildCache: use(du.BuildCache),
			At:         du.At.UTC().Format(time.RFC3339),
		}
	}
	h.writeJSON(w, http.StatusOK, out)
}

// diskUsageTTL is how long a system/df answer is shared before the next
// request asks Docker again: system/df is slow and grows with the Node's
// data.
const diskUsageTTL = 60 * time.Second

// sharedDiskUsage shares Docker's system/df answer between requests for
// diskUsageTTL after Docker computed it. Concurrent requests
// wait for one call rather than each making their own; a failed call
// isn't shared.
type sharedDiskUsage struct {
	now func() time.Time

	mu   sync.Mutex // held while system/df runs
	last *nodeinfo.DiskUsage
}

func (s *sharedDiskUsage) get(ctx context.Context, docker NodeDocker, apiVersion string) (nodeinfo.DiskUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil && s.now().Sub(s.last.At) < diskUsageTTL {
		return *s.last, nil
	}
	du, err := docker.DiskUsage(ctx, apiVersion)
	if err != nil {
		return nodeinfo.DiskUsage{}, err
	}
	s.last = &du
	return du, nil
}

// appNames maps App IDs to names, when any container is an App's.
func (h *handler) appNames(ctx context.Context, cs []nodeinfo.Container) map[string]string {
	names := map[string]string{}
	hasApp := false
	for _, c := range cs {
		_, ok := c.Labels[domain.AppLabel]
		hasApp = hasApp || ok
	}
	if !hasApp || h.deps.Apps == nil {
		return names
	}
	apps, err := h.deps.Apps.List(ctx)
	if err != nil {
		h.deps.Log.Warn("list apps for the containers tab", "err", err)
		return names
	}
	for _, a := range apps {
		names[a.ID] = a.Name
	}
	return names
}

// ports are a container's ports without duplicates and without the IPv6
// twin Docker lists next to a port published on every IPv4 address.
func ports(ps []nodeinfo.Port) []ContainerPort {
	type key struct {
		public, private int
		proto           string
	}
	v4 := map[key]bool{}
	for _, p := range ps {
		if p.PublicPort != 0 && !strings.Contains(p.IP, ":") {
			v4[key{p.PublicPort, p.PrivatePort, p.Type}] = true
		}
	}
	out := []ContainerPort{}
	seen := map[ContainerPort]bool{}
	for _, p := range ps {
		if strings.Contains(p.IP, ":") && v4[key{p.PublicPort, p.PrivatePort, p.Type}] {
			continue
		}
		cp := ContainerPort{PrivatePort: p.PrivatePort, Type: p.Type}
		if p.PublicPort != 0 {
			cp.IP, cp.PublicPort = p.IP, p.PublicPort
		}
		if !seen[cp] {
			seen[cp] = true
			out = append(out, cp)
		}
	}
	return out
}
