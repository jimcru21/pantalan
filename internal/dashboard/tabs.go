package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// Apps names the apps that Pantalan's containers belong to;
// service.AppService provides it.
type Apps interface {
	List(ctx context.Context) ([]domain.App, error)
}

// tabs is the Node overview's tab bar, in order. Each tab is a fragment
// of the same name, which renders the bar too, so switching tabs swaps
// the whole tabs section.
var tabs = []struct{ Name, Label string }{
	{"containers", "Containers"},
	{"images", "Images"},
	{"docker", "Docker"},
	{"events", "Events"},
}

// tabNav is the tab bar with Active marked.
type tabNav struct {
	Active string
	Tabs   []struct{ Name, Label string }
}

func nav(active string) tabNav { return tabNav{Active: active, Tabs: tabs} }

// containersTab is the dot of the "containers" block.
type containersTab struct {
	Nav tabNav
	// DockerErr is why Docker is unreachable; the tab is an error card
	// then.
	DockerErr string
	Rows      []containerRow
}

type containerRow struct {
	Name  string
	App   string // the Pantalan app's name, or "" for other containers
	Image string
	State string // "running · healthy", or Docker's status when not running
	Bad   bool   // the state is unhealthy or restarting
	Ports string
	// Uptime is how long it has been up, "–" when it isn't running.
	Uptime      string
	CPU, Memory string
}

func (d *Dashboard) containersData(r *http.Request) (any, error) {
	return d.containersTab(r.Context(), d.readNode(r.Context())), nil
}

func (d *Dashboard) containersTab(ctx context.Context, n node) containersTab {
	tab := containersTab{Nav: nav("containers")}
	if n.dockerErr != nil {
		tab.DockerErr = n.dockerErr.Error()
		return tab
	}
	apps := d.appNames(ctx, n.docker.Containers)
	var running []string
	for _, c := range n.docker.Containers {
		if c.Running() {
			running = append(running, c.ID)
		}
	}
	stats := d.deps.Docker.Stats(ctx, n.docker.APIVersion, running)
	for _, c := range n.docker.Containers {
		row := containerRow{
			Name:   c.Name(),
			Image:  c.Image,
			State:  c.Status,
			Bad:    containerTrouble(c) != "",
			Ports:  formatPorts(c.Ports),
			Uptime: "–",
			CPU:    "–",
			Memory: "–",
		}
		if id, ok := c.Labels[domain.AppLabel]; ok {
			row.App = id
			if name, ok := apps[id]; ok {
				row.App = name
			}
		}
		if c.Running() {
			row.State = "running"
			if h := c.HealthStatus(); h != "" {
				row.State += " · " + h
			}
			row.Uptime = uptime(c.Status)
			if s, ok := stats[c.ID]; ok {
				if s.HasCPU {
					row.CPU = fmt.Sprintf("%.1f%%", s.CPUPercent)
				}
				row.Memory = formatBytes(s.MemUsed) + " / " + formatBytes(s.MemLimit)
			}
		}
		tab.Rows = append(tab.Rows, row)
	}
	return tab
}

// imagesTab is the dot of the "images" block.
type imagesTab struct {
	Nav       tabNav
	DockerErr string // as in containersTab
	Rows      []imageRow
}

type imageRow struct {
	Tags    string // "repo:tag, repo:other", or "<none>:<none>"
	Size    string
	Created string // the date, UTC
	InUse   bool   // a container, running or not, uses it
}

func (d *Dashboard) imagesData(r *http.Request) (any, error) {
	ctx := r.Context()
	n := d.readNode(ctx)
	tab := imagesTab{Nav: nav("images")}
	var images []nodeinfo.Image
	if n.dockerErr == nil {
		images, n.dockerErr = d.deps.Docker.Images(ctx, n.docker.APIVersion)
	}
	if n.dockerErr != nil {
		tab.DockerErr = n.dockerErr.Error()
		return tab, nil
	}
	used := map[string]bool{}
	for _, c := range n.docker.Containers {
		used[c.ImageID] = true
	}
	for _, img := range images {
		tags := strings.Join(img.RepoTags, ", ")
		if tags == "" {
			tags = "<none>:<none>"
		}
		tab.Rows = append(tab.Rows, imageRow{
			Tags:    tags,
			Size:    formatBytes(uint64(max(img.Size, 0))),
			Created: time.Unix(img.Created, 0).UTC().Format(time.DateOnly),
			InUse:   used[img.ID],
		})
	}
	return tab, nil
}

// dockerTab is the dot of the "docker" block: the engine's details and
// its disk usage.
type dockerTab struct {
	Nav       tabNav
	DockerErr string // as in containersTab
	Version   string
	// APIVersion is the one Pantalan uses; DaemonAPI the range the
	// daemon accepts.
	APIVersion, DaemonAPI string
	Driver, Cgroup        string
	RootDir               string
	Warnings              []string
	DiskUsage             []diskUseRow
	// DiskUsageErr is why system/df failed; the rest still shows.
	DiskUsageErr string
	DiskUsageAt  string
}

type diskUseRow struct {
	Kind              string
	Total, Active     int
	Size, Reclaimable string
}

func (d *Dashboard) dockerData(r *http.Request) (any, error) {
	ctx := r.Context()
	n := d.readNode(ctx)
	tab := dockerTab{Nav: nav("docker")}
	var v nodeinfo.DockerVersion
	if n.dockerErr == nil {
		v, n.dockerErr = d.deps.Docker.Version(ctx, n.docker.APIVersion)
	}
	if n.dockerErr != nil {
		tab.DockerErr = n.dockerErr.Error()
		return tab, nil
	}
	info := n.docker.Info
	tab.Version = v.Version
	tab.APIVersion = n.docker.APIVersion
	tab.DaemonAPI = v.MinAPIVersion + "–" + n.docker.DaemonAPIVersion
	tab.Driver = info.Driver
	tab.Cgroup = "v" + info.CgroupVersion
	if info.CgroupDriver != "" {
		tab.Cgroup += " (" + info.CgroupDriver + ")"
	}
	tab.RootDir = info.DockerRootDir
	tab.Warnings = info.Warnings

	du, err := d.deps.Docker.DiskUsage(ctx, n.docker.APIVersion)
	if err != nil {
		tab.DiskUsageErr = err.Error()
		return tab, nil
	}
	tab.DiskUsageAt = du.At.Format("15:04:05")
	for _, k := range []struct {
		kind string
		use  nodeinfo.DiskUse
	}{{"Images", du.Images}, {"Containers", du.Containers}, {"Volumes", du.Volumes}, {"Build cache", du.BuildCache}} {
		tab.DiskUsage = append(tab.DiskUsage, diskUseRow{
			Kind: k.kind, Total: k.use.Total, Active: k.use.Active,
			Size: formatBytes(uint64(max(k.use.Size, 0))), Reclaimable: formatBytes(uint64(max(k.use.Reclaimable, 0))),
		})
	}
	return tab, nil
}

// appNames maps app IDs to names, when any container is an app's.
func (d *Dashboard) appNames(ctx context.Context, cs []nodeinfo.Container) map[string]string {
	names := map[string]string{}
	hasApp := false
	for _, c := range cs {
		_, ok := c.Labels[domain.AppLabel]
		hasApp = hasApp || ok
	}
	if !hasApp || d.deps.Apps == nil {
		return names
	}
	apps, err := d.deps.Apps.List(ctx)
	if err != nil {
		d.log.Warn("list apps for the containers tab", "err", err)
		return names
	}
	for _, a := range apps {
		names[a.ID] = a.Name
	}
	return names
}

// uptime is how long Docker says a running container has been up: its
// status "Up 3 hours (healthy)" without the "Up" and the health.
func uptime(status string) string {
	s := strings.TrimPrefix(status, "Up ")
	if i := strings.LastIndex(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		s = s[:i]
	}
	if s == "" {
		return "–"
	}
	return s
}

// formatPorts lists ports as "127.0.0.1:49153→8080/tcp" when published
// and "5432/tcp" when not, leaving out the IPv6 twin Docker lists next to
// a port published on every IPv4 address.
func formatPorts(ports []nodeinfo.Port) string {
	type key struct {
		public, private int
		proto           string
	}
	v4 := map[key]bool{}
	for _, p := range ports {
		if p.PublicPort != 0 && !strings.Contains(p.IP, ":") {
			v4[key{p.PublicPort, p.PrivatePort, p.Type}] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range ports {
		var s string
		switch {
		case p.PublicPort == 0:
			s = fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
		case strings.Contains(p.IP, ":"):
			if v4[key{p.PublicPort, p.PrivatePort, p.Type}] {
				continue
			}
			s = fmt.Sprintf("[%s]:%d→%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type)
		default:
			s = fmt.Sprintf("%s:%d→%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "–"
	}
	return strings.Join(out, ", ")
}
