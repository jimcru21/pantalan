package nodeinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MaxAPIVersion is the newest Docker Engine API version Pantalan knows.
// Docker negotiates down to it: requests use the version /_ping reports,
// capped at this one.
const MaxAPIVersion = "1.56"

// requestTimeout bounds every Engine API call except streams, so a hung
// daemon can't hang a Dashboard request.
const requestTimeout = 5 * time.Second

// Docker reads the Node's Docker through the Engine API on its unix
// socket, with net/http and no SDK. It only reads: container lifecycle for
// deployments stays on the docker CLI (internal/runtime/docker).
type Docker struct {
	statfs func(path string, buf *syscall.Statfs_t) error
	now    func() time.Time
	client *http.Client
	// statsSlots holds one token per stats request in flight, across all
	// callers: at most MaxStatsInFlight.
	statsSlots chan struct{}

	mu sync.Mutex
	// prevCPU is each running container's previous CPU sample, which
	// the next one's CPU % is a delta against.
	prevCPU map[string]cpuSample
}

// NewDocker reads the Engine API on socket. statfs is syscall.Statfs and
// now is time.Now, or fakes in tests; nil means the real ones.
func NewDocker(socket string, statfs func(path string, buf *syscall.Statfs_t) error, now func() time.Time) *Docker {
	if statfs == nil {
		statfs = syscall.Statfs
	}
	if now == nil {
		now = time.Now
	}
	var dialer net.Dialer
	return &Docker{
		statfs:     statfs,
		now:        now,
		statsSlots: make(chan struct{}, MaxStatsInFlight),
		prevCPU:    map[string]cpuSample{},
		client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socket)
			},
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
}

// DockerSummary is Docker as the top bar and the stat band show it.
type DockerSummary struct {
	// APIVersion is the version requests use: the daemon's, capped at
	// MaxAPIVersion. DaemonAPIVersion is the one /_ping reported.
	APIVersion, DaemonAPIVersion string
	Info                         DockerInfo
	Containers                   []Container // every container, running or not
	// RootDisk is the filesystem holding Info.DockerRootDir, unless
	// RootDiskErr says why it couldn't be read.
	RootDisk    Disk
	RootDiskErr error
}

// DockerInfo is the part of GET /info the Dashboard shows.
type DockerInfo struct {
	ServerVersion string
	DockerRootDir string
	Driver        string // storage driver
	CgroupVersion string
	CgroupDriver  string
	Warnings      []string
}

// Container is one entry of GET /containers/json.
type Container struct {
	ID      string `json:"Id"`
	Names   []string
	Image   string
	ImageID string
	Created int64 // Unix seconds
	State   string
	Status  string // Docker's own text: "Up 2 hours (healthy)"
	Labels  map[string]string
	Ports   []Port
	// Health is set from API 1.52 on; HealthStatus falls back to Status
	// before that.
	Health *struct{ Status string }
}

// Port is one of a container's ports, published or not.
type Port struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

// Name is the container's name without Docker's leading slash.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		return c.ID
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Running reports whether the container is running.
func (c Container) Running() bool { return c.State == "running" }

// HealthStatus is "healthy", "unhealthy", "starting", or "" for a
// container without a health check.
func (c Container) HealthStatus() string {
	if c.Health != nil && c.Health.Status != "none" {
		return c.Health.Status
	}
	switch {
	case strings.HasSuffix(c.Status, "(unhealthy)"):
		return "unhealthy"
	case strings.HasSuffix(c.Status, "(healthy)"):
		return "healthy"
	case strings.HasSuffix(c.Status, "(health: starting)"):
		return "starting"
	}
	return ""
}

// Summary pings Docker, negotiating the API version, then reads its info
// and container list, and statfs on its root dir. Any error means Docker
// is unreachable or not answering, and the summary is empty.
func (d *Docker) Summary(ctx context.Context) (DockerSummary, error) {
	var s DockerSummary
	var err error
	if s.APIVersion, s.DaemonAPIVersion, err = d.ping(ctx); err != nil {
		return DockerSummary{}, err
	}
	if err := d.get(ctx, s.APIVersion, "/info", nil, &s.Info); err != nil {
		return DockerSummary{}, err
	}
	if err := d.get(ctx, s.APIVersion, "/containers/json", url.Values{"all": {"1"}}, &s.Containers); err != nil {
		return DockerSummary{}, err
	}
	if s.Info.DockerRootDir != "" {
		s.RootDisk, s.RootDiskErr = readDisk(d.statfs, s.Info.DockerRootDir)
	} else {
		s.RootDiskErr = errors.New("docker reports no DockerRootDir")
	}
	return s, nil
}

// Image is one entry of GET /images/json.
type Image struct {
	ID       string `json:"Id"`
	RepoTags []string
	Size     int64
	Created  int64 // Unix seconds
}

// Images lists the images, through the apiVersion a Summary negotiated.
func (d *Docker) Images(ctx context.Context, apiVersion string) ([]Image, error) {
	var images []Image
	if err := d.get(ctx, apiVersion, "/images/json", nil, &images); err != nil {
		return nil, err
	}
	return images, nil
}

// ping calls /_ping and returns the API version to use and the daemon's.
func (d *Docker) ping(ctx context.Context) (use, daemon string, err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := d.do(ctx, "/_ping")
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("docker: ping: %s", resp.Status)
	}
	daemon = resp.Header.Get("Api-Version")
	if _, _, ok := parseAPIVersion(daemon); !ok {
		return "", "", fmt.Errorf("docker: ping reported API version %q", daemon)
	}
	return capAPIVersion(daemon), daemon, nil
}

// capAPIVersion is v, or MaxAPIVersion when v is newer.
func capAPIVersion(v string) string {
	maj, mnr, _ := parseAPIVersion(v)
	capMaj, capMin, _ := parseAPIVersion(MaxAPIVersion)
	if maj > capMaj || maj == capMaj && mnr > capMin {
		return MaxAPIVersion
	}
	return v
}

func parseAPIVersion(v string) (major, minor int, ok bool) {
	a, b, found := strings.Cut(v, ".")
	major, err1 := strconv.Atoi(a)
	minor, err2 := strconv.Atoi(b)
	return major, minor, found && err1 == nil && err2 == nil
}

// get decodes the JSON answer to GET /v<version><path>. It gives up
// after requestTimeout unless ctx has a deadline of its own.
func (d *Docker) get(ctx context.Context, version, path string, query url.Values, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
		defer cancel()
	}
	p := "/v" + version + path
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	resp, err := d.do(ctx, p)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return engineError(path, resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker: %s: %w", path, err)
	}
	return nil
}

func (d *Docker) do(ctx context.Context, pathAndQuery string) (*http.Response, error) {
	// The host is a placeholder: the transport always dials the socket.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+pathAndQuery, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	return resp, nil
}

// engineError is the daemon's {"message": ...} for a failed call.
func engineError(path string, resp *http.Response) error {
	var body struct{ Message string }
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &body) != nil || body.Message == "" {
		body.Message = strings.TrimSpace(string(data))
	}
	return fmt.Errorf("docker: %s: %s: %s", path, resp.Status, body.Message)
}
