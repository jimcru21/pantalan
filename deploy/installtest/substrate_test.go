//go:build installtest

package installtest

import (
	"strings"
	"testing"
	"time"
)

// A Substrate is where fresh Nodes come from: privileged systemd containers
// on every PR (container_test.go), cloud-image VMs on the VM tier
// (vm_test.go, INSTALLTEST_SUBSTRATE=vm). Tests only ever talk to a Node
// through this interface, so a path written once runs on either.
type Substrate interface {
	// Boot starts a fresh Node of distro d from the image variant v and
	// removes it when t ends. Every installer path gets its own Node.
	Boot(t *testing.T, d Distro, v Variant) Node
	// HostAddr is the address Nodes reach the machine running the tests on,
	// where the fake release server listens.
	HostAddr() string
}

// A Node is one booted host under test.
type Node interface {
	// Run runs c on the Node as root, with no terminal, and waits for it.
	Run(t *testing.T, c Cmd) Result
	// Addr is the Node's IP address as seen from outside it, from the
	// machine running the tests.
	Addr() string
}

// A Rebooter is a Node that really reboots: a VM, not a container (which
// has no real boot). The reboot paths skip on Nodes that aren't one.
type Rebooter interface {
	// Reboot reboots the Node and returns once it's back up.
	Reboot(t *testing.T)
}

// Cmd is a shell script for Node.Run.
type Cmd struct {
	Script  string        // run by `sh -c`
	Env     []string      // KEY=value pairs added to the environment
	Stdin   string        // empty means no stdin at all
	Timeout time.Duration // zero means two minutes
}

// Result is how a Cmd ended.
type Result struct {
	Code           int
	Stdout, Stderr string
}

// Output is stdout followed by stderr, for assertions that don't care which
// stream a message went to.
func (r Result) Output() string { return r.Stdout + r.Stderr }

// sh runs script on n, failing the test unless it exits 0, and returns its
// trimmed stdout.
func sh(t *testing.T, n Node, script string) string {
	t.Helper()
	r := n.Run(t, Cmd{Script: script})
	if r.Code != 0 {
		t.Fatalf("on the Node, %q exited %d:\n%s", script, r.Code, r.Output())
	}
	return strings.TrimSpace(r.Stdout)
}

// Variant is what a Node's image has installed before the installer runs.
type Variant string

const (
	// Bare is a fresh server: systemd, sshd, curl, iproute2 and sudo, and
	// no Docker or git.
	Bare Variant = "bare"
	// DockerOK is Bare plus docker-ce from Docker's apt repo, enabled.
	DockerOK Variant = "docker-ok"
	// UFW is Bare plus ufw, installed and inactive.
	UFW Variant = "ufw"
)

// Distro is one supported release.
type Distro struct {
	Name  string // as INSTALLTEST_DISTROS spells it, e.g. debian-12
	Image string // the Docker Hub image the container images start from
}

// distros are the releases the installer supports, in the order CI lists them.
var distros = []Distro{
	{Name: "debian-12", Image: "debian:12"},
	{Name: "debian-13", Image: "debian:13"},
	{Name: "ubuntu-24.04", Image: "ubuntu:24.04"},
	{Name: "ubuntu-26.04", Image: "ubuntu:26.04"},
}
