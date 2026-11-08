//go:build installtest

package installtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// containers is the PR-gate Substrate: each Node is a privileged container
// running systemd as PID 1, with its own nested dockerd, on the default
// Docker bridge. It shares the runner's kernel, which the installer test
// strategy accepts for the PR gate: the installer's logic doesn't depend on it.
//
// Images are built from images/<variant>.Dockerfile on first use, tagged by
// a hash of the Dockerfiles that make them up, and, when
// INSTALLTEST_IMAGE_CACHE names a directory, saved there as tarballs so CI
// can cache them between runs.
type containers struct {
	gateway  string // the bridge's gateway: the runner, as Nodes see it
	cacheDir string

	mu     sync.Mutex
	images map[string]*imageBuild
}

type imageBuild struct {
	once sync.Once
	tag  string
	err  error
}

func newContainers() (*containers, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("the installer harness needs the docker CLI and a running daemon")
	}
	gw, err := docker(context.Background(), nil, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		return nil, fmt.Errorf("find the docker bridge gateway: %w", err)
	}
	return &containers{
		gateway:  strings.TrimSpace(gw),
		cacheDir: os.Getenv("INSTALLTEST_IMAGE_CACHE"),
		images:   map[string]*imageBuild{},
	}, nil
}

func (c *containers) HostAddr() string { return c.gateway }

func (c *containers) Boot(t *testing.T, d Distro, v Variant) Node {
	t.Helper()
	image, err := c.image(d, v)
	if err != nil {
		t.Fatalf("image %s/%s: %v", d.Name, v, err)
	}
	name := "pantalan-installtest-" + d.Name + "-" + randomHex(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err = docker(ctx, nil, "run", "--detach", "--name", name,
		"--label", "pantalan.installtest=1",
		"--privileged", "--cgroupns=private",
		"--tmpfs", "/run", "--tmpfs", "/run/lock",
		"--hostname", "node-"+strings.ReplaceAll(d.Name, ".", ""),
		image)
	if err != nil {
		t.Fatalf("boot %s: %v", d.Name, err)
	}
	n := &container{name: name}
	t.Cleanup(func() {
		if t.Failed() {
			n.dumpDiagnostics(t)
		}
		docker(context.Background(), nil, "rm", "--force", "--volumes", name)
	})
	// systemd reports "degraded" when any unit failed, which a bare
	// container may well have; the tests care that boot finished.
	// The bus appears a moment after PID 1 starts.
	r := n.Run(t, Cmd{Script: `
		for i in $(seq 100); do
			state=$(systemctl is-system-running --wait 2>/dev/null) && break
			case $state in degraded) break ;; esac
			sleep 0.2
		done
		echo "$state"`, Timeout: 2 * time.Minute})
	if state := strings.TrimSpace(r.Stdout); state != "running" && state != "degraded" {
		t.Fatalf("boot %s: systemd is %q:\n%s", d.Name, state, r.Output())
	}
	addr, err := docker(ctx, nil, "inspect", "--format", "{{.NetworkSettings.IPAddress}}", name)
	if err != nil || strings.TrimSpace(addr) == "" {
		t.Fatalf("boot %s: no container IP: %v", d.Name, err)
	}
	n.addr = strings.TrimSpace(addr)
	return n
}

// image returns the tag of the image for d and v, building (or loading
// from the cache) it and the images it starts from on first use.
func (c *containers) image(d Distro, v Variant) (string, error) {
	chain := []Variant{Bare}
	if v != Bare {
		chain = append(chain, v)
	}
	parent, h := d.Image, sha256.New()
	fmt.Fprintf(h, "%s\x00", d.Image)
	for _, link := range chain {
		dockerfile := filepath.Join("images", string(link)+".Dockerfile")
		content, err := os.ReadFile(dockerfile)
		if err != nil {
			return "", err
		}
		h.Write(content)
		tag := "pantalan-installtest/" + d.Name + "-" + string(link) + ":" + hex.EncodeToString(h.Sum(nil))[:12]
		if err := c.ensure(tag, dockerfile, parent); err != nil {
			return "", err
		}
		parent = tag
	}
	return parent, nil
}

// ensure makes sure tag exists, once per process however many tests ask.
func (c *containers) ensure(tag, dockerfile, base string) error {
	c.mu.Lock()
	b, ok := c.images[tag]
	if !ok {
		b = &imageBuild{tag: tag}
		c.images[tag] = b
	}
	c.mu.Unlock()
	b.once.Do(func() { b.err = c.build(tag, dockerfile, base) })
	return b.err
}

func (c *containers) build(tag, dockerfile, base string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := docker(ctx, nil, "image", "inspect", tag); err == nil {
		return nil
	}
	var tarball string
	if c.cacheDir != "" {
		tarball = filepath.Join(c.cacheDir, strings.NewReplacer("/", "_", ":", "_").Replace(tag)+".tar")
		if _, err := os.Stat(tarball); err == nil {
			if _, err := docker(ctx, nil, "load", "--input", tarball); err == nil {
				return nil
			}
		}
	}
	if _, err := docker(ctx, nil, "build", "--pull=false", "--tag", tag,
		"--build-arg", "BASE="+base, "--file", dockerfile, "images"); err != nil {
		return err
	}
	if tarball != "" {
		if err := os.MkdirAll(c.cacheDir, 0o755); err != nil {
			return err
		}
		if _, err := docker(ctx, nil, "save", "--output", tarball, tag); err != nil {
			return err
		}
	}
	return nil
}

// container is one Node of the containers Substrate.
type container struct {
	name string
	addr string
}

func (n *container) Addr() string { return n.addr }

func (n *container) Run(t *testing.T, c Cmd) Result {
	t.Helper()
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"exec"}
	if c.Stdin != "" {
		args = append(args, "--interactive")
	}
	for _, kv := range c.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, n.name, "sh", "-c", c.Script)
	cmd := exec.CommandContext(ctx, "docker", args...)
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("on the Node, %q timed out after %s:\n%s", c.Script, timeout, r.Output())
	case errors.As(err, &exit):
		r.Code = exit.ExitCode()
	case err != nil:
		t.Fatalf("docker exec: %v", err)
	}
	return r
}

// dumpDiagnostics logs what a failing test most likely needs to see.
func (n *container) dumpDiagnostics(t *testing.T) {
	r := n.Run(t, Cmd{Script: `
		echo "--- systemctl status pantalan"; systemctl status --no-pager pantalan 2>&1
		echo "--- journalctl -u pantalan"; journalctl -u pantalan -n 80 --no-pager 2>&1
		echo "--- /var/log/pantalan-install.log (tail)"; tail -n 80 /var/log/pantalan-install.log 2>&1
	`})
	t.Logf("diagnostics from %s:\n%s", n.name, r.Output())
}

// docker runs the docker CLI and returns its stdout, or an error carrying
// its stderr.
func docker(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
